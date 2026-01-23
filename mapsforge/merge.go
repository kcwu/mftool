package mapsforge

import (
	"fmt"
	"io"
	"os"
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
		p, err := ParseFile(path, true) // Call ParseRest via ParseFile(..., true)
		if err != nil {
			return err
		}
		ps = append(ps, p)
	}

	if len(ps) < 2 {
		return fmt.Errorf("at least 2 input maps are required")
	}

	// 1. Merge Tags
	var stats []*map_stats
	for _, p := range ps {
		stats = append(stats, make_map_stats(&p.data.header, p.getTiles()))
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

		rw := newRawWriter(f)
		if outHeader.has_debug {
			rw.fixedString("+++IndexStart+++", 16)
		}

		// Placeholder for tile index
		indexStartPos, _ := f.Seek(0, io.SeekCurrent)
		indexEntries := make([]TileIndexEntry, len_x*len_y)
		for i := 0; i < len_x*len_y; i++ {
			rw.uint8(0)
			rw.uint32(0)
		}

		// Write Tile Data
		for ty := y; ty <= Y; ty++ {
			for tx := x; tx <= X; tx++ {
				tilePos, _ := f.Seek(0, io.SeekCurrent)
				relativeOffset := uint64(tilePos) - zic.pos
				idx := (tx - x) + len_x*(ty-y)
				indexEntries[idx].Offset = relativeOffset

				if flagTile != "" {
					if si != targetSi || tx != targetX || ty != targetY {
						continue
					}
				}

				combinedTd := &TileData{}
				zooms := int(zic.max_zoom_level-zic.min_zoom_level) + 1
				combinedTd.tile_header.zoom_table = make([]TileZoomTable, zooms)
				combinedTd.poi_data = make([][]POIData, zooms)
				combinedTd.way_data = make([][]WayProperties, zooms)

				hasData := false
				isWater := true
				anyMapCovered := false

				for ip, p := range ps {
					// Find subfile in this parser that matches baseZoom
					psi := findSubFileByZoom(p, baseZoom)
					if psi == -1 {
						// Check if this zoom level exists in ANY subfile
						psi = findSubFileContainingZoom(p, baseZoom)
						if psi == -1 {
							continue
						}
					}

					idx := p.GetTileIndex(psi, tx, ty)
					if idx == nil {
						continue
					}

					anyMapCovered = true
					if !idx.IsWater {
						isWater = false
					}

					// If offset changed, it has data
					sf := &p.data.subfiles[psi]
					i := sf.TileIndex(tx, ty)
					if idx.Offset != sf.tile_indexes[i+1].Offset {
						td, err := p.GetTileData(psi, tx, ty)
						if err != nil {
							return err
						}
						hasData = true
						// isWater = false // Removed: Water tiles can have data (e.g. boundaries)

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
					isWater = false
				}

				indexEntries[idx].IsWater = isWater

				if hasData {
					combinedTd.normalize()
					data, err := mw.WriteTileData(combinedTd)
					if err != nil {
						return err
					}
					f.Write(data)
				} else {
					// Empty tile, offset is same as next tile.
					// We'll fix this in the next iteration or at the end.
				}
			}
		}

		endPos, _ := f.Seek(0, io.SeekCurrent)
		zic.size = uint64(endPos) - zic.pos

		// Rewrite tile index
		f.Seek(indexStartPos, io.SeekStart)
		for i := 0; i < len(indexEntries); i++ {
			val := indexEntries[i].Offset
			if indexEntries[i].IsWater {
				val |= 0x8000000000
			}
			// Write 5 bytes
			rw.uint8(uint8(val >> 32))
			rw.uint32(uint32(val))
		}
		f.Seek(endPos, io.SeekStart)
	}

	// Double check file size and finalize header
	finalSize, _ := f.Seek(0, io.SeekEnd)
	outHeader.file_size = uint64(finalSize)
	err = mw.FinalizeHeader(&outHeader)

	return err
}

func merge_tags_simple(tcs []TagsStat) (result TagsStat, mapping [][]uint32) {
	for _, tc := range tcs {
		for _, stat := range tc.stat {
			result.find_tag_by_str(stat.str)
		}
	}

	mapping = make([][]uint32, len(tcs))
	for i, tc := range tcs {
		mapping[i] = make([]uint32, len(tc.stat))
		for j, stat := range tc.stat {
			idx := result.find_tag_by_str(stat.str)
			mapping[i][j] = idx
		}
	}
	return
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
