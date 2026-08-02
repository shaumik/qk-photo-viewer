//go:build !darwin

package trash

// Put is unsupported off macOS; callers use the rejects-folder fallback.
func Put(path string) (string, error) {
	return "", ErrUnsupported
}
