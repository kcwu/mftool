package mapsforge

import (
	"fmt"
)

func (d *MapsforgeData) dump_header() {
	h := d.header
	fmt.Println("header_size:", h.header_size)
	fmt.Println("file_version:", h.file_version)
	fmt.Println("file_size:", h.file_size)
	fmt.Println("creation_date:", h.creation_date)
	fmt.Println("min:", h.min.ToString())
	fmt.Println("max:", h.max.ToString())
	fmt.Println("tile_size:", h.tile_size)
	fmt.Printf("projection: %#v\n", h.projection)
	fmt.Println()

	fmt.Println("optional")
	fmt.Println("has_debug:", h.has_debug)
	if h.has_map_start {
		fmt.Println("map_start:", h.start.ToString())
	}
	if h.has_start_zoom {
		fmt.Println("start_zoom:", h.start_zoom)
	}
	if h.has_language_preference {
		fmt.Printf("language_preference: %#v\n", h.language_preference)
	}
	if h.has_comment {
		fmt.Printf("comment: %#v\n", h.comment)
	}
	if h.has_created_by {
		fmt.Printf("created_by: %#v\n", h.created_by)
	}
	fmt.Println()

	fmt.Println("num_poi_tags:", len(h.poi_tags))
	for i, v := range h.poi_tags {
		fmt.Printf("[%d]%s ", i, v)
	}
	fmt.Println()
	fmt.Println()

	fmt.Println("num_way_tags:", len(h.way_tags))
	for i, v := range h.way_tags {
		fmt.Printf("[%d]%s ", i, v)
	}
	fmt.Println()
	fmt.Println()

	fmt.Println("num_zoom_interval:", len(h.zoom_interval))
	for i, v := range h.zoom_interval {
		fmt.Printf("[%d] base_zoom_level=%d, min_zoom_level=%d, max_zoom_level=%d, pos=%d, size=%d\n",
			i,
			v.base_zoom_level, v.min_zoom_level, v.max_zoom_level, v.pos, v.size)
	}

	for si, sf := range d.subfiles {
		fmt.Printf("si=%d x=[%d,%d] y=[%d,%d]\n",
			si, sf.x, sf.X, sf.y, sf.Y)
	}

}

func (td *TileData) dump(header *Header) {
	for zi, zt := range td.tile_header.zoom_table {
		fmt.Printf("zoom_table[%d] num_pois=%d num_ways=%d\n",
			zi, zt.num_pois, zt.num_ways)
	}
	fmt.Println("first_way_offset:", td.tile_header.first_way_offset)
	for zi := 0; zi < len(td.tile_header.zoom_table); zi++ {
		fmt.Println("zi:", zi)
		for pi, poi := range td.poi_data[zi] {
			fmt.Printf("poi[zi=%d,%d] %s layer=%d", zi, pi, poi.LatLon.ToString(), poi.layer)
			for _, tag := range poi.tag_id {
				fmt.Printf(" %d(%s)", tag, header.poi_tags[tag])
			}

			if poi.has_name {
				fmt.Printf(" name=%#v", poi.name)
			}
			if poi.has_house_number {
				fmt.Printf(" house_number=%s", poi.house_number)
			}
			if poi.has_elevation {
				fmt.Printf(" ele=%d", poi.elevation)
			}
			fmt.Println()
		}

	}
	for zi := 0; zi < len(td.tile_header.zoom_table); zi++ {
		fmt.Println("zi:", zi)
		for wi, way := range td.way_data[zi] {
			fmt.Printf("way[zi=%d,%d] layer=%d sub_tile_bitmap=%04x", zi, wi, way.layer, way.sub_tile_bitmap)
			for _, tag := range way.tag_id {
				fmt.Printf(" %d(%s)", tag, header.way_tags[tag])
			}

			if way.has_name {
				fmt.Printf(" name=%#v", way.name)
			}
			if way.has_house_number {
				fmt.Printf(" house_number=%s", way.house_number)
			}
			if way.has_reference {
				fmt.Printf(" ref=%s", way.reference)
			}
			if way.has_label_position {
				fmt.Printf(" label_position=%s", way.label_position.ToString())
			}
			if way.has_num_way_blocks {
				fmt.Printf(" num_way_block=%d", way.num_way_block)
			}
			if way.encoding {
				fmt.Printf(" encoding=double_delta")
			} else {
				fmt.Printf(" encoding=single_delta")
			}
			fmt.Println()

			for bi, block := range way.block {
				for segment_idx, segment := range block.data {
					fmt.Printf("  block[%d] segment[%d]:", bi, segment_idx)
					for _, node := range segment {
						fmt.Printf(" %s", node.ToString())
					}
					fmt.Println()
				}
			}
		}
	}

}

func (sf *SubFile) dump_indexes() {
	fmt.Println("indexes:", len(sf.tile_indexes))
	for i := 0; i < len(sf.tile_indexes)-1; i++ {
		v := sf.tile_indexes[i]
		if v.IsWater {
			fmt.Printf("[%d]%d,%d\n", i, 1, v.Offset)
		} else {
			fmt.Printf("[%d]%d,%d\n", i, 0, v.Offset)
		}
	}
	fmt.Println()
}

func CmdDump(args []string, flagHeader bool, flagAll bool, flagTile string) error {
	fn := args[0]

	p, err := ParseFile(fn, false)
	if err != nil {
		return err
	}
	defer p.Close()
	if flagHeader || flagAll {
		p.data.dump_header()
	}

	if flagAll {
		for si, sf := range p.data.subfiles {
			for x := sf.x; x <= sf.X; x++ {
				for y := sf.y; y <= sf.Y; y++ {
					td, err := p.GetTileData(si, x, y)
					if err != nil {
						return err
					}
					if td == nil {
						fmt.Println("no data to dump")
					} else {
						td.dump(&p.data.header)
					}
				}
			}

		}
	}
	if flagTile != "" {
		var si, x, y int
		_, err := fmt.Sscanf(flagTile, "%d,%d,%d", &si, &x, &y)
		if err != nil {
			return err
		}

		td, err := p.GetTileData(si, x, y)
		if err != nil {
			return err
		}
		if td == nil {
			fmt.Println("no data to dump")
		} else {
			td.dump(&p.data.header)
		}
	}
	return nil
}
