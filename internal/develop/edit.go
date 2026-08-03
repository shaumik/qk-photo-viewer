package develop

import (
	"image"
	"math"
)

// Edit is the full state of a photo's development — everything QK knows
// about how a shot should look, and the only thing it stores. The zero
// value renders the frame as shot.
//
// Every field except Exposure is on a -100..100 scale, because a person
// nudging a slider is thinking "a bit more" and not in stops or Kelvin.
type Edit struct {
	Exposure   float64 `json:"exposure"`   // stops, -5..+5
	Temp       float64 `json:"temp"`       // warmer positive, cooler negative
	Tint       float64 `json:"tint"`       // magenta positive, green negative
	Highlights float64 `json:"highlights"` // negative recovers a blown sky
	Shadows    float64 `json:"shadows"`    // positive opens up dark areas
	Blacks     float64 `json:"blacks"`     // negative sets a deeper black point
	Contrast   float64 `json:"contrast"`
	Vibrance   float64 `json:"vibrance"` // saturation, weighted to dull colours
	Clarity    float64 `json:"clarity"`  // local contrast, the "pop" control
	Sharpen    float64 `json:"sharpen"`
	Noise      float64 `json:"noise"` // smooth high-ISO grain and colour blotches

	// Where the picture's edges are, and what the lens did to what is
	// inside them. Distortion and Vignette undo the lens; the camera does
	// this for its own JPEG and leaves the RAW to whoever opens it.
	Distortion float64 `json:"distortion"` // positive straightens barrel bowing
	Vignette   float64 `json:"vignette"`   // positive brightens the corners
	Rotate     float64 `json:"rotate"`     // degrees clockwise, for a level horizon

	// Crop, normalised to the corrected frame. A zero width or height
	// means the whole frame, so the zero Edit still means "as shot".
	CropX float64 `json:"cropX"`
	CropY float64 `json:"cropY"`
	CropW float64 `json:"cropW"`
	CropH float64 `json:"cropH"`
}

// IsZero reports whether the edit leaves the shot alone.
func (e Edit) IsZero() bool { return e == Edit{} }

// Clamp brings every field into range. Sidecars are plain files a person
// can edit, so nothing downstream should assume they are sane.
func (e Edit) Clamp() Edit {
	c := func(v, lo, hi float64) float64 {
		if math.IsNaN(v) {
			return 0
		}
		return math.Max(lo, math.Min(hi, v))
	}
	e.Exposure = c(e.Exposure, -5, 5)
	e.Temp = c(e.Temp, -100, 100)
	e.Tint = c(e.Tint, -100, 100)
	e.Highlights = c(e.Highlights, -100, 100)
	e.Shadows = c(e.Shadows, -100, 100)
	e.Blacks = c(e.Blacks, -100, 100)
	e.Contrast = c(e.Contrast, -100, 100)
	e.Vibrance = c(e.Vibrance, -100, 100)
	e.Clarity = c(e.Clarity, -100, 100)
	e.Sharpen = c(e.Sharpen, 0, 100)
	e.Noise = c(e.Noise, 0, 100)
	e.Distortion = c(e.Distortion, -100, 100)
	e.Vignette = c(e.Vignette, -100, 100)
	e.Rotate = c(e.Rotate, -maxRotate, maxRotate)
	// A crop is kept whole or discarded: half of one, with a width but no
	// origin, would silently move the frame.
	if e.CropW > 0 && e.CropH > 0 {
		e.CropX, e.CropY, e.CropW, e.CropH = e.CropRect()
	} else {
		e.CropX, e.CropY, e.CropW, e.CropH = 0, 0, 0, 0
	}
	return e
}

// WithLookOf returns this edit wearing another's look, keeping its own
// framing.
//
// The split matters. Tone, colour and lens correction are decisions about
// a shoot: the light was the same, the glass was the same, and a set that
// was developed one frame at a time looks like a set developed one frame
// at a time. Framing is the opposite — where you cut and how far you had
// to turn the camera are decisions about one photograph, and copying them
// across a shoot would crop every other frame wrongly.
func (e Edit) WithLookOf(look Edit) Edit {
	look.Rotate = e.Rotate
	look.CropX, look.CropY = e.CropX, e.CropY
	look.CropW, look.CropH = e.CropW, e.CropH
	return look
}

// How far each slider reaches at full travel.
const (
	tempStops     = 0.6  // temperature, in stops of red against blue
	tintStops     = 0.3  // tint, in stops of green
	toneStops     = 1.8  // highlight recovery and shadow lift
	blackDepth    = 0.03 // black point travel, in linear units
	contrastDepth = 0.55 // how much of a full S-curve contrast 100 applies
	clarityDepth  = 0.6
	sharpenDepth  = 1.2
	rolloffKnee   = 0.72 // where highlights start compressing instead of clipping
)

// Render develops a Scene into a displayable image.
//
// The order is not arbitrary. Exposure, white balance and the tonal
// controls all belong in linear light, where they mean what they say — a
// stop is a doubling — and where recovering a highlight is possible at
// all. Only then does the frame cross into display space, where contrast
// and saturation match how the eye reads them, and where local contrast
// and sharpening finish the job.
func Render(s *Scene, e Edit) *image.RGBA {
	g := applyGeometry(s, e.Clamp())
	return render(g, e, make([]float32, len(g.Pix)))
}

// RenderInPlace is Render for a Scene that will not be needed again. It
// works in the Scene's own buffer rather than allocating a second one,
// which on a full-resolution export is a few hundred megabytes not asked
// for. The Scene is left holding display values and must be discarded.
func RenderInPlace(s *Scene, e Edit) *image.RGBA {
	// Geometry allocates its own output anyway, so once it has run the
	// buffer to work in is the one it just made, not the caller's.
	if g := applyGeometry(s, e.Clamp()); g != s {
		return render(g, e, g.Pix)
	}
	return render(s, e, s.Pix)
}

// RenderUncropped develops the frame with the lens corrected and the
// straightening applied, but the crop ignored. This is what the crop tool
// draws on top of: you cannot choose where to cut if you can only see
// what survived the last cut.
func RenderUncropped(s *Scene, e Edit) *image.RGBA {
	e.CropX, e.CropY, e.CropW, e.CropH = 0, 0, 0, 0
	return Render(s, e)
}

func render(s *Scene, e Edit, buf []float32) *image.RGBA {
	e = e.Clamp()
	renderFloat(s, e, buf)
	spatial(buf, s.W, s.H, e)

	img := image.NewRGBA(image.Rect(0, 0, s.W, s.H))
	for i, n := 0, s.W*s.H; i < n; i++ {
		img.Pix[i*4] = quantize(buf[i*3])
		img.Pix[i*4+1] = quantize(buf[i*3+1])
		img.Pix[i*4+2] = quantize(buf[i*3+2])
		img.Pix[i*4+3] = 255
	}
	return img
}

// renderFloat runs the per-pixel half of the pipeline, writing
// display-referred values in 0..1 into out. out may alias s.Pix.
func renderFloat(s *Scene, e Edit, out []float32) {
	kr, kg, kb := whiteBalance(e)
	gain := float32(math.Exp2(e.Exposure))
	kr, kg, kb = kr*gain, kg*gain, kb*gain

	tone := toneLUT(e)
	lift := liftLUT(e)
	vib := float32(e.Vibrance / 100)

	for i, n := 0, s.W*s.H; i < n; i++ {
		r := s.Pix[i*3] * kr
		g := s.Pix[i*3+1] * kg
		b := s.Pix[i*3+2] * kb

		// One gain for all three channels keeps the hue while moving the
		// brightness — the difference between recovering a sky and
		// staining it.
		l := lift.at(0.2126*r + 0.7152*g + 0.0722*b)
		r, g, b = r*l, g*l, b*l

		dr, dg, db := tone.at(r), tone.at(g), tone.at(b)
		if vib != 0 {
			dr, dg, db = saturate(dr, dg, db, vib)
		}
		out[i*3], out[i*3+1], out[i*3+2] = dr, dg, db
	}
}

// whiteBalance turns the temperature and tint sliders into linear channel
// multipliers, normalised so that moving them changes colour and not
// brightness.
func whiteBalance(e Edit) (float32, float32, float32) {
	if e.Temp == 0 && e.Tint == 0 {
		return 1, 1, 1
	}
	kr := math.Exp2(e.Temp / 100 * tempStops)
	kb := math.Exp2(-e.Temp / 100 * tempStops)
	kg := math.Exp2(-e.Tint / 100 * tintStops)
	norm := 1 / (0.2126*kr + 0.7152*kg + 0.0722*kb)
	return float32(kr * norm), float32(kg * norm), float32(kb * norm)
}

// saturate scales a pixel's distance from its own luminance. Vibrance
// weights the push towards colours that are already dull, so a washed-out
// sky gains and a red jacket does not turn into a cartoon.
func saturate(r, g, b, amount float32) (float32, float32, float32) {
	lum := 0.2126*r + 0.7152*g + 0.0722*b
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
	sat := float32(0)
	if maxc > 1e-4 {
		sat = (maxc - minc) / maxc
	}
	f := 1 + amount*(1-sat)
	return lum + (r-lum)*f, lum + (g-lum)*f, lum + (b-lum)*f
}

/* ---------- lookup tables ----------

Both of the per-pixel curves below depend only on a single number, so they
are tabulated once per render rather than evaluated a few million times.
Tables are sampled finely enough that linear interpolation between entries
lands well inside a single 8-bit code. */

const (
	lutSize  = 16384
	lutRange = 16.0 // linear values above this are clipped white regardless
)

type lut struct {
	v     [lutSize + 1]float32
	scale float32
}

func (t *lut) at(x float32) float32 {
	if x <= 0 {
		return t.v[0]
	}
	f := x * t.scale
	if f >= lutSize {
		return t.v[lutSize]
	}
	i := int(f)
	frac := f - float32(i)
	return t.v[i] + (t.v[i+1]-t.v[i])*frac
}

func newLUT(f func(float64) float64) *lut {
	t := &lut{scale: lutSize / lutRange}
	for i := 0; i <= lutSize; i++ {
		t.v[i] = float32(f(float64(i) / lutSize * lutRange))
	}
	return t
}

// toneLUT maps one linear channel to its display value: black point,
// highlight rolloff, the sRGB transfer curve, then contrast.
func toneLUT(e Edit) *lut {
	blk := -e.Blacks / 100 * blackDepth
	span := 1 - blk
	if span < 0.1 {
		span = 0.1
	}
	amt := e.Contrast / 100 * contrastDepth
	return newLUT(func(x float64) float64 {
		x = (x - blk) / span
		if x < 0 {
			x = 0
		}
		// Above the knee, compress asymptotically towards white instead of
		// clipping flat. This is where a RAW file's headroom shows up as
		// cloud detail rather than a white hole.
		if x > rolloffKnee {
			x = rolloffKnee + (1-rolloffKnee)*(1-math.Exp(-(x-rolloffKnee)/(1-rolloffKnee)))
		}
		d := srgbEncode(x)
		if amt != 0 {
			d += amt * (d*d*(3-2*d) - d) // blend towards a smoothstep S-curve
		}
		return d
	})
}

// liftLUT maps a pixel's linear luminance to the gain that the highlight
// and shadow sliders want applied there.
func liftLUT(e Edit) *lut {
	if e.Highlights == 0 && e.Shadows == 0 {
		return newLUT(func(float64) float64 { return 1 })
	}
	sh, hi := e.Shadows/100, e.Highlights/100
	return newLUT(func(y float64) float64 {
		// Weight the two masks by perceived brightness, not linear light,
		// so "shadows" means what it looks like rather than what it meters.
		p := srgbEncode(math.Min(y, 1))
		ms := math.Max(0, 1-p/0.6)
		mh := math.Max(0, (p-0.4)/0.6)
		return math.Exp2(toneStops * (sh*ms*ms + hi*mh*mh))
	})
}

func quantize(v float32) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 255
	}
	return uint8(v*255 + 0.5)
}

/* ---------- spatial passes ---------- */

// spatial applies the two neighbourhood effects: clarity, which is local
// contrast at a coarse scale, and sharpening, which is the same idea at a
// one-pixel scale. Both work on luminance only — pushing colour around at
// an edge is what makes oversharpened photos look cheap.
func spatial(buf []float32, w, h int, e Edit) {
	if w < 8 || h < 8 {
		return
	}
	// Denoise first. Clarity and sharpening both amplify whatever is
	// already there, and grain is exactly what they would amplify.
	if e.Noise > 0 {
		denoise(buf, w, h, e.Noise/100)
	}
	if e.Clarity != 0 {
		radius := min(w, h) / 100
		if radius < 2 {
			radius = 2
		}
		unsharp(buf, w, h, radius, float32(e.Clarity/100*clarityDepth))
	}
	if e.Sharpen > 0 {
		unsharp(buf, w, h, 1, float32(e.Sharpen/100*sharpenDepth))
	}
}

// unsharp adds back a multiple of what a blur removed. Three box passes
// approximate a Gaussian closely enough that no one can tell, at a cost
// that does not depend on the radius.
func unsharp(buf []float32, w, h int, radius int, amount float32) {
	n := w * h
	lum := make([]float32, n)
	for i := 0; i < n; i++ {
		lum[i] = 0.2126*buf[i*3] + 0.7152*buf[i*3+1] + 0.0722*buf[i*3+2]
	}
	blur := append([]float32(nil), lum...)
	tmp := make([]float32, n)
	for pass := 0; pass < 3; pass++ {
		boxH(blur, tmp, w, h, radius)
		boxV(tmp, blur, w, h, radius)
	}
	for i := 0; i < n; i++ {
		l := lum[i]
		if l < 1e-4 {
			continue
		}
		ratio := 1 + amount*(l-blur[i])/l
		// Clamped so a blown edge cannot turn into a halo.
		if ratio < 0.25 {
			ratio = 0.25
		} else if ratio > 4 {
			ratio = 4
		}
		buf[i*3] *= ratio
		buf[i*3+1] *= ratio
		buf[i*3+2] *= ratio
	}
}

func boxH(src, dst []float32, w, h, r int) {
	inv := 1 / float32(2*r+1)
	for y := 0; y < h; y++ {
		row := y * w
		var sum float32
		for x := -r; x <= r; x++ {
			sum += src[row+clampi(x, 0, w-1)]
		}
		for x := 0; x < w; x++ {
			dst[row+x] = sum * inv
			sum += src[row+clampi(x+r+1, 0, w-1)] - src[row+clampi(x-r, 0, w-1)]
		}
	}
}

func boxV(src, dst []float32, w, h, r int) {
	inv := 1 / float32(2*r+1)
	for x := 0; x < w; x++ {
		var sum float32
		for y := -r; y <= r; y++ {
			sum += src[clampi(y, 0, h-1)*w+x]
		}
		for y := 0; y < h; y++ {
			dst[y*w+x] = sum * inv
			sum += src[clampi(y+r+1, 0, h-1)*w+x] - src[clampi(y-r, 0, h-1)*w+x]
		}
	}
}

func clampi(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
