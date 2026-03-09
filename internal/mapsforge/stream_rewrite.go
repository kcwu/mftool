package mapsforge

import "fmt"

// tileRewriteState holds all scratch buffers needed by streamRewriteTile.
// Allocated once per worker goroutine and reused across tiles.
type tileRewriteState struct {
	poiW raw_writer
	wayW raw_writer
	out  raw_writer
	tmp  raw_writer // per-way scratch
}

func newTileRewriteState() *tileRewriteState {
	s := &tileRewriteState{}
	s.poiW.data = make([]byte, 0, 4096)
	s.wayW.data = make([]byte, 0, 16384)
	s.out.data = make([]byte, 0, 16384)
	s.tmp.data = make([]byte, 0, 512)
	return s
}

// streamRewriteTile remaps tag IDs in raw tile bytes with zero per-tile allocations.
// poiMap[oldID] = newID, wayMap[oldID] = newID.
// Elements whose tags map to tagNotFound are silently dropped (matching remapPOITags behavior).
// All scratch buffers come from rs (caller-owned, reused across calls).
// Returns a fresh copy on success.
func streamRewriteTile(src []byte, poiMap, wayMap []uint32, zic *ZoomIntervalConfig, hasDebug bool, x, y int, rs *tileRewriteState) ([]byte, error) {
	r := newRawReader(src)

	zooms := int(zic.max_zoom_level-zic.min_zoom_level) + 1
	var zoom_table [256]TileZoomTable
	var outNumPois, outNumWays [256]uint32

	if hasDebug {
		r.skipBytes(32) // TileStart signature
	}

	for zi := 0; zi < zooms; zi++ {
		zoom_table[zi].num_pois = r.VbeU()
		zoom_table[zi].num_ways = r.VbeU()
	}
	r.VbeU() // consume original first_way_offset (recalculated below)
	if r.err != nil {
		return nil, r.err
	}

	// --- Encode POI section into rs.poiW, using rs.tmp as per-POI scratch ---
	rs.poiW.data = rs.poiW.data[:0]
	for zi := 0; zi < zooms; zi++ {
		for i := uint32(0); i < zoom_table[zi].num_pois; i++ {
			rs.tmp.data = rs.tmp.data[:0]
			if hasDebug {
				if len(r.buf) < 32 {
					return nil, fmt.Errorf("truncated POI signature")
				}
				rs.tmp.data = append(rs.tmp.data, r.buf[:32]...)
				r.buf = r.buf[32:]
			}
			rs.tmp.VbeS(r.VbeS())
			rs.tmp.VbeS(r.VbeS())
			special := r.uint8()
			num_tag := int(special & 0xf)
			rs.tmp.uint8(special)
			skip := false
			for ti := 0; ti < num_tag; ti++ {
				id := r.VbeU()
				if int(id) < len(poiMap) {
					id = poiMap[id]
				}
				if id == tagNotFound {
					skip = true
					for ti2 := ti + 1; ti2 < num_tag; ti2++ {
						r.VbeU()
					}
					break
				}
				rs.tmp.VbeU(id)
			}
			flags := r.uint8()
			if skip {
				if flags>>7&1 != 0 {
					r.skipVbeString()
				}
				if flags>>6&1 != 0 {
					r.skipVbeString()
				}
				if flags>>5&1 != 0 {
					r.VbeS()
				}
				continue
			}
			rs.tmp.uint8(flags)
			if flags>>7&1 != 0 {
				rs.tmp.copyVbeString(r)
			}
			if flags>>6&1 != 0 {
				rs.tmp.copyVbeString(r)
			}
			if flags>>5&1 != 0 {
				rs.tmp.VbeS(r.VbeS())
			}
			rs.poiW.data = append(rs.poiW.data, rs.tmp.data...)
			outNumPois[zi]++
		}
	}
	if r.err != nil {
		return nil, r.err
	}

	// --- Encode way section into rs.wayW ---
	rs.wayW.data = rs.wayW.data[:0]
	for zi := 0; zi < zooms; zi++ {
		for i := uint32(0); i < zoom_table[zi].num_ways; i++ {
			var debugSig []byte
			if hasDebug {
				if len(r.buf) < 32 {
					return nil, fmt.Errorf("truncated Way signature")
				}
				debugSig = r.buf[:32]
				r.buf = r.buf[32:]
			}
			way_data_size := r.VbeU()
			startLen := len(r.buf)
			if r.err != nil {
				return nil, r.err
			}

			rs.tmp.data = rs.tmp.data[:0]
			rs.tmp.uint16(r.uint16()) // sub_tile_bitmap
			special := r.uint8()
			num_tag := int(special & 0xf)
			rs.tmp.uint8(special)
			skip := false
			for ti := 0; ti < num_tag; ti++ {
				id := r.VbeU()
				if int(id) < len(wayMap) {
					id = wayMap[id]
				}
				if id == tagNotFound {
					skip = true
					for ti2 := ti + 1; ti2 < num_tag; ti2++ {
						r.VbeU()
					}
					break
				}
				rs.tmp.VbeU(id)
			}
			flags := r.uint8()
			if skip {
				if flags>>7&1 != 0 {
					r.skipVbeString()
				}
				if flags>>6&1 != 0 {
					r.skipVbeString()
				}
				if flags>>5&1 != 0 {
					r.skipVbeString()
				}
				if flags>>4&1 != 0 {
					r.VbeS()
					r.VbeS()
				}
				if flags>>3&1 != 0 {
					r.VbeU()
				}
				consumed := startLen - len(r.buf)
				block_len := int(way_data_size) - consumed
				if block_len < 0 || block_len > len(r.buf) {
					return nil, fmt.Errorf("way block len out of range: %d (way_data_size=%d consumed=%d)", block_len, way_data_size, consumed)
				}
				r.buf = r.buf[block_len:]
				continue
			}
			rs.tmp.uint8(flags)
			if flags>>7&1 != 0 {
				rs.tmp.copyVbeString(r)
			}
			if flags>>6&1 != 0 {
				rs.tmp.copyVbeString(r)
			}
			if flags>>5&1 != 0 {
				rs.tmp.copyVbeString(r)
			}
			if flags>>4&1 != 0 {
				rs.tmp.VbeS(r.VbeS())
				rs.tmp.VbeS(r.VbeS())
			}
			if flags>>3&1 != 0 { // has_num_way_blocks
				rs.tmp.VbeU(r.VbeU())
			}
			// Copy coordinate block bytes directly.
			consumed := startLen - len(r.buf)
			block_len := int(way_data_size) - consumed
			if block_len < 0 || block_len > len(r.buf) {
				return nil, fmt.Errorf("way block len out of range: %d (way_data_size=%d consumed=%d)", block_len, way_data_size, consumed)
			}
			rs.tmp.data = append(rs.tmp.data, r.buf[:block_len]...)
			r.buf = r.buf[block_len:]

			if hasDebug {
				rs.wayW.data = append(rs.wayW.data, debugSig...)
			}
			rs.wayW.VbeU(uint32(len(rs.tmp.data)))
			rs.wayW.data = append(rs.wayW.data, rs.tmp.data...)
			outNumWays[zi]++
		}
	}
	if r.err != nil {
		return nil, r.err
	}

	// --- Assemble final output into rs.out ---
	rs.out.data = rs.out.data[:0]
	if hasDebug {
		rs.out.fixedString(fmt.Sprintf("###TileStart### %d,%d", x, y), 32)
	}
	for zi := 0; zi < zooms; zi++ {
		rs.out.VbeU(outNumPois[zi])
		rs.out.VbeU(outNumWays[zi])
	}
	rs.out.VbeU(uint32(len(rs.poiW.data))) // first_way_offset
	rs.out.data = append(rs.out.data, rs.poiW.data...)
	rs.out.data = append(rs.out.data, rs.wayW.data...)

	// Return a copy so callers can hold it across tile boundaries.
	result := make([]byte, len(rs.out.data))
	copy(result, rs.out.data)
	return result, nil
}

// baseZoomBytes holds per-zoom byte ranges extracted from a raw base tile.
type baseZoomBytes struct {
	numPois, numWays   uint32
	poiBytes, wayBytes []byte
}

// extractBaseZoomBytes extracts per-zoom byte sections from a raw tile without struct decode.
// Returns one entry per zoom level (len = zooms).
func extractBaseZoomBytes(src []byte, zic *ZoomIntervalConfig, hasDebug bool) ([]baseZoomBytes, error) {
	r := newRawReader(src)
	zooms := int(zic.max_zoom_level-zic.min_zoom_level) + 1

	if hasDebug {
		r.skipBytes(32) // TileStart
	}

	var zt [256]TileZoomTable
	for zi := 0; zi < zooms; zi++ {
		zt[zi].num_pois = r.VbeU()
		zt[zi].num_ways = r.VbeU()
	}
	r.VbeU() // first_way_offset
	if r.err != nil {
		return nil, r.err
	}

	result := make([]baseZoomBytes, zooms)

	// Extract per-zoom POI byte ranges.
	for zi := 0; zi < zooms; zi++ {
		result[zi].numPois = zt[zi].num_pois
		poiStart := len(src) - len(r.buf)
		for i := uint32(0); i < zt[zi].num_pois; i++ {
			if hasDebug {
				r.skipBytes(32)
			}
			r.VbeS()
			r.VbeS()
			special := r.uint8()
			num_tag := int(special & 0xf)
			for ti := 0; ti < num_tag; ti++ {
				r.VbeU()
			}
			flags := r.uint8()
			if flags>>7&1 != 0 {
				r.skipVbeString()
			}
			if flags>>6&1 != 0 {
				r.skipVbeString()
			}
			if flags>>5&1 != 0 {
				r.VbeS()
			}
		}
		result[zi].poiBytes = src[poiStart : len(src)-len(r.buf)]
	}

	// Extract per-zoom way byte ranges.
	for zi := 0; zi < zooms; zi++ {
		result[zi].numWays = zt[zi].num_ways
		wayStart := len(src) - len(r.buf)
		for i := uint32(0); i < zt[zi].num_ways; i++ {
			if hasDebug {
				r.skipBytes(32)
			}
			wds := r.VbeU() // way_data_size
			r.skipBytes(wds)
		}
		result[zi].wayBytes = src[wayStart : len(src)-len(r.buf)]
	}

	if r.err != nil {
		return nil, r.err
	}
	return result, nil
}

// writeTileFromZoomBlobs assembles a tile directly from per-zoom blobs without struct decode.
// All blobs must already be in the output namespace (no remapping needed).
// Uses rs.out as the output buffer; returns a fresh copy.
func writeTileFromZoomBlobs(blobs []*[]byte, hasDebug bool, x, y int, rs *tileRewriteState) ([]byte, error) {
	zooms := len(blobs)
	// Stack-allocated per-zoom metadata (zooms is small, ≤32 in practice).
	type zoomInfo struct {
		numPois, numWays   uint32
		poiBytes, wayBytes []byte
	}
	var infos [32]zoomInfo

	totalPoiBytes := 0
	for zi, blobp := range blobs {
		blob := *blobp
		r := newRawReader(blob)
		infos[zi].numPois = r.VbeU()
		infos[zi].numWays = r.VbeU()
		poiLen := r.VbeU()
		if r.err != nil || int(poiLen) > len(r.buf) {
			return nil, fmt.Errorf("bad zoom blob at zi=%d poiLen=%d bufLen=%d", zi, poiLen, len(r.buf))
		}
		infos[zi].poiBytes = r.buf[:poiLen]
		infos[zi].wayBytes = r.buf[poiLen:]
		totalPoiBytes += int(poiLen)
	}

	rs.out.data = rs.out.data[:0]
	if hasDebug {
		rs.out.fixedString(fmt.Sprintf("###TileStart### %d,%d", x, y), 32)
	}
	for zi := 0; zi < zooms; zi++ {
		rs.out.VbeU(infos[zi].numPois)
		rs.out.VbeU(infos[zi].numWays)
	}
	rs.out.VbeU(uint32(totalPoiBytes))
	for zi := 0; zi < zooms; zi++ {
		rs.out.data = append(rs.out.data, infos[zi].poiBytes...)
	}
	for zi := 0; zi < zooms; zi++ {
		rs.out.data = append(rs.out.data, infos[zi].wayBytes...)
	}

	result := make([]byte, len(rs.out.data))
	copy(result, rs.out.data)
	return result, nil
}

// copyVbeString reads a VbeString from r and appends it to w without allocating a string.
func (w *raw_writer) copyVbeString(r *raw_reader) {
	n := r.VbeU()
	if r.err != nil || int(n) > len(r.buf) {
		return
	}
	w.VbeU(n)
	w.data = append(w.data, r.buf[:n]...)
	r.buf = r.buf[n:]
}
