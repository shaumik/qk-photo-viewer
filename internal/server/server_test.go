package server

import (
	"bytes"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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

func TestOpenFolderDTOs(t *testing.T) {
	dir, _ := shoot(t)
	s := New()
	dtos, err := s.OpenFolder(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(dtos) != 3 {
		t.Fatalf("got %d photos, want 3: %+v", len(dtos), dtos)
	}
	if dtos[0].Pair != "ARW +JPG" || dtos[2].Pair != "ARW" {
		t.Errorf("pair labels wrong: %+v", dtos)
	}
	if dtos[0].Name != "DSC00010.JPG" || dtos[2].Name != "DSC00012.ARW" {
		t.Errorf("display names wrong: %+v", dtos)
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

func TestCommitRemovesFromListAndAPI(t *testing.T) {
	dir, _ := shoot(t)
	s := New()
	if _, err := s.OpenFolder(dir); err != nil {
		t.Fatal(err)
	}
	moved, err := s.CommitRejects([]string{"DSC00010"})
	if err != nil {
		t.Fatal(err)
	}
	if moved != 2 {
		t.Errorf("moved %d files, want 2 (the whole pair)", moved)
	}
	if code, _ := get(t, s, "/api/preview/DSC00010"); code != 404 {
		t.Errorf("committed photo should 404, got HTTP %d", code)
	}
	if code, _ := get(t, s, "/api/preview/DSC00011"); code != 200 {
		t.Errorf("keeper should still serve, got HTTP %d", code)
	}
}

func TestCommitWithoutFolderErrors(t *testing.T) {
	if _, err := New().CommitRejects([]string{"X"}); err == nil {
		t.Error("commit with no folder open should error")
	}
}
