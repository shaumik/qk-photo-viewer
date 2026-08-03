// Package previewtest builds tiny synthetic camera files for tests: valid
// TIFF/ARW-shaped containers with embedded JPEG blobs at known offsets.
package previewtest

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"os"

	"github.com/shaumik/qk-photo-viewer/internal/tiff/tifftest"
)

// JPEGBlob returns a fake JPEG of exactly n bytes (n >= 4): SOI marker,
// fill bytes, EOI marker. Distinct fills let tests tell blobs apart.
func JPEGBlob(n int, fill byte) []byte {
	b := make([]byte, n)
	b[0], b[1] = 0xFF, 0xD8
	for i := 2; i < n-2; i++ {
		b[i] = fill
	}
	b[n-2], b[n-1] = 0xFF, 0xD9
	return b
}

// RealJPEG encodes an actual decodable JPEG (a hue ramp with an index-tinted
// stripe) — for fixtures that a browser has to render, e.g. demo shoots.
func RealJPEG(w, h, idx int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8(40 + 180*x/max(w, 1)),
				G: uint8(30 + (idx*47)%180),
				B: uint8(200 - 150*y/max(h, 1)),
				A: 255,
			})
		}
	}
	for y := h / 3; y < h/3+max(h/10, 2) && y < h; y++ { // per-index stripe
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8((idx * 83) % 255), G: 240, B: 90, A: 255})
		}
	}
	var b bytes.Buffer
	jpeg.Encode(&b, img, &jpeg.Options{Quality: 82})
	return b.Bytes()
}

// ARWBytes builds a little-endian TIFF resembling an ARW: IFD0 carries the
// thumbnail via JPEGInterchangeFormat and points at a SubIFD carrying the
// large preview the same way.
func ARWBytes(thumb, preview []byte) []byte {
	const (
		hdrSize  = 8
		ifd0Off  = int64(hdrSize)
		ifd0Size = 2 + 3*12 + 4 // 3 entries + next pointer
		subOff   = ifd0Off + ifd0Size
		subSize  = 2 + 2*12 + 4
		thumbOff = subOff + subSize
	)
	prevOff := thumbOff + int64(len(thumb))

	var b bytes.Buffer
	le := binary.LittleEndian
	b.WriteString("II")
	binary.Write(&b, le, uint16(42))
	binary.Write(&b, le, uint32(ifd0Off))

	entry := func(tag uint16, val int64) {
		binary.Write(&b, le, tag)
		binary.Write(&b, le, uint16(4)) // LONG
		binary.Write(&b, le, uint32(1))
		binary.Write(&b, le, uint32(val))
	}

	binary.Write(&b, le, uint16(3)) // IFD0: 3 entries
	entry(0x014A, subOff)           // SubIFDs -> preview IFD
	entry(0x0201, thumbOff)         // thumbnail offset
	entry(0x0202, int64(len(thumb)))
	binary.Write(&b, le, uint32(0)) // no chained IFD

	binary.Write(&b, le, uint16(2)) // SubIFD: 2 entries
	entry(0x0201, prevOff)
	entry(0x0202, int64(len(preview)))
	binary.Write(&b, le, uint32(0))

	b.Write(thumb)
	b.Write(preview)
	return b.Bytes()
}

// WriteARW writes a synthetic ARW to path.
func WriteARW(path string, thumb, preview []byte) error {
	return os.WriteFile(path, ARWBytes(thumb, preview), 0o644)
}

// JPEGWithExifThumb builds a JPEG shot whose APP1 EXIF block embeds a
// thumbnail via an IFD, followed by fake image data.
func JPEGWithExifThumb(thumb []byte, body int) []byte {
	// EXIF payload: TIFF header + one IFD + the thumb blob.
	const (
		tiffHdr  = 8
		ifdOff   = int64(tiffHdr)
		ifdSize  = 2 + 2*12 + 4
		thumbOff = ifdOff + ifdSize
	)
	var tiff bytes.Buffer
	le := binary.LittleEndian
	tiff.WriteString("II")
	binary.Write(&tiff, le, uint16(42))
	binary.Write(&tiff, le, uint32(ifdOff))
	entry := func(tag uint16, val int64) {
		binary.Write(&tiff, le, tag)
		binary.Write(&tiff, le, uint16(4))
		binary.Write(&tiff, le, uint32(1))
		binary.Write(&tiff, le, uint32(val))
	}
	binary.Write(&tiff, le, uint16(2))
	entry(0x0201, thumbOff)
	entry(0x0202, int64(len(thumb)))
	binary.Write(&tiff, le, uint32(0))
	tiff.Write(thumb)

	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8}) // SOI
	payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	b.Write([]byte{0xFF, 0xE1}) // APP1
	binary.Write(&b, binary.BigEndian, uint16(len(payload)+2))
	b.Write(payload)
	b.Write([]byte{0xFF, 0xDA, 0x00, 0x02}) // SOS: image data "begins"
	for i := 0; i < body; i++ {
		b.WriteByte(0x55)
	}
	b.Write([]byte{0xFF, 0xD9})
	return b.Bytes()
}

/* ---------- synthetic RAW ----------

The fixtures above are containers with JPEGs inside them, which is all the
culling path ever reads. Developing needs the other thing a camera file
holds: the sensor's own mosaic. DemoRAW writes one, so the demo shoot and
the end-to-end tests exercise the real decode-demosaic-develop path rather
than quietly falling back to the preview. */

// demoScene is a landscape with a hot sun and a dark foreground, exposed
// about a stop and a half under — a photograph with something wrong with
// it, which is the only kind worth pointing an editor at. idx moves the
// sun and the white balance so a shoot is not twelve copies of one frame.
func demoScene(fx, fy float64, idx int) (float64, float64, float64) {
	const horizon = 0.46
	var r, g, b float64
	if fy < horizon {
		t := fy / horizon
		base := 0.28 + 0.55*t*t
		r, g, b = base*0.5, base*0.68, base
		sx := 0.22 + 0.06*float64(idx%5)
		d := math.Hypot(fx-sx, (fy-0.15)*1.7)
		switch {
		case d < 0.055:
			r, g, b = 1, 0.97, 0.86 // saturates the sensor
		case d < 0.26:
			glow := (0.26 - d) / 0.2
			glow *= glow * 0.75
			r, g, b = r+glow, g+glow*0.92, b+glow*0.62
		}
	} else {
		t := (fy - horizon) / (1 - horizon)
		base := 0.07 - 0.045*t
		r, g, b = base*1.5, base*1.15, base*0.62
		// A ridge line, and trees with hard edges for sharpening to bite on.
		ridge := horizon + 0.03*math.Sin(fx*13+float64(idx))
		if fy > ridge && fy < ridge+0.06 {
			r, g, b = r*0.35, g*0.35, b*0.35
		}
		if math.Mod(fx*24+float64(idx), 3) < 1 && fy > horizon+0.12 {
			r, g, b = r*0.55, g*0.6, b*0.55
		}
	}
	// A touch of texture, so the frame is not synthetically clean. Kept
	// well below the sampling rate: a pattern near it would alias against
	// the colour filter array and make the fixture a moire test.
	n := math.Sin(fx*37.1+fy*19.7) * 0.008
	// Underexposed, warm, and differently so from frame to frame.
	k := 0.34 + 0.08*float64(idx%4)
	warm := 1 + 0.12*float64(idx%3)
	return clamp01(r*k*warm + n), clamp01(g*k + n), clamp01(b*k/warm + n)
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// sRGBFromXYZ is the standard matrix, scaled the way DNG's ColorMatrix tag
// wants it. Writing it into the fixture makes the develop pipeline's colour
// conversion the identity, so the demo comes out the colour it was drawn.
var sRGBFromXYZ = [9]int32{32406, -15372, -4986, -9689, 18758, 415, 557, -2040, 10570}

// DemoRAW writes a synthetic ARW at path: an uncompressed 16-bit CFA mosaic
// of demoScene, plus the JPEG preview a camera would have embedded beside
// it, rendered from the same scene.
func DemoRAW(path string, w, h, idx int) error {
	w, h = w&^1, h&^1
	const black, white = 512.0, 16300.0

	sensor := make([]byte, w*h*2)
	prev := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl := demoScene((float64(x)+0.5)/float64(w), (float64(y)+0.5)/float64(h), idx)
			// The mosaic keeps only the colour under each filter, RGGB.
			lin := [3]float64{r, g, bl}
			c := 1
			if y&1 == 0 && x&1 == 0 {
				c = 0
			} else if y&1 == 1 && x&1 == 1 {
				c = 2
			}
			v := black + lin[c]*(white-black)
			binary.LittleEndian.PutUint16(sensor[(y*w+x)*2:], uint16(v))
			// The camera's preview is the same scene, tone-mapped the way a
			// camera would: brighter than the RAW renders by default.
			prev.Set(x, y, color.RGBA{
				R: encode8(r * 1.9), G: encode8(g * 1.9), B: encode8(bl * 1.9), A: 255,
			})
		}
	}
	var pj bytes.Buffer
	if err := jpeg.Encode(&pj, prev, &jpeg.Options{Quality: 88}); err != nil {
		return err
	}

	b := tifftest.New()
	mosaic := b.AddBlob(sensor)
	preview := b.AddBlob(pj.Bytes())
	root := b.AddIFD()
	sub := b.AddIFD()
	sub.BlobOffset(0x0201, preview).Long(0x0202, int64(pj.Len()))

	cm := make([][2]int32, 9)
	for i, v := range sRGBFromXYZ {
		cm[i] = [2]int32{v, 10000}
	}
	root.ASCII(0x010F, "SONY").
		ASCII(0x0110, "QK-DEMO").
		Short(0x0106, 32803). // CFA
		Short(0x0100, int64(w)).
		Short(0x0101, int64(h)).
		Short(0x0102, 16).
		Short(0x0103, 1). // uncompressed
		Short(0x0115, 1).
		Short(0x0112, 1).
		Byte(0x828E, 0, 1, 1, 2). // RGGB
		BlobOffset(0x0111, mosaic).
		Long(0x0117, int64(len(sensor))).
		Short(0x7310, int64(black), int64(black), int64(black), int64(black)).
		Short(0x787F, int64(white)).
		SShort(0x7313, 1024, 1024, 1024, 1024). // neutral: the scene is the truth
		SRational(0xC621, cm...).
		SubIFD(sub)
	return os.WriteFile(path, b.Bytes(), 0o644)
}

func encode8(v float64) uint8 {
	v = clamp01(v)
	if v <= 0.0031308 {
		v *= 12.92
	} else {
		v = 1.055*math.Pow(v, 1/2.4) - 0.055
	}
	return uint8(v*255 + 0.5)
}
