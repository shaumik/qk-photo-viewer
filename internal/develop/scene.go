// Package develop turns sensor data into a picture.
//
// The unit of work is a Scene: the frame in scene-referred linear sRGB,
// where 1.0 is the sensor's saturation point and values above it are the
// headroom a RAW file keeps and a JPEG has already thrown away. Everything
// the editor does happens to a Scene, and only the last step — Render —
// commits to display values a screen can show.
//
// Building a Scene is the expensive half (decode, demosaic, colour) and
// depends only on the file; rendering one is the cheap half and depends on
// the edit. Splitting them there is what lets a slider stay live: the
// Scene is built once and cached, and dragging re-renders it.
package develop

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"math"

	"github.com/shaumik/qk-photo-viewer/internal/raw"
)

// Scene is a frame in scene-referred linear sRGB, already rotated to the
// orientation it should be viewed at.
type Scene struct {
	W, H int
	Pix  []float32 // 3 floats per pixel, row-major, linear sRGB

	// FromRAW is false when we fell back to the camera's rendered preview,
	// which means no highlight headroom and limited white-balance range.
	FromRAW bool
	// ApproxColor is true when the colour matrix was a generic stand-in
	// rather than this camera body's own.
	ApproxColor bool
	Camera      string
	// Headroom is how far above white the brightest sample sits, in stops.
	Headroom float64
}

// PreviewMaxDim bounds a Scene built for the screen. Big enough for a
// laptop display, small enough that a slider drag re-renders in one frame
// and a handful of Scenes fit in memory at once.
const PreviewMaxDim = 2048

// FromRAWImage builds a Scene from decoded sensor data. When maxDim is
// positive the frame is demosaiced by averaging each 2x2 filter block —
// half resolution, but free of the interpolation artefacts a full demosaic
// has to work to avoid, and four times less work. maxDim of 0 asks for the
// full-resolution gradient-corrected demosaic used on export.
func FromRAWImage(im *raw.Image, maxDim int) *Scene {
	var pix []float32
	var w, h int
	if maxDim > 0 {
		pix, w, h = binHalf(im)
		for w > maxDim || h > maxDim {
			pix, w, h = halve(pix, w, h)
		}
	} else {
		pix, w, h = demosaic(im)
	}
	toSRGB(pix, im.CamToSRGB)
	pix, w, h = orient(pix, w, h, im.Orientation)

	s := &Scene{W: w, H: h, Pix: pix, FromRAW: true,
		ApproxColor: im.Approximate, Camera: cameraName(im)}
	s.Headroom = headroom(pix)
	return s
}

// FromJPEGBytes builds a Scene from a rendered JPEG — the camera's
// embedded preview, or a JPEG-only shot. The picture is already
// display-referred, so this undoes the sRGB transfer curve to get back to
// something linear. What it cannot undo is the clipping: highlights the
// camera burned out are gone, and Headroom is zero to say so.
func FromJPEGBytes(data []byte, orientation int, maxDim int) (*Scene, error) {
	src, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("develop: decode preview: %w", err)
	}
	b := src.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), src, b.Min, draw.Src)

	w, h := b.Dx(), b.Dy()
	pix := make([]float32, w*h*3)
	var lut [256]float32
	for i := range lut {
		lut[i] = float32(srgbDecode(float64(i) / 255))
	}
	for i, n := 0, w*h; i < n; i++ {
		pix[i*3] = lut[rgba.Pix[i*4]]
		pix[i*3+1] = lut[rgba.Pix[i*4+1]]
		pix[i*3+2] = lut[rgba.Pix[i*4+2]]
	}
	for maxDim > 0 && (w > maxDim || h > maxDim) {
		pix, w, h = halve(pix, w, h)
	}
	pix, w, h = orient(pix, w, h, orientation)
	return &Scene{W: w, H: h, Pix: pix}, nil
}

func cameraName(im *raw.Image) string {
	switch {
	case im.Make != "" && im.Model != "":
		return im.Make + " " + im.Model
	case im.Model != "":
		return im.Model
	default:
		return im.Make
	}
}

// headroom reports how far the brightest 0.01% of the frame sits above
// white, in stops — the recovery budget an exposure pull has to work with.
func headroom(pix []float32) float64 {
	if len(pix) == 0 {
		return 0
	}
	// A coarse histogram over the log range above white is enough; we only
	// ever report this to a stop or so.
	const buckets = 64
	var hist [buckets]int
	total := 0
	for _, v := range pix {
		if v <= 1 {
			continue
		}
		total++
		b := int(math.Log2(float64(v)) / 5 * buckets)
		if b >= buckets {
			b = buckets - 1
		}
		if b < 0 {
			b = 0
		}
		hist[b]++
	}
	if total == 0 {
		return 0
	}
	cut := total / 10000
	seen := 0
	for b := buckets - 1; b >= 0; b-- {
		seen += hist[b]
		if seen > cut {
			return float64(b+1) / buckets * 5
		}
	}
	return 0
}

/* ---------- demosaic ---------- */

// binHalf averages each 2x2 filter block into one RGB pixel: the red
// sample is red, the blue sample is blue, and the two greens average. No
// interpolation means no interpolation artefacts, which is why every fast
// RAW viewer draws the screen this way.
func binHalf(im *raw.Image) ([]float32, int, int) {
	w, h := im.Width/2, im.Height/2
	out := make([]float32, w*h*3)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sum [3]float64
			var n [3]int
			for dy := 0; dy < 2; dy++ {
				for dx := 0; dx < 2; dx++ {
					sx, sy := x*2+dx, y*2+dy
					c := im.At(sx, sy)
					v := (float64(im.Data[sy*im.Width+sx]) - im.BlackAt(sx, sy)) /
						(im.White - im.BlackAt(sx, sy))
					sum[c] += v
					n[c]++
				}
			}
			o := (y*w + x) * 3
			for c := 0; c < 3; c++ {
				v := 0.0
				if n[c] > 0 {
					v = sum[c] / float64(n[c])
				}
				out[o+c] = float32(v * im.WB[c])
			}
		}
	}
	return out, w, h
}

// halve box-downsamples an RGB float image by two.
func halve(pix []float32, w, h int) ([]float32, int, int) {
	nw, nh := w/2, h/2
	if nw < 1 || nh < 1 {
		return pix, w, h
	}
	out := make([]float32, nw*nh*3)
	for y := 0; y < nh; y++ {
		for x := 0; x < nw; x++ {
			o := (y*nw + x) * 3
			a := ((y*2)*w + x*2) * 3
			b := ((y*2)*w + x*2 + 1) * 3
			c := ((y*2+1)*w + x*2) * 3
			d := ((y*2+1)*w + x*2 + 1) * 3
			for k := 0; k < 3; k++ {
				out[o+k] = (pix[a+k] + pix[b+k] + pix[c+k] + pix[d+k]) * 0.25
			}
		}
	}
	return out, nw, nh
}

// toSRGB applies the camera-to-sRGB colour matrix in place.
func toSRGB(pix []float32, m [9]float64) {
	m0, m1, m2 := float32(m[0]), float32(m[1]), float32(m[2])
	m3, m4, m5 := float32(m[3]), float32(m[4]), float32(m[5])
	m6, m7, m8 := float32(m[6]), float32(m[7]), float32(m[8])
	for i := 0; i < len(pix); i += 3 {
		r, g, b := pix[i], pix[i+1], pix[i+2]
		pix[i] = m0*r + m1*g + m2*b
		pix[i+1] = m3*r + m4*g + m5*b
		pix[i+2] = m6*r + m7*g + m8*b
	}
}

// orient rotates and flips a frame into the orientation the photographer
// held the camera in, so everything downstream — and the exported file —
// is the right way up without relying on a metadata tag.
func orient(pix []float32, w, h, o int) ([]float32, int, int) {
	if o <= 1 || o > 8 {
		return pix, w, h
	}
	swap := o >= 5
	nw, nh := w, h
	if swap {
		nw, nh = h, w
	}
	out := make([]float32, len(pix))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			s := (y*w + x) * 3
			d := (dy*nw + dx) * 3
			out[d], out[d+1], out[d+2] = pix[s], pix[s+1], pix[s+2]
		}
	}
	return out, nw, nh
}

func srgbDecode(x float64) float64 {
	if x <= 0.04045 {
		return x / 12.92
	}
	return math.Pow((x+0.055)/1.055, 2.4)
}

func srgbEncode(x float64) float64 {
	if x <= 0.0031308 {
		return 12.92 * x
	}
	return 1.055*math.Pow(x, 1/2.4) - 0.055
}
