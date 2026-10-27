package claudeinput

import (
	"encoding/json"
	"os/exec"
	"testing"
)

func TestPluginLifecycleInterleavings(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for the embedded JavaScript lifecycle harness")
	}
	if output, err := exec.Command(node, "plugin_lifecycle_test.mjs", "plugin.js").CombinedOutput(); err != nil {
		t.Fatalf("plugin lifecycle: %v\n%s", err, output)
	}
}

func TestNativeControlObserverProtocol(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for the embedded JavaScript lifecycle harness")
	}
	output, err := exec.Command(node, "control_guard_test.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("control observer protocol: %v\n%s", err, output)
	}
	var vectors struct {
		Reports []json.RawMessage `json:"reports"`
	}
	if err := json.Unmarshal(output, &vectors); err != nil || len(vectors.Reports) == 0 {
		t.Fatalf("no produced wire vectors: %v", err)
	}
	for i, raw := range vectors.Reports {
		var report controlReport
		if strictControlJSON(raw, &report) != nil || !validControlReport(report) {
			t.Fatalf("native-shaped JS report %d refused by production decoder: %s", i, raw)
		}
	}
}
