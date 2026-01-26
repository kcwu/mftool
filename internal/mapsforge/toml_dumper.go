package mapsforge

import (
	"fmt"
	"io"
	"strings"
)

type TOMLDumper struct {
	w io.Writer
}

func NewTOMLDumper(w io.Writer) *TOMLDumper {
	return &TOMLDumper{w}
}

func (d *TOMLDumper) printf(format string, args ...interface{}) {
	fmt.Fprintf(d.w, format, args...)
}

func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range s {
		switch c {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 {
				fmt.Fprintf(&b, "\\u%04x", c)
			} else {
				b.WriteRune(c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func (d *TOMLDumper) DumpHeader(h *Header) {
	d.printf("[header]\n")
	d.printf("header_size = %d\n", h.header_size)
	d.printf("file_version = %d\n", h.file_version)
	d.printf("file_size = %d\n", h.file_size)
	d.printf("creation_date = %d\n", h.creation_date)
	d.printf("min_lat = %d\n", h.min.lat)
	d.printf("min_lon = %d\n", h.min.lon)
	d.printf("max_lat = %d\n", h.max.lat)
	d.printf("max_lon = %d\n", h.max.lon)
	d.printf("tile_size = %d\n", h.tile_size)
	d.printf("projection = %s\n", quote(h.projection))

	d.printf("has_debug = %v\n", h.has_debug)
	d.printf("has_map_start = %v\n", h.has_map_start)
	if h.has_map_start {
		d.printf("start_lat = %d\n", h.start.lat)
		d.printf("start_lon = %d\n", h.start.lon)
	}
	d.printf("has_start_zoom = %v\n", h.has_start_zoom)
	if h.has_start_zoom {
		d.printf("start_zoom = %d\n", h.start_zoom)
	}
	d.printf("has_language_preference = %v\n", h.has_language_preference)
	if h.has_language_preference {
		d.printf("language_preference = %s\n", quote(h.language_preference))
	}
	d.printf("has_comment = %v\n", h.has_comment)
	if h.has_comment {
		d.printf("comment = %s\n", quote(h.comment))
	}
	d.printf("has_created_by = %v\n", h.has_created_by)
	if h.has_created_by {
		d.printf("created_by = %s\n", quote(h.created_by))
	}

	d.printf("\npoi_tags = [\n")
	for _, v := range h.poi_tags {
		d.printf("  %s,\n", quote(v))
	}
	d.printf("]\n")

	d.printf("\nway_tags = [\n")
	for _, v := range h.way_tags {
		d.printf("  %s,\n", quote(v))
	}
	d.printf("]\n\n")
}

func (d *TOMLDumper) DumpZoomIntervals(zics []ZoomIntervalConfig) {
	for _, zic := range zics {
		d.printf("[[zoom_intervals]]\n")
		d.printf("base_zoom = %d\n", zic.base_zoom_level)
		d.printf("min_zoom = %d\n", zic.min_zoom_level)
		d.printf("max_zoom = %d\n", zic.max_zoom_level)
		d.printf("pos = %d\n", zic.pos)
		d.printf("size = %d\n", zic.size)
		d.printf("\n")
	}
}

func (d *TOMLDumper) DumpTile(si, x, y int, td *TileData, header *Header, isWater bool) {
	d.printf("[[tiles]]\n")
	d.printf("id = \"%d/%d/%d\"\n", si, x, y)
	d.printf("si = %d\n", si)
	d.printf("x = %d\n", x)
	d.printf("y = %d\n", y)
	d.printf("is_water = %v\n", isWater)

	if td == nil {
		d.printf("# no data\n\n")
		return
	}

	d.printf("first_way_offset = %d\n", td.tile_header.first_way_offset)

	// Dump POIs
	for zi, pois := range td.poi_data {
		for _, poi := range pois {
			d.printf("\n[[tiles.pois]]\n")
			d.printf("zi_index = %d\n", zi)
			d.printf("lat = %d\n", poi.lat)
			d.printf("lon = %d\n", poi.lon)
			d.printf("layer = %d\n", poi.layer)

			d.printf("tags = [")
			for i, tagID := range poi.tag_id {
				if i > 0 {
					d.printf(", ")
				}
				d.printf("%s", quote(header.poi_tags[tagID]))
			}
			d.printf("]\n")

			if poi.has_name {
				d.printf("name = %s\n", quote(poi.name))
			}
			if poi.has_house_number {
				d.printf("house_number = %s\n", quote(poi.house_number))
			}
			if poi.has_elevation {
				d.printf("elevation = %d\n", poi.elevation)
			}
		}
	}

	// Dump Ways
	for zi, ways := range td.way_data {
		for _, way := range ways {
			d.printf("\n[[tiles.ways]]\n")
			d.printf("zi_index = %d\n", zi)
			d.printf("layer = %d\n", way.layer)
			d.printf("sub_tile_bitmap = %d\n", way.sub_tile_bitmap)
			d.printf("encoding = %v\n", way.encoding)

			d.printf("tags = [")
			for i, tagID := range way.tag_id {
				if i > 0 {
					d.printf(", ")
				}
				d.printf("%s", quote(header.way_tags[tagID]))
			}
			d.printf("]\n")

			if way.has_name {
				d.printf("name = %s\n", quote(way.name))
			}
			if way.has_house_number {
				d.printf("house_number = %s\n", quote(way.house_number))
			}
			if way.has_reference {
				d.printf("reference = %s\n", quote(way.reference))
			}
			if way.has_label_position {
				d.printf("label_lat = %d\n", way.label_position.lat)
				d.printf("label_lon = %d\n", way.label_position.lon)
			}

			// Blocks
			d.printf("blocks = [\n")
			for _, block := range way.block {
				d.printf("  [\n")
				for _, nodes := range block.data {
					d.printf("    [")
					for i, node := range nodes {
						if i > 0 {
							d.printf(", ")
						}
						d.printf("[%d, %d]", node.lat, node.lon)
					}
					d.printf("],\n")
				}
				d.printf("  ],\n")
			}
			d.printf("]\n")
		}
	}
	d.printf("\n")
}
