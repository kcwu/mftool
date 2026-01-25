package mapsforge

import (
	"fmt"
	"io"
	"os"
	"time"
)

func CropMap(inputPath, outputPath string, bboxStr string) error {
	var minLon, minLat, maxLon, maxLat float64
	_, err := fmt.Sscanf(bboxStr, "%f,%f,%f,%f", &minLon, &minLat, &maxLon, &maxLat)
	if err != nil {
		return fmt.Errorf("invalid bbox format (minLon,minLat,maxLon,maxLat): %v", err)
	}

	p, err := ParseFile(inputPath, false)
	if err != nil {
		return err
	}
	defer p.Close()

	// Microdegrees
	cropMinLat := int32(minLat * 1000000)
	cropMinLon := int32(minLon * 1000000)
	cropMaxLat := int32(maxLat * 1000000)
	cropMaxLon := int32(maxLon * 1000000)

	// Intersection
	outHeader := p.data.header
	if cropMinLat > outHeader.min.lat {
		outHeader.min.lat = cropMinLat
	}
	if cropMinLon > outHeader.min.lon {
		outHeader.min.lon = cropMinLon
	}
	if cropMaxLat < outHeader.max.lat {
		outHeader.max.lat = cropMaxLat
	}
	if cropMaxLon < outHeader.max.lon {
		outHeader.max.lon = cropMaxLon
	}

	if outHeader.min.lat > outHeader.max.lat || outHeader.min.lon > outHeader.max.lon {
		return fmt.Errorf("crop region does not intersect with map coverage")
	}

	outHeader.creation_date = uint64(time.Now().UnixMilli())

	// Deep copy zoom intervals to avoid modifying source parser state
	srcIntervals := outHeader.zoom_interval
	outHeader.zoom_interval = make([]ZoomIntervalConfig, len(srcIntervals))
	copy(outHeader.zoom_interval, srcIntervals)

	f, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer f.Close()

	mw := NewMapsforgeWriter(f)
	err = mw.WriteHeader(&outHeader)
	if err != nil {
		return err
	}

	for si := 0; si < len(outHeader.zoom_interval); si++ {
		zic := &outHeader.zoom_interval[si]
		baseZoom := zic.base_zoom_level

		// Calculate new tile range for this subfile based on intersected bbox
		x, Y := outHeader.min.ToXY(baseZoom)
		X, y := outHeader.max.ToXY(baseZoom)
		len_x := X - x + 1
		len_y := Y - y + 1

		// SubFile start position
		pos, _ := f.Seek(0, io.SeekCurrent)
		zic.pos = uint64(pos)

		rw := newRawWriter()
		if outHeader.has_debug {
			rw.fixedString("+++IndexStart+++", 16)
			// Writes to file immediately for this small part
			f.Write(rw.Bytes())
		}

		// Placeholder for tile index
		indexStartPos, _ := f.Seek(0, io.SeekCurrent)
		indexEntries := make([]TileIndexEntry, len_x*len_y)
		rwIndex := newRawWriter()
		for i := 0; i < len_x*len_y; i++ {
			rwIndex.uint8(0)
			rwIndex.uint32(0)
		}
		f.Write(rwIndex.Bytes())

		// Find source subfile index for this zoom
		srcSi := findSubFileByZoom(p, baseZoom)
		if srcSi == -1 {
			// Should not happen if we copied headers
			return fmt.Errorf("source subfile not found for zoom %d", baseZoom)
		}

		// Write Tile Data and update index
		for ty := y; ty <= Y; ty++ {
			for tx := x; tx <= X; tx++ {
				idx := (tx - x) + len_x*(ty-y)

				// Get data from source
				srcIdx := p.GetTileIndex(srcSi, tx, ty)

				// Default to empty/water if source tile invalid (e.g. outside source range, though intersection check should prevent this)
				isWater := true
				hasData := false

				if srcIdx != nil {
					isWater = srcIdx.IsWater
					// Check if has data
					sf := &p.data.subfiles[srcSi]
					srcLinearIdx := sf.TileIndex(tx, ty)
					if srcIdx.Offset != sf.tile_indexes[srcLinearIdx+1].Offset {
						hasData = true
					}
				}

				tilePos, _ := f.Seek(0, io.SeekCurrent)
				relativeOffset := uint64(tilePos) - zic.pos
				indexEntries[idx].Offset = relativeOffset
				indexEntries[idx].IsWater = isWater

				if hasData {
					bytes, err := p.GetRawTileBytes(srcSi, tx, ty)
					if err != nil {
						return err
					}
					f.Write(bytes)
				}
			}
		}

		endPos, _ := f.Seek(0, io.SeekCurrent)
		zic.size = uint64(endPos) - zic.pos

		// Rewrite tile index
		f.Seek(indexStartPos, io.SeekStart)
		rwIndexRewrite := newRawWriter()
		for i := 0; i < len(indexEntries); i++ {
			val := indexEntries[i].Offset
			if indexEntries[i].IsWater {
				val |= 0x8000000000
			}
			rwIndexRewrite.uint8(uint8(val >> 32))
			rwIndexRewrite.uint32(uint32(val))
		}
		f.Write(rwIndexRewrite.Bytes())
		f.Seek(endPos, io.SeekStart)
	}

	finalSize, _ := f.Seek(0, io.SeekEnd)
	outHeader.file_size = uint64(finalSize)
	err = mw.FinalizeHeader(&outHeader)

	return err
}

func EstimateCropSize(inputPath, bboxStr string) (uint64, error) {
	var minLon, minLat, maxLon, maxLat float64
	_, err := fmt.Sscanf(bboxStr, "%f,%f,%f,%f", &minLon, &minLat, &maxLon, &maxLat)
	if err != nil {
		return 0, fmt.Errorf("invalid bbox format (minLon,minLat,maxLon,maxLat): %v", err)
	}

	p, err := ParseFile(inputPath, false)
	if err != nil {
		return 0, err
	}
	defer p.Close()

	// Microdegrees
	cropMinLat := int32(minLat * 1000000)
	cropMinLon := int32(minLon * 1000000)
	cropMaxLat := int32(maxLat * 1000000)
	cropMaxLon := int32(maxLon * 1000000)

	// Intersection
	outHeader := p.data.header
	if cropMinLat > outHeader.min.lat {
		outHeader.min.lat = cropMinLat
	}
	if cropMinLon > outHeader.min.lon {
		outHeader.min.lon = cropMinLon
	}
	if cropMaxLat < outHeader.max.lat {
		outHeader.max.lat = cropMaxLat
	}
	if cropMaxLon < outHeader.max.lon {
		outHeader.max.lon = cropMaxLon
	}

	if outHeader.min.lat > outHeader.max.lat || outHeader.min.lon > outHeader.max.lon {
		return 0, fmt.Errorf("crop region does not intersect with map coverage")
	}

	// Start with header size
	totalSize := uint64(outHeader.header_size)

	for si := 0; si < len(outHeader.zoom_interval); si++ {
		zic := &outHeader.zoom_interval[si]
		baseZoom := zic.base_zoom_level

		// Calculate new tile range for this subfile based on intersected bbox
		x, Y := outHeader.min.ToXY(baseZoom)
		X, y := outHeader.max.ToXY(baseZoom)
		len_x := int(X - x + 1)
		len_y := int(Y - y + 1)
		numTiles := len_x * len_y

		// Subfile overhead
		subFileSize := uint64(numTiles * 5)
		if outHeader.has_debug {
			subFileSize += 16 // "+++IndexStart+++"
		}

		// Find source subfile index for this zoom
		srcSi := findSubFileByZoom(p, baseZoom)
		if srcSi == -1 {
			return 0, fmt.Errorf("source subfile not found for zoom %d", baseZoom)
		}

		sf := &p.data.subfiles[srcSi]

		// Sum data sizes
		for ty := y; ty <= Y; ty++ {
			for tx := x; tx <= X; tx++ {
				// Source logic from CropMap
				// But we need to use source logic for source index lookup
				// Correct: We need to use sf.TileIndex(tx, ty) on the SOURCE subfile
				// Check bounds on source
				if tx < sf.x || tx > sf.X || ty < sf.y || ty > sf.Y {
					continue
				}

				srcLinearIdx := sf.TileIndex(tx, ty)
				// Check if has data
				offset := sf.tile_indexes[srcLinearIdx].Offset
				nextOffset := sf.tile_indexes[srcLinearIdx+1].Offset

				if offset != nextOffset {
					subFileSize += (nextOffset - offset)
				}
			}
		}

		totalSize += subFileSize
	}

	return totalSize, nil
}
