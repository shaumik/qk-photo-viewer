package preview

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/shaumik/qk-photo-viewer/internal/preview/previewtest"
)

func write(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestARWThumbAndPreview(t *testing.T) {
	thumb := previewtest.JPEGBlob(300, 0x11)
	prev := previewtest.JPEGBlob(9000, 0x22)
	path := write(t, "DSC00001.ARW", previewtest.ARWBytes(thumb, prev))

	got, err := Thumb(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, thumb) {
		t.Errorf("Thumb returned %d bytes, want the %d-byte thumbnail", len(got), len(thumb))
	}
	got, err = Preview(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, prev) {
		t.Errorf("Preview returned %d bytes, want the %d-byte preview", len(got), len(prev))
	}
}

func TestCorruptOffsetsAreSkipped(t *testing.T) {
	// The preview offset points far past EOF: only the thumb is usable, and
	// both Thumb and Preview must settle on it rather than fail or misread.
	thumb := previewtest.JPEGBlob(300, 0x11)
	data := previewtest.ARWBytes(thumb, previewtest.JPEGBlob(9000, 0x22))
	data = data[:len(data)-8500] // truncate inside the preview blob
	path := write(t, "DSC00002.ARW", data)

	got, err := Preview(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, thumb) {
		t.Error("Preview should fall back to the only valid embedded JPEG")
	}
}

func TestNotATIFF(t *testing.T) {
	path := write(t, "junk.ARW", []byte("this is not a tiff at all, not even close"))
	if _, err := Preview(path); err == nil {
		t.Error("want an error for a non-TIFF file")
	}
}

func TestNoCandidates(t *testing.T) {
	// Valid TIFF header, one IFD with an irrelevant tag, no embedded JPEG.
	var b bytes.Buffer
	le := binary.LittleEndian
	b.WriteString("II")
	binary.Write(&b, le, uint16(42))
	binary.Write(&b, le, uint32(8))
	binary.Write(&b, le, uint16(1))
	binary.Write(&b, le, uint16(0x0100)) // ImageWidth
	binary.Write(&b, le, uint16(4))
	binary.Write(&b, le, uint32(1))
	binary.Write(&b, le, uint32(640))
	binary.Write(&b, le, uint32(0))
	path := write(t, "DSC00003.ARW", b.Bytes())
	if _, err := Preview(path); err == nil {
		t.Error("want an error when no embedded preview exists")
	}
}

func TestOldStyleJPEGStrip(t *testing.T) {
	// A chained IFD1 describing a single-strip image with Compression=6
	// (old-style JPEG) — the other way thumbnails hide in TIFF trees.
	blob := previewtest.JPEGBlob(500, 0x33)
	var b bytes.Buffer
	le := binary.LittleEndian
	b.WriteString("II")
	binary.Write(&b, le, uint16(42))
	binary.Write(&b, le, uint32(8))
	const ifdSize = 2 + 3*12 + 4
	blobOff := uint32(8 + ifdSize)
	binary.Write(&b, le, uint16(3))
	entry := func(tag uint16, val uint32) {
		binary.Write(&b, le, tag)
		binary.Write(&b, le, uint16(4))
		binary.Write(&b, le, uint32(1))
		binary.Write(&b, le, val)
	}
	entry(0x0103, 6)                 // Compression: old-style JPEG
	entry(0x0111, blobOff)           // StripOffsets
	entry(0x0117, uint32(len(blob))) // StripByteCounts
	binary.Write(&b, le, uint32(0))
	b.Write(blob)
	path := write(t, "DSC00004.ARW", b.Bytes())

	got, err := Preview(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, blob) {
		t.Error("strip-based embedded JPEG should be found")
	}
}

func TestJPEGShot(t *testing.T) {
	thumb := previewtest.JPEGBlob(200, 0x44)
	shot := previewtest.JPEGWithExifThumb(thumb, 5000)
	path := write(t, "DSC00005.JPG", shot)

	got, err := Preview(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, shot) {
		t.Error("a JPEG shot is its own preview")
	}
	got, err = Thumb(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, thumb) {
		t.Error("Thumb should return the EXIF thumbnail")
	}
}

func TestJPEGWithoutExif(t *testing.T) {
	shot := previewtest.JPEGBlob(4000, 0x55)
	path := write(t, "DSC00006.JPG", shot)
	for _, fn := range []func(string) ([]byte, error){Thumb, Preview} {
		got, err := fn(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, shot) {
			t.Error("with no EXIF, the shot itself is both thumb and preview")
		}
	}
}
