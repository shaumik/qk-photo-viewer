// Package library models a shoot folder on a memory card: photo discovery,
// RAW+JPEG pairing, and the mark-then-commit reject workflow.
package library

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/shaumik/qk-photo-viewer/internal/fsutil"
)

// Sony first (ARW); other RAW formats join as they're needed.
var rawExts = map[string]bool{".arw": true}
var jpegExts = map[string]bool{".jpg": true, ".jpeg": true}

// Photo is one shot: a RAW file, a JPEG, or a paired RAW+JPEG that the camera
// wrote side by side. Culling always treats the pair as a single unit.
type Photo struct {
	ID   string `json:"id"`   // shared basename, e.g. "DSC04810"
	Raw  string `json:"raw"`  // path to the RAW file, "" if shot JPEG-only
	Jpeg string `json:"jpeg"` // path to the JPEG, "" if RAW-only
}

// Pair describes what's on disk for the HUD chip, e.g. "ARW +JPG".
func (p Photo) Pair() string {
	switch {
	case p.Raw != "" && p.Jpeg != "":
		return strings.ToUpper(strings.TrimPrefix(filepath.Ext(p.Raw), ".")) + " +JPG"
	case p.Raw != "":
		return strings.ToUpper(strings.TrimPrefix(filepath.Ext(p.Raw), "."))
	default:
		return "JPG"
	}
}

// Files lists the photo's on-disk files (1 or 2 paths).
func (p Photo) Files() []string {
	var fs []string
	if p.Raw != "" {
		fs = append(fs, p.Raw)
	}
	if p.Jpeg != "" {
		fs = append(fs, p.Jpeg)
	}
	return fs
}

// dcfDirRe matches DCF-numbered card folders (100MSDCF, 101MSDCF, ...) —
// cameras roll over to a new one every 9999 shots.
var dcfDirRe = regexp.MustCompile(`^\d{3}[0-9A-Za-z_]{1,5}$`)

// Scan lists a shoot folder, pairs RAW+JPEG files that share a basename,
// and sorts by ID — which on a card is also chronological order. If dir is
// a DCIM-style parent (containing 100MSDCF-like subfolders), those are
// scanned too, with IDs prefixed "100MSDCF:" so rolled-over filenames
// can't collide.
func Scan(dir string) ([]Photo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", dir, err)
	}
	byID := map[string]*Photo{}
	collect := func(base, idPrefix, name string) {
		if strings.HasPrefix(name, ".") {
			return
		}
		ext := strings.ToLower(filepath.Ext(name))
		if !rawExts[ext] && !jpegExts[ext] {
			return
		}
		id := idPrefix + strings.TrimSuffix(name, filepath.Ext(name))
		p, ok := byID[id]
		if !ok {
			p = &Photo{ID: id}
			byID[id] = p
		}
		full := filepath.Join(base, name)
		if rawExts[ext] {
			p.Raw = full
		} else {
			p.Jpeg = full
		}
	}
	var dcfDirs []string
	for _, e := range entries {
		if e.IsDir() {
			if dcfDirRe.MatchString(e.Name()) {
				dcfDirs = append(dcfDirs, e.Name())
			}
			continue
		}
		collect(dir, "", e.Name())
	}
	for _, sub := range dcfDirs {
		subEntries, err := os.ReadDir(filepath.Join(dir, sub))
		if err != nil {
			continue // a vanished or unreadable subfolder shouldn't sink the scan
		}
		for _, e := range subEntries {
			if !e.IsDir() {
				collect(filepath.Join(dir, sub), sub+":", e.Name())
			}
		}
	}
	photos := make([]Photo, 0, len(byID))
	for _, p := range byID {
		photos = append(photos, *p)
	}
	sort.Slice(photos, func(i, j int) bool { return photos[i].ID < photos[j].ID })
	return photos, nil
}

// RejectsDirName holds committed rejects, created inside the shoot folder.
// Same volume, so each move is a rename: instant, and recoverable until the
// folder is emptied. Native macOS Trash integration lands in milestone 3.
const RejectsDirName = "QK_REJECTS"

// CommitRejects moves every file of every listed photo into RejectsDirName.
// It returns the number of files moved. A name collision in the rejects
// folder gets a numeric suffix rather than overwriting.
func CommitRejects(dir string, rejects []Photo) (int, error) {
	dest := filepath.Join(dir, RejectsDirName)
	moved := 0
	for _, p := range rejects {
		for _, src := range p.Files() {
			if _, err := fsutil.MoveInto(dest, src); err != nil {
				return moved, fmt.Errorf("move %s: %w", src, err)
			}
			moved++
		}
	}
	return moved, nil
}
