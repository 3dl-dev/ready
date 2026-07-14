package resolve_test

import (
	"testing"

	"github.com/3dl-dev/ready/pkg/resolve"
)

// TestErrNotFound verifies the error type.
func TestErrNotFound(t *testing.T) {
	err := resolve.ErrNotFound{ID: "ready-xyz"}
	if err.Error() == "" {
		t.Error("expected non-empty error message")
	}
}

// TestErrAmbiguous verifies the error type.
func TestErrAmbiguous(t *testing.T) {
	err := resolve.ErrAmbiguous{Prefix: "ready", Matches: []string{"ready-a1b", "ready-a2c"}}
	if err.Error() == "" {
		t.Error("expected non-empty error message")
	}
}
