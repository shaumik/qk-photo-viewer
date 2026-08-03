package raw

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/shaumik/qk-photo-viewer/internal/tiff"
	"github.com/shaumik/qk-photo-viewer/internal/tiff/tifftest"
)

/* ---------- fixtures ---------- */

// encodeARW2Row is the inverse of decodeARW2Row, so tests can round-trip a
// known frame through Sony's packing. Values must be 11-bit.
func encodeARW2Row(pixels []int, w int) []byte {
	row := make([]byte, w)
	cols := arw2ColumnOrder(w)
	for g, group := range cols {
		vals := make([]int, len(group))
		for i, c := range group {
			vals[i] = pixels[c]
		}
		copy(row[g*arw2GroupBytes:], encodeARW2Group(vals))
	}
	return row
}

// arw2ColumnOrder replays the decoder's traversal to say which columns each
// 16-byte group covers.
func arw2ColumnOrder(w int) [][]int {
	var out [][]int
	col := 0
	for col < w-30 {
		g := make([]int, 0, arw2GroupPix)
		for i := 0; i < arw2GroupPix; i++ {
			g = append(g, col)
			col += 2
		}
		out = append(out, g)
		if col&1 == 1 {
			col--
		} else {
			col -= 31
		}
	}
	return out
}

func encodeARW2Group(v []int) []byte {
	imax, imin := 0, 0
	for i, x := range v {
		if x > v[imax] {
			imax = i
		}
		if x < v[imin] {
			imin = i
		}
	}
	maxv, minv := v[imax], v[imin]
	sh := 0
	for sh < 4 && (0x80<<uint(sh)) <= maxv-minv {
		sh++
	}
	b := make([]byte, arw2GroupBytes+2)
	hdr := uint32(maxv&arw2Max) | uint32(minv&arw2Max)<<11 |
		uint32(imax&0xF)<<22 | uint32(imin&0xF)<<26
	binary.LittleEndian.PutUint32(b, hdr)
	bit := arw2DeltaStart
	for i := 0; i < arw2GroupPix; i++ {
		if i == imax || i == imin {
			continue
		}
		d := (v[i] - minv) >> uint(sh)
		if d > 0x7F {
			d = 0x7F
		}
		off := bit >> 3
		cur := int(b[off]) | int(b[off+1])<<8
		cur |= d << uint(bit&7)
		b[off] = byte(cur)
		b[off+1] = byte(cur >> 8)
		bit += 7
	}
	return b[:arw2GroupBytes]
}

type fixture struct {
	w, h        int
	pixels      []int // 11-bit pre-curve samples, w*h
	toneCurve   []int64
	noSonyLevel bool
	model       string
	cropW       int
	cropH       int
	compression int64
	bps         int64
	rawBytes    []byte // overrides the ARW2 encoding when set
}

func (fx fixture) write(t *testing.T) string {
	t.Helper()
	data := fx.rawBytes
	if data == nil {
		data = make([]byte, 0, fx.w*fx.h)
		for y := 0; y < fx.h; y++ {
			data = append(data, encodeARW2Row(fx.pixels[y*fx.w:(y+1)*fx.w], fx.w)...)
		}
	}
	comp := fx.compression
	if comp == 0 {
		comp = compARW2
	}
	bps := fx.bps
	if bps == 0 {
		bps = 8
	}
	model := fx.model
	if model == "" {
		model = "ILCE-7M3"
	}

	b := tifftest.New()
	blob := b.AddBlob(data)
	d := b.AddIFD()
	d.ASCII(tiff.TagMake, "SONY").
		ASCII(tiff.TagModel, model).
		Short(tiff.TagPhotometric, tiff.PhotometricCFA).
		Short(tiff.TagImageWidth, int64(fx.w)).
		Short(tiff.TagImageLength, int64(fx.h)).
		Short(tiff.TagBitsPerSample, bps).
		Short(tiff.TagCompression, comp).
		Short(tiff.TagSamplesPerPixel, 1).
		Short(tiff.TagOrientation, 1).
		Byte(tiff.TagCFAPattern, Red, Green, Green, Blue).
		BlobOffset(tiff.TagStripOffsets, blob).
		Long(tiff.TagStripByteCounts, int64(len(data))).
		SShort(tagSonyWBRGGB, 2288, 1024, 1024, 1616)
	if !fx.noSonyLevel {
		d.Short(tagSonyBlack, 512, 512, 512, 512).Short(tagSonyWhite, 16300)
	}
	if fx.toneCurve != nil {
		d.Short(tagSonyToneCurve, fx.toneCurve...)
	}
	if fx.cropW > 0 {
		d.Long(tagPixelXDimension, int64(fx.cropW)).Long(tagPixelYDimension, int64(fx.cropH))
	}

	path := filepath.Join(t.TempDir(), "DSC00001.ARW")
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

// ramp builds w*h samples whose within-group spread stays under 128, so
// the lossy packing round-trips exactly and the test can assert equality.
func ramp(w, h int) []int {
	p := make([]int, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			p[y*w+x] = 700 + (x%16)*4 + y*3
		}
	}
	return p
}

/* ---------- tests ---------- */

func TestDecodeARW2RoundTrip(t *testing.T) {
	fx := fixture{w: 64, h: 4, pixels: ramp(64, 4)}
	im, err := Decode(fx.write(t))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if im.Width != 64 || im.Height != 4 {
		t.Fatalf("size = %dx%d, want 64x4", im.Width, im.Height)
	}
	// With no tone curve in the file the mapping is the identity on the
	// packed 12-bit index, i.e. twice the 11-bit sample.
	for y := 0; y < 4; y++ {
		for x := 0; x < 64; x++ {
			want := uint16(fx.pixels[y*64+x] * 2)
			if got := im.Data[y*64+x]; got != want {
				t.Fatalf("pixel (%d,%d) = %d, want %d", x, y, got, want)
			}
		}
	}
}

func TestDecodeARW2AppliesToneCurve(t *testing.T) {
	// Breakpoints at 1000/2000/3000/3500: slope 1 below 1000, then 2, 4, 8, 16.
	fx := fixture{w: 64, h: 2, pixels: ramp(64, 2),
		toneCurve: []int64{1000 << 2, 2000 << 2, 3000 << 2, 3500 << 2}}
	im, err := Decode(fx.write(t))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	curve, built := buildCurveFor(t, fx.toneCurve)
	if !built {
		t.Fatal("expected the tone curve to be built from the tag")
	}
	// Every sample comes back expanded through the curve, not raw.
	for x := 0; x < 64; x++ {
		want := curve[fx.pixels[x]*2]
		if got := im.Data[x]; got != want {
			t.Fatalf("pixel %d = %d, want %d", x, got, want)
		}
	}
	if curve[fx.pixels[0]*2] == uint16(fx.pixels[0]*2) {
		t.Error("fixture is degenerate: samples must land above the first breakpoint")
	}
	// The curve expands the upper segments as Sony specifies.
	if curve[1000] != 1000 {
		t.Errorf("curve[1000] = %d, want 1000 (unit slope below the first break)", curve[1000])
	}
	if got, want := curve[2000], uint16(1000+1000*2); got != want {
		t.Errorf("curve[2000] = %d, want %d", got, want)
	}
	if got, want := curve[3000], uint16(1000+1000*2+1000*4); got != want {
		t.Errorf("curve[3000] = %d, want %d", got, want)
	}
	if curve[4095] <= curve[3500] {
		t.Error("curve should keep rising through the last segment")
	}
}

func buildCurveFor(t *testing.T, pts []int64) (*[4096]uint16, bool) {
	t.Helper()
	b := tifftest.New()
	b.AddIFD().Short(tagSonyToneCurve, pts...)
	data := b.Bytes()
	f, err := tiff.Parse(newReaderAt(data), int64(len(data)))
	if err != nil {
		t.Fatalf("parse curve fixture: %v", err)
	}
	return sonyToneCurve(f)
}

type readerAt []byte

func newReaderAt(b []byte) readerAt { return readerAt(b) }
func (r readerAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(r)) {
		return 0, errors.New("out of range")
	}
	return copy(p, r[off:]), nil
}

func TestLevelsScaleWithTheCurveDomain(t *testing.T) {
	// No tone curve: samples stay in a 12-bit domain, so the file's 14-bit
	// black and white levels have to come down by two stops to match.
	im, err := Decode(fixture{w: 64, h: 2, pixels: ramp(64, 2)}.write(t))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if im.Black[0] != 128 {
		t.Errorf("black = %v, want 128 in the 12-bit domain", im.Black[0])
	}
	if im.White != 16300*0.25 {
		t.Errorf("white = %v, want %v", im.White, 16300*0.25)
	}

	// With a curve, samples are back in the sensor's 14-bit domain and the
	// tags apply as written.
	im2, err := Decode(fixture{w: 64, h: 2, pixels: ramp(64, 2),
		toneCurve: []int64{1000 << 2, 2000 << 2, 3000 << 2, 3500 << 2}}.write(t))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if im2.Black[0] != 512 || im2.White != 16300 {
		t.Errorf("black/white = %v/%v, want 512/16300", im2.Black[0], im2.White)
	}
}

func TestWhiteBalanceFromSonyLevels(t *testing.T) {
	im, err := Decode(fixture{w: 64, h: 2, pixels: ramp(64, 2)}.write(t))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	wantR, wantB := 2288.0/1024, 1616.0/1024
	if math.Abs(im.WB[0]-wantR) > 1e-9 || im.WB[1] != 1 || math.Abs(im.WB[2]-wantB) > 1e-9 {
		t.Errorf("WB = %v, want [%v 1 %v]", im.WB, wantR, wantB)
	}
}

func TestColorMatrixKnownAndUnknownBodies(t *testing.T) {
	known, err := Decode(fixture{w: 64, h: 2, pixels: ramp(64, 2), model: "ILCE-7M3"}.write(t))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if known.Approximate {
		t.Error("a listed body should not be flagged approximate")
	}
	unknown, err := Decode(fixture{w: 64, h: 2, pixels: ramp(64, 2), model: "ILCE-9000"}.write(t))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !unknown.Approximate {
		t.Error("an unlisted body should be flagged approximate")
	}
	// A neutral camera reading must come out neutral on screen, whichever
	// matrix was used — that is what row normalisation buys.
	for _, im := range []*Image{known, unknown} {
		m := im.CamToSRGB
		r := m[0] + m[1] + m[2]
		g := m[3] + m[4] + m[5]
		b := m[6] + m[7] + m[8]
		if math.Abs(r-g) > 1e-6 || math.Abs(g-b) > 1e-6 {
			t.Errorf("grey maps to (%v,%v,%v); the matrix rows are not balanced", r, g, b)
		}
	}
}

func TestDecodeUncompressed14Bit(t *testing.T) {
	const w, h = 64, 4
	raw := make([]byte, w*h*2)
	for i := 0; i < w*h; i++ {
		binary.LittleEndian.PutUint16(raw[i*2:], uint16(1000+i))
	}
	im, err := Decode(fixture{w: w, h: h, compression: compUncompressed, bps: 14, rawBytes: raw}.write(t))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	for i := 0; i < w*h; i++ {
		if got, want := im.Data[i], uint16(1000+i); got != want {
			t.Fatalf("sample %d = %d, want %d", i, got, want)
		}
	}
	if im.Black[0] != 512 || im.White != 16300 {
		t.Errorf("black/white = %v/%v, want the tags applied as written", im.Black[0], im.White)
	}
}

func TestCropsMaskedBorder(t *testing.T) {
	fx := fixture{w: 64, h: 20, pixels: ramp(64, 20), cropW: 60, cropH: 18}
	im, err := Decode(fx.write(t))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if im.Width != 60 || im.Height != 18 {
		t.Fatalf("size = %dx%d, want 60x18", im.Width, im.Height)
	}
	// Cropping trims the right and bottom, so the origin — and with it the
	// CFA phase — is unchanged.
	for y := 0; y < 18; y++ {
		for x := 0; x < 60; x++ {
			if got, want := im.Data[y*60+x], uint16(fx.pixels[y*64+x]*2); got != want {
				t.Fatalf("cropped pixel (%d,%d) = %d, want %d", x, y, got, want)
			}
		}
	}
}

func TestCropIgnoresImplausibleTags(t *testing.T) {
	// A tag describing some other image in the file (here, a tiny preview)
	// must not be mistaken for the sensor's active area.
	fx := fixture{w: 64, h: 8, pixels: ramp(64, 8), cropW: 16, cropH: 4}
	im, err := Decode(fx.write(t))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if im.Width != 64 || im.Height != 8 {
		t.Errorf("size = %dx%d, want the crop ignored at 64x8", im.Width, im.Height)
	}
}

func TestUnsupportedFormatsAreRecognisable(t *testing.T) {
	// Callers fall back to the embedded preview on ErrUnsupported, so the
	// error has to be identifiable rather than just an error.
	fx := fixture{w: 64, h: 4, compression: compLosslessJPEG, bps: 14, rawBytes: make([]byte, 64*4*2)}
	if _, err := Decode(fx.write(t)); !errors.Is(err, ErrUnsupported) {
		t.Errorf("lossless JPEG: err = %v, want ErrUnsupported", err)
	}
	fx2 := fixture{w: 64, h: 4, compression: 12345, bps: 14, rawBytes: make([]byte, 64*4*2)}
	if _, err := Decode(fx2.write(t)); !errors.Is(err, ErrUnsupported) {
		t.Errorf("unknown compression: err = %v, want ErrUnsupported", err)
	}

	path := filepath.Join(t.TempDir(), "notaraw.ARW")
	os.WriteFile(path, []byte("this is not a TIFF at all"), 0o644)
	if _, err := Decode(path); !errors.Is(err, ErrUnsupported) {
		t.Errorf("non-TIFF: err = %v, want ErrUnsupported", err)
	}
	if _, err := Decode(filepath.Join(t.TempDir(), "missing.ARW")); err == nil {
		t.Error("a missing file should error")
	}
}

func TestCFAAccessors(t *testing.T) {
	im := &Image{CFA: [4]uint8{Red, Green, Green, Blue}, Black: [4]float64{1, 2, 3, 4}}
	cases := []struct {
		x, y  int
		color uint8
		black float64
	}{
		{0, 0, Red, 1}, {1, 0, Green, 2}, {0, 1, Green, 3}, {1, 1, Blue, 4},
		{2, 2, Red, 1}, {3, 3, Blue, 4},
	}
	for _, c := range cases {
		if got := im.At(c.x, c.y); got != c.color {
			t.Errorf("At(%d,%d) = %d, want %d", c.x, c.y, got, c.color)
		}
		if got := im.BlackAt(c.x, c.y); got != c.black {
			t.Errorf("BlackAt(%d,%d) = %v, want %v", c.x, c.y, got, c.black)
		}
	}
}
