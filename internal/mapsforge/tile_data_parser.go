package mapsforge

import (
	"errors"
	"fmt"
	"strconv"
)

type TileParser struct {
	x, y int

	data           []byte
	parent         *MapsforgeParser
	zic            *ZoomIntervalConfig
	parsed         *TileData
	skipBlockParse bool // skip decoding coordinate blocks; stores raw bytes in encodedBlocks only
}

func newTileDataParser(x, y int, data []byte, parent *MapsforgeParser, zic *ZoomIntervalConfig) *TileParser {
	return &TileParser{x: x, y: y, data: data, parent: parent, zic: zic}
}

func newTileDataParserLight(x, y int, data []byte, parent *MapsforgeParser, zic *ZoomIntervalConfig) *TileParser {
	return &TileParser{x: x, y: y, data: data, parent: parent, zic: zic, skipBlockParse: true}
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
	pd.tag_id_raw = make([]uint32, num_tag)
	for ti := 0; ti < num_tag; ti++ {
		pd.tag_id[ti] = r.VbeU()
		pd.tag_id_raw[ti] = pd.tag_id[ti]
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

// validateTileBytes checks tile byte stream integrity without allocating result structs.
func validateTileBytes(data []byte, x, y int, h *Header, zic *ZoomIntervalConfig) error {
	r := newRawReader(data)

	if h.has_debug {
		sig := r.fixedString(32)
		sm := re_tilestart.FindStringSubmatch(sig)
		if sm == nil || sm[1] != strconv.Itoa(x) || sm[2] != strconv.Itoa(y) {
			return fmt.Errorf("TileStart signature mismatch, expect %d,%d, actual: [%s]", x, y, sig)
		}
	}

	zooms := int(zic.max_zoom_level-zic.min_zoom_level) + 1
	// Use a fixed-size stack array to avoid heap allocation.
	var zoom_table [256]TileZoomTable
	for zi := 0; zi < zooms; zi++ {
		zoom_table[zi].num_pois = r.VbeU()
		zoom_table[zi].num_ways = r.VbeU()
	}
	r.VbeU() // first_way_offset
	if r.err != nil {
		return r.err
	}

	for zi := 0; zi < zooms; zi++ {
		for i := uint32(0); i < zoom_table[zi].num_pois; i++ {
			if err := validatePOIBytes(r, h); err != nil {
				return err
			}
		}
	}
	for zi := 0; zi < zooms; zi++ {
		for i := uint32(0); i < zoom_table[zi].num_ways; i++ {
			if err := validateWayBytes(r, h); err != nil {
				return err
			}
		}
	}
	return r.err
}

func validatePOIBytes(r *raw_reader, h *Header) error {
	if h.has_debug {
		sig := r.fixedString(32)
		if !re_poistart.MatchString(sig) {
			return errors.New("POIData signature mismatch")
		}
	}
	r.VbeS() // lat
	r.VbeS() // lon
	special := r.uint8()
	num_tag := int(special & 0xf)
	for ti := 0; ti < num_tag; ti++ {
		r.VbeU()
	}
	flags := r.uint8()
	if flags>>7&1 != 0 {
		r.skipVbeString() // name
	}
	if flags>>6&1 != 0 {
		r.skipVbeString() // house_number
	}
	if flags>>5&1 != 0 {
		r.VbeS() // elevation
	}
	return r.err
}

func validateWayBytes(r *raw_reader, h *Header) error {
	if h.has_debug {
		sig := r.fixedString(32)
		if !re_waystart.MatchString(sig) {
			return errors.New("WayProperties signature mismatch")
		}
	}
	way_data_size := r.VbeU()
	start_len := len(r.buf)

	r.uint16() // sub_tile_bitmap
	special := r.uint8()
	num_tag := int(special & 0xf)
	for ti := 0; ti < num_tag; ti++ {
		r.VbeU()
	}
	flags := r.uint8()
	if flags>>7&1 != 0 {
		r.skipVbeString() // name
	}
	if flags>>6&1 != 0 {
		r.skipVbeString() // house_number
	}
	if flags>>5&1 != 0 {
		r.skipVbeString() // reference
	}
	if flags>>4&1 != 0 {
		r.VbeS() // label lat
		r.VbeS() // label lon
	}
	num_way_block := uint32(1)
	if flags>>3&1 != 0 {
		num_way_block = r.VbeU()
	}
	for bi := uint32(0); bi < num_way_block; bi++ {
		num_way := r.VbeU()
		for wi := uint32(0); wi < num_way; wi++ {
			num_node := r.VbeU()
			for ni := uint32(0); ni < num_node; ni++ {
				r.VbeS()
				r.VbeS()
			}
		}
	}
	consumed := start_len - len(r.buf)
	if r.err == nil && uint32(consumed) != way_data_size {
		return fmt.Errorf("way_data_size mismatch: expected %d, consumed %d", way_data_size, consumed)
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

	way_data_size := r.VbeU()
	start_len := len(r.buf)

	wp.sub_tile_bitmap = r.uint16()

	special := r.uint8()
	wp.layer = int8(special>>4) - 5
	num_tag := int(special & 0xf)
	wp.tag_id = make([]uint32, num_tag)
	wp.tag_id_raw = make([]uint32, num_tag)
	for ti := 0; ti < num_tag; ti++ {
		wp.tag_id[ti] = r.VbeU()
		wp.tag_id_raw[ti] = wp.tag_id[ti]
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

	if tp.skipBlockParse {
		// Light parse: skip coordinate decoding — just store raw block bytes.
		// Used by apply path which never needs wp.block for sorting.
		consumed_so_far := start_len - len(r.buf)
		block_len := int(way_data_size) - consumed_so_far
		if block_len < 0 || block_len > len(r.buf) {
			return fmt.Errorf("way block length out of range: %d", block_len)
		}
		wp.encodedBlocks = r.buf[:block_len]
		r.buf = r.buf[block_len:]
		return r.err
	}

	// Full parse: decode coordinates into []WayData (needed for normalize/sort).
	// Capture raw block bytes before parsing for the encodedBlocks fast path.
	block_bytes_start := r.buf
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
	wp.encodedBlocks = block_bytes_start[:len(block_bytes_start)-len(r.buf)]

	consumed := start_len - len(r.buf)
	if uint32(consumed) != way_data_size {
		return fmt.Errorf("way_data_size mismatch: expected %d, consumed %d", way_data_size, consumed)
	}

	return r.err
}
