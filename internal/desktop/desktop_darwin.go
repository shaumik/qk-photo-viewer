package desktop

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// macOS puts images on the pasteboard through AppKit, which is not
// reachable from a pure-Go build. osascript is: it will read a file and
// coerce it to a picture, which is exactly the operation wanted. The cost
// is a temporary file, which is cleaned up immediately after.
func copyJPEG(data []byte) error {
	f, err := os.CreateTemp("", "qk-clipboard-*.jpg")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// The path is ours and ends in .jpg, so there is nothing here a photo
	// filename could inject; even so, it goes through a quoted POSIX file
	// reference rather than being spliced into a larger script.
	script := fmt.Sprintf("set the clipboard to (read (POSIX file %q) as JPEG picture)", tmp)
	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		return fmt.Errorf("desktop: copy to clipboard: %v: %s", err, out)
	}
	return nil
}

func reveal(path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	// -R selects the file in its folder; a folder is just opened.
	args := []string{"-R", path}
	if st, err := os.Stat(path); err == nil && st.IsDir() {
		args = []string{filepath.Clean(path)}
	}
	return exec.Command("open", args...).Start()
}
