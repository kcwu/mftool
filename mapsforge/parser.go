package mapsforge

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"regexp"
)

const mapsforge_file_magic string = "mapsforge binary OSM"

var (
	re_tilestart = regexp.MustCompile(`###TileStart(\d+),(\d+)###`)
	re_poistart  = regexp.MustCompile(`\*\*\*POIStart\d+\*\*\*`)
	re_waystart  = regexp.MustCompile(`---WayStart\d+---`)
)

var overflow = errors.New("variable int overflow")

type MapsforgeParser struct {
	file_content []byte
	reader       *raw_reader
	data         *MapsforgeData
}

func NewMapsforgeParser(r io.Reader) *MapsforgeParser {
	mp := &MapsforgeParser{}
	var err error
	mp.file_content, err = ioutil.ReadAll(r)
	if err != nil {
		return nil
	}
	mp.reader = newRawReader(mp.file_content)
	return mp
}

func (mp *MapsforgeParser) Parse() error {
	var err error
	mp.data = &MapsforgeData{}
	header := &mp.data.header

	err = mp.ParseHeader(header)
	if err != nil {
		return err
	}

	mp.data.subfiles = make([]SubFile, len(header.zoom_interval))
	for i := 0; i < len(header.zoom_interval); i++ {
		subfile := &mp.data.subfiles[i]
		subfile.zoom_interval = &header.zoom_interval[i]
		pos := subfile.zoom_interval.pos
		size := subfile.zoom_interval.size
		reader := newRawReader(mp.file_content[pos : pos+size])
		err = mp.parseSubFilePartial(reader, header, subfile)
		if err != nil {
			return err
		}
	}
	return nil
}

func (mp *MapsforgeParser) ParseHeader(h *Header) error {
	r := mp.reader

	magic := r.fixedString(20)
	h.header_size = r.uint32()
	h.file_version = r.uint32()
	h.file_size = r.uint64()
	h.creation_date = r.uint64()
	h.min.lat = r.int32()
	h.min.lon = r.int32()
	h.max.lat = r.int32()
	h.max.lon = r.int32()
	h.tile_size = r.uint16()
	h.projection = r.VbeString()

	flags := r.uint8()
	h.has_debug = (flags >> 7 & 1) != 0
	h.has_map_start = (flags >> 6 & 1) != 0
	h.has_start_zoom = (flags >> 5 & 1) != 0
	h.has_language_preference = (flags >> 4 & 1) != 0
	h.has_comment = (flags >> 3 & 1) != 0
	h.has_created_by = (flags >> 2 & 1) != 0

	if h.has_map_start {
		h.start.lat = r.int32()
		h.start.lon = r.int32()
	}
	if h.has_start_zoom {
		h.start_zoom = r.uint8()
	}
	if h.has_language_preference {
		h.language_preference = r.VbeString()
	}
	if h.has_comment {
		h.comment = r.VbeString()
	}
	if h.has_created_by {
		h.created_by = r.VbeString()
	}

	if r.err != nil {
		return r.err
	}
	if h.min.lat > h.max.lat || h.min.lon > h.max.lon {
		return errors.New("min should be <= max")
	}

	// ------------------------------

	num_poi_tags := r.uint16()
	h.poi_tags = make([]string, num_poi_tags)
	for i := uint32(0); i < uint32(num_poi_tags); i++ {
		h.poi_tags[i] = r.VbeString()
	}
	num_way_tags := r.uint16()
	h.way_tags = make([]string, num_way_tags)
	for i := uint32(0); i < uint32(num_way_tags); i++ {
		h.way_tags[i] = r.VbeString()
	}

	if r.err != nil {
		return r.err
	}

	// ------------------------------

	num_zoom_interval := r.uint8()
	h.zoom_interval = make([]ZoomIntervalConfig, num_zoom_interval)
	for i := 0; i < int(num_zoom_interval); i++ {
		cf := &h.zoom_interval[i]
		cf.base_zoom_level = r.uint8()
		cf.min_zoom_level = r.uint8()
		cf.max_zoom_level = r.uint8()
		cf.pos = r.uint64()
		cf.size = r.uint64()
		if r.err != nil {
			return r.err
		}
		if cf.min_zoom_level > cf.max_zoom_level {
			return errors.New(fmt.Sprintf("%v <= %v <= %v",
				cf.min_zoom_level,
				cf.base_zoom_level,
				cf.max_zoom_level))
		}
	}

	// ------------------------------

	if magic != mapsforge_file_magic {
		return errors.New("bad magic")
	}

	return nil
}

func (mp *MapsforgeParser) ParseRest() error {
	for si, sf := range mp.data.subfiles {
		for x := sf.x; x <= sf.X; x++ {
			for y := sf.y; y <= sf.Y; y++ {
				_, err := mp.getTileData(si, x, y)
				if err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (mp *MapsforgeParser) parseSubFilePartial(r *raw_reader, h *Header, sf *SubFile) error {
	zic := sf.zoom_interval

	if h.has_debug {
		signature := r.fixedString(16)
		if signature != "+++IndexStart+++" {
			return errors.New("signature IndexStart mismatch")
		}
	}

	base_zoom := zic.base_zoom_level
	x, Y := h.min.ToXY(base_zoom)
	X, y := h.max.ToXY(base_zoom)
	sf.x, sf.y, sf.X, sf.Y = x, y, X, Y

	if !(x <= X && y <= Y) {
		panic(fmt.Sprintf("tile xy %v<=%v, %v<=%v", x, X, y, Y))
	}

	len_x := X - x + 1
	len_y := Y - y + 1

	sf.tile_indexes = make([]TileIndexEntry, len_x*len_y+1)
	for i := 0; i < len_x*len_y; i++ {
		b0 := r.uint8()
		b1234 := r.uint32()
		sf.tile_indexes[i] = TileIndexEntry{
			is_water: b0&0x80 != 0,
			offset:   uint64(b0&0x7f)<<32 | uint64(b1234),
		}
	}

	// sential
	sf.tile_indexes[len_x*len_y] = TileIndexEntry{offset: zic.size}

	if r.err != nil {
		return r.err
	}

	sf.tile_data = make([]*TileData, len_x*len_y)

	return nil
}

func (mp *MapsforgeParser) getTileData(si, x, y int) (*TileData, error) {
	if !(0 <= si && si < len(mp.data.subfiles)) {
		return nil, errors.New("bad subfile index")
	}
	sf := &mp.data.subfiles[si]

	if !(sf.x <= x && x <= sf.X && sf.y <= y && y <= sf.Y) {
		return nil, nil
	}

	len_x := sf.X - sf.x + 1
	i := (x - sf.x) + len_x*(y-sf.y)
	if sf.tile_data[i] == nil {
		td := &TileData{}

		if sf.tile_indexes[i].offset != sf.tile_indexes[i+1].offset {
			sf_base := sf.zoom_interval.pos
			b := sf_base + sf.tile_indexes[i].offset
			e := sf_base + sf.tile_indexes[i+1].offset
			tdp := newTileDataParser(x, y, mp.file_content[b:e], mp, sf.zoom_interval)
			var err error
			td, err = tdp.parse()

			if err != nil {
				return nil, err
			}
		} else {
			zooms := (sf.zoom_interval.max_zoom_level - sf.zoom_interval.min_zoom_level) + 1
			td.poi_data = make([][]POIData, zooms)
			td.way_data = make([][]WayProperties, zooms)
		}
		sf.tile_data[i] = td
	}
	return sf.tile_data[i], nil
}

func (mp *MapsforgeParser) getBaseZooms() []uint8 {
	var result []uint8
	for _, z := range mp.data.header.zoom_interval {
		result = append(result, z.base_zoom_level)
	}
	return result
}

type Tile struct {
	zoom int
	x, y int
	pois *[]POIData
	ways *[]WayProperties
}

func (mp *MapsforgeParser) getTiles() (result []Tile) {
	for si, sf := range mp.data.subfiles {
		len_x := int(sf.X - sf.x + 1)
		for ti, td := range sf.tile_data {
			x := ti % len_x
			y := ti / len_x
			if td == nil {
				var err error
				td, err = mp.getTileData(si, x, y)
				if err != nil {
					continue
				}
				if td == nil {
					continue
				}
			}
			for zi := range td.poi_data {
				zoom := int(sf.zoom_interval.min_zoom_level) + zi

				result = append(result, Tile{
					zoom, x, y,
					&td.poi_data[zi],
					&td.way_data[zi]})
			}
		}
	}
	return
}

func parseFile(fn string, all bool) (*MapsforgeParser, error) {
	f, err := os.Open(fn)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	p := NewMapsforgeParser(f)
	err = p.Parse()
	if err != nil {
		return p, err
	}
	if all {
		err = p.ParseRest()
	}
	if err != nil {
		return p, err
	}
	return p, nil
}

func CmdParse(args []string) error {
	_, err := parseFile(args[0], true)
	if err != nil {
		return err
	}
	return nil
}
