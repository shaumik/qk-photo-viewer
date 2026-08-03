// Package desktop is the small set of things that only make sense on a
// real desktop: putting an image on the clipboard, and showing a file to
// the user in their file manager.
//
// Everything here is best-effort and platform-specific. Where a platform
// has no answer, the call returns ErrUnsupported and the caller offers the
// user something else — which is why exporting to a folder exists and the
// clipboard is a convenience on top of it.
package desktop

import "errors"

// ErrUnsupported means this platform has no way to do that.
var ErrUnsupported = errors.New("desktop: not supported on this platform")

// CopyJPEG puts an encoded JPEG on the system clipboard as an image, so it
// can be pasted straight into a message.
func CopyJPEG(data []byte) error { return copyJPEG(data) }

// Reveal shows a file or folder to the user in their file manager.
func Reveal(path string) error { return reveal(path) }
