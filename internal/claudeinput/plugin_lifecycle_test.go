package claudeinput

import (
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
