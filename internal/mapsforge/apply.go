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
func CmdApply(basePath string, deltaFiles []string, outputPath string, force, semantic bool) error {
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
	prevPOI := base.data.header.poi_tags
	prevWay := base.data.header.way_tags
	for i, path := range deltaFiles {
		deltas[i], err = loadMFD(path, prevPOI, prevWay)
		if err != nil {
			return fmt.Errorf("load delta %s: %w", path, err)
		}
		prevPOI = deltas[i].header.poi_tags
		prevWay = deltas[i].header.way_tags
	}

	outHeader := deltas[len(deltas)-1].header
	if err := applyDeltas(base, deltas, &outHeader, outputPath); err != nil {
		return err
	}
	if semantic {
		h, err := SemanticHash(outputPath, false, false)
		if err != nil {
			return err
		}
		fmt.Printf("SEMANTIC_HASH: %s\n", h)
	}
	return nil
}

// loadMFD reads and parses an MFD file (mfd\x05 format).
// prevPOITags/prevWayTags are the tag tables of the preceding version (base map
// or previous delta); they are needed to reconstruct the new tag tables from the diff.
func loadMFD(path string, prevPOITags, prevWayTags []string) (*mfdFile, error) {
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
		return nil, fmt.Errorf("parse compact header: %w", err)
	}

	// Reconstruct tag tables from the compact diff; compute poiMap/wayMap from them.
	newPOI, err := readTagDiff(r, prevPOITags)
	if err != nil {
		return nil, fmt.Errorf("read poi tag diff: %w", err)
	}
	newWay, err := readTagDiff(r, prevWayTags)
	if err != nil {
		return nil, fmt.Errorf("read way tag diff: %w", err)
	}
	mf.header.poi_tags = newPOI
	mf.header.way_tags = newWay
	mf.poiMap = buildTagMapByString(prevPOITags, newPOI)
	mf.wayMap = buildTagMapByString(prevWayTags, newWay)
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
	// Coordinates are delta-encoded: dy=Δy within same si, dx=Δx within same row.
	rRec := newRawReader(recRaw)
	mf.records = make(map[mfdTileKey]*mfdTileRecord)
	prevSI := -1
	prevX := 0
	prevY := 0
	for rRec.err == nil && len(rRec.buf) > 0 {
		si := rRec.uint8()
		if si == 0xFF {
			break
		}
		dy := int(rRec.VbeU())
		dx := int(rRec.VbeU())
		var x, y int
		if int(si) != prevSI {
			y = dy
			x = dx
		} else if dy > 0 {
			y = prevY + dy
			x = dx
		} else {
			y = prevY
			x = prevX + dx
		}
		prevSI = int(si)
		prevX = x
		prevY = y
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

// readTagDiff reconstructs a new tag list from a compact diff against prevTags.
// Each entry in the diff is a VbeU: 0 = new literal string follows; n>0 = prevTags[n-1].
func readTagDiff(r *raw_reader, prevTags []string) ([]string, error) {
	count := r.VbeU()
	if r.err != nil {
		return nil, r.err
	}
	tags := make([]string, count)
	for i := uint32(0); i < count; i++ {
		ref := r.VbeU()
		if r.err != nil {
			return nil, r.err
		}
		if ref == 0 {
			tags[i] = r.VbeString()
		} else {
			oldIdx := ref - 1
			if int(oldIdx) >= len(prevTags) {
				return nil, fmt.Errorf("tag ref %d out of range (have %d prev tags)", oldIdx, len(prevTags))
			}
			tags[i] = prevTags[oldIdx]
		}
	}
	return tags, r.err
}

// perTileState tracks the current zoom-blob state for one tile across a delta chain.
type perTileState struct {
	flags     uint8
	isEmpty   bool
	flagsVer  int      // which delta last set flags (and isEmpty); used for stale checks
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
		// After each delta, refresh stored blobs for tiles NOT recorded by this delta
		// but within its enumeration grid.  When a delta's tag mapping drops all content
		// (source-remapped == empty == destination), no record is emitted, but a stored
		// blob from an earlier version persists with content that should now be gone.
		if err := sweepTagErasure(base, deltas, di, delta, tileStates, cvm); err != nil {
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

// sweepTagErasure refreshes stored zoom blobs for tiles that have NO record in
// delta di but are within its enumeration grid.  When a delta's tag mapping drops
// all content (source-remapped == empty == destination), the delta generator emits
// no record — but the stored blob from an earlier version still has content that
// should be considered gone.  Without this sweep the stale blob leaks into output.
//
// For each such tile/zoom, we call buildRefBlobLight (the same reference-reconstruction
// path used during patch application) and overwrite the stored blob with the result.
// If the reference is empty, the blob becomes empty; if non-empty, it is re-encoded
// in delta di's namespace, preventing accumulation of cross-namespace drift.
func sweepTagErasure(base *MapsforgeParser, deltas []*mfdFile, di int, delta *mfdFile, tileStates map[mfdTileKey]*perTileState, cvm *crossVersionMaps) error {
	inputVer := di - 1

	// Pass 1: refresh stored blobs for tiles that have NO record in this delta.
	// Only needed when the delta's own mapping can erase content.  When neither
	// delta.poiMap nor delta.wayMap has a tagNotFound entry, no stored blob can
	// lose content through this delta — skip the entire pass.
	if hasTagNotFound(delta.poiMap) || hasTagNotFound(delta.wayMap) {
		for key, st := range tileStates {
			if st == nil || st.isEmpty || st.zoomMask == 0 {
				continue
			}
			si := key.si
			if si >= len(delta.header.zoom_interval) {
				continue
			}
			// Skip tiles that have a record in this delta — processOneRecord already
			// handles their zoom levels (including the unpatched-zoom refresh loop).
			if _, hasRecord := delta.records[key]; hasRecord {
				continue
			}
			zic := &delta.header.zoom_interval[si]

			// Skip if tile falls outside delta's geographic tile grid.
			txMin, tyMax := delta.header.min.ToXY(zic.base_zoom_level)
			txMax, tyMin := delta.header.max.ToXY(zic.base_zoom_level)
			if key.x < txMin || key.x > txMax || key.y < tyMin || key.y > tyMax {
				continue
			}

			minZoom := int(zic.min_zoom_level)
			maxZoom := int(zic.max_zoom_level)

			mwLocal := &MapsforgeWriter{}
			var baseTD *TileData
			getBase := func() (*TileData, error) {
				if baseTD != nil {
					return baseTD, nil
				}
				var err error
				baseTD, err = base.GetTileDataUncached(key.si, key.x, key.y)
				return baseTD, err
			}

			for actualZoom := minZoom; actualZoom <= maxZoom; actualZoom++ {
				if st.getZoomBlob(actualZoom) == nil {
					continue
				}
				// Don't refresh blobs that are already stale (e.g., an intermediate
				// delta had a different bbox/zoom-range that excluded this tile).
				// Refreshing a stale blob would un-stale it (new ver = di), causing
				// buildOutputTile to use wrong content instead of absent.
				blobVer := st.getZoomVer(actualZoom)
				if isZoomBlobStale(deltas, key.si, key.x, key.y, actualZoom, blobVer, di, int(zic.base_zoom_level)) {
					continue
				}
				refreshed, err := buildRefBlobLight(base, deltas, di, inputVer, key, actualZoom, st, delta, mwLocal, getBase, cvm)
				if err != nil {
					return fmt.Errorf("sweep tile si=%d x=%d y=%d zoom=%d: %w", key.si, key.x, key.y, actualZoom, err)
				}
				st.setZoomBlob(actualZoom, refreshed, di)
			}
		}
	}

	// Pass 2: handle base tiles not yet in tileStates.
	// When a delta's tag mapping erases all base content (remapped source == empty ==
	// destination), no delta record is emitted.  If the tile was never recorded by any
	// prior delta either (tileStates has no entry), buildOutputTile falls back to base
	// data — leaking content that should be absent in the output.
	// We detect this by iterating base tiles within the delta's grid, and for any tile
	// not already in tileStates or delta.records, we check whether the base content
	// maps to empty under this delta's tag mapping.  If so, we add an explicit
	// (possibly partial) empty-blob entry so that buildOutputTile produces an absent tile.
	//
	// Optimization: skip entirely when the composed base→di tag mapping has no
	// tagNotFound entries — no base content can be erased by this delta.  Using
	// hasTagNotFound rather than isIdentityMapping also skips deltas that only
	// reindex tags (present in di but at a different index in base).
	poiMap := cvm.getPOI(-1, di)
	wayMap := cvm.getWay(-1, di)
	if !hasTagNotFound(poiMap) && !hasTagNotFound(wayMap) {
		return nil
	}

	// emptyBlob is the canonical encoding for a zoom level with 0 POIs and 0 ways.
	// VbeU(0)+VbeU(0)+VbeU(0) = {0,0,0}.  Shared across all new tileStates entries
	// created in this pass (read-only in buildOutputTile, safe to share).
	emptyBlob := encodeZoomBlob(&MapsforgeWriter{}, nil, nil)

	for si := 0; si < len(delta.header.zoom_interval); si++ {
		if si >= len(base.data.header.zoom_interval) {
			break
		}
		zic := &delta.header.zoom_interval[si]
		baseZic := &base.data.header.zoom_interval[si]
		// Only process subfiles where the tile coordinate space is the same.
		if baseZic.base_zoom_level != zic.base_zoom_level {
			continue
		}

		txMin, tyMax := delta.header.min.ToXY(zic.base_zoom_level)
		txMax, tyMin := delta.header.max.ToXY(zic.base_zoom_level)
		bxMin, byMax := base.data.header.min.ToXY(baseZic.base_zoom_level)
		bxMax, byMin := base.data.header.max.ToXY(baseZic.base_zoom_level)

		// Intersect delta's grid with base's grid.
		xStart := max(txMin, bxMin)
		xEnd := min(txMax, bxMax)
		yStart := max(tyMin, byMin)
		yEnd := min(tyMax, byMax)
		if xStart > xEnd || yStart > yEnd {
			continue
		}

		minZoom := int(zic.min_zoom_level)
		maxZoom := int(zic.max_zoom_level)
		baseMinZoom := int(baseZic.min_zoom_level)

		for ty := yStart; ty <= yEnd; ty++ {
			for tx := xStart; tx <= xEnd; tx++ {
				key := mfdTileKey{si, tx, ty}
				if _, inStates := tileStates[key]; inStates {
					continue // Pass 1 already handles tiles in tileStates
				}
				if _, hasRecord := delta.records[key]; hasRecord {
					continue // applyDeltaRecords handles recorded tiles
				}
				// Quick check: base has a tile here?
				rawB, rerr := base.GetRawTileBytes(si, tx, ty)
				if rerr != nil {
					return rerr
				}
				if rawB == nil {
					continue // base is absent; nothing to erase
				}

				// Light parse: skip coordinate decoding, we only need tag IDs.
				btd, err := base.GetTileDataUncachedLight(si, tx, ty)
				if err != nil {
					return err
				}
				if btd == nil {
					continue
				}

				var newSt *perTileState
				for actualZoom := minZoom; actualZoom <= maxZoom; actualZoom++ {
					baseZi := actualZoom - baseMinZoom
					if baseZi < 0 || baseZi >= len(btd.poi_data) {
						continue
					}
					pois := btd.poi_data[baseZi]
					ways := btd.way_data[baseZi]
					if len(pois) == 0 && len(ways) == 0 {
						continue // no content at this zoom
					}
					// Check erasure using the direct base→di composition.
					// This is safe for Pass 2: tiles with no record must have
					// source_remapped == destination; if direct composition says
					// content is preserved, destination is also non-empty and a
					// record would have been generated — contradiction.
					if !tileZoomAllErased(pois, ways, poiMap, wayMap) {
						continue // some content survives; base fallback is correct
					}
					if newSt == nil {
						newSt = &perTileState{}
					}
					newSt.setZoomBlob(actualZoom, emptyBlob, di)
				}
				if newSt != nil {
					tileStates[key] = newSt
				}
			}
		}
	}
	return nil
}

// tileZoomAllErased reports whether every POI and way in the given slices would be
// dropped by the supplied tag mappings.  An element is dropped when any of its tags
// maps to tagNotFound (or to an out-of-range index).  A tag-less element always
// survives.  At least one of pois/ways must be non-empty (caller must verify).
func tileZoomAllErased(pois []POIData, ways []WayProperties, poiMap, wayMap []uint32) bool {
	for i := range pois {
		if !tagListErased(pois[i].tag_id, poiMap) {
			return false
		}
	}
	for i := range ways {
		if !tagListErased(ways[i].tag_id, wayMap) {
			return false
		}
	}
	return true
}

// tagListErased reports whether an element with the given tag IDs would be dropped
// by mapping: true iff at least one tag maps to tagNotFound (or is out of range).
// An empty tag list means the element has no tags and always survives → returns false.
func tagListErased(tagIDs []uint32, mapping []uint32) bool {
	if len(tagIDs) == 0 {
		return false
	}
	for _, id := range tagIDs {
		if int(id) >= len(mapping) || mapping[id] == tagNotFound {
			return true
		}
	}
	return false
}

// processOneRecord handles all zoom levels for a single tile record.
func processOneRecord(base *MapsforgeParser, deltas []*mfdFile, di, inputVer int, delta *mfdFile, j applyRecordJob, mw *MapsforgeWriter, cvm *crossVersionMaps) applyRecordResult {
	key, rec := j.key, j.rec

	if rec.flags&0x02 != 0 {
		return applyRecordResult{key: key, st: &perTileState{flags: rec.flags, isEmpty: true, flagsVer: di}}
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

	// Determine this delta's zoom interval so delta-relative zi can be converted
	// to actual zoom level for consistent cross-delta storage in perTileState.
	var deltaZic *ZoomIntervalConfig
	if key.si < len(delta.header.zoom_interval) {
		deltaZic = &delta.header.zoom_interval[key.si]
	}

	patchIdx := 0
	mask := rec.zoomMask
	for mask != 0 {
		bit := mask & uint32(-int32(mask))
		mask &^= bit
		bitPos := bits.TrailingZeros32(bit)

		// Convert delta-relative zoom index to actual zoom level so that blobs
		// stored by different deltas (with different zoom intervals) use a
		// consistent key in perTileState.
		actualZoom := bitPos
		if deltaZic != nil {
			actualZoom = int(deltaZic.min_zoom_level) + bitPos
		}

		refBlob, err := buildRefBlobLight(base, deltas, di, inputVer, key, actualZoom, st, delta, mw, getBase, cvm)
		if err != nil {
			return applyRecordResult{key: key, err: fmt.Errorf("tile si=%d x=%d y=%d zoom=%d: %w", key.si, key.x, key.y, actualZoom, err)}
		}

		newBlob, err := lz77Decode(rec.patches[patchIdx], refBlob)
		if err != nil {
			return applyRecordResult{key: key, err: fmt.Errorf("tile si=%d x=%d y=%d zoom=%d lz77: %w", key.si, key.x, key.y, actualZoom, err)}
		}
		patchIdx++
		st.setZoomBlob(actualZoom, newBlob, di)
	}

	// Refresh blobs for zoom levels in this delta's enumeration range that were
	// NOT patched by this record.  Without this, a blob from an older namespace
	// (e.g. version V) can persist with content that an intermediate delta would
	// have erased via its tag mapping (tags → tagNotFound), because the delta
	// generator emits no record when source-remapped == destination (both empty).
	// When a later delta does record changes at other zoom levels of the same tile,
	// we must bring all unpatched blobs up to this delta's namespace so that their
	// content correctly reflects what was "seen" at inputVer.
	// Skip when this delta cannot erase any tag: no stored blob can lose content.
	if deltaZic != nil && st.zoomMask != 0 && (hasTagNotFound(delta.poiMap) || hasTagNotFound(delta.wayMap)) {
		minZoom := int(deltaZic.min_zoom_level)
		maxZoom := int(deltaZic.max_zoom_level)
		outBaseZoom := int(deltaZic.base_zoom_level)
		for actualZoom := minZoom; actualZoom <= maxZoom; actualZoom++ {
			bitPos := uint(actualZoom - minZoom)
			if rec.zoomMask&(1<<bitPos) != 0 {
				continue // already handled above
			}
			if st.getZoomBlob(actualZoom) == nil {
				continue // no stored blob to refresh
			}
			// Don't refresh blobs that are already stale due to an intermediate
			// delta's bbox/zoom-range change.  Refreshing would un-stale the blob.
			blobVer := st.getZoomVer(actualZoom)
			if isZoomBlobStale(deltas, key.si, key.x, key.y, actualZoom, blobVer, di, outBaseZoom) {
				continue
			}
			refreshed, rerr := buildRefBlobLight(base, deltas, di, inputVer, key, actualZoom, st, delta, mw, getBase, cvm)
			if rerr != nil {
				return applyRecordResult{key: key, err: fmt.Errorf("tile si=%d x=%d y=%d zoom=%d refresh: %w", key.si, key.x, key.y, actualZoom, rerr)}
			}
			st.setZoomBlob(actualZoom, refreshed, di)
		}
	}

	st.flags = rec.flags
	st.flagsVer = di
	st.isEmpty = false
	return applyRecordResult{key: key, st: st}
}

// buildRefBlobLight is like buildRefBlob but uses a caller-supplied getBase callback
// (which parses the tile once and caches it within the caller's scope) instead of
// the shared GetTileData cache. Safe to call from concurrent goroutines.
func buildRefBlobLight(base *MapsforgeParser, deltas []*mfdFile, di, inputVer int, key mfdTileKey, zi int, st *perTileState, delta *mfdFile, mw *MapsforgeWriter, getBase func() (*TileData, error), cvm *crossVersionMaps) ([]byte, error) {
	currentVer := -1
	var blob []byte

	if st != nil && st.isEmpty {
		// The tile was made absent by a prior delta; the next delta's patch was
		// generated against a nil reference — return nil to match.
		return nil, nil
	}

	// If zi is outside inputVer's zoom range, the state at inputVer doesn't include
	// this zoom level.  Return the same empty encoding that delta generation uses
	// when the source map's zoom interval doesn't cover zi (td1 != nil, zi1 out of range).
	if inputVer >= 0 && key.si < len(deltas[inputVer].header.zoom_interval) {
		izic := &deltas[inputVer].header.zoom_interval[key.si]
		if zi < int(izic.min_zoom_level) || zi > int(izic.max_zoom_level) {
			return encodeZoomBlob(mw, nil, nil), nil
		}
	}

	// zi is an actual zoom level (not a delta-relative index); perTileState keys by actual zoom.
	if st != nil {
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
		if td != nil {
			// zi is already an actual zoom level; map to base's relative index.
			zi1 := zi
			if key.si < len(base.data.header.zoom_interval) {
				zi1 = zi - int(base.data.header.zoom_interval[key.si].min_zoom_level)
			}
			if zi1 >= 0 && zi1 < len(td.poi_data) {
				pois = td.poi_data[zi1]
				ways = td.way_data[zi1]
			}
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

type applyTileJob struct {
	si, tx, ty, idx int
	outZic          *ZoomIntervalConfig
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

	outHasDebug := outHeader.has_debug
	for i := 0; i < concurrency; i++ {
		go func() {
			mwLocal := &MapsforgeWriter{}
			mwLocal.HasDebug = outHasDebug
			rs := newTileRewriteState()
			for job := range jobs {
				data, isWater, hasData, err := buildOutputTile(base, deltas, &tm, lastDeltaIdx, tileStates, mwLocal, rs, outHasDebug, job.outZic, job.si, job.tx, job.ty)
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
				jobs <- applyTileJob{si: si, tx: tx, ty: ty, idx: idx, outZic: zic, resCh: resCh}

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

// isZoomBlobStale reports whether the blob stored at actualZoom (set at version ver)
// was killed by a later delta that:
//   - excluded actualZoom from its zoom range, OR
//   - used a different base_zoom for si (different tile coordinate space), OR
//   - has a bbox that does not contain tile (x, y) — meaning the output tile was
//     never enumerated by that delta and any changes (including deletion) were not
//     captured in a record.
//
// outBaseZoom is the base_zoom_level of the output subfile si.
func isZoomBlobStale(deltas []*mfdFile, si, x, y, actualZoom, ver, lastDeltaIdx, outBaseZoom int) bool {
	for v := ver + 1; v <= lastDeltaIdx; v++ {
		if si >= len(deltas[v].header.zoom_interval) {
			// Subfile si is absent from delta v's destination map entirely:
			// the tile was never enumerated, so any change (including deletion)
			// was not captured in a record.
			return true
		}
		zic := &deltas[v].header.zoom_interval[si]
		if actualZoom < int(zic.min_zoom_level) || actualZoom > int(zic.max_zoom_level) {
			return true
		}
		if int(zic.base_zoom_level) != outBaseZoom {
			return true
		}
		// Check if (x, y) falls within delta v's tile grid.
		txMin, tyMax := deltas[v].header.min.ToXY(zic.base_zoom_level)
		txMax, tyMin := deltas[v].header.max.ToXY(zic.base_zoom_level)
		if x < txMin || x > txMax || y < tyMin || y > tyMax {
			return true
		}
	}
	return false
}

// buildOutputTile computes the final encoded tile for (si, tx, ty).
func buildOutputTile(base *MapsforgeParser, deltas []*mfdFile, tm *applyTagMaps, lastDeltaIdx int, tileStates map[mfdTileKey]*perTileState, mw *MapsforgeWriter, rs *tileRewriteState, outHasDebug bool, outZic *ZoomIntervalConfig, si, x, y int) (data []byte, isWater, hasData bool, err error) {
	key := mfdTileKey{si, x, y}
	st := tileStates[key]

	if st == nil {
		// Check whether any zoom level's base data is invalidated by a coord-space
		// change in an intermediate delta (different base_zoom or zoom range that
		// excludes the zoom level).  A non-absent output tile after such a change
		// would have produced a delta record (b1=nil from the changed-space delta),
		// so st==nil guarantees those zoom levels are absent in the output.
		outBaseZoom := int(outZic.base_zoom_level)
		outZoomsCount := int(outZic.max_zoom_level-outZic.min_zoom_level) + 1
		anyStale := false
		for zi := 0; zi < outZoomsCount; zi++ {
			if isZoomBlobStale(deltas, si, x, y, int(outZic.min_zoom_level)+zi, -1, lastDeltaIdx, outBaseZoom) {
				anyStale = true
				break
			}
		}
		if !anyStale {
			// No stale zooms: base data is valid for all zoom levels.
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

			// Determine if we can use the fast path (raw bytes or stream-rewrite):
			// requires same debug mode AND same zoom structure as the output.
			// If either differs we must do a full re-encode via WriteTileData so that
			// (a) debug signatures are added/removed correctly, and
			// (b) only the zoom levels covered by outZic are written.
			var baseZicSt *ZoomIntervalConfig
			if si < len(base.data.header.zoom_interval) {
				baseZicSt = &base.data.header.zoom_interval[si]
			}
			sameDebug := outHasDebug == base.data.header.has_debug
			sameZooms := baseZicSt != nil &&
				baseZicSt.min_zoom_level == outZic.min_zoom_level &&
				baseZicSt.max_zoom_level == outZic.max_zoom_level

			if !sameDebug || !sameZooms {
				// Re-encode: parse base, extract outZic zoom levels, write in output format.
				td, rerr := base.GetTileDataUncachedLight(si, x, y)
				if rerr != nil {
					return nil, false, false, rerr
				}
				if td == nil {
					return nil, isWater, false, nil
				}
				outZooms := int(outZic.max_zoom_level-outZic.min_zoom_level) + 1
				outTD := &TileData{
					tile_header: TileHeader{zoom_table: make([]TileZoomTable, outZooms)},
					poi_data:    make([][]POIData, outZooms),
					way_data:    make([][]WayProperties, outZooms),
				}
				for zi := 0; zi < outZooms; zi++ {
					baseZi := zi
					if baseZicSt != nil {
						baseZi = int(outZic.min_zoom_level) + zi - int(baseZicSt.min_zoom_level)
					}
					var pois []POIData
					var ways []WayProperties
					if baseZi >= 0 && baseZi < len(td.poi_data) {
						if tm.baseIsIdentity {
							pois = td.poi_data[baseZi]
							ways = td.way_data[baseZi]
						} else {
							pois = remapPOITags(td.poi_data[baseZi], tm.basePOI)
							ways = remapWayTags(td.way_data[baseZi], tm.baseWay)
						}
					}
					outTD.poi_data[zi] = pois
					outTD.way_data[zi] = ways
					outTD.tile_header.zoom_table[zi].num_pois = uint32(len(pois))
					outTD.tile_header.zoom_table[zi].num_ways = uint32(len(ways))
				}
				encoded, rerr := mw.WriteTileData(outTD, x, y)
				if rerr != nil {
					return nil, false, false, rerr
				}
				return encoded, isWater, true, nil
			}

			// Same debug mode and same zoom structure: fast paths are safe.
			// Identity fast path: tag mapping is a no-op — return raw bytes directly.
			if tm.baseIsIdentity {
				return rawBytes, isWater, true, nil
			}
			// Streaming rewrite: remap tag IDs inline; elements with tagNotFound tags are dropped.
			encoded, rerr := streamRewriteTile(rawBytes, tm.basePOI, tm.baseWay, baseZicSt, base.data.header.has_debug, x, y, rs)
			if rerr != nil {
				return nil, false, false, rerr
			}
			return encoded, isWater, true, nil
		}
		// Some/all zoom levels are stale: route to per-zoom handling below.
		// Stale zoom levels will be absent; non-stale ones will use base data.
		st = &perTileState{}
	}

	isWater = (st.flags & 0x01) != 0
	// If the flags (including isWater) were set by a delta that is now stale due to a
	// later coord-space change, reset isWater to false.  The stale delta couldn't record
	// the actual isWater transition, so the output tile is absent with no water flag.
	if isWater && isZoomBlobStale(deltas, si, x, y, int(outZic.min_zoom_level), st.flagsVer, lastDeltaIdx, int(outZic.base_zoom_level)) {
		isWater = false
	}
	if st.isEmpty {
		// Check if the isEmpty state is stale: a later delta may have used a different
		// coordinate space (base_zoom/bbox/zoom-range change) and couldn't update or
		// clear this record.  Using any representative zoom level suffices because
		// base_zoom and bbox staleness are independent of the actual zoom value.
		outBaseZoom := int(outZic.base_zoom_level)
		if isZoomBlobStale(deltas, si, x, y, int(outZic.min_zoom_level), st.flagsVer, lastDeltaIdx, outBaseZoom) {
			// Stale: a coord-space change erased this tile from an intermediate map.
			// Any non-absent output tile would have generated a record from the nil
			// reference; no record means the output tile is absent with isWater=false.
			return nil, false, false, nil
		}
		return nil, isWater, false, nil
	}

	zooms := int(outZic.max_zoom_level-outZic.min_zoom_level) + 1

	// baseZic is used only when base data is needed for unmodified zoom levels.
	// It may differ from outZic when the maps have different zoom interval configs.
	var baseZic *ZoomIntervalConfig
	if si < len(base.data.header.zoom_interval) {
		baseZic = &base.data.header.zoom_interval[si]
	}

	// Check whether all blob-covered zoom levels are in the output namespace,
	// and whether all zoom levels have blobs (so no base data is needed).
	blobsNeedRemap := false
	allBlobsPresent := true
	for zi := 0; zi < zooms; zi++ {
		actualZoom := int(outZic.min_zoom_level) + zi
		blob := st.getZoomBlob(actualZoom)
		if blob == nil {
			allBlobsPresent = false
		} else {
			ver := st.getZoomVer(actualZoom)
			if isZoomBlobStale(deltas, si, x, y, actualZoom, ver, lastDeltaIdx, int(outZic.base_zoom_level)) {
				allBlobsPresent = false
			} else if ver != lastDeltaIdx {
				blobsNeedRemap = true
			}
		}
	}

	// Fast path: no blobs need cross-namespace remapping, and the output does not
	// require debug signatures. Zoom blobs are always debug-free (encodeZoomBlob
	// uses HasDebug=false), so when outHasDebug=true we must use the slow path
	// (WriteTileData) to emit the required per-element signatures.
	// Also requires allBlobsPresent or base debug mode matching output: if the
	// base has debug=true but output has debug=false and we need base tile bytes,
	// the raw base bytes include debug signatures that cannot be written verbatim
	// into a non-debug output — fall through to the slow path in that case.
	if !blobsNeedRemap && !outHasDebug && (allBlobsPresent || !base.data.header.has_debug) {
		// Fast path: assemble tile directly from blob bytes + optionally base bytes,
		// with no struct decode or re-encode.
		var baseZooms []baseZoomBytes
		if !allBlobsPresent {
			rawBytes, rerr := base.GetRawTileBytes(si, x, y)
			if rerr != nil {
				return nil, false, false, rerr
			}
			if rawBytes != nil && baseZic != nil {
				hasDebug := base.data.header.has_debug
				if tm.baseIsIdentity {
					var perr error
					baseZooms, perr = extractBaseZoomBytes(rawBytes, baseZic, hasDebug)
					if perr != nil {
						return nil, false, false, perr
					}
				} else {
					// Stream-rewrite base tile into output namespace, then extract per-zoom bytes.
					remapped, rerr := streamRewriteTile(rawBytes, tm.basePOI, tm.baseWay, baseZic, hasDebug, x, y, rs)
					if rerr != nil {
						return nil, false, false, rerr
					}
					if remapped != nil {
						var perr error
						baseZooms, perr = extractBaseZoomBytes(remapped, baseZic, hasDebug)
						if perr != nil {
							return nil, false, false, perr
						}
					}
				}
			}
		}

		rs.out.data = rs.out.data[:0]
		if outHasDebug {
			rs.out.fixedString(fmt.Sprintf("###TileStart%d,%d###", x, y), 32)
		}

		// Collect per-zoom poi/way bytes.
		var poiBytes [32][]byte
		var wayBytes [32][]byte
		var numPois [32]uint32
		var numWays [32]uint32
		totalPoiBytes := 0
		hasAnyFast := false

		for zi := 0; zi < zooms; zi++ {
			actualZoom := int(outZic.min_zoom_level) + zi
			blob := st.getZoomBlob(actualZoom)
			if blob != nil && isZoomBlobStale(deltas, si, x, y, actualZoom, st.getZoomVer(actualZoom), lastDeltaIdx, int(outZic.base_zoom_level)) {
				blob = nil
			}
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
			} else if baseZooms != nil && baseZic != nil &&
				!isZoomBlobStale(deltas, si, x, y, actualZoom, -1, lastDeltaIdx, int(outZic.base_zoom_level)) {
				// Map output zoom index to base zoom index via actual zoom level.
				baseZi := int(outZic.min_zoom_level) + zi - int(baseZic.min_zoom_level)
				if baseZi >= 0 && baseZi < len(baseZooms) {
					numPois[zi] = baseZooms[baseZi].numPois
					numWays[zi] = baseZooms[baseZi].numWays
					poiBytes[zi] = baseZooms[baseZi].poiBytes
					wayBytes[zi] = baseZooms[baseZi].wayBytes
				}
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
		actualZoom := int(outZic.min_zoom_level) + zi
		blob := st.getZoomBlob(actualZoom)
		if blob != nil && isZoomBlobStale(deltas, si, x, y, actualZoom, st.getZoomVer(actualZoom), lastDeltaIdx, int(outZic.base_zoom_level)) {
			blob = nil
		}
		var pois []POIData
		var ways []WayProperties

		if blob != nil {
			var perr error
			pois, ways, perr = decodeZoomBlob(blob)
			if perr != nil {
				return nil, false, false, fmt.Errorf("decode blob si=%d x=%d y=%d zi=%d: %w", si, x, y, zi, perr)
			}
			// Remap from per-zoom namespace to output namespace if needed.
			zoomVer := st.getZoomVer(actualZoom)
			if zoomVer != lastDeltaIdx {
				pois = remapPOITags(pois, tm.deltaPOI[zoomVer])
				ways = remapWayTags(ways, tm.deltaWay[zoomVer])
			}
		} else if !isZoomBlobStale(deltas, si, x, y, actualZoom, -1, lastDeltaIdx, int(outZic.base_zoom_level)) {
			// This zoom level was not modified: use base tile data.
			td, rerr := getBaseTD()
			if rerr != nil {
				return nil, false, false, rerr
			}
			if td != nil && baseZic != nil {
				// Map output zoom index to base zoom index via actual zoom level.
				baseZi := int(outZic.min_zoom_level) + zi - int(baseZic.min_zoom_level)
				if baseZi >= 0 && baseZi < len(td.poi_data) {
					pois = remapPOITags(td.poi_data[baseZi], tm.basePOI)
					ways = remapWayTags(td.way_data[baseZi], tm.baseWay)
				}
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

	// In debug mode, WriteTileData always emits at least the TileStart signature,
	// so even a tile with 0 POIs and 0 ways must be written (it is not absent).
	if !hasAny && !outHasDebug {
		return nil, isWater, false, nil
	}

	encoded, rerr := mw.WriteTileData(outTD, x, y)
	if rerr != nil {
		return nil, false, false, rerr
	}
	return encoded, isWater, true, nil
}
