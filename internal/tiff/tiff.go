// Package tiff reads the IFD tag structure of TIFF-based camera files —
// an ARW, a DNG, or the EXIF block inside a JPEG. It walks the whole IFD
// tree (chained IFDs, SubIFDs, the EXIF pointer) and hands back tag→value
// maps, leaving every question of meaning to the caller.
//
// This is deliberately separate from the preview package's scanner. That
// one hunts byte ranges that look like embedded JPEGs and cares about
// nothing else; this one needs values, of every type, from every IFD.
package tiff

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// Field types, as numbered by the TIFF 6.0 spec.
const (
	TByte      = 1
	TASCII     = 2
	TShort     = 3
	TLong      = 4
	TRational  = 5
	TSByte     = 6
	TUndefined = 7
	TSShort    = 8
	TSLong     = 9
	TSRational = 10
	TFloat     = 11
	TDouble    = 12
)

var typeSize = [...]int64{0, 1, 1, 2, 4, 8, 1, 1, 2, 4, 8, 4, 8}

// Well-known tag numbers used across the decoders.
const (
	TagImageWidth       = 0x0100
	TagImageLength      = 0x0101
	TagBitsPerSample    = 0x0102
	TagCompression      = 0x0103
	TagPhotometric      = 0x0106
	TagMake             = 0x010F
	TagModel            = 0x0110
	TagStripOffsets     = 0x0111
	TagOrientation      = 0x0112
	TagSamplesPerPixel  = 0x0115
	TagRowsPerStrip     = 0x0116
	TagStripByteCounts  = 0x0117
	TagSubIFDs          = 0x014A
	TagCFARepeatDim     = 0x828D
	TagCFAPattern       = 0x828E
	TagExifIFD          = 0x8769
	TagGPSIFD           = 0x8825
	TagDNGColorMatrix1  = 0xC621
	TagDNGColorMatrix2  = 0xC622
	TagDNGAsShotNeutral = 0xC628
	TagDNGBlackLevel    = 0xC61A
	TagDNGWhiteLevel    = 0xC61D
	TagDNGCFAPattern    = 0x828E
)

// PhotometricCFA marks an IFD whose image data is an undemosaiced sensor
// mosaic — how we find the RAW among a file's several images.
const PhotometricCFA = 32803

// Guard rails: a corrupt offset must cost us a bounded read, not the heap.
const (
	maxIFDs      = 96
	maxEntries   = 2048
	maxDepth     = 8
	maxEntryData = 1 << 20
)

// Entry is a single IFD field with its payload already resolved, whether
// that payload was stored inline in the entry or out at an offset.
type Entry struct {
	Tag   uint16
	Type  uint16
	Count uint32
	Data  []byte
	bo    binary.ByteOrder
}

// IFD is one image file directory: its fields, keyed by tag.
type IFD struct {
	Entries map[uint16]Entry
	Offset  int64
}

// File is a parsed TIFF structure: every IFD we could reach, in the order
// we reached them (IFD0 first).
type File struct {
	BO   binary.ByteOrder
	IFDs []*IFD
	r    io.ReaderAt
	size int64
}

// Parse reads the IFD tree of a TIFF structure. r must be positioned so
// that offset 0 is the TIFF header — the file itself for an ARW, or the
// APP1 payload for a JPEG's EXIF block.
func Parse(r io.ReaderAt, size int64) (*File, error) {
	var hdr [8]byte
	if _, err := r.ReadAt(hdr[:], 0); err != nil {
		return nil, errors.New("tiff: too short for a header")
	}
	var bo binary.ByteOrder
	switch string(hdr[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return nil, errors.New("tiff: bad byte-order mark")
	}
	if bo.Uint16(hdr[2:4]) != 42 {
		return nil, errors.New("tiff: bad magic")
	}
	f := &File{BO: bo, r: r, size: size}
	seen := map[int64]bool{}
	f.walk(int64(bo.Uint32(hdr[4:8])), 0, seen)
	if len(f.IFDs) == 0 {
		return nil, errors.New("tiff: no readable IFDs")
	}
	return f, nil
}

func (f *File) walk(off int64, depth int, seen map[int64]bool) {
	for off != 0 && depth <= maxDepth {
		if off < 0 || off+2 > f.size || seen[off] || len(seen) >= maxIFDs {
			return
		}
		seen[off] = true
		var cnt [2]byte
		if _, err := f.r.ReadAt(cnt[:], off); err != nil {
			return
		}
		n := int(f.BO.Uint16(cnt[:]))
		if n == 0 || n > maxEntries {
			return
		}
		buf := make([]byte, n*12+4)
		if _, err := f.r.ReadAt(buf, off+2); err != nil {
			// A truncated final IFD is still worth the entries we can read.
			buf = buf[:0]
			short := make([]byte, n*12)
			if _, err2 := f.r.ReadAt(short, off+2); err2 != nil {
				return
			}
			buf = append(short, 0, 0, 0, 0)
		}
		ifd := &IFD{Entries: make(map[uint16]Entry, n), Offset: off}
		for i := 0; i < n; i++ {
			if e, ok := f.entry(buf[i*12 : i*12+12]); ok {
				ifd.Entries[e.Tag] = e
			}
		}
		f.IFDs = append(f.IFDs, ifd)

		// Descend into the pointers that hide everything interesting:
		// SubIFDs (previews and, on some bodies, the RAW), the EXIF
		// directory, and the GPS one.
		for _, sub := range ifd.Ints(TagSubIFDs) {
			f.walk(sub, depth+1, seen)
		}
		for _, tag := range []uint16{TagExifIFD, TagGPSIFD} {
			if v, ok := ifd.Int(tag); ok {
				f.walk(v, depth+1, seen)
			}
		}
		off = int64(f.BO.Uint32(buf[n*12:])) // IFD1 and friends
	}
}

// entry resolves one 12-byte directory entry, following the offset when
// the payload does not fit in the entry's four value bytes.
func (f *File) entry(e []byte) (Entry, bool) {
	tag := f.BO.Uint16(e[0:2])
	typ := f.BO.Uint16(e[2:4])
	count := f.BO.Uint32(e[4:8])
	if int(typ) >= len(typeSize) || typeSize[typ] == 0 {
		return Entry{}, false
	}
	total := typeSize[typ] * int64(count)
	if total < 0 || total > maxEntryData {
		return Entry{}, false
	}
	out := Entry{Tag: tag, Type: typ, Count: count, bo: f.BO}
	if total <= 4 {
		out.Data = e[8 : 8+total]
		return out, true
	}
	off := int64(f.BO.Uint32(e[8:12]))
	if off <= 0 || off+total > f.size {
		return Entry{}, false
	}
	buf := make([]byte, total)
	if _, err := f.r.ReadAt(buf, off); err != nil {
		return Entry{}, false
	}
	out.Data = buf
	return out, true
}

/* ---------- typed reads ---------- */

// Ints decodes the entry as signed integers. Rationals are truncated;
// callers wanting the fractional part use Floats.
func (e Entry) Ints() []int64 {
	sz := typeSize[e.Type]
	n := int(e.Count)
	if sz == 0 || n == 0 || int64(len(e.Data)) < sz*int64(n) {
		return nil
	}
	out := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		b := e.Data[int64(i)*sz:]
		switch e.Type {
		case TByte, TUndefined, TASCII:
			out = append(out, int64(b[0]))
		case TSByte:
			out = append(out, int64(int8(b[0])))
		case TShort:
			out = append(out, int64(e.bo.Uint16(b)))
		case TSShort:
			out = append(out, int64(int16(e.bo.Uint16(b))))
		case TLong:
			out = append(out, int64(e.bo.Uint32(b)))
		case TSLong:
			out = append(out, int64(int32(e.bo.Uint32(b))))
		case TRational:
			d := e.bo.Uint32(b[4:])
			if d == 0 {
				out = append(out, 0)
			} else {
				out = append(out, int64(e.bo.Uint32(b)/d))
			}
		case TSRational:
			d := int32(e.bo.Uint32(b[4:]))
			if d == 0 {
				out = append(out, 0)
			} else {
				out = append(out, int64(int32(e.bo.Uint32(b))/d))
			}
		case TFloat:
			out = append(out, int64(math.Float32frombits(e.bo.Uint32(b))))
		case TDouble:
			out = append(out, int64(math.Float64frombits(e.bo.Uint64(b))))
		}
	}
	return out
}

// Floats decodes the entry as float64s, keeping rational precision.
func (e Entry) Floats() []float64 {
	sz := typeSize[e.Type]
	n := int(e.Count)
	if sz == 0 || n == 0 || int64(len(e.Data)) < sz*int64(n) {
		return nil
	}
	out := make([]float64, 0, n)
	for i := 0; i < n; i++ {
		b := e.Data[int64(i)*sz:]
		switch e.Type {
		case TRational:
			num, den := e.bo.Uint32(b), e.bo.Uint32(b[4:])
			if den == 0 {
				out = append(out, 0)
			} else {
				out = append(out, float64(num)/float64(den))
			}
		case TSRational:
			num, den := int32(e.bo.Uint32(b)), int32(e.bo.Uint32(b[4:]))
			if den == 0 {
				out = append(out, 0)
			} else {
				out = append(out, float64(num)/float64(den))
			}
		case TFloat:
			out = append(out, float64(math.Float32frombits(e.bo.Uint32(b))))
		case TDouble:
			out = append(out, math.Float64frombits(e.bo.Uint64(b)))
		default:
			ints := Entry{Tag: e.Tag, Type: e.Type, Count: 1, Data: b, bo: e.bo}.Ints()
			if len(ints) == 1 {
				out = append(out, float64(ints[0]))
			}
		}
	}
	return out
}

// String decodes an ASCII entry, trimmed of its NUL terminator and any
// trailing blanks cameras like to pad with.
func (e Entry) String() string {
	b := e.Data
	for len(b) > 0 && (b[len(b)-1] == 0 || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return string(b)
}

/* ---------- IFD conveniences ---------- */

// Int returns the first integer value of a tag.
func (d *IFD) Int(tag uint16) (int64, bool) {
	e, ok := d.Entries[tag]
	if !ok {
		return 0, false
	}
	v := e.Ints()
	if len(v) == 0 {
		return 0, false
	}
	return v[0], true
}

// IntOr returns the first integer value of a tag, or def when absent.
func (d *IFD) IntOr(tag uint16, def int64) int64 {
	if v, ok := d.Int(tag); ok {
		return v
	}
	return def
}

// Ints returns every integer value of a tag (empty when absent).
func (d *IFD) Ints(tag uint16) []int64 {
	if e, ok := d.Entries[tag]; ok {
		return e.Ints()
	}
	return nil
}

// Floats returns every value of a tag as float64 (empty when absent).
func (d *IFD) Floats(tag uint16) []float64 {
	if e, ok := d.Entries[tag]; ok {
		return e.Floats()
	}
	return nil
}

// Str returns an ASCII tag's value, or "" when absent.
func (d *IFD) Str(tag uint16) string {
	if e, ok := d.Entries[tag]; ok {
		return e.String()
	}
	return ""
}

// Has reports whether the IFD carries a tag.
func (d *IFD) Has(tag uint16) bool { _, ok := d.Entries[tag]; return ok }

// Find returns the first IFD satisfying pred, or nil.
func (f *File) Find(pred func(*IFD) bool) *IFD {
	for _, d := range f.IFDs {
		if pred(d) {
			return d
		}
	}
	return nil
}

// Lookup returns the first IFD carrying tag, searching every IFD — camera
// files scatter the fields we need across IFD0, SubIFDs and EXIF.
func (f *File) Lookup(tag uint16) (*IFD, bool) {
	for _, d := range f.IFDs {
		if d.Has(tag) {
			return d, true
		}
	}
	return nil, false
}

// AnyInt returns the first integer value of tag found in any IFD.
func (f *File) AnyInt(tag uint16) (int64, bool) {
	if d, ok := f.Lookup(tag); ok {
		return d.Int(tag)
	}
	return 0, false
}

// AnyInts returns every integer value of tag from the first IFD carrying it.
func (f *File) AnyInts(tag uint16) []int64 {
	if d, ok := f.Lookup(tag); ok {
		return d.Ints(tag)
	}
	return nil
}

// AnyFloats returns tag's values as floats from the first IFD carrying it.
func (f *File) AnyFloats(tag uint16) []float64 {
	if d, ok := f.Lookup(tag); ok {
		return d.Floats(tag)
	}
	return nil
}

// AnyStr returns tag's ASCII value from the first IFD carrying it.
func (f *File) AnyStr(tag uint16) string {
	if d, ok := f.Lookup(tag); ok {
		return d.Str(tag)
	}
	return ""
}

// ReadAt exposes the underlying reader so callers can pull image strips.
func (f *File) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off+int64(len(p)) > f.size {
		return 0, fmt.Errorf("tiff: read of %d bytes at %d is outside the file", len(p), off)
	}
	return f.r.ReadAt(p, off)
}

// Size is the length of the TIFF space, in bytes.
func (f *File) Size() int64 { return f.size }
