//go:build !darwin

package trash

import (
	"errors"
	"testing"
)

func TestPutUnsupportedOffMacOS(t *testing.T) {
	if _, err := Put("/tmp/whatever"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("want ErrUnsupported, got %v", err)
	}
}
