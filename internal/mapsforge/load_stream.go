package mapsforge

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"strconv"
	"unsafe"
)

// ---- fast zero-copy line reader ----

// lineReader uses bufio.Reader.ReadLine for zero-copy line reading.
// The returned []byte is only valid until the next call.
type lineReader struct {
	r          *bufio.Reader
	pending    []byte
	hasPending bool
}

func newLineReader(r io.Reader) *lineReader {
	return &lineReader{r: bufio.NewReaderSize(r, 4<<20)}
}

// next returns the next line (without trailing '\n').
// The slice is valid until the next call to next() unless pushBack was called.
func (lr *lineReader) next() ([]byte, bool) {
	if lr.hasPending {
		b := lr.pending
		lr.hasPending = false
		return b, true
	}
	line, isPrefix, err := lr.r.ReadLine()
	if err != nil {
		return nil, false
	}
	if isPrefix {
		// Line exceeded buffer — copy and read the rest.
		full := make([]byte, len(line))
		copy(full, line)
		for isPrefix {
			var more []byte
			more, isPrefix, err = lr.r.ReadLine()
			full = append(full, more...)
			if err != nil {
				break
			}
		}
		return full, true
	}
	return line, true
}

// pushBack saves a line to be returned by the next call to next().
// The line is copied so the caller need not keep it alive.
func (lr *lineReader) pushBack(line []byte) {
	lr.pending = append(lr.pending[:0], line...)
	lr.hasPending = true
}

// ---- unsafe zero-copy helpers ----

// bstring creates a string backed by b without copying.
// Only valid while b is live.
func bstring(b []byte) string {
	return unsafe.String(unsafe.SliceData(b), len(b))
}

// ---- fast integer / bool parsers on []byte ----

func parseIntB(b []byte) (int64, error) {
	if len(b) == 0 {
		return 0, fmt.Errorf("empty integer")
	}
	neg := b[0] == '-'
	if neg {
		b = b[1:]
	}
	var v uint64
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid integer char %q", c)
		}
		v = v*10 + uint64(c-'0')
	}
	if neg {
		return -int64(v), nil
	}
	return int64(v), nil
}

func parseUintB(b []byte) (uint64, error) {
	if len(b) == 0 {
		return 0, fmt.Errorf("empty uint")
	}
	var v uint64
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid uint char %q", c)
		}
		v = v*10 + uint64(c-'0')
	}
	return v, nil
}

func parseBoolB(b []byte) bool {
	return len(b) == 4 && b[0] == 't'
}

// ---- pre-parsed dump result ----

// subfileEncoded holds pre-encoded tile bytes indexed by grid key.
type subfileEncoded struct {
	tiles   map[int][]byte
	isWater map[int]bool
}

// streamParseDump opens tomlPath, parses the header and all tiles,
// encoding each tile immediately after parsing (so only []byte is kept in
// memory, not the full TileData object graph).
func streamParseDump(path string) (*Header, []subfileEncoded, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	lr := newLineReader(f)

	// ---- Phase 1: parse header and zoom intervals ----
	hdr, zics, err := parseHeaderAndZooms(lr)
	if err != nil {
		return nil, nil, err
	}

	h := &Header{
		header_size:             hdr.HeaderSize,
		file_version:            hdr.FileVersion,
		file_size:               hdr.FileSize,
		creation_date:           hdr.CreationDate,
		min:                     LatLon{hdr.MinLat, hdr.MinLon},
		max:                     LatLon{hdr.MaxLat, hdr.MaxLon},
		tile_size:               hdr.TileSize,
		projection:              hdr.Projection,
		has_debug:               hdr.HasDebug,
		has_map_start:           hdr.HasMapStart,
		start:                   LatLon{hdr.StartLat, hdr.StartLon},
		has_start_zoom:          hdr.HasStartZoom,
		start_zoom:              hdr.StartZoom,
		has_language_preference: hdr.HasLanguagePreference,
		language_preference:     hdr.LanguagePreference,
		has_comment:             hdr.HasComment,
		comment:                 hdr.Comment,
		has_created_by:          hdr.HasCreatedBy,
		created_by:              hdr.CreatedBy,
		poi_tags:                hdr.PoiTags,
		way_tags:                hdr.WayTags,
	}
	h.zoom_interval = make([]ZoomIntervalConfig, len(zics))
	for i, z := range zics {
		h.zoom_interval[i] = ZoomIntervalConfig{
			base_zoom_level: z.BaseZoom,
			min_zoom_level:  z.MinZoom,
			max_zoom_level:  z.MaxZoom,
		}
	}

	// Build tag lookup maps.
	poiTagMap := make(map[string]int, len(h.poi_tags))
	for i, t := range h.poi_tags {
		poiTagMap[t] = i
	}
	wayTagMap := make(map[string]int, len(h.way_tags))
	for i, t := range h.way_tags {
		wayTagMap[t] = i
	}

	// Compute per-subfile grid origins.
	type gridInfo struct{ x, y, len_x, zooms int }
	grids := make([]gridInfo, len(h.zoom_interval))
	for si, zic := range h.zoom_interval {
		gx, _ := h.min.ToXY(zic.base_zoom_level)
		gX, gy := h.max.ToXY(zic.base_zoom_level)
		grids[si] = gridInfo{
			x:     gx,
			y:     gy,
			len_x: int(gX - gx + 1),
			zooms: int(zic.max_zoom_level-zic.min_zoom_level) + 1,
		}
	}

	// Encoder (no I/O, just encodes to bytes).
	encMW := &MapsforgeWriter{HasDebug: h.has_debug}

	// Allocate per-subfile storage.
	subs := make([]subfileEncoded, len(h.zoom_interval))
	for i := range subs {
		subs[i].tiles = make(map[int][]byte)
		subs[i].isWater = make(map[int]bool)
	}

	// ---- Phase 2: stream-parse tiles, encode immediately ----

	const (
		secNone    = iota
		secTile
		secTilePOI
		secTileWay
	)
	section := secNone

	var curSi, curX, curY int
	var curIsWater bool
	var hasTile bool

	// We keep a TileData only during the parse of a single tile.
	var curTD *TileData
	var curPOI POIData
	var curPOIZiIndex int
	var hasPOI bool
	var curWay WayProperties
	var curWayZiIndex int
	var hasWay bool

	ensureTD := func() {
		if curTD == nil && hasTile {
			g := grids[curSi]
			curTD = &TileData{}
			curTD.poi_data = make([][]POIData, g.zooms)
			curTD.way_data = make([][]WayProperties, g.zooms)
		}
	}

	finalizePOI := func() {
		if hasPOI && curTD != nil {
			zi := curPOIZiIndex
			if zi >= 0 && zi < len(curTD.poi_data) {
				curTD.poi_data[zi] = append(curTD.poi_data[zi], curPOI)
			}
			curPOI = POIData{}
			hasPOI = false
		}
	}

	finalizeWay := func() {
		if hasWay && curTD != nil {
			zi := curWayZiIndex
			if zi >= 0 && zi < len(curTD.way_data) {
				curTD.way_data[zi] = append(curTD.way_data[zi], curWay)
			}
			curWay = WayProperties{}
			hasWay = false
		}
	}

	finalizeTile := func() {
		finalizePOI()
		finalizeWay()
		if !hasTile {
			return
		}
		si := curSi
		g := grids[si]
		gridKey := (curX - g.x) + g.len_x*(curY-g.y)

		if curIsWater {
			subs[si].isWater[gridKey] = true
		}

		if curTD != nil {
			td := curTD
			zooms := g.zooms
			td.tile_header.zoom_table = make([]TileZoomTable, zooms)
			for zi := 0; zi < zooms; zi++ {
				td.tile_header.zoom_table[zi] = TileZoomTable{
					num_pois: uint32(len(td.poi_data[zi])),
					num_ways: uint32(len(td.way_data[zi])),
				}
			}
			td.normalize()
			data, _ := encMW.WriteTileData(td, curX, curY)
			subs[si].tiles[gridKey] = data
			// Discard the TileData immediately; only keep the encoded bytes.
			curTD = nil
		}

		hasTile = false
	}

	// Section-header byte slices for zero-copy comparison.
	bTiles := []byte("[[tiles]]")
	bPois := []byte("[[tiles.pois]]")
	bWays := []byte("[[tiles.ways]]")

	for {
		line, ok := lr.next()
		if !ok {
			break
		}

		// Section headers.
		if bytes.Equal(line, bTiles) {
			finalizeTile()
			hasTile = true
			curSi, curX, curY = 0, 0, 0
			curIsWater = false
			curTD = nil
			section = secTile
			continue
		}
		if bytes.Equal(line, bPois) {
			finalizePOI()
			finalizeWay()
			ensureTD()
			curPOI = POIData{}
			curPOIZiIndex = 0
			hasPOI = true
			section = secTilePOI
			continue
		}
		if bytes.Equal(line, bWays) {
			finalizePOI()
			finalizeWay()
			ensureTD()
			curWay = WayProperties{}
			curWayZiIndex = 0
			hasWay = true
			section = secTileWay
			continue
		}

		if len(line) == 0 || line[0] == '#' {
			continue
		}

		// Find " = " separator.
		eqIdx := bytes.Index(line, []byte(" = "))
		if eqIdx < 0 {
			continue
		}
		key := line[:eqIdx]
		val := line[eqIdx+3:]

		var parseErr error
		switch section {
		case secTile:
			switch bstring(key) {
			case "si":
				v, e := parseIntB(val)
				parseErr = e
				curSi = int(v)
			case "x":
				v, e := parseIntB(val)
				parseErr = e
				curX = int(v)
			case "y":
				v, e := parseIntB(val)
				parseErr = e
				curY = int(v)
			case "is_water":
				curIsWater = parseBoolB(val)
			}

		case secTilePOI:
			switch bstring(key) {
			case "zi_index":
				v, e := parseIntB(val)
				parseErr = e
				curPOIZiIndex = int(v)
			case "lat":
				v, e := parseIntB(val)
				parseErr = e
				curPOI.lat = int32(v)
			case "lon":
				v, e := parseIntB(val)
				parseErr = e
				curPOI.lon = int32(v)
			case "layer":
				v, e := parseIntB(val)
				parseErr = e
				curPOI.layer = int8(v)
			case "tags":
				tags, e := parseTagsB(val, poiTagMap)
				parseErr = e
				curPOI.tag_id = tags
				curPOI.tag_id_raw = tags
			case "name":
				s, e := strconv.Unquote(string(val))
				parseErr = e
				curPOI.has_name = true
				curPOI.name = s
			case "house_number":
				s, e := strconv.Unquote(string(val))
				parseErr = e
				curPOI.has_house_number = true
				curPOI.house_number = s
			case "elevation":
				v, e := parseIntB(val)
				parseErr = e
				curPOI.has_elevation = true
				curPOI.elevation = int32(v)
			}

		case secTileWay:
			switch bstring(key) {
			case "zi_index":
				v, e := parseIntB(val)
				parseErr = e
				curWayZiIndex = int(v)
			case "layer":
				v, e := parseIntB(val)
				parseErr = e
				curWay.layer = int8(v)
			case "sub_tile_bitmap":
				v, e := parseUintB(val)
				parseErr = e
				curWay.sub_tile_bitmap = uint16(v)
			case "encoding":
				curWay.encoding = parseBoolB(val)
			case "tags":
				tags, e := parseTagsB(val, wayTagMap)
				parseErr = e
				curWay.tag_id = tags
				curWay.tag_id_raw = tags
			case "name":
				s, e := strconv.Unquote(string(val))
				parseErr = e
				curWay.has_name = true
				curWay.name = s
			case "house_number":
				s, e := strconv.Unquote(string(val))
				parseErr = e
				curWay.has_house_number = true
				curWay.house_number = s
			case "reference":
				s, e := strconv.Unquote(string(val))
				parseErr = e
				curWay.has_reference = true
				curWay.reference = s
			case "label_lat":
				v, e := parseIntB(val)
				parseErr = e
				curWay.has_label_position = true
				curWay.label_position.lat = int32(v)
			case "label_lon":
				v, e := parseIntB(val)
				parseErr = e
				curWay.label_position.lon = int32(v)
			case "blocks":
				if len(val) == 1 && val[0] == '[' {
					blocks, e := parseBlocksDirect(lr)
					parseErr = e
					curWay.block = blocks
					n := uint32(len(blocks))
					curWay.has_num_way_blocks = n > 1
					curWay.num_way_block = n
				}
			}
		}

		if parseErr != nil {
			return nil, nil, fmt.Errorf("field %q: %w", bstring(key), parseErr)
		}
	}
	finalizeTile()

	return h, subs, nil
}

// parseHeaderAndZooms reads the [header] and [[zoom_intervals]] sections.
// Stops when it sees "[[tiles]]" (pushes that line back).
func parseHeaderAndZooms(lr *lineReader) (*TOMLHeader, []TOMLZoomInterval, error) {
	var hdr TOMLHeader
	var zics []TOMLZoomInterval
	var curZoom TOMLZoomInterval
	inZoom := false

	const (
		secNone         = iota
		secHeader
		secZoomInterval
	)
	section := secNone

	bHeader := []byte("[header]")
	bZoom := []byte("[[zoom_intervals]]")
	bTiles := []byte("[[tiles]]")

	for {
		line, ok := lr.next()
		if !ok {
			break
		}
		if bytes.Equal(line, bTiles) {
			lr.pushBack(line)
			break
		}
		if bytes.Equal(line, bHeader) {
			section = secHeader
			continue
		}
		if bytes.Equal(line, bZoom) {
			if inZoom {
				zics = append(zics, curZoom)
			}
			curZoom = TOMLZoomInterval{}
			inZoom = true
			section = secZoomInterval
			continue
		}

		if len(line) == 0 || line[0] == '#' {
			continue
		}
		eqIdx := bytes.Index(line, []byte(" = "))
		if eqIdx < 0 {
			continue
		}
		key := line[:eqIdx]
		val := line[eqIdx+3:]

		switch section {
		case secHeader:
			if err := parseHeaderFieldB(&hdr, key, val, lr); err != nil {
				return nil, nil, fmt.Errorf("header field %q: %w", bstring(key), err)
			}
		case secZoomInterval:
			if err := parseZoomFieldB(&curZoom, key, val); err != nil {
				return nil, nil, fmt.Errorf("zoom field %q: %w", bstring(key), err)
			}
		}
	}
	if inZoom {
		zics = append(zics, curZoom)
	}
	return &hdr, zics, nil
}

// parseTagsB parses an inline tag array "["tag1", "tag2"]" and converts
// tag strings directly to IDs using tagMap (no intermediate []string).
func parseTagsB(b []byte, tagMap map[string]int) ([]uint32, error) {
	// Trim surrounding whitespace.
	for len(b) > 0 && b[0] == ' ' {
		b = b[1:]
	}
	if bytes.Equal(b, []byte("[]")) {
		return nil, nil
	}
	if len(b) < 2 || b[0] != '[' || b[len(b)-1] != ']' {
		return nil, fmt.Errorf("invalid tag array")
	}
	b = b[1 : len(b)-1]

	var result []uint32
	for {
		// Skip ", ".
		for len(b) > 0 && (b[0] == ' ' || b[0] == ',') {
			b = b[1:]
		}
		if len(b) == 0 {
			break
		}
		if b[0] != '"' {
			return nil, fmt.Errorf("expected '\"'")
		}
		end := 1
		hasEscape := false
		for end < len(b) {
			if b[end] == '\\' {
				hasEscape = true
				end += 2
				continue
			}
			if b[end] == '"' {
				break
			}
			end++
		}
		if end >= len(b) {
			return nil, fmt.Errorf("unclosed tag string")
		}
		quoted := b[:end+1]
		b = b[end+1:]

		var tag string
		if hasEscape {
			var err error
			tag, err = strconv.Unquote(bstring(quoted))
			if err != nil {
				return nil, err
			}
		} else {
			// Fast path: no escapes, strip quotes without allocation using unsafe.
			inner := quoted[1 : len(quoted)-1]
			tag = bstring(inner) // zero-copy, valid for map lookup
		}
		if id, ok := tagMap[tag]; ok {
			result = append(result, uint32(id))
		}
	}
	return result, nil
}

// parseBlocksDirect parses a multi-line blocks structure directly into []WayData.
// The opening "[" has already been consumed (value was "[").
func parseBlocksDirect(lr *lineReader) ([]WayData, error) {
	var blocks []WayData
	var curBlock WayData
	inBlock := false

	bBlockEnd := []byte("],")
	bEnd := []byte("]")
	bBlockStart := []byte("[")

	for {
		line, ok := lr.next()
		if !ok {
			break
		}
		// Trim leading spaces inline.
		trimmed := line
		for len(trimmed) > 0 && trimmed[0] == ' ' {
			trimmed = trimmed[1:]
		}
		if bytes.Equal(trimmed, bEnd) {
			break
		}
		if bytes.Equal(trimmed, bBlockStart) {
			curBlock = WayData{}
			inBlock = true
			continue
		}
		if bytes.Equal(trimmed, bBlockEnd) {
			if inBlock {
				blocks = append(blocks, curBlock)
				inBlock = false
			}
			continue
		}
		if inBlock && len(trimmed) > 0 && trimmed[0] == '[' {
			seg := parseSegmentDirect(bstring(trimmed))
			curBlock.data = append(curBlock.data, seg)
		}
	}
	return blocks, nil
}

// parseSegmentDirect parses one segment line "[[lat, lon], ...],".
// Uses direct byte-by-byte digit parsing to avoid strconv and strings.Index.
func parseSegmentDirect(s string) []LatLon {
	n := len(s)
	// Strip trailing comma.
	if n > 0 && s[n-1] == ',' {
		n--
	}
	// Strip outer [ ].
	if n < 2 || s[0] != '[' || s[n-1] != ']' {
		return nil
	}
	s = s[1 : n-1]

	var nodes []LatLon
	i, slen := 0, len(s)
	for i < slen {
		// Skip ", " between pairs.
		for i < slen && (s[i] == ',' || s[i] == ' ') {
			i++
		}
		if i >= slen || s[i] != '[' {
			break
		}
		i++

		// Parse lat.
		neg := false
		if i < slen && s[i] == '-' {
			neg = true
			i++
		}
		var lat int32
		for i < slen && s[i] >= '0' && s[i] <= '9' {
			lat = lat*10 + int32(s[i]-'0')
			i++
		}
		if neg {
			lat = -lat
		}

		// Skip ", ".
		for i < slen && (s[i] == ',' || s[i] == ' ') {
			i++
		}

		// Parse lon.
		neg = false
		if i < slen && s[i] == '-' {
			neg = true
			i++
		}
		var lon int32
		for i < slen && s[i] >= '0' && s[i] <= '9' {
			lon = lon*10 + int32(s[i]-'0')
			i++
		}
		if neg {
			lon = -lon
		}

		// Skip to ']'.
		for i < slen && s[i] != ']' {
			i++
		}
		if i < slen {
			i++
		}

		nodes = append(nodes, LatLon{lat, lon})
	}
	return nodes
}

// ---- Header/zoom field parsers ([]byte versions) ----

func parseHeaderFieldB(h *TOMLHeader, key, val []byte, lr *lineReader) error {
	switch bstring(key) {
	case "header_size":
		v, err := parseUintB(val)
		if err != nil {
			return err
		}
		h.HeaderSize = uint32(v)
	case "file_version":
		v, err := parseUintB(val)
		if err != nil {
			return err
		}
		h.FileVersion = uint32(v)
	case "file_size":
		v, err := parseUintB(val)
		if err != nil {
			return err
		}
		h.FileSize = v
	case "creation_date":
		v, err := parseUintB(val)
		if err != nil {
			return err
		}
		h.CreationDate = v
	case "min_lat":
		v, err := parseIntB(val)
		if err != nil {
			return err
		}
		h.MinLat = int32(v)
	case "min_lon":
		v, err := parseIntB(val)
		if err != nil {
			return err
		}
		h.MinLon = int32(v)
	case "max_lat":
		v, err := parseIntB(val)
		if err != nil {
			return err
		}
		h.MaxLat = int32(v)
	case "max_lon":
		v, err := parseIntB(val)
		if err != nil {
			return err
		}
		h.MaxLon = int32(v)
	case "tile_size":
		v, err := parseUintB(val)
		if err != nil {
			return err
		}
		h.TileSize = uint16(v)
	case "projection":
		s, err := strconv.Unquote(string(val))
		if err != nil {
			return err
		}
		h.Projection = s
	case "has_debug":
		h.HasDebug = parseBoolB(val)
	case "has_map_start":
		h.HasMapStart = parseBoolB(val)
	case "start_lat":
		v, err := parseIntB(val)
		if err != nil {
			return err
		}
		h.StartLat = int32(v)
	case "start_lon":
		v, err := parseIntB(val)
		if err != nil {
			return err
		}
		h.StartLon = int32(v)
	case "has_start_zoom":
		h.HasStartZoom = parseBoolB(val)
	case "start_zoom":
		v, err := parseUintB(val)
		if err != nil {
			return err
		}
		h.StartZoom = uint8(v)
	case "has_language_preference":
		h.HasLanguagePreference = parseBoolB(val)
	case "language_preference":
		s, err := strconv.Unquote(string(val))
		if err != nil {
			return err
		}
		h.LanguagePreference = s
	case "has_comment":
		h.HasComment = parseBoolB(val)
	case "comment":
		s, err := strconv.Unquote(string(val))
		if err != nil {
			return err
		}
		h.Comment = s
	case "has_created_by":
		h.HasCreatedBy = parseBoolB(val)
	case "created_by":
		s, err := strconv.Unquote(string(val))
		if err != nil {
			return err
		}
		h.CreatedBy = s
	case "poi_tags":
		if len(val) == 1 && val[0] == '[' {
			tags, err := parseMultiLineStringArray(lr)
			if err != nil {
				return err
			}
			h.PoiTags = tags
		}
	case "way_tags":
		if len(val) == 1 && val[0] == '[' {
			tags, err := parseMultiLineStringArray(lr)
			if err != nil {
				return err
			}
			h.WayTags = tags
		}
	}
	return nil
}

func parseZoomFieldB(z *TOMLZoomInterval, key, val []byte) error {
	switch bstring(key) {
	case "base_zoom":
		v, err := parseUintB(val)
		if err != nil {
			return err
		}
		z.BaseZoom = uint8(v)
	case "min_zoom":
		v, err := parseUintB(val)
		if err != nil {
			return err
		}
		z.MinZoom = uint8(v)
	case "max_zoom":
		v, err := parseUintB(val)
		if err != nil {
			return err
		}
		z.MaxZoom = uint8(v)
	case "pos":
		v, err := parseUintB(val)
		if err != nil {
			return err
		}
		z.Pos = v
	case "size":
		v, err := parseUintB(val)
		if err != nil {
			return err
		}
		z.Size = v
	}
	return nil
}

// parseMultiLineStringArray reads lines until a bare "]" line.
func parseMultiLineStringArray(lr *lineReader) ([]string, error) {
	bClose := []byte("]")
	var result []string
	for {
		line, ok := lr.next()
		if !ok {
			break
		}
		trimmed := line
		for len(trimmed) > 0 && trimmed[0] == ' ' {
			trimmed = trimmed[1:]
		}
		if bytes.Equal(trimmed, bClose) {
			break
		}
		if len(trimmed) == 0 {
			continue
		}
		// Strip trailing comma.
		if trimmed[len(trimmed)-1] == ',' {
			trimmed = trimmed[:len(trimmed)-1]
		}
		s, err := strconv.Unquote(string(trimmed))
		if err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, nil
}
