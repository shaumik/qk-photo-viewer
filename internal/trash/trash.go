// Package trash moves files to the operating system's trash, so committed
// rejects behave like any other deleted file: visible in the Trash,
// restorable, and gone when the user empties it. Where no system trash is
// available, Put returns ErrUnsupported and callers fall back to the
// on-card rejects folder.
package trash

import "errors"

// ErrUnsupported means this platform (or volume) has no reachable trash.
var ErrUnsupported = errors.New("system trash not supported here")
