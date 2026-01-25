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
- `-v`, `--verbose`: Enable verbose output.

## Directory Structure
- `cmd/mftool/`: Main entry point.
- `internal/cli/`: CLI command definitions (Cobra).
- `internal/mapsforge/`: Core library for parsing, writing, and merging Mapsforge files.

## License

This project is licensed under the Apache License 2.0 - see the [LICENSE](LICENSE) file for details.
