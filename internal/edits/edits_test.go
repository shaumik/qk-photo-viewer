package edits

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/shaumik/qk-photo-viewer/internal/develop"
)

func touch(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("photo"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSidecarSitsBesideThePhoto(t *testing.T) {
	dir := t.TempDir()
	arw := touch(t, filepath.Join(dir, "DSC04810.ARW"))
	s := New(dir, false)

	want := develop.Edit{Exposure: 0.75, Shadows: 30, Contrast: 12}
	if err := s.Set(arw, want); err != nil {
		t.Fatalf("Set: %v", err)
	}
	sidecar := filepath.Join(dir, "DSC04810"+Suffix)
	if _, err := os.Stat(sidecar); err != nil {
		t.Fatalf("no sidecar beside the photo: %v", err)
	}
	// The original must be untouched — that is the whole promise.
	if b, _ := os.ReadFile(arw); string(b) != "photo" {
		t.Errorf("the RAW was modified: %q", b)
	}

	// A fresh store reads it back.
	s2 := New(dir, false)
	s2.Preload([]string{arw})
	got, ok := s2.Get(arw)
	if !ok || got != want {
		t.Errorf("reloaded %+v (found %v), want %+v", got, ok, want)
	}
}

func TestPairSharesOneEdit(t *testing.T) {
	// A RAW and its camera JPEG are one photo, so they are one edit.
	dir := t.TempDir()
	arw := touch(t, filepath.Join(dir, "DSC04810.ARW"))
	jpg := touch(t, filepath.Join(dir, "DSC04810.JPG"))
	s := New(dir, false)
	if err := s.Set(arw, develop.Edit{Exposure: 1}); err != nil {
		t.Fatal(err)
	}
	if got, ok := s.Get(jpg); !ok || got.Exposure != 1 {
		t.Errorf("the JPEG half of the pair sees %+v (found %v), want the same edit", got, ok)
	}
	entries, _ := os.ReadDir(dir)
	n := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("wrote %d sidecars for one photo, want 1", n)
	}
}

func TestZeroEditRemovesTheSidecar(t *testing.T) {
	dir := t.TempDir()
	arw := touch(t, filepath.Join(dir, "DSC04810.ARW"))
	s := New(dir, false)
	s.Set(arw, develop.Edit{Exposure: 1})
	if err := s.Set(arw, develop.Edit{}); err != nil {
		t.Fatalf("Set to zero: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "DSC04810"+Suffix)); !os.IsNotExist(err) {
		t.Error("an as-shot photo should leave no sidecar behind")
	}
	if err := s.Reset(arw); err != nil {
		t.Errorf("Reset on a photo with no sidecar should succeed, got %v", err)
	}
	if _, ok := s.Get(arw); ok {
		t.Error("Reset should forget the edit")
	}
}

func TestLockedCardFallsBackToAppSupport(t *testing.T) {
	// A card with its lock switch on must still be editable.
	dir := t.TempDir()
	arw := touch(t, filepath.Join(dir, "DSC04810.ARW"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	s := New(dir, true)
	want := develop.Edit{Exposure: -0.5, Highlights: -40}
	if err := s.Set(arw, want); err != nil {
		t.Fatalf("Set on a read-only card: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "DSC04810"+Suffix)); !os.IsNotExist(err) {
		t.Error("nothing should have been written to the card")
	}
	s2 := New(dir, true)
	s2.Preload([]string{arw})
	if got, ok := s2.Get(arw); !ok || got != want {
		t.Errorf("reloaded %+v (found %v), want %+v from the backup", got, ok, want)
	}
}

func TestSidecarBesideThePhotoWinsOverTheBackup(t *testing.T) {
	dir := t.TempDir()
	arw := touch(t, filepath.Join(dir, "DSC04810.ARW"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	locked := New(dir, true)
	locked.Set(arw, develop.Edit{Exposure: -2}) // written to the backup
	unlocked := New(dir, false)
	unlocked.Set(arw, develop.Edit{Exposure: 2}) // written to the card

	fresh := New(dir, false)
	fresh.Preload([]string{arw})
	if got, _ := fresh.Get(arw); got.Exposure != 2 {
		t.Errorf("exposure = %v, want the sidecar on the card to win", got.Exposure)
	}
}

func TestNestedFoldersDoNotCollideInTheBackup(t *testing.T) {
	// Cards roll filenames over between folders; two DSC00001s must keep
	// their own edits even in the flat backup directory.
	dir := t.TempDir()
	a := touch(t, filepath.Join(dir, "100MSDCF", "DSC00001.ARW"))
	b := touch(t, filepath.Join(dir, "101MSDCF", "DSC00001.ARW"))
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	s := New(dir, true)
	s.Set(a, develop.Edit{Exposure: 1})
	s.Set(b, develop.Edit{Exposure: -1})

	s2 := New(dir, true)
	s2.Preload([]string{a, b})
	ea, _ := s2.Get(a)
	eb, _ := s2.Get(b)
	if ea.Exposure != 1 || eb.Exposure != -1 {
		t.Errorf("got %v and %v, want 1 and -1 kept apart", ea.Exposure, eb.Exposure)
	}
}

func TestPreloadIgnoresRubbishAndStrangers(t *testing.T) {
	dir := t.TempDir()
	arw := touch(t, filepath.Join(dir, "DSC04810.ARW"))
	other := touch(t, filepath.Join(dir, "DSC04811.ARW"))

	// Corrupt, wrong version, and belonging to a photo that is not here.
	os.WriteFile(filepath.Join(dir, "DSC04810"+Suffix), []byte("{not json"), 0o644)
	os.WriteFile(filepath.Join(dir, "DSC04811"+Suffix),
		[]byte(`{"version":99,"edit":{"exposure":3}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "DSC09999"+Suffix),
		[]byte(`{"version":1,"edit":{"exposure":3}}`), 0o644)

	s := New(dir, false)
	s.Preload([]string{arw, other})
	if _, ok := s.Get(arw); ok {
		t.Error("a corrupt sidecar should read as no edit at all")
	}
	if _, ok := s.Get(other); ok {
		t.Error("a sidecar from a future version should be ignored")
	}
	if len(s.Edited()) != 0 {
		t.Errorf("Edited() = %v, want nothing", s.Edited())
	}
}

func TestEditedTracksNonDefaultEdits(t *testing.T) {
	dir := t.TempDir()
	a := touch(t, filepath.Join(dir, "A.ARW"))
	b := touch(t, filepath.Join(dir, "B.ARW"))
	s := New(dir, false)
	s.Set(a, develop.Edit{Contrast: 20})
	s.Set(b, develop.Edit{})

	ed := s.Edited()
	if !ed[Key(a)] {
		t.Error("an edited photo should be listed")
	}
	if ed[Key(b)] {
		t.Error("an as-shot photo should not be listed")
	}
}

func TestSidecarIsReadableJSON(t *testing.T) {
	// The sidecar is a file a person may well open. It should make sense.
	dir := t.TempDir()
	arw := touch(t, filepath.Join(dir, "DSC04810.ARW"))
	New(dir, false).Set(arw, develop.Edit{Exposure: 0.5, Vibrance: 20})

	data, err := os.ReadFile(filepath.Join(dir, "DSC04810"+Suffix))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("sidecar is not valid JSON: %v", err)
	}
	if raw["version"] != float64(sidecarVersion) {
		t.Errorf("version = %v, want %d", raw["version"], sidecarVersion)
	}
	edit, ok := raw["edit"].(map[string]any)
	if !ok || edit["exposure"] != 0.5 {
		t.Errorf("edit block = %v, want a readable exposure of 0.5", raw["edit"])
	}
}

func TestOutOfRangeSidecarValuesAreClamped(t *testing.T) {
	// Sidecars are plain files; a hand-edited one must not reach the
	// pipeline with nonsense in it.
	dir := t.TempDir()
	arw := touch(t, filepath.Join(dir, "DSC04810.ARW"))
	os.WriteFile(filepath.Join(dir, "DSC04810"+Suffix),
		[]byte(`{"version":1,"edit":{"exposure":9000,"temp":-9000}}`), 0o644)

	s := New(dir, false)
	s.Preload([]string{arw})
	got, ok := s.Get(arw)
	if !ok {
		t.Fatal("sidecar not loaded")
	}
	if got.Exposure != 5 || got.Temp != -100 {
		t.Errorf("loaded %+v, want the values clamped into range", got)
	}
}
