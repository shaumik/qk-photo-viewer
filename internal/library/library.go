// Package library models a shoot folder on a memory card: photo discovery,
// RAW+JPEG pairing, and the mark-then-commit reject workflow.
package library

import (
	"fmt"
	"os"
	"path/filepath"
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

// maxScanDepth covers a card root → DCIM → 100MSDCF, or any hand-organized
// folder up to three levels deep.
const maxScanDepth = 3

// Scan lists a shoot folder including its subfolders (up to maxScanDepth
// levels), pairs RAW+JPEG files that share a basename, and sorts by ID —
// which on a card is also chronological order. IDs of nested files are
// prefixed with their folder path ("100MSDCF:DSC09999") so rolled-over
// filenames can't collide. Hidden entries and the rejects folder are
// skipped; unreadable subfolders are ignored rather than fatal.
func Scan(root string) ([]Photo, error) {
	rootEntries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", root, err)
	}
	byID := map[string]*Photo{}
	var walk func(dir, prefix string, entries []os.DirEntry, depth int)
	walk = func(dir, prefix string, entries []os.DirEntry, depth int) {
		for _, e := range entries {
			name := e.Name()
			// Skip our own output folders: a developed JPEG is not another
			// photo to cull, and a reject is already dealt with.
			if strings.HasPrefix(name, ".") || name == RejectsDirName || name == ExportDirName {
				continue
			}
			if e.IsDir() {
				if depth < maxScanDepth {
					if sub, err := os.ReadDir(filepath.Join(dir, name)); err == nil {
						walk(filepath.Join(dir, name), prefix+name+":", sub, depth+1)
					}
				}
				continue
			}
			ext := strings.ToLower(filepath.Ext(name))
			if !rawExts[ext] && !jpegExts[ext] {
				continue
			}
			id := prefix + strings.TrimSuffix(name, filepath.Ext(name))
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
	}
	walk(root, "", rootEntries, 0)
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

// ExportDirName holds developed JPEGs, created inside the shoot folder so
// they travel with the photos they came from. Scanning skips it for the
// same reason it skips the rejects: an export is not another shot to cull.
const ExportDirName = "QK_EDITED"

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
