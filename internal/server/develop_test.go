package server

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image/jpeg"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shaumik/qk-photo-viewer/internal/develop"
	"github.com/shaumik/qk-photo-viewer/internal/edits"
	"github.com/shaumik/qk-photo-viewer/internal/library"
	"github.com/shaumik/qk-photo-viewer/internal/preview/previewtest"
	"github.com/shaumik/qk-photo-viewer/internal/tiff"
	"github.com/shaumik/qk-photo-viewer/internal/tiff/tifftest"
)

/* ---------- fixtures ---------- */

const fixW, fixH = 64, 48

// realARW builds a synthetic but genuinely decodable ARW: an uncompressed
// 16-bit CFA mosaic holding a gradient, plus an embedded preview JPEG in a
// SubIFD the way a camera writes one.
func realARW(t *testing.T, dir, name string) string {
	t.Helper()
	sensor := make([]byte, fixW*fixH*2)
	for y := 0; y < fixH; y++ {
		for x := 0; x < fixW; x++ {
			// A dark ramp across the frame, so auto-develop has
			// something to correct.
			v := 600 + 900*x/fixW
			binary.LittleEndian.PutUint16(sensor[(y*fixW+x)*2:], uint16(v))
		}
	}
	b := tifftest.New()
	mosaic := b.AddBlob(sensor)
	prevJPEG := b.AddBlob(previewtest.RealJPEG(fixW, fixH, 3))
	root := b.AddIFD()
	sub := b.AddIFD()
	sub.BlobOffset(0x0201, prevJPEG).
		Long(0x0202, int64(len(previewtest.RealJPEG(fixW, fixH, 3))))
	root.ASCII(tiff.TagMake, "SONY").
		ASCII(tiff.TagModel, "ILCE-7M3").
		Short(tiff.TagPhotometric, tiff.PhotometricCFA).
		Short(tiff.TagImageWidth, fixW).
		Short(tiff.TagImageLength, fixH).
		Short(tiff.TagBitsPerSample, 16).
		Short(tiff.TagCompression, 1).
		Short(tiff.TagSamplesPerPixel, 1).
		Short(tiff.TagOrientation, 1).
		Byte(tiff.TagCFAPattern, 0, 1, 1, 2).
		BlobOffset(tiff.TagStripOffsets, mosaic).
		Long(tiff.TagStripByteCounts, int64(len(sensor))).
		Short(0x7310, 512, 512, 512, 512).
		Short(0x787F, 16300).
		SShort(0x7313, 2288, 1024, 1024, 1616).
		SubIFD(sub)

	path := filepath.Join(dir, name+".ARW")
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// editShoot is a folder with one decodable RAW and one JPEG-only shot, so
// both the RAW path and the preview fallback are exercised.
func editShoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	realARW(t, dir, "DSC00001")
	if err := os.WriteFile(filepath.Join(dir, "DSC00002.JPG"),
		previewtest.RealJPEG(fixW, fixH, 7), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func openShoot(t *testing.T) (*Service, string) {
	t.Helper()
	dir := editShoot(t)
	s := New()
	if _, err := s.OpenFolder(dir); err != nil {
		t.Fatalf("OpenFolder: %v", err)
	}
	return s, dir
}

func post(t *testing.T, s *Service, url string, body any) (int, []byte) {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", url, bytes.NewReader(b)))
	out, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, out
}

func developInfo(t *testing.T, s *Service, id string) DevelopInfo {
	t.Helper()
	code, body := get(t, s, "/api/developinfo/"+id)
	if code != 200 {
		t.Fatalf("developinfo %s: %d %s", id, code, body)
	}
	var info DevelopInfo
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatalf("developinfo %s: %v", id, err)
	}
	return info
}

/* ---------- tests ---------- */

func TestDevelopUsesTheSensorDataWhenItCan(t *testing.T) {
	s, _ := openShoot(t)

	raw := developInfo(t, s, "DSC00001")
	if raw.Source != "raw" {
		t.Errorf("a decodable ARW developed from %q, want raw", raw.Source)
	}
	if raw.Camera != "SONY ILCE-7M3" {
		t.Errorf("camera = %q, want SONY ILCE-7M3", raw.Camera)
	}
	if raw.Width != fixW/2 || raw.Height != fixH/2 {
		t.Errorf("preview scene is %dx%d, want half of %dx%d", raw.Width, raw.Height, fixW, fixH)
	}

	// A JPEG-only shot has no sensor data to reach for, and must say so
	// rather than pretend.
	jpg := developInfo(t, s, "DSC00002")
	if jpg.Source != "preview" {
		t.Errorf("a JPEG-only shot developed from %q, want preview", jpg.Source)
	}
	if jpg.Headroom != 0 {
		t.Errorf("headroom = %v on a camera JPEG, want 0", jpg.Headroom)
	}
}

func TestDevelopFallsBackRatherThanFailing(t *testing.T) {
	// An ARW whose compression we cannot decode must still be editable
	// through the camera's own preview.
	dir := t.TempDir()
	if err := previewtest.WriteARW(filepath.Join(dir, "DSC00099.ARW"),
		previewtest.RealJPEG(16, 16, 1), previewtest.RealJPEG(fixW, fixH, 2)); err != nil {
		t.Fatal(err)
	}
	s := New()
	if _, err := s.OpenFolder(dir); err != nil {
		t.Fatal(err)
	}
	info := developInfo(t, s, "DSC00099")
	if info.Source != "preview" {
		t.Errorf("source = %q, want the preview fallback", info.Source)
	}
	code, body := get(t, s, "/api/develop/DSC00099")
	if code != 200 {
		t.Fatalf("develop: %d %s", code, body)
	}
	if _, err := jpeg.Decode(bytes.NewReader(body)); err != nil {
		t.Errorf("fallback render is not a valid JPEG: %v", err)
	}
}

func TestDevelopRendersADecodableFrame(t *testing.T) {
	s, _ := openShoot(t)
	code, body := get(t, s, "/api/develop/DSC00001")
	if code != 200 {
		t.Fatalf("develop: %d %s", code, body)
	}
	img, err := jpeg.Decode(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("develop did not return a JPEG: %v", err)
	}
	if b := img.Bounds(); b.Dx() != fixW/2 || b.Dy() != fixH/2 {
		t.Errorf("rendered %dx%d, want %dx%d", b.Dx(), b.Dy(), fixW/2, fixH/2)
	}
	// The width hint bounds the render, for slider drags.
	_, small := get(t, s, "/api/develop/DSC00001?w=16")
	sm, err := jpeg.Decode(bytes.NewReader(small))
	if err != nil {
		t.Fatalf("reduced render did not decode: %v", err)
	}
	if sm.Bounds().Dx() > 16 {
		t.Errorf("w=16 gave a %dpx render", sm.Bounds().Dx())
	}
	if code, _ := get(t, s, "/api/develop/nope"); code != 404 {
		t.Errorf("unknown photo returned %d, want 404", code)
	}
}

func TestEditChangesTheRenderAndIsRemembered(t *testing.T) {
	s, dir := openShoot(t)
	before := mustRender(t, s, "DSC00001")

	code, body := post(t, s, "/api/edit", map[string]any{
		"id":   "DSC00001",
		"edit": develop.Edit{Exposure: 2, Contrast: 30},
	})
	if code != 200 {
		t.Fatalf("edit: %d %s", code, body)
	}
	var info DevelopInfo
	json.Unmarshal(body, &info)
	if !info.Edited || info.Edit.Exposure != 2 {
		t.Fatalf("edit returned %+v", info)
	}

	after := mustRender(t, s, "DSC00001")
	if bytes.Equal(before, after) {
		t.Error("the render did not change after a two-stop exposure lift")
	}
	if brightness(t, after) <= brightness(t, before) {
		t.Error("a positive exposure edit did not brighten the frame")
	}

	// The original file is untouched and the edit is on disk beside it.
	if _, err := os.Stat(filepath.Join(dir, "DSC00001"+edits.Suffix)); err != nil {
		t.Errorf("no sidecar written: %v", err)
	}

	// And a fresh session picks it up.
	s2 := New()
	res, err := s2.OpenFolder(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Edited) != 1 || res.Edited[0] != "DSC00001" {
		t.Errorf("reopened with edited = %v, want [DSC00001]", res.Edited)
	}
	if got := developInfo(t, s2, "DSC00001").Edit.Exposure; got != 2 {
		t.Errorf("reloaded exposure = %v, want 2", got)
	}
}

func TestOriginalFlagShowsTheUneditedFrame(t *testing.T) {
	s, _ := openShoot(t)
	plain := mustRender(t, s, "DSC00001")
	post(t, s, "/api/edit", map[string]any{
		"id": "DSC00001", "edit": develop.Edit{Exposure: 3},
	})
	_, original := get(t, s, "/api/develop/DSC00001?original=1")
	if !bytes.Equal(plain, original) {
		t.Error("the before half of a before/after should match the unedited render")
	}
	if bytes.Equal(original, mustRender(t, s, "DSC00001")) {
		t.Error("before and after should differ once there is an edit")
	}
}

func TestAutoDevelopMovesTheSliders(t *testing.T) {
	s, _ := openShoot(t)
	code, body := post(t, s, "/api/edit", map[string]any{"id": "DSC00001", "action": "auto"})
	if code != 200 {
		t.Fatalf("auto: %d %s", code, body)
	}
	var info DevelopInfo
	json.Unmarshal(body, &info)
	if info.Edit.IsZero() {
		t.Fatal("auto-develop left every slider at zero on a dark frame")
	}
	if info.Edit.Exposure <= 0 {
		t.Errorf("exposure = %v, want a lift on this fixture", info.Edit.Exposure)
	}
	if !info.Edited {
		t.Error("auto-develop should mark the photo as edited")
	}

	// Reset puts it back.
	code, body = post(t, s, "/api/edit", map[string]any{"id": "DSC00001", "action": "reset"})
	if code != 200 {
		t.Fatalf("reset: %d %s", code, body)
	}
	json.Unmarshal(body, &info)
	if !info.Edit.IsZero() || info.Edited {
		t.Errorf("after reset: %+v, want as shot", info)
	}
	if got := developInfo(t, s, "DSC00001"); got.Edited {
		t.Error("reset did not stick")
	}
}

func TestEveryReplyDescribesTheSourceTheSameWay(t *testing.T) {
	// The panel decides what to tell the user from DevelopInfo.Source. A
	// reply that leaves it out reads as "the RAW could not be decoded" —
	// so every path that returns one has to fill it in.
	s, _ := openShoot(t)
	want := developInfo(t, s, "DSC00001")
	if want.Source != "raw" {
		t.Fatalf("fixture should decode as raw, got %q", want.Source)
	}

	replies := map[string][]byte{}
	_, replies["set"] = post(t, s, "/api/edit", map[string]any{
		"id": "DSC00001", "edit": develop.Edit{Exposure: 1},
	})
	_, replies["hold"] = post(t, s, "/api/edit", map[string]any{
		"id": "DSC00001", "edit": develop.Edit{Exposure: 1.2}, "hold": true,
	})
	_, replies["auto"] = post(t, s, "/api/edit", map[string]any{"id": "DSC00001", "action": "auto"})
	_, replies["reset"] = post(t, s, "/api/edit", map[string]any{"id": "DSC00001", "action": "reset"})

	for name, body := range replies {
		var got DevelopInfo
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.Source != want.Source {
			t.Errorf("%s returned source %q, want %q", name, got.Source, want.Source)
		}
		if got.Camera != want.Camera {
			t.Errorf("%s returned camera %q, want %q", name, got.Camera, want.Camera)
		}
		if got.Width != want.Width || got.Height != want.Height {
			t.Errorf("%s returned %dx%d, want %dx%d", name, got.Width, got.Height, want.Width, want.Height)
		}
	}
}

func TestEditsBroadcastToEveryScreen(t *testing.T) {
	s, _ := openShoot(t)
	ch, cancel := s.Subscribe()
	defer cancel()

	post(t, s, "/api/edit", map[string]any{
		"id": "DSC00001", "edit": develop.Edit{Vibrance: 25},
	})
	select {
	case e := <-ch:
		if e.Type != "edit" || e.ID != "DSC00001" {
			t.Fatalf("got event %+v, want an edit for DSC00001", e)
		}
		if e.Edit == nil || e.Edit.Vibrance != 25 {
			t.Errorf("event carried %+v, want the new edit", e.Edit)
		}
		if e.Tag == "" {
			t.Error("event carried no tag, so a phone cannot tell its frame is stale")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no edit event reached the other screens")
	}
}

func TestExportWritesAFullSizeJPEG(t *testing.T) {
	s, _ := openShoot(t)
	post(t, s, "/api/edit", map[string]any{
		"id": "DSC00001", "edit": develop.Edit{Exposure: 1.5},
	})

	res, err := s.ExportOne("DSC00001", "")
	if err != nil {
		t.Fatalf("ExportOne: %v", err)
	}
	if filepath.Base(res.Path) != "DSC00001.jpg" {
		t.Errorf("exported to %q, want DSC00001.jpg", res.Path)
	}
	if filepath.Base(res.Dir) != library.ExportDirName {
		t.Errorf("exported into %q, want %s", res.Dir, library.ExportDirName)
	}
	data, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("export is not a valid JPEG: %v", err)
	}
	// Export is full resolution, not the half-size preview scene.
	if b := img.Bounds(); b.Dx() != fixW || b.Dy() != fixH {
		t.Errorf("exported %dx%d, want the full %dx%d", b.Dx(), b.Dy(), fixW, fixH)
	}
	// And it carries the shooting data across.
	if !bytes.Contains(data[:min(len(data), 4096)], []byte("ILCE-7M3")) {
		t.Error("exported JPEG lost its camera model")
	}

	// Exports must not come back as photos on the next scan.
	res2, err := s.Rescan()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range res2.Photos {
		if strings.Contains(p.Name, "DSC00001.jpg") && len(res2.Photos) > 2 {
			t.Fatalf("the export was rescanned as a photo: %v", res2.Photos)
		}
	}
	if len(res2.Photos) != 2 {
		t.Errorf("rescan found %d photos, want the original 2", len(res2.Photos))
	}
}

func TestExportAllReportsProgress(t *testing.T) {
	s, _ := openShoot(t)

	ch, cancel := s.Subscribe()
	defer cancel()

	res, err := s.ExportAll(nil, "")
	if err != nil {
		t.Fatalf("ExportAll: %v", err)
	}
	if res.Count != 2 {
		t.Errorf("queued %d photos, want 2", res.Count)
	}

	deadline := time.After(20 * time.Second)
	for {
		select {
		case e := <-ch:
			if e.Type != "export" {
				continue
			}
			if !e.Finished {
				continue
			}
			if e.Failed != 0 {
				t.Errorf("%d exports failed", e.Failed)
			}
			if e.Done != 2 || e.Total != 2 {
				t.Errorf("finished at %d of %d, want 2 of 2", e.Done, e.Total)
			}
			entries, _ := os.ReadDir(e.Dest)
			if len(entries) != 2 {
				t.Errorf("export folder holds %d files, want 2", len(entries))
			}
			return
		case <-deadline:
			t.Fatal("export never reported finishing")
		}
	}
}

func TestExportGoesSomewhereWritableWhenTheCardIsLocked(t *testing.T) {
	s, _ := openShoot(t)
	s.mu.Lock()
	s.readOnly = true // as if the card's lock switch were on
	s.mu.Unlock()

	dir, err := s.exportDir("")
	if err != nil {
		t.Fatalf("exportDir: %v", err)
	}
	if strings.HasPrefix(dir, s.dir) {
		t.Errorf("export dir %q is on the locked card", dir)
	}
	if !strings.Contains(dir, "QK Export") {
		t.Errorf("export dir = %q, want somewhere under the user's pictures", dir)
	}
}

func TestEditingAnUnknownPhoto(t *testing.T) {
	s, _ := openShoot(t)
	for _, body := range []map[string]any{
		{"id": "nope", "edit": develop.Edit{}},
		{"id": "nope", "action": "auto"},
		{"id": "nope", "action": "reset"},
	} {
		if code, _ := post(t, s, "/api/edit", body); code == 200 {
			t.Errorf("%v was accepted for a photo that does not exist", body)
		}
	}
	if code, _ := post(t, s, "/api/edit", map[string]any{"id": "DSC00001"}); code != 400 {
		t.Error("an edit request with no edit and no action should be rejected")
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/edit", nil))
	if rec.Code != 405 {
		t.Errorf("GET /api/edit returned %d, want 405", rec.Code)
	}
}

func TestPairKeepsOneEdit(t *testing.T) {
	// A RAW and its camera JPEG are one photo in the viewer, so they are
	// one edit — and it is the RAW that gets developed.
	dir := t.TempDir()
	realARW(t, dir, "DSC00001")
	os.WriteFile(filepath.Join(dir, "DSC00001.JPG"), previewtest.RealJPEG(fixW, fixH, 4), 0o644)
	s := New()
	if _, err := s.OpenFolder(dir); err != nil {
		t.Fatal(err)
	}
	info := developInfo(t, s, "DSC00001")
	if info.Source != "raw" {
		t.Errorf("a pair developed from %q, want the RAW half", info.Source)
	}
	post(t, s, "/api/edit", map[string]any{"id": "DSC00001", "edit": develop.Edit{Exposure: 1}})
	matches, _ := filepath.Glob(filepath.Join(dir, "*"+edits.Suffix))
	if len(matches) != 1 {
		t.Errorf("wrote %d sidecars for one photo: %v", len(matches), matches)
	}
}

/* ---------- helpers ---------- */

func mustRender(t *testing.T, s *Service, id string) []byte {
	t.Helper()
	code, body := get(t, s, "/api/develop/"+id)
	if code != 200 {
		t.Fatalf("develop %s: %d %s", id, code, body)
	}
	return body
}

func brightness(t *testing.T, jpegBytes []byte) float64 {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(jpegBytes))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	b := img.Bounds()
	sum := 0.0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			sum += float64(r+g+bl) / 3
		}
	}
	return sum / float64(b.Dx()*b.Dy())
}
