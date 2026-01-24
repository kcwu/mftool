package mapsforge

import (
	"fmt"
	"runtime"
	"sort"
)

const MAX_ZOOM = 20

type tag_stat struct {
	str           string
	count, appear int
	at            [MAX_ZOOM]int
}

type TagsStat struct {
	stat []tag_stat
}

type map_stats struct {
	poi_stats TagsStat
	way_stats TagsStat
}

type ByCount []tag_stat

func (a ByCount) Len() int      { return len(a) }
func (a ByCount) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a ByCount) Less(i, j int) bool {
	if a[i].count != a[j].count {
		return a[i].count > a[j].count
	}
	return a[i].str < a[j].str
}

type ByStr []tag_stat

func (a ByStr) Len() int      { return len(a) }
func (a ByStr) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a ByStr) Less(i, j int) bool {
	return a[i].str < a[j].str
}

func make_map_stats(header *Header, tiles []Tile) *map_stats {
	var result map_stats

	result.poi_stats.init(header.poi_tags)
	result.way_stats.init(header.way_tags)
	for _, tile := range tiles {
		for _, poi := range *tile.pois {
			for _, tag := range poi.tag_id {
				result.poi_stats.add(tag, tile.zoom, 1)
			}
		}
		for _, way := range *tile.ways {
			for _, tag := range way.tag_id {
				result.way_stats.add(tag, tile.zoom, 1)
			}
		}
	}

	return &result
}

func CollectStatsParallel(p *MapsforgeParser) *map_stats {
	numCPU := runtime.NumCPU()

	type job struct {
		si, x, y int
	}
	jobs := make(chan job, numCPU*4)
	results := make(chan *map_stats, numCPU)

	// Worker
	for w := 0; w < numCPU; w++ {
		go func() {
			stats := &map_stats{}
			// Ensure we initialize stats with empty tag lists so find_tag_by_str works consistently
			// Actually, we can just start empty and let merge union them.
			// But for consistent IDs? Local stats use local IDs. Merge handles remapping.
			stats.poi_stats.init(nil)
			stats.way_stats.init(nil)

			for j := range jobs {
				td, err := p.GetTileData(j.si, j.x, j.y)
				if err != nil || td == nil {
					continue
				}

				// Calculate zoom
				sf := &p.data.subfiles[j.si]
				minZoom := sf.zoom_interval.min_zoom_level

				for zi, pois := range td.poi_data {
					zoom := int(minZoom) + zi
					for _, poi := range pois {
						for _, tag := range poi.tag_id {
							// For local stats, we need to map tag ID from HEADER (global to file) to LOCAL stat index.
							// The header tags are fixed.
							// We should pre-populate local stats with header tags?
							// Or better: `map_stats` builds its own string table.
							// `td` has `tag_id` which are indices into `p.data.header.poi_tags`.
							tagStr := p.data.header.poi_tags[tag]
							tagIdx := stats.poi_stats.find_tag_by_str(tagStr)
							stats.poi_stats.add(tagIdx, zoom, 1)
						}
					}
				}
				for zi, ways := range td.way_data {
					zoom := int(minZoom) + zi
					for _, way := range ways {
						for _, tag := range way.tag_id {
							tagStr := p.data.header.way_tags[tag]
							tagIdx := stats.way_stats.find_tag_by_str(tagStr)
							stats.way_stats.add(tagIdx, zoom, 1)
						}
					}
				}
			}
			results <- stats
		}()
	}

	// Dispatcher
	go func() {
		for si, sf := range p.data.subfiles {
			for x := sf.x; x <= sf.X; x++ {
				for y := sf.y; y <= sf.Y; y++ {
					// We can check index first to skip empty tiles?
					// GetTileData checks cache and index.
					// Reading index is cheap?
					// Let's iterate all valid x,y.
					// GetTileData ensures efficient skip if no data offset change.
					jobs <- job{si, x, y}
				}
			}
		}
		close(jobs)
	}()

	// Collector
	var statsList []*map_stats
	for w := 0; w < numCPU; w++ {
		statsList = append(statsList, <-results)
	}

	// Merge partial results
	// map_stats is struct { poi_stats, way_stats TagsStat }
	// We can use merge_tags logic.

	var finalStats map_stats
	var poi_stats_list []TagsStat
	var way_stats_list []TagsStat
	for _, s := range statsList {
		poi_stats_list = append(poi_stats_list, s.poi_stats)
		way_stats_list = append(way_stats_list, s.way_stats)
	}

	finalStats.poi_stats, _ = merge_tags(poi_stats_list)
	finalStats.way_stats, _ = merge_tags(way_stats_list)

	return &finalStats
}

func (tc *TagsStat) init(strs []string) {
	n := len(strs)
	tc.stat = make([]tag_stat, n)
	for i := range tc.stat {
		tc.stat[i].str = strs[i]
		tc.stat[i].appear = 9999
	}
}
func (tc *TagsStat) add(tag uint32, zoom int, count int) {
	tc.stat[tag].count += count
	tc.stat[tag].at[zoom] += count
	tc.stat[tag].appear = min(tc.stat[tag].appear, zoom)
}

func (tc *TagsStat) find_tag_by_str(str string) uint32 {
	for i, stat := range tc.stat {
		if stat.str == str {
			return uint32(i)
		}
	}
	tc.stat = append(tc.stat, tag_stat{str: str, appear: 9999})
	return uint32(len(tc.stat) - 1)
}

func merge_tags(tcs []TagsStat) (result TagsStat, mapping [][]uint32) {
	for _, tc := range tcs {
		for _, stat := range tc.stat {
			idx := result.find_tag_by_str(stat.str)
			for z := range stat.at {
				if stat.at[z] == 0 {
					continue
				}
				result.add(idx, z, stat.at[z])
			}
		}
	}

	// Sort by count descending, then by string ascending for stability
	sort.Sort(ByCount(result.stat))

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

func merge_map_tags(ms []*map_stats) (result map_stats, poi_mapping [][]uint32, way_mapping [][]uint32) {
	var poi_stats []TagsStat
	for _, s := range ms {
		poi_stats = append(poi_stats, s.poi_stats)
	}
	result.poi_stats, poi_mapping = merge_tags(poi_stats)

	var way_stats []TagsStat
	for _, s := range ms {
		way_stats = append(way_stats, s.way_stats)
	}
	result.way_stats, way_mapping = merge_tags(way_stats)
	return
}


func (tc *TagsStat) print(prefix string) {
	// calculate width
	width_of := func(v int) int {
		w := 0
		for v > 0 {
			w++
			v /= 10
		}
		return w
	}

	tag_width := 0
	var width_for_zoom [MAX_ZOOM]int
	for _, stat := range tc.stat {
		tag_width = max(tag_width, len(stat.str))
		for z, c := range stat.at {
			width_for_zoom[z] = max(width_for_zoom[z], width_of(c))
		}
	}

	var header string
	header += fmt.Sprintf("%s %*s", prefix, tag_width, "")
	min_z, max_z := 100, 0
	for z, c := range width_for_zoom {
		if c != 0 {
			min_z = min(min_z, z)
			max_z = max(max_z, z)
		}
	}
	// hack
	reserved_spaces := []int{0,
		3, 3, 4, 3, 4,
		4, 4, 5, 5, 5,
		5, 6, 6, 6, 6,
		4, 5, 4, 4, 4,
	}
	for z := min_z; z <= max_z; z++ {
		width_for_zoom[z] = max(width_for_zoom[z], width_of(z))
		// hack
		width_for_zoom[z] = max(width_for_zoom[z], reserved_spaces[z])
		header += fmt.Sprintf("%*d ", width_for_zoom[z], z)
	}

	for i, stat := range tc.stat {
		if i%20 == 0 {
			fmt.Println(header)
		}
		fmt.Printf("%s %*s", prefix, -tag_width, stat.str)
		for z, c := range stat.at {
			if width_for_zoom[z] == 0 {
				continue
			} else if c == 0 {
				fmt.Printf("%*s", width_for_zoom[z], "")
			} else {
				// hack
				for xx := 10000000; xx > 0; xx /= 10 {
					if c > xx {
						c -= c % xx
					}
				}
				fmt.Printf("%*d", width_for_zoom[z], c)
			}
			if z < max_z {
				fmt.Print(",")
			}
		}
		fmt.Println()
	}
}

func apply_mapping(p *MapsforgeParser, poi_mapping []uint32, way_mapping []uint32) {

	for _, tile := range p.getTiles() {
		for _, poi := range *tile.pois {
			for i, tag := range poi.tag_id {
				poi.tag_id[i] = poi_mapping[tag]
			}
		}
		for _, way := range *tile.ways {
			for i, tag := range way.tag_id {
				way.tag_id[i] = way_mapping[tag]
			}
		}
	}
}

func (td *TileData) normalize() {
	for _, pois := range td.poi_data {
		for _, poi := range pois {
			sort.Sort(Uint32Slice(poi.tag_id))
		}
		sort.Sort(CmpByPOIData(pois))
	}
	for _, ways := range td.way_data {
		for _, way := range ways {
			sort.Sort(Uint32Slice(way.tag_id))
		}
		sort.Sort(CmpByWayData(ways))
	}
}

func CmdTags(args []string) error {
	chanp := make(chan *MapsforgeParser)
	for _, fn := range args {
		go func(fn string) {
			p, err := ParseFile(fn, true)
			if err != nil {
				fmt.Println(fn, err)
			}
			chanp <- p
		}(fn)
	}

	var ps []*MapsforgeParser
	for _ = range args {
		p := <-chanp
		ps = append(ps, p)
	}

	var stats []*map_stats
	for _, p := range ps {
		stats = append(stats, make_map_stats(&p.data.header, p.getTiles()))
	}

	merged, _, _ := merge_map_tags(stats)

	sort.Sort(ByStr(merged.poi_stats.stat))
	merged.poi_stats.print("poi_tag")
	fmt.Println("---------------------------------------------------------------------------------------")
	sort.Sort(ByStr(merged.way_stats.stat))
	merged.way_stats.print("way_tag")
	return nil
}
