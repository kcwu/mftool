package mapsforge

import (
	"io/ioutil"
	"os"

	"github.com/pelletier/go-toml/v2"
)

func LoadMapFromTOML(tomlPath, outputPath string) error {
	data, err := ioutil.ReadFile(tomlPath)
	if err != nil {
		return err
	}

	var tm TOMLMap
	err = toml.Unmarshal(data, &tm)
	if err != nil {
		return err
	}

	// Reconstruct Header
	h := &Header{
		header_size:             tm.Header.HeaderSize,
		file_version:            tm.Header.FileVersion,
		file_size:               tm.Header.FileSize,
		creation_date:           tm.Header.CreationDate,
		min:                     LatLon{tm.Header.MinLat, tm.Header.MinLon},
		max:                     LatLon{tm.Header.MaxLat, tm.Header.MaxLon},
		tile_size:               tm.Header.TileSize,
		projection:              tm.Header.Projection,
		has_debug:               tm.Header.HasDebug,
		has_map_start:           tm.Header.HasMapStart,
		start:                   LatLon{tm.Header.StartLat, tm.Header.StartLon},
		has_start_zoom:          tm.Header.HasStartZoom,
		start_zoom:              tm.Header.StartZoom,
		has_language_preference: tm.Header.HasLanguagePreference,
		language_preference:     tm.Header.LanguagePreference,
		has_comment:             tm.Header.HasComment,
		comment:                 tm.Header.Comment,
		has_created_by:          tm.Header.HasCreatedBy,
		created_by:              tm.Header.CreatedBy,
		poi_tags:                tm.Header.PoiTags,
		way_tags:                tm.Header.WayTags,
	}

	// Maps for tag lookup
	poiTagMap := make(map[string]int)
	for i, t := range h.poi_tags {
		poiTagMap[t] = i
	}
	wayTagMap := make(map[string]int)
	for i, t := range h.way_tags {
		wayTagMap[t] = i
	}

	// Reconstruct Zoom Intervals
	h.zoom_interval = make([]ZoomIntervalConfig, len(tm.ZoomIntervals))
	for i, z := range tm.ZoomIntervals {
		h.zoom_interval[i] = ZoomIntervalConfig{
			base_zoom_level: z.BaseZoom,
			min_zoom_level:  z.MinZoom,
			max_zoom_level:  z.MaxZoom,
			// Pos and Size will be recalculated by writer
		}
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer f.Close()

	mw := NewMapsforgeWriter(f)
	err = mw.WriteHeader(h)
	if err != nil {
		return err
	}

	// Group tiles by SI and sort
	tilesBySi := make(map[int][]TOMLTile)
	for _, t := range tm.Tiles {
		tilesBySi[t.Si] = append(tilesBySi[t.Si], t)
	}

	for si := 0; si < len(h.zoom_interval); si++ {
		zic := &h.zoom_interval[si]
		baseZoom := zic.base_zoom_level

		// Subfile calc
		x, Y := h.min.ToXY(baseZoom)
		X, y := h.max.ToXY(baseZoom)
		len_x := int(X - x + 1)
		len_y := int(Y - y + 1)

		// Process tiles for this subfile
		tiles := tilesBySi[si]

		// Start Subfile
		pos, _ := f.Seek(0, 1)
		zic.pos = uint64(pos)

		rw := newRawWriter()
		if h.has_debug {
			rw.fixedString("+++IndexStart+++", 16)
			f.Write(rw.Bytes())
		}

		// Index
		indexStartPos, _ := f.Seek(0, 1)
		indexEntries := make([]TileIndexEntry, len_x*len_y)
		rwIndex := newRawWriter()
		for i := 0; i < len_x*len_y; i++ {
			rwIndex.uint8(0)
			rwIndex.uint32(0)
		}
		f.Write(rwIndex.Bytes())

		// Sort by index for efficiency (and correct index update order is mostly sequential but random access write supports optional order.
		// However, Writer.WriteTileData just returns bytes. We write sequentially.
		// Wait, we need to locate the tile in indexEntries.
		// Map logic: Iterate all X,Y in subfile bounds. If we have data from TOML, write it. Else empty.

		// Map (x,y) -> TileData
		tileMap := make(map[int]TOMLTile)
		for _, t := range tiles {
			key := (t.X - x) + len_x*(t.Y-y)
			tileMap[key] = t
		}

		for ty := y; ty <= Y; ty++ {
			for tx := x; tx <= X; tx++ {
				idx := (tx - x) + len_x*(ty-y)

				var td *TileData
				var isWater bool
				// Default isWater? dumping doesn't provide it for each tile explicitly in the table used by `dump --all`.
				// `dump_indexes` provided it.
				// But `dump --all` produces `[[tiles]]`.
				// If a tile is missing from TOML, we assume it's empty (IsWater=false? or True?).
				// Usually empty = land/background.
				// If it was water, it might be represented in index but have no data.
				// Constraint: We can't know isWater from current TOML format if it has no data.
				// Improvement: `dump` should have included `is_water` in `[[tiles]]` even if empty.
				// But for now, default to false.

				if t, ok := tileMap[idx]; ok {
					// Populate TileData
					td = &TileData{
						tile_header: TileHeader{
							first_way_offset: t.FirstWayOffset, // Recalculated by writer actually?
							// Writer recalculates it. This field is informational in TOML.
						},
					}

					zooms := int(zic.max_zoom_level-zic.min_zoom_level) + 1
					td.poi_data = make([][]POIData, zooms)
					td.way_data = make([][]WayProperties, zooms)

					// POIs
					for _, p := range t.Pois {
						if p.ZiIndex < 0 || p.ZiIndex >= zooms {
							continue
						}
						tags := make([]uint32, 0, len(p.Tags))
						for _, tv := range p.Tags {
							if ti, ok := poiTagMap[tv]; ok {
								tags = append(tags, uint32(ti))
							}
						}

						// Writer handles flags based on has_* fields.

						poi := POIData{
							LatLon:           LatLon{p.Lat, p.Lon},
							layer:            p.Layer, // int8
							tag_id:           tags,
							has_name:         p.Name != "",
							name:             p.Name,
							has_house_number: p.HouseNumber != "",
							house_number:     p.HouseNumber,
							has_elevation:    p.Elevation != 0, // 0 might be valid elevation? TOML omission vs 0.
							elevation:        p.Elevation,
						}
						td.poi_data[p.ZiIndex] = append(td.poi_data[p.ZiIndex], poi)
					}

					// Ways
					for _, w := range t.Ways {
						if w.ZiIndex < 0 || w.ZiIndex >= zooms {
							continue
						}
						tags := make([]uint32, 0, len(w.Tags))
						for _, tv := range w.Tags {
							if ti, ok := wayTagMap[tv]; ok {
								tags = append(tags, uint32(ti))
							}
						}

						way := WayProperties{
							layer:              w.Layer,
							sub_tile_bitmap:    w.SubTileBitmap,
							tag_id:             tags,
							has_name:           w.Name != "",
							name:               w.Name,
							has_house_number:   w.HouseNumber != "",
							house_number:       w.HouseNumber,
							has_reference:      w.Reference != "",
							reference:          w.Reference,
							has_label_position: w.LabelLat != 0 || w.LabelLon != 0,
							label_position:     LatLon{w.LabelLat, w.LabelLon},
							encoding:           w.Encoding,
							// num_way_block, encoding derived
						}

						// Blocks
						way.block = make([]WayData, len(w.Blocks))
						for bi, b := range w.Blocks {
							var block WayData
							for _, segment := range b {
								var nodes []LatLon
								for _, coord := range segment {
									nodes = append(nodes, LatLon{coord[0], coord[1]})
								}
								block.data = append(block.data, nodes)
							}
							way.block[bi] = block
						}

						td.way_data[w.ZiIndex] = append(td.way_data[w.ZiIndex], way)
					}

					isWater = t.IsWater
				}

				tilePos, _ := f.Seek(0, 1)
				relativeOffset := uint64(tilePos) - zic.pos

				indexEntries[idx].Offset = relativeOffset
				indexEntries[idx].IsWater = isWater

				if td != nil {
					// Populate zoom_table counts
					zooms := int(zic.max_zoom_level-zic.min_zoom_level) + 1
					td.tile_header.zoom_table = make([]TileZoomTable, zooms)
					for zi := 0; zi < zooms; zi++ {
						td.tile_header.zoom_table[zi] = TileZoomTable{
							num_pois: uint32(len(td.poi_data[zi])),
							num_ways: uint32(len(td.way_data[zi])),
						}
					}

					// Normalize
					td.normalize()

					data, err := mw.WriteTileData(td)
					if err != nil {
						return err
					}

					f.Write(data)
				}
			}
		}

		endPos, _ := f.Seek(0, 1)
		zic.size = uint64(endPos) - zic.pos

		// Rewrite index
		f.Seek(indexStartPos, 0)
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
		f.Seek(endPos, 0)
	}

	finalSize, _ := f.Seek(0, 2)
	h.file_size = uint64(finalSize)
	return mw.FinalizeHeader(h)
}
