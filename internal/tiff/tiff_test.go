package tiff_test

import (
	"bytes"
	"testing"

	"github.com/shaumik/qk-photo-viewer/internal/tiff"
	"github.com/shaumik/qk-photo-viewer/internal/tiff/tifftest"
)

func parse(t *testing.T, b []byte) *tiff.File {
	t.Helper()
	f, err := tiff.Parse(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return f
}

func TestParseReadsTypedTags(t *testing.T) {
	b := tifftest.New()
	d := b.AddIFD()
	d.ASCII(tiff.TagModel, "ILCE-7M3").
		Short(tiff.TagImageWidth, 6024).
		Long(tiff.TagImageLength, 4024).
		SShort(0x7313, 2288, 1024, 1024, 1616).
		SRational(tiff.TagDNGColorMatrix1, [2]int32{7374, 10000}, [2]int32{-2389, 10000}).
		Byte(tiff.TagCFAPattern, 0, 1, 1, 2)
	f := parse(t, b.Bytes())

	if got := f.AnyStr(tiff.TagModel); got != "ILCE-7M3" {
		t.Errorf("model = %q, want ILCE-7M3", got)
	}
	if got, _ := f.AnyInt(tiff.TagImageWidth); got != 6024 {
		t.Errorf("width = %d, want 6024", got)
	}
	if got, _ := f.AnyInt(tiff.TagImageLength); got != 4024 {
		t.Errorf("length = %d, want 4024", got)
	}
	wb := f.AnyInts(0x7313)
	if len(wb) != 4 || wb[0] != 2288 || wb[3] != 1616 {
		t.Errorf("WB levels = %v, want [2288 1024 1024 1616]", wb)
	}
	cm := f.AnyFloats(tiff.TagDNGColorMatrix1)
	if len(cm) != 2 || cm[0] != 0.7374 || cm[1] != -0.2389 {
		t.Errorf("color matrix = %v, want [0.7374 -0.2389]", cm)
	}
	cfa := f.AnyInts(tiff.TagCFAPattern)
	if len(cfa) != 4 || cfa[0] != 0 || cfa[3] != 2 {
		t.Errorf("CFA = %v, want [0 1 1 2]", cfa)
	}
}

func TestParseDescendsIntoSubIFDs(t *testing.T) {
	// Cameras bury the RAW a level or two down; the walker has to recurse.
	b := tifftest.New()
	root := b.AddIFD()
	sub := b.AddIFD()
	deep := b.AddIFD()
	deep.Short(0x8827, 6400) // ISO, two levels down
	sub.Short(tiff.TagPhotometric, tiff.PhotometricCFA).Short(tiff.TagImageWidth, 4000).SubIFD(deep)
	root.ASCII(tiff.TagMake, "SONY").SubIFD(sub)

	f := parse(t, b.Bytes())
	if len(f.IFDs) != 3 {
		t.Fatalf("reached %d IFDs, want 3", len(f.IFDs))
	}
	cfa := f.Find(func(d *tiff.IFD) bool {
		return d.IntOr(tiff.TagPhotometric, 0) == tiff.PhotometricCFA
	})
	if cfa == nil {
		t.Fatal("did not reach the CFA SubIFD")
	}
	if w, _ := cfa.Int(tiff.TagImageWidth); w != 4000 {
		t.Errorf("CFA width = %d, want 4000", w)
	}
	if iso, ok := f.AnyInt(0x8827); !ok || iso != 6400 {
		t.Errorf("nested ISO = %d (found %v), want 6400", iso, ok)
	}
}

func TestParseRejectsNonTIFF(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"short", []byte{'I', 'I'}},
		{"bad byte order", []byte{'X', 'X', 42, 0, 8, 0, 0, 0}},
		{"bad magic", []byte{'I', 'I', 43, 0, 8, 0, 0, 0}},
	} {
		if _, err := tiff.Parse(bytes.NewReader(tc.data), int64(len(tc.data))); err == nil {
			t.Errorf("%s: expected an error", tc.name)
		}
	}
}

// A corrupt offset must cost a bounded read, not an allocation the size of
// the offset it claims.
func TestParseSurvivesCorruptOffsets(t *testing.T) {
	b := tifftest.New()
	b.AddIFD().Long(tiff.TagImageWidth, 100).ASCII(tiff.TagModel, "a long model name that goes out of line")
	data := b.Bytes()
	// Point the out-of-line ASCII payload past the end of the file.
	// IFD0 sits at offset 8; its entries start after the 2-byte count.
	for i := 10; i+12 <= len(data); i += 12 {
		if data[i] == 0x10 && data[i+1] == 0x01 { // TagModel, little-endian
			data[i+8], data[i+9], data[i+10], data[i+11] = 0xFF, 0xFF, 0xFF, 0x7F
		}
	}
	f, err := tiff.Parse(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := f.AnyStr(tiff.TagModel); got != "" {
		t.Errorf("model = %q, want the unreachable entry to be dropped", got)
	}
	if w, _ := f.AnyInt(tiff.TagImageWidth); w != 100 {
		t.Errorf("width = %d, want the readable entry to survive at 100", w)
	}
}

func TestReadAtIsBoundsChecked(t *testing.T) {
	b := tifftest.New()
	blob := b.AddBlob([]byte("sensor data"))
	b.AddIFD().BlobOffset(tiff.TagStripOffsets, blob).Long(tiff.TagStripByteCounts, 11)
	data := b.Bytes()
	f := parse(t, data)

	off, _ := f.AnyInt(tiff.TagStripOffsets)
	n, _ := f.AnyInt(tiff.TagStripByteCounts)
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, off); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if string(buf) != "sensor data" {
		t.Errorf("blob = %q, want %q", buf, "sensor data")
	}
	if _, err := f.ReadAt(make([]byte, 64), int64(len(data))-2); err == nil {
		t.Error("expected an out-of-range read to fail")
	}
}
