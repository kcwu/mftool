# Delta Feature Design Doc

## Overview

The `delta` and `apply` commands allow incremental updates of Mapsforge `.map` files. Instead of distributing a full new map (300–400 MB), a compact binary delta file (`.mfd`) encodes only what changed. The `apply` command reconstructs the new map by replaying the delta against an older base.

```
delta -o d.mfd  old.map  new.map
apply -o out.map  old.map  d.mfd
apply -o out.map  base.map  d1.mfd d2.mfd ... dN.mfd   # chain
```

---

## MFD File Format

Magic: `"mfd\x04"` (4 bytes).

```
[magic 4B]
[header_len VbeU][header_bytes]        -- serialized Mapsforge header of the NEW map
[poi_map_count VbeU]
  [mapping VbeU] × poi_map_count       -- old poi tag index → new poi tag index (tagNotFound=0xFFFFFFFF)
[way_map_count VbeU]
  [mapping VbeU] × way_map_count       -- old way tag index → new way tag index
[records_zstd_len VbeU][records_zstd_bytes]  -- zstd-compressed records section
  per changed tile:
    [si uint8]                         -- subfile index (0–254; 0xFF = end-of-stream sentinel)
    [x VbeU][y VbeU]                   -- tile grid coordinates
    [flags uint8]                      -- 0x01=is_water, 0x02=tile_empty
    if flags & 0x02 == 0:
      [zoom_mask VbeU]                 -- bitmask of changed zoom levels (bit i = zoom min+i)
      per set bit in zoom_mask (low→high):
        [patch_len VbeU][patch_bytes]  -- LZ77 patch (see below)
  [0xFF uint8]                         -- end-of-stream sentinel
```

The **records section** is zstd-compressed (`SpeedBestCompression`) as a single unit. This exploits cross-tile patterns in the LZ77 INSERT literals (adjacent tiles share coordinate ranges and structural similarities), compressing the literal stream efficiently.

---

## Zoom Blob Format

Each LZ77 patch encodes the diff between the *reference blob* (old zoom level) and the *target blob* (new zoom level). The zoom blob is a self-contained binary encoding:

```
[num_pois VbeU][num_ways VbeU]
[poi_bytes_len VbeU]
[poi_bytes]       -- serialized POIData records (absolute lat/lon, tags, name, ...)
[way_bytes]       -- serialized WayProperties records (absolute coordinates, tags, ...)
```

Serialization uses the same coordinate encoding as the original tile format (VbeS delta values), preserving them verbatim so blobs are self-contained and order-independent.

---

## Normalization

Before encoding a zoom blob (for either reference or target), the data is **normalized**:

1. Within each POI/Way element, sort `tag_id[]` ascending.
2. Sort all POIs by `(lat, lon, tag_ids, name, ...)` using `CmpByPOIData`.
3. Sort all Ways by `(sub_tile_bitmap, tag_ids, name, ...)` using `CmpByWayData`.

**Why this is critical**: Different versions of the same map may encode the same semantic content with tags in different orders (the map generator ranks tags by usage frequency, which shifts between versions). Without normalization, ~9,900 tiles appear changed instead of the actual ~2,600.

The same normalization is applied in both `delta` and `apply`, so that the reference blob reconstructed during `apply` is byte-identical to the one used during `delta`.

---

## Tag Mapping

Each map file has its own `poi_tags[]` and `way_tags[]` string tables. Tag IDs are indices into these tables, and they differ between map versions.

`buildTagMapByString(oldTags, newTags) []uint32` builds a per-file remapping array by matching tag strings. Tags missing from the new map are mapped to `tagNotFound = 0xFFFFFFFF`.

`remapPOITags` / `remapWayTags`: remap tag IDs for each element, returning a new slice. Elements with any unmappable tag are **silently dropped** from the reference. This allows cross-type deltas (e.g., contour-lines map → OSM map) where most tags don't exist in the other map.

`remapPOITagsInPlace` / `remapWayTagsInPlace`: same semantics, but mutate the slice in place (compacting dropped elements by shifting survivors forward). Used in `buildRefBlobLight` where the caller owns the data (decoded from a stored blob or from `GetTileDataUncached`).

---

## LZ77 Patch Encoding

Each zoom-level patch is a custom LZ77 binary stream. The **reference** is the normalized old zoom blob (with tags remapped to the new map's namespace); the **target** is the normalized new zoom blob.

### Op format

```
[header VbeU]
  if header is even:  COPY op   -- length = (header>>1)+1, then [offset VbeU]
  if header is odd:   INSERT op -- length = (header>>1)+1, then [literal bytes × length]
```

### Encoding

A hash-chain index (`lz77Index`) is built over the reference for O(n) match lookup. It uses a fixed-size `head [1<<18]int32` array (mapping 3-gram hashes to the most-recent matching position) and a `next []int32` chain array — avoiding the per-key `[]int` allocations of a `map[uint32][]int`. Chain traversal is capped at `maxChainDepth = 128` to bound worst-case scan time on repetitive input. At each target position:
- Look up the 3-byte key in the index.
- Find the longest match among all candidates.
- If match ≥ 3 bytes: emit COPY; otherwise accumulate and eventually emit INSERT.

There is no 32 KB window limit — the full reference (which can be hundreds of KB for large tiles) is available for back-references. This is the key advantage over `compress/flate` with a preset dictionary, which truncates to the last 32 KB.

### Why not flate with preset dictionary?

`flate.NewWriterDict` uses a 32 KB sliding window. For large tiles (reference blobs can reach 500+ KB), matches in the earlier portion of the reference are missed entirely, producing output larger than the uncompressed input. Measurements:

| Reference size | flate+dict result | LZ77 result |
|---|---|---|
| 20 KB (99% match) | 896 B | ~200 B |
| 50 KB (99% match) | 50 KB (worse than raw!) | ~500 B |

### Compression ratio

For a typical consecutive-version delta (`old.map` → `new.map`, ~2600 changed tiles):
- 99.5% of decoded bytes come from COPY ops (reference matches).
- 0.5% are INSERT literals (actual changed content, ~341 KB).
- LZ77 encodes this as ~836 KB of patches.
- After whole-section zstd, the records section compresses to ~550 KB.
- Total MFD size: ~706 KB for a 368 MB map.

---

## Delta Generation (`CmdDelta`)

```
ParseFile(old) + ParseFile(new)
buildTagMapByString(old.poi_tags, new.poi_tags) → poiMap
buildTagMapByString(old.way_tags, new.way_tags) → wayMap
serializeMapforgeHeader(new.header) → headerBytes
collectDeltaRecords(old, new, poiMap, wayMap) → []*mfdTileRecord
writeMFD(...)
```

### `collectDeltaRecords` — parallel worker pool

Tiles are dispatched via a buffered channel to `runtime.NumCPU()` workers, each calling `computeDeltaRecord`. A lookahead queue (`[]chan deltaResult`, depth = 4×workers) preserves approximate dispatch order while allowing parallel execution. Results are collected in dispatch order to enable sequential writing.

### `computeDeltaRecord`

For tile `(si, x, y)`:

1. **Byte-equality fast path**: if tag mappings are identity and `bytes.Equal(b1, b2)` → skip (no change).
2. **Streaming fast path** (`streamTilesEqual`): compare tiles field-by-field applying tag remapping inline, with zero allocations. Catches semantically-unchanged tiles even when byte representations differ due to tag ID renumbering. Conservative: returns false if element order differs, falling through to full parse.
3. Parse both tiles with `GetTileDataUncached` (single-use tiles; no caching avoids GC pressure).
4. For each zoom level `zi`:
   - Build **reference**: `remapPOITags(p1.poi_data[zi], poiMap)` + `remapWayTags(...)` → `normalizeZoomLevel(...)` → `encodeZoomBlob(...)`.
   - Build **target**: `normalizeZoomLevel(td2.poi_data[zi], td2.way_data[zi])` → `encodeZoomBlob(...)`. Since `td2` is uncached (caller-owned), tag_ids are sorted in-place — no deep-copy needed.
   - If `bytes.Equal(ref, target)`: skip this zoom level.
   - Otherwise: `lz77Encode(target, ref)` → patch. Set bit `zi` in `zoomMask`.
5. If `zoomMask == 0` and water flag unchanged: return nil (no record needed).

---

## Delta Application (`CmdApply`)

```
ParseFile(base)
loadMFD(delta1), loadMFD(delta2), ...
outHeader = last_delta.header
applyDeltas(base, deltas, outHeader, outputPath)
```

### `loadMFD`

Reads the MFD file, parses header + tag mappings, zstd-decompresses the records section, then parses tile records into `map[mfdTileKey]*mfdTileRecord`.

### `applyDeltas` — two-phase approach

Before phase 1, `buildCrossVersionMaps` pre-computes all `(from, to)` tag-map pairs for versions `-1` (base) through `len(deltas)-1`. With N deltas there are `(N+1)²` pairs, each a `[]uint32` built once by `buildTagMapByString`. Workers look up entries in this table instead of recomputing per tile.

**Phase 1**: Iterate deltas in order, calling `applyDeltaRecords` for each. This builds a `tileStates map[mfdTileKey]*perTileState` where each entry holds per-zoom blob state. Each delta's records are processed in parallel (see below); deltas are applied sequentially so that delta `i+1` sees the full state produced by delta `i`.

**Phase 2**: `writeOutputMap` writes the final output by iterating all tiles in the output header's coordinate grid, calling `buildOutputTile` for each (parallelised with a worker pool, results collected in order for sequential I/O).

### `applyDeltaRecords`

Records within a single delta are independent, so processing is **parallelised** with `runtime.NumCPU()` workers. The full set of `(key, rec, st)` inputs is snapshotted from `tileStates` before any jobs are dispatched (avoiding concurrent map reads and writes). Jobs are sent to a buffered channel; results are collected via a separate fully-buffered results channel and written back to `tileStates` only after all workers have finished. Unlike `collectDeltaRecords`, no ordering guarantee is needed — `tileStates` is a map, not a sequential stream.

Each worker processes one tile record (`processOneRecord`):
- If `tile_empty` flag: mark tile deleted in `tileStates`.
- Parse the base tile once per tile (lazily, via `GetTileDataUncached` — full parse, no shared cache, safe for concurrent calls).
- For each changed zoom level (`zoomMask` bit set):
  1. `buildRefBlobLight`: reconstruct the reference blob for this (tile, zoom):
     - **Early return**: if a stored blob exists, it is already in `inputVer`'s namespace (`currentVer == inputVer`), and both `d.poiMap` and `d.wayMap` are identity mappings, the stored blob is byte-identical to the reference — return it directly (no decode/normalize/re-encode).
     - Otherwise: decode the stored blob (or load from base via `GetTileDataUncached`), cross-remap using the pre-computed `crossVersionMaps` table, apply `d.poiMap`/`d.wayMap` in-place (`remapPOITagsInPlace` / `remapWayTagsInPlace`), normalize, encode → `refBlob`.
  2. `lz77Decode(patch, refBlob)` → new blob.
  3. Store in `perTileState.zoomBlobs[zi]` with `zoomVers[zi] = di` (this delta's index).

`GetTileDataUncached` (not `GetTileDataUncachedLight`) is required here because `normalizeZoomLevel` sorts by `wp.block` (decoded coordinate data); the light variant leaves `block = nil`, producing a different sort order and thus a different `refBlob` — breaking LZ77 decode.

### `buildOutputTile`

**No delta state (`st == nil`)**:
- Identity mapping: return raw base bytes directly (zero work).
- Non-identity: `streamRewriteTile` — remaps tag IDs inline with zero struct allocations; elements whose tags map to `tagNotFound` are silently dropped (matching `remapPOITags` / `remapWayTags` behaviour).

**With delta state (`st != nil`)**:

Fast path (`!blobsNeedRemap` — all blobs already in output namespace):
- All zoom levels have blobs (`allBlobsPresent`): assemble tile directly from blob byte sections (no decode).
- Some zoom levels lack blobs (base needed):
  - Identity base mapping: `extractBaseZoomBytes` on raw base bytes.
  - Non-identity: `streamRewriteTile` on raw base → `extractBaseZoomBytes` on the result.
- In both cases: no struct decode, no `remapPOITags`/`remapWayTags`.

Slow path (`blobsNeedRemap` — some blobs need cross-namespace remap): full struct decode (`decodeZoomBlob`), remap, `WriteTileData`. Occurs only in multi-delta chains where a zoom level's blob is from an older delta namespace.

### Chain correctness

Each zoom level tracks its own namespace version (`zoomVers[zi]`) independently. When a tile is updated by d12 for zoom levels 0–2 and later by d23 for only zoom level 0, zoom levels 1–2 retain `zoomVers[zi] = 0` (m2 namespace) while zoom level 0 gets `zoomVers[0] = 1` (m3 namespace). `buildOutputTile` remaps each zoom level individually, preventing the wrong namespace from being applied.

---

## Key Invariant

The reference blob reconstructed during `apply` must be **byte-identical** to the reference blob used during `delta`. This requires:
- Same normalization (sort tag_ids within element, sort elements).
- Same tag remapping (apply `d.poiMap`/`d.wayMap` before encoding).
- Same serialization (`encodeZoomBlob` using `MapsforgeWriter.writePOIData` / `writeWayProperties`).

Any divergence causes `lz77Decode` to produce corrupted output without raising an error (since LZ77 is not self-validating at the byte level).

---

## Performance Notes

- **Delta generation**: ~2600 changed tiles out of ~70K total. The fast-path `bytes.Equal` skips ~97% of tiles before any parsing.
- **Apply (single delta)**: ~0.74s wall on an 8-core machine for a 368 MB map with ~2600 changed tiles.
- **Apply (8-delta chain)**: ~1.6s wall (vs ~1.8s before the phase-1 optimisations below).
  - Phase 1 (`applyDeltaRecords`): parallelised across all CPUs; cost scales with the number of modified tiles per delta. Three optimisations reduce per-tile overhead in long chains:
    1. **Pre-computed cross-version tag maps** (`buildCrossVersionMaps`): all `(from, to)` version pairs are computed once at startup; `buildRefBlobLight` does a table lookup instead of calling `buildTagMapByString` per tile.
    2. **Early return for in-namespace blobs**: when a stored blob is already in `inputVer`'s namespace and both tag remappings are identity, the blob is returned directly — skipping `decodeZoomBlob`, `normalizeZoomLevel`, and `encodeZoomBlob`.
    3. **In-place tag remapping**: `remapPOITagsInPlace` / `remapWayTagsInPlace` mutate owned data in place, eliminating the `make([]uint32, ...)` per way/POI in both the cross-version and delta remaps.
  - Phase 2 (`writeOutputMap`): parallelised; for the ~97% unchanged tiles, `streamRewriteTile` remaps tag IDs inline with zero per-tile allocations (~15 µs/tile at 8× parallelism).
  - GC pressure reduced by `GOGC=400` in both `CmdApply` and `CmdDelta`, and by deferring struct allocations to the rare slow path.
- **LZ77 performance**: Index building (the `idx.head`/`idx.next` population loop inside `lz77Index.encode`) is `O(n)` in reference size and dominates for large tiles. Potential future optimization: reuse the index across zoom levels of the same tile.
- **Zstd overhead**: `SpeedBestCompression` on the full records section adds ~0.5s to encode and is negligible to decode for a ~850 KB uncompressed records section.
