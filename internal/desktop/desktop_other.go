//go:build !darwin

package desktop

// QK ships for macOS today. On anything else these fail cleanly and the
// UI falls back to exporting a file, which works everywhere.

func copyJPEG([]byte) error { return ErrUnsupported }

func reveal(string) error { return ErrUnsupported }
