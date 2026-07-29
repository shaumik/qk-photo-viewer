// Package library models a shoot folder on a memory card: photo discovery,
// RAW+JPEG pairing, and the mark-then-commit reject workflow.
package library

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

func (p Photo) files() []string {
	var fs []string
	if p.Raw != "" {
		fs = append(fs, p.Raw)
	}
	if p.Jpeg != "" {
		fs = append(fs, p.Jpeg)
	}
	return fs
}

// Scan lists dir (non-recursive: cards keep a flat DCIM/100MSDCF layout),
// pairs RAW+JPEG files that share a basename, and sorts by name — which on a
// card is also chronological order.
func Scan(dir string) ([]Photo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", dir, err)
	}
	byID := map[string]*Photo{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(name))
		if !rawExts[ext] && !jpegExts[ext] {
			continue
		}
		id := strings.TrimSuffix(name, filepath.Ext(name))
		p, ok := byID[id]
		if !ok {
			p = &Photo{ID: id}
			byID[id] = p
		}
		full := filepath.Join(dir, name)
		if rawExts[ext] {
			p.Raw = full
		} else {
			p.Jpeg = full
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
	if len(rejects) == 0 {
		return 0, nil
	}
	dest := filepath.Join(dir, RejectsDirName)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return 0, fmt.Errorf("create rejects folder: %w", err)
	}
	moved := 0
	for _, p := range rejects {
		for _, src := range p.files() {
			target := filepath.Join(dest, filepath.Base(src))
			for n := 1; ; n++ {
				if _, err := os.Lstat(target); os.IsNotExist(err) {
					break
				}
				ext := filepath.Ext(src)
				base := strings.TrimSuffix(filepath.Base(src), ext)
				target = filepath.Join(dest, fmt.Sprintf("%s-%d%s", base, n, ext))
			}
			if err := os.Rename(src, target); err != nil {
				return moved, fmt.Errorf("move %s: %w", src, err)
			}
			moved++
		}
	}
	return moved, nil
}
