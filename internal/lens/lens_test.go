package lens

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestKeyIdentifiesALensAtAFocalLength(t *testing.T) {
	if got := Key("E PZ 16-50mm F3.5-5.6 OSS", 16); got != "E PZ 16-50mm F3.5-5.6 OSS@16mm" {
		t.Errorf("key = %q", got)
	}
	// The same zoom at another focal length is a different profile,
	// because it distorts differently there.
	if Key("zoom", 16) == Key("zoom", 50) {
		t.Error("16mm and 50mm should not share a profile")
	}
	// Reported focal lengths wobble; a millimetre apart is the same lens
	// setting as far as distortion is concerned.
	if Key("zoom", 16.2) != Key("zoom", 16) {
		t.Error("16.2mm and 16mm should land in the same profile")
	}
	// Nothing to recognise means nothing remembered: applying one lens's
	// correction to another's photos is worse than applying none.
	for _, k := range []string{Key("", 16), Key("zoom", 0), Key("zoom", -3), Key("zoom", 99999)} {
		if k != "" {
			t.Errorf("expected no key, got %q", k)
		}
	}
}

func TestLearnsAndRemembersAcrossSessions(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	key := Key("E PZ 16-50mm F3.5-5.6 OSS", 16)

	s := Open()
	if _, ok := s.Get(key); ok {
		t.Fatal("a fresh store should know nothing")
	}
	want := Profile{Distortion: 42, Vignette: 30}
	if err := s.Set(key, want); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// A later session — a different shoot, a different card — knows it.
	s2 := Open()
	got, ok := s2.Get(key)
	if !ok || got != want {
		t.Errorf("reopened with %+v (found %v), want %+v", got, ok, want)
	}
	if s2.Len() != 1 {
		t.Errorf("store holds %d profiles, want 1", s2.Len())
	}
}

func TestZeroProfileIsWorthRemembering(t *testing.T) {
	// "This lens needs no correction" is a decision, and forgetting it
	// would mean asking again on every shoot.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	key := Key("Sonnar T* FE 55mm F1.8 ZA", 55)
	if err := Open().Set(key, Profile{}); err != nil {
		t.Fatal(err)
	}
	p, ok := Open().Get(key)
	if !ok {
		t.Fatal("a zero profile should still be remembered")
	}
	if !p.IsZero() {
		t.Errorf("profile = %+v, want zero", p)
	}
}

func TestForgetRemovesAProfile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	key := Key("zoom", 16)
	s := Open()
	s.Set(key, Profile{Distortion: 20})
	if err := s.Forget(key); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if _, ok := s.Get(key); ok {
		t.Error("the profile survived being forgotten")
	}
	if _, ok := Open().Get(key); ok {
		t.Error("the profile came back on reload")
	}
	if err := s.Forget("nothing at all"); err != nil {
		t.Errorf("forgetting an unknown lens should be fine, got %v", err)
	}
}

func TestUnknownLensIsNeverRemembered(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := Open()
	if err := s.Set("", Profile{Distortion: 50}); err != nil {
		t.Fatal(err)
	}
	if s.Len() != 0 {
		t.Error("an unidentifiable lens should not create a profile")
	}
	if _, ok := s.Get(""); ok {
		t.Error("the empty key should never match")
	}
}

func TestCorruptAndFutureFilesAreIgnored(t *testing.T) {
	// The file is plain JSON in a folder a person can open. Neither a
	// mangled one nor one from a later version should reach the pipeline.
	for name, body := range map[string]string{
		"corrupt": "{not json",
		"future":  `{"version":99,"lenses":{"zoom@16mm":{"distortion":80}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", home)
			dir := filepath.Join(home, "QK")
			os.MkdirAll(dir, 0o755)
			os.WriteFile(filepath.Join(dir, "lenses.json"), []byte(body), 0o644)
			if n := Open().Len(); n != 0 {
				t.Errorf("loaded %d profiles from a %s file", n, name)
			}
		})
	}
}

func TestOutOfRangeValuesAreClamped(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := Open()
	s.Set(Key("zoom", 16), Profile{Distortion: 5000, Vignette: math.NaN()})
	p, _ := s.Get(Key("zoom", 16))
	if p.Distortion != 100 || p.Vignette != 0 {
		t.Errorf("stored %+v, want the values brought into range", p)
	}
}
