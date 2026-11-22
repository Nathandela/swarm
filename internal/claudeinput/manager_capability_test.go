package claudeinput

import "testing"

func TestNativeInputAndManagerTurnVersionFloors(t *testing.T) {
	for _, version := range []string{"2.1.296", "2.1.310", "2.1.318"} {
		if !SupportedVersion(version) || SupportsNativeTurns(version) {
			t.Fatalf("expected worker input only for %s", version)
		}
	}
	for _, version := range []string{"2.1.319", "2.1.326", "2.1.330", "3.0.0"} {
		if !SupportedVersion(version) || !SupportsNativeTurns(version) {
			t.Fatalf("expected worker input and manager turns for %s", version)
		}
	}
	for _, version := range []string{"2.1.295", "2.1.296-beta.1", "2.1.330-beta.1", "2.330", "unknown", ""} {
		if SupportedVersion(version) || SupportsNativeTurns(version) {
			t.Fatalf("unsupported worker/manager version %q was admitted", version)
		}
	}
}
