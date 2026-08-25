// Package raw decodes camera RAW sensor data — the part the viewer's
// preview package deliberately never touches.
//
// Culling uses the JPEG the camera already rendered, because it is instant.
// Editing cannot: that JPEG has already been white-balanced, tone-mapped
// and clipped, so its blown highlights are gone for good and large colour
// shifts tear. This package goes to the sensor values instead — the ~2 to
// 4 stops of headroom above white and the untouched colour that make
// "recover the sky" and "fix the colour cast" possible at all.
//
// Sony ARW is what QK opens today, so ARW is what this decodes: the
// uncompressed 12/14-bit layouts and the ARW2 lossy compressed one. Files
// it cannot decode return ErrUnsupported, and callers fall back to the
// embedded preview rather than failing.
package raw

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/shaumik/qk-photo-viewer/internal/tiff"
)

// ErrUnsupported means the file is a RAW we recognise but cannot decode —
// a newer compression scheme, or a make we have no decoder for. Callers
// should fall back to the embedded preview.
var ErrUnsupported = errors.New("raw: unsupported format")

// Colours in a CFA pattern.
const (
	Red   = 0
	Green = 1
	Blue  = 2
)

// Image is one undemosaiced frame: a single sensor reading per pixel, laid
// out in the colour filter array's mosaic, plus everything needed to turn
// those readings into colour.
type Image struct {
	Width, Height int
	Data          []uint16 // Width*Height mosaic samples, row-major

	// CFA gives the colour of each position in the repeating 2x2 filter
	// block: [ (0,0), (1,0), (0,1), (1,1) ]. Sony sensors are RGGB.
	CFA [4]uint8

	// Black is the sensor's zero level per CFA position and White its
	// saturation point, both in the same units as Data.
	Black [4]float64
	White float64

	// WB are the white-balance multipliers for R, G and B, normalised so
	// green is 1, and WBSource says where they came from.
	WB       [3]float64
	WBSource string

	// CamToSRGB converts white-balanced camera RGB to linear sRGB,
	// row-major.
	CamToSRGB [9]float64

	Make, Model string
	Orientation int  // EXIF orientation, 1 when absent
	Approximate bool // colour matrix is a generic fallback, not this body's

	// ISO says how much grain to expect better than the pixels do, which
	// is what decides how hard to denoise and how much to sharpen.
	ISO int

	// Framing is the rectangle the photographer actually composed, as
	// x, y, w, h in fractions of the decoded frame. A body set to shoot
	// 16:9 records the full sensor and states the narrower picture it was
	// framed for; this is that picture. The zero value means the whole
	// frame, so a camera that says nothing costs nothing.
	//
	// It is deliberately not applied to Data. Those rows are real
	// exposure, and a crop that throws them away for good is not a crop
	// QK gets to make on someone's behalf.
	Framing [4]float64
}

// FramingRect returns the composed rectangle, defaulting to the whole
// frame. Callers get a usable rectangle without each having to know that
// the zero value is special.
func (im *Image) FramingRect() (x, y, w, h float64) {
	if im.Framing[2] <= 0 || im.Framing[3] <= 0 {
		return 0, 0, 1, 1
	}
	return im.Framing[0], im.Framing[1], im.Framing[2], im.Framing[3]
}

// At returns the CFA colour of pixel (x, y).
func (im *Image) At(x, y int) uint8 { return im.CFA[(y&1)<<1|(x&1)] }

// BlackAt returns the black level for pixel (x, y)'s CFA position.
func (im *Image) BlackAt(x, y int) float64 { return im.Black[(y&1)<<1|(x&1)] }

// Sony compression values seen in the wild.
const (
	compUncompressed = 1
	compLosslessJPEG = 7
	compARW2         = 32767
)

// Sony's private tags in the RAW IFD. Names follow ExifTool's.
const (
	tagSonyToneCurve = 0x7010
	tagSonyBlack     = 0x7310
	tagSonyWBRGGB    = 0x7313
	tagSonyWBGRBG    = 0x7303
	tagSonyWhite     = 0x787F
	tagISOSpeed      = 0x8827
)

// Decode reads a RAW file's sensor data. It is the expensive path — tens
// of megabytes read and unpacked — so callers cache the result.
func Decode(path string) (*Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	t, err := tiff.Parse(f, st.Size())
	if err != nil {
		return nil, fmt.Errorf("%w: %s is not a TIFF-based RAW", ErrUnsupported, path)
	}
	return decodeTIFF(t, path)
}

func decodeTIFF(t *tiff.File, path string) (*Image, error) {
	d := findRawIFD(t)
	if d == nil {
		return nil, fmt.Errorf("%w: %s has no sensor image", ErrUnsupported, path)
	}
	w := int(d.IntOr(tiff.TagImageWidth, 0))
	h := int(d.IntOr(tiff.TagImageLength, 0))
	if w <= 0 || h <= 0 || w > 1<<16 || h > 1<<16 {
		return nil, fmt.Errorf("%w: %s has implausible RAW dimensions %dx%d", ErrUnsupported, path, w, h)
	}

	strips, err := readStrips(t, d)
	if err != nil {
		return nil, err
	}

	im := &Image{
		Width: w, Height: h,
		CFA:         cfaPattern(d),
		Make:        t.AnyStr(tiff.TagMake),
		Model:       t.AnyStr(tiff.TagModel),
		Orientation: int(orDefault(t, tiff.TagOrientation, 1)),
		ISO:         int(orDefault(t, tagISOSpeed, 0)),
	}

	bps := int(d.IntOr(tiff.TagBitsPerSample, 0))
	comp := d.IntOr(tiff.TagCompression, compUncompressed)
	// Sony's lossy layout re-quantises through a tone curve, so the levels
	// below have to be read in whatever domain that curve lands in.
	curveDomain14 := true
	switch comp {
	case compARW2:
		curve, built := sonyToneCurve(t)
		im.Data, err = decodeARW2(strips, w, h, curve)
		curveDomain14 = built
	case compUncompressed:
		im.Data, err = decodeUncompressed(strips, w, h, bps)
	case compLosslessJPEG:
		return nil, fmt.Errorf("%w: %s uses Sony lossless compression, which QK cannot decode yet", ErrUnsupported, path)
	default:
		return nil, fmt.Errorf("%w: %s uses compression %d", ErrUnsupported, path, comp)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	readLevels(t, d, im, bps, comp, curveDomain14)
	im.WB, im.WBSource = readWhiteBalance(t, im.Make)
	im.CamToSRGB, im.Approximate = colorMatrix(t, im.Model)
	cropActive(t, im)
	return im, nil
}

// EXIF tags describing the frame the camera intends you to see.
const (
	tagPixelXDimension = 0xA002
	tagPixelYDimension = 0xA003
	tagDefaultCropSize = 0xC620
)

// cropActive trims the masked border pixels that sensors carry beyond the
// visible frame — the columns Sony uses as a black reference, which would
// otherwise show as a dark band down one edge. Sony's margin is on the
// right and bottom, so the origin is untouched and the CFA phase with it.
func cropActive(t *tiff.File, im *Image) {
	w, h := 0, 0
	if v := t.AnyInts(tagDefaultCropSize); len(v) >= 2 {
		w, h = int(v[0]), int(v[1])
	} else {
		x, okX := t.AnyInt(tagPixelXDimension)
		y, okY := t.AnyInt(tagPixelYDimension)
		if okX && okY {
			w, h = int(x), int(y)
		}
	}
	w, h = w&^1, h&^1 // keep the 2x2 filter block whole

	// The size the camera reports covers two different things, and they
	// want opposite treatment. A few percent off an axis is the masked
	// border, which is not picture and should go. A sixth off one axis is
	// the photographer having set the camera to 16:9, which is picture,
	// and is framing rather than margin.
	//
	// The same number separates them: a margin is a trim, a framing is a
	// visible fraction of the frame. Anything smaller still on both axes
	// is a tag describing some other image in the file, and is ignored.
	if w <= 0 || w > im.Width {
		w = im.Width
	}
	if h <= 0 || h > im.Height {
		h = im.Height
	}
	// No aspect a camera offers cuts an axis in half — 1:1 out of 16:9, the
	// narrowest of them, keeps 56%. So a reported size below half the frame
	// on either axis is not a framing of this picture at all; it is a tag
	// describing some other image in the file, usually the preview, and
	// nothing in it can be trusted.
	if w*2 < im.Width || h*2 < im.Height {
		return
	}
	trimW, frameW := split(w, im.Width)
	trimH, frameH := split(h, im.Height)
	if frameW < 1 || frameH < 1 {
		// Centred, which is where every body puts an aspect crop.
		im.Framing = [4]float64{(1 - frameW) / 2, (1 - frameH) / 2, frameW, frameH}
	}
	if trimW == im.Width && trimH == im.Height {
		return
	}
	out := make([]uint16, trimW*trimH)
	for y := 0; y < trimH; y++ {
		copy(out[y*trimW:(y+1)*trimW], im.Data[y*im.Width:y*im.Width+trimW])
	}
	im.Data, im.Width, im.Height = out, trimW, trimH
}

// split separates a reported size into the part that is a masked-border
// trim and the part that is the photographer's framing. It returns the
// size to crop to and the fraction of what remains that was composed.
func split(want, have int) (trim int, frame float64) {
	if want <= 0 || want >= have {
		return have, 1
	}
	if want*10 >= have*9 {
		return want, 1 // a trim: margin, not picture
	}
	return have, float64(want) / float64(have)
}

// findRawIFD picks the sensor image out of a file that also contains a
// thumbnail and a preview. A CFA photometric tag is definitive; failing
// that, the largest single-sample image wins.
func findRawIFD(t *tiff.File) *tiff.IFD {
	var best *tiff.IFD
	var bestPixels int64
	for _, d := range t.IFDs {
		if !d.Has(tiff.TagStripOffsets) {
			continue
		}
		w := d.IntOr(tiff.TagImageWidth, 0)
		h := d.IntOr(tiff.TagImageLength, 0)
		if w <= 0 || h <= 0 {
			continue
		}
		// A CFA photometric tag says "this is the mosaic" outright.
		if d.IntOr(tiff.TagPhotometric, -1) == tiff.PhotometricCFA {
			return d
		}
		// Otherwise the sensor image is the biggest one-sample-per-pixel
		// picture in the file; thumbnails and previews are three.
		if d.IntOr(tiff.TagSamplesPerPixel, 3) != 1 {
			continue
		}
		if w*h > bestPixels {
			best, bestPixels = d, w*h
		}
	}
	return best
}

// readStrips concatenates the IFD's image strips. RAW files almost always
// use one strip, but the spec allows several and some bodies use them.
func readStrips(t *tiff.File, d *tiff.IFD) ([]byte, error) {
	offs := d.Ints(tiff.TagStripOffsets)
	lens := d.Ints(tiff.TagStripByteCounts)
	if len(offs) == 0 || len(lens) == 0 {
		return nil, errors.New("raw: RAW image has no strips")
	}
	if len(lens) < len(offs) {
		return nil, errors.New("raw: strip offsets and byte counts disagree")
	}
	total := int64(0)
	for i := range offs {
		total += lens[i]
	}
	if total <= 0 || total > 1<<31 {
		return nil, fmt.Errorf("raw: implausible sensor data length %d", total)
	}
	buf := make([]byte, 0, total)
	for i, off := range offs {
		chunk := make([]byte, lens[i])
		if _, err := t.ReadAt(chunk, off); err != nil {
			return nil, fmt.Errorf("raw: read sensor strip %d: %w", i, err)
		}
		buf = append(buf, chunk...)
	}
	return buf, nil
}

func cfaPattern(d *tiff.IFD) [4]uint8 {
	p := d.Ints(tiff.TagCFAPattern)
	if len(p) != 4 {
		return [4]uint8{Red, Green, Green, Blue} // Sony sensors are RGGB
	}
	var out [4]uint8
	for i, v := range p {
		if v < 0 || v > 3 {
			return [4]uint8{Red, Green, Green, Blue}
		}
		out[i] = uint8(v)
	}
	return out
}

func orDefault(t *tiff.File, tag uint16, def int64) int64 {
	if v, ok := t.AnyInt(tag); ok && v != 0 {
		return v
	}
	return def
}

// readLevels fills in black and white points. Sony writes both in its
// private tags in the 14-bit domain; DNG-style tags are honoured too, and
// a sane default covers files carrying neither.
func readLevels(t *tiff.File, d *tiff.IFD, im *Image, bps int, comp int64, domain14 bool) {
	scale := 1.0
	if comp == compARW2 && !domain14 {
		// No tone curve in the file: samples came out in a 12-bit domain,
		// so the file's 14-bit levels have to come down to meet them.
		scale = 0.25
	}

	black := 512.0
	if v := firstInts(t, tagSonyBlack, tiff.TagDNGBlackLevel); len(v) > 0 {
		if len(v) >= 4 {
			for i := 0; i < 4; i++ {
				im.Black[i] = float64(v[i]) * scale
			}
			black = -1
		} else {
			black = float64(v[0])
		}
	} else if comp == compUncompressed && bps == 12 {
		black = 128
	}
	if black >= 0 {
		for i := range im.Black {
			im.Black[i] = black * scale
		}
	}

	white := 0.0
	if v := firstInts(t, tagSonyWhite, tiff.TagDNGWhiteLevel); len(v) > 0 && v[0] > 0 {
		white = float64(v[0]) * scale
	}
	if white <= 0 {
		switch {
		case comp == compARW2 && domain14:
			white = 16300
		case comp == compARW2:
			white = 4095
		case bps > 0 && bps <= 16:
			white = float64(int(1)<<uint(bps) - 1)
		default:
			white = 16383
		}
	}
	// A white level at or below black would divide the whole frame by zero.
	maxBlack := im.Black[0]
	for _, b := range im.Black[1:] {
		if b > maxBlack {
			maxBlack = b
		}
	}
	if white <= maxBlack {
		white = maxBlack + 1
	}
	im.White = white
	_ = d
}

func firstInts(t *tiff.File, tags ...uint16) []int64 {
	for _, tag := range tags {
		if v := t.AnyInts(tag); len(v) > 0 {
			return v
		}
	}
	return nil
}

// Where a frame's white balance came from. Two of these are answers and
// one is a guess, which is a distinction worth keeping: what QK is
// entitled to do next depends on whether the balance is known.
const (
	WBFromCamera = "camera"   // the file said so
	WBMeasured   = "measured" // fitted against the camera's own rendering
	WBDefault    = "default"  // a typical daylight balance for the make
)

// WBKnown reports whether a balance was established rather than assumed.
func WBKnown(source string) bool {
	return source == WBFromCamera || source == WBMeasured
}

// Sensors are far more sensitive to green than to red or blue, so raw
// data with no white balance applied is violently green — not slightly
// off, unusable. Some bodies publish their as-shot balance in a tag; the
// a6000 and its generation bury it in an obfuscated maker-note block that
// QK does not read. Rendering those with no balance at all is not an
// option, so they start from a typical daylight balance for the make and
// are corrected from there.
var defaultWB = map[string][3]float64{
	"SONY": {2.2, 1, 1.6},
}

var genericWB = [3]float64{2.0, 1, 1.7}

// readWhiteBalance returns the as-shot multipliers, normalised on green,
// and says whether they came from the file or from a default.
func readWhiteBalance(t *tiff.File, make string) ([3]float64, string) {
	if v := firstInts(t, tagSonyWBRGGB); len(v) >= 4 && v[1] > 0 {
		g := float64(v[1]+v[2]) / 2
		if g > 0 && v[0] > 0 && v[3] > 0 {
			return [3]float64{float64(v[0]) / g, 1, float64(v[3]) / g}, WBFromCamera
		}
	}
	if v := firstInts(t, tagSonyWBGRBG); len(v) >= 4 {
		g := float64(v[0]+v[3]) / 2
		if g > 0 && v[1] > 0 && v[2] > 0 {
			return [3]float64{float64(v[1]) / g, 1, float64(v[2]) / g}, WBFromCamera
		}
	}
	if v := t.AnyFloats(tiff.TagDNGAsShotNeutral); len(v) >= 3 && v[0] > 0 && v[1] > 0 && v[2] > 0 {
		return [3]float64{v[1] / v[0], 1, v[1] / v[2]}, WBFromCamera
	}
	if wb, ok := defaultWB[strings.ToUpper(strings.TrimSpace(make))]; ok {
		return wb, WBDefault
	}
	return genericWB, WBDefault
}

// sonyToneCurve rebuilds the piecewise-linear curve the camera used when
// quantising to the lossy layout, mapping the packed 12-bit index back to
// the sensor's 14-bit domain. Reports false when the file has no curve, in
// which case the identity below leaves samples in a 12-bit domain.
func sonyToneCurve(t *tiff.File) (*[4096]uint16, bool) {
	var lut [4096]uint16
	pts := t.AnyInts(tagSonyToneCurve)
	if len(pts) < 4 {
		for i := range lut {
			lut[i] = uint16(i)
		}
		return &lut, false
	}
	// Five segments whose slopes double: 1, 2, 4, 8, 16.
	breaks := [6]int{0, 0, 0, 0, 0, 4095}
	for i := 0; i < 4; i++ {
		v := int(pts[i]>>2) & 0xFFF
		breaks[i+1] = v
	}
	for i := 1; i < 6; i++ { // a malformed curve must stay monotonic
		if breaks[i] < breaks[i-1] {
			breaks[i] = breaks[i-1]
		}
		if breaks[i] > 4095 {
			breaks[i] = 4095
		}
	}
	for i := 0; i < 5; i++ {
		step := uint16(1) << uint(i)
		for j := breaks[i] + 1; j <= breaks[i+1]; j++ {
			lut[j] = lut[j-1] + step
		}
	}
	for j := breaks[5] + 1; j < 4096; j++ {
		lut[j] = lut[breaks[5]]
	}
	return &lut, true
}

// decodeUncompressed unpacks a linear sensor dump. Sony writes 14-bit data
// as 16-bit little-endian words; 12-bit files pack samples bit-tight.
func decodeUncompressed(buf []byte, w, h, bps int) ([]uint16, error) {
	n := w * h
	out := make([]uint16, n)
	switch {
	case len(buf) >= n*2:
		for i := 0; i < n; i++ {
			out[i] = uint16(buf[i*2]) | uint16(buf[i*2+1])<<8
		}
	case bps > 0 && bps < 16 && len(buf) >= (n*bps+7)/8:
		bit := 0
		mask := uint32(1)<<uint(bps) - 1
		for i := 0; i < n; i++ {
			byteOff := bit >> 3
			var acc uint32
			for k := 0; k < 3 && byteOff+k < len(buf); k++ {
				acc |= uint32(buf[byteOff+k]) << uint(8*k)
			}
			out[i] = uint16((acc >> uint(bit&7)) & mask)
			bit += bps
		}
	default:
		return nil, fmt.Errorf("%w: %d bytes is too little for %dx%d at %d bits", ErrUnsupported, len(buf), w, h, bps)
	}
	return out, nil
}
