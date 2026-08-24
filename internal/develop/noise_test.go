package develop

import (
	"math"
	"testing"
)

// noisyScene is a flat patch with pseudo-random speckle added, split into
// luminance grain and colour blotches so each half can be measured on its
// own. The generator is deterministic: a flaky noise test is worthless.
func noisyScene(w, h int, lumaAmp, chromaAmp float32) *Scene {
	s := &Scene{W: w, H: h, Pix: make([]float32, w*h*3)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			n := float32(math.Sin(float64(x)*12.9898+float64(y)*78.233) * 43758.5453) // hash-ish
			n -= float32(math.Floor(float64(n)))
			n = n*2 - 1
			m := float32(math.Sin(float64(x)*39.3468+float64(y)*11.135) * 4127.71)
			m -= float32(math.Floor(float64(m)))
			m = m*2 - 1
			o := (y*w + x) * 3
			s.Pix[o] = 0.5 + n*lumaAmp + m*chromaAmp
			s.Pix[o+1] = 0.5 + n*lumaAmp
			s.Pix[o+2] = 0.5 + n*lumaAmp - m*chromaAmp
		}
	}
	return s
}

// spread is the mean absolute deviation from a channel's own mean: how
// much speckle is left.
func spread(pix []float32, n, ch int) float64 {
	mean := 0.0
	for i := 0; i < n; i++ {
		mean += float64(pix[i*3+ch])
	}
	mean /= float64(n)
	dev := 0.0
	for i := 0; i < n; i++ {
		dev += math.Abs(float64(pix[i*3+ch]) - mean)
	}
	return dev / float64(n)
}

// chromaSpread measures how far pixels stray from grey, which is what
// colour noise looks like.
func chromaSpread(pix []float32, n int) float64 {
	sum := 0.0
	for i := 0; i < n; i++ {
		r, g, b := pix[i*3], pix[i*3+1], pix[i*3+2]
		l := 0.2126*r + 0.7152*g + 0.0722*b
		sum += math.Abs(float64(r-l)) + math.Abs(float64(b-l))
	}
	return sum / float64(n)
}

func TestDenoiseFlattensSpeckle(t *testing.T) {
	const w, h = 96, 96
	s := noisyScene(w, h, 0.035, 0.05)
	before := append([]float32(nil), s.Pix...)
	denoise(s.Pix, w, h, 1)

	n := w * h
	if got, was := spread(s.Pix, n, 1), spread(before, n, 1); got > was*0.75 {
		t.Errorf("luminance speckle went from %v to %v; want a clear reduction", was, got)
	}
	// Colour noise is the ugly half and can be smoothed far harder,
	// because the eye resolves colour coarsely and the detail is not there.
	got, was := chromaSpread(s.Pix, n), chromaSpread(before, n)
	if got > was*0.35 {
		t.Errorf("colour speckle went from %v to %v; want it mostly gone", was, got)
	}
	if got >= spread(s.Pix, n, 1) && was < spread(before, n, 1)*3 {
		t.Log("note: chroma should end up cleaner than luma")
	}
}

func TestDenoiseKeepsTheAverageBrightness(t *testing.T) {
	// Smoothing must not shift exposure or tint the frame.
	const w, h = 64, 64
	s := noisyScene(w, h, 0.03, 0.04)
	var was [3]float64
	for c := 0; c < 3; c++ {
		for i := 0; i < w*h; i++ {
			was[c] += float64(s.Pix[i*3+c])
		}
	}
	denoise(s.Pix, w, h, 1)
	for c := 0; c < 3; c++ {
		got := 0.0
		for i := 0; i < w*h; i++ {
			got += float64(s.Pix[i*3+c])
		}
		if math.Abs(got-was[c])/float64(w*h) > 0.01 {
			t.Errorf("channel %d mean moved by %v", c, (got-was[c])/float64(w*h))
		}
	}
}

func TestDenoiseKeepsEdges(t *testing.T) {
	// The whole reason for a sigma filter rather than a blur: a hard edge
	// is a large difference, so the pixels across it are simply not
	// averaged in. Detail survives; grain does not.
	const w, h = 64, 64
	s := &Scene{W: w, H: h, Pix: make([]float32, w*h*3)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := float32(0.25)
			if x >= w/2 {
				v = 0.75
			}
			o := (y*w + x) * 3
			s.Pix[o], s.Pix[o+1], s.Pix[o+2] = v, v, v
		}
	}
	denoise(s.Pix, w, h, 1)
	lo := s.Pix[((h/2)*w+w/2-1)*3]
	hi := s.Pix[((h/2)*w+w/2)*3]
	if hi-lo < 0.45 {
		t.Errorf("edge collapsed from 0.50 to %v; the filter is blurring, not denoising", hi-lo)
	}
	// And far from the edge nothing moved at all.
	if v := s.Pix[((h/2)*w+4)*3]; math.Abs(float64(v-0.25)) > 0.005 {
		t.Errorf("a flat area drifted to %v, want 0.25", v)
	}
}

func TestDenoiseIsSkippedWhenOff(t *testing.T) {
	s := noisyScene(64, 64, 0.03, 0.03)
	before := append([]float32(nil), s.Pix...)
	denoise(s.Pix, 64, 64, 0)
	for i := range before {
		if s.Pix[i] != before[i] {
			t.Fatal("zero amount should touch nothing")
		}
	}
	// Tiny frames have no neighbourhood to work with and must not panic.
	for _, n := range []int{1, 2, 4, 7} {
		tiny := noisyScene(n, n, 0.02, 0.02)
		denoise(tiny.Pix, n, n, 1)
	}
}

func TestNoiseSliderReachesTheRender(t *testing.T) {
	s := noisyScene(96, 96, 0.03, 0.05)
	plain := Render(s, Edit{})
	cleaned := Render(noisyScene(96, 96, 0.03, 0.05), Edit{Noise: 100})
	rough, smooth := 0.0, 0.0
	for i := 1; i < 96*96; i++ {
		rough += math.Abs(float64(plain.Pix[i*4]) - float64(plain.Pix[(i-1)*4]))
		smooth += math.Abs(float64(cleaned.Pix[i*4]) - float64(cleaned.Pix[(i-1)*4]))
	}
	if smooth >= rough*0.8 {
		t.Errorf("neighbour-to-neighbour variation went from %v to %v; the slider is not "+
			"reaching the pixels", rough, smooth)
	}
}

func TestAutoTradesSharpeningForSmoothingAsISOClimbs(t *testing.T) {
	// At base ISO there is nothing to smooth and detail worth sharpening.
	// High up it is the other way round, and sharpening grain is actively
	// worse than leaving it alone.
	cases := []struct {
		iso                int
		wantNoise          bool
		wantFullSharpening bool
	}{
		{100, false, true},
		{400, false, true},
		{800, false, true},
		{1600, true, false},
		{6400, true, false},
		{25600, true, false},
	}
	var lastSharpen, lastNoise float64 = 999, -1
	for _, c := range cases {
		s := photoScene(64, 64, 0.1, 0.8, [3]float64{1, 1, 1})
		s.ISO = c.iso
		e := Auto(s)
		if (e.Noise > 0) != c.wantNoise {
			t.Errorf("ISO %d: noise = %v, wantNoise %v", c.iso, e.Noise, c.wantNoise)
		}
		if (e.Sharpen == sharpenAtBase) != c.wantFullSharpening {
			t.Errorf("ISO %d: sharpen = %v, wantFull %v", c.iso, e.Sharpen, c.wantFullSharpening)
		}
		if e.Sharpen > lastSharpen {
			t.Errorf("ISO %d sharpened more than a lower ISO did", c.iso)
		}
		if e.Noise < lastNoise {
			t.Errorf("ISO %d smoothed less than a lower ISO did", c.iso)
		}
		if e.Sharpen < sharpenFloor || e.Noise > noiseCeiling {
			t.Errorf("ISO %d ran past its limits: sharpen %v noise %v", c.iso, e.Sharpen, e.Noise)
		}
		lastSharpen, lastNoise = e.Sharpen, e.Noise
	}
}

func TestAutoLeavesACameraJPEGAlone(t *testing.T) {
	// A camera JPEG arrives sharpened and denoised already. Doing either
	// again makes it worse, whatever the ISO says.
	s := photoScene(64, 64, 0.1, 0.8, [3]float64{1, 1, 1})
	s.FromRAW = false
	s.ISO = 12800
	e := Auto(s)
	if e.Noise != 0 || e.Sharpen != 0 {
		t.Errorf("camera JPEG got noise %v and sharpen %v, want neither", e.Noise, e.Sharpen)
	}
}

func TestUnknownISOIsTreatedAsClean(t *testing.T) {
	// A file that does not record its ISO should not be smoothed on
	// suspicion; guessing wrong here costs detail.
	s := photoScene(64, 64, 0.1, 0.8, [3]float64{1, 1, 1})
	s.ISO = 0
	if e := Auto(s); e.Noise != 0 || e.Sharpen != sharpenAtBase {
		t.Errorf("unknown ISO gave noise %v sharpen %v, want 0 and %v",
			e.Noise, e.Sharpen, sharpenAtBase)
	}
}
