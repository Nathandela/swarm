package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDispatcherGuidanceDiscovery(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"--help"}, []string{"swarm dispatcher-guide"}},
		{[]string{"teammate", "--help"}, []string{"swarm dispatcher-guide"}},
		{[]string{"dispatcher-guide"}, []string{"--kind needs-input", "--kind complete", "same ID", "swarm teammate reply"}},
		{[]string{"dispatcher-guide", "--skill"}, []string{"name: swarm-dispatcher", "swarm dispatcher-guide"}},
	} {
		var out, errout bytes.Buffer
		if code := dispatch(tc.args, &out, &errout); code != 0 {
			t.Fatalf("%v: %d: %s", tc.args, code, &errout)
		}
		for _, want := range tc.want {
			if !strings.Contains(out.String(), want) {
				t.Errorf("%v: missing %q", tc.args, want)
			}
		}
	}
}
