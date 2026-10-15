package accountconfig

import "testing"

func TestSupportedNativeVersion_AcceptsClaudeAtOrAboveFloor(t *testing.T) {
	for _, version := range []string{"2.1.288", "2.1.289", "2.1.290", "2.1.300", "2.2.0", "3.0.0"} {
		if !SupportedNativeVersion("claude", version) {
			t.Errorf("supported Claude rejected: %q", version)
		}
	}
	for _, version := range []string{"", "2.1.287", "2.0.999", "2.1", "v2.1.300", "02.1.300", "2.1.300-beta", "2.1.300+build", "../../2.1.300"} {
		if SupportedNativeVersion("claude", version) {
			t.Errorf("invalid or unsupported Claude accepted: %q", version)
		}
	}
}
