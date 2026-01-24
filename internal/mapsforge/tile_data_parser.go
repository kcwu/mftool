package mapsforge

import (
	"errors"
	"fmt"
	"strconv"
)

type TileParser struct {
	x, y int

	data   []byte
	parent *MapsforgeParser
	zic    *ZoomIntervalConfig
	parsed *TileData
}

func newTileDataParser(x, y int, data []byte, parent *MapsforgeParser, zic *ZoomIntervalConfig) *TileParser {
	return &TileParser{x, y, data, parent, zic, nil}
}

func (tp *TileParser) file_header() *Header {
	return &tp.parent.data.header
}

func (tp *TileParser) parse() (*TileData, error) {
	if tp.parsed != nil {
		return tp.parsed, nil
	}

	r := newRawReader(tp.data)
	td := &TileData{}
	tp.parsed = td

	if tp.file_header().has_debug {
		sig := r.fixedString(32)
		sm := re_tilestart.FindStringSubmatch(sig)
		if sm == nil || sm[1] != strconv.Itoa(tp.x) || sm[2] != strconv.Itoa(tp.y) {
			return nil, errors.New(
				fmt.Sprintf("TileStart signature mismatch, expect %d,%d, actual: [%s]", tp.x, tp.y, sig))
		}
	}

	zooms := int(tp.zic.max_zoom_level-tp.zic.min_zoom_level) + 1
	td.tile_header.zoom_table = make([]TileZoomTable, zooms)
	for zi := 0; zi < zooms; zi++ {
		td.tile_header.zoom_table[zi] =
			TileZoomTable{
				num_pois: r.VbeU(),
				num_ways: r.VbeU(),
			}
	}
	td.tile_header.first_way_offset = r.VbeU()
	if r.err != nil {
		return nil, r.err
	}

	td.poi_data = make([][]POIData, zooms)
	for zi := 0; zi < zooms; zi++ {
		num_pois := td.tile_header.zoom_table[zi].num_pois
		td.poi_data[zi] = make([]POIData, num_pois)
		for i := uint32(0); i < num_pois; i++ {
			pd := &td.poi_data[zi][i]
			err := tp.parsePOIData(r, pd)
			if err != nil {
				return nil, err
			}
		}
	}

	td.way_data = make([][]WayProperties, zooms)
	for zi := 0; zi < zooms; zi++ {
		num_ways := td.tile_header.zoom_table[zi].num_ways
		td.way_data[zi] = make([]WayProperties, num_ways)
		for i := uint32(0); i < num_ways; i++ {
			wd := &td.way_data[zi][i]
			err := tp.parseWayProperties(r, wd)
			if err != nil {
				return nil, err
			}
		}
	}
	return td, nil
}
func (tp *TileParser) parsePOIData(r *raw_reader, pd *POIData) error {
	if tp.file_header().has_debug {
		sig := r.fixedString(32)
		if !re_poistart.MatchString(sig) {
			return errors.New("POIData signature mismatch")
		}
	}

	pd.LatLon.lat = r.VbeS()
	pd.LatLon.lon = r.VbeS()
	special := r.uint8()
	pd.layer = int8(special>>4) - 5
	num_tag := int(special & 0xf)
	pd.tag_id = make([]uint32, num_tag)
	for ti := 0; ti < num_tag; ti++ {
		pd.tag_id[ti] = r.VbeU()
	}

	flags := r.uint8()
	pd.has_name = (flags >> 7 & 1) != 0
	pd.has_house_number = (flags >> 6 & 1) != 0
	pd.has_elevation = (flags >> 5 & 1) != 0
	if pd.has_name {
		pd.name = r.VbeString()
	}
	if pd.has_house_number {
		pd.house_number = r.VbeString()
	}
	if pd.has_elevation {
		pd.elevation = r.VbeS()
	}
	return r.err
}

func (tp *TileParser) parseWayProperties(r *raw_reader, wp *WayProperties) error {
	if tp.file_header().has_debug {
		sig := r.fixedString(32)
		if !re_waystart.MatchString(sig) {
			return errors.New("WayProperties signature mismatch")
		}
	}

	r.VbeU() // skip way_data_size

	wp.sub_tile_bitmap = r.uint16()

	special := r.uint8()
	wp.layer = int8(special>>4) - 5
	num_tag := int(special & 0xf)
	wp.tag_id = make([]uint32, num_tag)
	for ti := 0; ti < num_tag; ti++ {
		wp.tag_id[ti] = r.VbeU()
	}

	flags := r.uint8()
	wp.has_name = (flags >> 7 & 1) != 0
	wp.has_house_number = (flags >> 6 & 1) != 0
	wp.has_reference = (flags >> 5 & 1) != 0
	wp.has_label_position = (flags >> 4 & 1) != 0
	wp.has_num_way_blocks = (flags >> 3 & 1) != 0
	wp.encoding = (flags >> 2 & 1) != 0
	if wp.has_name {
		wp.name = r.VbeString()
	}
	if wp.has_house_number {
		wp.house_number = r.VbeString()
	}
	if wp.has_reference {
		wp.reference = r.VbeString()
	}
	if wp.has_label_position {
		wp.label_position = LatLon{
			r.VbeS(),
			r.VbeS()}
	}

	if wp.has_num_way_blocks {
		wp.num_way_block = r.VbeU()
	} else {
		wp.num_way_block = 1
	}

	wp.block = make([]WayData, wp.num_way_block)
	for bi := uint32(0); bi < wp.num_way_block; bi++ {
		num_way := r.VbeU()
		wp.block[bi].data = make([][]LatLon, num_way)
		for wi := uint32(0); wi < num_way; wi++ {
			num_node := r.VbeU()
			wp.block[bi].data[wi] = make([]LatLon, num_node)
			for ni := uint32(0); ni < num_node; ni++ {
				wp.block[bi].data[wi][ni] = LatLon{
					r.VbeS(),
					r.VbeS()}
			}
		}
	}

	return r.err
}
