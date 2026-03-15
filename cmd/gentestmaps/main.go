// gentestmaps generates a suite of Mapsforge binary map files (.map) for use
// as test fixtures. It produces both valid files (covering all major format
// features) and deliberately invalid files (for negative testing).
//
// Output directory: testdata/gen/  (created relative to the working directory)
//
// Usage:
//
//	go run ./cmd/gentestmaps/
//
// The bounding box for all files covers a tiny area in Berlin that maps to a
// single tile at every zoom level used, keeping file sizes minimal.
package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"strings"
)

// ─── bounding box ─────────────────────────────────────────────────────────────
//
// Berlin area: ~52.484°N, 13.379°E  →  ~52.485°N, 13.380°E
// This 1000-microdegree square fits inside one tile at every zoom level used.
const (
	bboxMinLat = int32(52484000)
	bboxMinLon = int32(13379000)
	bboxMaxLat = int32(52485000)
	bboxMaxLon = int32(13380000)
)

// file-level constants
const (
	fileFormatVersion = uint32(3)
	tileSizePx        = uint16(256)
	mapProjection     = "Mercator"
	creationDateMs    = uint64(1700000000000) // 2023-11-14 in ms since epoch
)

// ─── byte buffer ─────────────────────────────────────────────────────────────

// buf is a write buffer with big-endian helpers and in-place patching.
type buf struct{ data []byte }

func (b *buf) u8(v byte)    { b.data = append(b.data, v) }
func (b *buf) u16(v uint16) { b.data = binary.BigEndian.AppendUint16(b.data, v) }
func (b *buf) u32(v uint32) { b.data = binary.BigEndian.AppendUint32(b.data, v) }
func (b *buf) u64(v uint64) { b.data = binary.BigEndian.AppendUint64(b.data, v) }
func (b *buf) i32(v int32)  { b.u32(uint32(v)) }
func (b *buf) raw(p []byte) { b.data = append(b.data, p...) }
func (b *buf) pos() int     { return len(b.data) }

// vbeU encodes a variable-length unsigned integer (VBE-U INT per spec §2).
func (b *buf) vbeU(v uint32) {
	if v < 0x80 {
		b.data = append(b.data, byte(v))
		return
	}
	for {
		bb := byte(v & 0x7f)
		v >>= 7
		if v == 0 {
			b.data = append(b.data, bb)
			return
		}
		b.data = append(b.data, bb|0x80)
	}
}

// vbeS encodes a variable-length signed integer (VBE-S INT per spec §2).
// Sign is stored as magnitude + sign bit in the last byte's bit 6.
func (b *buf) vbeS(v int32) {
	abs := v
	var sign byte
	if v < 0 {
		abs = -v
		sign = 0x40
	}
	u := uint32(abs)
	switch {
	case u < 0x40:
		b.data = append(b.data, byte(u)|sign)
	case u < 0x2000:
		b.data = append(b.data, byte(u&0x7f)|0x80, byte(u>>7)|sign)
	case u < 0x100000:
		b.data = append(b.data, byte(u&0x7f)|0x80, byte((u>>7)&0x7f)|0x80, byte(u>>14)|sign)
	default:
		for {
			if u < 0x40 {
				b.data = append(b.data, byte(u)|sign)
				return
			}
			b.data = append(b.data, byte(u&0x7f)|0x80)
			u >>= 7
		}
	}
}

// str writes a VBE-U length-prefixed UTF-8 string.
func (b *buf) str(s string) {
	b.vbeU(uint32(len(s)))
	b.data = append(b.data, s...)
}

// fixedStr writes s padded (or truncated) to exactly n bytes with null padding.
func (b *buf) fixedStr(s string, n int) {
	if len(s) >= n {
		b.data = append(b.data, []byte(s)[:n]...)
	} else {
		b.data = append(b.data, []byte(s)...)
		for i := len(s); i < n; i++ {
			b.data = append(b.data, 0)
		}
	}
}

// patchU32 / patchU64 overwrite a previously-written placeholder in place.
func (b *buf) patchU32(pos int, v uint32) { binary.BigEndian.PutUint32(b.data[pos:], v) }
func (b *buf) patchU64(pos int, v uint64) { binary.BigEndian.PutUint64(b.data[pos:], v) }

// ─── tile coordinate helper ───────────────────────────────────────────────────

// toXY converts (lat, lon) in microdegrees to tile (x, y) at the given zoom.
// Matches the formula in internal/mapsforge/basic_func.go exactly.
func toXY(latMicro, lonMicro int32, zoom uint8) (int, int) {
	lat := float64(latMicro) / 1e6
	lon := float64(lonMicro) / 1e6
	n := math.Pow(2, float64(zoom))
	x := int((lon + 180.0) / 360.0 * n)
	y := int((1.0 - math.Log(math.Tan(lat/180*math.Pi)+(1/math.Cos(lat/180*math.Pi)))/math.Pi) / 2.0 * n)
	return x, y
}

// ─── POI / Way spec structs ───────────────────────────────────────────────────

type poiSpec struct {
	LatDiff, LonDiff int32
	Layer            int8
	Tags             []uint32
	Name             string
	HouseNumber      string
	HasElevation     bool
	Elevation        int32
}

func buildPOIBytes(ps poiSpec, debug bool, index int) []byte {
	b := &buf{}
	if debug {
		b.fixedStr(fmt.Sprintf("***POIStart%d***", index), 32)
	}
	b.vbeS(ps.LatDiff)
	b.vbeS(ps.LonDiff)
	layer := int(ps.Layer) + 5
	b.u8(byte(layer<<4) | byte(len(ps.Tags)&0xf))
	for _, t := range ps.Tags {
		b.vbeU(t)
	}
	var flags byte
	if ps.Name != "" {
		flags |= 0x80
	}
	if ps.HouseNumber != "" {
		flags |= 0x40
	}
	if ps.HasElevation {
		flags |= 0x20
	}
	b.u8(flags)
	if ps.Name != "" {
		b.str(ps.Name)
	}
	if ps.HouseNumber != "" {
		b.str(ps.HouseNumber)
	}
	if ps.HasElevation {
		b.vbeS(ps.Elevation)
	}
	return b.data
}

type waySpec struct {
	SubTileBitmap      uint16
	Layer              int8
	Tags               []uint32
	Name               string
	HouseNumber        string
	Reference          string
	HasLabelPos        bool
	LabelLat, LabelLon int32
	DoubleDelta        bool
	ExplicitNumBlocks  bool
	// Blocks[i] = way-data-block i; Blocks[i][j] = coord-block j; Blocks[i][j][k] = [lat,lon] diff
	Blocks [][][2]int32
}

func buildWayBytes(ws waySpec, debug bool, index int) []byte {
	inner := &buf{}
	inner.u16(ws.SubTileBitmap)
	layer := int(ws.Layer) + 5
	inner.u8(byte(layer<<4) | byte(len(ws.Tags)&0xf))
	for _, t := range ws.Tags {
		inner.vbeU(t)
	}

	var flags byte
	if ws.Name != "" {
		flags |= 0x80
	}
	if ws.HouseNumber != "" {
		flags |= 0x40
	}
	if ws.Reference != "" {
		flags |= 0x20
	}
	if ws.HasLabelPos {
		flags |= 0x10
	}
	numBlocks := len(ws.Blocks)
	if numBlocks == 0 {
		numBlocks = 1
	}
	writeNumBlocks := ws.ExplicitNumBlocks || numBlocks > 1
	if writeNumBlocks {
		flags |= 0x08
	}
	if ws.DoubleDelta {
		flags |= 0x04
	}
	inner.u8(flags)

	if ws.Name != "" {
		inner.str(ws.Name)
	}
	if ws.HouseNumber != "" {
		inner.str(ws.HouseNumber)
	}
	if ws.Reference != "" {
		inner.str(ws.Reference)
	}
	if ws.HasLabelPos {
		inner.vbeS(ws.LabelLat)
		inner.vbeS(ws.LabelLon)
	}
	if writeNumBlocks {
		inner.vbeU(uint32(numBlocks))
	}

	for _, block := range ws.Blocks {
		// Each element of ws.Blocks is one way-data-block containing exactly
		// one coordinate-block ([][2]int32 = slice of nodes).
		inner.vbeU(1)                  // num_coord_blocks = 1 per block
		inner.vbeU(uint32(len(block))) // num_nodes
		for _, node := range block {
			inner.vbeS(node[0])
			inner.vbeS(node[1])
		}
	}
	// If no blocks specified, write a default single block with 0 nodes
	if len(ws.Blocks) == 0 {
		inner.vbeU(1) // 1 coord block
		inner.vbeU(0) // 0 nodes
	}

	b := &buf{}
	if debug {
		b.fixedStr(fmt.Sprintf("---WayStart%d---", index), 32)
	}
	b.vbeU(uint32(len(inner.data)))
	b.raw(inner.data)
	return b.data
}

// ─── tile body builders ───────────────────────────────────────────────────────

// emptyTileBody builds a tile with zero POIs and zero ways at every zoom row.
// Used for tiles that are present in the index but carry no features.
func emptyTileBody(minZoom, maxZoom uint8, debug bool, tx, ty int) []byte {
	b := &buf{}
	if debug {
		b.fixedStr(fmt.Sprintf("###TileStart%d,%d###", tx, ty), 32)
	}
	zooms := int(maxZoom-minZoom) + 1
	for range zooms {
		b.vbeU(0) // num_pois
		b.vbeU(0) // num_ways
	}
	b.vbeU(0) // first_way_offset
	return b.data
}

// poiTileBody builds a tile with a single POI (name + elevation) at the first
// zoom row. poiTags contains the tag IDs to include.
func poiTileBody(minZoom, maxZoom uint8, debug bool, tx, ty int, poiTags []uint32) []byte {
	b := &buf{}
	if debug {
		b.fixedStr(fmt.Sprintf("###TileStart%d,%d###", tx, ty), 32)
	}
	zooms := int(maxZoom-minZoom) + 1
	b.vbeU(1) // 1 POI at the first zoom row
	b.vbeU(0) // 0 ways
	for zi := 1; zi < zooms; zi++ {
		b.vbeU(0)
		b.vbeU(0)
	}

	poi := &buf{}
	if debug {
		poi.fixedStr("***POIStart0***", 32)
	}
	poi.vbeS(0) // lat_diff: at tile top-left corner
	poi.vbeS(0) // lon_diff
	// special byte: (layer+5)<<4 | num_tags.  layer=0 → stored value 5.
	poi.u8(byte(5<<4) | byte(len(poiTags)&0xf))
	for _, t := range poiTags {
		poi.vbeU(t)
	}
	poi.u8(0x80 | 0x20) // flags: has_name | has_elevation
	poi.str("Test POI")
	poi.vbeS(100) // elevation = 100 m

	b.vbeU(uint32(len(poi.data))) // first_way_offset = size of all POI data
	b.raw(poi.data)
	// no ways
	return b.data
}

// wayTileBody builds a tile with a single named way (3-node triangle,
// single-delta encoding) at the first zoom row.
func wayTileBody(minZoom, maxZoom uint8, debug bool, tx, ty int, wayTags []uint32) []byte {
	b := &buf{}
	if debug {
		b.fixedStr(fmt.Sprintf("###TileStart%d,%d###", tx, ty), 32)
	}
	zooms := int(maxZoom-minZoom) + 1
	b.vbeU(0) // 0 POIs
	b.vbeU(1) // 1 way at the first zoom row
	for zi := 1; zi < zooms; zi++ {
		b.vbeU(0)
		b.vbeU(0)
	}
	b.vbeU(0) // first_way_offset = 0 (no POIs)

	// Build way_data (everything counted by way_data_size).
	inner := &buf{}
	inner.u16(0xffff)                             // sub_tile_bitmap: set for all 16 sub-tiles
	inner.u8(byte(5<<4) | byte(len(wayTags)&0xf)) // layer=0, num_tags
	for _, t := range wayTags {
		inner.vbeU(t)
	}
	inner.u8(0x80) // flags: has_name only; 1 block implicit (no has_num_way_blocks); single-delta
	inner.str("Test Way")
	// Way data: 1 coordinate block, 3 nodes forming a triangle.
	// Single-delta: first node is diff from tile top-left; subsequent nodes are diffs from prev.
	inner.vbeU(1)    // number of coordinate blocks
	inner.vbeU(3)    // number of nodes in block 0
	inner.vbeS(0)    // node 0 lat_diff (from tile top-left)
	inner.vbeS(0)    // node 0 lon_diff
	inner.vbeS(1000) // node 1 lat_diff (from node 0)
	inner.vbeS(0)    // node 1 lon_diff
	inner.vbeS(-500) // node 2 lat_diff (from node 1)
	inner.vbeS(500)  // node 2 lon_diff

	if debug {
		b.fixedStr("---WayStart0---", 32)
	}
	b.vbeU(uint32(len(inner.data))) // way_data_size
	b.raw(inner.data)
	return b.data
}

// poiAndWayTileBody builds a tile with a rich POI (name, house number,
// elevation) and a rich way (name, reference, label position, 4-node quad).
func poiAndWayTileBody(minZoom, maxZoom uint8, debug bool, tx, ty int, poiTags, wayTags []uint32) []byte {
	b := &buf{}
	if debug {
		b.fixedStr(fmt.Sprintf("###TileStart%d,%d###", tx, ty), 32)
	}
	zooms := int(maxZoom-minZoom) + 1
	b.vbeU(1) // 1 POI
	b.vbeU(1) // 1 way
	for zi := 1; zi < zooms; zi++ {
		b.vbeU(0)
		b.vbeU(0)
	}

	poi := &buf{}
	if debug {
		poi.fixedStr("***POIStart0***", 32)
	}
	poi.vbeS(500) // lat_diff
	poi.vbeS(500) // lon_diff
	// layer=1 → stored value 6.
	poi.u8(byte(6<<4) | byte(len(poiTags)&0xf))
	for _, t := range poiTags {
		poi.vbeU(t)
	}
	poi.u8(0x80 | 0x40 | 0x20) // has_name | has_house_number | has_elevation
	poi.str("Café Roma")
	poi.str("12A")
	poi.vbeS(45) // elevation = 45 m

	b.vbeU(uint32(len(poi.data))) // first_way_offset
	b.raw(poi.data)

	inner := &buf{}
	inner.u16(0x00ff)                             // sub_tile_bitmap
	inner.u8(byte(5<<4) | byte(len(wayTags)&0xf)) // layer=0, num_tags
	for _, t := range wayTags {
		inner.vbeU(t)
	}
	// flags: has_name | has_reference | has_label_position; 1 block implicit; single-delta
	inner.u8(0x80 | 0x20 | 0x10)
	inner.str("Main Street")
	inner.str("B42")
	inner.vbeS(200) // label_position lat_diff
	inner.vbeS(300) // label_position lon_diff
	// 1 coordinate block, 4 nodes (open quadrilateral).
	inner.vbeU(1)    // number of coordinate blocks
	inner.vbeU(4)    // number of nodes
	inner.vbeS(0)    // node 0 lat_diff
	inner.vbeS(0)    // node 0 lon_diff
	inner.vbeS(2000) // node 1
	inner.vbeS(0)
	inner.vbeS(0) // node 2
	inner.vbeS(2000)
	inner.vbeS(-2000) // node 3
	inner.vbeS(0)

	if debug {
		b.fixedStr("---WayStart0---", 32)
	}
	b.vbeU(uint32(len(inner.data)))
	b.raw(inner.data)
	return b.data
}

// buildTileBody builds a tile body from per-zoom-row POI/way specs.
// poisByZoom[zi] and waysByZoom[zi] are the POIs/ways for zoom row zi.
func buildTileBody(minZoom, maxZoom uint8, debug bool, tx, ty int,
	poisByZoom [][]poiSpec, waysByZoom [][]waySpec) []byte {
	zooms := int(maxZoom-minZoom) + 1
	b := &buf{}
	if debug {
		b.fixedStr(fmt.Sprintf("###TileStart%d,%d###", tx, ty), 32)
	}

	// Zoom table
	for zi := 0; zi < zooms; zi++ {
		numPOIs := 0
		if zi < len(poisByZoom) {
			numPOIs = len(poisByZoom[zi])
		}
		numWays := 0
		if zi < len(waysByZoom) {
			numWays = len(waysByZoom[zi])
		}
		b.vbeU(uint32(numPOIs))
		b.vbeU(uint32(numWays))
	}

	// Build all POI data
	poiData := &buf{}
	for zi := 0; zi < zooms; zi++ {
		if zi < len(poisByZoom) {
			for i, ps := range poisByZoom[zi] {
				poiData.raw(buildPOIBytes(ps, debug, i))
			}
		}
	}
	b.vbeU(uint32(len(poiData.data))) // first_way_offset
	b.raw(poiData.data)

	// Build all way data
	for zi := 0; zi < zooms; zi++ {
		if zi < len(waysByZoom) {
			for i, ws := range waysByZoom[zi] {
				b.raw(buildWayBytes(ws, debug, i))
			}
		}
	}

	return b.data
}

// ─── file assembler ───────────────────────────────────────────────────────────

// zicSpec describes one zoom interval in the file header.
type zicSpec struct{ base, min, max uint8 }

// headerSpec holds all parameters needed to write a file header.
type headerSpec struct {
	minLat, minLon, maxLat, maxLon int32

	hasDebug     bool
	hasMapStart  bool
	startLat     int32
	startLon     int32
	hasStartZoom bool
	startZoom    uint8
	hasLanguage  bool
	language     string
	hasComment   bool
	comment      string
	hasCreatedBy bool
	createdBy    string

	tileSize     uint16 // 0 = use default 256
	fileVersion  uint32 // 0 = use default 3
	projection   string // "" = use default "Mercator"
	creationDate uint64 // 0 = use default creationDateMs
	extraFlags   byte   // OR'd into the flags byte (for reserved bits tests)

	poiTags []string
	wayTags []string

	zoomIntervals []zicSpec
}

// tileFn provides the body bytes and water flag for tile (tx, ty) in subfile si.
// Return (nil, false) to omit the tile body entirely (tile index entry points
// to the same offset as the next tile, signalling an absent tile).
type tileFn func(si, tx, ty int) (data []byte, isWater bool)

// buildFile assembles a complete .map file in memory.
//
// Layout (per spec §3):
//
//	file header
//	for each zoom interval:
//	    tile index header  (16 B debug signature, optional)
//	    tile index entries (5 B each)
//	    tile data          (one body per non-empty tile)
func buildFile(hs headerSpec, fn tileFn) []byte {
	b := &buf{}

	tileSize := hs.tileSize
	if tileSize == 0 {
		tileSize = tileSizePx
	}
	fVersion := hs.fileVersion
	if fVersion == 0 {
		fVersion = fileFormatVersion
	}
	proj := hs.projection
	if proj == "" && hs.projection == "" {
		proj = mapProjection
	}
	cDate := hs.creationDate
	if cDate == 0 {
		cDate = creationDateMs
	}

	// ── file header ──────────────────────────────────────────────────────
	b.fixedStr("mapsforge binary OSM", 20)
	headerSizePos := b.pos()
	b.u32(0) // header_size placeholder; patched after all header fields are written
	headerDataStart := b.pos()

	b.u32(fVersion)
	fileSizePos := b.pos()
	b.u64(0) // file_size placeholder; patched at the very end
	b.u64(cDate)
	b.i32(hs.minLat)
	b.i32(hs.minLon)
	b.i32(hs.maxLat)
	b.i32(hs.maxLon)
	b.u16(tileSize)
	b.str(proj)

	var flags byte
	if hs.hasDebug {
		flags |= 0x80
	}
	if hs.hasMapStart {
		flags |= 0x40
	}
	if hs.hasStartZoom {
		flags |= 0x20
	}
	if hs.hasLanguage {
		flags |= 0x10
	}
	if hs.hasComment {
		flags |= 0x08
	}
	if hs.hasCreatedBy {
		flags |= 0x04
	}
	flags |= hs.extraFlags
	b.u8(flags)

	if hs.hasMapStart {
		b.i32(hs.startLat)
		b.i32(hs.startLon)
	}
	if hs.hasStartZoom {
		b.u8(hs.startZoom)
	}
	if hs.hasLanguage {
		b.str(hs.language)
	}
	if hs.hasComment {
		b.str(hs.comment)
	}
	if hs.hasCreatedBy {
		b.str(hs.createdBy)
	}

	b.u16(uint16(len(hs.poiTags)))
	for _, t := range hs.poiTags {
		b.str(t)
	}
	b.u16(uint16(len(hs.wayTags)))
	for _, t := range hs.wayTags {
		b.str(t)
	}

	b.u8(uint8(len(hs.zoomIntervals)))

	// One config entry per zoom interval; pos and size are placeholders.
	zicPosOff := make([]int, len(hs.zoomIntervals))
	zicSzOff := make([]int, len(hs.zoomIntervals))
	for i, zic := range hs.zoomIntervals {
		b.u8(zic.base)
		b.u8(zic.min)
		b.u8(zic.max)
		zicPosOff[i] = b.pos()
		b.u64(0)
		zicSzOff[i] = b.pos()
		b.u64(0)
	}

	// Patch header_size: bytes from after magic+header_size field to end of header.
	b.patchU32(headerSizePos, uint32(b.pos()-headerDataStart))

	// ── sub-files ────────────────────────────────────────────────────────
	for si, zic := range hs.zoomIntervals {
		subStart := b.pos()
		b.patchU64(zicPosOff[si], uint64(subStart))

		if hs.hasDebug {
			b.fixedStr("+++IndexStart+++", 16)
		}

		// The tile grid is determined by mapping the bounding box corners to
		// tile coordinates at the base zoom level.
		// min.lat (south) → larger y;  max.lat (north) → smaller y.
		x0, yMax := toXY(hs.minLat, hs.minLon, zic.base)
		xMax, y0 := toXY(hs.maxLat, hs.maxLon, zic.base)
		lenX := xMax - x0 + 1
		lenY := yMax - y0 + 1
		numTiles := lenX * lenY

		// Write placeholder tile index (5 bytes × numTiles).
		indexStart := b.pos()
		for range numTiles {
			b.u8(0)
			b.u32(0)
		}

		tileDataStart := b.pos()
		offsets := make([]uint64, numTiles) // relative to subStart
		var written uint64

		// Tiles are stored row-wise (y outer), column-wise (x inner).
		for ty := y0; ty <= yMax; ty++ {
			for tx := x0; tx <= xMax; tx++ {
				idx := (tx - x0) + lenX*(ty-y0)
				data, isWater := fn(si, tx, ty)

				off := uint64(tileDataStart-subStart) + written
				if isWater {
					off |= uint64(1) << 39 // water flag in the top bit of the 5-byte field
				}
				offsets[idx] = off

				if data != nil {
					b.raw(data)
					written += uint64(len(data))
				}
				// nil data → empty tile: next tile will have the same offset,
				// which the parser treats as "absent".
			}
		}

		b.patchU64(zicSzOff[si], uint64(b.pos()-subStart))

		// Patch the tile index with the real offsets.
		for i, off := range offsets {
			p := indexStart + i*5
			b.data[p] = byte(off >> 32)
			binary.BigEndian.PutUint32(b.data[p+1:], uint32(off))
		}
	}

	b.patchU64(fileSizePos, uint64(b.pos()))
	return b.data
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func defaultHS(zics []zicSpec, poiTags, wayTags []string) headerSpec {
	return headerSpec{
		minLat: bboxMinLat, minLon: bboxMinLon,
		maxLat: bboxMaxLat, maxLon: bboxMaxLon,
		poiTags:       poiTags,
		wayTags:       wayTags,
		zoomIntervals: zics,
	}
}

// clone returns a deep copy of a byte slice.
func clone(d []byte) []byte {
	c := make([]byte, len(d))
	copy(c, d)
	return c
}

var total int

func write(dir, name string, data []byte) {
	path := dir + "/" + name
	if err := os.WriteFile(path, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "error writing %s: %v\n", path, err)
		os.Exit(1)
	}
	fmt.Printf("  %-44s  %d bytes\n", name, len(data))
	total++
}

func mustMkdir(path string) {
	if err := os.MkdirAll(path, 0755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// ─── zoom interval presets ────────────────────────────────────────────────────

var (
	singleZoom14 = []zicSpec{{base: 14, min: 14, max: 14}}
	multiZoom    = []zicSpec{
		{base: 7, min: 0, max: 7},
		{base: 12, min: 8, max: 12},
		{base: 14, min: 13, max: 21},
	}
)

// noTiles is a tileFn that always returns absent tiles.
var noTiles tileFn = func(si, tx, ty int) ([]byte, bool) { return nil, false }

// simplePOI returns a single default poiSpec for quick usage.
func simplePOI() poiSpec {
	return poiSpec{LatDiff: 0, LonDiff: 0, Layer: 0, Tags: []uint32{0}, Name: "Test"}
}

// simpleWay returns a single default waySpec (triangle) for quick usage.
func simpleWay() waySpec {
	return waySpec{
		SubTileBitmap: 0xffff,
		Layer:         0,
		Tags:          []uint32{0},
		Name:          "Road",
		Blocks: [][][2]int32{
			{{0, 0}, {1000, 0}, {500, 500}},
		},
	}
}

// makeNTags returns n tags strings "tag0=val0" ... "tagN-1=valN-1".
func makeNTags(n int) []string {
	tags := make([]string, n)
	for i := range tags {
		tags[i] = fmt.Sprintf("tag%d=val%d", i, i)
	}
	return tags
}

// makeNTagIDs returns tag IDs 0..n-1.
func makeNTagIDs(n int) []uint32 {
	ids := make([]uint32, n)
	for i := range ids {
		ids[i] = uint32(i)
	}
	return ids
}

// singleZoom returns a single-zoom zicSpec slice.
func singleZoom(base uint8) []zicSpec {
	return []zicSpec{{base: base, min: base, max: base}}
}

// layerStr converts a layer value (-5..+5) to a filename-safe string.
// Negative values use prefix "m", non-negative use prefix "p".
func layerStr(l int8) string {
	if l < 0 {
		return fmt.Sprintf("m%d", -int(l))
	}
	return fmt.Sprintf("p%d", int(l))
}

// ─── main ─────────────────────────────────────────────────────────────────────

func main() {
	dir := "testdata/gen"
	mustMkdir(dir)

	fmt.Println("=== Valid map files (testdata/) ===")

	// 1. minimal.map
	write(dir, "minimal.map",
		buildFile(defaultHS(singleZoom14, nil, nil), noTiles))

	// 2. with_options.map
	{
		hs := defaultHS(singleZoom14, nil, nil)
		hs.hasMapStart = true
		hs.startLat = bboxMinLat
		hs.startLon = bboxMinLon
		hs.hasStartZoom = true
		hs.startZoom = 14
		hs.hasLanguage = true
		hs.language = "en"
		hs.hasComment = true
		hs.comment = "Test map with all optional header fields"
		hs.hasCreatedBy = true
		hs.createdBy = "gentestmaps"
		write(dir, "with_options.map",
			buildFile(hs, func(si, tx, ty int) ([]byte, bool) { return nil, false }))
	}

	// 3. with_poi.map
	write(dir, "with_poi.map",
		buildFile(
			defaultHS(singleZoom14,
				[]string{"amenity=restaurant", "tourism=hotel"}, nil),
			func(si, tx, ty int) ([]byte, bool) {
				zic := singleZoom14[si]
				return poiTileBody(zic.min, zic.max, false, tx, ty, []uint32{0}), false
			}))

	// 4. with_way.map
	write(dir, "with_way.map",
		buildFile(
			defaultHS(singleZoom14,
				nil, []string{"highway=residential", "landuse=forest"}),
			func(si, tx, ty int) ([]byte, bool) {
				zic := singleZoom14[si]
				return wayTileBody(zic.min, zic.max, false, tx, ty, []uint32{0}), false
			}))

	// 5. with_debug.map
	{
		hs := defaultHS(singleZoom14, []string{"amenity=pub"}, []string{"highway=path"})
		hs.hasDebug = true
		write(dir, "with_debug.map",
			buildFile(hs, func(si, tx, ty int) ([]byte, bool) {
				zic := singleZoom14[si]
				return poiAndWayTileBody(zic.min, zic.max, true, tx, ty,
					[]uint32{0}, []uint32{0}), false
			}))
	}

	// 6. water_tile.map
	write(dir, "water_tile.map",
		buildFile(defaultHS(singleZoom14, nil, nil),
			func(si, tx, ty int) ([]byte, bool) { return nil, true }))

	// 7. multi_zoom.map
	write(dir, "multi_zoom.map",
		buildFile(
			defaultHS(multiZoom,
				[]string{"place=city"}, []string{"boundary=administrative"}),
			func(si, tx, ty int) ([]byte, bool) { return nil, false }))

	// 8. poi_and_way.map
	write(dir, "poi_and_way.map",
		buildFile(
			defaultHS(singleZoom14,
				[]string{"amenity=restaurant", "tourism=hotel", "shop=bakery"},
				[]string{"highway=residential", "landuse=forest", "waterway=river"}),
			func(si, tx, ty int) ([]byte, bool) {
				zic := singleZoom14[si]
				return poiAndWayTileBody(zic.min, zic.max, false, tx, ty,
					[]uint32{0, 2}, []uint32{0, 1}), false
			}))

	fmt.Println("\n=== Invalid map files (testdata/) ===")

	// 9. bad_magic.map.bad
	{
		d := clone(buildFile(defaultHS(singleZoom14, nil, nil), noTiles))
		copy(d[0:4], []byte{0xDE, 0xAD, 0xBE, 0xEF})
		write(dir, "bad_magic.map.bad", d)
	}

	// 10. bad_header_size.map.bad
	{
		d := clone(buildFile(defaultHS(singleZoom14, nil, nil), noTiles))
		v := binary.BigEndian.Uint32(d[20:24])
		binary.BigEndian.PutUint32(d[20:24], v+1)
		write(dir, "bad_header_size.map.bad", d)
	}

	// 11. bad_bbox.map.bad
	{
		d := clone(buildFile(defaultHS(singleZoom14, nil, nil), noTiles))
		var tmp [4]byte
		copy(tmp[:], d[44:48])
		copy(d[44:48], d[52:56])
		copy(d[52:56], tmp[:])
		write(dir, "bad_bbox.map.bad", d)
	}

	// 12. truncated.map.bad
	{
		d := buildFile(defaultHS(singleZoom14, nil, nil), noTiles)
		write(dir, "truncated.map.bad", d[:50])
	}

	// 13. bad_way_data_size.map.bad
	{
		d := clone(buildFile(
			defaultHS(singleZoom14, nil, []string{"highway=residential"}),
			func(si, tx, ty int) ([]byte, bool) {
				zic := singleZoom14[si]
				return wayTileBody(zic.min, zic.max, false, tx, ty, []uint32{0}), false
			}))
		const wayDataSizeOffset = 124
		d[wayDataSizeOffset]++
		write(dir, "bad_way_data_size.map.bad", d)
	}

	// 14. bad_tile_sig.map.bad
	{
		hs := defaultHS(singleZoom14, nil, nil)
		hs.hasDebug = true
		d := clone(buildFile(hs, func(si, tx, ty int) ([]byte, bool) {
			zic := singleZoom14[si]
			return emptyTileBody(zic.min, zic.max, true, tx, ty), false
		}))
		const sigStart = 117
		for i := range 32 {
			d[sigStart+i] = 0
		}
		copy(d[sigStart:], "###TileStart0,0###")
		write(dir, "bad_tile_sig.map.bad", d)
	}

	// 15. bad_zoom_interval.map.bad
	{
		d := clone(buildFile(defaultHS(singleZoom14, nil, nil), noTiles))
		d[78] = 17
		d[79] = 10
		write(dir, "bad_zoom_interval.map.bad", d)
	}

	// ─── header/ ─────────────────────────────────────────────────────────────
	fmt.Println("\n=== header/ ===")
	hdir := dir + "/header"
	mustMkdir(hdir)

	// tile_size variations
	for _, ts := range []uint16{64, 128, 256, 512, 1024} {
		hs := defaultHS(singleZoom14, nil, nil)
		hs.tileSize = ts
		write(hdir, fmt.Sprintf("tile_size_%d.map", ts), buildFile(hs, noTiles))
	}

	// file_version variations
	for _, fv := range []uint32{3, 4, 5} {
		hs := defaultHS(singleZoom14, nil, nil)
		hs.fileVersion = fv
		write(hdir, fmt.Sprintf("version_%d.map", fv), buildFile(hs, noTiles))
	}

	// bbox variations
	{
		// world bbox: use zoom 0 (1 tile covers everything)
		hs := headerSpec{
			minLat: -85000000, minLon: -180000000,
			maxLat: 85000000, maxLon: 180000000,
			zoomIntervals: []zicSpec{{base: 0, min: 0, max: 0}},
		}
		write(hdir, "bbox_world.map", buildFile(hs, noTiles))
	}
	{
		// europe bbox
		hs := headerSpec{
			minLat: 36000000, minLon: -10000000,
			maxLat: 71000000, maxLon: 40000000,
			zoomIntervals: []zicSpec{{base: 7, min: 7, max: 7}},
		}
		write(hdir, "bbox_europe.map", buildFile(hs, noTiles))
	}
	{
		// southern hemisphere
		hs := headerSpec{
			minLat: -55000000, minLon: 110000000,
			maxLat: -10000000, maxLon: 155000000,
			zoomIntervals: singleZoom14,
		}
		write(hdir, "bbox_southern_hemisphere.map", buildFile(hs, noTiles))
	}
	{
		// negative lon
		hs := headerSpec{
			minLat: 51000000, minLon: -5000000,
			maxLat: 52000000, maxLon: -4000000,
			zoomIntervals: singleZoom14,
		}
		write(hdir, "bbox_negative_lon.map", buildFile(hs, noTiles))
	}
	{
		// single point bbox (min == max)
		hs := headerSpec{
			minLat: 52484000, minLon: 13379000,
			maxLat: 52484000, maxLon: 13379000,
			zoomIntervals: singleZoom14,
		}
		write(hdir, "bbox_single_point.map", buildFile(hs, noTiles))
	}

	// flags variations (all tiles absent)
	{
		hs := defaultHS(singleZoom14, nil, nil)
		write(hdir, "flag_none.map", buildFile(hs, noTiles))
	}
	{
		hs := defaultHS(singleZoom14, nil, nil)
		hs.hasDebug = true
		write(hdir, "flag_debug_only.map", buildFile(hs, noTiles))
	}
	{
		hs := defaultHS(singleZoom14, nil, nil)
		hs.hasMapStart = true
		hs.startLat = bboxMinLat
		hs.startLon = bboxMinLon
		write(hdir, "flag_map_start_only.map", buildFile(hs, noTiles))
	}
	{
		hs := defaultHS(singleZoom14, nil, nil)
		hs.hasStartZoom = true
		hs.startZoom = 14
		write(hdir, "flag_start_zoom_only.map", buildFile(hs, noTiles))
	}
	{
		hs := defaultHS(singleZoom14, nil, nil)
		hs.hasLanguage = true
		hs.language = "en"
		write(hdir, "flag_language_only.map", buildFile(hs, noTiles))
	}
	{
		hs := defaultHS(singleZoom14, nil, nil)
		hs.hasComment = true
		hs.comment = "just a comment"
		write(hdir, "flag_comment_only.map", buildFile(hs, noTiles))
	}
	{
		hs := defaultHS(singleZoom14, nil, nil)
		hs.hasCreatedBy = true
		hs.createdBy = "tool"
		write(hdir, "flag_created_by_only.map", buildFile(hs, noTiles))
	}
	{
		hs := defaultHS(singleZoom14, nil, nil)
		hs.hasDebug = true
		hs.hasMapStart = true
		hs.startLat = bboxMinLat
		hs.startLon = bboxMinLon
		hs.hasStartZoom = true
		hs.startZoom = 14
		hs.hasLanguage = true
		hs.language = "en"
		hs.hasComment = true
		hs.comment = "all flags"
		hs.hasCreatedBy = true
		hs.createdBy = "gentestmaps"
		write(hdir, "flag_all_flags.map", buildFile(hs, noTiles))
	}
	{
		// reserved bits set (0x02 and 0x01) — should still parse as valid
		hs := defaultHS(singleZoom14, nil, nil)
		hs.extraFlags = 0x03
		write(hdir, "flag_reserved_bits_set.map", buildFile(hs, noTiles))
	}

	// language variations
	for _, lang := range []string{"en", "de", "zh", "en,de,fr", ""} {
		hs := defaultHS(singleZoom14, nil, nil)
		hs.hasLanguage = true
		hs.language = lang
		name := lang
		if name == "" {
			name = "empty"
		} else {
			name = strings.ReplaceAll(name, ",", "_")
		}
		write(hdir, fmt.Sprintf("language_%s.map", name), buildFile(hs, noTiles))
	}

	// comment variations
	{
		hs := defaultHS(singleZoom14, nil, nil)
		hs.hasComment = true
		hs.comment = "hi"
		write(hdir, "comment_short.map", buildFile(hs, noTiles))
	}
	{
		hs := defaultHS(singleZoom14, nil, nil)
		hs.hasComment = true
		hs.comment = strings.Repeat("A", 200)
		write(hdir, "comment_long.map", buildFile(hs, noTiles))
	}
	{
		hs := defaultHS(singleZoom14, nil, nil)
		hs.hasComment = true
		hs.comment = "地図コメント"
		write(hdir, "comment_unicode.map", buildFile(hs, noTiles))
	}

	// start_zoom variations
	for _, sz := range []uint8{0, 7, 14, 21} {
		hs := defaultHS(singleZoom14, nil, nil)
		hs.hasStartZoom = true
		hs.startZoom = sz
		write(hdir, fmt.Sprintf("start_zoom_%d.map", sz), buildFile(hs, noTiles))
	}

	// poi_tag_count variations
	for _, n := range []int{0, 1, 10, 100} {
		hs := defaultHS(singleZoom14, makeNTags(n), nil)
		write(hdir, fmt.Sprintf("poi_tags_%d.map", n), buildFile(hs, noTiles))
	}

	// way_tag_count variations
	for _, n := range []int{0, 1, 10, 100} {
		hs := defaultHS(singleZoom14, nil, makeNTags(n))
		write(hdir, fmt.Sprintf("way_tags_%d.map", n), buildFile(hs, noTiles))
	}

	// projection variations
	{
		hs := defaultHS(singleZoom14, nil, nil)
		hs.projection = "Mercator"
		write(hdir, "projection_mercator.map", buildFile(hs, noTiles))
	}
	{
		hs := defaultHS(singleZoom14, nil, nil)
		hs.projection = " " // use a space so str() doesn't fall back to default
		write(hdir, "projection_empty.map", buildFile(hs, noTiles))
	}

	// creation_date variations
	{
		hs := defaultHS(singleZoom14, nil, nil)
		hs.creationDate = 1 // non-zero to avoid default
		write(hdir, "creation_date_zero.map", buildFile(hs, noTiles))
	}
	{
		hs := defaultHS(singleZoom14, nil, nil)
		hs.creationDate = ^uint64(0) // max uint64
		write(hdir, "creation_date_max.map", buildFile(hs, noTiles))
	}

	// ─── poi/ ─────────────────────────────────────────────────────────────────
	fmt.Println("\n=== poi/ ===")
	pdir := dir + "/poi"
	mustMkdir(pdir)

	basePOITags := []string{"amenity=restaurant"}
	makeOnePOIFile := func(filename string, ps poiSpec) {
		write(pdir, filename, buildFile(
			defaultHS(singleZoom14, basePOITags, nil),
			func(si, tx, ty int) ([]byte, bool) {
				zic := singleZoom14[si]
				body := buildTileBody(zic.min, zic.max, false, tx, ty,
					[][]poiSpec{{ps}}, nil)
				return body, false
			}))
	}

	// layer_m5 through layer_p5
	for layer := int8(-5); layer <= 5; layer++ {
		ps := poiSpec{LatDiff: 0, LonDiff: 0, Layer: layer}
		write(pdir, fmt.Sprintf("layer_%+d.map", layer),
			buildFile(
				defaultHS(singleZoom14, basePOITags, nil),
				func(si, tx, ty int) ([]byte, bool) {
					zic := singleZoom14[si]
					body := buildTileBody(zic.min, zic.max, false, tx, ty,
						[][]poiSpec{{ps}}, nil)
					return body, false
				}))
	}

	// tags_N
	for _, n := range []int{0, 1, 2, 5, 15} {
		nTags := makeNTags(n + 1) // +1 to ensure at least basePOITags slot
		if n == 0 {
			nTags = basePOITags
		}
		ps := poiSpec{Tags: makeNTagIDs(n)}
		tagsCopy := nTags
		nCopy := n
		write(pdir, fmt.Sprintf("tags_%d.map", n),
			buildFile(
				defaultHS(singleZoom14, tagsCopy, nil),
				func(si, tx, ty int) ([]byte, bool) {
					_ = nCopy
					zic := singleZoom14[si]
					body := buildTileBody(zic.min, zic.max, false, tx, ty,
						[][]poiSpec{{ps}}, nil)
					return body, false
				}))
	}

	// flags variations
	makeOnePOIFile("flags_none.map", poiSpec{})
	makeOnePOIFile("flags_name.map", poiSpec{Name: "Cafe"})
	makeOnePOIFile("flags_house.map", poiSpec{HouseNumber: "42"})
	makeOnePOIFile("flags_elevation.map", poiSpec{HasElevation: true, Elevation: 100})
	makeOnePOIFile("flags_name_house.map", poiSpec{Name: "Shop", HouseNumber: "7B"})
	makeOnePOIFile("flags_name_elevation.map", poiSpec{Name: "Peak", HasElevation: true, Elevation: 2000})
	makeOnePOIFile("flags_house_elevation.map", poiSpec{HouseNumber: "1", HasElevation: true, Elevation: 50})
	makeOnePOIFile("flags_all.map", poiSpec{Name: "Full", HouseNumber: "99", HasElevation: true, Elevation: 300})

	// elevation variations
	makeOnePOIFile("elevation_neg416.map", poiSpec{HasElevation: true, Elevation: -416})
	makeOnePOIFile("elevation_0.map", poiSpec{HasElevation: true, Elevation: 0})
	makeOnePOIFile("elevation_100.map", poiSpec{HasElevation: true, Elevation: 100})
	makeOnePOIFile("elevation_8848.map", poiSpec{HasElevation: true, Elevation: 8848})

	// name variations
	makeOnePOIFile("name_ascii.map", poiSpec{Name: "Cafe"})
	makeOnePOIFile("name_unicode.map", poiSpec{Name: "Bäckerei"})
	makeOnePOIFile("name_long.map", poiSpec{Name: strings.Repeat("X", 100)})
	makeOnePOIFile("name_empty.map", poiSpec{Name: ""}) // has_name=false since Name==""

	// position variations
	makeOnePOIFile("position_origin.map", poiSpec{LatDiff: 0, LonDiff: 0})
	makeOnePOIFile("position_positive.map", poiSpec{LatDiff: 500, LonDiff: 500})
	makeOnePOIFile("position_negative_lat.map", poiSpec{LatDiff: -500, LonDiff: 300})

	// count_N
	for _, n := range []int{0, 1, 5, 20, 100} {
		nCopy := n
		write(pdir, fmt.Sprintf("count_%d.map", n),
			buildFile(
				defaultHS(singleZoom14, basePOITags, nil),
				func(si, tx, ty int) ([]byte, bool) {
					zic := singleZoom14[si]
					pois := make([]poiSpec, nCopy)
					for i := range pois {
						pois[i] = poiSpec{Tags: []uint32{0}, Name: fmt.Sprintf("POI %d", i)}
					}
					body := buildTileBody(zic.min, zic.max, false, tx, ty,
						[][]poiSpec{pois}, nil)
					return body, false
				}))
	}

	// ─── way/ ─────────────────────────────────────────────────────────────────
	fmt.Println("\n=== way/ ===")
	wdir := dir + "/way"
	mustMkdir(wdir)

	baseWayTags := []string{"highway=residential"}
	triNodes := [][][2]int32{
		{{0, 0}, {1000, 0}, {500, 500}},
	}
	makeOneWayFile := func(filename string, ws waySpec) {
		write(wdir, filename, buildFile(
			defaultHS(singleZoom14, nil, baseWayTags),
			func(si, tx, ty int) ([]byte, bool) {
				zic := singleZoom14[si]
				body := buildTileBody(zic.min, zic.max, false, tx, ty,
					nil, [][]waySpec{{ws}})
				return body, false
			}))
	}

	// layer_m5 through layer_p5
	for layer := int8(-5); layer <= 5; layer++ {
		ws := waySpec{SubTileBitmap: 0xffff, Layer: layer, Tags: []uint32{0}, Blocks: triNodes}
		write(wdir, fmt.Sprintf("layer_%+d.map", layer),
			buildFile(
				defaultHS(singleZoom14, nil, baseWayTags),
				func(si, tx, ty int) ([]byte, bool) {
					zic := singleZoom14[si]
					body := buildTileBody(zic.min, zic.max, false, tx, ty,
						nil, [][]waySpec{{ws}})
					return body, false
				}))
	}

	// bitmap variations
	for _, bm := range []struct {
		name string
		val  uint16
	}{
		{"0x0000", 0x0000},
		{"0xffff", 0xffff},
		{"0x00ff", 0x00ff},
		{"0xff00", 0xff00},
		{"0x5555", 0x5555},
		{"0xaaaa", 0xaaaa},
	} {
		ws := waySpec{SubTileBitmap: bm.val, Tags: []uint32{0}, Blocks: triNodes}
		makeOneWayFile(fmt.Sprintf("bitmap_%s.map", bm.name), ws)
	}

	// tags_N
	for _, n := range []int{0, 1, 5, 15} {
		nTags := makeNTags(n + 1)
		if n == 0 {
			nTags = baseWayTags
		}
		ws := waySpec{SubTileBitmap: 0xffff, Tags: makeNTagIDs(n), Blocks: triNodes}
		wsCopy := ws
		tagsCopy := nTags
		write(wdir, fmt.Sprintf("tags_%d.map", n),
			buildFile(
				defaultHS(singleZoom14, nil, tagsCopy),
				func(si, tx, ty int) ([]byte, bool) {
					zic := singleZoom14[si]
					body := buildTileBody(zic.min, zic.max, false, tx, ty,
						nil, [][]waySpec{{wsCopy}})
					return body, false
				}))
	}

	// flags variations
	makeOneWayFile("flags_name.map", waySpec{SubTileBitmap: 0xffff, Tags: []uint32{0}, Name: "Street", Blocks: triNodes})
	makeOneWayFile("flags_house.map", waySpec{SubTileBitmap: 0xffff, Tags: []uint32{0}, HouseNumber: "3", Blocks: triNodes})
	makeOneWayFile("flags_reference.map", waySpec{SubTileBitmap: 0xffff, Tags: []uint32{0}, Reference: "A1", Blocks: triNodes})
	makeOneWayFile("flags_label_position.map", waySpec{SubTileBitmap: 0xffff, Tags: []uint32{0}, HasLabelPos: true, LabelLat: 500, LabelLon: 500, Blocks: triNodes})
	makeOneWayFile("flags_explicit_1block.map", waySpec{SubTileBitmap: 0xffff, Tags: []uint32{0}, ExplicitNumBlocks: true, Blocks: triNodes})
	makeOneWayFile("flags_double_delta.map", waySpec{SubTileBitmap: 0xffff, Tags: []uint32{0}, DoubleDelta: true, Blocks: triNodes})
	makeOneWayFile("flags_all.map", waySpec{
		SubTileBitmap: 0xffff, Tags: []uint32{0},
		Name: "Full Way", HouseNumber: "5", Reference: "B2",
		HasLabelPos: true, LabelLat: 200, LabelLon: 300,
		ExplicitNumBlocks: true, DoubleDelta: true,
		Blocks: triNodes,
	})

	// nodes_N
	for _, n := range []int{1, 2, 3, 10, 100} {
		nodes := make([][2]int32, n)
		for i := range nodes {
			nodes[i] = [2]int32{int32(i * 100), 0}
		}
		ws := waySpec{SubTileBitmap: 0xffff, Tags: []uint32{0}, Blocks: append([][][2]int32{}, nodes)}
		makeOneWayFile(fmt.Sprintf("nodes_%d.map", n), ws)
	}

	// single-delta vs double-delta (straight line: double deltas are all 0)
	{
		// straight line: (0,0),(1000,0),(2000,0),(3000,0)
		// single delta: each diff from prev
		ws := waySpec{
			SubTileBitmap: 0xffff, Tags: []uint32{0},
			DoubleDelta: false,
			Blocks:      [][][2]int32{{{0, 0}, {1000, 0}, {1000, 0}, {1000, 0}}},
		}
		makeOneWayFile("encoding_single_delta.map", ws)
	}
	{
		// same straight line, double delta encoding:
		// v0=(0,0), v1=(1000,0), v2=(0,0), v3=(0,0)
		ws := waySpec{
			SubTileBitmap: 0xffff, Tags: []uint32{0},
			DoubleDelta: true,
			Blocks:      [][][2]int32{{{0, 0}, {1000, 0}, {0, 0}, {0, 0}}},
		}
		makeOneWayFile("encoding_double_delta.map", ws)
	}

	// multipolygon: 2 blocks (outer + inner hole)
	{
		outerSquare := [][2]int32{{0, 0}, {2000, 0}, {0, 2000}, {-2000, 0}}
		innerSquare := [][2]int32{{500, 500}, {1000, 0}, {0, 1000}, {-1000, 0}}
		ws := waySpec{
			SubTileBitmap: 0xffff, Tags: []uint32{0},
			Blocks: [][][2]int32{outerSquare, innerSquare},
		}
		makeOneWayFile("multipolygon_2blocks.map", ws)
	}

	// multipolygon: 3 blocks (outer + 2 inner holes)
	{
		outerSquare := [][2]int32{{0, 0}, {3000, 0}, {0, 3000}, {-3000, 0}}
		inner1 := [][2]int32{{500, 500}, {500, 0}, {0, 500}, {-500, 0}}
		inner2 := [][2]int32{{1500, 1500}, {500, 0}, {0, 500}, {-500, 0}}
		ws := waySpec{
			SubTileBitmap: 0xffff, Tags: []uint32{0},
			Blocks: [][][2]int32{outerSquare, inner1, inner2},
		}
		makeOneWayFile("multipolygon_3blocks.map", ws)
	}

	// count_N
	for _, n := range []int{0, 1, 5, 20, 100} {
		nCopy := n
		write(wdir, fmt.Sprintf("count_%d.map", n),
			buildFile(
				defaultHS(singleZoom14, nil, baseWayTags),
				func(si, tx, ty int) ([]byte, bool) {
					zic := singleZoom14[si]
					ways := make([]waySpec, nCopy)
					for i := range ways {
						ways[i] = waySpec{
							SubTileBitmap: 0xffff,
							Tags:          []uint32{0},
							Blocks:        triNodes,
						}
					}
					body := buildTileBody(zic.min, zic.max, false, tx, ty,
						nil, [][]waySpec{ways})
					return body, false
				}))
	}

	// ─── tile/ ────────────────────────────────────────────────────────────────
	fmt.Println("\n=== tile/ ===")
	tdir := dir + "/tile"
	mustMkdir(tdir)

	// empty_absent: tile body is absent (nil)
	write(tdir, "empty_absent.map",
		buildFile(defaultHS(singleZoom14, nil, nil), noTiles))

	// empty_present: tile body present but contains 0 POIs, 0 ways
	write(tdir, "empty_present.map",
		buildFile(defaultHS(singleZoom14, nil, nil),
			func(si, tx, ty int) ([]byte, bool) {
				zic := singleZoom14[si]
				return emptyTileBody(zic.min, zic.max, false, tx, ty), false
			}))

	// water_no_body
	write(tdir, "water_no_body.map",
		buildFile(defaultHS(singleZoom14, nil, nil),
			func(si, tx, ty int) ([]byte, bool) { return nil, true }))

	// water_empty_body
	write(tdir, "water_empty_body.map",
		buildFile(defaultHS(singleZoom14, nil, nil),
			func(si, tx, ty int) ([]byte, bool) {
				zic := singleZoom14[si]
				return emptyTileBody(zic.min, zic.max, false, tx, ty), true
			}))

	// zoom_table_poi_at_z0_way_at_z1: 2-zoom subfile (min=13, max=14)
	{
		zics := []zicSpec{{base: 14, min: 13, max: 14}}
		hs := defaultHS(zics, basePOITags, baseWayTags)
		write(tdir, "zoom_table_poi_at_z0_way_at_z1.map",
			buildFile(hs, func(si, tx, ty int) ([]byte, bool) {
				// zi=0 (zoom 13): 1 POI, 0 ways
				// zi=1 (zoom 14): 0 POIs, 1 way
				pois := [][]poiSpec{{simplePOI()}, nil}
				ways := [][]waySpec{nil, {simpleWay()}}
				return buildTileBody(13, 14, false, tx, ty, pois, ways), false
			}))
	}

	// zoom_table_way_at_z0_poi_at_z1
	{
		zics := []zicSpec{{base: 14, min: 13, max: 14}}
		hs := defaultHS(zics, basePOITags, baseWayTags)
		write(tdir, "zoom_table_way_at_z0_poi_at_z1.map",
			buildFile(hs, func(si, tx, ty int) ([]byte, bool) {
				pois := [][]poiSpec{nil, {simplePOI()}}
				ways := [][]waySpec{{simpleWay()}, nil}
				return buildTileBody(13, 14, false, tx, ty, pois, ways), false
			}))
	}

	// zoom_table_all_at_z0
	{
		zics := []zicSpec{{base: 14, min: 13, max: 14}}
		hs := defaultHS(zics, basePOITags, baseWayTags)
		write(tdir, "zoom_table_all_at_z0.map",
			buildFile(hs, func(si, tx, ty int) ([]byte, bool) {
				pois := [][]poiSpec{{simplePOI()}, nil}
				ways := [][]waySpec{{simpleWay()}, nil}
				return buildTileBody(13, 14, false, tx, ty, pois, ways), false
			}))
	}

	// zoom_table_all_at_last
	{
		zics := []zicSpec{{base: 14, min: 13, max: 14}}
		hs := defaultHS(zics, basePOITags, baseWayTags)
		write(tdir, "zoom_table_all_at_last.map",
			buildFile(hs, func(si, tx, ty int) ([]byte, bool) {
				pois := [][]poiSpec{nil, {simplePOI()}}
				ways := [][]waySpec{nil, {simpleWay()}}
				return buildTileBody(13, 14, false, tx, ty, pois, ways), false
			}))
	}

	// first_way_offset_zero: 1 way, 0 POIs
	write(tdir, "first_way_offset_zero.map",
		buildFile(defaultHS(singleZoom14, nil, baseWayTags),
			func(si, tx, ty int) ([]byte, bool) {
				zic := singleZoom14[si]
				return buildTileBody(zic.min, zic.max, false, tx, ty,
					nil, [][]waySpec{{simpleWay()}}), false
			}))

	// first_way_offset_nonzero: 1 POI + 1 way
	write(tdir, "first_way_offset_nonzero.map",
		buildFile(defaultHS(singleZoom14, basePOITags, baseWayTags),
			func(si, tx, ty int) ([]byte, bool) {
				zic := singleZoom14[si]
				return buildTileBody(zic.min, zic.max, false, tx, ty,
					[][]poiSpec{{simplePOI()}}, [][]waySpec{{simpleWay()}}), false
			}))

	// debug_index_sig: has IndexStart signature
	{
		hs := defaultHS(singleZoom14, nil, nil)
		hs.hasDebug = true
		write(tdir, "debug_index_sig.map", buildFile(hs, noTiles))
	}

	// debug_tile_sig
	{
		hs := defaultHS(singleZoom14, nil, nil)
		hs.hasDebug = true
		write(tdir, "debug_tile_sig.map",
			buildFile(hs, func(si, tx, ty int) ([]byte, bool) {
				zic := singleZoom14[si]
				return emptyTileBody(zic.min, zic.max, true, tx, ty), false
			}))
	}

	// debug_poi_sig
	{
		hs := defaultHS(singleZoom14, basePOITags, nil)
		hs.hasDebug = true
		write(tdir, "debug_poi_sig.map",
			buildFile(hs, func(si, tx, ty int) ([]byte, bool) {
				zic := singleZoom14[si]
				return buildTileBody(zic.min, zic.max, true, tx, ty,
					[][]poiSpec{{simplePOI()}}, nil), false
			}))
	}

	// debug_way_sig
	{
		hs := defaultHS(singleZoom14, nil, baseWayTags)
		hs.hasDebug = true
		write(tdir, "debug_way_sig.map",
			buildFile(hs, func(si, tx, ty int) ([]byte, bool) {
				zic := singleZoom14[si]
				return buildTileBody(zic.min, zic.max, true, tx, ty,
					nil, [][]waySpec{{simpleWay()}}), false
			}))
	}

	// debug_all_sigs
	{
		hs := defaultHS(singleZoom14, basePOITags, baseWayTags)
		hs.hasDebug = true
		write(tdir, "debug_all_sigs.map",
			buildFile(hs, func(si, tx, ty int) ([]byte, bool) {
				zic := singleZoom14[si]
				return buildTileBody(zic.min, zic.max, true, tx, ty,
					[][]poiSpec{{simplePOI()}}, [][]waySpec{{simpleWay()}}), false
			}))
	}

	// ─── zoom/ ────────────────────────────────────────────────────────────────
	fmt.Println("\n=== zoom/ ===")
	zdir := dir + "/zoom"
	mustMkdir(zdir)

	// base_zoom_N
	for _, base := range []uint8{0, 7, 10, 12, 14} {
		zics := []zicSpec{{base: base, min: base, max: base}}
		var hs headerSpec
		if base == 0 {
			hs = headerSpec{
				minLat: -85000000, minLon: -180000000,
				maxLat: 85000000, maxLon: 180000000,
				zoomIntervals: zics,
			}
		} else {
			hs = defaultHS(zics, nil, nil)
		}
		write(zdir, fmt.Sprintf("base_zoom_%d.map", base), buildFile(hs, noTiles))
	}

	// wide_zoom_range: base=14, min=0, max=21
	write(zdir, "wide_zoom_range.map",
		buildFile(defaultHS([]zicSpec{{base: 14, min: 0, max: 21}}, nil, nil), noTiles))

	// two_intervals
	write(zdir, "two_intervals.map",
		buildFile(defaultHS([]zicSpec{
			{base: 7, min: 0, max: 7},
			{base: 14, min: 8, max: 21},
		}, nil, nil), noTiles))

	// three_intervals
	write(zdir, "three_intervals.map",
		buildFile(defaultHS([]zicSpec{
			{base: 7, min: 0, max: 7},
			{base: 12, min: 8, max: 12},
			{base: 14, min: 13, max: 21},
		}, nil, nil), noTiles))

	// grid_2x1_zoom14: 2 tiles wide
	{
		hs := headerSpec{
			minLat: bboxMinLat, minLon: 13360000,
			maxLat: bboxMaxLat, maxLon: 13380000,
			zoomIntervals: singleZoom14,
		}
		write(zdir, "grid_2x1_zoom14.map", buildFile(hs, noTiles))
	}

	// grid_3x1_zoom14: 3 tiles wide
	{
		hs := headerSpec{
			minLat: bboxMinLat, minLon: 13300000,
			maxLat: bboxMaxLat, maxLon: 13430000,
			zoomIntervals: singleZoom14,
		}
		write(zdir, "grid_3x1_zoom14.map", buildFile(hs, noTiles))
	}

	// grid_1x2_zoom14: 2 tiles tall
	{
		hs := headerSpec{
			minLat: 52450000, minLon: bboxMinLon,
			maxLat: 52510000, maxLon: bboxMaxLon,
			zoomIntervals: singleZoom14,
		}
		write(zdir, "grid_1x2_zoom14.map", buildFile(hs, noTiles))
	}

	// grid_2x2_zoom14
	{
		hs := headerSpec{
			minLat: 52450000, minLon: 13360000,
			maxLat: 52510000, maxLon: 13380000,
			zoomIntervals: singleZoom14,
		}
		write(zdir, "grid_2x2_zoom14.map", buildFile(hs, noTiles))
	}

	// subfile_all_absent
	write(zdir, "subfile_all_absent.map",
		buildFile(defaultHS(singleZoom14, nil, nil), noTiles))

	// subfile_all_present_empty
	write(zdir, "subfile_all_present_empty.map",
		buildFile(defaultHS(singleZoom14, nil, nil),
			func(si, tx, ty int) ([]byte, bool) {
				zic := singleZoom14[si]
				return emptyTileBody(zic.min, zic.max, false, tx, ty), false
			}))

	// subfile_mixed: 2x1 grid, first tile has content, second is absent
	{
		hs := headerSpec{
			minLat: bboxMinLat, minLon: 13360000,
			maxLat: bboxMaxLat, maxLon: 13380000,
			zoomIntervals: singleZoom14,
		}
		// Determine tile x values
		x0, _ := toXY(bboxMinLat, 13360000, 14)
		write(zdir, "subfile_mixed.map",
			buildFile(hs, func(si, tx, ty int) ([]byte, bool) {
				if tx == x0 {
					zic := singleZoom14[si]
					return emptyTileBody(zic.min, zic.max, false, tx, ty), false
				}
				return nil, false
			}))
	}

	// min_equals_max: base=14, min=14, max=14
	write(zdir, "min_equals_max.map",
		buildFile(defaultHS([]zicSpec{{base: 14, min: 14, max: 14}}, nil, nil), noTiles))

	// min_lt_max_small: base=14, min=13, max=15
	write(zdir, "min_lt_max_small.map",
		buildFile(defaultHS([]zicSpec{{base: 14, min: 13, max: 15}}, nil, nil), noTiles))

	// min_lt_max_large: base=7, min=0, max=14
	write(zdir, "min_lt_max_large.map",
		buildFile(defaultHS([]zicSpec{{base: 7, min: 0, max: 14}}, nil, nil), noTiles))

	// ─── encoding/ ────────────────────────────────────────────────────────────
	fmt.Println("\n=== encoding/ ===")
	edir := dir + "/encoding"
	mustMkdir(edir)

	// VBE-U edge cases via tag IDs
	for _, tc := range []struct {
		name string
		val  uint32
	}{
		{"vbeu_val0", 0},
		{"vbeu_val127", 127},
		{"vbeu_val128", 128},
		{"vbeu_val16383", 16383},
		{"vbeu_val16384", 16384},
	} {
		n := int(tc.val) + 1
		nTags := makeNTags(n)
		ps := poiSpec{Tags: []uint32{tc.val}}
		write(edir, tc.name+".map",
			buildFile(
				defaultHS(singleZoom14, nTags, nil),
				func(si, tx, ty int) ([]byte, bool) {
					zic := singleZoom14[si]
					return buildTileBody(zic.min, zic.max, false, tx, ty,
						[][]poiSpec{{ps}}, nil), false
				}))
	}

	// VBE-S edge cases via POI lat_diff
	for _, tc := range []struct {
		name string
		val  int32
	}{
		{"vbes_zero", 0},
		{"vbes_pos63", 63},
		{"vbes_neg63", -63},
		{"vbes_pos64", 64},
		{"vbes_neg64", -64},
		{"vbes_pos8191", 8191},
		{"vbes_neg8191", -8191},
		{"vbes_pos8192", 8192},
		{"vbes_neg8192", -8192},
	} {
		ps := poiSpec{LatDiff: tc.val, LonDiff: 0, Tags: []uint32{0}}
		write(edir, tc.name+".map",
			buildFile(
				defaultHS(singleZoom14, basePOITags, nil),
				func(si, tx, ty int) ([]byte, bool) {
					zic := singleZoom14[si]
					return buildTileBody(zic.min, zic.max, false, tx, ty,
						[][]poiSpec{{ps}}, nil), false
				}))
	}

	// unicode string
	{
		ps := poiSpec{Tags: []uint32{0}, Name: "東京都 Tōkyō-to 🗺"}
		write(edir, "unicode_string.map",
			buildFile(
				defaultHS(singleZoom14, basePOITags, nil),
				func(si, tx, ty int) ([]byte, bool) {
					zic := singleZoom14[si]
					return buildTileBody(zic.min, zic.max, false, tx, ty,
						[][]poiSpec{{ps}}, nil), false
				}))
	}

	// large_coords: way with node lat_diff=999999
	{
		ws := waySpec{
			SubTileBitmap: 0xffff, Tags: []uint32{0},
			Blocks: [][][2]int32{{{0, 0}, {999999, 0}, {-999999, 0}}},
		}
		write(edir, "large_coords.map",
			buildFile(
				defaultHS(singleZoom14, nil, baseWayTags),
				func(si, tx, ty int) ([]byte, bool) {
					zic := singleZoom14[si]
					return buildTileBody(zic.min, zic.max, false, tx, ty,
						nil, [][]waySpec{{ws}}), false
				}))
	}

	// ─── invalid/ ─────────────────────────────────────────────────────────────
	fmt.Println("\n=== invalid/ ===")
	idir := dir + "/invalid"
	mustMkdir(idir)

	// Build a valid base file with debug=true, 1 POI, 1 way
	baseHS := defaultHS(singleZoom14, []string{"amenity=restaurant"}, []string{"highway=residential"})
	baseHS.hasDebug = true
	baseValid := buildFile(baseHS, func(si, tx, ty int) ([]byte, bool) {
		zic := singleZoom14[si]
		return poiAndWayTileBody(zic.min, zic.max, true, tx, ty, []uint32{0}, []uint32{0}), false
	})

	// ── header corruption ──
	{
		d := clone(baseValid)
		d[0] = 0xDE
		d[1] = 0xAD
		d[2] = 0xBE
		d[3] = 0xEF
		write(idir, "bad_magic_4bytes.map.bad", d)
	}
	{
		d := clone(baseValid)
		d[19] ^= 0xff
		write(idir, "bad_magic_last_byte.map.bad", d)
	}
	{
		d := clone(baseValid)
		v := binary.BigEndian.Uint32(d[20:24])
		binary.BigEndian.PutUint32(d[20:24], v+1)
		write(idir, "bad_header_size_plus1.map.bad", d)
	}
	{
		d := clone(baseValid)
		v := binary.BigEndian.Uint32(d[20:24])
		if v > 0 {
			binary.BigEndian.PutUint32(d[20:24], v-1)
		}
		write(idir, "bad_header_size_minus1.map.bad", d)
	}
	{
		d := clone(baseValid)
		binary.BigEndian.PutUint32(d[20:24], 0)
		write(idir, "bad_header_size_zero.map.bad", d)
	}
	{
		d := clone(baseValid)
		var tmp [4]byte
		copy(tmp[:], d[44:48])
		copy(d[44:48], d[52:56])
		copy(d[52:56], tmp[:])
		write(idir, "bad_bbox_minlat_gt_maxlat.map.bad", d)
	}
	{
		d := clone(baseValid)
		var tmp [4]byte
		copy(tmp[:], d[48:52])
		copy(d[48:52], d[56:60])
		copy(d[56:60], tmp[:])
		write(idir, "bad_bbox_minlon_gt_maxlon.map.bad", d)
	}
	{
		// Find zoom interval bytes: after the fixed header fields, projection string, flags, tag lists...
		// Build a fresh minimal valid file to locate zoom interval bytes
		// For our baseHS: the zoom interval config starts after all header fields
		// We'll scan for known base/min/max pattern (14,14,14) after the tag lists
		// Actually, simply build the same spec without debug to find the offset, then apply
		d := clone(baseValid)
		// Scan for the zoom interval byte triple 14,14,14
		for i := 0; i < len(d)-3; i++ {
			if d[i] == 14 && d[i+1] == 14 && d[i+2] == 14 {
				// Verify this is the zoom interval (next bytes should be 8-byte offset placeholders)
				d[i+1] = 20 // min_zoom = 20
				d[i+2] = 5  // max_zoom = 5
				break
			}
		}
		write(idir, "bad_zoom_min_gt_max.map.bad", d)
	}

	// ── truncations ──
	for _, cutAt := range []struct {
		at   int
		name string
	}{
		{19, "truncated_at_19"},
		{23, "truncated_at_23"},
		{27, "truncated_at_27"},
		{43, "truncated_at_43"},
		{50, "truncated_at_50"},
		{61, "truncated_at_61"},
	} {
		if cutAt.at <= len(baseValid) {
			write(idir, cutAt.name+".map.bad", clone(baseValid[:cutAt.at]))
		}
	}
	// truncated_in_zoom_config: cut right in the middle of the header
	{
		hdrSize := int(binary.BigEndian.Uint32(baseValid[20:24]))
		mid := 24 + hdrSize/2
		if mid < len(baseValid) {
			write(idir, "truncated_in_zoom_config.map.bad", clone(baseValid[:mid]))
		}
	}
	// truncated_before_subfile: valid header, no subfile data
	{
		hdrSize := int(binary.BigEndian.Uint32(baseValid[20:24]))
		cutAt := 24 + hdrSize
		if cutAt <= len(baseValid) {
			write(idir, "truncated_before_subfile.map.bad", clone(baseValid[:cutAt]))
		}
	}
	// truncated_in_tile_index: cut in middle of tile index
	{
		// Subfile starts right after the header. For our base file with debug=true,
		// the subfile starts at offset = 24 + headerSize.
		hdrSize := int(binary.BigEndian.Uint32(baseValid[20:24]))
		subStart := 24 + hdrSize
		// Debug index sig is 16 bytes, then tile index starts
		if baseHS.hasDebug {
			subStart += 16
		}
		cutAt := subStart + 2 // cut in middle of 5-byte tile index entry
		if cutAt < len(baseValid) {
			write(idir, "truncated_in_tile_index.map.bad", clone(baseValid[:cutAt]))
		}
	}
	// truncated_in_tile_body: cut in middle of tile body
	{
		hdrSize := int(binary.BigEndian.Uint32(baseValid[20:24]))
		subStart := 24 + hdrSize
		// After index sig (16) + tile index (5) = 21 bytes into subfile
		bodyStart := subStart + 16 + 5 // with debug
		cutAt := bodyStart + 5         // cut into tile body
		if cutAt < len(baseValid) {
			write(idir, "truncated_in_tile_body.map.bad", clone(baseValid[:cutAt]))
		}
	}

	// ── debug signature corruptions ──
	{
		// Find and corrupt IndexStart signature
		d := clone(baseValid)
		sig := []byte("+++IndexStart+++")
		for i := 0; i <= len(d)-len(sig); i++ {
			if string(d[i:i+len(sig)]) == string(sig) {
				copy(d[i:], "+++BADXXXXXXX+++")
				break
			}
		}
		write(idir, "bad_index_sig.map.bad", d)
	}
	{
		// Find and corrupt TileStart signature
		d := clone(baseValid)
		prefix := "###TileStart"
		for i := 0; i <= len(d)-32; i++ {
			if string(d[i:i+len(prefix)]) == prefix {
				// Zero out and write wrong content
				for j := 0; j < 32; j++ {
					d[i+j] = 0
				}
				copy(d[i:], "###TileStart0,0###")
				break
			}
		}
		write(idir, "bad_tile_sig.map.bad", d)
	}
	{
		// Find and corrupt POIStart signature
		d := clone(baseValid)
		prefix := "***POIStart"
		for i := 0; i <= len(d)-32; i++ {
			if i+len(prefix) <= len(d) && string(d[i:i+len(prefix)]) == prefix {
				for j := 0; j < 32; j++ {
					d[i+j] = 0
				}
				copy(d[i:], "***WRONGSIG0***")
				break
			}
		}
		write(idir, "bad_poi_sig.map.bad", d)
	}
	{
		// Find and corrupt WayStart signature
		d := clone(baseValid)
		prefix := "---WayStart"
		for i := 0; i <= len(d)-32; i++ {
			if i+len(prefix) <= len(d) && string(d[i:i+len(prefix)]) == prefix {
				for j := 0; j < 32; j++ {
					d[i+j] = 0
				}
				copy(d[i:], "---BADXXXXX0---")
				break
			}
		}
		write(idir, "bad_way_sig.map.bad", d)
	}

	// ── data corruption ──
	{
		// bad_way_data_size_plus1: find VbeU(way_data_size) and increment
		d := clone(baseValid)
		// Find the WayStart debug sig, then skip it to get to way_data_size
		prefix := "---WayStart"
		for i := 0; i <= len(d)-32; i++ {
			if i+len(prefix) <= len(d) && string(d[i:i+len(prefix)]) == prefix {
				// way_data_size VbeU is at i+32
				d[i+32]++
				break
			}
		}
		write(idir, "bad_way_data_size_plus1.map.bad", d)
	}
	{
		// bad_way_data_size_minus1
		d := clone(baseValid)
		prefix := "---WayStart"
		for i := 0; i <= len(d)-32; i++ {
			if i+len(prefix) <= len(d) && string(d[i:i+len(prefix)]) == prefix {
				if d[i+32] > 0 {
					d[i+32]--
				}
				break
			}
		}
		write(idir, "bad_way_data_size_minus1.map.bad", d)
	}
	{
		// bad_poi_count_overflow: find zoom table and set num_pois[0] = 99
		// In our debug tile: after TileStart sig (32B), zoom table starts
		// Find TileStart sig
		d := clone(baseValid)
		prefix := "###TileStart"
		for i := 0; i <= len(d)-32; i++ {
			if i+len(prefix) <= len(d) && string(d[i:i+len(prefix)]) == prefix {
				// zoom table: num_pois (VbeU) at i+32
				d[i+32] = 99 // set num_pois=99 (only 1 actually present)
				break
			}
		}
		write(idir, "bad_poi_count_overflow.map.bad", d)
	}
	{
		// bad_way_count_overflow: set num_ways[0] = 99
		d := clone(baseValid)
		prefix := "###TileStart"
		for i := 0; i <= len(d)-32; i++ {
			if i+len(prefix) <= len(d) && string(d[i:i+len(prefix)]) == prefix {
				// zoom table: num_pois (1 byte VbeU) at i+32, num_ways (1 byte VbeU) at i+33
				d[i+33] = 99
				break
			}
		}
		write(idir, "bad_way_count_overflow.map.bad", d)
	}

	// ─── poi/flags/ ──────────────────────────────────────────────────────────
	fmt.Println("\n=== poi/flags/ ===")
	pfdir := dir + "/poi/flags"
	mustMkdir(pfdir)

	// 88 files: 8 flag combos × 11 layers
	for layerV := int8(-5); layerV <= 5; layerV++ {
		for flagsV := 0; flagsV < 8; flagsV++ {
			lv := layerV
			fv := flagsV
			ps := poiSpec{Layer: lv}
			if fv&4 != 0 {
				ps.Name = "Name"
			}
			if fv&2 != 0 {
				ps.HouseNumber = "42"
			}
			if fv&1 != 0 {
				ps.HasElevation = true
				ps.Elevation = 100
			}
			psCopy := ps
			fname := fmt.Sprintf("layer_%s_flags_%s.map", layerStr(lv), fmt.Sprintf("%03b", fv))
			write(pfdir, fname, buildFile(
				defaultHS(singleZoom14, []string{"amenity=restaurant"}, nil),
				func(si, tx, ty int) ([]byte, bool) {
					zic := singleZoom14[si]
					body := buildTileBody(zic.min, zic.max, false, tx, ty,
						[][]poiSpec{{psCopy}}, nil)
					return body, false
				}))
		}
	}

	// ─── way/flags/ ───────────────────────────────────────────────────────────
	fmt.Println("\n=== way/flags/ ===")
	wfdir := dir + "/way/flags"
	mustMkdir(wfdir)

	// 704 files: 64 flag combos × 11 layers
	for layerV := int8(-5); layerV <= 5; layerV++ {
		for flagsV := 0; flagsV < 64; flagsV++ {
			lv := layerV
			fv := flagsV
			ws := waySpec{
				SubTileBitmap: 0xffff,
				Layer:         lv,
				Tags:          []uint32{0},
				Blocks:        [][][2]int32{{{0, 0}, {1000, 0}, {500, 500}}},
			}
			if fv&32 != 0 {
				ws.Name = "Road"
			}
			if fv&16 != 0 {
				ws.HouseNumber = "7"
			}
			if fv&8 != 0 {
				ws.Reference = "A1"
			}
			if fv&4 != 0 {
				ws.HasLabelPos = true
				ws.LabelLat = 300
				ws.LabelLon = 200
			}
			if fv&2 != 0 {
				ws.ExplicitNumBlocks = true
			}
			if fv&1 != 0 {
				ws.DoubleDelta = true
			}
			wsCopy := ws
			fname := fmt.Sprintf("layer_%s_flags_%s.map", layerStr(lv), fmt.Sprintf("%06b", fv))
			write(wfdir, fname, buildFile(
				defaultHS(singleZoom14, nil, []string{"highway=residential"}),
				func(si, tx, ty int) ([]byte, bool) {
					zic := singleZoom14[si]
					body := buildTileBody(zic.min, zic.max, false, tx, ty,
						nil, [][]waySpec{{wsCopy}})
					return body, false
				}))
		}
	}

	// ─── header/flags_combo/ ─────────────────────────────────────────────────
	fmt.Println("\n=== header/flags_combo/ ===")
	hfcdir := dir + "/header/flags_combo"
	mustMkdir(hfcdir)

	// 64 files: all combos of 6 header flags
	for combo := 0; combo < 64; combo++ {
		hs := defaultHS(singleZoom14, nil, nil)
		if combo&32 != 0 {
			hs.hasDebug = true
		}
		if combo&16 != 0 {
			hs.hasMapStart = true
			hs.startLat = bboxMinLat
			hs.startLon = bboxMinLon
		}
		if combo&8 != 0 {
			hs.hasStartZoom = true
			hs.startZoom = 14
		}
		if combo&4 != 0 {
			hs.hasLanguage = true
			hs.language = "en"
		}
		if combo&2 != 0 {
			hs.hasComment = true
			hs.comment = "test"
		}
		if combo&1 != 0 {
			hs.hasCreatedBy = true
			hs.createdBy = "gen"
		}
		fname := fmt.Sprintf("combo_%s.map", fmt.Sprintf("%06b", combo))
		write(hfcdir, fname, buildFile(hs, noTiles))
	}

	// ─── zoom/ additional base zooms ─────────────────────────────────────────
	fmt.Println("\n=== zoom/ additional base zooms ===")
	// Already written: 0, 7, 10, 12, 14 — write the rest up to 21
	skipZooms := map[uint8]bool{0: true, 7: true, 10: true, 12: true, 14: true}
	for base := uint8(1); base <= 21; base++ {
		if skipZooms[base] {
			continue
		}
		zics := []zicSpec{{base: base, min: base, max: base}}
		var hs headerSpec
		if base < 5 {
			hs = headerSpec{
				minLat: -85000000, minLon: -180000000,
				maxLat: 85000000, maxLon: 180000000,
				zoomIntervals: zics,
			}
		} else {
			hs = defaultHS(zics, nil, nil)
		}
		write(zdir, fmt.Sprintf("base_zoom_%d.map", base), buildFile(hs, noTiles))
	}

	// ─── encoding/ additional VBE-U boundary values ───────────────────────────
	fmt.Println("\n=== encoding/ additional VBE-U values ===")
	// New values: 1, 62, 63, 64, 126, 254, 255, 256, 2047, 2048
	// (0, 127, 128, 16383, 16384 already exist)
	for _, tc := range []struct {
		name string
		val  uint32
	}{
		{"vbeu_val1", 1},
		{"vbeu_val62", 62},
		{"vbeu_val63", 63},
		{"vbeu_val64", 64},
		{"vbeu_val126", 126},
		{"vbeu_val254", 254},
		{"vbeu_val255", 255},
		{"vbeu_val256", 256},
		{"vbeu_val2047", 2047},
		{"vbeu_val2048", 2048},
	} {
		n := int(tc.val) + 1
		nTags := makeNTags(n)
		psV := poiSpec{Tags: []uint32{tc.val}}
		psVCopy := psV
		nTagsCopy := nTags
		write(edir, tc.name+".map",
			buildFile(
				defaultHS(singleZoom14, nTagsCopy, nil),
				func(si, tx, ty int) ([]byte, bool) {
					zic := singleZoom14[si]
					return buildTileBody(zic.min, zic.max, false, tx, ty,
						[][]poiSpec{{psVCopy}}, nil), false
				}))
	}

	// ─── encoding/ additional VBE-S boundary values ───────────────────────────
	fmt.Println("\n=== encoding/ additional VBE-S values ===")
	// New values: ±1, ±62, ±126, ±8190, ±8193, ±524287, ±524288
	// (0, ±63, ±64, ±8191, ±8192 already exist)
	for _, tc := range []struct {
		name string
		val  int32
	}{
		{"vbes_pos1", 1},
		{"vbes_neg1", -1},
		{"vbes_pos62", 62},
		{"vbes_neg62", -62},
		{"vbes_pos126", 126},
		{"vbes_neg126", -126},
		{"vbes_pos8190", 8190},
		{"vbes_neg8190", -8190},
		{"vbes_pos8193", 8193},
		{"vbes_neg8193", -8193},
		{"vbes_pos524287", 524287},
		{"vbes_neg524287", -524287},
		{"vbes_pos524288", 524288},
		{"vbes_neg524288", -524288},
	} {
		psS := poiSpec{LatDiff: tc.val, LonDiff: 0, Tags: []uint32{0}}
		psSCopy := psS
		write(edir, tc.name+".map",
			buildFile(
				defaultHS(singleZoom14, basePOITags, nil),
				func(si, tx, ty int) ([]byte, bool) {
					zic := singleZoom14[si]
					return buildTileBody(zic.min, zic.max, false, tx, ty,
						[][]poiSpec{{psSCopy}}, nil), false
				}))
	}

	// ─── encoding/ string length boundary values ──────────────────────────────
	fmt.Println("\n=== encoding/ string length boundary values ===")
	for _, n := range []int{0, 1, 63, 64, 127, 128, 200, 500} {
		nameStr := strings.Repeat("A", n)
		psStr := poiSpec{Tags: []uint32{0}, Name: nameStr}
		if n == 0 {
			psStr.Name = "" // has_name = false
		}
		psStrCopy := psStr
		write(edir, fmt.Sprintf("string_len_%d.map", n),
			buildFile(
				defaultHS(singleZoom14, basePOITags, nil),
				func(si, tx, ty int) ([]byte, bool) {
					zic := singleZoom14[si]
					return buildTileBody(zic.min, zic.max, false, tx, ty,
						[][]poiSpec{{psStrCopy}}, nil), false
				}))
	}

	// ─── way/ single-bit bitmaps ──────────────────────────────────────────────
	fmt.Println("\n=== way/ single-bit bitmaps ===")
	for bit := 0; bit < 16; bit++ {
		bm := uint16(1) << uint(bit)
		ws := waySpec{
			SubTileBitmap: bm,
			Tags:          []uint32{0},
			Blocks:        [][][2]int32{{{0, 0}, {1000, 0}, {500, 500}}},
		}
		makeOneWayFile(fmt.Sprintf("bitmap_bit%d.map", bit), ws)
	}

	// ─── way/ more node counts ────────────────────────────────────────────────
	fmt.Println("\n=== way/ more node counts ===")
	// Already have 1,2,3,10,100; add 4,5,6,7,8,9,15,20,50
	for _, n := range []int{4, 5, 6, 7, 8, 9, 15, 20, 50} {
		nodes := make([][2]int32, n)
		for i := range nodes {
			nodes[i] = [2]int32{int32(i * 100), 0}
		}
		ws := waySpec{
			SubTileBitmap: 0xffff,
			Tags:          []uint32{0},
			Blocks:        append([][][2]int32{}, nodes),
		}
		makeOneWayFile(fmt.Sprintf("nodes_%d.map", n), ws)
	}

	// ─── way/ more block counts for multipolygon ──────────────────────────────
	fmt.Println("\n=== way/ more multipolygon block counts ===")
	// Already have 2,3 blocks; add 4..10
	for n := 4; n <= 10; n++ {
		blocks := make([][][2]int32, n)
		for bi := 0; bi < n; bi++ {
			blocks[bi] = [][2]int32{
				{int32(bi * 100), 0}, {100, 0}, {0, 100}, {-100, 0},
			}
		}
		ws := waySpec{
			SubTileBitmap: 0xffff,
			Tags:          []uint32{0},
			Blocks:        blocks,
		}
		makeOneWayFile(fmt.Sprintf("multipolygon_%dblocks.map", n), ws)
	}

	// ─── poi/ more count stress tests ────────────────────────────────────────
	fmt.Println("\n=== poi/ more count stress tests ===")
	for _, n := range []int{200, 500, 1000} {
		nCopyPOI := n
		write(pdir, fmt.Sprintf("count_%d.map", n),
			buildFile(
				defaultHS(singleZoom14, basePOITags, nil),
				func(si, tx, ty int) ([]byte, bool) {
					zic := singleZoom14[si]
					pois := make([]poiSpec, nCopyPOI)
					for i := range pois {
						pois[i] = poiSpec{Tags: []uint32{0}, Name: fmt.Sprintf("POI %d", i)}
					}
					body := buildTileBody(zic.min, zic.max, false, tx, ty,
						[][]poiSpec{pois}, nil)
					return body, false
				}))
	}

	// ─── way/ more count stress tests ────────────────────────────────────────
	fmt.Println("\n=== way/ more count stress tests ===")
	for _, n := range []int{200, 500, 1000} {
		nCopyWay := n
		write(wdir, fmt.Sprintf("count_%d.map", n),
			buildFile(
				defaultHS(singleZoom14, nil, baseWayTags),
				func(si, tx, ty int) ([]byte, bool) {
					zic := singleZoom14[si]
					ways := make([]waySpec, nCopyWay)
					for i := range ways {
						ways[i] = waySpec{
							SubTileBitmap: 0xffff,
							Tags:          []uint32{0},
							Blocks:        triNodes,
						}
					}
					body := buildTileBody(zic.min, zic.max, false, tx, ty,
						nil, [][]waySpec{ways})
					return body, false
				}))
	}

	// ─── combo/ ───────────────────────────────────────────────────────────────
	fmt.Println("\n=== combo/ ===")
	cdir := dir + "/combo"
	mustMkdir(cdir)

	// multi-zoom subfile: base=14, min=12, max=16 = 5 zoom rows
	comboZics := []zicSpec{{base: 14, min: 12, max: 16}}

	// 1. poi_zoom0_way_zoom4.map: 1 POI at zi=0, 1 way at zi=4
	write(cdir, "poi_zoom0_way_zoom4.map",
		buildFile(
			defaultHS(comboZics, []string{"amenity=restaurant"}, []string{"highway=residential"}),
			func(si, tx, ty int) ([]byte, bool) {
				pois := make([][]poiSpec, 5)
				ways := make([][]waySpec, 5)
				pois[0] = []poiSpec{simplePOI()}
				ways[4] = []waySpec{simpleWay()}
				return buildTileBody(12, 16, false, tx, ty, pois, ways), false
			}))

	// 2. poi_zoom2_way_zoom2.map: 1 POI + 1 way at zi=2
	write(cdir, "poi_zoom2_way_zoom2.map",
		buildFile(
			defaultHS(comboZics, []string{"amenity=restaurant"}, []string{"highway=residential"}),
			func(si, tx, ty int) ([]byte, bool) {
				pois := make([][]poiSpec, 5)
				ways := make([][]waySpec, 5)
				pois[2] = []poiSpec{simplePOI()}
				ways[2] = []waySpec{simpleWay()}
				return buildTileBody(12, 16, false, tx, ty, pois, ways), false
			}))

	// 3. multi_poi_per_zoom.map: 1 POI at each of 5 zoom rows
	write(cdir, "multi_poi_per_zoom.map",
		buildFile(
			defaultHS(comboZics, []string{"amenity=restaurant"}, nil),
			func(si, tx, ty int) ([]byte, bool) {
				pois := make([][]poiSpec, 5)
				for zi := range pois {
					pois[zi] = []poiSpec{{Tags: []uint32{0}, Name: fmt.Sprintf("POI zoom%d", zi)}}
				}
				return buildTileBody(12, 16, false, tx, ty, pois, nil), false
			}))

	// 4. multi_way_per_zoom.map: 1 way at each of 5 zoom rows
	write(cdir, "multi_way_per_zoom.map",
		buildFile(
			defaultHS(comboZics, nil, []string{"highway=residential"}),
			func(si, tx, ty int) ([]byte, bool) {
				ways := make([][]waySpec, 5)
				for zi := range ways {
					ways[zi] = []waySpec{{
						SubTileBitmap: 0xffff,
						Tags:          []uint32{0},
						Name:          fmt.Sprintf("Road zoom%d", zi),
						Blocks:        [][][2]int32{{{0, 0}, {1000, 0}, {500, 500}}},
					}}
				}
				return buildTileBody(12, 16, false, tx, ty, nil, ways), false
			}))

	// 5. poi_and_way_all_zooms.map: 1 POI + 1 way at every zoom row
	write(cdir, "poi_and_way_all_zooms.map",
		buildFile(
			defaultHS(comboZics, []string{"amenity=restaurant"}, []string{"highway=residential"}),
			func(si, tx, ty int) ([]byte, bool) {
				pois := make([][]poiSpec, 5)
				ways := make([][]waySpec, 5)
				for zi := range pois {
					pois[zi] = []poiSpec{simplePOI()}
					ways[zi] = []waySpec{simpleWay()}
				}
				return buildTileBody(12, 16, false, tx, ty, pois, ways), false
			}))

	// 6. two_poi_tags_poi.map: POI with 2 tags, way with 2 tags
	write(cdir, "two_poi_tags_poi.map",
		buildFile(
			defaultHS(singleZoom14,
				[]string{"amenity=restaurant", "tourism=hotel"},
				[]string{"highway=residential", "landuse=forest"}),
			func(si, tx, ty int) ([]byte, bool) {
				pois := [][]poiSpec{{{Tags: []uint32{0, 1}, Name: "MultiTag POI"}}}
				ways := [][]waySpec{{{
					SubTileBitmap: 0xffff,
					Tags:          []uint32{0, 1},
					Name:          "MultiTag Way",
					Blocks:        [][][2]int32{{{0, 0}, {1000, 0}, {500, 500}}},
				}}}
				zic := singleZoom14[si]
				return buildTileBody(zic.min, zic.max, false, tx, ty, pois, ways), false
			}))

	// 7. debug_poi_way_multi_zoom.map: debug=true, POI at zi=0, way at zi=4
	{
		dbgHS := defaultHS(comboZics,
			[]string{"amenity=restaurant"}, []string{"highway=residential"})
		dbgHS.hasDebug = true
		write(cdir, "debug_poi_way_multi_zoom.map",
			buildFile(dbgHS, func(si, tx, ty int) ([]byte, bool) {
				pois := make([][]poiSpec, 5)
				ways := make([][]waySpec, 5)
				pois[0] = []poiSpec{simplePOI()}
				ways[4] = []waySpec{simpleWay()}
				return buildTileBody(12, 16, true, tx, ty, pois, ways), false
			}))
	}

	// 8. water_with_poi.map: tile is water=true with a POI body
	write(cdir, "water_with_poi.map",
		buildFile(
			defaultHS(singleZoom14, []string{"amenity=restaurant"}, nil),
			func(si, tx, ty int) ([]byte, bool) {
				zic := singleZoom14[si]
				pois := [][]poiSpec{{simplePOI()}}
				return buildTileBody(zic.min, zic.max, false, tx, ty, pois, nil), true
			}))

	// 9. empty_poi_table.map: POI table has 0 entries, way table has 3 entries
	write(cdir, "empty_poi_table.map",
		buildFile(
			defaultHS(singleZoom14, nil,
				[]string{"highway=residential", "landuse=forest", "waterway=river"}),
			func(si, tx, ty int) ([]byte, bool) {
				zic := singleZoom14[si]
				ways := [][]waySpec{{{
					SubTileBitmap: 0xffff,
					Tags:          []uint32{0, 1, 2},
					Blocks:        [][][2]int32{{{0, 0}, {1000, 0}, {500, 500}}},
				}}}
				return buildTileBody(zic.min, zic.max, false, tx, ty, nil, ways), false
			}))

	// 10. three_subfiles_with_content.map: 3 zoom intervals, each with content
	{
		threeZics := []zicSpec{
			{base: 7, min: 0, max: 7},
			{base: 12, min: 8, max: 12},
			{base: 14, min: 13, max: 21},
		}
		threeHS := defaultHS(threeZics,
			[]string{"amenity=restaurant"}, []string{"highway=residential"})
		write(cdir, "three_subfiles_with_content.map",
			buildFile(threeHS, func(si, tx, ty int) ([]byte, bool) {
				zic := threeZics[si]
				switch si {
				case 0: // si=0: 1 POI
					pois := [][]poiSpec{{simplePOI()}}
					return buildTileBody(zic.min, zic.max, false, tx, ty, pois, nil), false
				case 1: // si=1: 1 way
					ways := [][]waySpec{{simpleWay()}}
					return buildTileBody(zic.min, zic.max, false, tx, ty, nil, ways), false
				default: // si=2: 1 POI + 1 way
					pois := [][]poiSpec{{simplePOI()}}
					ways := [][]waySpec{{simpleWay()}}
					return buildTileBody(zic.min, zic.max, false, tx, ty, pois, ways), false
				}
			}))
	}

	// 11. poi_all_flags_combo.map: POI with all optional fields at all zoom rows
	write(cdir, "poi_all_flags_combo.map",
		buildFile(
			defaultHS(comboZics, []string{"amenity=restaurant"}, nil),
			func(si, tx, ty int) ([]byte, bool) {
				pois := make([][]poiSpec, 5)
				for zi := range pois {
					pois[zi] = []poiSpec{{
						Tags:         []uint32{0},
						Name:         "Full POI",
						HouseNumber:  "42",
						HasElevation: true,
						Elevation:    100,
						Layer:        int8(zi - 2),
					}}
				}
				return buildTileBody(12, 16, false, tx, ty, pois, nil), false
			}))

	// 12. way_all_flags_combo.map: way with all optional fields at all zoom rows
	write(cdir, "way_all_flags_combo.map",
		buildFile(
			defaultHS(comboZics, nil, []string{"highway=residential"}),
			func(si, tx, ty int) ([]byte, bool) {
				ways := make([][]waySpec, 5)
				for zi := range ways {
					ways[zi] = []waySpec{{
						SubTileBitmap:     0xffff,
						Tags:              []uint32{0},
						Name:              "Full Way",
						HouseNumber:       "5",
						Reference:         "A1",
						HasLabelPos:       true,
						LabelLat:          300,
						LabelLon:          200,
						ExplicitNumBlocks: true,
						Blocks:            [][][2]int32{{{0, 0}, {1000, 0}, {500, 500}}},
					}}
				}
				return buildTileBody(12, 16, false, tx, ty, nil, ways), false
			}))

	// 13. mixed_layer_poi_way.map: POI at layer=-3, way at layer=+3
	write(cdir, "mixed_layer_poi_way.map",
		buildFile(
			defaultHS(singleZoom14,
				[]string{"amenity=restaurant"}, []string{"highway=residential"}),
			func(si, tx, ty int) ([]byte, bool) {
				pois := [][]poiSpec{{{Tags: []uint32{0}, Layer: -3, Name: "Low POI"}}}
				ways := [][]waySpec{{{
					SubTileBitmap: 0xffff,
					Tags:          []uint32{0},
					Layer:         3,
					Name:          "High Way",
					Blocks:        [][][2]int32{{{0, 0}, {1000, 0}, {500, 500}}},
				}}}
				zic := singleZoom14[si]
				return buildTileBody(zic.min, zic.max, false, tx, ty, pois, ways), false
			}))

	// 14. poi_negative_coords.map: POI with negative lat/lon diffs
	write(cdir, "poi_negative_coords.map",
		buildFile(
			defaultHS(singleZoom14, []string{"amenity=restaurant"}, nil),
			func(si, tx, ty int) ([]byte, bool) {
				pois := [][]poiSpec{{{
					Tags:    []uint32{0},
					LatDiff: -500,
					LonDiff: -300,
					Name:    "Neg Coord POI",
				}}}
				zic := singleZoom14[si]
				return buildTileBody(zic.min, zic.max, false, tx, ty, pois, nil), false
			}))

	// 15. way_double_delta_multi_block.map: double-delta + multi-block
	write(cdir, "way_double_delta_multi_block.map",
		buildFile(
			defaultHS(singleZoom14, nil, []string{"highway=residential"}),
			func(si, tx, ty int) ([]byte, bool) {
				ways := [][]waySpec{{{
					SubTileBitmap: 0xffff,
					Tags:          []uint32{0},
					DoubleDelta:   true,
					Blocks: [][][2]int32{
						{{0, 0}, {1000, 0}, {0, 0}, {0, 0}},
						{{500, 500}, {500, 0}, {0, 0}},
					},
				}}}
				zic := singleZoom14[si]
				return buildTileBody(zic.min, zic.max, false, tx, ty, nil, ways), false
			}))

	// 16. poi_zero_elevation.map: POI with elevation=0 (has_elevation=true but value=0)
	write(cdir, "poi_zero_elevation.map",
		buildFile(
			defaultHS(singleZoom14, []string{"amenity=restaurant"}, nil),
			func(si, tx, ty int) ([]byte, bool) {
				pois := [][]poiSpec{{{
					Tags:         []uint32{0},
					HasElevation: true,
					Elevation:    0,
					Name:         "Sea Level",
				}}}
				zic := singleZoom14[si]
				return buildTileBody(zic.min, zic.max, false, tx, ty, pois, nil), false
			}))

	// 17. poi_negative_elevation.map: underground feature
	write(cdir, "poi_negative_elevation.map",
		buildFile(
			defaultHS(singleZoom14, []string{"amenity=restaurant"}, nil),
			func(si, tx, ty int) ([]byte, bool) {
				pois := [][]poiSpec{{{
					Tags:         []uint32{0},
					HasElevation: true,
					Elevation:    -100,
					Name:         "Underground",
				}}}
				zic := singleZoom14[si]
				return buildTileBody(zic.min, zic.max, false, tx, ty, pois, nil), false
			}))

	// 18. way_bitmap_zero.map: way with sub_tile_bitmap=0
	write(cdir, "way_bitmap_zero.map",
		buildFile(
			defaultHS(singleZoom14, nil, []string{"highway=residential"}),
			func(si, tx, ty int) ([]byte, bool) {
				ways := [][]waySpec{{{
					SubTileBitmap: 0x0000,
					Tags:          []uint32{0},
					Blocks:        [][][2]int32{{{0, 0}, {1000, 0}, {500, 500}}},
				}}}
				zic := singleZoom14[si]
				return buildTileBody(zic.min, zic.max, false, tx, ty, nil, ways), false
			}))

	// 19. many_tags_poi.map: POI + way each with 10 tags
	{
		manyPOITags := makeNTags(10)
		manyWayTags := makeNTags(10)
		write(cdir, "many_tags_poi.map",
			buildFile(
				defaultHS(singleZoom14, manyPOITags, manyWayTags),
				func(si, tx, ty int) ([]byte, bool) {
					pois := [][]poiSpec{{{Tags: makeNTagIDs(10)}}}
					ways := [][]waySpec{{{
						SubTileBitmap: 0xffff,
						Tags:          makeNTagIDs(10),
						Blocks:        [][][2]int32{{{0, 0}, {1000, 0}, {500, 500}}},
					}}}
					zic := singleZoom14[si]
					return buildTileBody(zic.min, zic.max, false, tx, ty, pois, ways), false
				}))
	}

	// 20. debug_all_zoom_rows.map: debug=true, data at every zoom row
	{
		dbgAllHS := defaultHS(comboZics,
			[]string{"amenity=restaurant"}, []string{"highway=residential"})
		dbgAllHS.hasDebug = true
		write(cdir, "debug_all_zoom_rows.map",
			buildFile(dbgAllHS, func(si, tx, ty int) ([]byte, bool) {
				pois := make([][]poiSpec, 5)
				ways := make([][]waySpec, 5)
				for zi := range pois {
					pois[zi] = []poiSpec{simplePOI()}
					ways[zi] = []waySpec{simpleWay()}
				}
				return buildTileBody(12, 16, true, tx, ty, pois, ways), false
			}))
	}

	// 21. poi_house_only.map: POI with only house number (no name, no elevation)
	write(cdir, "poi_house_only.map",
		buildFile(
			defaultHS(singleZoom14, []string{"amenity=restaurant"}, nil),
			func(si, tx, ty int) ([]byte, bool) {
				pois := [][]poiSpec{{{Tags: []uint32{0}, HouseNumber: "99"}}}
				zic := singleZoom14[si]
				return buildTileBody(zic.min, zic.max, false, tx, ty, pois, nil), false
			}))

	// 22. way_reference_only.map: way with only reference field
	write(cdir, "way_reference_only.map",
		buildFile(
			defaultHS(singleZoom14, nil, []string{"highway=residential"}),
			func(si, tx, ty int) ([]byte, bool) {
				ways := [][]waySpec{{{
					SubTileBitmap: 0xffff,
					Tags:          []uint32{0},
					Reference:     "M25",
					Blocks:        [][][2]int32{{{0, 0}, {1000, 0}, {500, 500}}},
				}}}
				zic := singleZoom14[si]
				return buildTileBody(zic.min, zic.max, false, tx, ty, nil, ways), false
			}))

	// 23. water_all_subfiles.map: every subfile tile is water
	write(cdir, "water_all_subfiles.map",
		buildFile(
			defaultHS(multiZoom, nil, nil),
			func(si, tx, ty int) ([]byte, bool) { return nil, true }))

	// 24. mixed_water_land.map: some tiles water, some land
	{
		mwZics := []zicSpec{{base: 14, min: 14, max: 14}}
		mwHS := headerSpec{
			minLat: bboxMinLat, minLon: 13360000,
			maxLat: bboxMaxLat, maxLon: 13380000,
			zoomIntervals: mwZics,
			poiTags:       nil,
			wayTags:       nil,
		}
		x0mw, _ := toXY(bboxMinLat, 13360000, 14)
		write(cdir, "mixed_water_land.map",
			buildFile(mwHS, func(si, tx, ty int) ([]byte, bool) {
				if tx == x0mw {
					return nil, true // water
				}
				return nil, false // absent/land
			}))
	}

	// ─── invalid/ additional files ────────────────────────────────────────────
	fmt.Println("\n=== invalid/ additional files ===")

	// Build a fresh minimal valid base (non-debug)
	minimalValid := buildFile(defaultHS(singleZoom14, nil, nil), noTiles)

	// K1: truncations at multiples of 4 from 4 to 80
	skipTruncAt := map[int]bool{19: true, 23: true, 27: true, 43: true, 50: true, 61: true}
	for n := 4; n <= 80; n += 4 {
		if skipTruncAt[n] {
			continue
		}
		if n <= len(minimalValid) {
			write(idir, fmt.Sprintf("truncated_at_%d.map.bad", n), clone(minimalValid[:n]))
		}
	}

	// K2: single-byte corruptions in the magic (bytes 1..19)
	for n := 1; n < 20; n++ {
		d := clone(minimalValid)
		d[n] ^= 0xff
		write(idir, fmt.Sprintf("bad_magic_byte%d.map.bad", n), d)
	}

	// K3: corrupt the bbox
	{
		d := clone(minimalValid)
		binary.BigEndian.PutUint32(d[44:48], 0x7FFFFFFF)
		write(idir, "bad_bbox_minlat_max.map.bad", d)
	}
	{
		d := clone(minimalValid)
		binary.BigEndian.PutUint32(d[52:56], 0x80000000)
		write(idir, "bad_bbox_maxlat_min.map.bad", d)
	}
	// Note: all-zero bbox (minLat=minLon=maxLat=maxLon=0) is a valid single-point
	// bbox at the equator/prime meridian. It lives in header/ as bbox_equator_point.
	{
		hs := headerSpec{
			minLat: 0, minLon: 0, maxLat: 0, maxLon: 0,
			zoomIntervals: []zicSpec{{base: 0, min: 0, max: 0}},
		}
		write(hdir, "bbox_equator_point.map", buildFile(hs, noTiles))
	}

	// K4: corrupt tile index entry offset to max 39-bit value
	{
		// Find the tile index in minimalValid: after header (24+headerSize), optional debug sig
		hdrSize := int(binary.BigEndian.Uint32(minimalValid[20:24]))
		subStart := 24 + hdrSize
		// no debug in minimalValid, so tile index starts right at subStart+0
		// (after the +++IndexStart+++ debug sig if debug, but minimalValid has no debug)
		tileIdxStart := subStart
		if tileIdxStart+5 <= len(minimalValid) {
			d := clone(minimalValid)
			// Set offset to max 39-bit value (0x7FFFFFFFFF)
			d[tileIdxStart] = 0x7F
			binary.BigEndian.PutUint32(d[tileIdxStart+1:], 0xFFFFFFFF)
			write(idir, "bad_tile_index_offset_overflow.map.bad", d)
		}
	}

	// K5: wrong file_size field values
	{
		actualSize := uint64(len(minimalValid))
		{
			d := clone(minimalValid)
			binary.BigEndian.PutUint64(d[28:36], actualSize+1000)
			write(idir, "bad_file_size_larger.map.bad", d)
		}
		{
			d := clone(minimalValid)
			if actualSize > 0 {
				binary.BigEndian.PutUint64(d[28:36], actualSize-1)
			}
			write(idir, "bad_file_size_smaller.map.bad", d)
		}
		{
			d := clone(minimalValid)
			binary.BigEndian.PutUint64(d[28:36], 0)
			write(idir, "bad_file_size_zero.map.bad", d)
		}
	}

	// ─── invalid/: out-of-bounds cases ───────────────────────────────────────
	fmt.Println("\n=== invalid/ (out-of-bounds) ===")

	// bad_vbeu_overflow: tile body starts with 5 continuation bytes → VbeU overflow
	// raw_reader.go triggers "overflow" after 5 bytes all with bit 7 set.
	{
		hs := defaultHS(singleZoom14, nil, nil)
		d := buildFile(hs, func(si, tx, ty int) ([]byte, bool) {
			// 5 bytes with continuation bit set, then a terminator: overflows VbeU
			return []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x00}, false
		})
		write(idir, "bad_vbeu_overflow.map.bad", d)
	}

	// bad_vbes_overflow: a POI lat_diff field encoded with 5 continuation bytes
	{
		hs := defaultHS(singleZoom14, []string{"amenity=restaurant"}, nil)
		d := buildFile(hs, func(si, tx, ty int) ([]byte, bool) {
			b := &buf{}
			b.vbeU(1) // num_pois
			b.vbeU(0) // num_ways
			b.vbeU(8) // first_way_offset (size of the malformed POI data below)
			// POI with overflowing VBE-S lat_diff (5 continuation bytes + terminator)
			b.raw([]byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x00}) // lat_diff overflow
			b.raw([]byte{0x00})                               // lon_diff (never reached)
			return b.data, false
		})
		write(idir, "bad_vbes_overflow.map.bad", d)
	}

	// bad_first_way_offset_large: first_way_offset >> actual POI data size.
	// The parser stores but doesn't validate first_way_offset; included for
	// future validation coverage.
	{
		hs := defaultHS(singleZoom14, []string{"amenity=restaurant"}, nil)
		d := buildFile(hs, func(si, tx, ty int) ([]byte, bool) {
			b := &buf{}
			b.vbeU(1)      // num_pois
			b.vbeU(0)      // num_ways
			b.vbeU(999999) // first_way_offset = huge (actual POI is ~10 bytes)
			b.raw(buildPOIBytes(simplePOI(), false, 0))
			return b.data, false
		})
		write(idir, "bad_first_way_offset_large.map.bad", d)
	}

	// bad_way_block_len_negative: way_data_size < bytes consumed by properties,
	// so block_len = way_data_size - consumed_so_far < 0.
	// Triggers the block_len < 0 check in tile_data_parser.go.
	{
		hs := defaultHS(singleZoom14, nil, []string{"highway=residential"})
		d := buildFile(hs, func(si, tx, ty int) ([]byte, bool) {
			b := &buf{}
			b.vbeU(0) // num_pois
			b.vbeU(1) // num_ways
			b.vbeU(0) // first_way_offset

			inner := &buf{}
			inner.u16(0xffff) // sub_tile_bitmap (2 bytes)
			inner.u8(0x50)    // layer=0, 0 tags (1 byte)
			inner.u8(0x00)    // flags: no optional fields (1 byte); consumed = 4 bytes total

			b.vbeU(1) // way_data_size = 1, but 4 bytes will be consumed → block_len = -3
			b.raw(inner.data)
			return b.data, false
		})
		write(idir, "bad_way_block_len_negative.map.bad", d)
	}

	// bad_num_way_blocks_large: num_way_blocks = 9999 but no block data follows.
	// Parser tries to read 9999 blocks → EOF.
	{
		hs := defaultHS(singleZoom14, nil, []string{"highway=residential"})
		d := buildFile(hs, func(si, tx, ty int) ([]byte, bool) {
			b := &buf{}
			b.vbeU(0) // num_pois
			b.vbeU(1) // num_ways
			b.vbeU(0) // first_way_offset

			inner := &buf{}
			inner.u16(0xffff) // sub_tile_bitmap
			inner.u8(0x50)    // layer=0, 0 tags
			inner.u8(0x08)    // flags: has_num_way_blocks (bit 3)
			inner.vbeU(9999)  // num_way_blocks = 9999 (no block data follows)

			b.vbeU(uint32(len(inner.data))) // way_data_size
			b.raw(inner.data)
			return b.data, false
		})
		write(idir, "bad_num_way_blocks_large.map.bad", d)
	}

	// bad_num_nodes_large: num_nodes = 9999 but no node data follows.
	// Parser reads num_nodes, then tries to read 9999 VBE-S pairs → EOF.
	{
		hs := defaultHS(singleZoom14, nil, []string{"highway=residential"})
		d := buildFile(hs, func(si, tx, ty int) ([]byte, bool) {
			b := &buf{}
			b.vbeU(0) // num_pois
			b.vbeU(1) // num_ways
			b.vbeU(0) // first_way_offset

			inner := &buf{}
			inner.u16(0xffff) // sub_tile_bitmap
			inner.u8(0x50)    // layer=0, 0 tags
			inner.u8(0x00)    // flags: single block implicit, single-delta
			inner.vbeU(1)     // num_coord_blocks = 1
			inner.vbeU(9999)  // num_nodes = 9999 (no node bytes follow)

			b.vbeU(uint32(len(inner.data))) // way_data_size
			b.raw(inner.data)
			return b.data, false
		})
		write(idir, "bad_num_nodes_large.map.bad", d)
	}

	// bad_string_len_overflow: POI name length field = 9999 but no string bytes follow.
	// Parser calls VbeString() → reads length 9999, then tries to read 9999 bytes → EOF.
	{
		hs := defaultHS(singleZoom14, []string{"amenity=restaurant"}, nil)
		d := buildFile(hs, func(si, tx, ty int) ([]byte, bool) {
			b := &buf{}
			b.vbeU(1) // num_pois
			b.vbeU(0) // num_ways

			poi := &buf{}
			poi.vbeS(0)    // lat_diff
			poi.vbeS(0)    // lon_diff
			poi.u8(0x50)   // layer=0, 0 tags
			poi.u8(0x80)   // flags: has_name
			poi.vbeU(9999) // name length = 9999 (no string bytes follow)

			b.vbeU(uint32(len(poi.data))) // first_way_offset
			b.raw(poi.data)
			return b.data, false
		})
		write(idir, "bad_string_len_overflow.map.bad", d)
	}

	// bad_zoom_base_outside_minmax: base zoom not in [min, max].
	// base=14 but min=15, max=21 → "15 <= 14 <= 21" validation error in parser.
	{
		hs := headerSpec{
			minLat: bboxMinLat, minLon: bboxMinLon,
			maxLat: bboxMaxLat, maxLon: bboxMaxLon,
			zoomIntervals: []zicSpec{{base: 14, min: 15, max: 21}},
		}
		write(idir, "bad_zoom_base_outside_minmax.map.bad", buildFile(hs, noTiles))
	}

	// bad_zoom_base_below_min: base=5 but min=7, max=14.
	{
		hs := defaultHS([]zicSpec{{base: 5, min: 7, max: 14}}, nil, nil)
		write(idir, "bad_zoom_base_below_min.map.bad", buildFile(hs, noTiles))
	}

	// bad_subfile_size_zero: sub-file size field = 0 in zoom interval config.
	// Scan for the base/min/max triple and zero its size field.
	{
		d := clone(buildFile(defaultHS(singleZoom14, nil, nil), noTiles))
		for i := 0; i <= len(d)-19; i++ {
			if d[i] == 14 && d[i+1] == 14 && d[i+2] == 14 {
				// i+3: 8-byte sub-file start position; i+11: 8-byte sub-file size
				binary.BigEndian.PutUint64(d[i+11:], 0)
				break
			}
		}
		write(idir, "bad_subfile_size_zero.map.bad", d)
	}

	// bad_tile_index_offset_backward: tile index entry points to offset 0
	// (the very start of the subfile), before the tile data segment.
	{
		base := buildFile(defaultHS(singleZoom14, nil, nil), func(si, tx, ty int) ([]byte, bool) {
			zic := singleZoom14[si]
			return emptyTileBody(zic.min, zic.max, false, tx, ty), false
		})
		d := clone(base)
		hdrSize := int(binary.BigEndian.Uint32(d[20:24]))
		subStart := 24 + hdrSize
		// No debug sig; tile index starts right at subStart.
		// Set the 5-byte tile index entry to offset 0 (points before tile data).
		d[subStart] = 0
		binary.BigEndian.PutUint32(d[subStart+1:], 0)
		write(idir, "bad_tile_index_offset_backward.map.bad", d)
	}

	// bad_tag_id_out_of_range: POI uses tag ID 5 but only tag ID 0 exists.
	// The parser stores tag IDs without bounds-checking; included as a future
	// validation target.
	{
		hs := defaultHS(singleZoom14, []string{"amenity=restaurant"}, nil) // 1 tag
		d := buildFile(hs, func(si, tx, ty int) ([]byte, bool) {
			zic := singleZoom14[si]
			ps := poiSpec{Tags: []uint32{5}} // tag ID 5 is out of range
			return buildTileBody(zic.min, zic.max, false, tx, ty,
				[][]poiSpec{{ps}}, nil), false
		})
		write(idir, "bad_tag_id_out_of_range.map.bad", d)
	}

	// bad_way_tag_id_out_of_range: way uses tag ID 10 but only ID 0 exists.
	{
		hs := defaultHS(singleZoom14, nil, []string{"highway=residential"}) // 1 tag
		d := buildFile(hs, func(si, tx, ty int) ([]byte, bool) {
			zic := singleZoom14[si]
			ws := waySpec{
				SubTileBitmap: 0xffff,
				Tags:          []uint32{10}, // out of range
				Blocks:        [][][2]int32{{{0, 0}, {1000, 0}, {500, 500}}},
			}
			return buildTileBody(zic.min, zic.max, false, tx, ty,
				nil, [][]waySpec{{ws}}), false
		})
		write(idir, "bad_way_tag_id_out_of_range.map.bad", d)
	}

	// bad_poi_layer_overflow: layer field encodes value > 15 in 4-bit slot.
	// The special byte high nibble stores (layer+5); value 16 → layer=11 which
	// is above the defined range (-5..+5). Parser stores it; future validation target.
	{
		hs := defaultHS(singleZoom14, []string{"amenity=restaurant"}, nil)
		d := buildFile(hs, func(si, tx, ty int) ([]byte, bool) {
			b := &buf{}
			b.vbeU(1) // num_pois
			b.vbeU(0) // num_ways

			poi := &buf{}
			poi.vbeS(0)
			poi.vbeS(0)
			poi.u8(0xF0)                  // high nibble = 15 → layer = 15-5 = 10 (above spec range)
			poi.u8(0x00)                  // flags
			b.vbeU(uint32(len(poi.data))) // first_way_offset
			b.raw(poi.data)
			return b.data, false
		})
		write(idir, "bad_poi_layer_overflow.map.bad", d)
	}

	fmt.Printf("\nGenerated %d files in testdata/\n", total)
}
