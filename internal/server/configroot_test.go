package server

import (
	"os"
	"testing"

	"github.com/shaumik/qk-photo-viewer/internal/fsutil"
)

// TestMain puts everything this package stores for itself inside a
// temporary directory, for every test, whether or not the test remembered
// to ask.
//
// Isolating per test is one forgotten line away from not being isolated,
// and the failure is invisible on the machine that writes it: os.UserConfigDir
// resolves differently per platform, so a test can be sandboxed on Linux
// and, in the same run on macOS, be editing the lens profiles belonging to
// whoever ran it. Pinning it here means no test in this package can reach
// the real store even by mistake.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "qk-config-*")
	if err != nil {
		panic(err)
	}
	os.Setenv(fsutil.ConfigDirEnv, dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
