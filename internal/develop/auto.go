package develop

import (
	"math"
	"sort"
)

// Auto is the answer to "I don't know what any of these sliders do".
//
// It looks at the frame the way a person would if they were quick about
// it: is it too dark, is the colour off, is the sky blown, is it flat? —
// and then moves the sliders it would have taken you a minute to find. The
// result is an ordinary Edit, so every decision it made is visible in the
// panel and can be argued with.
//
// It is deliberately conservative. A photo nudged in the right direction
// beats one wrenched into a look, and every correction here is damped so
// that a sunset stays warm and a night shot stays dark.
func Auto(s *Scene) Edit {
	small, n := sample(s)
	if n == 0 {
		return Edit{}
	}
	e := Edit{}

	// --- Colour ---------------------------------------------------------
	// Averaging the frame under a p-norm sits between "the whole scene
	// averages to grey" and "the brightest thing is white". Neither is true
	// on its own; together they are right often enough, and the result is
	// damped so a genuinely warm scene keeps its warmth.
	mr, mg, mb := colorMeans(small, n)
	if mr > 1e-6 && mg > 1e-6 && mb > 1e-6 {
		const damp = 0.75
		// Multipliers that would neutralise the cast outright, pulled back
		// towards 1 so the correction is a nudge and not a verdict.
		wr := math.Pow(mg/mr, damp)
		wb := math.Pow(mg/mb, damp)
		e.Temp = clamp(100*math.Log2(wr/wb)/(2*tempStops), -60, 60)
		// Green/magenta is the axis people notice least and cameras get
		// most nearly right, so it moves at half the confidence.
		tint := 100 * math.Log2(wr*wb) / 2 / tintStops
		e.Tint = clamp(tint*0.5, -40, 40)
	}

	// --- Exposure -------------------------------------------------------
	// Two answers, and the darker one wins: put the midtone where a midtone
	// belongs, but never so bright that the highlights have nowhere to go.
	kr, kg, kb := whiteBalance(e)
	lum := luminances(small, n, kr, kg, kb)
	sort.Float64s(lum)
	mid := pct(lum, 0.5)
	high := pct(lum, 0.995)
	const midTarget = 0.176 // linear value that lands near the middle of the display range
	const highCeiling = 2.0 // a stop of overshoot, which the rolloff absorbs
	ev := 0.0
	if mid > 1e-6 {
		ev = math.Log2(midTarget / mid)
	}
	if high > 1e-6 {
		ev = math.Min(ev, math.Log2(highCeiling/high))
	}
	e.Exposure = clamp(ev, -3, 3)

	// --- Tone -----------------------------------------------------------
	// Everything below is measured after exposure and white balance, since
	// those change the answers.
	gain := math.Exp2(e.Exposure)
	for i := range lum {
		lum[i] *= gain
	}
	p001 := pct(lum, 0.002)
	p10 := pct(lum, 0.10)
	p95 := pct(lum, 0.95)
	p999 := pct(lum, 0.999)

	// Highlights: how far past white the brightest real detail sits. Only
	// a RAW frame has anything up there to bring back.
	if p999 > 1.05 {
		over := math.Log2(p999)
		e.Highlights = -clamp(over*55, 0, 100)
	}
	// Shadows: lift only if the dark end is genuinely closed up.
	if d := srgbEncode(p10); d < 0.12 {
		e.Shadows = clamp((0.12-d)*550, 0, 70)
	}
	// Blacks: a flat frame has nothing at true black — haze, or a lifted
	// profile. Pulling the floor down is most of what makes it "pop".
	if p001 > 0.002 {
		e.Blacks = -clamp(p001/blackDepth*100, 0, 60)
	}

	// Contrast: judged on how much of the display range the frame occupies
	// once the ends have been set.
	spread := srgbEncode(math.Min(p95, 1)) - srgbEncode(math.Max(p10-p001, 0))
	if spread < 0.78 {
		e.Contrast = clamp((0.78-spread)*140, 0, 40)
	}

	// --- Colour intensity and detail ------------------------------------
	if sat := meanSaturation(small, n); sat < 0.34 {
		e.Vibrance = clamp((0.34-sat)*150, 0, 40)
	}
	e.Clarity = 12
	if s.FromRAW {
		// Demosaicing always costs a little acuity; a camera JPEG has
		// already been sharpened and does not want a second pass.
		e.Sharpen = 28
	}
	return e.Clamp()
}

// sampleMaxDim bounds the thumbnail the analysis runs on. Every statistic
// below is a percentile or a mean, and neither needs pixels.
const sampleMaxDim = 192

// sample returns a small copy of the scene and its pixel count.
func sample(s *Scene) ([]float32, int) {
	pix, w, h := s.Pix, s.W, s.H
	for w > sampleMaxDim || h > sampleMaxDim {
		pix, w, h = halve(pix, w, h)
	}
	if w <= 0 || h <= 0 {
		return nil, 0
	}
	return pix, w * h
}

// colorMeans returns each channel's Minkowski mean with p = 6, ignoring
// pixels at or past clipping, which carry no colour information.
func colorMeans(pix []float32, n int) (float64, float64, float64) {
	const p = 6.0
	var sr, sg, sb float64
	cnt := 0
	for i := 0; i < n; i++ {
		r, g, b := float64(pix[i*3]), float64(pix[i*3+1]), float64(pix[i*3+2])
		if r >= 1 || g >= 1 || b >= 1 || r < 0 || g < 0 || b < 0 {
			continue
		}
		sr += math.Pow(r, p)
		sg += math.Pow(g, p)
		sb += math.Pow(b, p)
		cnt++
	}
	if cnt == 0 {
		return 0, 0, 0
	}
	f := func(s float64) float64 { return math.Pow(s/float64(cnt), 1/p) }
	return f(sr), f(sg), f(sb)
}

// luminances returns the frame's linear luminances under a set of white
// balance multipliers.
func luminances(pix []float32, n int, kr, kg, kb float32) []float64 {
	out := make([]float64, n)
	for i := 0; i < n; i++ {
		out[i] = float64(0.2126*pix[i*3]*kr + 0.7152*pix[i*3+1]*kg + 0.0722*pix[i*3+2]*kb)
	}
	return out
}

func meanSaturation(pix []float32, n int) float64 {
	sum, cnt := 0.0, 0
	for i := 0; i < n; i++ {
		r, g, b := pix[i*3], pix[i*3+1], pix[i*3+2]
		maxc, minc := r, r
		if g > maxc {
			maxc = g
		}
		if b > maxc {
			maxc = b
		}
		if g < minc {
			minc = g
		}
		if b < minc {
			minc = b
		}
		if maxc <= 1e-4 {
			continue
		}
		sum += float64((maxc - minc) / maxc)
		cnt++
	}
	if cnt == 0 {
		return 0
	}
	return sum / float64(cnt)
}

// pct reads a percentile out of an ascending slice.
func pct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	i := int(p * float64(len(sorted)-1))
	return sorted[clampi(i, 0, len(sorted)-1)]
}

func clamp(v, lo, hi float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return math.Max(lo, math.Min(hi, v))
}
