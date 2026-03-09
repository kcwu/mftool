package mapsforge

import (
	"bufio"
	"fmt"
	"io"
	"math/bits"
	"os"
	"runtime"
	"runtime/debug"
)

// CmdApply applies one or more MFD delta files to a base map to produce an output map.
func CmdApply(basePath string, deltaFiles []string, outputPath string, force bool) error {
	if !force {
		if _, err := os.Stat(outputPath); err == nil {
			return fmt.Errorf("output file %s already exists (use -f to overwrite)", outputPath)
		}
	}

	oldGC := debug.SetGCPercent(400)
	defer debug.SetGCPercent(oldGC)

	base, err := ParseFile(basePath, false)
	if err != nil {
		return fmt.Errorf("open base: %w", err)
	}
	defer base.Close()

	deltas := make([]*mfdFile, len(deltaFiles))
	for i, path := range deltaFiles {
		deltas[i], err = loadMFD(path)
		if err != nil {
			return fmt.Errorf("load delta %s: %w", path, err)
		}
	}

	outHeader := deltas[len(deltas)-1].header
	return applyDeltas(base, deltas, &outHeader, outputPath)
}

// loadMFD reads and parses an MFD file (mfd\x04 format).
func loadMFD(path string) (*mfdFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	r := newRawReader(data)

	magic := r.fixedString(4)
	if r.err != nil {
		return nil, fmt.Errorf("read magic: %w", r.err)
	}
	if magic != mfdMagic {
		return nil, fmt.Errorf("bad magic %q, expected %q", magic, mfdMagic)
	}

	headerLen := r.VbeU()
	if r.err != nil {
		return nil, r.err
	}
	if uint32(len(r.buf)) < headerLen {
		return nil, fmt.Errorf("truncated header")
	}
	headerBytes := r.buf[:headerLen]
	r.buf = r.buf[headerLen:]

	mf := &mfdFile{}
	hp := &MapsforgeParser{file_content: headerBytes}
	hp.reader = newRawReader(headerBytes)
	if err := hp.ParseHeader(&mf.header); err != nil {
		return nil, fmt.Errorf("parse embedded header: %w", err)
	}

	poiCount := r.VbeU()
	mf.poiMap = make([]uint32, poiCount)
	for i := range mf.poiMap {
		mf.poiMap[i] = r.VbeU()
	}
	wayCount := r.VbeU()
	mf.wayMap = make([]uint32, wayCount)
	for i := range mf.wayMap {
		mf.wayMap[i] = r.VbeU()
	}
	if r.err != nil {
		return nil, r.err
	}

	// Read and decompress records section.
	recZstdLen := r.VbeU()
	if r.err != nil {
		return nil, r.err
	}
	if uint32(len(r.buf)) < recZstdLen {
		return nil, fmt.Errorf("truncated records section")
	}
	recRaw, err := zstdDecompress(r.buf[:recZstdLen])
	if err != nil {
		return nil, fmt.Errorf("decompress records: %w", err)
	}

	// Parse tile records.
	rRec := newRawReader(recRaw)
	mf.records = make(map[mfdTileKey]*mfdTileRecord)
	for rRec.err == nil && len(rRec.buf) > 0 {
		si := rRec.uint8()
		if si == 0xFF {
			break
		}
		x := int(rRec.VbeU())
		y := int(rRec.VbeU())
		flags := rRec.uint8()
		if rRec.err != nil {
			break
		}
		rec := &mfdTileRecord{si: int(si), x: x, y: y, flags: flags}
		if flags&0x02 == 0 {
			rec.zoomMask = rRec.VbeU()
			mask := rec.zoomMask
			for mask != 0 {
				bit := mask & uint32(-int32(mask))
				mask &^= bit
				patchLen := int(rRec.VbeU())
				if rRec.err != nil {
					break
				}
				if len(rRec.buf) < patchLen {
					rRec.err = fmt.Errorf("truncated patch for tile si=%d x=%d y=%d", si, x, y)
					break
				}
				patch := make([]byte, patchLen)
				copy(patch, rRec.buf[:patchLen])
				rRec.buf = rRec.buf[patchLen:]
				rec.patches = append(rec.patches, patch)
			}
		}
		key := mfdTileKey{rec.si, rec.x, rec.y}
		mf.records[key] = rec
	}

	if rRec.err != nil {
		return nil, rRec.err
	}
	return mf, nil
}

// perTileState tracks the current zoom-blob state for one tile across a delta chain.
type perTileState struct {
	flags     uint8
	isEmpty   bool
	zoomMask  uint32   // which zoom levels have been modified
	zoomBlobs [][]byte // per set bit in zoomMask, target blob bytes
	zoomVers  []int    // per set bit in zoomMask, which delta's namespace the blob is encoded in (-1 = base)
}

// zoomIdx returns the index into zoomBlobs/zoomVers for zoom level zi.
// Returns -1 if zi is not in zoomMask.
func (s *perTileState) zoomIdx(zi int) int {
	bit := uint32(1) << uint(zi)
	if s.zoomMask&bit == 0 {
		return -1
	}
	idx := 0
	for b := uint32(0); b < uint32(zi); b++ {
		if s.zoomMask&(1<<b) != 0 {
			idx++
		}
	}
	return idx
}

// getZoomBlob returns the stored blob for zoom level zi, or nil if not modified.
func (s *perTileState) getZoomBlob(zi int) []byte {
	idx := s.zoomIdx(zi)
	if idx < 0 {
		return nil
	}
	return s.zoomBlobs[idx]
}

// getZoomVer returns the namespace version for zoom level zi (-1 = base, or delta index).
// Returns -1 if zi is not in zoomMask.
func (s *perTileState) getZoomVer(zi int) int {
	idx := s.zoomIdx(zi)
	if idx < 0 {
		return -1
	}
	return s.zoomVers[idx]
}

// setZoomBlob stores a blob for zoom level zi with the given namespace version.
func (s *perTileState) setZoomBlob(zi int, blob []byte, ver int) {
	bit := uint32(1) << uint(zi)
	if s.zoomMask&bit == 0 {
		idx := 0
		for b := uint32(0); b < uint32(zi); b++ {
			if s.zoomMask&(1<<b) != 0 {
				idx++
			}
		}
		s.zoomMask |= bit
		s.zoomBlobs = append(s.zoomBlobs, nil)
		copy(s.zoomBlobs[idx+1:], s.zoomBlobs[idx:])
		s.zoomBlobs[idx] = blob
		s.zoomVers = append(s.zoomVers, 0)
		copy(s.zoomVers[idx+1:], s.zoomVers[idx:])
		s.zoomVers[idx] = ver
	} else {
		idx := 0
		for b := uint32(0); b < uint32(zi); b++ {
			if s.zoomMask&(1<<b) != 0 {
				idx++
			}
		}
		s.zoomBlobs[idx] = blob
		s.zoomVers[idx] = ver
	}
}

// poiTagsVer returns the poi_tags for a given version (-1 = base).
func poiTagsVer(ver int, base *MapsforgeParser, deltas []*mfdFile) []string {
	if ver < 0 {
		return base.data.header.poi_tags
	}
	return deltas[ver].header.poi_tags
}

// wayTagsVer returns the way_tags for a given version (-1 = base).
func wayTagsVer(ver int, base *MapsforgeParser, deltas []*mfdFile) []string {
	if ver < 0 {
		return base.data.header.way_tags
	}
	return deltas[ver].header.way_tags
}

// crossVersionMaps holds pre-computed tag maps between all version pairs.
// Version -1 (base) maps to index 0; delta i maps to index i+1.
// Access via getPOI(from, to) / getWay(from, to).
type crossVersionMaps struct {
	n   int        // len(deltas) + 1
	poi [][]uint32 // indexed [from+1]*n + [to+1]
	way [][]uint32
}

func (c *crossVersionMaps) getPOI(from, to int) []uint32 {
	return c.poi[(from+1)*c.n+(to+1)]
}

func (c *crossVersionMaps) getWay(from, to int) []uint32 {
	return c.way[(from+1)*c.n+(to+1)]
}

// buildCrossVersionMaps pre-computes all (from, to) tag maps for versions -1..len(deltas)-1.
func buildCrossVersionMaps(base *MapsforgeParser, deltas []*mfdFile) *crossVersionMaps {
	n := len(deltas) + 1
	cvm := &crossVersionMaps{
		n:   n,
		poi: make([][]uint32, n*n),
		way: make([][]uint32, n*n),
	}
	for from := -1; from < len(deltas); from++ {
		fromPOI := poiTagsVer(from, base, deltas)
		fromWay := wayTagsVer(from, base, deltas)
		for to := -1; to < len(deltas); to++ {
			idx := (from+1)*n + (to + 1)
			cvm.poi[idx] = buildTagMapByString(fromPOI, poiTagsVer(to, base, deltas))
			cvm.way[idx] = buildTagMapByString(fromWay, wayTagsVer(to, base, deltas))
		}
	}
	return cvm
}

// applyDeltas applies the delta chain to base and writes the output map.
func applyDeltas(base *MapsforgeParser, deltas []*mfdFile, outHeader *Header, outputPath string) error {
	// Pre-compute all cross-version tag maps once; avoid per-tile buildTagMapByString calls.
	cvm := buildCrossVersionMaps(base, deltas)

	// Phase 1: apply all deltas, building tileStates.
	tileStates := make(map[mfdTileKey]*perTileState)
	mw := &MapsforgeWriter{}
	for di, delta := range deltas {
		if err := applyDeltaRecords(base, deltas, di, delta, tileStates, mw, cvm); err != nil {
			return err
		}
	}

	// Phase 2: write output map using tileStates + base.
	return writeOutputMap(base, deltas, outHeader, tileStates, outputPath)
}

type applyRecordJob struct {
	key mfdTileKey
	rec *mfdTileRecord
	st  *perTileState // current state from prior deltas (may be nil)
}

type applyRecordResult struct {
	key mfdTileKey
	st  *perTileState
	err error
}

// applyDeltaRecords processes all records of one delta, updating tileStates.
// Records are independent within a single delta, so processing is parallelized.
func applyDeltaRecords(base *MapsforgeParser, deltas []*mfdFile, di int, delta *mfdFile, tileStates map[mfdTileKey]*perTileState, _ *MapsforgeWriter, cvm *crossVersionMaps) error {
	inputVer := di - 1 // namespace of delta's input (-1 = base)

	// Snapshot inputs before any parallel writes to tileStates.
	allJobs := make([]applyRecordJob, 0, len(delta.records))
	for key, rec := range delta.records {
		allJobs = append(allJobs, applyRecordJob{key: key, rec: rec, st: tileStates[key]})
	}

	concurrency := runtime.NumCPU()
	jobs := make(chan applyRecordJob, concurrency*2)
	results := make(chan applyRecordResult, len(allJobs))

	for i := 0; i < concurrency; i++ {
		go func() {
			mwLocal := &MapsforgeWriter{}
			for j := range jobs {
				results <- processOneRecord(base, deltas, di, inputVer, delta, j, mwLocal, cvm)
			}
		}()
	}

	for _, j := range allJobs {
		jobs <- j
	}
	close(jobs)

	var firstErr error
	for range allJobs {
		r := <-results
		if r.err != nil && firstErr == nil {
			firstErr = r.err
		}
		tileStates[r.key] = r.st
	}
	return firstErr
}

// processOneRecord handles all zoom levels for a single tile record.
func processOneRecord(base *MapsforgeParser, deltas []*mfdFile, di, inputVer int, delta *mfdFile, j applyRecordJob, mw *MapsforgeWriter, cvm *crossVersionMaps) applyRecordResult {
	key, rec := j.key, j.rec

	if rec.flags&0x02 != 0 {
		return applyRecordResult{key: key, st: &perTileState{flags: rec.flags, isEmpty: true}}
	}

	st := j.st
	if st == nil {
		st = &perTileState{}
	}

	// Parse base tile once (lazily) across all zoom levels of this tile.
	var baseTD *TileData
	getBase := func() (*TileData, error) {
		if baseTD != nil {
			return baseTD, nil
		}
		var err error
		// Use GetTileDataUncached (full parse with block coords) so that
		// normalizeZoomLevel sorts by block data, matching delta generation.
		baseTD, err = base.GetTileDataUncached(key.si, key.x, key.y)
		return baseTD, err
	}

	patchIdx := 0
	mask := rec.zoomMask
	for mask != 0 {
		bit := mask & uint32(-int32(mask))
		mask &^= bit
		bitPos := bits.TrailingZeros32(bit)

		refBlob, err := buildRefBlobLight(base, deltas, di, inputVer, key, bitPos, st, delta, mw, getBase, cvm)
		if err != nil {
			return applyRecordResult{key: key, err: fmt.Errorf("tile si=%d x=%d y=%d zi=%d: %w", key.si, key.x, key.y, bitPos, err)}
		}

		newBlob, err := lz77Decode(rec.patches[patchIdx], refBlob)
		if err != nil {
			return applyRecordResult{key: key, err: fmt.Errorf("tile si=%d x=%d y=%d zi=%d lz77: %w", key.si, key.x, key.y, bitPos, err)}
		}
		patchIdx++
		st.setZoomBlob(bitPos, newBlob, di)
	}

	st.flags = rec.flags
	return applyRecordResult{key: key, st: st}
}

// buildRefBlobLight is like buildRefBlob but uses a caller-supplied getBase callback
// (which parses the tile once and caches it within the caller's scope) instead of
// the shared GetTileData cache. Safe to call from concurrent goroutines.
func buildRefBlobLight(base *MapsforgeParser, deltas []*mfdFile, di, inputVer int, key mfdTileKey, zi int, st *perTileState, delta *mfdFile, mw *MapsforgeWriter, getBase func() (*TileData, error), cvm *crossVersionMaps) ([]byte, error) {
	currentVer := -1
	var blob []byte

	if st != nil && !st.isEmpty {
		blob = st.getZoomBlob(zi)
		if blob != nil {
			currentVer = st.getZoomVer(zi)
		}
	}

	// Proposal 3: if the stored blob is already in inputVer's namespace and both
	// remappings are identity, the blob is already normalized and correct — return directly.
	if blob != nil && currentVer == inputVer &&
		isIdentityMapping(delta.poiMap) && isIdentityMapping(delta.wayMap) {
		return blob, nil
	}

	var pois []POIData
	var ways []WayProperties

	if blob != nil {
		var err error
		pois, ways, err = decodeZoomBlob(blob)
		if err != nil {
			return nil, err
		}
	} else {
		td, err := getBase()
		if err != nil {
			return nil, err
		}
		if td != nil && zi < len(td.poi_data) {
			pois = td.poi_data[zi]
			ways = td.way_data[zi]
		}
		currentVer = -1
	}

	// Proposal 2+4: use pre-computed cross-version maps; remap in-place (we own the data).
	if currentVer != inputVer {
		remapPOITagsInPlace(&pois, cvm.getPOI(currentVer, inputVer))
		remapWayTagsInPlace(&ways, cvm.getWay(currentVer, inputVer))
	}
	remapPOITagsInPlace(&pois, delta.poiMap)
	remapWayTagsInPlace(&ways, delta.wayMap)
	normalizeZoomLevel(pois, ways)
	return encodeZoomBlob(mw, pois, ways), nil
}

// buildRefBlob reconstructs the reference zoom blob for one (tile, zoom) pair.
func buildRefBlob(base *MapsforgeParser, deltas []*mfdFile, di, inputVer int, key mfdTileKey, zi int, st *perTileState, delta *mfdFile, mw *MapsforgeWriter) ([]byte, error) {
	var pois []POIData
	var ways []WayProperties
	currentVer := -1

	if st != nil && !st.isEmpty {
		blob := st.getZoomBlob(zi)
		if blob != nil {
			var err error
			pois, ways, err = decodeZoomBlob(blob)
			if err != nil {
				return nil, err
			}
			currentVer = st.getZoomVer(zi)
		}
	}

	if pois == nil && ways == nil {
		// Use base tile data.
		td, err := base.GetTileData(key.si, key.x, key.y)
		if err != nil {
			return nil, err
		}
		if td != nil && zi < len(td.poi_data) {
			pois = td.poi_data[zi]
			ways = td.way_data[zi]
		}
		currentVer = -1
	}

	// Cross-remap from currentVer namespace to inputVer namespace (if different).
	if currentVer != inputVer {
		crossPOI := buildTagMapByString(poiTagsVer(currentVer, base, deltas), poiTagsVer(inputVer, base, deltas))
		crossWay := buildTagMapByString(wayTagsVer(currentVer, base, deltas), wayTagsVer(inputVer, base, deltas))
		pois = remapPOITags(pois, crossPOI)
		ways = remapWayTags(ways, crossWay)
	}

	// Apply delta's mapping (input → output) and normalize.
	pois = remapPOITags(pois, delta.poiMap)
	ways = remapWayTags(ways, delta.wayMap)
	normalizeZoomLevel(pois, ways)
	return encodeZoomBlob(mw, pois, ways), nil
}

type applyTileJob struct {
	si, tx, ty, idx int
	resCh           chan tileResult
}

// applyTagMaps holds pre-computed tag remapping tables for the apply phase.
type applyTagMaps struct {
	basePOI []uint32 // base → output
	baseWay []uint32
	// deltaPOI[i] / deltaWay[i]: deltas[i] → output (for cross-version zoom blobs)
	deltaPOI       [][]uint32
	deltaWay       [][]uint32
	baseIsIdentity bool // isIdentityMapping(basePOI) && isIdentityMapping(baseWay)
}

// writeOutputMap writes the final output map using tileStates + base.
func writeOutputMap(base *MapsforgeParser, deltas []*mfdFile, outHeader *Header, tileStates map[mfdTileKey]*perTileState, outputPath string) error {
	f, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer f.Close()

	mw := NewMapsforgeWriter(f)
	if err = mw.WriteHeader(outHeader); err != nil {
		return err
	}

	lastDeltaIdx := len(deltas) - 1
	outPoiTags := deltas[lastDeltaIdx].header.poi_tags
	outWayTags := deltas[lastDeltaIdx].header.way_tags

	// Pre-compute all tag maps and identity flags once — avoids per-tile recomputation.
	tm := applyTagMaps{
		basePOI:  buildTagMapByString(base.data.header.poi_tags, outPoiTags),
		baseWay:  buildTagMapByString(base.data.header.way_tags, outWayTags),
		deltaPOI: make([][]uint32, len(deltas)),
		deltaWay: make([][]uint32, len(deltas)),
	}
	for i, d := range deltas {
		tm.deltaPOI[i] = buildTagMapByString(d.header.poi_tags, outPoiTags)
		tm.deltaWay[i] = buildTagMapByString(d.header.way_tags, outWayTags)
	}
	tm.baseIsIdentity = isIdentityMapping(tm.basePOI) && isIdentityMapping(tm.baseWay)

	concurrency := runtime.NumCPU()
	jobs := make(chan applyTileJob, concurrency*2)
	defer close(jobs)

	for i := 0; i < concurrency; i++ {
		go func() {
			mwLocal := &MapsforgeWriter{}
			rs := newTileRewriteState()
			for job := range jobs {
				data, isWater, hasData, err := buildOutputTile(base, &tm, lastDeltaIdx, tileStates, mwLocal, rs, job.si, job.tx, job.ty)
				job.resCh <- tileResult{idx: job.idx, data: data, isWater: isWater, hasData: hasData, err: err}
			}
		}()
	}

	for si := 0; si < len(outHeader.zoom_interval); si++ {
		zic := &outHeader.zoom_interval[si]
		baseZoom := zic.base_zoom_level

		x, bigY := outHeader.min.ToXY(baseZoom)
		bigX, y := outHeader.max.ToXY(baseZoom)
		lenX := bigX - x + 1
		lenY := bigY - y + 1

		pos, _ := f.Seek(0, io.SeekCurrent)
		zic.pos = uint64(pos)

		if outHeader.has_debug {
			rw := newRawWriter()
			rw.fixedString("+++IndexStart+++", 16)
			f.Write(rw.Bytes())
		}

		indexStartPos, _ := f.Seek(0, io.SeekCurrent)
		indexEntries := make([]TileIndexEntry, lenX*lenY)

		rwIndex := newRawWriter()
		for i := 0; i < lenX*lenY; i++ {
			rwIndex.uint8(0)
			rwIndex.uint32(0)
		}
		f.Write(rwIndex.Bytes())

		resultQueue := make([]chan tileResult, 0, concurrency*2)
		bw := bufio.NewWriterSize(f, 4*1024*1024)
		startTileDataPos, _ := f.Seek(0, io.SeekCurrent)
		var currentBytesWritten uint64

		for ty := y; ty <= bigY; ty++ {
			for tx := x; tx <= bigX; tx++ {
				idx := (tx - x) + lenX*(ty-y)
				resCh := make(chan tileResult, 1)
				resultQueue = append(resultQueue, resCh)
				jobs <- applyTileJob{si: si, tx: tx, ty: ty, idx: idx, resCh: resCh}

				if len(resultQueue) >= concurrency*2 {
					res := <-resultQueue[0]
					resultQueue = resultQueue[1:]
					if res.err != nil {
						return res.err
					}
					relativeOffset := (uint64(startTileDataPos) - zic.pos) + currentBytesWritten
					indexEntries[res.idx].Offset = relativeOffset
					indexEntries[res.idx].IsWater = res.isWater
					if res.hasData {
						n, werr := bw.Write(res.data)
						if werr != nil {
							return werr
						}
						currentBytesWritten += uint64(n)
					}
				}
			}
		}

		for _, resCh := range resultQueue {
			res := <-resCh
			if res.err != nil {
				return res.err
			}
			relativeOffset := (uint64(startTileDataPos) - zic.pos) + currentBytesWritten
			indexEntries[res.idx].Offset = relativeOffset
			indexEntries[res.idx].IsWater = res.isWater
			if res.hasData {
				n, werr := bw.Write(res.data)
				if werr != nil {
					return werr
				}
				currentBytesWritten += uint64(n)
			}
		}

		if err = bw.Flush(); err != nil {
			return err
		}

		endPos, _ := f.Seek(0, io.SeekEnd)
		zic.size = uint64(endPos) - zic.pos

		f.Seek(indexStartPos, io.SeekStart)
		rwIndexRewrite := newRawWriter()
		for i := 0; i < len(indexEntries); i++ {
			val := indexEntries[i].Offset
			if indexEntries[i].IsWater {
				val |= 0x8000000000
			}
			rwIndexRewrite.uint8(uint8(val >> 32))
			rwIndexRewrite.uint32(uint32(val))
		}
		f.Write(rwIndexRewrite.Bytes())
		f.Seek(endPos, io.SeekStart)
	}

	finalSize, _ := f.Seek(0, io.SeekEnd)
	outHeader.file_size = uint64(finalSize)
	return mw.FinalizeHeader(outHeader)
}

// buildOutputTile computes the final encoded tile for (si, tx, ty).
func buildOutputTile(base *MapsforgeParser, tm *applyTagMaps, lastDeltaIdx int, tileStates map[mfdTileKey]*perTileState, mw *MapsforgeWriter, rs *tileRewriteState, si, x, y int) (data []byte, isWater, hasData bool, err error) {
	key := mfdTileKey{si, x, y}
	st := tileStates[key]

	if st == nil {
		// No delta covers this tile: re-encode from base with output tag mapping.
		rawBytes, rerr := base.GetRawTileBytes(si, x, y)
		if rerr != nil {
			return nil, false, false, rerr
		}
		if idx := base.GetTileIndex(si, x, y); idx != nil {
			isWater = idx.IsWater
		}
		if rawBytes == nil {
			return nil, isWater, false, nil
		}
		// Identity fast path: tag mapping is a no-op — return raw bytes directly.
		if tm.baseIsIdentity {
			return rawBytes, isWater, true, nil
		}
		// Streaming rewrite: remap tag IDs inline; elements with tagNotFound tags are dropped.
		zic := &base.data.header.zoom_interval[si]
		encoded, rerr := streamRewriteTile(rawBytes, tm.basePOI, tm.baseWay, zic, base.data.header.has_debug, x, y, rs)
		if rerr != nil {
			return nil, false, false, rerr
		}
		return encoded, isWater, true, nil
	}

	isWater = (st.flags & 0x01) != 0
	if st.isEmpty {
		return nil, isWater, false, nil
	}

	zic := &base.data.header.zoom_interval[si]
	zooms := int(zic.max_zoom_level-zic.min_zoom_level) + 1

	// Check whether all blob-covered zoom levels are in the output namespace,
	// and whether all zoom levels have blobs (so no base data is needed).
	blobsNeedRemap := false
	allBlobsPresent := true
	for zi := 0; zi < zooms; zi++ {
		blob := st.getZoomBlob(zi)
		if blob == nil {
			allBlobsPresent = false
		} else if st.getZoomVer(zi) != lastDeltaIdx {
			blobsNeedRemap = true
		}
	}

	// Fast path: no blobs need cross-namespace remapping.
	// Covers all cases: allBlobsPresent or not, baseIsIdentity or not.
	if !blobsNeedRemap {
		// Fast path: assemble tile directly from blob bytes + optionally base bytes,
		// with no struct decode or re-encode.
		var baseZooms []baseZoomBytes
		if !allBlobsPresent {
			rawBytes, rerr := base.GetRawTileBytes(si, x, y)
			if rerr != nil {
				return nil, false, false, rerr
			}
			if rawBytes != nil {
				hasDebug := base.data.header.has_debug
				if tm.baseIsIdentity {
					var perr error
					baseZooms, perr = extractBaseZoomBytes(rawBytes, zic, hasDebug)
					if perr != nil {
						return nil, false, false, perr
					}
				} else {
					// Stream-rewrite base tile into output namespace, then extract per-zoom bytes.
					remapped, rerr := streamRewriteTile(rawBytes, tm.basePOI, tm.baseWay, zic, hasDebug, x, y, rs)
					if rerr != nil {
						return nil, false, false, rerr
					}
					if remapped != nil {
						var perr error
						baseZooms, perr = extractBaseZoomBytes(remapped, zic, hasDebug)
						if perr != nil {
							return nil, false, false, perr
						}
					}
				}
			}
		}

		rs.out.data = rs.out.data[:0]
		if base.data.header.has_debug {
			rs.out.fixedString(fmt.Sprintf("###TileStart### %d,%d", x, y), 32)
		}

		// Collect per-zoom poi/way bytes.
		var poiBytes [32][]byte
		var wayBytes [32][]byte
		var numPois [32]uint32
		var numWays [32]uint32
		totalPoiBytes := 0
		hasAnyFast := false

		for zi := 0; zi < zooms; zi++ {
			blob := st.getZoomBlob(zi)
			if blob != nil {
				// Extract from blob header.
				r := newRawReader(blob)
				numPois[zi] = r.VbeU()
				numWays[zi] = r.VbeU()
				poiLen := r.VbeU()
				if r.err != nil || int(poiLen) > len(r.buf) {
					return nil, false, false, fmt.Errorf("bad blob zi=%d", zi)
				}
				poiBytes[zi] = r.buf[:poiLen]
				wayBytes[zi] = r.buf[poiLen:]
			} else if baseZooms != nil && zi < len(baseZooms) {
				numPois[zi] = baseZooms[zi].numPois
				numWays[zi] = baseZooms[zi].numWays
				poiBytes[zi] = baseZooms[zi].poiBytes
				wayBytes[zi] = baseZooms[zi].wayBytes
			}
			totalPoiBytes += len(poiBytes[zi])
			if len(poiBytes[zi]) > 0 || len(wayBytes[zi]) > 0 {
				hasAnyFast = true
			}
		}

		if !hasAnyFast {
			return nil, isWater, false, nil
		}

		for zi := 0; zi < zooms; zi++ {
			rs.out.VbeU(numPois[zi])
			rs.out.VbeU(numWays[zi])
		}
		rs.out.VbeU(uint32(totalPoiBytes))
		for zi := 0; zi < zooms; zi++ {
			rs.out.data = append(rs.out.data, poiBytes[zi]...)
		}
		for zi := 0; zi < zooms; zi++ {
			rs.out.data = append(rs.out.data, wayBytes[zi]...)
		}
		result := make([]byte, len(rs.out.data))
		copy(result, rs.out.data)
		return result, isWater, true, nil
	}

	// Slow path: some blobs need tag remapping (cross-namespace) or base needs remapping.
	outTD := &TileData{
		tile_header: TileHeader{zoom_table: make([]TileZoomTable, zooms)},
		poi_data:    make([][]POIData, zooms),
		way_data:    make([][]WayProperties, zooms),
	}
	var baseTD *TileData
	getBaseTD := func() (*TileData, error) {
		if baseTD != nil {
			return baseTD, nil
		}
		var rerr error
		baseTD, rerr = base.GetTileDataUncachedLight(si, x, y)
		return baseTD, rerr
	}
	hasAny := false
	for zi := 0; zi < zooms; zi++ {
		blob := st.getZoomBlob(zi)
		var pois []POIData
		var ways []WayProperties

		if blob != nil {
			var perr error
			pois, ways, perr = decodeZoomBlob(blob)
			if perr != nil {
				return nil, false, false, fmt.Errorf("decode blob si=%d x=%d y=%d zi=%d: %w", si, x, y, zi, perr)
			}
			// Remap from per-zoom namespace to output namespace if needed.
			zoomVer := st.getZoomVer(zi)
			if zoomVer != lastDeltaIdx {
				pois = remapPOITags(pois, tm.deltaPOI[zoomVer])
				ways = remapWayTags(ways, tm.deltaWay[zoomVer])
			}
		} else {
			// This zoom level was not modified: use base tile data.
			td, rerr := getBaseTD()
			if rerr != nil {
				return nil, false, false, rerr
			}
			if td != nil && zi < len(td.poi_data) {
				pois = remapPOITags(td.poi_data[zi], tm.basePOI)
				ways = remapWayTags(td.way_data[zi], tm.baseWay)
			}
		}

		outTD.poi_data[zi] = pois
		outTD.way_data[zi] = ways
		outTD.tile_header.zoom_table[zi].num_pois = uint32(len(pois))
		outTD.tile_header.zoom_table[zi].num_ways = uint32(len(ways))
		if len(pois) > 0 || len(ways) > 0 {
			hasAny = true
		}
	}

	if !hasAny {
		return nil, isWater, false, nil
	}

	encoded, rerr := mw.WriteTileData(outTD, x, y)
	if rerr != nil {
		return nil, false, false, rerr
	}
	return encoded, isWater, true, nil
}
