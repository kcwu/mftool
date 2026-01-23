package mapsforge

import (
	"bytes"
	"encoding/binary"
	"io"
)

type raw_writer struct {
	w       io.Writer
	scratch [8]byte
}

func newRawWriter(w io.Writer) *raw_writer {
	return &raw_writer{w: w}
}

func (w *raw_writer) uint8(v uint8) {
	w.scratch[0] = v
	w.w.Write(w.scratch[:1])
}

func (w *raw_writer) uint16(v uint16) {
	binary.BigEndian.PutUint16(w.scratch[:], v)
	w.w.Write(w.scratch[:2])
}

func (w *raw_writer) uint32(v uint32) {
	binary.BigEndian.PutUint32(w.scratch[:], v)
	w.w.Write(w.scratch[:4])
}

func (w *raw_writer) uint64(v uint64) {
	binary.BigEndian.PutUint64(w.scratch[:], v)
	w.w.Write(w.scratch[:8])
}

func (w *raw_writer) int32(v int32) {
	binary.BigEndian.PutUint32(w.scratch[:], uint32(v))
	w.w.Write(w.scratch[:4])
}

func (w *raw_writer) VbeU(v uint32) {
	if v < 0x80 {
		w.uint8(uint8(v))
		return
	}

	// Max VBE for uint32 is 5 bytes
	var buf [5]byte
	i := 0
	for {
		b := uint8(v & 0x7f)
		v >>= 7
		if v == 0 {
			buf[i] = b
			i++
			break
		}
		buf[i] = b | 0x80
		i++
	}
	w.w.Write(buf[:i])
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
		w.uint8(b)
		return
	}

	// Max VBE for int32 is 5 bytes
	var buf [5]byte
	i := 0
	for {
		if v_u < 0x40 { // fits in 6 bits
			b := uint8(v_u)
			if sign {
				b |= 0x40
			}
			buf[i] = b
			i++
			break
		}
		buf[i] = uint8(v_u&0x7f) | 0x80
		i++
		v_u >>= 7
	}
	w.w.Write(buf[:i])
}

func (w *raw_writer) VbeString(s string) {
	bs := []byte(s)
	w.VbeU(uint32(len(bs)))
	w.w.Write(bs)
}

func (w *raw_writer) fixedString(s string, size int) {
	bs := []byte(s)
	if len(bs) > size {
		bs = bs[:size]
	}
	w.w.Write(bs)
	if len(bs) < size {
		padding := make([]byte, size-len(bs))
		w.w.Write(padding)
	}
}

type MapsforgeWriter struct {
	w io.WriteSeeker
}

func NewMapsforgeWriter(w io.WriteSeeker) *MapsforgeWriter {
	return &MapsforgeWriter{w}
}

func (mw *MapsforgeWriter) WriteHeader(h *Header) error {
	rw := newRawWriter(mw.w)

	// Magic
	rw.fixedString(mapsforge_file_magic, 20)

	// Placeholder for header size (4 bytes)
	headerSizePos, _ := mw.w.Seek(0, io.SeekCurrent)
	rw.uint32(0)

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
	// Placeholder for zoom interval config
	mw.w.Seek(0, io.SeekCurrent)
	for i := 0; i < len(h.zoom_interval); i++ {
		rw.uint8(0)  // base
		rw.uint8(0)  // min
		rw.uint8(0)  // max
		rw.uint64(0) // pos
		rw.uint64(0) // size
	}

	// Update header size
	endHeaderPos, _ := mw.w.Seek(0, io.SeekCurrent)
	headerSize := uint32(endHeaderPos - headerSizePos - 4)
	mw.w.Seek(headerSizePos, io.SeekStart)
	rw.uint32(headerSize)
	mw.w.Seek(endHeaderPos, io.SeekStart)

	h.header_size = headerSize // cache it for later if needed
	return nil
}

func (mw *MapsforgeWriter) WriteTileData(td *TileData) ([]byte, error) {
	var buf bytes.Buffer
	rw := newRawWriter(&buf)

	// In the specification, first_way_offset is relative to the byte AFTER itself.
	// But we need to write the zoom table first.

	zooms := len(td.tile_header.zoom_table)
	for zi := 0; zi < zooms; zi++ {
		rw.VbeU(td.tile_header.zoom_table[zi].num_pois)
		rw.VbeU(td.tile_header.zoom_table[zi].num_ways)
	}

	// Offset to first way
	// We'll calculate this after writing POIs
	// Placeholder for VBE-U (max 5 bytes, let's just use a large enough temporary buffer or calculate it)
	// Actually, let's write POIs to a separate buffer first.

	var poiBuf bytes.Buffer
	poiWriter := newRawWriter(&poiBuf)
	for zi := 0; zi < zooms; zi++ {
		for _, poi := range td.poi_data[zi] {
			mw.writePOIData(poiWriter, &poi)
		}
	}

	var wayBuf bytes.Buffer
	wayWriter := newRawWriter(&wayBuf)
	for zi := 0; zi < zooms; zi++ {
		for _, way := range td.way_data[zi] {
			mw.writeWayProperties(wayWriter, &way)
		}
	}

	// Now we can write firstWayOffset
	rw.VbeU(uint32(poiBuf.Len()))
	buf.Write(poiBuf.Bytes())
	buf.Write(wayBuf.Bytes())

	return buf.Bytes(), nil
}

func (mw *MapsforgeWriter) writePOIData(w *raw_writer, pd *POIData) {
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

func (mw *MapsforgeWriter) writeWayProperties(w *raw_writer, wp *WayProperties) {
	// We need to calculate way_data_size which excludes signature and way_data_size itself.
	// Let's write way data to a buffer first.
	var buf bytes.Buffer
	ww := newRawWriter(&buf)

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

	w.VbeU(uint32(buf.Len()))
	w.w.Write(buf.Bytes())
}

func (mw *MapsforgeWriter) FinalizeHeader(h *Header) error {
	rw := newRawWriter(mw.w)

	// Go back to zoom interval config
	// magic (20) + header size (4) + rest of header fields
	// We need to exactly locate it.
	// Actually it's easier to just store the position in WriteHeader.
	// Let's re-calculate it or use a simpler approach.

	// For now, let's assume we can just overwrite the whole header if we have it all.
	mw.w.Seek(0, io.SeekStart)
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

	return nil
}
