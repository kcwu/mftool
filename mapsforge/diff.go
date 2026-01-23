package mapsforge

import (
	"errors"
	"fmt"
)

func is_ignore_poi(poi *POIData) bool {
	return false
}

func is_ignore_way(way *WayProperties, stat *TagsStat) bool {
	if len(way.tag_id) >= 1 {
		tag := way.tag_id[0]
		switch stat.stat[tag].str {
		case "contour_ext=elevation_minor":
			return true
		case "contour_ext=elevation_medium":
			return true
		case "contour_ext=elevation_major":
			return true
		case "natural=land":
			return true
		case "natural=sea":
			return true
		}
	}
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
			if !is_ignore_way(&d1[i], &stats.way_stats) {
				if !detail {
					return true
				}

				found_diff = true
				fmt.Println(z, x, y, "-way", d1[i].ToString(&stats.way_stats))
			}
			i++
		} else if i == len(d1) || d2[j].less(&d1[i]) {
			if !is_ignore_way(&d2[j], &stats.way_stats) {
				if !detail {
					return true
				}

				found_diff = true
				fmt.Println(z, x, y, "+way", d2[j].ToString(&stats.way_stats))
			}
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

func compareTile(stats map_stats, min_zoom_level, x, y int, t1, t2 *TileData, flagDetail bool) {
	if len(t1.poi_data) != len(t2.poi_data) {
		fmt.Println(len(t1.poi_data), len(t2.poi_data))
		panic("error")
	}
	if len(t1.way_data) != len(t2.way_data) {
		panic("error")
	}

	t1.normalize()
	t2.normalize()
	for zi := 0; zi < len(t1.tile_header.zoom_table); zi++ {
		z := min_zoom_level + zi
		var found_diff bool
		if compare_poi_datas(stats, z, x, y, t1.poi_data[zi], t2.poi_data[zi], flagDetail) {
			found_diff = true
		}
		if (!flagDetail && found_diff) || compare_way_datas(stats, z, x, y, t1.way_data[zi], t2.way_data[zi], flagDetail) {
			found_diff = true
		}
		if !flagDetail && found_diff {
			fmt.Println(z, x, y)
		}
	}
}

func CmdDiff(args []string, flagDetail bool) error {
	if len(args) != 2 {
		return errors.New("only 2 arguments")
	}

	var ps [2]*MapsforgeParser
	for i, fn := range args {
		p, err := parseFile(fn, true)
		if err != nil {
			return err
		}

		ps[i] = p
	}

	if !zic_eq(ps[0].data.header.zoom_interval, ps[1].data.header.zoom_interval) {
		return errors.New("zoom interval config mismatch")
	}

	// The tag_id of two map files may be different, so we need to remap them.
	var stats []*map_stats
	for _, p := range ps {
		stats = append(stats, make_map_stats(&p.data.header, p.getTiles()))
	}

	merged_stats, poi_mapping, way_mapping := merge_map_tags(stats)

	for i := 0; i < 2; i++ {
		apply_mapping(ps[i], poi_mapping[i], way_mapping[i])
	}

	for si := 0; si < len(ps[0].data.subfiles); si++ {
		sf1 := &ps[0].data.subfiles[si]
		sf2 := &ps[1].data.subfiles[si]

		// ignore tiles on boundaries
		min_x := max(sf1.x, sf2.x) + 1
		max_x := min(sf1.X, sf2.X) - 1
		min_y := max(sf1.y, sf2.y) + 1
		max_y := min(sf1.Y, sf2.Y) - 1

		for x := min_x; x <= max_x; x++ {
			for y := min_y; y <= max_y; y++ {
				t1, err := ps[0].getTileData(si, x, y)
				if err != nil {
					return err
				}
				t2, err := ps[1].getTileData(si, x, y)
				if err != nil {
					return err
				}
				if t1 == nil || t2 == nil {
					continue
				}

				compareTile(merged_stats, int(sf1.zoom_interval.min_zoom_level), x, y, t1, t2, flagDetail)
			}
		}

	}

	return nil
}
