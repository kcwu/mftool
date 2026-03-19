package mapsforge

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/zeebo/xxh3"
)

// xxh3VbeStrSeed reads a VbeU-length string from r and chains it into h using
// xxh3.HashSeed. The fl byte (already in the fixed header) encodes which fields
// are present, so the raw string bytes are unambiguous without a length prefix.
func xxh3VbeStrSeed(h uint64, r *raw_reader) uint64 {
	n := r.VbeU()
	if r.err != nil || int(n) > len(r.buf) {
		if r.err == nil {
			r.err = io.EOF
		}
		return h
	}
	h = xxh3.HashSeed(r.buf[:n], h)
	r.buf = r.buf[n:]
	return h
}

// poiFingerprintStr computes a canonical xxh3 fingerprint for a single POI.
// Tags are combined via commutative xxh3.HashString sum (no sort needed).
// Fixed fields are packed into a 19-byte buffer and hashed once; optional
// fields are chained with xxh3.HashSeed.
func poiFingerprintStr(r *raw_reader, poiTags []string) (uint64, bool) {
	lat := r.VbeS()
	lon := r.VbeS()
	sp := r.uint8()
	layer := sp >> 4
	numTag := int(sp & 0xf)
	var tagSum uint64
	for ti := 0; ti < numTag; ti++ {
		t := r.VbeU()
		if r.err != nil {
			return 0, false
		}
		if int(t) >= len(poiTags) {
			return 0, false
		}
		tagSum += xxh3.HashString(poiTags[t])
	}
	fl := r.uint8()
	if r.err != nil {
		return 0, false
	}
	var fixed [19]byte
	binary.LittleEndian.PutUint32(fixed[0:], uint32(lat))
	binary.LittleEndian.PutUint32(fixed[4:], uint32(lon))
	fixed[8] = layer
	fixed[9] = byte(numTag)
	fixed[10] = fl
	binary.LittleEndian.PutUint64(fixed[11:], tagSum)
	h := xxh3.Hash(fixed[:19])
	if fl>>7&1 != 0 {
		h = xxh3VbeStrSeed(h, r)
	}
	if fl>>6&1 != 0 {
		h = xxh3VbeStrSeed(h, r)
	}
	if fl>>5&1 != 0 {
		var buf [4]byte
		binary.LittleEndian.PutUint32(buf[:], uint32(r.VbeS()))
		h = xxh3.HashSeed(buf[:], h)
	}
	if r.err != nil {
		return 0, false
	}
	return h, true
}

// wayFingerprintStr computes a canonical xxh3 fingerprint for a single way.
// Tags use commutative xxh3.HashString sum; block bytes are hashed with
// xxh3.HashSeed (AVX2/SSE2 accelerated, seeded by the accumulated header hash).
func wayFingerprintStr(r *raw_reader, wayTags []string) (uint64, bool) {
	sz := r.VbeU()
	if r.err != nil {
		return 0, false
	}
	start := len(r.buf)
	bitmap := r.uint16()
	sp := r.uint8()
	layer := sp >> 4
	numTag := int(sp & 0xf)
	var tagSum uint64
	for ti := 0; ti < numTag; ti++ {
		t := r.VbeU()
		if r.err != nil {
			return 0, false
		}
		if int(t) >= len(wayTags) {
			return 0, false
		}
		tagSum += xxh3.HashString(wayTags[t])
	}
	fl := r.uint8()
	if r.err != nil {
		return 0, false
	}
	var fixed [13]byte
	binary.LittleEndian.PutUint16(fixed[0:], bitmap)
	fixed[2] = layer
	fixed[3] = byte(numTag)
	fixed[4] = fl
	binary.LittleEndian.PutUint64(fixed[5:], tagSum)
	h := xxh3.Hash(fixed[:13])
	if fl>>7&1 != 0 {
		h = xxh3VbeStrSeed(h, r)
	}
	if fl>>6&1 != 0 {
		h = xxh3VbeStrSeed(h, r)
	}
	if fl>>5&1 != 0 {
		h = xxh3VbeStrSeed(h, r)
	}
	if fl>>4&1 != 0 {
		var buf [8]byte
		binary.LittleEndian.PutUint32(buf[0:], uint32(r.VbeS()))
		binary.LittleEndian.PutUint32(buf[4:], uint32(r.VbeS()))
		h = xxh3.HashSeed(buf[:8], h)
	}
	if r.err != nil {
		return 0, false
	}
	consumed := start - len(r.buf)
	blockLen := int(sz) - consumed
	if blockLen < 0 || len(r.buf) < blockLen {
		return 0, false
	}
	h = xxh3.HashSeed(r.buf[:blockLen], h)
	r.buf = r.buf[blockLen:]
	return h, true
}

// tileHashInto computes per-zoom POI and way fingerprint sums for tile bytes b
// and writes them into sums[0..zooms-1] (POI) and sums[zooms..2*zooms-1] (way).
// sums must have length 2*zooms.
func tileHashInto(b []byte, zooms int, poiTags, wayTags []string, sums []uint64) error {
	r := newRawReader(b)
	var numPOIs [256]uint32
	var numWays [256]uint32
	for zi := 0; zi < zooms; zi++ {
		numPOIs[zi] = r.VbeU()
		numWays[zi] = r.VbeU()
	}
	r.VbeU() // first_way_offset — skip
	if r.err != nil {
		return r.err
	}
	// Commit element counts so that a tile with N elements of fingerprint 0
	// is distinguishable from a tile with fewer elements.
	for zi := 0; zi < zooms; zi++ {
		sums[zi] += uint64(numPOIs[zi])
		sums[zooms+zi] += uint64(numWays[zi])
	}
	for zi := 0; zi < zooms; zi++ {
		for range numPOIs[zi] {
			fp, ok := poiFingerprintStr(r, poiTags)
			if !ok {
				return fmt.Errorf("POI parse error at zoom %d", zi)
			}
			sums[zi] += fp
		}
	}
	for zi := 0; zi < zooms; zi++ {
		for range numWays[zi] {
			fp, ok := wayFingerprintStr(r, wayTags)
			if !ok {
				return fmt.Errorf("way parse error at zoom %d", zi)
			}
			sums[zooms+zi] += fp
		}
	}
	if r.err != nil {
		return r.err
	}
	return nil
}

// hashW wraps an io.Writer (sha256.Hash) with helpers for fixed-size binary encoding.
type hashW struct {
	w   io.Writer
	buf [8]byte
}

func (h *hashW) u8(v uint8)   { h.w.Write([]byte{v}) }
func (h *hashW) u32(v uint32) { binary.LittleEndian.PutUint32(h.buf[:4], v); h.w.Write(h.buf[:4]) }
func (h *hashW) u64(v uint64) { binary.LittleEndian.PutUint64(h.buf[:8], v); h.w.Write(h.buf[:8]) }
func (h *hashW) i32(v int32)  { h.u32(uint32(v)) }
func (h *hashW) str(s string) { h.u32(uint32(len(s))); io.WriteString(h.w, s) }

// SemanticHash computes a 64-character hex semantic hash of the map at path.
// Two maps hash identically if and only if they are semantically equal (i.e.
// CmdDiff would report no differences, modulo the same ignore flags).
//
// Implementation: header fields are hashed into SHA-256 first; then tiles are
// fingerprinted in parallel (per-zoom commutative xxh3 sums, order-agnostic
// within each zoom); finally the ordered sums are folded into SHA-256.
func SemanticHash(path string, ignoreComment, ignoreTimestamp bool) (string, error) {
	p, err := ParseFile(path, false)
	if err != nil {
		return "", err
	}
	defer p.Close()

	hdr := &p.data.header
	if hdr.has_debug {
		return "", errors.New("hash: debug maps are not supported")
	}

	sha := sha256.New()
	hw := &hashW{w: sha}

	// Hash header fields (same set that CmdDiff compares).
	hw.i32(hdr.min.lat)
	hw.i32(hdr.min.lon)
	hw.i32(hdr.max.lat)
	hw.i32(hdr.max.lon)
	hw.u32(uint32(hdr.tile_size))
	hw.str(hdr.projection)
	hw.u8(hdr.start_zoom)
	hw.str(hdr.language_preference)
	if !ignoreComment {
		hw.str(hdr.comment)
	}
	hw.str(hdr.created_by)
	if !ignoreTimestamp {
		hw.u64(hdr.creation_date)
	}
	hw.u32(uint32(len(hdr.zoom_interval)))
	for _, zic := range hdr.zoom_interval {
		hw.u8(zic.base_zoom_level)
		hw.u8(zic.min_zoom_level)
		hw.u8(zic.max_zoom_level)
	}

	poiTags := hdr.poi_tags
	wayTags := hdr.way_tags

	// Phase 1: enumerate present tiles and pre-allocate a single flat backing
	// array for all per-zoom fingerprint sums.  Each tile needs zooms*2 slots
	// (poi sums first, then way sums).
	type tileJob struct {
		si, x, y int
		water    uint8
		b        []byte // nil for water-only tiles
		zooms    int
		base     int // offset into backing
	}
	var jobs []tileJob
	totalSlots := 0
	for si := range p.data.subfiles {
		sf := &p.data.subfiles[si]
		zic := &hdr.zoom_interval[si]
		zooms := int(zic.max_zoom_level-zic.min_zoom_level) + 1
		for x := sf.x; x <= sf.X; x++ {
			for y := sf.y; y <= sf.Y; y++ {
				idx := p.GetTileIndex(si, x, y)
				if idx == nil {
					continue
				}
				var water uint8
				if idx.IsWater {
					water = 1
				}
				b, err := p.GetRawTileBytes(si, x, y)
				if err != nil {
					return "", err
				}
				jobs = append(jobs, tileJob{si, x, y, water, b, zooms, totalSlots})
				if b != nil {
					totalSlots += zooms * 2
				}
			}
		}
	}

	// Single allocation for all fingerprint sums.
	backing := make([]uint64, totalSlots)
	errs := make([]error, len(jobs))

	// Phase 2: parallel fingerprint computation.
	// Workers atomically claim the next job index — lock-free load balancing
	// with no channel overhead and no idle time from range imbalance.
	numWorkers := min(runtime.NumCPU(), 16)
	var next atomic.Int64
	var wg sync.WaitGroup
	for range numWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				ji := int(next.Add(1)) - 1
				if ji >= len(jobs) {
					return
				}
				j := &jobs[ji]
				if j.b == nil {
					continue // water-only tile; no content to hash
				}
				errs[ji] = tileHashInto(j.b, j.zooms, poiTags, wayTags, backing[j.base:j.base+j.zooms*2])
			}
		}()
	}
	wg.Wait()

	// Phase 3: sequential SHA-256 assembly in enumeration order.
	for ji := range jobs {
		if errs[ji] != nil {
			j := &jobs[ji]
			return "", fmt.Errorf("tile si=%d x=%d y=%d: %w", j.si, j.x, j.y, errs[ji])
		}
		j := &jobs[ji]
		hw.u32(uint32(j.si))
		hw.u32(uint32(j.x))
		hw.u32(uint32(j.y))
		hw.u8(j.water)
		if j.b != nil {
			sums := backing[j.base : j.base+j.zooms*2]
			for zi := 0; zi < j.zooms; zi++ {
				hw.u64(sums[zi])         // poi sum
				hw.u64(sums[j.zooms+zi]) // way sum
			}
		}
	}

	return fmt.Sprintf("%x", sha.Sum(nil)), nil
}

// CmdHash computes and prints a 64-character hex semantic hash of a map file.
func CmdHash(args []string, ignoreComment, ignoreTimestamp bool) error {
	if len(args) != 1 {
		return errors.New("exactly 1 argument required")
	}

	h, err := SemanticHash(args[0], ignoreComment, ignoreTimestamp)
	if err != nil {
		return err
	}
	fmt.Println(h)
	return nil
}
