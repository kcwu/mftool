# Mapsforge Map File Tool (mftool)

`mftool` is a command-line tool designed to manipulate and analyze Mapsforge binary map files (`.map`). It allows merging multiple map files, comparing them for differences, and dumping detailed internal structures for debugging.

## Features

- **Merge**: Combine multiple `.map` files into a single output file. Merging correctly handles:
    - Bounding box unions.
    - Tag mapping (POI and Way tags).
    - Water flags (`IsWater`), ensuring correct status even when data is present.
    - Parallel processing for high performance.
- **Diff**: Compare two `.map` files to detect:
    - Metadata differences (header fields).
    - Tile-level differences (water flags, offsets).
    - Semantic mismatches.
- **Dump**: Inspect the internal structure of a `.map` file, including:
    - Header information.
    - Tile index details (offsets, water flags).
    - Decoded POI and Way data.

## Build

Building the tool requires Go 1.24+ (or compatible).

```bash
go build -o mftool cmd/mftool/main.go
```

## Usage

### Merge Maps
Merge multiple input maps into a single output file.

```bash
./mftool merge output.map input1.map input2.map [input3.map ...]
```

Options:
- `--tile <si>,<x>,<y>`: Only merge usage for a specific tile index (useful for debugging).

### Diff Maps
Compare two map files.

```bash
./mftool diff file1.map file2.map
```

The tool reports:
- Header mismatches.
- Specific tile indices that differ in water status or data offset.
- Detailed tag or coordinate differences if semantic checking is enabled.

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
