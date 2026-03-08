package mapsforge

import "sync"

type LatLon struct {
	lat int32
	lon int32
}

type Header struct {
	header_size   uint32
	file_version  uint32
	file_size     uint64
	creation_date uint64
	min           LatLon
	max           LatLon
	tile_size     uint16
	projection    string

	has_debug               bool
	has_map_start           bool
	has_start_zoom          bool
	has_language_preference bool
	has_comment             bool
	has_created_by          bool

	start               LatLon
	start_zoom          uint8
	language_preference string
	comment             string
	created_by          string

	poi_tags []string
	way_tags []string

	zoom_interval []ZoomIntervalConfig
}

type ZoomIntervalConfig struct {
	base_zoom_level uint8
	min_zoom_level  uint8
	max_zoom_level  uint8
	pos             uint64
	size            uint64
}

type TileIndexEntry struct {
	IsWater bool
	Offset  uint64
}

type TileZoomTable struct {
	num_pois uint32
	num_ways uint32
}
type TileHeader struct {
	zoom_table       []TileZoomTable
	first_way_offset uint32
}

type TileData struct {
	tile_header TileHeader
	poi_data    [][]POIData
	way_data    [][]WayProperties
}

type POIData struct {
	LatLon
	layer      int8
	tag_id     []uint32
	tag_id_raw []uint32

	has_name         bool
	has_house_number bool
	has_elevation    bool
	name             string
	house_number     string
	elevation        int32
}

type WayProperties struct {
	sub_tile_bitmap uint16
	layer           int8
	tag_id          []uint32
	tag_id_raw      []uint32

	has_name           bool
	has_house_number   bool
	has_reference      bool
	has_label_position bool
	has_num_way_blocks bool
	encoding           bool
	name               string
	house_number       string
	reference          string
	label_position     LatLon
	num_way_block      uint32

	block []WayData

	// encodedBlocks holds pre-encoded block bytes (set by the load path).
	// When non-nil, writeWayProperties uses these directly instead of block.
	encodedBlocks []byte
}

type WayData struct {
	data [][]LatLon
}

type SubFile struct {
	x, y, X, Y    int                 // calculated
	zoom_interval *ZoomIntervalConfig // pointer to header

	tile_indexes []TileIndexEntry
	tile_data    []*TileData
	mu           sync.Mutex
}

type MapsforgeData struct {
	header   Header
	subfiles []SubFile
}
