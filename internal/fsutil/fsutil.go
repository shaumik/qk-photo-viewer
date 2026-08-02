// Package fsutil holds small file operations shared by the reject flows.
package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MoveInto renames src into destDir, creating destDir if needed. A name
// collision in destDir gets a numeric suffix rather than overwriting.
// Returns the final path of the moved file.
func MoveInto(destDir, src string) (string, error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", destDir, err)
	}
	target := filepath.Join(destDir, filepath.Base(src))
	for n := 1; ; n++ {
		if _, err := os.Lstat(target); os.IsNotExist(err) {
			break
		}
		ext := filepath.Ext(src)
		base := strings.TrimSuffix(filepath.Base(src), ext)
		target = filepath.Join(destDir, fmt.Sprintf("%s-%d%s", base, n, ext))
	}
	if err := os.Rename(src, target); err != nil {
		return "", err
	}
	return target, nil
}
