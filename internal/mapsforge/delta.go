package mapsforge

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"sync"

	"github.com/klauspost/compress/zstd"
)

const mfdMagic = "mfd\x04"

// mfdTileRecord holds per-tile delta data (LZ77 patches per changed zoom level).
type mfdTileRecord struct {
	si, x, y int
	flags    uint8    // 0x01=is_water, 0x02=tile_empty
	zoomMask uint32   // bitmask of changed zoom levels
	patches  [][]byte // per set bit in zoomMask (low→high): LZ77 patch bytes
}

// mfdFile holds the parsed content of one MFD file.
type mfdFile struct {
	header  Header
	poiMap  []uint32
	wayMap  []uint32
	records map[mfdTileKey]*mfdTileRecord
}

type mfdTileKey struct{ si, x, y int }

// CmdDelta generates a binary delta (MFD) between old and new maps.
func CmdDelta(oldPath, newPath, outputPath string, force bool) error {
	if !force {
		if _, err := os.Stat(outputPath); err == nil {
			return fmt.Errorf("output file %s already exists (use -f to overwrite)", outputPath)
		}
	}

	// Reduce GC frequency during tile processing: parsed tiles are short-lived
	// (uncached) so heap stays small; aggressive GC just adds overhead.
	oldGC := debug.SetGCPercent(400)
	defer debug.SetGCPercent(oldGC)

	p1, err := ParseFile(oldPath, false)
	if err != nil {
		return fmt.Errorf("open old: %w", err)
	}
	defer p1.Close()

	p2, err := ParseFile(newPath, false)
	if err != nil {
		return fmt.Errorf("open new: %w", err)
	}
	defer p2.Close()

	return writeMFD(p1, p2, outputPath)
}

func writeMFD(p1, p2 *MapsforgeParser, outputPath string) error {
	h1 := &p1.data.header
	h2 := &p2.data.header

	poiMap := buildTagMapByString(h1.poi_tags, h2.poi_tags)
	wayMap := buildTagMapByString(h1.way_tags, h2.way_tags)
	headerBytes := serializeMapforgeHeader(h2)

	records, err := collectDeltaRecords(p1, p2, poiMap, wayMap)
	if err != nil {
		return err
	}

	// Build records section.
	rRec := newRawWriter()
	for _, rec := range records {
		rRec.uint8(uint8(rec.si))
		rRec.VbeU(uint32(rec.x))
		rRec.VbeU(uint32(rec.y))
		rRec.uint8(rec.flags)
		if rec.flags&0x02 == 0 {
			rRec.VbeU(rec.zoomMask)
			for _, patch := range rec.patches {
				rRec.VbeU(uint32(len(patch)))
				rRec.data = append(rRec.data, patch...)
			}
		}
	}
	rRec.uint8(0xFF) // end-of-stream sentinel

	compressedRec, err := zstdCompress(rRec.data)
	if err != nil {
		return err
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer f.Close()

	w := newRawWriter()
	w.data = append(w.data, mfdMagic...)
	w.VbeU(uint32(len(headerBytes)))
	w.data = append(w.data, headerBytes...)
	w.VbeU(uint32(len(poiMap)))
	for _, id := range poiMap {
		w.VbeU(id)
	}
	w.VbeU(uint32(len(wayMap)))
	for _, id := range wayMap {
		w.VbeU(id)
	}
	w.VbeU(uint32(len(compressedRec)))
	w.data = append(w.data, compressedRec...)

	_, err = f.Write(w.Bytes())
	return err
}

// streamTilesEqual compares two raw tile byte slices for semantic equality after
// applying poiMap/wayMap tag remapping. Zero allocations — elements are read and
// compared in place. Returns false conservatively if element order differs or on
// any mismatch; the caller falls through to full parse in that case.
// b1 is from p1 (old map), b2 is from p2 (new map).
func streamTilesEqual(b1, b2 []byte, poiMap, wayMap []uint32, h1 *Header, zic *ZoomIntervalConfig) bool {
	if h1.has_debug {
		return false // debug signatures complicate in-place comparison
	}

	r1 := newRawReader(b1)
	r2 := newRawReader(b2)
	zooms := int(zic.max_zoom_level-zic.min_zoom_level) + 1

	// Use fixed-size stack arrays to avoid heap allocation.
	var numPOIs [256]uint32
	var numWays [256]uint32
	for zi := 0; zi < zooms; zi++ {
		numPOIs[zi] = r1.VbeU()
		n2 := r2.VbeU()
		if numPOIs[zi] != n2 {
			return false
		}
		numWays[zi] = r1.VbeU()
		n2 = r2.VbeU()
		if numWays[zi] != n2 {
			return false
		}
	}
	r1.VbeU() // first_way_offset — may differ; skip
	r2.VbeU()
	if r1.err != nil || r2.err != nil {
		return false
	}

	for zi := 0; zi < zooms; zi++ {
		for i := uint32(0); i < numPOIs[zi]; i++ {
			if !streamPOIEqual(r1, r2, poiMap) {
				return false
			}
		}
	}
	for zi := 0; zi < zooms; zi++ {
		for i := uint32(0); i < numWays[zi]; i++ {
			if !streamWayEqual(r1, r2, wayMap) {
				return false
			}
		}
	}
	return r1.err == nil && r2.err == nil
}

func streamPOIEqual(r1, r2 *raw_reader, poiMap []uint32) bool {
	if r1.VbeS() != r2.VbeS() { // lat
		return false
	}
	if r1.VbeS() != r2.VbeS() { // lon
		return false
	}
	sp1 := r1.uint8()
	sp2 := r2.uint8()
	if sp1>>4 != sp2>>4 { // layer
		return false
	}
	numTag := int(sp1 & 0xf)
	if int(sp2&0xf) != numTag {
		return false
	}
	for ti := 0; ti < numTag; ti++ {
		t1 := r1.VbeU()
		t2 := r2.VbeU()
		if int(t1) >= len(poiMap) {
			return false
		}
		if poiMap[t1] != t2 {
			return false
		}
	}
	fl1 := r1.uint8()
	fl2 := r2.uint8()
	if fl1 != fl2 {
		return false
	}
	if fl1>>7&1 != 0 { // has_name
		if r1.VbeString() != r2.VbeString() {
			return false
		}
	}
	if fl1>>6&1 != 0 { // has_house_number
		if r1.VbeString() != r2.VbeString() {
			return false
		}
	}
	if fl1>>5&1 != 0 { // has_elevation
		if r1.VbeS() != r2.VbeS() {
			return false
		}
	}
	return r1.err == nil && r2.err == nil
}

func streamWayEqual(r1, r2 *raw_reader, wayMap []uint32) bool {
	// way_data_size may differ due to tag-ID VbeU width changes; read and discard.
	sz1 := r1.VbeU()
	sz2 := r2.VbeU()
	if r1.err != nil || r2.err != nil {
		return false
	}
	// Compare content byte-by-byte after headers are validated.
	// We'll verify sz1 vs sz2 after reading the actual content.
	start1 := len(r1.buf)
	start2 := len(r2.buf)

	if r1.uint16() != r2.uint16() { // sub_tile_bitmap
		return false
	}
	sp1 := r1.uint8()
	sp2 := r2.uint8()
	if sp1>>4 != sp2>>4 { // layer
		return false
	}
	numTag := int(sp1 & 0xf)
	if int(sp2&0xf) != numTag {
		return false
	}
	for ti := 0; ti < numTag; ti++ {
		t1 := r1.VbeU()
		t2 := r2.VbeU()
		if int(t1) >= len(wayMap) {
			return false
		}
		if wayMap[t1] != t2 {
			return false
		}
	}
	fl1 := r1.uint8()
	fl2 := r2.uint8()
	if fl1 != fl2 {
		return false
	}
	if fl1>>7&1 != 0 { // has_name
		if r1.VbeString() != r2.VbeString() {
			return false
		}
	}
	if fl1>>6&1 != 0 { // has_house_number
		if r1.VbeString() != r2.VbeString() {
			return false
		}
	}
	if fl1>>5&1 != 0 { // has_reference
		if r1.VbeString() != r2.VbeString() {
			return false
		}
	}
	if fl1>>4&1 != 0 { // has_label_position
		if r1.VbeS() != r2.VbeS() || r1.VbeS() != r2.VbeS() {
			return false
		}
	}
	// Consume remaining block bytes from r1 and r2 (coordinates).
	// sz1/sz2 count bytes from sub_tile_bitmap onward.
	consumed1 := start1 - len(r1.buf)
	consumed2 := start2 - len(r2.buf)
	blockLen1 := int(sz1) - consumed1
	blockLen2 := int(sz2) - consumed2
	if blockLen1 < 0 || blockLen2 < 0 || blockLen1 != blockLen2 {
		return false
	}
	if len(r1.buf) < blockLen1 || len(r2.buf) < blockLen2 {
		return false
	}
	// Block bytes must be identical (coordinate encoding is unchanged).
	if !bytes.Equal(r1.buf[:blockLen1], r2.buf[:blockLen2]) {
		return false
	}
	r1.buf = r1.buf[blockLen1:]
	r2.buf = r2.buf[blockLen2:]
	return r1.err == nil && r2.err == nil
}

// lz77Index is a per-worker reusable hash-chain index for lz77Encode.
// Using linked lists avoids the per-key []int allocations of map[uint32][]int.
type lz77Index struct {
	// head maps a 3-gram hash to the most-recent ref position with that hash (-1 = none).
	// Size is a power of two; we mask the 24-bit 3-gram key to fit.
	head [1 << 18]int32
	// next[i] is the previous ref position with the same hash as position i (-1 = end).
	next []int32
}

// collectDeltaRecords generates delta records for all tiles where p1 and p2 differ.
func collectDeltaRecords(p1, p2 *MapsforgeParser, poiMap, wayMap []uint32) ([]*mfdTileRecord, error) {
	h2 := &p2.data.header

	type deltaResult struct {
		rec *mfdTileRecord
		err error
	}
	type job struct {
		si, x, y int
		resCh    chan deltaResult
	}

	numWorkers := runtime.NumCPU()
	jobs := make(chan job, numWorkers*4)

	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mw := &MapsforgeWriter{}
			idx := new(lz77Index)
			for i := range idx.head {
				idx.head[i] = -1
			}
			for j := range jobs {
				rec, err := computeDeltaRecord(p1, p2, j.si, j.x, j.y, poiMap, wayMap, mw, idx)
				j.resCh <- deltaResult{rec, err}
			}
		}()
	}

	resultQueue := make([]chan deltaResult, 0, numWorkers*4)
	var records []*mfdTileRecord
	var firstErr error

	dispatch := func(si, x, y int) {
		resCh := make(chan deltaResult, 1)
		resultQueue = append(resultQueue, resCh)
		jobs <- job{si: si, x: x, y: y, resCh: resCh}
	}

	drain := func(n int) {
		for i := 0; i < n && len(resultQueue) > 0; i++ {
			res := <-resultQueue[0]
			resultQueue = resultQueue[1:]
			if res.err != nil && firstErr == nil {
				firstErr = res.err
			} else if res.rec != nil {
				records = append(records, res.rec)
			}
		}
	}

	for si := 0; si < len(h2.zoom_interval); si++ {
		zic := &h2.zoom_interval[si]
		baseZoom := zic.base_zoom_level
		x, bigY := h2.min.ToXY(baseZoom)
		bigX, y := h2.max.ToXY(baseZoom)

		for ty := y; ty <= bigY; ty++ {
			for tx := x; tx <= bigX; tx++ {
				if firstErr != nil {
					goto done
				}
				dispatch(si, tx, ty)
				if len(resultQueue) >= numWorkers*4 {
					drain(1)
				}
			}
		}
	}
done:
	close(jobs)
	wg.Wait()
	drain(len(resultQueue))

	if firstErr != nil {
		return nil, firstErr
	}

	var out []*mfdTileRecord
	for _, r := range records {
		if r != nil {
			out = append(out, r)
		}
	}
	return out, nil
}

// computeDeltaRecord computes LZ77 patches for one tile, or returns nil if unchanged.
func computeDeltaRecord(p1, p2 *MapsforgeParser, si, x, y int, poiMap, wayMap []uint32, mw *MapsforgeWriter, idx *lz77Index) (*mfdTileRecord, error) {
	b1, err := p1.GetRawTileBytes(si, x, y)
	if err != nil {
		return nil, err
	}
	b2, err := p2.GetRawTileBytes(si, x, y)
	if err != nil {
		return nil, err
	}

	idx2 := p2.GetTileIndex(si, x, y)
	var isWater2 bool
	if idx2 != nil {
		isWater2 = idx2.IsWater
	}
	idx1 := p1.GetTileIndex(si, x, y)
	var isWater1 bool
	if idx1 != nil {
		isWater1 = idx1.IsWater
	}

	if idx2 == nil {
		return nil, nil
	}

	if b1 == nil && b2 == nil {
		if isWater1 == isWater2 {
			return nil, nil
		}
		flags := uint8(0x02)
		if isWater2 {
			flags |= 0x01
		}
		return &mfdTileRecord{si: si, x: x, y: y, flags: flags}, nil
	}

	if b2 == nil {
		if b1 == nil && isWater1 == isWater2 {
			return nil, nil
		}
		flags := uint8(0x02)
		if isWater2 {
			flags |= 0x01
		}
		return &mfdTileRecord{si: si, x: x, y: y, flags: flags}, nil
	}

	if b1 != nil && isIdentityMapping(poiMap) && isIdentityMapping(wayMap) && bytes.Equal(b1, b2) {
		return nil, nil
	}

	zic := &p2.data.header.zoom_interval[si]

	// Streaming fast-path: compare tiles element-by-element applying tag remapping,
	// with zero allocations. Catches semantically-unchanged tiles even when byte
	// representations differ due to tag ID renumbering. Conservative: returns false
	// if element order differs (falls through to full parse).
	if b1 != nil && isWater1 == isWater2 && streamTilesEqual(b1, b2, poiMap, wayMap, &p1.data.header, zic) {
		return nil, nil
	}
	zooms := int(zic.max_zoom_level-zic.min_zoom_level) + 1

	// Use uncached parse: delta processes each tile once, so caching wastes memory.
	td2, err := p2.GetTileDataUncached(si, x, y)
	if err != nil {
		return nil, err
	}

	var td1 *TileData
	if b1 != nil {
		td1, err = p1.GetTileDataUncached(si, x, y)
		if err != nil {
			return nil, err
		}
	}

	var zoomMask uint32
	var patches [][]byte

	for zi := 0; zi < zooms; zi++ {
		// Build reference: p1 zoom level data remapped to p2 tag IDs, normalized.
		var refBlob []byte
		if td1 != nil {
			remappedPOIs := remapPOITags(td1.poi_data[zi], poiMap)
			remappedWays := remapWayTags(td1.way_data[zi], wayMap)
			normalizeZoomLevel(remappedPOIs, remappedWays)
			refBlob = encodeZoomBlob(mw, remappedPOIs, remappedWays)
		}

		// Build target: p2 zoom level data, normalized.
		// Since td2 is uncached (we own it), sort tag_ids in-place — no deep-copy needed.
		normalizeZoomLevel(td2.poi_data[zi], td2.way_data[zi])
		targetBlob := encodeZoomBlob(mw, td2.poi_data[zi], td2.way_data[zi])

		if bytes.Equal(refBlob, targetBlob) {
			continue
		}

		zoomMask |= 1 << uint(zi)
		patches = append(patches, idx.encode(targetBlob, refBlob))
	}

	if zoomMask == 0 && isWater1 == isWater2 {
		return nil, nil
	}

	var flags uint8
	if isWater2 {
		flags |= 0x01
	}

	return &mfdTileRecord{
		si:       si,
		x:        x,
		y:        y,
		flags:    flags,
		zoomMask: zoomMask,
		patches:  patches,
	}, nil
}

// encode encodes target as an LZ77 patch against ref using hash chains.
// Op format: header VbeU; even=COPY (len=(hdr>>1)+1, offset VbeU); odd=INSERT (len=(hdr>>1)+1, literals).
// The index reuses its head table across calls (cleared after each use) to avoid allocations.
func (idx *lz77Index) encode(target, ref []byte) []byte {
	const mask = uint32(len(idx.head) - 1)
	const maxChainDepth = 128

	// Grow next slice to cover ref without reallocation when possible.
	if cap(idx.next) < len(ref) {
		idx.next = make([]int32, len(ref), len(ref)*2)
	}
	idx.next = idx.next[:len(ref)]

	// Build hash chains over ref.
	for i := 0; i+3 <= len(ref); i++ {
		key := (uint32(ref[i]) | uint32(ref[i+1])<<8 | uint32(ref[i+2])<<16) & mask
		idx.next[i] = idx.head[key]
		idx.head[key] = int32(i)
	}

	w := newRawWriter()
	insertStart := 0

	flushInsert := func(end int) {
		n := end - insertStart
		if n == 0 {
			return
		}
		w.VbeU(uint32(n-1)<<1 | 1) // odd = INSERT
		w.data = append(w.data, target[insertStart:end]...)
	}

	pos := 0
	for pos < len(target) {
		bestLen := 0
		bestOff := 0
		if pos+3 <= len(target) {
			key := (uint32(target[pos]) | uint32(target[pos+1])<<8 | uint32(target[pos+2])<<16) & mask
			depth := 0
			for rpos := idx.head[key]; rpos >= 0 && depth < maxChainDepth; rpos = idx.next[rpos] {
				depth++
				ri := int(rpos)
				maxL := len(target) - pos
				if rem := len(ref) - ri; rem < maxL {
					maxL = rem
				}
				l := 0
				for l < maxL && target[pos+l] == ref[ri+l] {
					l++
				}
				if l > bestLen {
					bestLen = l
					bestOff = ri
				}
			}
		}
		if bestLen >= 3 {
			flushInsert(pos)
			insertStart = pos + bestLen
			w.VbeU(uint32(bestLen-1) << 1) // even = COPY
			w.VbeU(uint32(bestOff))
			pos += bestLen
		} else {
			pos++
		}
	}
	flushInsert(len(target))

	// Clear only the head slots we wrote, so the table is ready for the next call.
	for i := 0; i+3 <= len(ref); i++ {
		key := (uint32(ref[i]) | uint32(ref[i+1])<<8 | uint32(ref[i+2])<<16) & mask
		idx.head[key] = -1
	}

	return w.data
}

// lz77Decode applies an LZ77 patch against ref to reconstruct the target.
func lz77Decode(patch, ref []byte) ([]byte, error) {
	r := newRawReader(patch)
	var out []byte
	for r.err == nil && len(r.buf) > 0 {
		header := r.VbeU()
		length := int(header>>1) + 1
		if header&1 == 0 {
			// COPY op
			offset := int(r.VbeU())
			if r.err != nil {
				break
			}
			if offset+length > len(ref) {
				return nil, fmt.Errorf("lz77: copy [%d:%d] out of ref range %d", offset, offset+length, len(ref))
			}
			out = append(out, ref[offset:offset+length]...)
		} else {
			// INSERT op
			if r.err != nil {
				break
			}
			if len(r.buf) < length {
				return nil, fmt.Errorf("lz77: insert truncated: have %d need %d", len(r.buf), length)
			}
			out = append(out, r.buf[:length]...)
			r.buf = r.buf[length:]
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	return out, nil
}

// zstdCompress compresses data using zstd BestCompression.
func zstdCompress(data []byte) ([]byte, error) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		return nil, err
	}
	compressed := enc.EncodeAll(data, nil)
	enc.Close()
	return compressed, nil
}

// zstdDecompress decompresses zstd-compressed data.
func zstdDecompress(compressed []byte) ([]byte, error) {
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	defer dec.Close()
	return dec.DecodeAll(compressed, nil)
}

// remapPOITagsInPlace remaps tag IDs of pois in-place, compacting out elements
// with any tag mapping to tagNotFound. Safe when the caller owns the slice.
func remapPOITagsInPlace(pois *[]POIData, mapping []uint32) {
	if len(mapping) == 0 || isIdentityMapping(mapping) {
		return
	}
	dst := 0
	for i := range *pois {
		skip := false
		for j, t := range (*pois)[i].tag_id {
			if int(t) >= len(mapping) {
				skip = true
				break
			}
			mapped := mapping[t]
			if mapped == tagNotFound {
				skip = true
				break
			}
			(*pois)[i].tag_id[j] = mapped
		}
		if !skip {
			(*pois)[dst] = (*pois)[i]
			dst++
		}
	}
	*pois = (*pois)[:dst]
}

// remapWayTagsInPlace remaps tag IDs of ways in-place, compacting out elements
// with any tag mapping to tagNotFound. Safe when the caller owns the slice.
func remapWayTagsInPlace(ways *[]WayProperties, mapping []uint32) {
	if len(mapping) == 0 || isIdentityMapping(mapping) {
		return
	}
	dst := 0
	for i := range *ways {
		skip := false
		for j, t := range (*ways)[i].tag_id {
			if int(t) >= len(mapping) {
				skip = true
				break
			}
			mapped := mapping[t]
			if mapped == tagNotFound {
				skip = true
				break
			}
			(*ways)[i].tag_id[j] = mapped
		}
		if !skip {
			(*ways)[dst] = (*ways)[i]
			dst++
		}
	}
	*ways = (*ways)[:dst]
}

// remapPOITags returns a copy of pois with tag IDs remapped.
// Elements with any tag mapping to tagNotFound are dropped.
func remapPOITags(pois []POIData, mapping []uint32) []POIData {
	if len(mapping) == 0 || isIdentityMapping(mapping) {
		return pois
	}
	result := make([]POIData, 0, len(pois))
	for _, p := range pois {
		newTags := make([]uint32, len(p.tag_id))
		skip := false
		for j, t := range p.tag_id {
			mapped := mapping[t]
			if mapped == tagNotFound {
				skip = true
				break
			}
			newTags[j] = mapped
		}
		if skip {
			continue
		}
		np := p
		np.tag_id = newTags
		result = append(result, np)
	}
	return result
}

// remapWayTags returns a copy of ways with tag IDs remapped.
// Elements with any tag mapping to tagNotFound are dropped.
func remapWayTags(ways []WayProperties, mapping []uint32) []WayProperties {
	if len(mapping) == 0 || isIdentityMapping(mapping) {
		return ways
	}
	result := make([]WayProperties, 0, len(ways))
	for _, w := range ways {
		newTags := make([]uint32, len(w.tag_id))
		skip := false
		for j, t := range w.tag_id {
			mapped := mapping[t]
			if mapped == tagNotFound {
				skip = true
				break
			}
			newTags[j] = mapped
		}
		if skip {
			continue
		}
		nw := w
		nw.tag_id = newTags
		result = append(result, nw)
	}
	return result
}

// normalizeZoomLevel sorts tag_ids within each element and sorts elements.
func normalizeZoomLevel(pois []POIData, ways []WayProperties) {
	for i := range pois {
		sort.Sort(Uint32Slice(pois[i].tag_id))
	}
	sort.Sort(CmpByPOIData(pois))
	for i := range ways {
		sort.Sort(Uint32Slice(ways[i].tag_id))
	}
	sort.Sort(CmpByWayData(ways))
}

// encodeZoomBlob encodes one zoom level's POI and way data as a zoom blob.
func encodeZoomBlob(mw *MapsforgeWriter, pois []POIData, ways []WayProperties) []byte {
	w := newRawWriter()
	w.VbeU(uint32(len(pois)))
	w.VbeU(uint32(len(ways)))

	poiW := newRawWriter()
	for i := range pois {
		mw.writePOIData(poiW, &pois[i], i)
	}

	wayW := newRawWriter()
	for i := range ways {
		mw.writeWayProperties(wayW, &ways[i], i)
	}

	w.VbeU(uint32(len(poiW.data)))
	w.data = append(w.data, poiW.data...)
	w.data = append(w.data, wayW.data...)
	return w.data
}

// decodeZoomBlob decodes a zoom blob into POI and way slices.
func decodeZoomBlob(blob []byte) ([]POIData, []WayProperties, error) {
	r := newRawReader(blob)
	numPois := r.VbeU()
	numWays := r.VbeU()
	r.VbeU() // poi_bytes_len (skip)
	if r.err != nil {
		return nil, nil, r.err
	}

	pois := make([]POIData, numPois)
	for i := range pois {
		if err := parsePOIDataRaw(r, &pois[i]); err != nil {
			return nil, nil, fmt.Errorf("poi %d: %w", i, err)
		}
	}
	ways := make([]WayProperties, numWays)
	for i := range ways {
		if err := parseWayPropertiesRaw(r, &ways[i]); err != nil {
			return nil, nil, fmt.Errorf("way %d: %w", i, err)
		}
	}
	return pois, ways, r.err
}

// parsePOIDataRaw parses a POI from a raw reader (no debug signatures).
func parsePOIDataRaw(r *raw_reader, pd *POIData) error {
	pd.LatLon.lat = r.VbeS()
	pd.LatLon.lon = r.VbeS()
	special := r.uint8()
	pd.layer = int8(special>>4) - 5
	numTag := int(special & 0xf)
	pd.tag_id = make([]uint32, numTag)
	for ti := range pd.tag_id {
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

// parseWayPropertiesRaw parses a WayProperties from a raw reader (no debug signatures).
func parseWayPropertiesRaw(r *raw_reader, wp *WayProperties) error {
	wayDataSize := r.VbeU()
	startLen := len(r.buf)

	wp.sub_tile_bitmap = r.uint16()
	special := r.uint8()
	wp.layer = int8(special>>4) - 5
	numTag := int(special & 0xf)
	wp.tag_id = make([]uint32, numTag)
	for ti := range wp.tag_id {
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
		wp.label_position = LatLon{r.VbeS(), r.VbeS()}
	}
	if wp.has_num_way_blocks {
		wp.num_way_block = r.VbeU()
	} else {
		wp.num_way_block = 1
	}
	// Capture the raw block bytes before parsing — used by writeWayProperties fast path.
	blockBytesStart := r.buf
	wp.block = make([]WayData, wp.num_way_block)
	for bi := uint32(0); bi < wp.num_way_block; bi++ {
		numWay := r.VbeU()
		wp.block[bi].data = make([][]LatLon, numWay)
		for wi := uint32(0); wi < numWay; wi++ {
			numNode := r.VbeU()
			wp.block[bi].data[wi] = make([]LatLon, numNode)
			for ni := uint32(0); ni < numNode; ni++ {
				wp.block[bi].data[wi][ni] = LatLon{r.VbeS(), r.VbeS()}
			}
		}
	}
	// Store raw block bytes so writeWayProperties can skip re-encoding coordinates.
	wp.encodedBlocks = blockBytesStart[:len(blockBytesStart)-len(r.buf)]
	consumed := startLen - len(r.buf)
	if r.err == nil && uint32(consumed) != wayDataSize {
		return fmt.Errorf("way_data_size mismatch: expected %d, consumed %d", wayDataSize, consumed)
	}
	return r.err
}

// serializeMapforgeHeader serializes a Header into mapsforge binary format.
func serializeMapforgeHeader(h *Header) []byte {
	rw := newRawWriter()
	rw.fixedString(mapsforge_file_magic, 20)
	rw.uint32(0) // header_size placeholder
	rw.uint32(h.file_version)
	rw.uint64(h.file_size)
	rw.uint64(h.creation_date)
	rw.int32(h.min.lat)
	rw.int32(h.min.lon)
	rw.int32(h.max.lat)
	rw.int32(h.max.lon)
	rw.uint16(h.tile_size)
	rw.VbeString(h.projection)

	var flags uint8
	if h.has_debug {
		flags |= 0x80
	}
	if h.has_map_start {
		flags |= 0x40
	}
	if h.has_start_zoom {
		flags |= 0x20
	}
	if h.has_language_preference {
		flags |= 0x10
	}
	if h.has_comment {
		flags |= 0x08
	}
	if h.has_created_by {
		flags |= 0x04
	}
	rw.uint8(flags)

	if h.has_map_start {
		rw.int32(h.start.lat)
		rw.int32(h.start.lon)
	}
	if h.has_start_zoom {
		rw.uint8(h.start_zoom)
	}
	if h.has_language_preference {
		rw.VbeString(h.language_preference)
	}
	if h.has_comment {
		rw.VbeString(h.comment)
	}
	if h.has_created_by {
		rw.VbeString(h.created_by)
	}

	rw.uint16(uint16(len(h.poi_tags)))
	for _, tag := range h.poi_tags {
		rw.VbeString(tag)
	}
	rw.uint16(uint16(len(h.way_tags)))
	for _, tag := range h.way_tags {
		rw.VbeString(tag)
	}

	rw.uint8(uint8(len(h.zoom_interval)))
	for _, zic := range h.zoom_interval {
		rw.uint8(zic.base_zoom_level)
		rw.uint8(zic.min_zoom_level)
		rw.uint8(zic.max_zoom_level)
		rw.uint64(zic.pos)
		rw.uint64(zic.size)
	}

	headerSize := uint32(len(rw.data) - 24)
	binary.BigEndian.PutUint32(rw.data[20:24], headerSize)
	return rw.data
}

// tagNotFound is a sentinel for tag IDs that don't exist in the target map.
const tagNotFound = ^uint32(0)

// buildTagMapByString builds a mapping from old tag indices to new tag indices by string.
// Tags missing from newTags are mapped to tagNotFound.
func buildTagMapByString(oldTags, newTags []string) []uint32 {
	newIdx := make(map[string]uint32, len(newTags))
	for i, s := range newTags {
		newIdx[s] = uint32(i)
	}
	mapping := make([]uint32, len(oldTags))
	for i, s := range oldTags {
		if idx, ok := newIdx[s]; ok {
			mapping[i] = idx
		} else {
			mapping[i] = tagNotFound
		}
	}
	return mapping
}

// hasTagNotFound returns true if any entry in m equals tagNotFound.
func hasTagNotFound(m []uint32) bool {
	for _, v := range m {
		if v == tagNotFound {
			return true
		}
	}
	return false
}

// isIdentityMapping returns true if mapping[i]==i for all i.
func isIdentityMapping(m []uint32) bool {
	for i, v := range m {
		if uint32(i) != v {
			return false
		}
	}
	return true
}
