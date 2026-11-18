package claudeinput

import "testing"

func TestManagerNativeTurnsDoNotBroadenWorkerInputSupport(t *testing.T) {
	for _, version := range []string{"2.1.296", "2.1.310", "2.1.311"} {
		if !SupportedVersion(version) || SupportsNativeTurns(version) {
			t.Fatalf("worker/manager capability changed for %s", version)
		}
	}
	for _, version := range []string{"2.1.319", "2.1.326"} {
		if !SupportedVersion(version) || !SupportsNativeTurns(version) {
			t.Fatalf("manager native-turn qualification missing for %s", version)
		}
	}
	for _, version := range []string{"2.1.320", "2.1.325", "2.1.327", "2.1.326-beta.1"} {
		if SupportedVersion(version) || SupportsNativeTurns(version) {
			t.Fatalf("unqualified manager/runtime version %s", version)
		}
	}
}
