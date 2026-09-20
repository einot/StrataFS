// Package xdr implements the subset of XDR (RFC 4506) needed to speak
// ONC RPC and NFSv3 on the wire.
//
// Both Writer and Reader latch the first error they hit so that long
// decode sequences can run without a check after every field; callers
// check Err() once at the end.
package xdr

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
)

var ErrShort = errors.New("xdr: short buffer")
var ErrTooLong = errors.New("xdr: length exceeds limit")

// maxVarLen bounds any variable-length field we will allocate for. It keeps a
// malformed or hostile length prefix from turning into a huge allocation.
const maxVarLen = 64 << 20

// Writer builds an XDR-encoded message.
type Writer struct {
	buf []byte
}

func NewWriter() *Writer { return &Writer{buf: make([]byte, 0, 512)} }

func (w *Writer) Bytes() []byte { return w.buf }
func (w *Writer) Len() int      { return len(w.buf) }

func (w *Writer) Uint32(v uint32) {
	w.buf = binary.BigEndian.AppendUint32(w.buf, v)
}

func (w *Writer) Int32(v int32) { w.Uint32(uint32(v)) }

func (w *Writer) Uint64(v uint64) {
	w.buf = binary.BigEndian.AppendUint64(w.buf, v)
}

func (w *Writer) Int64(v int64) { w.Uint64(uint64(v)) }

func (w *Writer) Bool(v bool) {
	if v {
		w.Uint32(1)
	} else {
		w.Uint32(0)
	}
}

// Fixed writes a fixed-length opaque: the bytes themselves, zero-padded up to
// a 4-byte boundary, with no length prefix.
func (w *Writer) Fixed(b []byte) {
	w.buf = append(w.buf, b...)
	w.pad(len(b))
}

// Opaque writes a variable-length opaque: a uint32 length followed by padded bytes.
func (w *Writer) Opaque(b []byte) {
	w.Uint32(uint32(len(b)))
	w.Fixed(b)
}

func (w *Writer) String(s string) {
	w.Uint32(uint32(len(s)))
	w.buf = append(w.buf, s...)
	w.pad(len(s))
}

func (w *Writer) pad(n int) {
	for r := (4 - n%4) % 4; r > 0; r-- {
		w.buf = append(w.buf, 0)
	}
}

// Reader decodes an XDR-encoded message.
type Reader struct {
	b   []byte
	off int
	err error
}

func NewReader(b []byte) *Reader { return &Reader{b: b} }

func (r *Reader) Err() error { return r.err }

// Remaining reports how many undecoded bytes are left.
func (r *Reader) Remaining() int { return len(r.b) - r.off }

func (r *Reader) fail(err error) {
	if r.err == nil {
		r.err = err
	}
}

func (r *Reader) take(n int) []byte {
	if r.err != nil {
		return nil
	}
	if n < 0 || r.off+n > len(r.b) {
		r.fail(ErrShort)
		return nil
	}
	s := r.b[r.off : r.off+n]
	r.off += n
	return s
}

func (r *Reader) Uint32() uint32 {
	s := r.take(4)
	if s == nil {
		return 0
	}
	return binary.BigEndian.Uint32(s)
}

func (r *Reader) Int32() int32 { return int32(r.Uint32()) }

func (r *Reader) Uint64() uint64 {
	s := r.take(8)
	if s == nil {
		return 0
	}
	return binary.BigEndian.Uint64(s)
}

func (r *Reader) Int64() int64 { return int64(r.Uint64()) }

func (r *Reader) Bool() bool { return r.Uint32() != 0 }

// Fixed reads n bytes plus padding and returns a copy.
func (r *Reader) Fixed(n int) []byte {
	s := r.take(n)
	if s == nil {
		return nil
	}
	out := make([]byte, n)
	copy(out, s)
	r.skipPad(n)
	return out
}

// Opaque reads a length-prefixed variable opaque and returns a copy.
func (r *Reader) Opaque() []byte {
	n := r.Uint32()
	if r.err != nil {
		return nil
	}
	if n > maxVarLen {
		r.fail(ErrTooLong)
		return nil
	}
	return r.Fixed(int(n))
}

func (r *Reader) String() string {
	b := r.Opaque()
	if b == nil {
		return ""
	}
	return string(b)
}

func (r *Reader) skipPad(n int) {
	if p := (4 - n%4) % 4; p > 0 {
		r.take(p)
	}
}

// Skip advances past n bytes plus padding, discarding them.
func (r *Reader) Skip(n int) {
	r.take(n)
	r.skipPad(n)
}

// CheckLen guards a length read from the wire before it is used to size work.
func CheckLen(n uint32, limit uint32) error {
	if n > limit || n > math.MaxInt32 {
		return ErrTooLong
	}
	return nil
}

var _ = io.EOF
