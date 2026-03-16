package mapsforge

import (
	"bufio"
	"os"
)

func LoadMapFromTOML(tomlPath, outputPath string) error {
	h, subs, err := streamParseDump(tomlPath)
	if err != nil {
		return err
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer f.Close()

	// mw wraps f directly so FinalizeHeader can Seek to position 0.
	mw := NewMapsforgeWriter(f)
	if err := mw.WriteHeader(h); err != nil {
		return err
	}

	// bw provides large-buffer sequential writes for tile data.
	bw := bufio.NewWriterSize(f, 4<<20)
	bw.Reset(f)

	for si := 0; si < len(h.zoom_interval); si++ {
		zic := &h.zoom_interval[si]
		baseZoom := zic.base_zoom_level

		x, Y := h.min.ToXY(baseZoom)
		X, y := h.max.ToXY(baseZoom)
		len_x := int(X - x + 1)
		len_y := int(Y - y + 1)

		// Flush so f's file offset is accurate.
		if err := bw.Flush(); err != nil {
			return err
		}
		sfStartPos, _ := f.Seek(0, 1)
		zic.pos = uint64(sfStartPos)

		if h.has_debug {
			rw := newRawWriter()
			rw.fixedString("+++IndexStart+++", 16)
			f.Write(rw.Bytes())
			sfStartPos += int64(len(rw.Bytes()))
		}

		// Write placeholder index; overwrite at end.
		indexStartPos := sfStartPos
		indexEntries := make([]TileIndexEntry, len_x*len_y)
		indexPlaceholder := make([]byte, len_x*len_y*5)
		f.Write(indexPlaceholder)

		// Track write position manually to avoid per-tile Seeks.
		writePos := uint64(sfStartPos) + uint64(len(indexPlaceholder))
		bw.Reset(f)

		sub := subs[si]
		for ty := y; ty <= Y; ty++ {
			for tx := x; tx <= X; tx++ {
				idx := (tx - x) + len_x*(ty-y)
				indexEntries[idx].Offset = writePos - zic.pos

				if data, ok := sub.tiles[idx]; ok {
					indexEntries[idx].IsWater = sub.isWater[idx]
					if _, err := bw.Write(data); err != nil {
						return err
					}
					writePos += uint64(len(data))
				} else if sub.isWater[idx] {
					indexEntries[idx].IsWater = true
				}
			}
		}

		zic.size = writePos - zic.pos

		// Flush tile data, seek back to write the real index, seek to end.
		if err := bw.Flush(); err != nil {
			return err
		}
		f.Seek(indexStartPos, 0)
		rwIndex := newRawWriter()
		for i := 0; i < len(indexEntries); i++ {
			val := indexEntries[i].Offset
			if indexEntries[i].IsWater {
				val |= 0x8000000000
			}
			rwIndex.uint8(uint8(val >> 32))
			rwIndex.uint32(uint32(val))
		}
		f.Write(rwIndex.Bytes())
		f.Seek(int64(writePos), 0)
		bw.Reset(f)
	}

	if err := bw.Flush(); err != nil {
		return err
	}
	finalSize, _ := f.Seek(0, 2)
	h.file_size = uint64(finalSize)
	return mw.FinalizeHeader(h)
}
