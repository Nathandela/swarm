package claudeinput

import "testing"

func TestManagerNativeTurnsDoNotBroadenWorkerInputSupport(t *testing.T) {
	for _, version := range []string{"2.1.296", "2.1.310", "2.1.311"} {
		if !SupportedVersion(version) || SupportsNativeTurns(version) {
			t.Fatalf("worker/manager capability changed for %s", version)
		}
	}
	if !SupportedVersion("2.1.319") || !SupportsNativeTurns("2.1.319") || SupportsNativeTurns("2.1.320") {
		t.Fatal("manager native-turn qualification drifted")
	}
}
