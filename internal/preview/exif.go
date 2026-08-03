package preview

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strings"
)

// Meta is the shooting metadata worth showing while culling. Absent fields
// stay empty — a file with no EXIF yields a zero Meta, not an error.
type Meta struct {
	Camera   string   `json:"camera,omitempty"`
	Taken    string   `json:"taken,omitempty"`    // "2026-08-02 16:41:03"
	Shutter  string   `json:"shutter,omitempty"`  // "1/2000s"
	Aperture string   `json:"aperture,omitempty"` // "f/5.6"
	ISO      int64    `json:"iso,omitempty"`
	Focal    string   `json:"focal,omitempty"` // "210mm"
	Lat      *float64 `json:"lat,omitempty"`
	Lng      *float64 `json:"lng,omitempty"`
}

// EXIF tags we surface.
const (
	tagModel        = 0x0110
	tagDateTime     = 0x0132
	tagExposureTime = 0x829A
	tagFNumber      = 0x829D
	tagISO          = 0x8827
	tagDateOriginal = 0x9003
	tagFocalLength  = 0x920A
	tagGPSIFD       = 0x8825

	gpsLatRef = 0x0001
	gpsLat    = 0x0002
	gpsLngRef = 0x0003
	gpsLng    = 0x0004
)

// ReadMeta extracts shooting metadata from an ARW or JPEG. It reads only
// the IFD structures — a few KB — never the image data.
func ReadMeta(path string) (Meta, error) {
	f, err := os.Open(path)
	if err != nil {
		return Meta{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Meta{}, err
	}
	size := st.Size()

	var magic [2]byte
	if _, err := f.ReadAt(magic[:], 0); err != nil {
		return Meta{}, nil
	}
	var space io.ReaderAt = f
	spaceSize := size
	if magic == [2]byte{0xFF, 0xD8} { // JPEG shot: EXIF lives in APP1
		sec := exifSection(f, size)
		if sec == nil {
			return Meta{}, nil
		}
		space, spaceSize = sec, sec.Size()
	}

	var hdr [8]byte
	if _, err := space.ReadAt(hdr[:], 0); err != nil {
		return Meta{}, nil
	}
	var bo binary.ByteOrder
	switch string(hdr[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return Meta{}, nil
	}
	m := &metaReader{r: space, size: spaceSize, bo: bo}

	var meta Meta
	ifd0 := m.ifd(int64(bo.Uint32(hdr[4:8])))
	if e, ok := ifd0[tagModel]; ok {
		meta.Camera = m.ascii(e)
	}
	if e, ok := ifd0[tagDateTime]; ok {
		meta.Taken = fmtTaken(m.ascii(e))
	}
	if e, ok := ifd0[tagExifIFD]; ok {
		if off, valid := m.scalar(e); valid {
			exif := m.ifd(off)
			if e, ok := exif[tagDateOriginal]; ok {
				if t := fmtTaken(m.ascii(e)); t != "" {
					meta.Taken = t
				}
			}
			if e, ok := exif[tagExposureTime]; ok {
				if num, den, valid := m.firstRational(e); valid {
					meta.Shutter = fmtShutter(num, den)
				}
			}
			if e, ok := exif[tagFNumber]; ok {
				if v := m.rationals(e); len(v) > 0 && v[0] > 0 {
					meta.Aperture = fmt.Sprintf("f/%.1f", v[0])
				}
			}
			if e, ok := exif[tagISO]; ok {
				if v, valid := m.scalar(e); valid {
					meta.ISO = v
				}
			}
			if e, ok := exif[tagFocalLength]; ok {
				if v := m.rationals(e); len(v) > 0 && v[0] > 0 {
					meta.Focal = fmt.Sprintf("%.0fmm", v[0])
				}
			}
		}
	}
	if e, ok := ifd0[tagGPSIFD]; ok {
		if off, valid := m.scalar(e); valid {
			gps := m.ifd(off)
			if lat, ok := m.coord(gps, gpsLat, gpsLatRef, "S"); ok {
				if lng, ok := m.coord(gps, gpsLng, gpsLngRef, "W"); ok {
					meta.Lat, meta.Lng = &lat, &lng
				}
			}
		}
	}
	return meta, nil
}

/* ---------- IFD value reading ---------- */

type entryData struct {
	typ   uint16
	count uint32
	raw   [4]byte // inline value, or offset to the data
}

type metaReader struct {
	r    io.ReaderAt
	size int64
	bo   binary.ByteOrder
}

func (m *metaReader) ifd(off int64) map[uint16]entryData {
	out := map[uint16]entryData{}
	if off <= 0 || off+2 > m.size {
		return out
	}
	var cnt [2]byte
	if _, err := m.r.ReadAt(cnt[:], off); err != nil {
		return out
	}
	n := int(m.bo.Uint16(cnt[:]))
	if n == 0 || n > maxEntries {
		return out
	}
	buf := make([]byte, n*12)
	if _, err := m.r.ReadAt(buf, off+2); err != nil {
		return out
	}
	for i := 0; i < n; i++ {
		e := buf[i*12 : i*12+12]
		var raw [4]byte
		copy(raw[:], e[8:12])
		out[m.bo.Uint16(e[0:2])] = entryData{
			typ: m.bo.Uint16(e[2:4]), count: m.bo.Uint32(e[4:8]), raw: raw,
		}
	}
	return out
}

func typeSize(typ uint16) int64 {
	switch typ {
	case 1, 2, 6, 7: // BYTE, ASCII, SBYTE, UNDEFINED
		return 1
	case 3, 8: // SHORT, SSHORT
		return 2
	case 4, 9: // LONG, SLONG
		return 4
	case 5, 10: // RATIONAL, SRATIONAL
		return 8
	}
	return 0
}

func (m *metaReader) bytes(e entryData) []byte {
	ts := typeSize(e.typ)
	if ts == 0 || e.count == 0 || e.count > 4096 {
		return nil
	}
	total := ts * int64(e.count)
	if total <= 4 {
		return e.raw[:total]
	}
	off := int64(m.bo.Uint32(e.raw[:]))
	if off <= 0 || off+total > m.size {
		return nil
	}
	buf := make([]byte, total)
	if _, err := m.r.ReadAt(buf, off); err != nil {
		return nil
	}
	return buf
}

func (m *metaReader) ascii(e entryData) string {
	if e.typ != 2 {
		return ""
	}
	return strings.TrimSpace(strings.TrimRight(string(m.bytes(e)), "\x00"))
}

func (m *metaReader) scalar(e entryData) (int64, bool) {
	b := m.bytes(e)
	switch e.typ {
	case 3:
		if len(b) >= 2 {
			return int64(m.bo.Uint16(b)), true
		}
	case 4:
		if len(b) >= 4 {
			return int64(m.bo.Uint32(b)), true
		}
	}
	return 0, false
}

func (m *metaReader) rationals(e entryData) []float64 {
	if e.typ != 5 {
		return nil
	}
	b := m.bytes(e)
	out := make([]float64, 0, len(b)/8)
	for i := 0; i+8 <= len(b); i += 8 {
		num, den := float64(m.bo.Uint32(b[i:])), float64(m.bo.Uint32(b[i+4:]))
		if den == 0 {
			out = append(out, 0)
		} else {
			out = append(out, num/den)
		}
	}
	return out
}

func (m *metaReader) firstRational(e entryData) (uint32, uint32, bool) {
	if e.typ != 5 {
		return 0, 0, false
	}
	b := m.bytes(e)
	if len(b) < 8 {
		return 0, 0, false
	}
	return m.bo.Uint32(b), m.bo.Uint32(b[4:]), true
}

// coord assembles degrees/minutes/seconds plus a hemisphere ref into a
// signed decimal coordinate.
func (m *metaReader) coord(gps map[uint16]entryData, valTag, refTag uint16, negRef string) (float64, bool) {
	e, ok := gps[valTag]
	if !ok {
		return 0, false
	}
	v := m.rationals(e)
	if len(v) < 3 {
		return 0, false
	}
	deg := v[0] + v[1]/60 + v[2]/3600
	if r, ok := gps[refTag]; ok && strings.HasPrefix(m.ascii(r), negRef) {
		deg = -deg
	}
	return deg, true
}

/* ---------- formatting ---------- */

func fmtShutter(num, den uint32) string {
	if num == 0 || den == 0 {
		return ""
	}
	if num < den {
		return fmt.Sprintf("1/%.0fs", float64(den)/float64(num))
	}
	return fmt.Sprintf("%.1fs", float64(num)/float64(den))
}

// fmtTaken turns EXIF "2026:08:02 16:41:03" into "2026-08-02 16:41:03".
func fmtTaken(s string) string {
	if len(s) < 19 {
		return ""
	}
	return strings.Replace(s, ":", "-", 2)
}
