// Package fsutil holds small file operations shared by the reject flows.
package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ConfigDirEnv relocates everything QK stores for itself — learned lens
// profiles, and edits for cards it cannot write to.
const ConfigDirEnv = "QK_CONFIG_DIR"

// ConfigDir names the directory QK keeps its own state in, and reports
// whether there is one at all. Callers must cope with there not being: a
// session that cannot remember anything still has to work.
//
// It exists so that "where does QK store things" has a single answer that
// does not vary by platform. os.UserConfigDir alone does not: it reads
// XDG_CONFIG_HOME on Linux and ignores it on macOS, in favour of
// $HOME/Library/Application Support. A test that points XDG_CONFIG_HOME at
// a temporary directory is therefore isolated on one OS and, silently, on
// the other, editing the real store belonging to whoever ran it.
func ConfigDir() (string, bool) {
	if dir := os.Getenv(ConfigDirEnv); dir != "" {
		return filepath.Join(dir, "QK"), true
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return "", false
	}
	return filepath.Join(root, "QK"), true
}

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
