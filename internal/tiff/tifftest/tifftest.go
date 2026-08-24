// Package tifftest assembles synthetic little-endian TIFF files for tests:
// arbitrary IFD trees, typed tags, and image blobs at offsets the builder
// resolves for you. The RAW decoder tests use it to fabricate ARWs.
package tifftest

import (
	"bytes"
	"encoding/binary"
)

var le = binary.LittleEndian

// Blob is a handle to image data added with AddBlob; use it with
// (*IFD).BlobOffset to write a tag holding the blob's file offset.
type Blob int

const (
	kValue = iota
	kBlobOff
	kSubIFDs
)

type entry struct {
	tag   uint16
	typ   uint16
	count uint32
	kind  int
	data  []byte
	blob  Blob
	subs  []*IFD
}

// IFD is one directory under construction. The setters chain.
type IFD struct {
	entries []entry
	off     int64
}

// Builder assembles the whole file.
type Builder struct {
	ifds  []*IFD
	blobs [][]byte
}

// New starts an empty TIFF.
func New() *Builder { return &Builder{} }

// AddBlob appends image data and returns a handle to its future offset.
func (b *Builder) AddBlob(data []byte) Blob {
	b.blobs = append(b.blobs, data)
	return Blob(len(b.blobs) - 1)
}

// AddIFD appends a directory. The first one added becomes IFD0; the rest
// are reachable only if something points at them (see SubIFD).
func (b *Builder) AddIFD() *IFD {
	d := &IFD{}
	b.ifds = append(b.ifds, d)
	return d
}

func (d *IFD) add(e entry) *IFD { d.entries = append(d.entries, e); return d }

// Short writes SHORT values.
func (d *IFD) Short(tag uint16, vals ...int64) *IFD {
	buf := make([]byte, 2*len(vals))
	for i, v := range vals {
		le.PutUint16(buf[i*2:], uint16(v))
	}
	return d.add(entry{tag: tag, typ: 3, count: uint32(len(vals)), data: buf})
}

// SShort writes SSHORT values (Sony stores white-balance levels this way).
func (d *IFD) SShort(tag uint16, vals ...int64) *IFD {
	buf := make([]byte, 2*len(vals))
	for i, v := range vals {
		le.PutUint16(buf[i*2:], uint16(int16(v)))
	}
	return d.add(entry{tag: tag, typ: 8, count: uint32(len(vals)), data: buf})
}

// Long writes LONG values.
func (d *IFD) Long(tag uint16, vals ...int64) *IFD {
	buf := make([]byte, 4*len(vals))
	for i, v := range vals {
		le.PutUint32(buf[i*4:], uint32(v))
	}
	return d.add(entry{tag: tag, typ: 4, count: uint32(len(vals)), data: buf})
}

// Rational writes RATIONAL values from numerator/denominator pairs.
func (d *IFD) Rational(tag uint16, pairs ...[2]uint32) *IFD {
	buf := make([]byte, 8*len(pairs))
	for i, p := range pairs {
		le.PutUint32(buf[i*8:], p[0])
		le.PutUint32(buf[i*8+4:], p[1])
	}
	return d.add(entry{tag: tag, typ: 5, count: uint32(len(pairs)), data: buf})
}

// SRational writes SRATIONAL values from numerator/denominator pairs.
func (d *IFD) SRational(tag uint16, pairs ...[2]int32) *IFD {
	buf := make([]byte, 8*len(pairs))
	for i, p := range pairs {
		le.PutUint32(buf[i*8:], uint32(p[0]))
		le.PutUint32(buf[i*8+4:], uint32(p[1]))
	}
	return d.add(entry{tag: tag, typ: 10, count: uint32(len(pairs)), data: buf})
}

// Byte writes BYTE values.
func (d *IFD) Byte(tag uint16, vals ...byte) *IFD {
	return d.add(entry{tag: tag, typ: 1, count: uint32(len(vals)), data: append([]byte{}, vals...)})
}

// ASCII writes a NUL-terminated string.
func (d *IFD) ASCII(tag uint16, s string) *IFD {
	buf := append([]byte(s), 0)
	return d.add(entry{tag: tag, typ: 2, count: uint32(len(buf)), data: buf})
}

// BlobOffset writes a LONG tag holding the blob's resolved file offset.
func (d *IFD) BlobOffset(tag uint16, blob Blob) *IFD {
	return d.add(entry{tag: tag, typ: 4, count: 1, kind: kBlobOff, blob: blob, data: make([]byte, 4)})
}

// SubIFD writes a SubIFDs tag pointing at the given directories.
func (d *IFD) SubIFD(subs ...*IFD) *IFD {
	return d.add(entry{tag: 0x014A, typ: 4, count: uint32(len(subs)), kind: kSubIFDs,
		subs: subs, data: make([]byte, 4*len(subs))})
}

// Bytes serializes the file: header, directories in the order added,
// out-of-line tag payloads, then blobs.
func (b *Builder) Bytes() []byte {
	cursor := int64(8)
	for _, d := range b.ifds {
		d.off = cursor
		cursor += 2 + 12*int64(len(d.entries)) + 4
	}
	dataOff := make([]map[int]int64, len(b.ifds))
	for i, d := range b.ifds {
		dataOff[i] = map[int]int64{}
		for j, e := range d.entries {
			if len(e.data) > 4 {
				dataOff[i][j] = cursor
				cursor += int64(len(e.data))
				if cursor&1 == 1 {
					cursor++
				}
			}
		}
	}
	blobOff := make([]int64, len(b.blobs))
	for i, bl := range b.blobs {
		blobOff[i] = cursor
		cursor += int64(len(bl))
	}

	var out bytes.Buffer
	out.WriteString("II")
	binary.Write(&out, le, uint16(42))
	binary.Write(&out, le, uint32(8))

	for i, d := range b.ifds {
		binary.Write(&out, le, uint16(len(d.entries)))
		for j, e := range d.entries {
			payload := append([]byte{}, e.data...)
			switch e.kind {
			case kBlobOff:
				le.PutUint32(payload, uint32(blobOff[e.blob]))
			case kSubIFDs:
				for k, s := range e.subs {
					le.PutUint32(payload[k*4:], uint32(s.off))
				}
			}
			binary.Write(&out, le, e.tag)
			binary.Write(&out, le, e.typ)
			binary.Write(&out, le, e.count)
			if len(payload) > 4 {
				binary.Write(&out, le, uint32(dataOff[i][j]))
			} else {
				var inline [4]byte
				copy(inline[:], payload)
				out.Write(inline[:])
			}
		}
		binary.Write(&out, le, uint32(0)) // IFDs here are reached by pointer, not chain
	}

	for i, d := range b.ifds {
		for j, e := range d.entries {
			off, ok := dataOff[i][j]
			if !ok {
				continue
			}
			for int64(out.Len()) < off {
				out.WriteByte(0)
			}
			payload := append([]byte{}, e.data...)
			if e.kind == kSubIFDs {
				for k, s := range e.subs {
					le.PutUint32(payload[k*4:], uint32(s.off))
				}
			}
			out.Write(payload)
		}
	}
	for i, bl := range b.blobs {
		for int64(out.Len()) < blobOff[i] {
			out.WriteByte(0)
		}
		out.Write(bl)
	}
	return out.Bytes()
}
