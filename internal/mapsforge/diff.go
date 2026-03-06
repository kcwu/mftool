package mapsforge

import (
	"errors"
	"fmt"
)

func is_ignore_poi(poi *POIData) bool {
	return false
}

func is_ignore_way(way *WayProperties, stat *TagsStat) bool {
	return false
}

func compare_poi_datas(stats map_stats, z, x, y int, d1, d2 []POIData, detail bool) bool {
	var found_diff bool
	for i, j := 0, 0; i < len(d1) || j < len(d2); {
		if j == len(d2) || (i < len(d1) && d1[i].less(&d2[j])) {
			if !detail {
				return true
			}

			found_diff = true
			fmt.Println(z, x, y, "-poi,", d1[i].ToString(&stats.poi_stats))
			i++
		} else if i == len(d1) || d2[j].less(&d1[i]) {
			if !detail {
				return true
			}
			found_diff = true
			fmt.Println(z, x, y, "+poi,", d2[j].ToString(&stats.poi_stats))
			j++
		} else {
			i++
			j++
		}
	}
	if found_diff {
		fmt.Println()
	}
	return found_diff
}

func compare_way_datas(stats map_stats, z, x, y int, d1, d2 []WayProperties, detail bool) bool {
	var found_diff bool
	for i, j := 0, 0; i < len(d1) || j < len(d2); {
		if j == len(d2) || (i < len(d1) && d1[i].less(&d2[j])) {
			if !detail {
				return true
			}

			found_diff = true
			fmt.Println(z, x, y, "-way", d1[i].ToString(&stats.way_stats))
			i++
		} else if i == len(d1) || d2[j].less(&d1[i]) {
			if !detail {
				return true
			}

			found_diff = true
			fmt.Println(z, x, y, "+way", d2[j].ToString(&stats.way_stats))
			j++
		} else {
			//fmt.Println("=", d1[i].ToString())
			//fmt.Println("=", d2[j].ToString())
			i++
			j++
		}
	}
	if found_diff {
		fmt.Println()
	}
	return found_diff
}

func compareHeaders(h1, h2 *Header, ignoreComment, ignoreTimestamp bool) {
	if h1.min != h2.min {
		fmt.Printf("Header mismatch: min %v != %v\n", h1.min, h2.min)
	}
	if h1.max != h2.max {
		fmt.Printf("Header mismatch: max %v != %v\n", h1.max, h2.max)
	}
	if h1.tile_size != h2.tile_size {
		fmt.Printf("Header mismatch: tile_size %v != %v\n", h1.tile_size, h2.tile_size)
	}
	if h1.projection != h2.projection {
		fmt.Printf("Header mismatch: projection %v != %v\n", h1.projection, h2.projection)
	}
	if h1.start_zoom != h2.start_zoom {
		fmt.Printf("Header mismatch: start_zoom %v != %v\n", h1.start_zoom, h2.start_zoom)
	}
	if h1.language_preference != h2.language_preference {
		fmt.Printf("Header mismatch: language_preference %q != %q\n", h1.language_preference, h2.language_preference)
	}
	if !ignoreComment && h1.comment != h2.comment {
		fmt.Printf("Header mismatch: comment %q != %q\n", h1.comment, h2.comment)
	}
	if h1.created_by != h2.created_by {
		fmt.Printf("Header mismatch: created_by %q != %q\n", h1.created_by, h2.created_by)
	}
	if !ignoreTimestamp && h1.creation_date != h2.creation_date {
		fmt.Printf("Header mismatch: creation_date %v != %v\n", h1.creation_date, h2.creation_date)
	}
}

func compareTile(stats map_stats, min_zoom_level, x, y int, t1, t2 *TileData, flagDetail bool) {
	if t1 == nil || t2 == nil {
		return
	}
	if len(t1.poi_data) != len(t2.poi_data) {
		fmt.Printf("Tile zi mismatch: %d %d %d - %d != %d\n", min_zoom_level, x, y, len(t1.poi_data), len(t2.poi_data))
		return
	}

	t1.normalize()
	t2.normalize()
	for zi := 0; zi < len(t1.poi_data); zi++ {
		z := min_zoom_level + zi
		var found_diff bool
		if compare_poi_datas(stats, z, x, y, t1.poi_data[zi], t2.poi_data[zi], flagDetail) {
			found_diff = true
		}
		if compare_way_datas(stats, z, x, y, t1.way_data[zi], t2.way_data[zi], flagDetail) {
			found_diff = true
		}
		if !flagDetail && found_diff {
			fmt.Println(z, x, y)
		}
	}
}

func CmdDiff(args []string, flagDetail bool, ignoreComment, ignoreTimestamp bool) error {
	if len(args) != 2 {
		return errors.New("only 2 arguments")
	}

	var ps [2]*MapsforgeParser
	for i, fn := range args {
		p, err := ParseFile(fn, false)
		if err != nil {
			return err
		}

		ps[i] = p
		defer p.Close()
	}

	compareHeaders(&ps[0].data.header, &ps[1].data.header, ignoreComment, ignoreTimestamp)

	if !zic_eq(ps[0].data.header.zoom_interval, ps[1].data.header.zoom_interval) {
		fmt.Println("Warning: zoom interval config mismatch")
	}

	// The tag_id of two map files may be different, so we need to remap them.
	var stats []*map_stats
	for _, p := range ps {
		stats = append(stats, CollectStatsParallel(p))
	}

	merged_stats, poi_mapping, way_mapping := merge_map_tags(stats)

	for i := 0; i < 2; i++ {
		apply_mapping(ps[i], poi_mapping[i], way_mapping[i])
	}

	max_si := min(len(ps[0].data.subfiles), len(ps[1].data.subfiles))
	for si := 0; si < max_si; si++ {
		sf1 := &ps[0].data.subfiles[si]
		sf2 := &ps[1].data.subfiles[si]

		min_x := min(sf1.x, sf2.x)
		max_x := max(sf1.X, sf2.X)
		min_y := min(sf1.y, sf2.y)
		max_y := max(sf1.Y, sf2.Y)

		for x := min_x; x <= max_x; x++ {
			for y := min_y; y <= max_y; y++ {
				t1, _ := ps[0].GetTileData(si, x, y)
				t2, _ := ps[1].GetTileData(si, x, y)
				i1 := ps[0].GetTileIndex(si, x, y)
				i2 := ps[1].GetTileIndex(si, x, y)

				if (i1 == nil) != (i2 == nil) {
					fmt.Printf("Tile existence mismatch: si=%d x=%d y=%d (map1: %v, map2: %v)\n", si, x, y, i1 != nil, i2 != nil)
					continue
				}
				if i1 == nil {
					continue
				}

				if i1.IsWater != i2.IsWater {
					fmt.Printf("Tile water flag mismatch: si=%d x=%d y=%d (map1: %v, map2: %v)\n", si, x, y, i1.IsWater, i2.IsWater)
				}

				compareTile(merged_stats, int(sf1.zoom_interval.min_zoom_level), x, y, t1, t2, flagDetail)
			}
		}

	}

	return nil
}
