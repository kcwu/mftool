package mapsforge

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
)

type raw_writer struct {
	data []byte
}

func newRawWriter() *raw_writer {
	return &raw_writer{data: make([]byte, 0, 1024)}
}

func (w *raw_writer) Reset() {
	w.data = w.data[:0]
}

func (w *raw_writer) Bytes() []byte {
	return w.data
}

func (w *raw_writer) uint8(v uint8) {
	w.data = append(w.data, v)
}

func (w *raw_writer) uint16(v uint16) {
	w.data = binary.BigEndian.AppendUint16(w.data, v)
}

func (w *raw_writer) uint32(v uint32) {
	w.data = binary.BigEndian.AppendUint32(w.data, v)
}

func (w *raw_writer) uint64(v uint64) {
	w.data = binary.BigEndian.AppendUint64(w.data, v)
}

func (w *raw_writer) int32(v int32) {
	w.data = binary.BigEndian.AppendUint32(w.data, uint32(v))
}

func (w *raw_writer) VbeU(v uint32) {
	if v < 0x80 {
		w.data = append(w.data, uint8(v))
		return
	}

	for {
		b := uint8(v & 0x7f)
		v >>= 7
		if v == 0 {
			w.data = append(w.data, b)
			break
		}
		w.data = append(w.data, b|0x80)
	}
}

func (w *raw_writer) VbeS(v int32) {
	abs_v := v
	sign := false
	if v < 0 {
		abs_v = -v
		sign = true
	}

	v_u := uint32(abs_v)
	if v_u < 0x40 {
		b := uint8(v_u)
		if sign {
			b |= 0x40
		}
		w.data = append(w.data, b)
		return
	}

	for {
		if v_u < 0x40 { // fits in 6 bits
			b := uint8(v_u)
			if sign {
				b |= 0x40
			}
			w.data = append(w.data, b)
			break
		}
		w.data = append(w.data, uint8(v_u&0x7f)|0x80)
		v_u >>= 7
	}
}

func (w *raw_writer) VbeString(s string) {
	w.VbeU(uint32(len(s)))
	w.data = append(w.data, s...)
}

func (w *raw_writer) fixedString(s string, size int) {
	if len(s) > size {
		w.data = append(w.data, s[:size]...)
	} else {
		w.data = append(w.data, s...)
		for i := 0; i < size-len(s); i++ {
			w.data = append(w.data, 0)
		}
	}
}

type MapsforgeWriter struct {
	w        io.WriteSeeker
	HasDebug bool
}

func NewMapsforgeWriter(w io.WriteSeeker) *MapsforgeWriter {
	return &MapsforgeWriter{w: w}
}

func (mw *MapsforgeWriter) WriteHeader(h *Header) error {
	rw := newRawWriter()

	// Magic
	rw.fixedString(mapsforge_file_magic, 20)

	rw.uint32(0) // header size placeholder
	rw.uint32(h.file_version)
	rw.uint64(h.file_size)
	rw.uint64(h.creation_date)
	rw.int32(h.min.lat)
	rw.int32(h.min.lon)
	rw.int32(h.max.lat)
	rw.int32(h.max.lon)
	rw.uint16(h.tile_size)
	rw.VbeString(h.projection)

	var flags uint8
	if h.has_debug {
		flags |= 0x80
	}
	if h.has_map_start {
		flags |= 0x40
	}
	if h.has_start_zoom {
		flags |= 0x20
	}
	if h.has_language_preference {
		flags |= 0x10
	}
	if h.has_comment {
		flags |= 0x08
	}
	if h.has_created_by {
		flags |= 0x04
	}
	rw.uint8(flags)

	if h.has_map_start {
		rw.int32(h.start.lat)
		rw.int32(h.start.lon)
	}
	if h.has_start_zoom {
		rw.uint8(h.start_zoom)
	}
	if h.has_language_preference {
		rw.VbeString(h.language_preference)
	}
	if h.has_comment {
		rw.VbeString(h.comment)
	}
	if h.has_created_by {
		rw.VbeString(h.created_by)
	}

	rw.uint16(uint16(len(h.poi_tags)))
	for _, tag := range h.poi_tags {
		rw.VbeString(tag)
	}
	rw.uint16(uint16(len(h.way_tags)))
	for _, tag := range h.way_tags {
		rw.VbeString(tag)
	}

	rw.uint8(uint8(len(h.zoom_interval)))
	// zoomIntervalOffset := rw.Bytes()
	// zoomIntervalPos := len(zoomIntervalOffset)

	// Placeholder for zoom interval config
	for i := 0; i < len(h.zoom_interval); i++ {
		rw.uint8(0)  // base
		rw.uint8(0)  // min
		rw.uint8(0)  // max
		rw.uint64(0) // pos
		rw.uint64(0) // size
	}

	// Calculate header size (excluding magic and header size field)
	headerSize := uint32(len(rw.data) - 24)
	binary.BigEndian.PutUint32(rw.data[20:24], headerSize)

	h.header_size = headerSize // cache it

	// Write everything to file
	mw.HasDebug = h.has_debug
	_, err := mw.w.Write(rw.Bytes())
	return err
}

var bufferPool = sync.Pool{
	New: func() interface{} {
		return new(bytes.Buffer)
	},
}

func (mw *MapsforgeWriter) WriteTileData(td *TileData, x, y int) ([]byte, error) {
	rw := newRawWriter()

	if mw.HasDebug {
		rw.fixedString(fmt.Sprintf("###TileStart%d,%d###", x, y), 32)
	}

	// In the specification, first_way_offset is relative to the byte AFTER itself.
	// But we need to write the zoom table first.

	zooms := len(td.tile_header.zoom_table)
	for zi := 0; zi < zooms; zi++ {
		rw.VbeU(td.tile_header.zoom_table[zi].num_pois)
		rw.VbeU(td.tile_header.zoom_table[zi].num_ways)
	}

	poiWriter := newRawWriter()
	for zi := 0; zi < zooms; zi++ {
		for i, poi := range td.poi_data[zi] {
			mw.writePOIData(poiWriter, &poi, i)
		}
	}

	wayWriter := newRawWriter()
	for zi := 0; zi < zooms; zi++ {
		for i, way := range td.way_data[zi] {
			mw.writeWayProperties(wayWriter, &way, i)
		}
	}

	// Now we can write firstWayOffset
	rw.VbeU(uint32(len(poiWriter.data)))
	rw.data = append(rw.data, poiWriter.data...)
	rw.data = append(rw.data, wayWriter.data...)

	return rw.data, nil
}

func (mw *MapsforgeWriter) writePOIData(w *raw_writer, pd *POIData, index int) {
	if mw.HasDebug {
		w.fixedString(fmt.Sprintf("***POIStart%d***", index), 32)
	}
	w.VbeS(pd.lat)
	w.VbeS(pd.lon)

	special := uint8(pd.layer+5)<<4 | uint8(len(pd.tag_id)&0xf)
	w.uint8(special)
	for _, tag := range pd.tag_id {
		w.VbeU(tag)
	}

	var flags uint8
	if pd.has_name {
		flags |= 0x80
	}
	if pd.has_house_number {
		flags |= 0x40
	}
	if pd.has_elevation {
		flags |= 0x20
	}
	w.uint8(flags)

	if pd.has_name {
		w.VbeString(pd.name)
	}
	if pd.has_house_number {
		w.VbeString(pd.house_number)
	}
	if pd.has_elevation {
		w.VbeS(pd.elevation)
	}
}

func (mw *MapsforgeWriter) writeWayProperties(w *raw_writer, wp *WayProperties, index int) {
	if mw.HasDebug {
		w.fixedString(fmt.Sprintf("---WayStart%d---", index), 32)
	}
	// We need to calculate way_data_size which excludes signature and way_data_size itself.
	// Let's write way data to a buffer first.
	ww := newRawWriter()

	ww.uint16(wp.sub_tile_bitmap)
	special := uint8(wp.layer+5)<<4 | uint8(len(wp.tag_id)&0xf)
	ww.uint8(special)
	for _, tag := range wp.tag_id {
		ww.VbeU(tag)
	}

	var flags uint8
	if wp.has_name {
		flags |= 0x80
	}
	if wp.has_house_number {
		flags |= 0x40
	}
	if wp.has_reference {
		flags |= 0x20
	}
	if wp.has_label_position {
		flags |= 0x10
	}
	if wp.has_num_way_blocks {
		flags |= 0x08
	}
	if wp.encoding {
		flags |= 0x04
	}
	ww.uint8(flags)

	if wp.has_name {
		ww.VbeString(wp.name)
	}
	if wp.has_house_number {
		ww.VbeString(wp.house_number)
	}
	if wp.has_reference {
		ww.VbeString(wp.reference)
	}
	if wp.has_label_position {
		ww.VbeS(wp.label_position.lat)
		ww.VbeS(wp.label_position.lon)
	}
	if wp.has_num_way_blocks {
		ww.VbeU(wp.num_way_block)
	}

	// Way data blocks
	for _, block := range wp.block {
		ww.VbeU(uint32(len(block.data))) // amount of way coordinate blocks
		// Wait, parser says:
		/*
			num_way := r.VbeU()
			wp.block[bi].data = make([][]LatLon, num_way)
			for wi := uint32(0); wi < num_way; wi++ {
				num_node := r.VbeU()
		*/
		// Parser treats WayData as multiple segments?
		for _, nodes := range block.data {
			ww.VbeU(uint32(len(nodes)))
			for _, node := range nodes {
				ww.VbeS(node.lat)
				ww.VbeS(node.lon)
			}
		}
	}

	w.VbeU(uint32(len(ww.data)))
	w.data = append(w.data, ww.data...)
}

func (mw *MapsforgeWriter) FinalizeHeader(h *Header) error {
	rw := newRawWriter()

	// Magic
	rw.fixedString(mapsforge_file_magic, 20)
	rw.uint32(h.header_size)
	rw.uint32(h.file_version)
	rw.uint64(h.file_size)
	rw.uint64(h.creation_date)
	rw.int32(h.min.lat)
	rw.int32(h.min.lon)
	rw.int32(h.max.lat)
	rw.int32(h.max.lon)
	rw.uint16(h.tile_size)
	rw.VbeString(h.projection)

	var flags uint8
	if h.has_debug {
		flags |= 0x80
	}
	if h.has_map_start {
		flags |= 0x40
	}
	if h.has_start_zoom {
		flags |= 0x20
	}
	if h.has_language_preference {
		flags |= 0x10
	}
	if h.has_comment {
		flags |= 0x08
	}
	if h.has_created_by {
		flags |= 0x04
	}
	rw.uint8(flags)

	if h.has_map_start {
		rw.int32(h.start.lat)
		rw.int32(h.start.lon)
	}
	if h.has_start_zoom {
		rw.uint8(h.start_zoom)
	}
	if h.has_language_preference {
		rw.VbeString(h.language_preference)
	}
	if h.has_comment {
		rw.VbeString(h.comment)
	}
	if h.has_created_by {
		rw.VbeString(h.created_by)
	}

	rw.uint16(uint16(len(h.poi_tags)))
	for _, tag := range h.poi_tags {
		rw.VbeString(tag)
	}
	rw.uint16(uint16(len(h.way_tags)))
	for _, tag := range h.way_tags {
		rw.VbeString(tag)
	}

	rw.uint8(uint8(len(h.zoom_interval)))
	for i := 0; i < len(h.zoom_interval); i++ {
		zic := h.zoom_interval[i]
		rw.uint8(zic.base_zoom_level)
		rw.uint8(zic.min_zoom_level)
		rw.uint8(zic.max_zoom_level)
		rw.uint64(zic.pos)
		rw.uint64(zic.size)
	}

	mw.w.Seek(0, io.SeekStart)
	_, err := mw.w.Write(rw.Bytes())
	return err
}
