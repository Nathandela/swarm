//go:build !linux

package loginworker

import (
	"errors"
	"testing"
)

func TestClaudeAuthManagerUnsupportedPlatformIsReadOnlyRefusal(t *testing.T) {
	if err := ValidateClaudeAuthManager(t.Context()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported manager: %v", err)
	}
}
