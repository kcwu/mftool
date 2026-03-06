package mapsforge

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"
)

func MergeMaps(inputPaths []string, outputPath string, flagTile string) error {
	var targetSi, targetX, targetY int
	if flagTile != "" {
		_, err := fmt.Sscanf(flagTile, "%d,%d,%d", &targetSi, &targetX, &targetY)
		if err != nil {
			return err
		}
	}

	var ps []*MapsforgeParser
	for _, path := range inputPaths {
		p, err := ParseFile(path, false) // Lazy parsing
		if err != nil {
			return err
		}
		ps = append(ps, p)
		defer p.Close()
	}

	if len(ps) < 2 {
		return fmt.Errorf("at least 2 input maps are required")
	}

	// 1. Merge Tags
	// 1. Merge Tags
	var stats []*map_stats
	for _, p := range ps {
		stats = append(stats, CollectStatsParallel(p))
	}

	merged, poiMapping, wayMapping := merge_map_tags(stats)
	mergedPoiTags := merged.poi_stats
	mergedWayTags := merged.way_stats

	// 2. Combine Bounding Box
	outHeader := ps[0].data.header
	for i := 1; i < len(ps); i++ {
		h := ps[i].data.header
		if h.min.lat < outHeader.min.lat {
			outHeader.min.lat = h.min.lat
		}
		if h.min.lon < outHeader.min.lon {
			outHeader.min.lon = h.min.lon
		}
		if h.max.lat > outHeader.max.lat {
			outHeader.max.lat = h.max.lat
		}
		if h.max.lon > outHeader.max.lon {
			outHeader.max.lon = h.max.lon
		}
	}

	// Deep copy zoom intervals to avoid corrupting the first input map parser
	srcIntervals := outHeader.zoom_interval
	outHeader.zoom_interval = make([]ZoomIntervalConfig, len(srcIntervals))
	copy(outHeader.zoom_interval, srcIntervals)

	outHeader.poi_tags = get_tag_strings(mergedPoiTags)
	outHeader.way_tags = get_tag_strings(mergedWayTags)
	outHeader.creation_date = uint64(time.Now().UnixMilli())

	// 3. Prepare SubFiles
	// We assume input maps have compatible zoom intervals.
	// For now, let's use the zoom intervals from the first map.
	// In a more robust version, we should merge zoom intervals too.

	f, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer f.Close()

	mw := NewMapsforgeWriter(f)
	err = mw.WriteHeader(&outHeader)
	if err != nil {
		return err
	}

	concurrency := runtime.NumCPU()
	jobs := make(chan tileJob, concurrency*2)
	defer close(jobs)
	for i := 0; i < concurrency; i++ {
		go mergeWorker(jobs, ps, &outHeader, poiMapping, wayMapping, mw)
	}

	for si := 0; si < len(outHeader.zoom_interval); si++ {
		zic := &outHeader.zoom_interval[si]
		baseZoom := zic.base_zoom_level

		x, Y := outHeader.min.ToXY(baseZoom)
		X, y := outHeader.max.ToXY(baseZoom)
		len_x := X - x + 1
		len_y := Y - y + 1

		// SubFile start position
		pos, _ := f.Seek(0, io.SeekCurrent)
		zic.pos = uint64(pos)

		rw := newRawWriter()
		if outHeader.has_debug {
			rw.fixedString("+++IndexStart+++", 16)
			// Writes to file immediately for this small part
			f.Write(rw.Bytes())
		}

		// Placeholder for tile index
		indexStartPos, _ := f.Seek(0, io.SeekCurrent)
		indexEntries := make([]TileIndexEntry, len_x*len_y)

		// Use buffered writer for index initialization
		rwIndex := newRawWriter()
		for i := 0; i < len_x*len_y; i++ {
			rwIndex.uint8(0)
			rwIndex.uint32(0)
		}
		f.Write(rwIndex.Bytes())

		// Write Tile Data
		// We use a lookahead buffer to keep write order sequential
		resultQueue := make([]chan tileResult, 0, concurrency*2)

		bw := bufio.NewWriterSize(f, 4*1024*1024)
		startTileDataPos, _ := f.Seek(0, io.SeekCurrent)
		var currentBytesWritten uint64

		for ty := y; ty <= Y; ty++ {
			for tx := x; tx <= X; tx++ {
				idx := (tx - x) + len_x*(ty-y)

				resCh := make(chan tileResult, 1)
				resultQueue = append(resultQueue, resCh)

				shouldProcess := true
				if flagTile != "" {
					if si != targetSi || tx != targetX || ty != targetY {
						shouldProcess = false
					}
				}

				if shouldProcess {
					jobs <- tileJob{
						si:       si,
						tx:       tx,
						ty:       ty,
						idx:      idx,
						baseZoom: baseZoom,
						resCh:    resCh,
					}
				} else {
					resCh <- tileResult{idx: idx, isWater: false, hasData: false}
				}

				// If queue full, pop and process
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
						n, err := bw.Write(res.data)
						if err != nil {
							return err
						}
						currentBytesWritten += uint64(n)
					}
				}
			}
		}

		// Drain remaining results
		for _, resCh := range resultQueue {
			res := <-resCh
			if res.err != nil {
				return res.err
			}

			relativeOffset := (uint64(startTileDataPos) - zic.pos) + currentBytesWritten
			indexEntries[res.idx].Offset = relativeOffset
			indexEntries[res.idx].IsWater = res.isWater

			if res.hasData {
				n, err := bw.Write(res.data)
				if err != nil {
					return err
				}
				currentBytesWritten += uint64(n)
			}
		}

		err = bw.Flush()
		if err != nil {
			return err
		}

		endPos, _ := f.Seek(0, io.SeekCurrent)
		zic.size = uint64(endPos) - zic.pos

		// Rewrite tile index
		f.Seek(indexStartPos, io.SeekStart)
		// Rewrite tile index
		f.Seek(indexStartPos, io.SeekStart)
		rwIndexRewrite := newRawWriter()
		for i := 0; i < len(indexEntries); i++ {
			val := indexEntries[i].Offset
			if indexEntries[i].IsWater {
				val |= 0x8000000000
			}
			// Write 5 bytes
			rwIndexRewrite.uint8(uint8(val >> 32))
			rwIndexRewrite.uint32(uint32(val))
		}
		f.Write(rwIndexRewrite.Bytes())
		f.Seek(endPos, io.SeekStart)
	}

	// Double check file size and finalize header
	finalSize, _ := f.Seek(0, io.SeekEnd)
	outHeader.file_size = uint64(finalSize)
	err = mw.FinalizeHeader(&outHeader)

	return err
}

type tileJob struct {
	si, tx, ty, idx int
	baseZoom        uint8
	resCh           chan tileResult
}

type tileResult struct {
	data    []byte
	hasData bool
	isWater bool
	idx     int
	err     error
}

func mergeWorker(jobs <-chan tileJob, ps []*MapsforgeParser, outHeader *Header, poiMapping, wayMapping [][]uint32, mw *MapsforgeWriter) {
	for job := range jobs {
		res := tileResult{idx: job.idx, isWater: true}

		combinedTd := &TileData{}
		zooms := int(outHeader.zoom_interval[job.si].max_zoom_level-outHeader.zoom_interval[job.si].min_zoom_level) + 1
		combinedTd.tile_header.zoom_table = make([]TileZoomTable, zooms)
		combinedTd.poi_data = make([][]POIData, zooms)
		combinedTd.way_data = make([][]WayProperties, zooms)

		anyMapCovered := false

		for ip, p := range ps {
			// Find subfile in this parser that matches baseZoom
			psi := findSubFileByZoom(p, job.baseZoom)
			if psi == -1 {
				// Check if this zoom level exists in ANY subfile
				psi = findSubFileContainingZoom(p, job.baseZoom)
				if psi == -1 {
					continue
				}
			}

			idx := p.GetTileIndex(psi, job.tx, job.ty)
			if idx == nil {
				continue
			}

			anyMapCovered = true
			if !idx.IsWater {
				res.isWater = false
			}

			// If offset changed, it has data
			sf := &p.data.subfiles[psi]
			i := sf.TileIndex(job.tx, job.ty)
			if idx.Offset != sf.tile_indexes[i+1].Offset {
				td, err := p.GetTileData(psi, job.tx, job.ty)
				if err != nil {
					res.err = err
					job.resCh <- res
					continue
				}
				res.hasData = true

				// Merge td into combinedTd
				for zi := 0; zi < zooms; zi++ {
					combinedTd.tile_header.zoom_table[zi].num_pois += uint32(len(td.poi_data[zi]))
					combinedTd.tile_header.zoom_table[zi].num_ways += uint32(len(td.way_data[zi]))

					for _, poi := range td.poi_data[zi] {
						newPoi := poi
						newPoi.tag_id = remap_tags(poi.tag_id, poiMapping[ip])
						combinedTd.poi_data[zi] = append(combinedTd.poi_data[zi], newPoi)
					}
					for _, way := range td.way_data[zi] {
						newWay := way
						newWay.tag_id = remap_tags(way.tag_id, wayMapping[ip])
						combinedTd.way_data[zi] = append(combinedTd.way_data[zi], newWay)
					}
				}
			}
		}

		if !anyMapCovered {
			res.isWater = false
		}

		if res.hasData {
			combinedTd.normalize()
			var err error
			res.data, err = mw.WriteTileData(combinedTd, job.tx, job.ty)
			if err != nil {
				res.err = err
			}
		}

		job.resCh <- res
	}
}

func get_tag_strings(ts TagsStat) []string {
	var res []string
	for _, s := range ts.stat {
		res = append(res, s.str)
	}
	return res
}

func remap_tags(tags []uint32, mapping []uint32) []uint32 {
	res := make([]uint32, len(tags))
	for i, t := range tags {
		res[i] = mapping[t]
	}
	return res
}

func findSubFileByZoom(p *MapsforgeParser, baseZoom uint8) int {
	for i, zic := range p.data.header.zoom_interval {
		if zic.base_zoom_level == baseZoom {
			return i
		}
	}
	return -1
}

func findSubFileContainingZoom(p *MapsforgeParser, zoom uint8) int {
	for i, zic := range p.data.header.zoom_interval {
		if zoom >= zic.min_zoom_level && zoom <= zic.max_zoom_level {
			return i
		}
	}
	return -1
}
