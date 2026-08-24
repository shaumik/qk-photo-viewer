package develop

import (
	"bytes"
	"image"
	"image/draw"
	"image/jpeg"
	"math"

	"github.com/shaumik/qk-photo-viewer/internal/raw"
)

// Learning the white balance from the camera's own rendering.
//
// Sensors see roughly twice as much green as red or blue, so sensor data
// with no white balance applied is not slightly off — it is violently
// green. Most cameras publish the balance they chose in a tag; some, the
// a6000 among them, bury it in an obfuscated maker-note block.
//
// But every RAW carries a JPEG the camera rendered itself, with the right
// balance already in it. That picture is the answer, written down in the
// file, in a form anyone can read. So rather than reverse-engineering
// where the numbers are hidden, this measures them: find the multipliers
// that make our rendering of the sensor data agree with the camera's
// rendering of the same scene.
//
// The comparison is on chromaticity — colour with brightness divided out.
// The camera's JPEG has a tone curve on it and ours does not, and a tone
// curve moves brightness far more than it moves hue. Comparing channel
// averages instead gives an answer that is wrong by the amount the curve
// lifted the darker channels, which on a real frame is a lot.

const (
	wbGridW   = 64   // the comparison runs on a thumbnail; colour is low-frequency
	wbCoarse  = 0.25 // first pass step
	wbFine    = 0.02
	wbLo      = 0.8 // multipliers outside this are not white balance
	wbHi      = 6.0
	wbMinCell = 64 // too few cells to compare means no answer
)

// FitWhiteBalance finds the multipliers that best reconcile the sensor
// data with the camera's own rendering of the same frame, and returns how
// closely they agree. A larger error means a worse match; callers compare
// it against the error of whatever they would have used otherwise, so a
// bad fit can never make things worse than not fitting at all.
func FitWhiteBalance(im *raw.Image, cameraJPEG []byte) (wb [3]float64, fitErr float64, ok bool) {
	ref, cam, full, rw, rh := grids(im, cameraJPEG)
	if ref == nil {
		return im.WB, math.Inf(1), false
	}

	// The matrix is linear, so each channel's contribution can be worked
	// out once and then scaled per candidate rather than re-multiplied.
	n := rw * rh
	var basis [3][]float64
	for c := 0; c < 3; c++ {
		basis[c] = make([]float64, n*3)
	}
	for i := 0; i < n; i++ {
		for c := 0; c < 3; c++ {
			for r := 0; r < 3; r++ {
				basis[c][i*3+r] = im.CamToSRGB[r*3+c] * cam[i*3+c]
			}
		}
	}
	refChroma := make([]float64, n*2)
	used := make([]bool, n)
	count := 0
	for i := 0; i < n; i++ {
		if !full[i] {
			continue
		}
		s := ref[i*3] + ref[i*3+1] + ref[i*3+2]
		// Ignore cells with nothing in them: the chromaticity of near-black
		// is noise, and of clipped white is a constant that fits anything.
		if s < 0.01 || ref[i*3] > 0.97 || ref[i*3+1] > 0.97 || ref[i*3+2] > 0.97 {
			continue
		}
		refChroma[i*2] = ref[i*3] / s
		refChroma[i*2+1] = ref[i*3+2] / s
		used[i] = true
		count++
	}
	if count < wbMinCell {
		return im.WB, math.Inf(1), false
	}

	score := func(kr, kb float64) float64 {
		total := 0.0
		for i := 0; i < n; i++ {
			if !used[i] {
				continue
			}
			r := kr*basis[0][i*3] + basis[1][i*3] + kb*basis[2][i*3]
			g := kr*basis[0][i*3+1] + basis[1][i*3+1] + kb*basis[2][i*3+1]
			b := kr*basis[0][i*3+2] + basis[1][i*3+2] + kb*basis[2][i*3+2]
			s := r + g + b
			if s < 1e-9 {
				continue
			}
			total += math.Hypot(r/s-refChroma[i*2], b/s-refChroma[i*2+1])
		}
		return total / float64(count)
	}

	bestR, bestB, best := im.WB[0], im.WB[2], math.Inf(1)
	for kr := wbLo; kr <= wbHi; kr += wbCoarse {
		for kb := wbLo; kb <= wbHi; kb += wbCoarse {
			if e := score(kr, kb); e < best {
				best, bestR, bestB = e, kr, kb
			}
		}
	}
	for kr := bestR - wbCoarse; kr <= bestR+wbCoarse; kr += wbFine {
		for kb := bestB - wbCoarse; kb <= bestB+wbCoarse; kb += wbFine {
			if kr < wbLo || kb < wbLo {
				continue
			}
			if e := score(kr, kb); e < best {
				best, bestR, bestB = e, kr, kb
			}
		}
	}
	return [3]float64{bestR, 1, bestB}, best, true
}

// ScoreWhiteBalance rates one set of multipliers the same way the fit
// does, so a caller can keep whichever of two answers actually matches the
// camera and never end up worse off for having tried.
func ScoreWhiteBalance(im *raw.Image, cameraJPEG []byte, wb [3]float64) float64 {
	ref, cam, full, rw, rh := grids(im, cameraJPEG)
	if ref == nil {
		return math.Inf(1)
	}
	total, count := 0.0, 0
	for i := 0; i < rw*rh; i++ {
		if !full[i] {
			continue
		}
		s := ref[i*3] + ref[i*3+1] + ref[i*3+2]
		if s < 0.01 || ref[i*3] > 0.97 || ref[i*3+1] > 0.97 || ref[i*3+2] > 0.97 {
			continue
		}
		var out [3]float64
		for r := 0; r < 3; r++ {
			for c := 0; c < 3; c++ {
				out[r] += im.CamToSRGB[r*3+c] * cam[i*3+c] * wb[c]
			}
		}
		os := out[0] + out[1] + out[2]
		if os < 1e-9 {
			continue
		}
		total += math.Hypot(out[0]/os-ref[i*3]/s, out[2]/os-ref[i*3+2]/s)
		count++
	}
	if count < wbMinCell {
		return math.Inf(1)
	}
	return total / float64(count)
}

// grids reduces both pictures to the same small grid: the camera's
// rendering with its transfer curve undone, the sensor's own colours, and
// a flag per cell saying whether it actually saw all three.
func grids(im *raw.Image, cameraJPEG []byte) (ref, cam []float64, full []bool, gw, gh int) {
	src, err := jpeg.Decode(bytes.NewReader(cameraJPEG))
	if err != nil {
		return nil, nil, nil, 0, 0
	}
	b := src.Bounds()
	if b.Dx() < 8 || b.Dy() < 8 || im.Width < 16 || im.Height < 16 {
		return nil, nil, nil, 0, 0
	}
	// A cell finer than the filter block would miss whole colours, so the
	// grid never gets finer than the sensor can fill.
	gw = wbGridW
	if lim := im.Width / 8; gw > lim {
		gw = lim
	}
	if gw > b.Dx() {
		gw = b.Dx()
	}
	gh = b.Dy() * gw / b.Dx()
	if gh < 4 || gw < 4 {
		return nil, nil, nil, 0, 0
	}
	ref = decodeToGrid(src, gw, gh)
	cam, full = binMosaic(im, float64(gw)/float64(gh), gw, gh)
	if cam == nil {
		return nil, nil, nil, 0, 0
	}
	return ref, cam, full, gw, gh
}

// decodeToGrid undoes the camera JPEG's transfer curve and bins it down.
func decodeToGrid(src image.Image, gw, gh int) []float64 {
	b := src.Bounds()
	rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(rgba, rgba.Bounds(), src, b.Min, draw.Src)
	out := make([]float64, gw*gh*3)
	cnt := make([]int, gw*gh)
	var lut [256]float64
	for i := range lut {
		lut[i] = srgbDecode(float64(i) / 255)
	}
	for y := 0; y < b.Dy(); y++ {
		gy := y * gh / b.Dy()
		for x := 0; x < b.Dx(); x++ {
			g := gy*gw + x*gw/b.Dx()
			o := (y*b.Dx() + x) * 4
			out[g*3] += lut[rgba.Pix[o]]
			out[g*3+1] += lut[rgba.Pix[o+1]]
			out[g*3+2] += lut[rgba.Pix[o+2]]
			cnt[g]++
		}
	}
	for i := range cnt {
		if cnt[i] > 0 {
			for c := 0; c < 3; c++ {
				out[i*3+c] /= float64(cnt[i])
			}
		}
	}
	return out
}

// binMosaic averages the sensor's own colours into the same grid. The
// camera's rendering may be a narrower aspect than the sensor — a body set
// to shoot 16:9 crops its JPEG and leaves the RAW whole — so the region
// compared is the centre slice matching the reference's shape.
func binMosaic(im *raw.Image, refAspect float64, gw, gh int) ([]float64, []bool) {
	if im.Width < 8 || im.Height < 8 || refAspect <= 0 {
		return nil, nil
	}
	x0, y0 := 0, 0
	w, h := im.Width, im.Height
	if a := float64(w) / float64(h); a < refAspect {
		h = int(float64(w) / refAspect)
		y0 = (im.Height - h) &^ 1 / 2 * 2 // keep the filter block's phase
	} else if a > refAspect {
		w = int(float64(h) * refAspect)
		x0 = (im.Width - w) &^ 1 / 2 * 2
	}
	if w < 8 || h < 8 {
		return nil, nil
	}
	out := make([]float64, gw*gh*3)
	cnt := make([]int, gw*gh*3)
	for y := 0; y < h; y++ {
		gy := y * gh / h
		sy := y0 + y
		row := sy * im.Width
		for x := 0; x < w; x++ {
			sx := x0 + x
			c := im.At(sx, sy)
			blk := im.BlackAt(sx, sy)
			v := (float64(im.Data[row+sx]) - blk) / (im.White - blk)
			if v < 0 {
				v = 0
			}
			g := gy*gw + x*gw/w
			out[g*3+int(c)] += v
			cnt[g*3+int(c)]++
		}
	}
	full := make([]bool, gw*gh)
	for i := range full {
		full[i] = cnt[i*3] > 0 && cnt[i*3+1] > 0 && cnt[i*3+2] > 0
	}
	for i := range cnt {
		if cnt[i] > 0 {
			out[i] /= float64(cnt[i])
		}
	}
	return out, full
}
