// Package lens remembers how a lens behaves so you only have to say it
// once.
//
// A lens distorts the same way every time at the same focal length. That
// makes it exactly the wrong thing to ask a person to fix per photo: the
// answer for the kit zoom at 16mm is the same answer for every 16mm frame
// ever shot with it. So the first time a correction is dialled in, it is
// filed under that lens and focal length, and every later shot that
// matches starts out corrected.
//
// This is the honest version of a lens profile database. QK does not ship
// measurements for lenses it has never seen; it learns yours, from you,
// once.
package lens

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
)

// Profile is what was learned about a lens at a focal length.
type Profile struct {
	Distortion float64 `json:"distortion"`
	Vignette   float64 `json:"vignette"`
}

// IsZero reports a profile that corrects nothing, which is worth
// remembering too: it means "this lens is fine, stop asking".
func (p Profile) IsZero() bool { return p == Profile{} }

// Key names a lens at a focal length. An empty key means the photo did
// not say enough to recognise the lens again, and nothing is remembered —
// guessing would apply one lens's correction to another's photos.
func Key(name string, focalMM float64) string {
	if name == "" || focalMM <= 0 || focalMM > 5000 {
		return ""
	}
	// A millimetre is finer than distortion changes and coarser than the
	// noise in what a camera reports, so profiles accumulate where they
	// are used rather than scattering.
	return fmt.Sprintf("%s@%dmm", name, int(math.Round(focalMM)))
}

type file struct {
	Version int                `json:"version"`
	Lenses  map[string]Profile `json:"lenses"`
}

const fileVersion = 1

// Store holds the learned profiles, backed by a file in application
// support. It is shared across shoots on purpose: the lens is a property
// of your bag, not of the card in the camera.
type Store struct {
	mu     sync.RWMutex
	byKey  map[string]Profile
	path   string
	loaded bool
}

// Open returns the store, reading what has been learned so far. A store
// with nowhere to write still works for the session; it just forgets.
func Open() *Store {
	s := &Store{byKey: map[string]Profile{}}
	if root, err := os.UserConfigDir(); err == nil {
		s.path = filepath.Join(root, "QK", "lenses.json")
	}
	s.load()
	return s
}

func (s *Store) load() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loaded = true
	if s.path == "" {
		return
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return // nothing learned yet is the normal case
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil || f.Version != fileVersion {
		return
	}
	for k, p := range f.Lenses {
		s.byKey[k] = clamp(p)
	}
}

// Get returns what is known about a lens, and whether anything is.
func (s *Store) Get(key string) (Profile, bool) {
	if key == "" {
		return Profile{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.byKey[key]
	return p, ok
}

// Set records a correction for a lens. Writing is best effort: failing to
// remember for next time must not fail the edit that is happening now.
func (s *Store) Set(key string, p Profile) error {
	if key == "" {
		return nil
	}
	s.mu.Lock()
	s.byKey[key] = clamp(p)
	s.mu.Unlock()
	return s.persist()
}

// Forget drops what was learned about a lens.
func (s *Store) Forget(key string) error {
	if key == "" {
		return nil
	}
	s.mu.Lock()
	_, had := s.byKey[key]
	delete(s.byKey, key)
	s.mu.Unlock()
	if !had {
		return nil
	}
	return s.persist()
}

func (s *Store) persist() error {
	s.mu.RLock()
	snapshot := make(map[string]Profile, len(s.byKey))
	for k, v := range s.byKey {
		snapshot[k] = v
	}
	path := s.path
	s.mu.RUnlock()

	if path == "" {
		return nil // nowhere to write: the session still works, it just forgets
	}
	data, err := json.MarshalIndent(file{Version: fileVersion, Lenses: snapshot}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeAtomic(path, append(data, '\n'))
}

// Len reports how many lens and focal length combinations are known.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byKey)
}

func clamp(p Profile) Profile {
	c := func(v float64) float64 {
		if math.IsNaN(v) {
			return 0
		}
		return math.Max(-100, math.Min(100, v))
	}
	return Profile{Distortion: c(p.Distortion), Vignette: c(p.Vignette)}
}

// writeAtomic replaces the file in one step, so an interrupted write
// leaves the old profiles rather than half of the new ones.
func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".qk-lenses-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
