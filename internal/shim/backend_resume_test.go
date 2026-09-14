package shim

import (
	"reflect"
	"testing"
)

func TestBackendAgentArgvPreservesFallbackAndExecutable(t *testing.T) {
	original := []string{"/trusted/codex", "resume", "thread", "--sandbox", "read-only"}
	alternate := []string{"resume", "thread"}
	attach := []string{"--remote", "unix:///session/codex.sock"}
	for _, tc := range []struct {
		name                    string
		alternate, attach, want []string
	}{
		{"attached", alternate, attach, []string{"/trusted/codex", "resume", "thread", "--remote", "unix:///session/codex.sock"}},
		{"degraded", alternate, nil, original},
		{"legacy", nil, attach, append(append([]string(nil), original...), attach...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := backendAgentArgv(original, tc.alternate, tc.attach)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("argv = %v, want %v", got, tc.want)
			}
			got[0] = "mutated"
			if original[0] != "/trusted/codex" {
				t.Fatal("mutated original fallback")
			}
		})
	}
}
