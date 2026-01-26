package mapsforge

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

type TOMLDumper struct {
	w   io.Writer
	buf []byte
}

func NewTOMLDumper(w io.Writer) *TOMLDumper {
	return &TOMLDumper{w, make([]byte, 0, 64*1024)}
}

func (d *TOMLDumper) Flush() {
	if len(d.buf) > 0 {
		d.w.Write(d.buf)
		d.buf = d.buf[:0]
	}
}

func (d *TOMLDumper) checkFlush() {
	if len(d.buf) > 60*1024 {
		d.Flush()
	}
}

func (d *TOMLDumper) printStr(s string) {
	d.buf = append(d.buf, s...)
	d.checkFlush()
}

func (d *TOMLDumper) printInt(v int) {
	d.buf = strconv.AppendInt(d.buf, int64(v), 10)
	d.checkFlush()
}

func (d *TOMLDumper) printKeyInt(key string, v int) {
	d.buf = append(d.buf, key...)
	d.buf = append(d.buf, " = "...)
	d.buf = strconv.AppendInt(d.buf, int64(v), 10)
	d.buf = append(d.buf, '\n')
	d.checkFlush()
}

func (d *TOMLDumper) printKeyInt64(key string, v int64) {
	d.buf = append(d.buf, key...)
	d.buf = append(d.buf, " = "...)
	d.buf = strconv.AppendInt(d.buf, v, 10)
	d.buf = append(d.buf, '\n')
	d.checkFlush()
}

func (d *TOMLDumper) printKeyUint64(key string, v uint64) {
	d.buf = append(d.buf, key...)
	d.buf = append(d.buf, " = "...)
	d.buf = strconv.AppendUint(d.buf, v, 10)
	d.buf = append(d.buf, '\n')
	d.checkFlush()
}

func (d *TOMLDumper) printKeyBool(key string, v bool) {
	d.buf = append(d.buf, key...)
	d.buf = append(d.buf, " = "...)
	if v {
		d.buf = append(d.buf, "true\n"...)
	} else {
		d.buf = append(d.buf, "false\n"...)
	}
	d.checkFlush()
}

func (d *TOMLDumper) printKeyString(key string, v string) {
	d.buf = append(d.buf, key...)
	d.buf = append(d.buf, " = "...)
	// Quote allocates, but we can't avoid easily without modifying quote.
	// For now, write internal buffer to clean up, then write string to underlying writer?
	// OR just append if fits?
	// s := quote(v)
	// if len(d.buf) + len(s) + 1 > cap(d.buf) { d.Flush() }
	// But quote loops string.
	// Just append to buffer. If buffer overflows, it reallocs. That's bad.
	// checkFlush logic: Flush if > 60KB.
	// If string is huge, buffer grows. Can be fine.
	// But let's stick to append.
	d.buf = append(d.buf, quote(v)...)
	d.buf = append(d.buf, '\n')
	d.checkFlush()
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
	d.printStr("[header]\n")
	d.printKeyUint64("header_size", uint64(h.header_size))
	d.printKeyUint64("file_version", uint64(h.file_version))
	d.printKeyUint64("file_size", h.file_size)
	d.printKeyUint64("creation_date", h.creation_date)
	d.printKeyInt("min_lat", int(h.min.lat))
	d.printKeyInt("min_lon", int(h.min.lon))
	d.printKeyInt("max_lat", int(h.max.lat))
	d.printKeyInt("max_lon", int(h.max.lon))
	d.printKeyInt("tile_size", int(h.tile_size))
	d.printKeyString("projection", h.projection)

	d.printKeyBool("has_debug", h.has_debug)
	d.printKeyBool("has_map_start", h.has_map_start)
	if h.has_map_start {
		d.printKeyInt("start_lat", int(h.start.lat))
		d.printKeyInt("start_lon", int(h.start.lon))
	}
	d.printKeyBool("has_start_zoom", h.has_start_zoom)
	if h.has_start_zoom {
		d.printKeyInt("start_zoom", int(h.start_zoom))
	}
	d.printKeyBool("has_language_preference", h.has_language_preference)
	if h.has_language_preference {
		d.printKeyString("language_preference", h.language_preference)
	}
	d.printKeyBool("has_comment", h.has_comment)
	if h.has_comment {
		d.printKeyString("comment", h.comment)
	}
	d.printKeyBool("has_created_by", h.has_created_by)
	if h.has_created_by {
		d.printKeyString("created_by", h.created_by)
	}

	d.printStr("\npoi_tags = [\n")
	for _, v := range h.poi_tags {
		d.printStr("  ")
		d.printStr(quote(v))
		d.printStr(",\n")
	}
	d.printStr("]\n")

	d.printStr("\nway_tags = [\n")
	for _, v := range h.way_tags {
		d.printStr("  ")
		d.printStr(quote(v))
		d.printStr(",\n")
	}
	d.printStr("]\n\n")
}

func (d *TOMLDumper) DumpZoomIntervals(zics []ZoomIntervalConfig) {
	for _, zic := range zics {
		d.printStr("[[zoom_intervals]]\n")
		d.printKeyInt("base_zoom", int(zic.base_zoom_level))
		d.printKeyInt("min_zoom", int(zic.min_zoom_level))
		d.printKeyInt("max_zoom", int(zic.max_zoom_level))
		d.printKeyUint64("pos", zic.pos)
		d.printKeyUint64("size", zic.size)
		d.printStr("\n")
	}
}

func (d *TOMLDumper) DumpTile(si, x, y int, td *TileData, header *Header, isWater bool) {
	d.printStr("[[tiles]]\n")

	// ID
	d.buf = append(d.buf, `id = "`...)
	d.buf = strconv.AppendInt(d.buf, int64(si), 10)
	d.buf = append(d.buf, '/')
	d.buf = strconv.AppendInt(d.buf, int64(x), 10)
	d.buf = append(d.buf, '/')
	d.buf = strconv.AppendInt(d.buf, int64(y), 10)
	d.buf = append(d.buf, '"', '\n')
	d.checkFlush()

	d.printKeyInt("si", si)
	d.printKeyInt("x", x)
	d.printKeyInt("y", y)
	d.printKeyBool("is_water", isWater)

	if td == nil {
		d.printStr("# no data\n\n")
		return
	}

	d.printKeyUint64("first_way_offset", uint64(td.tile_header.first_way_offset))

	// Dump POIs
	for zi, pois := range td.poi_data {
		for _, poi := range pois {
			d.printStr("\n[[tiles.pois]]\n")
			d.printKeyInt("zi_index", zi)
			d.printKeyInt("lat", int(poi.lat))
			d.printKeyInt("lon", int(poi.lon))
			d.printKeyInt("layer", int(poi.layer))

			d.printStr("tags = [")
			for i, tagID := range poi.tag_id {
				if i > 0 {
					d.printStr(", ")
				}
				d.printStr(quote(header.poi_tags[tagID]))
			}
			d.printStr("]\n")

			if poi.has_name {
				d.printKeyString("name", poi.name)
			}
			if poi.has_house_number {
				d.printKeyString("house_number", poi.house_number)
			}
			if poi.has_elevation {
				d.printKeyInt("elevation", int(poi.elevation))
			}
		}
	}

	// Dump Ways
	for zi, ways := range td.way_data {
		for _, way := range ways {
			d.printStr("\n[[tiles.ways]]\n")
			d.printKeyInt("zi_index", zi)
			d.printKeyInt("layer", int(way.layer))
			d.printKeyInt("sub_tile_bitmap", int(way.sub_tile_bitmap))
			d.printKeyBool("encoding", way.encoding)

			d.printStr("tags = [")
			for i, tagID := range way.tag_id {
				if i > 0 {
					d.printStr(", ")
				}
				d.printStr(quote(header.way_tags[tagID]))
			}
			d.printStr("]\n")

			if way.has_name {
				d.printKeyString("name", way.name)
			}
			if way.has_house_number {
				d.printKeyString("house_number", way.house_number)
			}
			if way.has_reference {
				d.printKeyString("reference", way.reference)
			}
			if way.has_label_position {
				d.printKeyInt("label_lat", int(way.label_position.lat))
				d.printKeyInt("label_lon", int(way.label_position.lon))
			}

			// Blocks
			d.printStr("blocks = [\n")
			for _, block := range way.block {
				d.printStr("  [\n")
				for _, nodes := range block.data {
					d.printStr("    [")
					for i, node := range nodes {
						if i > 0 {
							d.printStr(", ")
						}
						d.printStr("[")
						d.printInt(int(node.lat))
						d.printStr(", ")
						d.printInt(int(node.lon))
						d.printStr("]")
					}
					d.printStr("],\n")
				}
				d.printStr("  ],\n")
			}
			d.printStr("]\n")
		}
	}
	d.printStr("\n")
}
