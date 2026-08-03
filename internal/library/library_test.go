package library

import (
	"os"
	"path/filepath"
	"testing"
)

func touch(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(name), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestScanPairsAndSorts(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "DSC04812.JPG") // deliberately out of order
	touch(t, dir, "DSC04810.ARW")
	touch(t, dir, "DSC04810.JPG")
	touch(t, dir, "DSC04811.ARW")
	touch(t, dir, ".DS_Store")    // hidden: ignored
	touch(t, dir, "SOUND001.WAV") // wrong type: ignored
	if err := os.Mkdir(filepath.Join(dir, "SUBDIR"), 0o755); err != nil {
		t.Fatal(err)
	}

	photos, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(photos) != 3 {
		t.Fatalf("got %d photos, want 3: %+v", len(photos), photos)
	}
	if photos[0].ID != "DSC04810" || photos[1].ID != "DSC04811" || photos[2].ID != "DSC04812" {
		t.Fatalf("wrong order: %+v", photos)
	}
	if photos[0].Raw == "" || photos[0].Jpeg == "" {
		t.Fatalf("DSC04810 should be a RAW+JPEG pair: %+v", photos[0])
	}
	if photos[1].Raw == "" || photos[1].Jpeg != "" {
		t.Fatalf("DSC04811 should be RAW-only: %+v", photos[1])
	}
	if photos[2].Raw != "" || photos[2].Jpeg == "" {
		t.Fatalf("DSC04812 should be JPEG-only: %+v", photos[2])
	}
}

func TestScanDescendsIntoSubfolders(t *testing.T) {
	// Cameras roll to a new numbered folder every 9999 shots, and filenames
	// restart — the same DSC number can exist in both. Opening the DCIM
	// parent (or any organized folder) must find everything, with IDs kept
	// distinct by folder prefix.
	dir := t.TempDir()
	for _, sub := range []string{"100MSDCF", "101MSDCF", "keepers"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	touch(t, filepath.Join(dir, "100MSDCF"), "DSC09999.ARW")
	touch(t, filepath.Join(dir, "101MSDCF"), "DSC09999.ARW")
	touch(t, filepath.Join(dir, "keepers"), "DSC00001.ARW")

	photos, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(photos) != 3 {
		t.Fatalf("got %d photos, want 3: %+v", len(photos), photos)
	}
	if photos[0].ID != "100MSDCF:DSC09999" || photos[1].ID != "101MSDCF:DSC09999" ||
		photos[2].ID != "keepers:DSC00001" {
		t.Errorf("IDs should be folder-prefixed and sorted: %+v", photos)
	}
}

func TestScanDepthLimitAndRejectsFolder(t *testing.T) {
	dir := t.TempDir()
	// Card-root shape: root/DCIM/100MSDCF/photo — three levels, included.
	deep := filepath.Join(dir, "DCIM", "100MSDCF")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	touch(t, deep, "DSC00001.ARW")
	// Third folder level: still in scope.
	third := filepath.Join(deep, "extra")
	if err := os.MkdirAll(third, 0o755); err != nil {
		t.Fatal(err)
	}
	touch(t, third, "DSC00002.ARW")
	// Fourth level: past the limit, ignored.
	tooDeep := filepath.Join(third, "deeper")
	if err := os.MkdirAll(tooDeep, 0o755); err != nil {
		t.Fatal(err)
	}
	touch(t, tooDeep, "DSC00004.ARW")
	// Previously committed rejects must never resurface in a scan.
	rej := filepath.Join(dir, RejectsDirName)
	if err := os.MkdirAll(rej, 0o755); err != nil {
		t.Fatal(err)
	}
	touch(t, rej, "DSC00003.ARW")

	photos, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(photos) != 2 || photos[0].ID != "DCIM:100MSDCF:DSC00001" ||
		photos[1].ID != "DCIM:100MSDCF:extra:DSC00002" {
		t.Fatalf("want the two in-scope photos, got: %+v", photos)
	}
}

func TestPairLabels(t *testing.T) {
	cases := []struct {
		p    Photo
		want string
	}{
		{Photo{Raw: "a/DSC1.ARW", Jpeg: "a/DSC1.JPG"}, "ARW +JPG"},
		{Photo{Raw: "a/DSC1.ARW"}, "ARW"},
		{Photo{Jpeg: "a/DSC1.JPG"}, "JPG"},
	}
	for _, c := range cases {
		if got := c.p.Pair(); got != c.want {
			t.Errorf("Pair() = %q, want %q", got, c.want)
		}
	}
}

func TestCommitRejectsMovesWholePair(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "DSC04810.ARW")
	touch(t, dir, "DSC04810.JPG")
	touch(t, dir, "DSC04811.ARW")

	photos, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	moved, err := CommitRejects(dir, photos[:1]) // reject the pair
	if err != nil {
		t.Fatal(err)
	}
	if moved != 2 {
		t.Fatalf("moved %d files, want 2 (both halves of the pair)", moved)
	}
	for _, gone := range []string{"DSC04810.ARW", "DSC04810.JPG"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s should have left the shoot folder", gone)
		}
		if _, err := os.Stat(filepath.Join(dir, RejectsDirName, gone)); err != nil {
			t.Errorf("%s should be in the rejects folder: %v", gone, err)
		}
	}

	// keeper untouched, and a re-scan no longer lists the rejected pair
	after, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].ID != "DSC04811" {
		t.Fatalf("re-scan should list only the keeper: %+v", after)
	}
}

func TestCommitRejectsCollisionGetsSuffix(t *testing.T) {
	dir := t.TempDir()
	touch(t, dir, "DSC04810.ARW")
	if _, err := CommitRejects(dir, []Photo{{ID: "DSC04810", Raw: filepath.Join(dir, "DSC04810.ARW")}}); err != nil {
		t.Fatal(err)
	}
	// same name rejected again (e.g. camera reused numbering after a format)
	touch(t, dir, "DSC04810.ARW")
	if _, err := CommitRejects(dir, []Photo{{ID: "DSC04810", Raw: filepath.Join(dir, "DSC04810.ARW")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, RejectsDirName, "DSC04810-1.ARW")); err != nil {
		t.Errorf("collision should get a -1 suffix, not overwrite: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, RejectsDirName, "DSC04810.ARW")); err != nil {
		t.Errorf("original reject should still exist: %v", err)
	}
}

func TestCommitRejectsEmptyIsNoop(t *testing.T) {
	dir := t.TempDir()
	moved, err := CommitRejects(dir, nil)
	if err != nil || moved != 0 {
		t.Fatalf("empty commit: moved=%d err=%v", moved, err)
	}
	if _, err := os.Stat(filepath.Join(dir, RejectsDirName)); !os.IsNotExist(err) {
		t.Error("empty commit should not create the rejects folder")
	}
}
