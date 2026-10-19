package opencodebridge

import "testing"

func TestSupportedNativeVersion(t *testing.T) {
	for _, tc := range []struct {
		version string
		allowed bool
	}{{"1.19.3", true}, {"1.19.4", true}, {"1.20.0", true}, {"2.0.0", true}, {"10000.0.0", true}, {"9999999999999999999999999999999999999999.0.0", true}, {"1.19.2", false}, {"1.18.99", false}, {"0.99.99", false}, {"", false}, {"1.19", false}, {"1.19.3.0", false}, {"v1.19.3", false}, {"01.19.3", false}, {"1.019.3", false}, {"1.19.03", false}, {"1.19.3-rc.1", false}, {"2.0.0-rc.1", false}, {"1.19.3+build", false}, {"2.0.0+build", false}, {" 1.19.3", false}, {"1.19.3\n", false}, {"invalid", false}} {
		t.Run(tc.version, func(t *testing.T) {
			if got := SupportedNativeVersion(tc.version); got != tc.allowed {
				t.Fatalf("SupportedNativeVersion(%q)=%v, want %v", tc.version, got, tc.allowed)
			}
		})
	}
}
