package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMoveIntoCreatesDirAndAvoidsCollisions(t *testing.T) {
	src := t.TempDir()
	dest := filepath.Join(t.TempDir(), "nested", "rejects")

	mk := func(name string) string {
		p := filepath.Join(src, name)
		if err := os.WriteFile(p, []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	first, err := MoveInto(dest, mk("DSC1.ARW"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(first) != "DSC1.ARW" {
		t.Errorf("first move renamed unnecessarily: %s", first)
	}
	second, err := MoveInto(dest, mk("DSC1.ARW"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(second) != "DSC1-1.ARW" {
		t.Errorf("collision should get -1 suffix, got %s", second)
	}
	third, err := MoveInto(dest, mk("DSC1.ARW"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(third) != "DSC1-2.ARW" {
		t.Errorf("second collision should get -2 suffix, got %s", third)
	}
}

func TestMoveIntoMissingSource(t *testing.T) {
	if _, err := MoveInto(t.TempDir(), filepath.Join(t.TempDir(), "ghost.ARW")); err == nil {
		t.Error("moving a missing file should error")
	}
}

func TestConfigDirOverrideWinsOnEveryPlatform(t *testing.T) {
	// The override has to be consulted before os.UserConfigDir, not after
	// it, because os.UserConfigDir reads a different variable on each OS.
	// Tests isolate themselves with this one; if it ever stopped taking
	// precedence they would go on passing while quietly reading and writing
	// the real store belonging to whoever ran them.
	want := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "wrong"))
	t.Setenv(ConfigDirEnv, want)

	got, ok := ConfigDir()
	if !ok {
		t.Fatal("no config dir with an override set")
	}
	if got != filepath.Join(want, "QK") {
		t.Errorf("config dir = %q, want %q", got, filepath.Join(want, "QK"))
	}
}

func TestConfigDirFallsBackToTheSystemLocation(t *testing.T) {
	t.Setenv(ConfigDirEnv, "")
	got, ok := ConfigDir()
	if !ok {
		t.Skip("no user config dir on this machine")
	}
	if filepath.Base(got) != "QK" {
		t.Errorf("config dir %q does not end in QK", got)
	}
}
