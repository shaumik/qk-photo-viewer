//go:build darwin

package trash

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/shaumik/qk-photo-viewer/internal/fsutil"
)

// Put moves the file into the trash of the volume it lives on: the hidden
// .Trashes/<uid> directory for external volumes (SD cards under /Volumes),
// ~/.Trash for the boot volume. Same-volume renames, so it's instant.
func Put(path string) (string, error) {
	dir, err := dirFor(path)
	if err != nil {
		return "", err
	}
	return fsutil.MoveInto(dir, path)
}

func dirFor(path string) (string, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return "", err
	}
	mnt := cstring(st.Mntonname[:])
	if mnt == "" {
		return "", errors.New("cannot determine volume mount point")
	}
	if strings.HasPrefix(mnt, "/Volumes/") {
		return filepath.Join(mnt, ".Trashes", strconv.Itoa(os.Getuid())), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".Trash"), nil
}

func cstring(b []int8) string {
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if c == 0 {
			break
		}
		out = append(out, byte(c))
	}
	return string(out)
}
