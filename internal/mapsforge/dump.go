package mapsforge

import (
	"fmt"
	"os"
)

func CmdDump(args []string, flagHeader bool, flagAll bool, flagTile string) error {
	fn := args[0]

	p, err := ParseFile(fn, false)
	if err != nil {
		return err
	}
	defer p.Close()

	dumper := NewTOMLDumper(os.Stdout)
	if flagHeader || flagAll {
		dumper.DumpHeader(&p.data.header)
		dumper.DumpZoomIntervals(p.data.header.zoom_interval)
	}

	if flagAll {
		for si, sf := range p.data.subfiles {
			for x := sf.x; x <= sf.X; x++ {
				for y := sf.y; y <= sf.Y; y++ {
					td, err := p.GetTileData(si, x, y)
					if err != nil {
						return err
					}

					ti := p.GetTileIndex(si, x, y)
					isWater := false
					if ti != nil {
						isWater = ti.IsWater
					}

					// Dump if data exists OR if it's a water tile (no data but flag set)
					if td != nil || isWater {
						dumper.DumpTile(si, x, y, td, &p.data.header, isWater)
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

		ti := p.GetTileIndex(si, x, y)
		isWater := false
		if ti != nil {
			isWater = ti.IsWater
		}

		// Single tile dump
		dumper.DumpTile(si, x, y, td, &p.data.header, isWater)
	}
	return nil
}
