package mapsforge

type TOMLMap struct {
	Header        TOMLHeader         `toml:"header"`
	ZoomIntervals []TOMLZoomInterval `toml:"zoom_intervals"`
	Tiles         []TOMLTile         `toml:"tiles"`
}

type TOMLHeader struct {
	HeaderSize   uint32 `toml:"header_size"`
	FileVersion  uint32 `toml:"file_version"`
	FileSize     uint64 `toml:"file_size"`
	CreationDate uint64 `toml:"creation_date"`
	MinLat       int32  `toml:"min_lat"`
	MinLon       int32  `toml:"min_lon"`
	MaxLat       int32  `toml:"max_lat"`
	MaxLon       int32  `toml:"max_lon"`
	TileSize     uint16 `toml:"tile_size"`
	Projection   string `toml:"projection"`

	HasDebug              bool   `toml:"has_debug"`
	HasMapStart           bool   `toml:"has_map_start"` // Derived if StartLat/Lon present? Or explicit? Dumper output explicit.
	StartLat              int32  `toml:"start_lat,omitempty"`
	StartLon              int32  `toml:"start_lon,omitempty"`
	HasStartZoom          bool   `toml:"has_start_zoom"` // Dumper didn't output this bool explicitly? Dumper: "if h.has_start_zoom { print start_zoom }".
	StartZoom             uint8  `toml:"start_zoom,omitempty"`
	HasLanguagePreference bool   `toml:"has_language_preference"` // Dumper implicit
	LanguagePreference    string `toml:"language_preference,omitempty"`
	HasComment            bool   `toml:"has_comment"`
	Comment               string `toml:"comment,omitempty"`
	HasCreatedBy          bool   `toml:"has_created_by"`
	CreatedBy             string `toml:"created_by,omitempty"`

	PoiTags []string `toml:"poi_tags"`
	WayTags []string `toml:"way_tags"`
}

type TOMLZoomInterval struct {
	BaseZoom uint8  `toml:"base_zoom"`
	MinZoom  uint8  `toml:"min_zoom"`
	MaxZoom  uint8  `toml:"max_zoom"`
	Pos      uint64 `toml:"pos"` // Read but maybe re-calculated on write? Load should probably recalculate offsets.
	Size     uint64 `toml:"size"`
}

type TOMLTile struct {
	ID      string `toml:"id"` // "z/x/y"
	Si      int    `toml:"si"`
	X       int    `toml:"x"`
	Y       int    `toml:"y"`
	IsWater bool   `toml:"is_water"`

	FirstWayOffset uint32 `toml:"first_way_offset"`

	Pois []TOMLPOI `toml:"pois"`
	Ways []TOMLWay `toml:"ways"`
}

type TOMLPOI struct {
	ZiIndex     int      `toml:"zi_index"`
	Lat         int32    `toml:"lat"`
	Lon         int32    `toml:"lon"`
	Layer       int8     `toml:"layer"` // int8 in struct, dumper prints %d
	Tags        []string `toml:"tags"`  // Strings, need to map back to IDs
	Name        string   `toml:"name,omitempty"`
	HouseNumber string   `toml:"house_number,omitempty"`
	Elevation   int32    `toml:"elevation,omitempty"`
}

type TOMLWay struct {
	ZiIndex       int      `toml:"zi_index"`
	Layer         int8     `toml:"layer"`
	SubTileBitmap uint16   `toml:"sub_tile_bitmap"`
	Encoding      bool     `toml:"encoding"`
	Tags          []string `toml:"tags"`
	Name          string   `toml:"name,omitempty"`
	HouseNumber   string   `toml:"house_number,omitempty"`
	Reference     string   `toml:"reference,omitempty"`
	LabelLat      *int32   `toml:"label_lat,omitempty"`
	LabelLon      *int32   `toml:"label_lon,omitempty"`

	Blocks [][][][2]int32 `toml:"blocks"` // List of blocks -> List of segments -> List of [lat, lon]
}
