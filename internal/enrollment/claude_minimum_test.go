//go:build linux

package enrollment

import (
	"errors"
	"testing"
)

func TestValidateConfig_AcceptsClaudeAtOrAboveFloor(t *testing.T) {
	for _, version := range []string{"2.1.288", "2.1.290", "2.1.300"} {
		cfg := testConfig(t, "claude", "ready")
		cfg.NativeVersion = version
		if err := validateConfig(cfg); err != nil {
			t.Errorf("Claude %s enrollment refused: %v", version, err)
		}
	}
	cfg := testConfig(t, "claude", "ready")
	cfg.NativeVersion = "2.1.287"
	if err := validateConfig(cfg); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Claude below the floor accepted: %v", err)
	}
}
