package develop

import (
	"bytes"
	"image"
	"image/jpeg"
	"math"
	"testing"

	"github.com/shaumik/qk-photo-viewer/internal/raw"
)

// colourfulMosaic builds a sensor frame of several distinct colours. Varied
// colour matters here: a flat grey patch is degenerate, because any balance
// that makes it neutral fits it perfectly.
func colourfulMosaic(w, h int, wb [3]float64) *raw.Image {
	im := &raw.Image{
		Width: w, Height: h,
		Data:      make([]uint16, w*h),
		CFA:       [4]uint8{raw.Red, raw.Green, raw.Green, raw.Blue},
		White:     1000,
		WB:        wb,
		WBSource:  raw.WBFromCamera,
		CamToSRGB: [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1},
	}
	// Patches of scene colour, in the sensor's own (unbalanced) space: the
	// balance is what has to be divided back out to get here.
	patches := [][3]float64{
		{0.45, 0.55, 0.40}, {0.70, 0.40, 0.20}, {0.20, 0.35, 0.65},
		{0.55, 0.60, 0.55}, {0.30, 0.55, 0.30}, {0.65, 0.55, 0.25},
		{0.25, 0.25, 0.30}, {0.60, 0.62, 0.58}, {0.40, 0.30, 0.50},
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			p := patches[(y*3/h)*3+(x*3/w)]
			// Undo the balance so that applying it again lands on the scene.
			v := p[im.At(x, y)] / wb[im.At(x, y)]
			im.Data[y*w+x] = uint16(math.Max(0, math.Min(1, v)) * 1000)
		}
	}
	return im
}

// cameraRendering is what the camera would have embedded: the same scene,
// balanced and through a transfer curve, as a JPEG.
func cameraRendering(im *raw.Image, wb [3]float64) []byte {
	s := FromRAWImage(&raw.Image{
		Width: im.Width, Height: im.Height, Data: im.Data, CFA: im.CFA,
		White: im.White, WB: wb, CamToSRGB: im.CamToSRGB, Orientation: 1,
	}, 0)
	img := Render(s, Edit{})
	var b bytes.Buffer
	jpeg.Encode(&b, img, &jpeg.Options{Quality: 92})
	return b.Bytes()
}

func TestFitRecoversTheBalanceTheCameraUsed(t *testing.T) {
	// The camera's own rendering is the answer written down in a form
	// anyone can read. Given it, the multipliers should be recoverable.
	want := [3]float64{2.4, 1, 1.7}
	im := colourfulMosaic(96, 96, want)
	ref := cameraRendering(im, want)

	im.WB, im.WBSource = [3]float64{2.0, 1, 1.7}, raw.WBDefault
	got, fitErr, ok := FitWhiteBalance(im, ref)
	if !ok {
		t.Fatal("fit gave up on a frame it should manage")
	}
	if math.Abs(got[0]-want[0]) > 0.25 || math.Abs(got[2]-want[2]) > 0.25 {
		t.Errorf("fitted [%.3f 1 %.3f], want about [%.2f 1 %.2f]", got[0], got[2], want[0], want[2])
	}
	if got[1] != 1 {
		t.Errorf("green should stay the reference, got %v", got[1])
	}
	if fitErr > 0.05 {
		t.Errorf("fit error %v is high for a frame with no tone curve mismatch", fitErr)
	}
}

func TestFittedBalanceBeatsAWrongOne(t *testing.T) {
	// The caller keeps whichever answer scores better, so the scoring has
	// to actually rank a right answer above a wrong one.
	want := [3]float64{2.4, 1, 1.7}
	im := colourfulMosaic(96, 96, want)
	ref := cameraRendering(im, want)

	right := ScoreWhiteBalance(im, ref, want)
	none := ScoreWhiteBalance(im, ref, [3]float64{1, 1, 1})
	wrong := ScoreWhiteBalance(im, ref, [3]float64{1.2, 1, 3.5})
	t.Logf("scores: correct=%.4f  none=%.4f  wrong=%.4f", right, none, wrong)
	if right >= none {
		t.Errorf("the correct balance (%v) did not beat no balance (%v)", right, none)
	}
	if right >= wrong {
		t.Errorf("the correct balance (%v) did not beat a wrong one (%v)", right, wrong)
	}
}

func TestFitDeclinesWhenItCannotTell(t *testing.T) {
	im := colourfulMosaic(64, 64, [3]float64{2, 1, 1.6})
	for name, ref := range map[string][]byte{
		"not a jpeg": []byte("nonsense"),
		"empty":      nil,
	} {
		if _, _, ok := FitWhiteBalance(im, ref); ok {
			t.Errorf("%s: fit claimed success", name)
		}
	}
	// A frame with nothing in it has no colour to match on.
	black := image.NewRGBA(image.Rect(0, 0, 64, 64))
	var b bytes.Buffer
	jpeg.Encode(&b, black, nil)
	if _, _, ok := FitWhiteBalance(im, b.Bytes()); ok {
		t.Error("a black reference should not produce a confident answer")
	}
}

func TestFitHandlesADifferentlyCroppedReference(t *testing.T) {
	// A body set to shoot 16:9 crops its own JPEG and leaves the RAW at the
	// sensor's full shape, so the two pictures are not the same rectangle.
	want := [3]float64{2.3, 1, 1.9}
	im := colourfulMosaic(120, 80, want)
	full := cameraRendering(im, want)
	src, err := jpeg.Decode(bytes.NewReader(full))
	if err != nil {
		t.Fatal(err)
	}
	// Centre-crop the reference to 16:9, as the camera would.
	ch := 120 * 9 / 16
	y0 := (80 - ch) / 2
	wide := image.NewRGBA(image.Rect(0, 0, 120, ch))
	for y := 0; y < ch; y++ {
		for x := 0; x < 120; x++ {
			wide.Set(x, y, src.At(x, y0+y))
		}
	}
	var b bytes.Buffer
	jpeg.Encode(&b, wide, &jpeg.Options{Quality: 92})

	im.WB, im.WBSource = [3]float64{2.0, 1, 1.7}, raw.WBDefault
	got, _, ok := FitWhiteBalance(im, b.Bytes())
	if !ok {
		t.Fatal("fit gave up on a cropped reference")
	}
	if math.Abs(got[0]-want[0]) > 0.35 || math.Abs(got[2]-want[2]) > 0.35 {
		t.Errorf("fitted [%.3f 1 %.3f] from a 16:9 reference, want about [%.2f 1 %.2f]",
			got[0], got[2], want[0], want[2])
	}
}
