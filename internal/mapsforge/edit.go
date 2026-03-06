package mapsforge

import (
	"io"
	"os"
)

func CmdEdit(inputPath string, outputPath string, comment *string, timestamp *int64) error {
	p, err := ParseFile(inputPath, false)
	if err != nil {
		return err
	}
	defer p.Close()

	h := p.data.header

	if comment != nil {
		h.comment = *comment
		h.has_comment = true
	}
	if timestamp != nil {
		h.creation_date = uint64(*timestamp)
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer f.Close()

	mw := NewMapsforgeWriter(f)
	err = mw.WriteHeader(&h)
	if err != nil {
		return err
	}

	// Copy subfiles
	for i := 0; i < len(h.zoom_interval); i++ {
		zic := &h.zoom_interval[i]
		oldPos := p.data.header.zoom_interval[i].pos
		oldSize := p.data.header.zoom_interval[i].size

		newPos, _ := f.Seek(0, io.SeekCurrent)
		zic.pos = uint64(newPos)
		zic.size = oldSize

		_, err = f.Write(p.file_content[oldPos : oldPos+oldSize])
		if err != nil {
			return err
		}
	}

	finalSize, _ := f.Seek(0, io.SeekEnd)
	h.file_size = uint64(finalSize)

	return mw.FinalizeHeader(&h)
}
