package accountconfig

import "testing"

func TestCodexNativeVersionUsesMinimumStableRelease(t *testing.T) {
	for _, version := range []string{"0.160.0", "0.160.1", "0.162.1", "0.999.0", "1.0.0", "10.0.0"} {
		if !SupportedNativeVersion("codex", version) {
			t.Errorf("supported Codex rejected: %q", version)
		}
	}
	for _, version := range []string{"", "0.159.999", "0.160", "v0.162.1", "00.162.1", "0.162.1-beta", "0.162.1+build", "../../0.162.1"} {
		if SupportedNativeVersion("codex", version) {
			t.Errorf("invalid or unsupported Codex accepted: %q", version)
		}
	}
	if !SupportedNativeVersion("claude", "2.1.296") {
		t.Fatal("Claude minimum-version policy rejected a later stable release")
	}
}
