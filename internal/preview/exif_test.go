package preview

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"

	"github.com/shaumik/qk-photo-viewer/internal/preview/previewtest"
)

// buildMetaTIFF hand-lays-out a little-endian TIFF with Model, Exif and GPS
// IFDs at precomputed offsets; b.Len() assertions catch layout drift.
func buildMetaTIFF(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	le := binary.LittleEndian
	w16 := func(v uint16) { binary.Write(&b, le, v) }
	w32 := func(v uint32) { binary.Write(&b, le, v) }
	entry := func(tag, typ uint16, count, val uint32) {
		w16(tag)
		w16(typ)
		w32(count)
		w32(val)
	}
	at := func(want int) {
		if b.Len() != want {
			t.Fatalf("layout drift: at %d, want %d", b.Len(), want)
		}
	}

	b.WriteString("II")
	w16(42)
	w32(8)

	at(8) // IFD0
	w16(3)
	entry(0x0110, 2, 10, 50) // Model -> offset 50
	entry(0x8769, 4, 1, 60)  // Exif IFD -> 60
	entry(0x8825, 4, 1, 170) // GPS IFD -> 170
	w32(0)

	at(50)
	b.WriteString("ILCE-6000\x00")

	at(60) // Exif IFD
	w16(5)
	entry(0x9003, 2, 20, 126) // DateTimeOriginal
	entry(0x829A, 5, 1, 146)  // ExposureTime
	entry(0x829D, 5, 1, 154)  // FNumber
	entry(0x8827, 3, 1, 400)  // ISO, inline
	entry(0x920A, 5, 1, 162)  // FocalLength
	w32(0)

	at(126)
	b.WriteString("2026:08:02 16:41:03\x00")
	at(146)
	w32(1)
	w32(2000) // 1/2000s
	at(154)
	w32(56)
	w32(10) // f/5.6
	at(162)
	w32(200)
	w32(1) // 200mm

	at(170) // GPS IFD
	w16(4)
	entry(0x0001, 2, 2, uint32('N')) // "N\0" inline (little-endian: N first)
	entry(0x0002, 5, 3, 224)         // latitude rationals
	entry(0x0003, 2, 2, uint32('W'))
	entry(0x0004, 5, 3, 248)
	w32(0)

	at(224)
	for _, v := range []uint32{37, 1, 49, 1, 1188, 100} { // 37° 49' 11.88"
		w32(v)
	}
	at(248)
	for _, v := range []uint32{122, 1, 28, 1, 4404, 100} { // 122° 28' 44.04"
		w32(v)
	}
	return b.Bytes()
}

func TestReadMeta(t *testing.T) {
	path := write(t, "DSC00100.ARW", buildMetaTIFF(t))
	m, err := ReadMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	if m.Camera != "ILCE-6000" {
		t.Errorf("Camera = %q", m.Camera)
	}
	if m.Taken != "2026-08-02 16:41:03" {
		t.Errorf("Taken = %q", m.Taken)
	}
	if m.Shutter != "1/2000s" {
		t.Errorf("Shutter = %q", m.Shutter)
	}
	if m.Aperture != "f/5.6" {
		t.Errorf("Aperture = %q", m.Aperture)
	}
	if m.ISO != 400 {
		t.Errorf("ISO = %d", m.ISO)
	}
	if m.Focal != "200mm" {
		t.Errorf("Focal = %q", m.Focal)
	}
	if m.Lat == nil || m.Lng == nil {
		t.Fatalf("GPS missing: %+v", m)
	}
	if math.Abs(*m.Lat-37.81997) > 1e-4 {
		t.Errorf("Lat = %v", *m.Lat)
	}
	if math.Abs(*m.Lng-(-122.47890)) > 1e-4 {
		t.Errorf("Lng = %v (should be negative: W)", *m.Lng)
	}
}

func TestReadMetaAbsentIsEmptyNotError(t *testing.T) {
	// A plain JPEG with no EXIF, and outright garbage: both yield a zero
	// Meta without an error — no metadata is a normal state.
	for name, data := range map[string][]byte{
		"plain.JPG": previewtest.JPEGBlob(500, 0x11),
		"junk.ARW":  []byte("not a real file at all"),
	} {
		m, err := ReadMeta(write(t, name, data))
		if err != nil {
			t.Errorf("%s: unexpected error %v", name, err)
		}
		if m != (Meta{}) {
			t.Errorf("%s: want zero Meta, got %+v", name, m)
		}
	}
}
