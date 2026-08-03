package develop

import (
	"math"
	"testing"
)

// markedScene is a black frame with a single white pixel, so a geometric
// operation can be checked by asking where the pixel went.
func markedScene(w, h, mx, my int) *Scene {
	s := &Scene{W: w, H: h, Pix: make([]float32, w*h*3)}
	o := (my*w + mx) * 3
	s.Pix[o], s.Pix[o+1], s.Pix[o+2] = 1, 1, 1
	return s
}

// brightest returns the position of the strongest pixel, which after a
// resample is where the mark ended up.
func brightest(s *Scene) (int, int, float32) {
	bx, by, best := -1, -1, float32(-1)
	for y := 0; y < s.H; y++ {
		for x := 0; x < s.W; x++ {
			if v := s.Pix[(y*s.W+x)*3]; v > best {
				bx, by, best = x, y, v
			}
		}
	}
	return bx, by, best
}

// checkerScene is a frame of alternating blocks: enough structure that a
// resample which loses or duplicates content shows up.
func checkerScene(w, h, block int) *Scene {
	s := &Scene{W: w, H: h, Pix: make([]float32, w*h*3)}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := float32(0.2)
			if (x/block+y/block)%2 == 0 {
				v = 0.8
			}
			o := (y*w + x) * 3
			s.Pix[o], s.Pix[o+1], s.Pix[o+2] = v, v, v
		}
	}
	return s
}

func TestNoGeometryIsNotAResample(t *testing.T) {
	// An identity warp must be skipped outright, not run: resampling for
	// no reason costs time and softens the picture.
	s := checkerScene(64, 48, 8)
	if got := applyGeometry(s, Edit{}); got != s {
		t.Error("an edit with no geometry should return the very same Scene")
	}
	if got := applyGeometry(s, Edit{Exposure: 2, Contrast: 40}); got != s {
		t.Error("tonal edits are not geometry and must not trigger a resample")
	}
	if (Edit{}).HasGeometry() {
		t.Error("the zero edit has no geometry")
	}
	for _, e := range []Edit{
		{Rotate: 0.5}, {Distortion: 10}, {Vignette: -20},
		{CropX: 0.1, CropY: 0.1, CropW: 0.8, CropH: 0.8},
	} {
		if !e.HasGeometry() {
			t.Errorf("%+v should count as geometry", e)
		}
	}
}

func TestCropTakesTheRegionAsked(t *testing.T) {
	s := checkerScene(100, 100, 10)
	e := Edit{CropX: 0.25, CropY: 0.5, CropW: 0.5, CropH: 0.25}
	g := applyGeometry(s, e)
	if g.W != 50 || g.H != 25 {
		t.Fatalf("cropped to %dx%d, want 50x25", g.W, g.H)
	}
	// The centre of the crop must be the same content as the source point
	// it was taken from.
	want := s.Pix[((100*5/8)*100+50)*3]
	got := g.Pix[((25/2)*50+25)*3]
	if math.Abs(float64(got-want)) > 0.05 {
		t.Errorf("crop centre reads %v, want about %v", got, want)
	}
}

func TestUnsetCropIsTheWholeFrame(t *testing.T) {
	// The zero Edit has crop fields too, and they have to mean "all of it".
	for _, e := range []Edit{{}, {CropW: 0.5}, {CropH: 0.5}, {CropX: 0.3}} {
		x, y, w, h := e.CropRect()
		if x != 0 || y != 0 || w != 1 || h != 1 {
			t.Errorf("%+v gave crop %v %v %v %v, want the whole frame", e, x, y, w, h)
		}
	}
	// And a half-written crop is discarded rather than half-applied.
	c := Edit{CropX: 0.3, CropW: 0}.Clamp()
	if c.CropX != 0 {
		t.Errorf("a crop with no width kept its origin: %+v", c)
	}
}

func TestRotateTurnsTheRightWay(t *testing.T) {
	// A mark directly above centre should swing to the right when the
	// frame is rotated clockwise — that is what "straighten a horizon
	// that droops to the right" has to do.
	s := markedScene(101, 101, 50, 20)
	g := applyGeometry(s, Edit{Rotate: 30})
	x, y, v := brightest(g)
	if v < 0.2 {
		t.Fatalf("the mark did not survive rotation (peak %v)", v)
	}
	if x <= 50 {
		t.Errorf("mark landed at x=%d; a clockwise rotation should move it right of centre", x)
	}
	if y >= 50 {
		t.Errorf("mark landed at y=%d; it should still be above centre", y)
	}
	// Turning the other way is the mirror image.
	gl := applyGeometry(s, Edit{Rotate: -30})
	xl, _, _ := brightest(gl)
	if xl >= 50 {
		t.Errorf("anticlockwise put the mark at x=%d, want left of centre", xl)
	}
	if math.Abs(float64((50-xl)-(x-50))) > 2 {
		t.Errorf("the two directions are not symmetric: %d and %d", xl, x)
	}
}

func TestRotateNeverReadsOutsideTheFrame(t *testing.T) {
	// Straightening swings blank corners into view unless the result is
	// zoomed to fit. Assert it on the mapping rather than on brightness: a
	// cubic rings around hard edges, so a dark pixel is not proof of an
	// empty one, and a mapping inside the frame is proof of the opposite.
	s := checkerScene(120, 80, 10)
	for _, e := range []Edit{
		{Rotate: -45}, {Rotate: -12}, {Rotate: -3}, {Rotate: 3}, {Rotate: 12}, {Rotate: 45},
		{Rotate: 8, Distortion: -80}, {Distortion: -100},
		{Rotate: 20, CropX: 0.1, CropY: 0.1, CropW: 0.5, CropH: 0.5},
	} {
		wp := newWarp(s, e)
		for oy := 0; oy < wp.outH; oy++ {
			fy := wp.cy + (float64(oy)+0.5)/float64(wp.outH)*wp.ch
			for ox := 0; ox < wp.outW; ox++ {
				fx := wp.cx + (float64(ox)+0.5)/float64(wp.outW)*wp.cw
				px, py, _ := wp.source(fx, fy)
				if px < 0 || py < 0 || px > float64(s.W-1) || py > float64(s.H-1) {
					t.Fatalf("%+v: output (%d,%d) reads source (%.2f,%.2f), outside %dx%d",
						e, ox, oy, px, py, s.W, s.H)
				}
			}
		}
	}
}

func TestFitScaleOnlyZoomsWhenItHasTo(t *testing.T) {
	s := checkerScene(120, 80, 10)
	if got := newWarp(s, Edit{}).fit; got != 1 {
		t.Errorf("fit = %v with nothing to correct, want 1", got)
	}
	if got := newWarp(s, Edit{Rotate: 10}).fit; got >= 1 || got < 0.5 {
		t.Errorf("fit = %v for a 10 degree rotation, want a modest zoom under 1", got)
	}
	// A tighter rotation should need less zoom than a looser one.
	small := newWarp(s, Edit{Rotate: 3}).fit
	large := newWarp(s, Edit{Rotate: 25}).fit
	if small <= large {
		t.Errorf("3 degrees needed fit %v and 25 degrees %v; the smaller turn should zoom less",
			small, large)
	}
}

func TestDistortionCorrectsBarrelOutward(t *testing.T) {
	// Barrel distortion pulls the edges of the frame inward. Correcting it
	// pushes them back out, so an output pixel near the corner must be
	// reading from further in than where it sits.
	s := checkerScene(101, 101, 10)
	wp := newWarp(s, Edit{Distortion: 100})
	wp.fit = 1                       // measure the lens model, not the zoom that compensates for it
	px, py, r := wp.source(0.9, 0.5) // right of centre, on the horizontal
	if r <= 0 {
		t.Fatal("radius should be positive off-centre")
	}
	if px >= 0.9*float64(s.W) {
		t.Errorf("corrected pixel at 0.9 reads source x=%v; barrel correction should "+
			"sample nearer the centre", px)
	}
	if math.Abs(py-50) > 0.5 {
		t.Errorf("a point on the horizontal axis moved vertically to %v", py)
	}
	// The centre is the one point a radial correction never moves.
	cx, cy, cr := wp.source(0.5, 0.5)
	if math.Abs(cx-50) > 0.01 || math.Abs(cy-50) > 0.01 || cr > 0.01 {
		t.Errorf("centre moved to (%v,%v) at radius %v", cx, cy, cr)
	}
	// Pincushion is the same correction the other way.
	wpIn := newWarp(s, Edit{Distortion: -100})
	wpIn.fit = 1
	pxIn, _, _ := wpIn.source(0.9, 0.5)
	if pxIn <= px {
		t.Errorf("negative distortion sampled at %v, want further out than %v", pxIn, px)
	}
}

func TestVignetteBrightensCornersNotTheCentre(t *testing.T) {
	if g := vignetteGain(1, 0); math.Abs(g-1) > 1e-9 {
		t.Errorf("centre gain = %v, want exactly 1", g)
	}
	corner := vignetteGain(1, 1)
	if corner < 2 || corner > 3 {
		t.Errorf("corner gain = %v, want roughly a stop and a bit", corner)
	}
	if mid := vignetteGain(1, 0.5); mid <= 1 || mid >= corner {
		t.Errorf("mid gain = %v, want between 1 and %v", mid, corner)
	}
	// Negative adds a vignette rather than removing one, and never to black.
	if dark := vignetteGain(-1, 1); dark >= 1 || dark <= 0 {
		t.Errorf("negative corner gain = %v, want a positive value under 1", dark)
	}
	if floor := vignetteGain(-100, 1); floor <= 0 {
		t.Errorf("gain floor = %v, want it clamped above zero", floor)
	}
}

func TestVignetteReachesThePixels(t *testing.T) {
	s := checkerScene(80, 80, 40)
	flat := &Scene{W: 80, H: 80, Pix: make([]float32, 80*80*3)}
	for i := range flat.Pix {
		flat.Pix[i] = 0.3
	}
	g := applyGeometry(flat, Edit{Vignette: 100})
	centre := g.Pix[((40)*80+40)*3]
	corner := g.Pix[(1*80+1)*3]
	if math.Abs(float64(centre-0.3)) > 0.01 {
		t.Errorf("centre changed to %v, want it left at 0.3", centre)
	}
	if corner <= centre*1.5 {
		t.Errorf("corner = %v against centre %v; correction did not reach the corners",
			corner, centre)
	}
	_ = s
}

func TestSamplerIsExactOnWholePixels(t *testing.T) {
	// At an integer coordinate the cubic must return that pixel and
	// nothing else. Half a pixel out here would shift and soften every
	// frame that went through geometry, and would look like nothing.
	s := checkerScene(40, 40, 5)
	var got [3]float32
	for y := 0; y < 40; y++ {
		for x := 0; x < 40; x++ {
			sampleCatmullRom(s, float64(x), float64(y), got[:])
			want := s.Pix[(y*40+x)*3]
			if math.Abs(float64(got[0]-want)) > 1e-5 {
				t.Fatalf("sample at (%d,%d) = %v, want %v", x, y, got[0], want)
			}
		}
	}
}

func TestCropAlignsToWholePixels(t *testing.T) {
	// A crop on pixel boundaries is a copy, not an interpolation. If the
	// coordinate convention is half a pixel out, this blurs.
	s := checkerScene(120, 120, 12)
	g := applyGeometry(s, Edit{CropX: 0.25, CropY: 0.25, CropW: 0.5, CropH: 0.5})
	if g.W != 60 || g.H != 60 {
		t.Fatalf("crop is %dx%d, want 60x60", g.W, g.H)
	}
	worst := 0.0
	for y := 0; y < 60; y++ {
		for x := 0; x < 60; x++ {
			d := math.Abs(float64(g.Pix[(y*60+x)*3] - s.Pix[((y+30)*120+x+30)*3]))
			if d > worst {
				worst = d
			}
		}
	}
	if worst > 1e-4 {
		t.Errorf("a pixel-aligned crop changed pixels by up to %v; the sampling grid is off",
			worst)
	}
}

func TestGeometrySurvivesExtremes(t *testing.T) {
	// Sidecars are editable by hand and sliders reach their stops; none of
	// these should panic or produce a frame with nothing in it.
	s := checkerScene(64, 64, 8)
	for _, e := range []Edit{
		{Rotate: 45, Distortion: 100, Vignette: 100},
		{Rotate: -45, Distortion: -100, Vignette: -100},
		{CropX: 0.99, CropY: 0.99, CropW: 0.99, CropH: 0.99},
		{CropX: -5, CropY: -5, CropW: 99, CropH: 99},
		{Rotate: 1e6, Distortion: 1e6},
		{Rotate: math.NaN(), Distortion: math.NaN(), CropW: math.NaN()},
	} {
		g := applyGeometry(s, e.Clamp())
		if g.W < 1 || g.H < 1 || len(g.Pix) != g.W*g.H*3 {
			t.Fatalf("%+v produced a %dx%d frame with %d values", e, g.W, g.H, len(g.Pix))
		}
		for _, v := range g.Pix {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				t.Fatalf("%+v produced a non-finite pixel", e)
			}
		}
	}
}

func TestRenderAppliesGeometryEndToEnd(t *testing.T) {
	s := checkerScene(80, 60, 10)
	full := Render(s, Edit{})
	if b := full.Bounds(); b.Dx() != 80 || b.Dy() != 60 {
		t.Fatalf("uncropped render is %dx%d, want 80x60", b.Dx(), b.Dy())
	}
	cropped := Render(s, Edit{CropX: 0.25, CropY: 0.25, CropW: 0.5, CropH: 0.5})
	if b := cropped.Bounds(); b.Dx() != 40 || b.Dy() != 30 {
		t.Errorf("cropped render is %dx%d, want 40x30", b.Dx(), b.Dy())
	}
	// The crop tool needs the frame it is cutting from, not the offcut.
	unc := RenderUncropped(s, Edit{CropX: 0.25, CropY: 0.25, CropW: 0.5, CropH: 0.5})
	if b := unc.Bounds(); b.Dx() != 80 || b.Dy() != 60 {
		t.Errorf("uncropped render is %dx%d, want the full 80x60", b.Dx(), b.Dy())
	}
	// RenderInPlace must agree with Render, geometry and all.
	a := Render(checkerScene(80, 60, 10), Edit{Rotate: 5, Exposure: 1})
	b := RenderInPlace(checkerScene(80, 60, 10), Edit{Rotate: 5, Exposure: 1})
	if a.Bounds() != b.Bounds() {
		t.Fatalf("in-place render is %v, want %v", b.Bounds(), a.Bounds())
	}
	for i := range a.Pix {
		if a.Pix[i] != b.Pix[i] {
			t.Fatalf("in-place render differs at byte %d: %d vs %d", i, b.Pix[i], a.Pix[i])
		}
	}
}
