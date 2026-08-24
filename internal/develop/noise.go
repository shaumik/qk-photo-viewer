package develop

// Noise reduction, split the way the eye splits it.
//
// A high-ISO frame carries two different problems that look like one.
// Colour noise is the coloured blotches — magenta and green patches in the
// shadows — and it is by far the uglier of the two. Luminance noise is
// grain, which reads as texture and, in moderation, as film.
//
// They deserve opposite treatment. The eye resolves colour far more
// coarsely than brightness, so chroma can be smoothed hard and lose almost
// nothing: the blotches go and the detail, which lives in the luminance,
// stays exactly where it was. Grain has to be handled delicately, because
// the filter that removes it is the same filter that removes fine detail.
//
// So: blur the colour, and edge-preserve the brightness.

// Where the two halves of the slider reach at full travel.
const (
	chromaRadiusMax = 8    // colour blotches are large; the blur has to be too
	lumaSigmaMax    = 0.09 // in display units: how far a neighbour can differ and still count
	lumaBlend       = 0.85
)

// denoise smooths buf in place. amount runs 0 to 1.
func denoise(buf []float32, w, h int, amount float64) {
	if amount <= 0 || w < 8 || h < 8 {
		return
	}
	n := w * h
	y := make([]float32, n)
	cb := make([]float32, n)
	cr := make([]float32, n)
	for i := 0; i < n; i++ {
		r, g, b := buf[i*3], buf[i*3+1], buf[i*3+2]
		l := 0.2126*r + 0.7152*g + 0.0722*b
		y[i], cb[i], cr[i] = l, b-l, r-l
	}

	denoiseChroma(cb, cr, w, h, amount)
	denoiseLuma(y, w, h, amount)

	for i := 0; i < n; i++ {
		l, b, r := y[i], y[i]+cb[i], y[i]+cr[i]
		// Green is whatever is left once luminance is accounted for.
		g := l - (0.2126*cr[i]+0.0722*cb[i])/0.7152
		buf[i*3], buf[i*3+1], buf[i*3+2] = r, g, b
	}
}

// denoiseChroma blurs the colour difference channels outright. This is the
// cheap half of the deal: it costs no detail, because there is no detail
// in these channels worth keeping.
func denoiseChroma(cb, cr []float32, w, h int, amount float64) {
	radius := int(1 + amount*chromaRadiusMax)
	if radius < 1 {
		return
	}
	tmp := make([]float32, w*h)
	for _, ch := range [][]float32{cb, cr} {
		for pass := 0; pass < 2; pass++ {
			boxH(ch, tmp, w, h, radius)
			boxV(tmp, ch, w, h, radius)
		}
	}
}

// denoiseLuma is a sigma filter: average the neighbours that are close in
// value to the centre and ignore the rest. Noise is small deviations, so
// it averages away; an edge is a large deviation, so the pixels across it
// are simply not counted and the edge survives. One pass, no ringing, and
// nothing to tune but the threshold.
func denoiseLuma(y []float32, w, h int, amount float64) {
	const radius = 2
	sigma := float32(amount * lumaSigmaMax)
	if sigma <= 0 {
		return
	}
	blend := float32(amount * lumaBlend)
	src := append([]float32(nil), y...)

	parallelRows(h, func(y0, y1 int) {
		for py := y0; py < y1; py++ {
			for px := 0; px < w; px++ {
				c := src[py*w+px]
				var sum float32
				count := 0
				for dy := -radius; dy <= radius; dy++ {
					sy := py + dy
					if sy < 0 || sy >= h {
						continue
					}
					row := sy * w
					for dx := -radius; dx <= radius; dx++ {
						sx := px + dx
						if sx < 0 || sx >= w {
							continue
						}
						v := src[row+sx]
						d := v - c
						if d < 0 {
							d = -d
						}
						if d <= sigma {
							sum += v
							count++
						}
					}
				}
				if count > 1 {
					y[py*w+px] = c + (sum/float32(count)-c)*blend
				}
			}
		}
	})
}
