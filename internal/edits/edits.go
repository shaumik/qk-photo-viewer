// Package edits stores how each photo should be developed, without ever
// touching the photo.
//
// An edit is a couple of hundred bytes of JSON describing slider
// positions. It lives beside the file it belongs to — DSC04810.qk.json
// next to DSC04810.ARW — so it travels with the shoot when the card is
// copied, and so a folder full of photos is still just a folder full of
// photos.
//
// Cards are not always writable: the lock switch, a read-only mount, a
// card that has started to fail. When the folder will not take a sidecar
// the edit goes to application support instead, keyed by the folder it
// came from, and reads look in both places. Losing the ability to write
// beside the photo must not mean losing the ability to edit it.
package edits

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/shaumik/qk-photo-viewer/internal/develop"
)

// Suffix is appended to a photo's base name to make its sidecar.
const Suffix = ".qk.json"

// sidecarVersion guards the format. Bump it when the meaning of a field
// changes, so an old sidecar is ignored rather than misread.
const sidecarVersion = 1

type sidecar struct {
	Version int          `json:"version"`
	Edit    develop.Edit `json:"edit"`
	Note    string       `json:"note,omitempty"`
}

// Store holds a shoot's edits: authoritative in memory, mirrored to disk.
type Store struct {
	mu     sync.RWMutex
	mem    map[string]develop.Edit // keyed by photo file path
	dir    string                  // the open shoot folder
	backup string                  // where sidecars go when the card is read-only
	ro     bool
}

// New opens the edit store for a shoot folder. readOnly comes from the
// caller, which has already probed the folder by trying to write to it.
func New(dir string, readOnly bool) *Store {
	return &Store{
		mem:    map[string]develop.Edit{},
		dir:    dir,
		ro:     readOnly,
		backup: backupDir(dir),
	}
}

// backupDir names a stable per-folder directory under application
// support. The hash keeps two cards with the same folder name apart; the
// readable prefix keeps the directory browsable by a human.
func backupDir(dir string) string {
	root, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(dir))
	name := sanitize(filepath.Base(dir)) + "-" + hex.EncodeToString(sum[:4])
	return filepath.Join(root, "QK", "edits", name)
}

func sanitize(s string) string {
	out := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, s)
	if out == "" {
		return "shoot"
	}
	return out
}

// stemOf reduces a photo file to the identity its edit is stored under.
// A RAW and its JPEG twin are one photo and share one edit, which is what
// dropping the extension gives us.
func stemOf(photoPath string) string {
	return strings.TrimSuffix(photoPath, filepath.Ext(photoPath))
}

// Preload reads every sidecar for the given photos in one pass. Listing
// each directory once beats asking after a sidecar for every photo, most
// of which will not have one — on a card, that difference is felt.
func (s *Store) Preload(paths []string) {
	seen := map[string]bool{}
	var stems []string
	dirs := map[string]bool{}
	for _, p := range paths {
		st := stemOf(p)
		if seen[st] {
			continue
		}
		seen[st] = true
		stems = append(stems, st)
		dirs[filepath.Dir(st)] = true
	}

	beside := map[string]develop.Edit{}
	for dir := range dirs {
		for name, e := range sidecarsIn(dir) {
			beside[filepath.Join(dir, name)] = e
		}
	}
	var backups map[string]develop.Edit
	if s.backup != "" {
		backups = sidecarsIn(s.backup)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range stems {
		if e, ok := beside[st]; ok {
			s.mem[st] = e
			continue
		}
		// Nothing beside the photo: the card may have been locked when the
		// edit was made, so look where that would have put it.
		if e, ok := backups[s.backupKey(st)]; ok {
			s.mem[st] = e
		}
	}
}

// sidecarsIn returns a directory's sidecars, keyed by the base name of the
// photo each belongs to.
func sidecarsIn(dir string) map[string]develop.Edit {
	out := map[string]develop.Edit{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, Suffix) {
			continue
		}
		e, err := readSidecar(filepath.Join(dir, name))
		if err != nil {
			continue // an unreadable sidecar means "as shot", not a failure
		}
		out[strings.TrimSuffix(name, Suffix)] = e
	}
	return out
}

func readSidecar(path string) (develop.Edit, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return develop.Edit{}, err
	}
	var sc sidecar
	if err := json.Unmarshal(data, &sc); err != nil {
		return develop.Edit{}, err
	}
	if sc.Version != sidecarVersion {
		return develop.Edit{}, fmt.Errorf("edits: %s is version %d, not %d",
			path, sc.Version, sidecarVersion)
	}
	return sc.Edit.Clamp(), nil
}

// Get returns a photo's edit and whether one was ever set. The zero Edit
// and false mean "as shot". Any file of a RAW+JPEG pair will do.
func (s *Store) Get(photoPath string) (develop.Edit, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.mem[stemOf(photoPath)]
	return e, ok
}

// Hold records an edit in memory without touching the disk. Dragging a
// slider produces dozens of values a second and only the one you stop on
// is worth writing to a memory card; the caller follows up with Set.
func (s *Store) Hold(photoPath string, e develop.Edit) {
	s.mu.Lock()
	s.mem[stemOf(photoPath)] = e.Clamp()
	s.mu.Unlock()
}

// Set records an edit and writes it out. The in-memory value is updated
// even if the write fails, so a card that goes away mid-session does not
// also take the work done before it went.
func (s *Store) Set(photoPath string, e develop.Edit) error {
	stem := stemOf(photoPath)
	e = e.Clamp()
	s.mu.Lock()
	s.mem[stem] = e
	s.mu.Unlock()

	if e.IsZero() {
		return s.removeFiles(stem) // as shot needs no sidecar
	}
	data, err := json.MarshalIndent(sidecar{Version: sidecarVersion, Edit: e}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if !s.ro {
		if err := writeAtomic(stem+Suffix, data); err == nil {
			return nil
		}
		// Fall through: the folder claimed to be writable and is not any
		// more. Better to keep the edit somewhere than to lose it.
	}
	if s.backup == "" {
		return fmt.Errorf("edits: nowhere to save %s", filepath.Base(photoPath))
	}
	if err := os.MkdirAll(s.backup, 0o755); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.backup, s.backupKey(stem)+Suffix), data)
}

// Reset drops a photo's edit entirely.
func (s *Store) Reset(photoPath string) error {
	stem := stemOf(photoPath)
	s.mu.Lock()
	delete(s.mem, stem)
	s.mu.Unlock()
	return s.removeFiles(stem)
}

func (s *Store) removeFiles(stem string) error {
	var firstErr error
	drop := func(p string) {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	if !s.ro {
		drop(stem + Suffix)
	}
	if s.backup != "" {
		drop(filepath.Join(s.backup, s.backupKey(stem)+Suffix))
	}
	return firstErr
}

// Edited reports which photos carry a non-default edit, keyed the same way
// Get is, so the UI can mark them without asking one at a time.
func (s *Store) Edited() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]bool, len(s.mem))
	for stem, e := range s.mem {
		if !e.IsZero() {
			out[stem] = true
		}
	}
	return out
}

// Key returns the identity Edited's map is keyed by, for a given photo.
func Key(photoPath string) string { return stemOf(photoPath) }

// backupKey flattens a photo's path relative to the shoot folder into a
// single filename, so nested folders cannot collide in the flat backup.
func (s *Store) backupKey(stem string) string {
	rel, err := filepath.Rel(s.dir, stem)
	if err != nil {
		rel = filepath.Base(stem)
	}
	return sanitize(filepath.ToSlash(rel))
}

// writeAtomic writes through a temporary file so a card pulled mid-write
// leaves either the old sidecar or the new one, never half of either.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".qk-edit-*")
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
