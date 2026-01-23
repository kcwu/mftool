package mapsforge

import (
	"io"
)

type raw_reader struct {
	buf []byte
	err error
}

func newRawReader(buf []byte) *raw_reader {
	return &raw_reader{
		buf,
		nil,
	}
}

// read VBE-U int
func (r *raw_reader) VbeU() uint32 {
	if r.err != nil {
		return 0
	}

	var v uint32
	var shift uint
	for {
		b := r.uint8()
		v |= uint32(b&0x7f) << shift
		shift += 7
		if b&0x80 == 0 {
			break
		}
		if shift > 32 {
			r.err = overflow
			return 0
		}
	}
	if r.err != nil {
		return 0
	}
	return v
}

// read VBE-S int
func (r *raw_reader) VbeS() int32 {
	if r.err != nil {
		return 0
	}

	var v int32
	var shift uint
	var sign bool

	for i := 0; ; i++ {
		b := r.uint8()

		if b&0x80 == 0 {
			// last byte
			v |= int32(b&0x3f) << shift
			sign = b&0x40 != 0
			break
		} else {
			v |= int32(b&0x7f) << shift
			shift += 7
		}
		if shift > 32 {
			r.err = overflow
			return 0
		}
	}
	if r.err != nil {
		return 0
	}

	if sign {
		v = -v
	}

	return v
}

// read variable length string
func (r *raw_reader) VbeString() string {
	if r.err != nil {
		return ""
	}

	return r.fixedString(r.VbeU())
}

func (r *raw_reader) fixedString(size uint32) string {
	if r.err != nil {
		return ""
	}
	if uint32(len(r.buf)) < size {
		r.err = io.EOF
		return ""
	}

	bs := r.buf[:size]
	r.buf = r.buf[size:]

	return string(bs)
}

func (r *raw_reader) uint8() uint8 {
	if r.err != nil {
		return 0
	}

	if len(r.buf) < 1 {
		r.err = io.EOF
		return 0
	}

	v := r.buf[0]
	r.buf = r.buf[1:]
	return v
}

func (r *raw_reader) uint16() uint16 {
	if r.err != nil {
		return 0
	}

	v := uint16(r.uint8())<<8 | uint16(r.uint8())
	if r.err != nil {
		return 0
	}
	return v
}

func (r *raw_reader) uint32() uint32 {
	if r.err != nil {
		return 0
	}

	v := uint32(r.uint16())<<16 | uint32(r.uint16())
	if r.err != nil {
		return 0
	}
	return v
}

func (r *raw_reader) int32() int32 {
	return int32(r.uint32())
}

func (r *raw_reader) uint64() uint64 {
	if r.err != nil {
		return 0
	}

	v := uint64(r.uint32())<<32 | uint64(r.uint32())
	if r.err != nil {
		return 0
	}
	return v
}
