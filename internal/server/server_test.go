package server

import (
	"bytes"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/shaumik/qk-photo-viewer/internal/library"
	"github.com/shaumik/qk-photo-viewer/internal/preview/previewtest"
)

// shoot builds a folder with two synthetic ARW+JPG pairs and one RAW-only
// shot, returning the folder and the preview bytes keyed by photo ID.
func shoot(t *testing.T) (string, map[string][]byte) {
	t.Helper()
	dir := t.TempDir()
	previews := map[string][]byte{}

	// pairs: the camera JPEG is the display file
	for i, id := range []string{"DSC00010", "DSC00011"} {
		jpg := previewtest.JPEGWithExifThumb(previewtest.JPEGBlob(200, byte(i+1)), 3000)
		if err := os.WriteFile(filepath.Join(dir, id+".JPG"), jpg, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := previewtest.WriteARW(filepath.Join(dir, id+".ARW"),
			previewtest.JPEGBlob(200, 0xAA), previewtest.JPEGBlob(4000, 0xBB)); err != nil {
			t.Fatal(err)
		}
		previews[id] = jpg // a JPEG shot is its own preview
	}

	// RAW-only: the embedded preview is the display image
	arwPrev := previewtest.JPEGBlob(5000, 0xCC)
	if err := previewtest.WriteARW(filepath.Join(dir, "DSC00012.ARW"),
		previewtest.JPEGBlob(200, 0xDD), arwPrev); err != nil {
		t.Fatal(err)
	}
	previews["DSC00012"] = arwPrev
	return dir, previews
}

func get(t *testing.T, s *Service, url string) (int, []byte) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
	body, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, body
}

func TestOpenFolderResult(t *testing.T) {
	dir, _ := shoot(t)
	s := New()
	res, err := s.OpenFolder(dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Dir != dir {
		t.Errorf("Dir = %q, want %q", res.Dir, dir)
	}
	if len(res.Photos) != 3 {
		t.Fatalf("got %d photos, want 3: %+v", len(res.Photos), res.Photos)
	}
	if res.Photos[0].Pair != "ARW +JPG" || res.Photos[2].Pair != "ARW" {
		t.Errorf("pair labels wrong: %+v", res.Photos)
	}
	if res.Photos[0].Name != "DSC00010.JPG" || res.Photos[2].Name != "DSC00012.ARW" {
		t.Errorf("display names wrong: %+v", res.Photos)
	}
	if os.Getuid() != 0 && res.ReadOnly {
		t.Error("a writable temp dir should not report read-only")
	}
}

func TestReadOnlyDetection(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root writes anywhere; read-only probing is meaningless")
	}
	dir, _ := shoot(t)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })
	res, err := New().OpenFolder(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !res.ReadOnly {
		t.Error("a read-only folder should be reported as such")
	}
}

func TestServesPreviewsAndThumbs(t *testing.T) {
	dir, previews := shoot(t)
	s := New()
	if _, err := s.OpenFolder(dir); err != nil {
		t.Fatal(err)
	}

	for id, want := range previews {
		code, body := get(t, s, "/api/preview/"+id)
		if code != 200 {
			t.Fatalf("preview %s: HTTP %d: %s", id, code, body)
		}
		if !bytes.Equal(body, want) {
			t.Errorf("preview %s: got %d bytes, want %d", id, len(body), len(want))
		}
		code, body = get(t, s, "/api/thumb/"+id)
		if code != 200 || len(body) < 4 || body[0] != 0xFF || body[1] != 0xD8 {
			t.Errorf("thumb %s: HTTP %d, %d bytes — want a JPEG", id, code, len(body))
		}
	}
}

func TestUnknownIDIs404(t *testing.T) {
	dir, _ := shoot(t)
	s := New()
	if _, err := s.OpenFolder(dir); err != nil {
		t.Fatal(err)
	}
	if code, _ := get(t, s, "/api/preview/NOPE"); code != 404 {
		t.Errorf("unknown id: HTTP %d, want 404", code)
	}
}

func TestCommitMovesWholePairAndReports(t *testing.T) {
	dir, _ := shoot(t)
	s := New()
	if _, err := s.OpenFolder(dir); err != nil {
		t.Fatal(err)
	}
	res, err := s.CommitRejects([]string{"DSC00010"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.MovedIDs) != 1 || res.MovedIDs[0] != "DSC00010" {
		t.Errorf("MovedIDs = %v, want [DSC00010]", res.MovedIDs)
	}
	// Off macOS the trash is unsupported, so the fallback destination is used.
	if res.Dest != library.RejectsDirName {
		t.Errorf("Dest = %q, want %q", res.Dest, library.RejectsDirName)
	}
	if len(res.Errors) != 0 {
		t.Errorf("unexpected errors: %v", res.Errors)
	}
	for _, f := range []string{"DSC00010.ARW", "DSC00010.JPG"} {
		if _, err := os.Stat(filepath.Join(dir, library.RejectsDirName, f)); err != nil {
			t.Errorf("%s should be in the rejects folder: %v", f, err)
		}
	}
	if code, _ := get(t, s, "/api/preview/DSC00010"); code != 404 {
		t.Errorf("committed photo should 404, got HTTP %d", code)
	}
	if code, _ := get(t, s, "/api/preview/DSC00011"); code != 200 {
		t.Errorf("keeper should still serve, got HTTP %d", code)
	}
}

func TestCommitPartialFailureKeepsBrokenPhotoListed(t *testing.T) {
	dir, _ := shoot(t)
	s := New()
	if _, err := s.OpenFolder(dir); err != nil {
		t.Fatal(err)
	}
	// Simulate the card dying under one photo: its files vanish pre-commit.
	os.Remove(filepath.Join(dir, "DSC00011.ARW"))
	os.Remove(filepath.Join(dir, "DSC00011.JPG"))

	res, err := s.CommitRejects([]string{"DSC00010", "DSC00011"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.MovedIDs) != 1 || res.MovedIDs[0] != "DSC00010" {
		t.Errorf("MovedIDs = %v, want only the healthy photo", res.MovedIDs)
	}
	if len(res.Errors) == 0 {
		t.Error("the vanished photo's failure should be reported")
	}
	// The failed photo must stay in the session (500 on fetch, not 404 —
	// it is still tracked, its files are just unreadable right now).
	if code, _ := get(t, s, "/api/preview/DSC00011"); code == 404 {
		t.Error("failed photo should remain listed, not be dropped")
	}
}

func TestFolderPresentAndRescan(t *testing.T) {
	dir, _ := shoot(t)
	s := New()
	if _, err := s.OpenFolder(dir); err != nil {
		t.Fatal(err)
	}
	if !s.FolderPresent() {
		t.Error("folder should be present after open")
	}
	res, err := s.Rescan()
	if err != nil || len(res.Photos) != 3 {
		t.Fatalf("rescan: %+v, %v", res, err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if s.FolderPresent() {
		t.Error("folder should be reported missing after removal")
	}
	if _, err := s.Rescan(); err == nil {
		t.Error("rescan of a missing folder should error")
	}
}

func TestCommitWithoutFolderErrors(t *testing.T) {
	if _, err := New().CommitRejects([]string{"X"}); err == nil {
		t.Error("commit with no folder open should error")
	}
}
