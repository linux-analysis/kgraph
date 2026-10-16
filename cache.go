package kgraph

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unsafe"
)

// cacheVersion is the layout of the cache file and of what the parsers put
// in it, so that no build mixes commits read the old way with new ones.
// TestParserDigest fails when the parsers read anything else and it stays.
const cacheVersion = 1

var cacheMagic = []byte("kgraph-c")

// cacheAlign is the boundary every fixed-width column starts at, so that
// the bytes read are used in place; the padding is zero. The file is
// little-endian, as the host is taken to be.
const cacheAlign = 8

// save writes s to path through a temporary file, so that a reader never
// sees half of it.
func (s *store) save(path string) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(f.Name())
		}
	}()
	w := bufio.NewWriterSize(f, 1<<20)
	e := &encoder{w: w}
	e.write(cacheMagic)
	e.u32(cacheVersion)
	e.hashes(s.tips)
	e.hashes(s.hashes)
	encodeSlice(e, s.order)
	encodeSlice(e, s.keys)
	encodeSlice(e, s.date)
	encodeSlice(e, s.parents.start)
	encodeSlice(e, s.parents.list)
	for _, col := range s.facts.keyCols() {
		encodeSlice(e, *col)
	}
	for _, col := range s.facts.namedCols() {
		e.u32(uint32(len(*col)))
		e.pad()
		for _, n := range *col {
			e.u32(n.commit)
			e.write(n.id[:])
		}
	}
	for _, col := range s.facts.refCols() {
		e.refs(*col)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// loadStore reads a store that save wrote. The columns of the store, and
// of the graph built from it, refer into the bytes read.
func loadStore(path string) (*store, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return decodeStore(path, b)
}

// decodeStore reads the store in b, the bytes of the file at path.
func decodeStore(path string, b []byte) (*store, error) {
	d := &decoder{b: b}
	if !bytes.Equal(d.take(len(cacheMagic)), cacheMagic) {
		return nil, fmt.Errorf("%s is not a commit graph cache", path)
	}
	if v := d.u32(); v != cacheVersion {
		return nil, fmt.Errorf("%s has version %d, not %d", path, v, cacheVersion)
	}
	s := newStore()
	s.tips = view[hash](d, d.count(20))
	s.hashes = view[hash](d, d.count(20))
	s.order = view[uint32](d, d.count(4))
	s.keys = view[uint64](d, d.count(8))
	s.date = view[int64](d, d.count(8))
	s.parents.start = view[uint32](d, d.count(4))
	s.parents.list = view[uint32](d, d.count(4))
	for _, col := range s.facts.keyCols() {
		*col = view[uint64](d, d.count(8))
	}
	for _, col := range s.facts.namedCols() {
		*col = view[namedRef](d, d.count(24))
	}
	for _, col := range s.facts.refCols() {
		*col = d.refs()
	}
	if d.err == nil && len(d.b) > 0 {
		d.err = errors.New("trailing bytes")
	}
	if d.err == nil {
		d.err = s.check()
	}
	if d.err == nil {
		s.fanout = fanoutOf(s.keys)
	}
	if d.err != nil {
		return nil, fmt.Errorf("read %s: %w", path, d.err)
	}
	return s, nil
}

// check tells whether the lengths and indexes of s fit together, so that
// a damaged cache cannot make a build index out of range.
func (s *store) check() error {
	n := uint32(len(s.hashes))
	p := &s.parents
	short := len(s.order) != int(n) || len(s.keys) != int(n) || len(s.date) != int(n) || len(p.start) != int(n)+1 || p.start[n] != uint32(len(p.list))
	for _, col := range s.facts.keyCols() {
		short = short || len(*col) != int(n)
	}
	if short {
		return errors.New("lengths do not match")
	}
	for i := range n {
		if p.start[i] > p.start[i+1] {
			return errors.New("parent offsets out of order")
		}
	}
	for _, c := range p.list {
		if c >= n {
			return errors.New("parent out of range")
		}
	}
	for _, c := range s.order {
		if c >= n {
			return errors.New("order out of range")
		}
	}
	for _, col := range s.facts.namedCols() {
		for _, r := range *col {
			if r.commit >= n {
				return errors.New("commit out of range")
			}
		}
	}
	for _, col := range s.facts.refCols() {
		refs := *col
		for i, fx := range refs {
			if fx.commit >= n || i > 0 && fx.commit < refs[i-1].commit {
				return errors.New("refs out of order")
			}
			if len(fx.id) > 40 {
				return errors.New("id too long")
			}
		}
	}
	return nil
}

// encoder writes little-endian values; the writer keeps the first error
// for Flush to return.
type encoder struct {
	w   *bufio.Writer
	n   int // bytes written
	buf [8]byte
}

func (e *encoder) write(b []byte) {
	e.w.Write(b)
	e.n += len(b)
}

// pad writes zeros up to the next column boundary.
func (e *encoder) pad() {
	var zeros [cacheAlign]byte
	if r := e.n % cacheAlign; r != 0 {
		e.write(zeros[:cacheAlign-r])
	}
}

func (e *encoder) u32(v uint32) {
	binary.LittleEndian.PutUint32(e.buf[:], v)
	e.write(e.buf[:4])
}

func (e *encoder) u64(v uint64) {
	binary.LittleEndian.PutUint64(e.buf[:], v)
	e.write(e.buf[:8])
}

func (e *encoder) bool(v bool) {
	e.buf[0] = 0
	if v {
		e.buf[0] = 1
	}
	e.write(e.buf[:1])
}

func (e *encoder) bytes(b []byte) {
	e.u32(uint32(len(b)))
	e.write(b)
}

func (e *encoder) hashes(hs []hash) {
	e.u32(uint32(len(hs)))
	e.pad()
	for _, h := range hs {
		e.write(h[:])
	}
}

// refs writes a column of ids the messages name, which are not
// fixed-width.
func (e *encoder) refs(refs []fixRef) {
	e.u32(uint32(len(refs)))
	for _, fx := range refs {
		e.u32(fx.commit)
		e.bytes(fx.id)
		e.u64(fx.subject)
		e.bool(fx.quoted)
	}
}

func encodeSlice[T uint32 | uint64 | int64](e *encoder, s []T) {
	e.u32(uint32(len(s)))
	e.pad()
	binary.Write(e.w, binary.LittleEndian, s)
	e.n += len(s) * binary.Size(T(0))
}

// decoder reads what encoder wrote and keeps the first error; after one it
// returns zero values.
type decoder struct {
	b   []byte
	off int // bytes taken
	err error
}

func (d *decoder) take(n int) []byte {
	if d.err != nil {
		return nil
	}
	if n > len(d.b) {
		d.err = io.ErrUnexpectedEOF
		return nil
	}
	b := d.b[:n:n]
	d.b = d.b[n:]
	d.off += n
	return b
}

// align skips the padding before a column.
func (d *decoder) align() {
	if r := d.off % cacheAlign; r != 0 {
		d.take(cacheAlign - r)
	}
}

// count reads the length of a list whose entries take at least size bytes
// each, and checks that the rest of the file can hold them.
func (d *decoder) count(size int) int {
	n := int(d.u32())
	if n > len(d.b)/size {
		d.err = io.ErrUnexpectedEOF
		return 0
	}
	return n
}

func (d *decoder) u32() uint32 {
	if b := d.take(4); b != nil {
		return binary.LittleEndian.Uint32(b)
	}
	return 0
}

func (d *decoder) u64() uint64 {
	if b := d.take(8); b != nil {
		return binary.LittleEndian.Uint64(b)
	}
	return 0
}

func (d *decoder) bool() bool {
	b := d.take(1)
	return b != nil && b[0] == 1
}

func (d *decoder) bytes() []byte { return d.take(int(d.u32())) }

// refs reads what encoder.refs wrote; the ids are copied out of the file.
func (d *decoder) refs() []fixRef {
	refs := make([]fixRef, d.count(17))
	for i := range refs {
		fx := &refs[i]
		fx.commit, fx.id, fx.subject, fx.quoted = d.u32(), bytes.Clone(d.bytes()), d.u64(), d.bool()
	}
	return refs
}

// view returns the n values of T at the decoder's position, the bytes read
// themselves.
func view[T uint32 | uint64 | int64 | hash | namedRef](d *decoder, n int) []T {
	var zero T
	d.align()
	b := d.take(n * int(unsafe.Sizeof(zero)))
	if b == nil || n == 0 {
		return []T{}
	}
	return unsafe.Slice((*T)(unsafe.Pointer(&b[0])), n)
}
