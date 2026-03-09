# Mapsforge Map File Tool (mftool)

`mftool` is a command-line tool designed to manipulate and analyze Mapsforge binary map files (`.map`). It allows merging multiple map files, comparing them for differences, and dumping detailed internal structures for debugging.

## Features

- **Merge**: Combine multiple `.map` files into a single output file. Merging correctly handles:
    - Bounding box unions.
    - Tag mapping (POI and Way tags).
    - Water flags (`IsWater`), ensuring correct status even when data is present.
- **Diff**: Compare two `.map` files to detect:
    - Metadata differences (header fields).
    - Tile-level differences (water flags, offsets).
    - Semantic mismatches.
- **Delta**: Generate a compact binary diff (`.mfd`) between two map versions, encoding only changed tiles.
- **Apply**: Reconstruct a new map by applying one or more delta files to a base map, supporting incremental map updates.
- **Crop**: Extract a sub-region from a map file using:
    - Bounding box coordinates.
    - Center point and distance.
- **Dump**: Inspect the internal structure of a `.map` file, including:
    - Header information.
    - Tile index details (offsets, water flags).
    - Decoded POI and Way data.
- **Performance**:
    - **Memory Mapped I/O**: Instant startup time for large map files.
    - **Parallel Processing**: Multi-threaded parsing and merging.

## Installation

To install `mftool` directly from the repository:

```bash
go install github.com/kcwu/mftool/cmd/mftool@latest
```

## Build

Building the tool requires Go 1.24+ (or compatible).

```bash
go build -o mftool cmd/mftool/main.go
```

## Usage

### Merge Maps
Merge multiple input maps into a single output file.

```bash
./mftool merge -o output.map input1.map input2.map [input3.map ...]
```

Options:
- `-o, --output <file>`: Output map file (required).
- `-f, --force`: Overwrite output file if it exists.
- `--tile <si>,<x>,<y>`: Merge only usage for a specific tile index (useful for debugging).

### Diff Maps
Compare two map files.

```bash
./mftool diff file1.map file2.map
```

The tool reports:
- Header mismatches.
- Specific tile indices that differ in water status or data offset.
- Detailed tag or coordinate differences if semantic checking is enabled.

Options:
- `-v`, `--verbose`: Show detailed diff output (tag and coordinate differences).
- `--ignore-comment`: Ignore comment field mismatches.
- `--ignore-timestamp`: Ignore creation date mismatches.
- `-s`, `--strict`: Report tag ordering mismatches between files.

### Generate Delta
Generate a compact binary delta (`.mfd`) between two map versions.

```bash
./mftool delta -o output.mfd old.map new.map
```

Options:
- `-o, --output <file>`: Output delta file (required).
- `-f, --force`: Overwrite output file if it exists.

### Apply Delta
Reconstruct a new map by applying one or more delta files to a base map.

```bash
./mftool apply -o output.map base.map delta1.mfd [delta2.mfd ...]
```

Multiple delta files are applied in order, enabling incremental updates across several map versions.

Options:
- `-o, --output <file>`: Output map file (required).
- `-f, --force`: Overwrite output file if it exists.

### Crop Map
Extract a sub-region from a map file.

```bash
./mftool crop -o output.map input.map --bbox minLon,minLat,maxLon,maxLat
```
Or by center and distance:
```bash
./mftool crop -o output.map input.map --center lat,lon --distance <km>
```

Options:
- `-o, --output <file>`: Output map file (required).
- `-f, --force`: Overwrite output file if it exists.

### Validate Map
Check the integrity of a map file.

```bash
./mftool validate file.map
```

### Show Tag Stats
Show statistics of tag usage.

```bash
./mftool tags input1.map [input2.map ...]
```

### Dump Map Info
Dump information about a map file.

```bash
./mftool dump file.map [flags]
```

Options:
- `-a`, `--all`: Parse full file content (not just header).
- `-s`, `--short`: Show short summary.
- `--tile <si>,<x>,<y>`: Dump detailed data for a specific tile.

### Global Flags
- `--cpuprofile <file>`: Write CPU profile to the specified file (useful for performance analysis).

## Directory Structure
- `cmd/mftool/`: Main entry point.
- `internal/cli/`: CLI command definitions (Cobra).
- `internal/mapsforge/`: Core library for parsing, writing, and merging Mapsforge files.

## License

This project is licensed under the Apache License 2.0 - see the [LICENSE](LICENSE) file for details.
