// Package preview extracts the JPEG images that cameras embed inside their
// files — the trick that makes culling instant. An ARW is a TIFF container:
// somewhere in its IFD tree sit a small thumbnail and a large preview JPEG
// that the camera already rendered. We walk the IFDs, find those byte
// ranges, and read just them. The RAW sensor data is never decoded.
package preview

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

// Thumb returns the smallest embedded JPEG (the filmstrip thumbnail).
// For a plain JPEG shot it prefers the EXIF thumbnail, falling back to the
// file itself.
func Thumb(path string) ([]byte, error) { return pick(path, false) }

// Preview returns the largest embedded JPEG (the full-screen preview).
// For a plain JPEG shot that is the shot itself.
func Preview(path string) ([]byte, error) { return pick(path, true) }

type candidate struct{ off, length int64 }

func pick(path string, largest bool) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := st.Size()

	var magic [2]byte
	if _, err := f.ReadAt(magic[:], 0); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	isJPEG := magic == [2]byte{0xFF, 0xD8}

	// The "TIFF space" is where IFD offsets are resolved: the whole file for
	// an ARW, or the EXIF APP1 payload for a JPEG shot.
	var space io.ReaderAt = f
	var cands []candidate
	if isJPEG {
		if sec := exifSection(f, size); sec != nil {
			space = sec
			cands, _ = scanTIFF(sec, sec.Size()) // best effort; the shot itself is the fallback
		}
	} else {
		if cands, err = scanTIFF(f, size); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}

	if isJPEG && (largest || len(cands) == 0) {
		return io.ReadAll(io.NewSectionReader(f, 0, size)) // the shot is its own preview
	}
	if len(cands) == 0 {
		return nil, fmt.Errorf("%s: no embedded JPEG preview found", path)
	}
	best := cands[0]
	for _, c := range cands[1:] {
		if (largest && c.length > best.length) || (!largest && c.length < best.length) {
			best = c
		}
	}
	buf := make([]byte, best.length)
	if _, err := space.ReadAt(buf, best.off); err != nil {
		return nil, fmt.Errorf("%s: read preview: %w", path, err)
	}
	return buf, nil
}

/* ---------- TIFF walking ---------- */

const (
	tagCompression = 0x0103
	tagStripOffset = 0x0111
	tagStripCount  = 0x0117
	tagSubIFDs     = 0x014A
	tagJPEGOffset  = 0x0201 // JPEGInterchangeFormat
	tagJPEGLength  = 0x0202 // JPEGInterchangeFormatLength
	tagExifIFD     = 0x8769

	maxIFDs    = 64
	maxEntries = 1024
	maxDepth   = 8
	minJPEGLen = 128 // anything smaller is not a real preview
)

// scanTIFF collects every embedded-JPEG byte range reachable from the IFD
// tree: JPEGInterchangeFormat pairs, plus single-strip images whose
// compression says old-style/new-style JPEG.
func scanTIFF(r io.ReaderAt, size int64) ([]candidate, error) {
	var hdr [8]byte
	if _, err := r.ReadAt(hdr[:], 0); err != nil {
		return nil, errors.New("not a TIFF: too short")
	}
	var bo binary.ByteOrder
	switch string(hdr[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return nil, errors.New("not a TIFF: bad byte-order mark")
	}
	if bo.Uint16(hdr[2:4]) != 42 {
		return nil, errors.New("not a TIFF: bad magic")
	}
	s := &scanner{r: r, size: size, bo: bo, seen: map[int64]bool{}}
	s.walk(int64(bo.Uint32(hdr[4:8])), 0)
	return s.cands, nil
}

type scanner struct {
	r     io.ReaderAt
	size  int64
	bo    binary.ByteOrder
	seen  map[int64]bool
	cands []candidate
}

func (s *scanner) walk(off int64, depth int) {
	for off != 0 && depth <= maxDepth {
		if off < 0 || off+2 > s.size || s.seen[off] || len(s.seen) >= maxIFDs {
			return
		}
		s.seen[off] = true
		var cnt [2]byte
		if _, err := s.r.ReadAt(cnt[:], off); err != nil {
			return
		}
		n := int(s.bo.Uint16(cnt[:]))
		if n == 0 || n > maxEntries {
			return
		}
		buf := make([]byte, n*12+4)
		if _, err := s.r.ReadAt(buf, off+2); err != nil {
			return
		}
		var jpegOff, jpegLen, stripOff, stripLen int64
		compression := int64(-1)
		for i := 0; i < n; i++ {
			e := buf[i*12 : i*12+12]
			tag, typ := s.bo.Uint16(e[0:2]), s.bo.Uint16(e[2:4])
			count := s.bo.Uint32(e[4:8])
			switch tag {
			case tagJPEGOffset:
				jpegOff = s.scalar(e, typ)
			case tagJPEGLength:
				jpegLen = s.scalar(e, typ)
			case tagStripOffset:
				if count == 1 {
					stripOff = s.scalar(e, typ)
				}
			case tagStripCount:
				if count == 1 {
					stripLen = s.scalar(e, typ)
				}
			case tagCompression:
				compression = s.scalar(e, typ)
			case tagSubIFDs:
				for _, sub := range s.longs(e, typ, count) {
					s.walk(sub, depth+1)
				}
			case tagExifIFD:
				s.walk(s.scalar(e, typ), depth+1)
			}
		}
		if jpegOff > 0 && jpegLen > 0 {
			s.add(jpegOff, jpegLen)
		}
		if stripOff > 0 && stripLen > 0 && (compression == 6 || compression == 7) {
			s.add(stripOff, stripLen)
		}
		off = int64(s.bo.Uint32(buf[n*12:])) // chained IFD (IFD1 holds the thumbnail)
	}
}

// scalar reads a single SHORT or LONG value stored inline in the entry.
func (s *scanner) scalar(e []byte, typ uint16) int64 {
	switch typ {
	case 3: // SHORT
		return int64(s.bo.Uint16(e[8:10]))
	case 4: // LONG
		return int64(s.bo.Uint32(e[8:12]))
	}
	return 0
}

// longs reads a LONG array (inline for count 1, via offset otherwise).
func (s *scanner) longs(e []byte, typ uint16, count uint32) []int64 {
	if typ != 4 || count == 0 || count > 16 {
		return nil
	}
	if count == 1 {
		return []int64{int64(s.bo.Uint32(e[8:12]))}
	}
	off := int64(s.bo.Uint32(e[8:12]))
	buf := make([]byte, 4*count)
	if off <= 0 || off+int64(len(buf)) > s.size {
		return nil
	}
	if _, err := s.r.ReadAt(buf, off); err != nil {
		return nil
	}
	out := make([]int64, count)
	for i := range out {
		out[i] = int64(s.bo.Uint32(buf[i*4:]))
	}
	return out
}

// add records a candidate if the range is inside the file and actually
// starts with a JPEG marker — corrupt offsets are silently skipped.
func (s *scanner) add(off, length int64) {
	if off <= 0 || length < minJPEGLen || off+length > s.size {
		return
	}
	var m [2]byte
	if _, err := s.r.ReadAt(m[:], off); err != nil || m != [2]byte{0xFF, 0xD8} {
		return
	}
	for _, c := range s.cands {
		if c.off == off {
			return
		}
	}
	s.cands = append(s.cands, candidate{off, length})
}

/* ---------- JPEG (EXIF) handling ---------- */

// exifSection finds the APP1 "Exif" segment of a JPEG and returns a reader
// over its embedded TIFF structure, or nil if there is none.
func exifSection(r io.ReaderAt, size int64) *io.SectionReader {
	pos := int64(2) // past SOI
	var b [4]byte
	for pos+4 <= size {
		if _, err := r.ReadAt(b[:], pos); err != nil || b[0] != 0xFF {
			return nil
		}
		marker := b[1]
		if marker == 0xDA || marker == 0xD9 { // image data begins: no EXIF ahead
			return nil
		}
		segLen := int64(binary.BigEndian.Uint16(b[2:4]))
		if segLen < 2 {
			return nil
		}
		if marker == 0xE1 && segLen >= 16 {
			var sig [6]byte
			if _, err := r.ReadAt(sig[:], pos+4); err == nil && string(sig[:]) == "Exif\x00\x00" {
				start := pos + 10
				return io.NewSectionReader(r, start, min(size-start, segLen-8))
			}
		}
		pos += 2 + segLen
	}
	return nil
}
