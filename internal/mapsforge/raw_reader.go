package mapsforge

import (
	"encoding/binary"
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
	for i, b := range r.buf {
		v |= uint32(b&0x7f) << shift
		shift += 7
		if b&0x80 == 0 {
			r.buf = r.buf[i+1:]
			return v
		}
		if shift > 32 {
			r.err = overflow
			return 0
		}
	}
	r.err = io.EOF
	return 0
}

// read VBE-S int
func (r *raw_reader) VbeS() int32 {
	if r.err != nil {
		return 0
	}
	if len(r.buf) == 0 {
		r.err = io.EOF
		return 0
	}

	b0 := r.buf[0]
	if b0&0x80 == 0 {
		// 1-byte: abs < 64
		r.buf = r.buf[1:]
		v := int32(b0 & 0x3f)
		if b0&0x40 != 0 {
			return -v
		}
		return v
	}
	if len(r.buf) >= 2 {
		b1 := r.buf[1]
		if b1&0x80 == 0 {
			// 2-byte: abs in [64, 8191]
			r.buf = r.buf[2:]
			v := int32(b0&0x7f) | int32(b1&0x3f)<<7
			if b1&0x40 != 0 {
				return -v
			}
			return v
		}
		if len(r.buf) >= 3 {
			b2 := r.buf[2]
			if b2&0x80 == 0 {
				// 3-byte: abs in [8192, 1048575]
				r.buf = r.buf[3:]
				v := int32(b0&0x7f) | int32(b1&0x7f)<<7 | int32(b2&0x3f)<<14
				if b2&0x40 != 0 {
					return -v
				}
				return v
			}
		}
	}

	// General loop for 4+ byte values (rare)
	var v int32
	var shift uint
	for i, b := range r.buf {
		if b&0x80 == 0 {
			v |= int32(b&0x3f) << shift
			r.buf = r.buf[i+1:]
			if b&0x40 != 0 {
				return -v
			}
			return v
		}
		v |= int32(b&0x7f) << shift
		shift += 7
		if shift > 32 {
			r.err = overflow
			return 0
		}
	}
	r.err = io.EOF
	return 0
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
	if len(r.buf) < 2 {
		r.err = io.EOF
		return 0
	}

	v := binary.BigEndian.Uint16(r.buf)
	r.buf = r.buf[2:]
	return v
}

func (r *raw_reader) uint32() uint32 {
	if r.err != nil {
		return 0
	}
	if len(r.buf) < 4 {
		r.err = io.EOF
		return 0
	}

	v := binary.BigEndian.Uint32(r.buf)
	r.buf = r.buf[4:]
	return v
}

func (r *raw_reader) int32() int32 {
	return int32(r.uint32())
}

func (r *raw_reader) uint64() uint64 {
	if r.err != nil {
		return 0
	}
	if len(r.buf) < 8 {
		r.err = io.EOF
		return 0
	}

	v := binary.BigEndian.Uint64(r.buf)
	r.buf = r.buf[8:]
	return v
}
