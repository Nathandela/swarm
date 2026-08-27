package skeleton

import "testing"

func TestCoreAPISetTagReachesTheRealDaemon(t *testing.T) {
	sk := assemble(t)
	m := launchFake(t, sk, "print ready\nexit 0\n")
	if err := sk.api.SetTag(m.ID, "frontend"); err != nil {
		t.Fatalf("SetTag: %v", err)
	}
	got, ok := sk.core.Get(m.ID)
	if !ok || got.Tag != "frontend" {
		t.Fatalf("daemon tag = %q (ok=%v), want frontend", got.Tag, ok)
	}
}
