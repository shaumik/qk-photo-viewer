package develop

import "github.com/shaumik/qk-photo-viewer/internal/raw"

// Full-resolution demosaic, used on export.
//
// Every pixel on the sensor measured exactly one colour; the other two have
// to be inferred from its neighbours. Averaging them — plain bilinear —
// blurs detail and puts coloured fringes on every hard edge, because it
// treats the three channels as unrelated when in reality they carry almost
// the same detail.
//
// This is the Malvar-He-Cutler interpolation, which fixes that by
// correcting each channel's bilinear estimate with the local Laplacian of
// the channel that *was* measured there. It costs one 5x5 pass and buys
// roughly a 5 dB improvement over bilinear, which is the difference
// between "acceptable" and "sharp" at 100%.

// What a pixel is, given its position in the 2x2 filter block.
const (
	kindR  = iota // red sample
	kindB         // blue sample
	kindGr        // green sample on a row that also carries red
	kindGb        // green sample on a row that also carries blue
)

func demosaic(im *raw.Image) ([]float32, int, int) {
	w, h := im.Width, im.Height
	m := normalizedMosaic(im)
	out := make([]float32, w*h*3)

	// Classify the four positions of the filter block once, not per pixel.
	var kinds [4]int
	for i := 0; i < 4; i++ {
		x, y := i&1, i>>1
		switch im.CFA[i] {
		case raw.Red:
			kinds[i] = kindR
		case raw.Blue:
			kinds[i] = kindB
		default:
			if im.CFA[(y&1)<<1|((x+1)&1)] == raw.Red {
				kinds[i] = kindGr
			} else {
				kinds[i] = kindGb
			}
		}
	}

	at := func(x, y int) float32 { return m[y*w+x] }
	for y := 2; y < h-2; y++ {
		for x := 2; x < w-2; x++ {
			c := at(x, y)
			// Neighbour shorthand: cardinal at one and two steps, plus the
			// four diagonals — every tap the MHC kernels use.
			n1, s1 := at(x, y-1), at(x, y+1)
			w1, e1 := at(x-1, y), at(x+1, y)
			n2, s2 := at(x, y-2), at(x, y+2)
			w2, e2 := at(x-2, y), at(x+2, y)
			nw, ne := at(x-1, y-1), at(x+1, y-1)
			sw, se := at(x-1, y+1), at(x+1, y+1)

			cross := n1 + s1 + w1 + e1
			far := n2 + s2 + w2 + e2
			diag := nw + ne + sw + se
			o := (y*w + x) * 3

			switch kinds[(y&1)<<1|(x&1)] {
			case kindR:
				out[o] = c
				out[o+1] = (2*cross - far + 4*c) / 8
				out[o+2] = (2*diag - 1.5*far + 6*c) / 8
			case kindB:
				out[o] = (2*diag - 1.5*far + 6*c) / 8
				out[o+1] = (2*cross - far + 4*c) / 8
				out[o+2] = c
			case kindGr:
				// Red lies left and right; blue above and below.
				out[o] = (5*c + 4*(w1+e1) - (w2 + e2) + 0.5*(n2+s2) - diag) / 8
				out[o+1] = c
				out[o+2] = (5*c + 4*(n1+s1) - (n2 + s2) + 0.5*(w2+e2) - diag) / 8
			default: // kindGb: blue lies left and right, red above and below
				out[o] = (5*c + 4*(n1+s1) - (n2 + s2) + 0.5*(w2+e2) - diag) / 8
				out[o+1] = c
				out[o+2] = (5*c + 4*(w1+e1) - (w2 + e2) + 0.5*(n2+s2) - diag) / 8
			}
		}
	}

	// The two-pixel frame the 5x5 kernel cannot reach falls back to
	// averaging whatever same-colour neighbours are in bounds.
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x >= 2 && y >= 2 && x < w-2 && y < h-2 {
				continue
			}
			o := (y*w + x) * 3
			out[o] = nearestColor(m, im, w, h, x, y, raw.Red)
			out[o+1] = nearestColor(m, im, w, h, x, y, raw.Green)
			out[o+2] = nearestColor(m, im, w, h, x, y, raw.Blue)
		}
	}
	return out, w, h
}

// normalizedMosaic subtracts black, scales white to 1, and applies the
// as-shot white balance — all before interpolation, because MHC assumes
// the channels track each other, and unbalanced channels do not.
func normalizedMosaic(im *raw.Image) []float32 {
	w, h := im.Width, im.Height
	m := make([]float32, w*h)
	var mul [4]float64
	var sub [4]float64
	for i := 0; i < 4; i++ {
		b := im.Black[i]
		span := im.White - b
		if span <= 0 {
			span = 1
		}
		sub[i] = b
		mul[i] = im.WB[im.CFA[i]] / span
	}
	for y := 0; y < h; y++ {
		row := y * w
		p := (y & 1) << 1
		for x := 0; x < w; x++ {
			i := p | (x & 1)
			m[row+x] = float32((float64(im.Data[row+x]) - sub[i]) * mul[i])
		}
	}
	return m
}

// nearestColor averages the samples of colour c within a small window —
// the border fallback, where the full kernel would read out of bounds.
func nearestColor(m []float32, im *raw.Image, w, h, x, y int, c uint8) float32 {
	if im.At(x, y) == c {
		return m[y*w+x]
	}
	sum, n := float32(0), 0
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			sx, sy := x+dx, y+dy
			if sx < 0 || sy < 0 || sx >= w || sy >= h || im.At(sx, sy) != c {
				continue
			}
			sum += m[sy*w+sx]
			n++
		}
	}
	if n == 0 {
		return m[y*w+x]
	}
	return sum / float32(n)
}
