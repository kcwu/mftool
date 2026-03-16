package mapsforge

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
)

func compare_poi_datas(stats map_stats, z, x, y int, d1, d2 []POIData, detail bool, strict bool) bool {
	var found_diff bool
	for i, j := 0, 0; i < len(d1) || j < len(d2); {
		if j == len(d2) || (i < len(d1) && d1[i].less(&d2[j])) {
			if !detail {
				return true
			}

			found_diff = true
			fmt.Println(z, x, y, "-poi,", d1[i].ToString(&stats.poi_stats))
			i++
		} else if i == len(d1) || d2[j].less(&d1[i]) {
			if !detail {
				return true
			}
			found_diff = true
			fmt.Println(z, x, y, "+poi,", d2[j].ToString(&stats.poi_stats))
			j++
		} else {
			if strict && !slices_equal(d1[i].tag_id_raw, d2[j].tag_id_raw) {
				found_diff = true
				fmt.Println(z, x, y, "poi tag order mismatch")
				fmt.Println("  -", d1[i].ToString(&stats.poi_stats))
				fmt.Println("  +", d2[j].ToString(&stats.poi_stats))
			}
			i++
			j++
		}
	}
	if found_diff {
		fmt.Println()
	}
	return found_diff
}

func compare_way_datas(stats map_stats, z, x, y int, d1, d2 []WayProperties, detail bool, strict bool) bool {
	var found_diff bool
	for i, j := 0, 0; i < len(d1) || j < len(d2); {
		if j == len(d2) || (i < len(d1) && d1[i].less(&d2[j])) {
			if !detail {
				return true
			}

			found_diff = true
			fmt.Println(z, x, y, "-way", d1[i].ToString(&stats.way_stats))
			i++
		} else if i == len(d1) || d2[j].less(&d1[i]) {
			if !detail {
				return true
			}

			found_diff = true
			fmt.Println(z, x, y, "+way", d2[j].ToString(&stats.way_stats))
			j++
		} else {
			if strict && !slices_equal(d1[i].tag_id_raw, d2[j].tag_id_raw) {
				found_diff = true
				fmt.Println(z, x, y, "way tag order mismatch")
				fmt.Println("  -", d1[i].ToString(&stats.way_stats))
				fmt.Println("  +", d2[j].ToString(&stats.way_stats))
			}
			i++
			j++
		}
	}
	if found_diff {
		fmt.Println()
	}
	return found_diff
}

func compareHeaders(h1, h2 *Header, ignoreComment, ignoreTimestamp bool) bool {
	var found_diff bool
	if h1.min != h2.min {
		found_diff = true
		fmt.Printf("Header mismatch: min %v != %v\n", h1.min, h2.min)
	}
	if h1.max != h2.max {
		found_diff = true
		fmt.Printf("Header mismatch: max %v != %v\n", h1.max, h2.max)
	}
	if h1.tile_size != h2.tile_size {
		found_diff = true
		fmt.Printf("Header mismatch: tile_size %v != %v\n", h1.tile_size, h2.tile_size)
	}
	if h1.projection != h2.projection {
		found_diff = true
		fmt.Printf("Header mismatch: projection %v != %v\n", h1.projection, h2.projection)
	}
	if h1.start_zoom != h2.start_zoom {
		found_diff = true
		fmt.Printf("Header mismatch: start_zoom %v != %v\n", h1.start_zoom, h2.start_zoom)
	}
	if h1.language_preference != h2.language_preference {
		found_diff = true
		fmt.Printf("Header mismatch: language_preference %q != %q\n", h1.language_preference, h2.language_preference)
	}
	if !ignoreComment && h1.comment != h2.comment {
		found_diff = true
		fmt.Printf("Header mismatch: comment %q != %q\n", h1.comment, h2.comment)
	}
	if h1.created_by != h2.created_by {
		found_diff = true
		fmt.Printf("Header mismatch: created_by %q != %q\n", h1.created_by, h2.created_by)
	}
	if !ignoreTimestamp && h1.creation_date != h2.creation_date {
		found_diff = true
		fmt.Printf("Header mismatch: creation_date %v != %v\n", h1.creation_date, h2.creation_date)
	}
	return found_diff
}

func compareTile(stats map_stats, min_zoom_level, x, y int, t1, t2 *TileData, flagDetail bool, strict bool) bool {
	if t1 == nil && t2 == nil {
		return false
	}
	if t1 == nil || t2 == nil {
		return true
	}
	if len(t1.poi_data) != len(t2.poi_data) {
		fmt.Printf("Tile zi mismatch: %d %d %d - %d != %d\n", min_zoom_level, x, y, len(t1.poi_data), len(t2.poi_data))
		return true
	}

	t1.normalize()
	t2.normalize()
	var any_diff bool
	for zi := 0; zi < len(t1.poi_data); zi++ {
		z := min_zoom_level + zi
		var found_diff bool
		if compare_poi_datas(stats, z, x, y, t1.poi_data[zi], t2.poi_data[zi], flagDetail, strict) {
			found_diff = true
		}
		if compare_way_datas(stats, z, x, y, t1.way_data[zi], t2.way_data[zi], flagDetail, strict) {
			found_diff = true
		}
		if !flagDetail && found_diff {
			fmt.Println(z, x, y)
		}
		if found_diff {
			any_diff = true
		}
	}
	return any_diff
}

// remapTile remaps tag IDs (both tag_id and tag_id_raw) to the merged namespace in-place.
func remapTile(td *TileData, poi_m, way_m []uint32) {
	if isIdentityMapping(poi_m) && isIdentityMapping(way_m) {
		return
	}
	for zi := range td.poi_data {
		remapPOITagsInPlace(&td.poi_data[zi], poi_m)
		for i := range td.poi_data[zi] {
			for j, t := range td.poi_data[zi][i].tag_id_raw {
				if int(t) < len(poi_m) {
					td.poi_data[zi][i].tag_id_raw[j] = poi_m[t]
				}
			}
		}
		remapWayTagsInPlace(&td.way_data[zi], way_m)
		for i := range td.way_data[zi] {
			for j, t := range td.way_data[zi][i].tag_id_raw {
				if int(t) < len(way_m) {
					td.way_data[zi][i].tag_id_raw[j] = way_m[t]
				}
			}
		}
	}
}

// insertionSortU32 sorts a small slice in place. Optimal for n<=15 (zero heap allocation).
func insertionSortU32(a []uint32) {
	for i := 1; i < len(a); i++ {
		v := a[i]
		j := i - 1
		for j >= 0 && a[j] > v {
			a[j+1] = a[j]
			j--
		}
		a[j+1] = v
	}
}

// vbeStringsRawEqual reads a length-prefixed string from each reader and compares
// the raw bytes without allocating a Go string.
func vbeStringsRawEqual(r1, r2 *raw_reader) bool {
	n1 := r1.VbeU()
	n2 := r2.VbeU()
	if n1 != n2 || int(n1) > len(r1.buf) || int(n2) > len(r2.buf) {
		r1.buf = r1.buf[min(int(n1), len(r1.buf)):]
		r2.buf = r2.buf[min(int(n2), len(r2.buf)):]
		return false
	}
	eq := bytes.Equal(r1.buf[:n1], r2.buf[:n2])
	r1.buf = r1.buf[n1:]
	r2.buf = r2.buf[n2:]
	return eq
}

// streamPOIEqualUnordered is like streamPOIEqual but handles arbitrary tag ordering
// within each element. Tags are read into stack arrays, sorted, then compared.
// Zero heap allocation; safe because numTag <= 15 (4-bit field).
func streamPOIEqualUnordered(r1, r2 *raw_reader, poiMap []uint32) bool {
	if r1.VbeS() != r2.VbeS() { // lat
		return false
	}
	if r1.VbeS() != r2.VbeS() { // lon
		return false
	}
	sp1 := r1.uint8()
	sp2 := r2.uint8()
	if sp1>>4 != sp2>>4 { // layer
		return false
	}
	numTag := int(sp1 & 0xf)
	if int(sp2&0xf) != numTag {
		return false
	}
	var tags1, tags2 [16]uint32
	for ti := 0; ti < numTag; ti++ {
		t1 := r1.VbeU()
		if int(t1) >= len(poiMap) {
			return false
		}
		mapped := poiMap[t1]
		if mapped == tagNotFound {
			return false
		}
		tags1[ti] = mapped
		tags2[ti] = r2.VbeU()
	}
	insertionSortU32(tags1[:numTag])
	insertionSortU32(tags2[:numTag])
	for ti := 0; ti < numTag; ti++ {
		if tags1[ti] != tags2[ti] {
			return false
		}
	}
	fl1 := r1.uint8()
	fl2 := r2.uint8()
	if fl1 != fl2 {
		return false
	}
	if fl1>>7&1 != 0 { // has_name
		if !vbeStringsRawEqual(r1, r2) {
			return false
		}
	}
	if fl1>>6&1 != 0 { // has_house_number
		if !vbeStringsRawEqual(r1, r2) {
			return false
		}
	}
	if fl1>>5&1 != 0 { // has_elevation
		if r1.VbeS() != r2.VbeS() {
			return false
		}
	}
	return r1.err == nil && r2.err == nil
}

// streamWayEqualUnordered is like streamWayEqual but handles arbitrary tag ordering.
func streamWayEqualUnordered(r1, r2 *raw_reader, wayMap []uint32) bool {
	sz1 := r1.VbeU()
	sz2 := r2.VbeU()
	if r1.err != nil || r2.err != nil {
		return false
	}
	start1 := len(r1.buf)
	start2 := len(r2.buf)

	if r1.uint16() != r2.uint16() { // sub_tile_bitmap
		return false
	}
	sp1 := r1.uint8()
	sp2 := r2.uint8()
	if sp1>>4 != sp2>>4 { // layer
		return false
	}
	numTag := int(sp1 & 0xf)
	if int(sp2&0xf) != numTag {
		return false
	}
	var tags1, tags2 [16]uint32
	for ti := 0; ti < numTag; ti++ {
		t1 := r1.VbeU()
		if int(t1) >= len(wayMap) {
			return false
		}
		mapped := wayMap[t1]
		if mapped == tagNotFound {
			return false
		}
		tags1[ti] = mapped
		tags2[ti] = r2.VbeU()
	}
	insertionSortU32(tags1[:numTag])
	insertionSortU32(tags2[:numTag])
	for ti := 0; ti < numTag; ti++ {
		if tags1[ti] != tags2[ti] {
			return false
		}
	}
	fl1 := r1.uint8()
	fl2 := r2.uint8()
	if fl1 != fl2 {
		return false
	}
	if fl1>>7&1 != 0 { // has_name
		if !vbeStringsRawEqual(r1, r2) {
			return false
		}
	}
	if fl1>>6&1 != 0 { // has_house_number
		if !vbeStringsRawEqual(r1, r2) {
			return false
		}
	}
	if fl1>>5&1 != 0 { // has_reference
		if !vbeStringsRawEqual(r1, r2) {
			return false
		}
	}
	if fl1>>4&1 != 0 { // has_label_position
		if r1.VbeS() != r2.VbeS() || r1.VbeS() != r2.VbeS() {
			return false
		}
	}
	consumed1 := start1 - len(r1.buf)
	consumed2 := start2 - len(r2.buf)
	blockLen1 := int(sz1) - consumed1
	blockLen2 := int(sz2) - consumed2
	if blockLen1 < 0 || blockLen2 < 0 || blockLen1 != blockLen2 {
		return false
	}
	if len(r1.buf) < blockLen1 || len(r2.buf) < blockLen2 {
		return false
	}
	if !bytes.Equal(r1.buf[:blockLen1], r2.buf[:blockLen2]) {
		return false
	}
	r1.buf = r1.buf[blockLen1:]
	r2.buf = r2.buf[blockLen2:]
	return r1.err == nil && r2.err == nil
}

// streamTilesEqualUnordered is like streamTilesEqual but handles arbitrary tag
// ordering within elements. Used when streamTilesEqual fails conservatively due
// to tag reordering (e.g. after a dump→load round-trip that sorts tags).
// Zero heap allocation; uses stack-based insertion sort for n<=15 tags.
func streamTilesEqualUnordered(b1, b2 []byte, poiMap, wayMap []uint32, h1 *Header, zic *ZoomIntervalConfig) bool {
	if h1.has_debug {
		return false
	}
	r1 := newRawReader(b1)
	r2 := newRawReader(b2)
	zooms := int(zic.max_zoom_level-zic.min_zoom_level) + 1

	var numPOIs [256]uint32
	var numWays [256]uint32
	for zi := 0; zi < zooms; zi++ {
		numPOIs[zi] = r1.VbeU()
		n2 := r2.VbeU()
		if numPOIs[zi] != n2 {
			return false
		}
		numWays[zi] = r1.VbeU()
		n2 = r2.VbeU()
		if numWays[zi] != n2 {
			return false
		}
	}
	r1.VbeU() // first_way_offset — may differ; skip
	r2.VbeU()
	if r1.err != nil || r2.err != nil {
		return false
	}
	for zi := 0; zi < zooms; zi++ {
		for i := uint32(0); i < numPOIs[zi]; i++ {
			if !streamPOIEqualUnordered(r1, r2, poiMap) {
				return false
			}
		}
	}
	for zi := 0; zi < zooms; zi++ {
		for i := uint32(0); i < numWays[zi]; i++ {
			if !streamWayEqualUnordered(r1, r2, wayMap) {
				return false
			}
		}
	}
	return r1.err == nil && r2.err == nil
}

// wayLessLight compares two WayProperties without decoded block coordinates.
// Uses num_way_block and encodedBlocks bytes in place of the block[] slice.
func wayLessLight(a, b *WayProperties) bool {
	if a.layer != b.layer {
		return a.layer < b.layer
	}
	if a.num_way_block != b.num_way_block {
		return a.num_way_block < b.num_way_block
	}
	if len(a.tag_id) != len(b.tag_id) {
		return len(a.tag_id) < len(b.tag_id)
	}
	if a.has_label_position != b.has_label_position {
		return b.has_label_position
	}
	if a.has_label_position && !a.label_position.eq(b.label_position) {
		return a.label_position.less(b.label_position)
	}
	for i := range a.tag_id {
		if a.tag_id[i] != b.tag_id[i] {
			return a.tag_id[i] < b.tag_id[i]
		}
	}
	if a.name != b.name {
		return a.name < b.name
	}
	if a.house_number != b.house_number {
		return a.house_number < b.house_number
	}
	if a.reference != b.reference {
		return a.reference < b.reference
	}
	return bytes.Compare(a.encodedBlocks, b.encodedBlocks) < 0
}

// tilesEqualLight checks semantic equality using light-parsed TileData
// (block==nil, encodedBlocks has raw coordinate bytes). Returns true only when
// provably equal; false means "unknown — fall through to full parse".
func tilesEqualLight(t1, t2 *TileData) bool {
	if len(t1.poi_data) != len(t2.poi_data) {
		return false
	}
	for zi := range t1.poi_data {
		p1, p2 := t1.poi_data[zi], t2.poi_data[zi]
		w1, w2 := t1.way_data[zi], t2.way_data[zi]
		if len(p1) != len(p2) || len(w1) != len(w2) {
			return false
		}
		// Sort tag_id within each POI/way before sorting the elements themselves,
		// mirroring what normalize() does. Without this step, two POIs with the same
		// tags in different order compare as different.
		for i := range p1 {
			sort.Sort(Uint32Slice(p1[i].tag_id))
		}
		for i := range p2 {
			sort.Sort(Uint32Slice(p2[i].tag_id))
		}
		for i := range w1 {
			sort.Sort(Uint32Slice(w1[i].tag_id))
		}
		for i := range w2 {
			sort.Sort(Uint32Slice(w2[i].tag_id))
		}
		sort.Sort(CmpByPOIData(p1))
		sort.Sort(CmpByPOIData(p2))
		for i := range p1 {
			if !p1[i].eq(&p2[i]) {
				return false
			}
		}
		// Sort by index to avoid copying large WayProperties structs during swap.
		idx1 := make([]int, len(w1))
		for i := range idx1 {
			idx1[i] = i
		}
		sort.Slice(idx1, func(a, b int) bool { return wayLessLight(&w1[idx1[a]], &w1[idx1[b]]) })
		idx2 := make([]int, len(w2))
		for i := range idx2 {
			idx2[i] = i
		}
		sort.Slice(idx2, func(a, b int) bool { return wayLessLight(&w2[idx2[a]], &w2[idx2[b]]) })
		for i := range idx1 {
			a, b := &w1[idx1[i]], &w2[idx2[i]]
			if wayLessLight(a, b) || wayLessLight(b, a) {
				return false
			}
		}
	}
	return true
}

func CmdDiff(args []string, flagDetail bool, ignoreComment, ignoreTimestamp bool, strict bool) error {
	if len(args) != 2 {
		return errors.New("only 2 arguments")
	}

	var ps [2]*MapsforgeParser
	for i, fn := range args {
		p, err := ParseFile(fn, false)
		if err != nil {
			return err
		}

		ps[i] = p
		defer p.Close()
	}

	ps[0].Strict = strict
	ps[1].Strict = strict

	var found_diff bool
	if compareHeaders(&ps[0].data.header, &ps[1].data.header, ignoreComment, ignoreTimestamp) {
		found_diff = true
	}

	if !zic_eq(ps[0].data.header.zoom_interval, ps[1].data.header.zoom_interval) {
		fmt.Println("Warning: zoom interval config mismatch — skipping tile comparison")
		return errors.New("files differ")
	}

	// Proposal 3: build tag mappings directly from header tag tables — no tile parse needed.
	var poi_stats_h [2]TagsStat
	var way_stats_h [2]TagsStat
	for i := range ps {
		poi_stats_h[i].init(ps[i].data.header.poi_tags)
		way_stats_h[i].init(ps[i].data.header.way_tags)
	}
	merged_poi, pm := merge_tags([]TagsStat{poi_stats_h[0], poi_stats_h[1]})
	merged_way, wm := merge_tags([]TagsStat{way_stats_h[0], way_stats_h[1]})
	merged_stats := map_stats{poi_stats: merged_poi, way_stats: merged_way}
	poi_mapping := [2][]uint32{pm[0], pm[1]}
	way_mapping := [2][]uint32{wm[0], wm[1]}

	// Direct mappings ps[0]→ps[1] for fast-path comparisons.
	poi_direct := buildTagMapByString(ps[0].data.header.poi_tags, ps[1].data.header.poi_tags)
	way_direct := buildTagMapByString(ps[0].data.header.way_tags, ps[1].data.header.way_tags)
	// Proposal D: hoist identity check — computed once, not per tile.
	poiIdentity := isIdentityMapping(poi_direct)
	wayIdentity := isIdentityMapping(way_direct)

	max_si := min(len(ps[0].data.subfiles), len(ps[1].data.subfiles))
	for si := 0; si < max_si; si++ {
		sf1 := &ps[0].data.subfiles[si]
		sf2 := &ps[1].data.subfiles[si]

		min_x := min(sf1.x, sf2.x)
		max_x := max(sf1.X, sf2.X)
		min_y := min(sf1.y, sf2.y)
		max_y := max(sf1.Y, sf2.Y)

		zic1 := &ps[0].data.header.zoom_interval[si]
		zic2 := &ps[1].data.header.zoom_interval[si]
		sameZoomRange := zic1.min_zoom_level == zic2.min_zoom_level &&
			zic1.max_zoom_level == zic2.max_zoom_level

		for x := min_x; x <= max_x; x++ {
			for y := min_y; y <= max_y; y++ {
				i1 := ps[0].GetTileIndex(si, x, y)
				i2 := ps[1].GetTileIndex(si, x, y)

				if (i1 == nil) != (i2 == nil) {
					fmt.Printf("Tile existence mismatch: si=%d x=%d y=%d (map1: %v, map2: %v)\n", si, x, y, i1 != nil, i2 != nil)
					continue
				}
				if i1 == nil {
					continue
				}

				if i1.IsWater != i2.IsWater {
					fmt.Printf("Tile water flag mismatch: si=%d x=%d y=%d (map1: %v, map2: %v)\n", si, x, y, i1.IsWater, i2.IsWater)
					found_diff = true
				}

				b1, err := ps[0].GetRawTileBytes(si, x, y)
				if err != nil {
					return err
				}
				b2, err := ps[1].GetRawTileBytes(si, x, y)
				if err != nil {
					return err
				}

				// Proposal 1: byte equality fast path (identity mapping).
				if b1 != nil && poiIdentity && wayIdentity && bytes.Equal(b1, b2) {
					continue
				}

				// Proposal 2: streaming comparison (handles tag renumbering and arbitrary
				// tag ordering within elements, zero allocations).
				if b1 != nil && b2 != nil && sameZoomRange &&
					streamTilesEqualUnordered(b1, b2, poi_direct, way_direct, &ps[0].data.header, zic2) {
					continue
				}

				// Proposal 6: light parse — confirm equality without coordinate (VbeS) decoding.
				if b1 != nil && b2 != nil {
					lt1, err := ps[0].GetTileDataUncachedLight(si, x, y)
					if err != nil {
						return err
					}
					lt2, err := ps[1].GetTileDataUncachedLight(si, x, y)
					if err != nil {
						return err
					}
					remapTile(lt1, poi_mapping[0], way_mapping[0])
					remapTile(lt2, poi_mapping[1], way_mapping[1])
					if tilesEqualLight(lt1, lt2) {
						continue
					}
				}

				// Proposal 4: full parse via GetTileDataUncached (avoids caching all tiles).
				t1, err := ps[0].GetTileDataUncached(si, x, y)
				if err != nil {
					return err
				}
				t2, err := ps[1].GetTileDataUncached(si, x, y)
				if err != nil {
					return err
				}
				remapTile(t1, poi_mapping[0], way_mapping[0])
				remapTile(t2, poi_mapping[1], way_mapping[1])
				if compareTile(merged_stats, int(sf1.zoom_interval.min_zoom_level), x, y, t1, t2, flagDetail, strict) {
					found_diff = true
				}
			}
		}
	}

	if found_diff {
		return errors.New("files differ")
	}
	return nil
}

func slices_equal(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
