package develop

import (
	"bytes"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/shaumik/qk-photo-viewer/internal/raw"
	"github.com/shaumik/qk-photo-viewer/internal/tiff"
	"github.com/shaumik/qk-photo-viewer/internal/tiff/tifftest"
)

/* ---------- helpers ---------- */

// flatScene is a uniform frame at a known linear value.
func flatScene(w, h int, r, g, b float32) *Scene {
	pix := make([]float32, w*h*3)
	for i := 0; i < w*h; i++ {
		pix[i*3], pix[i*3+1], pix[i*3+2] = r, g, b
	}
	return &Scene{W: w, H: h, Pix: pix, FromRAW: true}
}

// mosaicImage builds an RGGB sensor frame whose demosaiced result should be
// the given constant colour.
func mosaicImage(w, h int, r, g, b float64) *raw.Image {
	im := &raw.Image{
		Width: w, Height: h,
		Data: make([]uint16, w*h),
		CFA:  [4]uint8{raw.Red, raw.Green, raw.Green, raw.Blue},
		// A white level of 1000 keeps the fixtures exact: the values below
		// quantise without rounding, so a failure means a real one.
		White:     1000,
		WB:        [3]float64{1, 1, 1},
		CamToSRGB: [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1},
	}
	lv := [3]float64{r, g, b}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			im.Data[y*w+x] = uint16(math.Round(lv[im.At(x, y)] * 1000))
		}
	}
	return im
}

func closeTo(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

/* ---------- rendering ---------- */

func TestZeroEditIsTransferCurveOnly(t *testing.T) {
	// With no edits, developing a frame should be nothing but the sRGB
	// transfer curve: what the sensor saw, shown honestly.
	for _, v := range []float64{0, 0.02, 0.18, 0.5, 0.7} {
		s := flatScene(4, 4, float32(v), float32(v), float32(v))
		img := Render(s, Edit{})
		want := uint8(srgbEncode(v)*255 + 0.5)
		if got := img.Pix[0]; got != want {
			t.Errorf("linear %v rendered to %d, want %d", v, got, want)
		}
	}
}

func TestExposureMovesInStops(t *testing.T) {
	s := flatScene(4, 4, 0.1, 0.1, 0.1)
	base := Render(s, Edit{}).Pix[0]
	up := Render(s, Edit{Exposure: 1}).Pix[0]
	down := Render(s, Edit{Exposure: -1}).Pix[0]
	if up <= base || down >= base {
		t.Fatalf("exposure did not move the frame: -1EV=%d, 0=%d, +1EV=%d", down, base, up)
	}
	// A stop up is a doubling in linear light, below the rolloff knee.
	want := uint8(srgbEncode(0.2)*255 + 0.5)
	if up != want {
		t.Errorf("+1EV on 0.1 gave %d, want %d (0.2 linear)", up, want)
	}
}

func TestHighlightRolloffIsAShoulderNotACliff(t *testing.T) {
	render := func(v float32) int {
		return int(Render(flatScene(4, 4, v, v, v), Edit{}).Pix[0])
	}
	// Just over the sensor's white point, a default render should still
	// separate values instead of clipping them all to a flat white.
	near := []float32{0.9, 1.0, 1.1, 1.2}
	for i := 1; i < len(near); i++ {
		lo, hi := render(near[i-1]), render(near[i])
		if hi <= lo {
			t.Errorf("%.1f rendered %d, %.1f rendered %d: no separation above white",
				near[i-1], lo, near[i], hi)
		}
		if hi >= 255 {
			t.Errorf("%.1f clipped to white at a default render", near[i])
		}
	}
	// Far above white, a default render is allowed to say "this is white" —
	// that is what the Highlights slider is for, and it must reach down
	// into the headroom and bring detail back.
	if render(2.5) < 250 {
		t.Errorf("2.5x white rendered %d; a default render should read as white", render(2.5))
	}
	recovered := func(v float32) int {
		return int(Render(flatScene(4, 4, v, v, v), Edit{Highlights: -100}).Pix[0])
	}
	a, b := recovered(2.5), recovered(4.0)
	if a >= 245 || b <= a {
		t.Errorf("highlight recovery gave 2.5 -> %d and 4.0 -> %d; want both well clear "+
			"of white and still distinguishable", a, b)
	}
}

func TestToneCurveIsMonotonic(t *testing.T) {
	// A non-monotonic tone curve inverts detail somewhere in the frame,
	// which looks like solarisation. Check the extremes of every slider.
	edits := []Edit{
		{}, {Contrast: 100}, {Contrast: -100}, {Blacks: -100}, {Blacks: 100},
		{Contrast: 100, Blacks: -100}, {Contrast: -100, Blacks: 100},
	}
	for _, e := range edits {
		lut := toneLUT(e.Clamp())
		prev := float32(-1)
		for i := 0; i <= lutSize; i++ {
			if lut.v[i] < prev-1e-6 {
				t.Fatalf("%+v: tone curve dips at index %d (%v after %v)", e, i, lut.v[i], prev)
			}
			prev = lut.v[i]
		}
	}
}

func TestWhiteBalanceIsBrightnessNeutral(t *testing.T) {
	// Moving temperature should change colour, not exposure.
	for _, temp := range []float64{-100, -50, 50, 100} {
		kr, kg, kb := whiteBalance(Edit{Temp: temp})
		lum := 0.2126*kr + 0.7152*kg + 0.0722*kb
		if !closeTo(float64(lum), 1, 1e-5) {
			t.Errorf("temp %v changed luminance to %v, want 1", temp, lum)
		}
	}
	warm, _, coolB := whiteBalance(Edit{Temp: 100})
	if warm <= 1 || coolB >= 1 {
		t.Errorf("warming should raise red and lower blue, got r=%v b=%v", warm, coolB)
	}
}

func TestVibranceFavoursDullColours(t *testing.T) {
	// A nearly grey pixel should gain more saturation than an already
	// vivid one — that is what separates vibrance from saturation.
	dullR, dullG, dullB := saturate(0.50, 0.48, 0.46, 0.5)
	vividR, vividG, vividB := saturate(0.90, 0.20, 0.10, 0.5)
	dullGain := (dullR - dullB) / (0.50 - 0.46)
	vividGain := (vividR - vividB) / (0.90 - 0.10)
	if dullGain <= vividGain {
		t.Errorf("dull colour gained %vx, vivid gained %vx; vibrance should favour the dull one",
			dullGain, vividGain)
	}
	_ = dullG
	_ = vividG
}

func TestClarityAndSharpenSurviveTinyFrames(t *testing.T) {
	// The spatial passes index neighbours; a 2x2 frame must not panic.
	for _, n := range []int{1, 2, 4, 8, 16} {
		s := flatScene(n, n, 0.3, 0.3, 0.3)
		Render(s, Edit{Clarity: 100, Sharpen: 100})
	}
}

func TestClarityRaisesLocalContrast(t *testing.T) {
	// A soft edge should get harder, without the flat areas moving.
	w, h := 64, 64
	pix := make([]float32, w*h*3)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := float32(0.2)
			if x > w/2 {
				v = 0.4
			}
			for c := 0; c < 3; c++ {
				pix[(y*w+x)*3+c] = v
			}
		}
	}
	s := &Scene{W: w, H: h, Pix: pix}
	plain := Render(s, Edit{})
	sharp := Render(s, Edit{Clarity: 100})
	at := func(img []uint8, x, y int) int { return int(img[(y*w+x)*4]) }
	edgeBefore := at(plain.Pix, w/2+1, h/2) - at(plain.Pix, w/2-1, h/2)
	edgeAfter := at(sharp.Pix, w/2+1, h/2) - at(sharp.Pix, w/2-1, h/2)
	if edgeAfter <= edgeBefore {
		t.Errorf("clarity did not steepen the edge: %d -> %d", edgeBefore, edgeAfter)
	}
	if far := at(sharp.Pix, 2, 2); math.Abs(float64(far-at(plain.Pix, 2, 2))) > 2 {
		t.Errorf("clarity moved a flat area by %d codes", far-at(plain.Pix, 2, 2))
	}
}

func TestEditClampRejectsNonsense(t *testing.T) {
	e := Edit{Exposure: 99, Temp: -5000, Sharpen: -20, Contrast: math.NaN()}.Clamp()
	if e.Exposure != 5 || e.Temp != -100 || e.Sharpen != 0 || e.Contrast != 0 {
		t.Errorf("Clamp left nonsense in place: %+v", e)
	}
	if !(Edit{}).IsZero() || (Edit{Exposure: 0.1}).IsZero() {
		t.Error("IsZero disagrees with the zero value")
	}
}

/* ---------- demosaic and scene construction ---------- */

func TestDemosaicIsExactOnAFlatField(t *testing.T) {
	// Every interpolation correction is a Laplacian, and a flat field has
	// none — so a uniform colour must come back exactly uniform.
	im := mosaicImage(32, 32, 0.5, 0.6, 0.7)
	s := FromRAWImage(im, 0)
	if s.W != 32 || s.H != 32 {
		t.Fatalf("full demosaic gave %dx%d, want 32x32", s.W, s.H)
	}
	want := [3]float64{0.5, 0.6, 0.7}
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			for c := 0; c < 3; c++ {
				got := float64(s.Pix[(y*32+x)*3+c])
				if !closeTo(got, want[c], 1e-3) {
					t.Fatalf("pixel (%d,%d) channel %d = %v, want %v", x, y, c, got, want[c])
				}
			}
		}
	}
}

func TestDemosaicTracksAGradient(t *testing.T) {
	// A smooth horizontal ramp is the case bilinear handles worst; the
	// gradient correction should follow it closely.
	const w, h = 64, 16
	im := mosaicImage(w, h, 0, 0, 0)
	value := func(x int) float64 { return 0.2 + 0.6*float64(x)/float64(w-1) }
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			im.Data[y*w+x] = uint16(math.Round(value(x) * 1000))
		}
	}
	s := FromRAWImage(im, 0)
	worst := 0.0
	for y := 4; y < h-4; y++ {
		for x := 4; x < w-4; x++ {
			for c := 0; c < 3; c++ {
				d := math.Abs(float64(s.Pix[(y*w+x)*3+c]) - value(x))
				if d > worst {
					worst = d
				}
			}
		}
	}
	if worst > 0.01 {
		t.Errorf("worst gradient error %v, want under 0.01", worst)
	}
}

func TestBinHalfAveragesTheFilterBlock(t *testing.T) {
	im := mosaicImage(16, 16, 0.25, 0.5, 0.75)
	s := FromRAWImage(im, PreviewMaxDim)
	if s.W != 8 || s.H != 8 {
		t.Fatalf("preview scene is %dx%d, want 8x8", s.W, s.H)
	}
	for i := 0; i < s.W*s.H; i++ {
		if !closeTo(float64(s.Pix[i*3]), 0.25, 1e-4) ||
			!closeTo(float64(s.Pix[i*3+1]), 0.5, 1e-4) ||
			!closeTo(float64(s.Pix[i*3+2]), 0.75, 1e-4) {
			t.Fatalf("binned pixel %d = %v, want (0.25 0.5 0.75)", i, s.Pix[i*3:i*3+3])
		}
	}
}

func TestBlackLevelAndWhiteBalanceApply(t *testing.T) {
	im := mosaicImage(8, 8, 0, 0, 0)
	im.Black = [4]float64{100, 100, 100, 100}
	im.White = 1100 // a span of exactly 1000 above black
	im.WB = [3]float64{2, 1, 3}
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			im.Data[y*8+x] = 600 // 500 above black, i.e. half scale
		}
	}
	s := FromRAWImage(im, PreviewMaxDim)
	got := [3]float64{float64(s.Pix[0]), float64(s.Pix[1]), float64(s.Pix[2])}
	want := [3]float64{1.0, 0.5, 1.5} // 0.5 scaled by each multiplier
	for c := 0; c < 3; c++ {
		if !closeTo(got[c], want[c], 1e-4) {
			t.Errorf("channel %d = %v, want %v", c, got[c], want[c])
		}
	}
}

func TestOrientationRotatesTheFrame(t *testing.T) {
	// A 2x1 frame: left pixel red, right pixel blue. Turn it clockwise and
	// the left end swings to the top; anticlockwise and it swings down.
	pix := []float32{1, 0, 0, 0, 0, 1}
	cw, w, h := orient(append([]float32(nil), pix...), 2, 1, 6)
	if w != 1 || h != 2 {
		t.Fatalf("rotate 90 gave %dx%d, want 1x2", w, h)
	}
	if cw[0] != 1 || cw[5] != 1 {
		t.Errorf("after 90 CW got top=%v bottom=%v, want red over blue", cw[0:3], cw[3:6])
	}
	ccw, w2, h2 := orient(append([]float32(nil), pix...), 2, 1, 8)
	if w2 != 1 || h2 != 2 {
		t.Fatalf("rotate 270 gave %dx%d, want 1x2", w2, h2)
	}
	if ccw[2] != 1 || ccw[3] != 1 {
		t.Errorf("after 90 CCW got top=%v bottom=%v, want blue over red", ccw[0:3], ccw[3:6])
	}
	same, w3, h3 := orient(append([]float32(nil), pix...), 2, 1, 1)
	if w3 != 2 || h3 != 1 || same[0] != 1 {
		t.Error("orientation 1 should leave the frame alone")
	}
}

func TestFromJPEGHasNoHeadroom(t *testing.T) {
	// The fallback path must be honest that the highlights are already gone.
	img := Render(flatScene(32, 32, 0.4, 0.4, 0.4), Edit{})
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	s, err := FromJPEGBytes(buf.Bytes(), 1, PreviewMaxDim)
	if err != nil {
		t.Fatalf("FromJPEGBytes: %v", err)
	}
	if s.FromRAW {
		t.Error("a JPEG-sourced scene should not claim to be RAW")
	}
	if s.Headroom != 0 {
		t.Errorf("headroom = %v, want 0", s.Headroom)
	}
	// It should round-trip back to roughly the linear value it came from.
	if !closeTo(float64(s.Pix[0]), 0.4, 0.02) {
		t.Errorf("decoded linear value %v, want about 0.4", s.Pix[0])
	}
	if _, err := FromJPEGBytes([]byte("not a jpeg"), 1, 0); err == nil {
		t.Error("expected an error decoding rubbish")
	}
}

func TestHeadroomMeasuresOverexposure(t *testing.T) {
	if h := flatSceneHeadroom(t, 0.5); h != 0 {
		t.Errorf("a frame under white has headroom %v, want 0", h)
	}
	if h := flatSceneHeadroom(t, 3.5); h < 1 || h > 2.5 {
		t.Errorf("a frame at 3.5x white reports %v stops, want about 1.8", h)
	}
}

func flatSceneHeadroom(t *testing.T, v float32) float64 {
	t.Helper()
	return headroom(flatScene(16, 16, v, v, v).Pix)
}

/* ---------- auto-develop ---------- */

// photoScene is a frame ramping evenly across a range of *perceived*
// brightness — which is roughly how a photograph is distributed, and not
// at all how a ramp in linear light is.
func photoScene(w, h int, loDisplay, hiDisplay float64, tint [3]float64) *Scene {
	pix := make([]float32, w*h*3)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			f := float64(x) / float64(w-1)
			v := srgbDecode(loDisplay + (hiDisplay-loDisplay)*f)
			for c := 0; c < 3; c++ {
				pix[(y*w+x)*3+c] = float32(v * tint[c])
			}
		}
	}
	return &Scene{W: w, H: h, Pix: pix, FromRAW: true}
}

// blownScene is an ordinary frame with a hot band across the top — a
// properly exposed subject under a sky the sensor could not hold.
func blownScene(w, h int) *Scene {
	s := photoScene(w, h, 0.1, 0.7, [3]float64{1, 1, 1})
	for y := 0; y < h/8; y++ {
		for x := 0; x < w; x++ {
			for c := 0; c < 3; c++ {
				s.Pix[(y*w+x)*3+c] = 4.0
			}
		}
	}
	return s
}

func TestAutoBrightensAnUnderexposedFrame(t *testing.T) {
	dark := photoScene(64, 64, 0, 0.12, [3]float64{1, 1, 1})
	e := Auto(dark)
	if e.Exposure < 1.5 {
		t.Errorf("exposure = %v stops, want a real lift on a frame this dark", e.Exposure)
	}
	// And the lift must actually show up in the render.
	mid := (64*32 + 32) * 4
	before := Render(dark, Edit{}).Pix[mid]
	after := Render(dark, e).Pix[mid]
	if after <= before {
		t.Errorf("auto did not brighten the render: %d -> %d", before, after)
	}
}

func TestAutoRecoversABlownSky(t *testing.T) {
	blown := blownScene(64, 64)
	e := Auto(blown)
	if e.Exposure > 0 {
		t.Errorf("exposure = %v, want no lift on a frame that is already clipping", e.Exposure)
	}
	if e.Highlights >= -10 {
		t.Errorf("highlights = %v, want a real recovery", e.Highlights)
	}
	// The recovery has to reach the pixels, not just the slider.
	plain := Render(blown, Edit{}).Pix[0]
	fixed := Render(blown, e).Pix[0]
	if plain < 250 {
		t.Fatalf("fixture is not actually blown: the sky renders at %d", plain)
	}
	if fixed >= 250 {
		t.Errorf("auto left the sky at %d; the detail is still buried", fixed)
	}
}

func TestAutoNeutralisesAColourCast(t *testing.T) {
	warm := photoScene(64, 64, 0.15, 0.85, [3]float64{1.35, 1.0, 0.7})
	e := Auto(warm)
	if e.Temp >= 0 {
		t.Errorf("temp = %v, want a cooling correction on a warm frame", e.Temp)
	}
	cold := photoScene(64, 64, 0.15, 0.85, [3]float64{0.7, 1.0, 1.35})
	if e2 := Auto(cold); e2.Temp <= 0 {
		t.Errorf("temp = %v, want a warming correction on a cold frame", e2.Temp)
	}
	// The correction should be a nudge, not a verdict: a fully neutralised
	// sunset stops being a sunset.
	if e.Temp < -60 {
		t.Errorf("temp = %v, want the correction damped", e.Temp)
	}
}

func TestAutoLeavesAWellExposedFrameNearlyAlone(t *testing.T) {
	good := photoScene(64, 64, 0.05, 0.95, [3]float64{1, 1, 1})
	e := Auto(good)
	if math.Abs(e.Exposure) > 0.8 {
		t.Errorf("exposure = %v, want only a small move on a good frame", e.Exposure)
	}
	if e.Highlights < -20 {
		t.Errorf("highlights = %v, want no big recovery on a frame that is not blown", e.Highlights)
	}
	if math.Abs(e.Temp) > 10 || math.Abs(e.Tint) > 10 {
		t.Errorf("temp/tint = %v/%v, want no colour correction on a neutral frame", e.Temp, e.Tint)
	}
}

func TestAutoStaysInRangeOnDegenerateFrames(t *testing.T) {
	for name, s := range map[string]*Scene{
		"black":  flatScene(32, 32, 0, 0, 0),
		"white":  flatScene(32, 32, 40, 40, 40),
		"tiny":   flatScene(1, 1, 0.2, 0.2, 0.2),
		"empty":  {W: 0, H: 0},
		"single": flatScene(2, 2, 0.5, 0.5, 0.5),
	} {
		e := Auto(s)
		if e != e.Clamp() {
			t.Errorf("%s: auto produced out-of-range values %+v", name, e)
		}
	}
}

func TestAutoOnlySharpensRAW(t *testing.T) {
	s := photoScene(32, 32, 0.2, 0.8, [3]float64{1, 1, 1})
	if Auto(s).Sharpen == 0 {
		t.Error("a demosaiced frame should get some sharpening")
	}
	s.FromRAW = false
	if Auto(s).Sharpen != 0 {
		t.Error("a camera JPEG is already sharpened and should not be sharpened again")
	}
}

/* ---------- export metadata ---------- */

func writeARWWithMeta(t *testing.T) string {
	t.Helper()
	b := tifftest.New()
	blob := b.AddBlob(make([]byte, 64))
	// The first directory added is the one the TIFF header points at, so
	// the root has to come first or nothing below it is reachable.
	root := b.AddIFD()
	exif := b.AddIFD()
	gps := b.AddIFD()
	exif.SRational(tagExposureTime, [2]int32{1, 2000}).
		SRational(tagFNumber, [2]int32{56, 10}).
		Short(tagISO, 640).
		SRational(tagFocalLength, [2]int32{2100, 10}).
		ASCII(tagDateTimeOriginal, "2026:08:02 16:41:03")
	gps.ASCII(tagGPSLatRef, "N").
		SRational(tagGPSLat, [2]int32{51, 1}, [2]int32{30, 1}, [2]int32{26, 1}).
		ASCII(tagGPSLngRef, "W").
		SRational(tagGPSLng, [2]int32{0, 1}, [2]int32{7, 1}, [2]int32{39, 1})
	root.ASCII(tiff.TagMake, "SONY").
		ASCII(tiff.TagModel, "ILCE-7M3").
		Short(tiff.TagOrientation, 6).
		BlobOffset(tiff.TagStripOffsets, blob).
		SubIFD(exif, gps)

	path := filepath.Join(t.TempDir(), "DSC01234.ARW")
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExifForRebuildsRAWMetadata(t *testing.T) {
	seg := ExifFor(writeARWWithMeta(t), 6000, 4000)
	if len(seg) < 12 {
		t.Fatal("ExifFor returned no segment for a RAW with metadata")
	}
	if seg[0] != 0xFF || seg[1] != 0xE1 || string(seg[4:10]) != "Exif\x00\x00" {
		t.Fatalf("segment is not an EXIF APP1: % x", seg[:10])
	}
	body := seg[10:]
	f, err := tiff.Parse(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("the segment we wrote does not parse: %v", err)
	}
	if got := f.AnyStr(tiff.TagMake); got != "SONY" {
		t.Errorf("make = %q, want SONY", got)
	}
	if got := f.AnyStr(tiff.TagModel); got != "ILCE-7M3" {
		t.Errorf("model = %q, want ILCE-7M3", got)
	}
	if got, _ := f.AnyInt(tagISO); got != 640 {
		t.Errorf("ISO = %d, want 640", got)
	}
	if got := f.AnyFloats(tagExposureTime); len(got) == 0 || !closeTo(got[0], 1.0/2000, 1e-9) {
		t.Errorf("exposure time = %v, want 1/2000", got)
	}
	if got := f.AnyFloats(tagFNumber); len(got) == 0 || !closeTo(got[0], 5.6, 1e-9) {
		t.Errorf("f-number = %v, want 5.6", got)
	}
	if got := f.AnyStr(tagDateTimeOriginal); got != "2026:08:02 16:41:03" {
		t.Errorf("date = %q, want the original", got)
	}
	if got := f.AnyStr(tagGPSLatRef); got != "N" {
		t.Errorf("GPS latitude ref = %q, want N", got)
	}
	if got := f.AnyFloats(tagGPSLat); len(got) != 3 || got[0] != 51 {
		t.Errorf("GPS latitude = %v, want 51 30 26", got)
	}
	// Orientation must be reset: develop already rotated the pixels, and a
	// viewer that honours the tag would rotate them again.
	if got, ok := f.AnyInt(tiff.TagOrientation); !ok || got != 1 {
		t.Errorf("orientation = %d, want 1", got)
	}
	if got, _ := f.AnyInt(tagPixelXDimension); got != 6000 {
		t.Errorf("pixel width = %d, want the exported size 6000", got)
	}
}

func TestExifForCopiesAJPEGsBlockAndResetsOrientation(t *testing.T) {
	// A JPEG with EXIF saying "rotate 90".
	inner := tifftest.New()
	inner.AddIFD().ASCII(tiff.TagMake, "SONY").Short(tiff.TagOrientation, 6).Short(tagISO, 200)
	payload := append([]byte("Exif\x00\x00"), inner.Bytes()...)

	img := Render(flatScene(8, 8, 0.3, 0.3, 0.3), Edit{})
	var body bytes.Buffer
	jpeg.Encode(&body, img, nil)
	src := body.Bytes()
	withExif := append([]byte{}, src[:2]...)
	withExif = append(withExif, 0xFF, 0xE1, byte((len(payload)+2)>>8), byte(len(payload)+2))
	withExif = append(withExif, payload...)
	withExif = append(withExif, src[2:]...)

	path := filepath.Join(t.TempDir(), "DSC01234.JPG")
	if err := os.WriteFile(path, withExif, 0o644); err != nil {
		t.Fatal(err)
	}
	seg := ExifFor(path, 0, 0)
	if seg == nil {
		t.Fatal("no EXIF copied from the JPEG")
	}
	f, err := tiff.Parse(bytes.NewReader(seg[10:]), int64(len(seg)-10))
	if err != nil {
		t.Fatalf("copied EXIF does not parse: %v", err)
	}
	if got, _ := f.AnyInt(tiff.TagOrientation); got != 1 {
		t.Errorf("orientation = %d, want it reset to 1", got)
	}
	if got, _ := f.AnyInt(tagISO); got != 200 {
		t.Errorf("ISO = %d, want the original 200 to survive", got)
	}
}

func TestEncodeJPEGSplicesExifAndStaysDecodable(t *testing.T) {
	img := Render(flatScene(32, 32, 0.35, 0.3, 0.25), Edit{})
	seg := ExifFor(writeARWWithMeta(t), 32, 32)
	out, err := EncodeJPEG(img, DefaultQuality, seg)
	if err != nil {
		t.Fatalf("EncodeJPEG: %v", err)
	}
	if out[0] != 0xFF || out[1] != 0xD8 || out[2] != 0xFF || out[3] != 0xE1 {
		t.Errorf("EXIF is not the first segment after SOI: % x", out[:6])
	}
	dec, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("the JPEG we wrote does not decode: %v", err)
	}
	if b := dec.Bounds(); b.Dx() != 32 || b.Dy() != 32 {
		t.Errorf("decoded %dx%d, want 32x32", b.Dx(), b.Dy())
	}
	if copied := copyAPP1(out); copied == nil {
		t.Error("the spliced segment cannot be found again")
	}
	// No metadata is not an error.
	plain, err := EncodeJPEG(img, DefaultQuality, nil)
	if err != nil {
		t.Fatalf("EncodeJPEG without EXIF: %v", err)
	}
	if _, err := jpeg.Decode(bytes.NewReader(plain)); err != nil {
		t.Fatalf("plain JPEG does not decode: %v", err)
	}
}

func TestExifForMissingFile(t *testing.T) {
	if seg := ExifFor(filepath.Join(t.TempDir(), "nope.ARW"), 100, 100); seg != nil {
		t.Error("expected no segment for a missing file")
	}
}
