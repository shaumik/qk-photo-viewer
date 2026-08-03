package develop

import (
	"math"
	"runtime"
	"sync"
)

// Geometry: where the picture's edges are, and where the lens put things
// that should have been straight.
//
// Four separate-sounding operations happen here — lens distortion
// correction, vignetting, straightening, and crop — and they are one
// operation. Each is a map from an output pixel back to somewhere in the
// source; composing the maps and sampling once costs a single resample,
// while doing them in sequence would cost four and soften the picture
// every time.
//
// The lens part matters more than it sounds on a kit zoom. Sony's camera
// applies distortion and vignetting correction when it writes a JPEG and
// leaves the RAW alone, expecting whatever opens it to do the same. Skip
// it and straight lines bow outward and the corners go dark — worse than
// the picture the camera itself would have given you.

// How far the sliders reach at full travel.
const (
	// distortionDepth is the second-order coefficient at slider 100. A kit
	// zoom's worst barrel distortion is around 5%, so this leaves room.
	distortionDepth = 0.15
	// vignetteDepth sets corner gain at slider 100 to 2.5x, about a stop
	// and a third — more than any lens loses.
	vignetteDepth = 1.5
	maxRotate     = 45.0
)

// CropRect returns the edit's crop as a normalised rectangle. An unset
// crop is the whole frame, which is what makes the zero Edit mean "as
// shot" even with crop fields present.
func (e Edit) CropRect() (x, y, w, h float64) {
	if e.CropW <= 0 || e.CropH <= 0 {
		return 0, 0, 1, 1
	}
	x = math.Max(0, math.Min(1, e.CropX))
	y = math.Max(0, math.Min(1, e.CropY))
	w = math.Max(0.02, math.Min(1-x, e.CropW))
	h = math.Max(0.02, math.Min(1-y, e.CropH))
	return
}

// HasGeometry reports whether the edit moves any pixel. When it does not,
// the whole resample is skipped rather than run as an expensive identity.
func (e Edit) HasGeometry() bool {
	x, y, w, h := e.CropRect()
	return e.Distortion != 0 || e.Vignette != 0 || e.Rotate != 0 ||
		x != 0 || y != 0 || w != 1 || h != 1
}

// warp is the composed map from output pixel to source pixel, built once
// per render and then evaluated a few million times.
type warp struct {
	srcW, srcH int
	outW, outH int
	cx, cy     float64 // crop origin, normalised
	cw, ch     float64 // crop size, normalised
	fit        float64 // zoom that keeps rotation from exposing empty corners
	sin, cos   float64
	k1         float64 // radial distortion coefficient
	vig        float64 // vignetting correction strength
	rNorm      float64 // radius of the frame corner, in pixels
	halfW      float64
	halfH      float64
}

func newWarp(s *Scene, e Edit) *warp {
	x, y, w, h := e.CropRect()
	rad := e.Rotate * math.Pi / 180
	wp := &warp{
		srcW: s.W, srcH: s.H,
		cx: x, cy: y, cw: w, ch: h,
		sin: math.Sin(rad), cos: math.Cos(rad),
		// Positive slider corrects barrel, which means sampling nearer the
		// centre as radius grows — a negative coefficient.
		k1:    -distortionDepth * e.Distortion / 100,
		vig:   e.Vignette / 100,
		halfW: float64(s.W) / 2, halfH: float64(s.H) / 2,
	}
	wp.rNorm = math.Hypot(wp.halfW, wp.halfH)
	wp.fit = fitScale(wp)
	// The fit zoom samples a smaller region; sizing the output by it too
	// means each output pixel still comes from about one source pixel.
	// Otherwise straightening would quietly upscale, and pay for a level
	// horizon in sharpness rather than in the pixels it actually costs.
	wp.outW = maxInt(1, int(math.Round(float64(s.W)*w*wp.fit)))
	wp.outH = maxInt(1, int(math.Round(float64(s.H)*h*wp.fit)))
	return wp
}

// source maps a point in the full corrected frame — normalised, with the
// crop not yet applied — back to source pixel coordinates, and reports the
// radius it came from so vignetting can use it.
//
// Pixel i's centre sits at coordinate i, so the frame spans normalised 0
// to 1 across coordinates -0.5 to W-0.5. Getting that half pixel wrong
// would shift and soften every frame that went through here, invisibly.
func (wp *warp) source(fx, fy float64) (px, py, r float64) {
	dx := (fx - 0.5) * float64(wp.srcW) * wp.fit
	dy := (fy - 0.5) * float64(wp.srcH) * wp.fit
	rx := dx*wp.cos + dy*wp.sin
	ry := -dx*wp.sin + dy*wp.cos
	r = math.Hypot(rx, ry) / wp.rNorm
	if wp.k1 != 0 {
		f := 1 + wp.k1*r*r
		rx, ry = rx*f, ry*f
	}
	return rx + wp.halfW - 0.5, ry + wp.halfH - 0.5, r
}

// fitScale finds the largest zoom no greater than 1 that keeps the whole
// corrected frame inside the source. Straightening a photo otherwise
// swings empty corners into view; this is the automatic tightening every
// editor does, worked out by asking rather than by trigonometry, so it
// covers distortion correction in the same breath.
//
// It probes the frame's outermost pixel centres — the same points the
// resample will actually ask for — rather than the frame's outer edge,
// which is half a pixel further out and would zoom for no reason.
func fitScale(wp *warp) float64 {
	const steps = 64
	insetX := 0.5 / float64(wp.srcW)
	insetY := 0.5 / float64(wp.srcH)
	inside := func(s float64) bool {
		saved := wp.fit
		wp.fit = s
		defer func() { wp.fit = saved }()
		for i := 0; i <= steps; i++ {
			t := float64(i) / steps
			x := insetX + t*(1-2*insetX)
			y := insetY + t*(1-2*insetY)
			for _, p := range [4][2]float64{
				{x, insetY}, {x, 1 - insetY}, {insetX, y}, {1 - insetX, y},
			} {
				px, py, _ := wp.source(p[0], p[1])
				if px < 0 || py < 0 || px > float64(wp.srcW-1) || py > float64(wp.srcH-1) {
					return false
				}
			}
		}
		return true
	}
	if inside(1) {
		return 1
	}
	lo, hi := 0.05, 1.0
	for i := 0; i < 28; i++ {
		mid := (lo + hi) / 2
		if inside(mid) {
			lo = mid
		} else {
			hi = mid
		}
	}
	return lo
}

// applyGeometry returns the frame with the lens corrected, straightened
// and cropped. The Scene it returns is a new one; the original is left
// alone because it is the cached, edit-independent version.
func applyGeometry(s *Scene, e Edit) *Scene {
	if !e.HasGeometry() || s.W < 2 || s.H < 2 {
		return s
	}
	wp := newWarp(s, e)
	out := &Scene{
		W: wp.outW, H: wp.outH, Pix: make([]float32, wp.outW*wp.outH*3),
		FromRAW: s.FromRAW, ApproxColor: s.ApproxColor,
		Camera: s.Camera, Headroom: s.Headroom,
	}

	rows := func(y0, y1 int) {
		for oy := y0; oy < y1; oy++ {
			fy := wp.cy + (float64(oy)+0.5)/float64(wp.outH)*wp.ch
			for ox := 0; ox < wp.outW; ox++ {
				fx := wp.cx + (float64(ox)+0.5)/float64(wp.outW)*wp.cw
				px, py, r := wp.source(fx, fy)
				o := (oy*wp.outW + ox) * 3
				sampleCatmullRom(s, px, py, out.Pix[o:o+3])
				if wp.vig != 0 {
					g := float32(vignetteGain(wp.vig, r))
					out.Pix[o] *= g
					out.Pix[o+1] *= g
					out.Pix[o+2] *= g
				}
			}
		}
	}
	parallelRows(wp.outH, rows)
	return out
}

// vignetteGain brightens (or, negative, darkens) with the square of the
// radius the way a lens falls off.
func vignetteGain(v, r float64) float64 {
	g := 1 + v*vignetteDepth*(0.35*r*r+0.65*r*r*r*r)
	if g < 0.05 {
		return 0.05
	}
	return g
}

// parallelRows splits a row range across the cores available. The resample
// is the most expensive pass in an export and every row is independent.
func parallelRows(h int, fn func(y0, y1 int)) {
	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}
	if workers < 2 || h < 64 {
		fn(0, h)
		return
	}
	var wg sync.WaitGroup
	band := (h + workers - 1) / workers
	for y := 0; y < h; y += band {
		y0, y1 := y, minInt(y+band, h)
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn(y0, y1)
		}()
	}
	wg.Wait()
}

// sampleCatmullRom reads a pixel at fractional coordinates with a cubic
// filter. Bilinear would be cheaper and visibly softer, and softening the
// picture is the one thing a geometric correction has no business doing.
func sampleCatmullRom(s *Scene, px, py float64, dst []float32) {
	ix := int(math.Floor(px))
	iy := int(math.Floor(py))
	fx := px - float64(ix)
	fy := py - float64(iy)
	var wx, wy [4]float64
	catmullWeights(fx, &wx)
	catmullWeights(fy, &wy)

	var acc [3]float64
	for j := 0; j < 4; j++ {
		sy := clampi(iy-1+j, 0, s.H-1)
		row := sy * s.W
		var line [3]float64
		for i := 0; i < 4; i++ {
			sx := clampi(ix-1+i, 0, s.W-1)
			o := (row + sx) * 3
			line[0] += wx[i] * float64(s.Pix[o])
			line[1] += wx[i] * float64(s.Pix[o+1])
			line[2] += wx[i] * float64(s.Pix[o+2])
		}
		acc[0] += wy[j] * line[0]
		acc[1] += wy[j] * line[1]
		acc[2] += wy[j] * line[2]
	}
	for c := 0; c < 3; c++ {
		// The cubic can undershoot past black on a hard edge; scene-linear
		// values below zero are not a colour.
		if acc[c] < 0 {
			acc[c] = 0
		}
		dst[c] = float32(acc[c])
	}
}

func catmullWeights(t float64, w *[4]float64) {
	t2 := t * t
	t3 := t2 * t
	w[0] = 0.5 * (-t3 + 2*t2 - t)
	w[1] = 0.5 * (3*t3 - 5*t2 + 2)
	w[2] = 0.5 * (-3*t3 + 4*t2 + t)
	w[3] = 0.5 * (t3 - t2)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
