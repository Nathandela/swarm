package claudeinput

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/testbin"
)

func TestNativeClaudeManagerProvenance(t *testing.T) {
	if os.Getenv("SWARM_NATIVE_MANAGER_PROOF") != "1" {
		t.Skip("explicit isolated installed-Claude manager proof fixture only")
	}
	binary, version, dir := privateNativeClaude(t)
	if version != "2.1.319" {
		t.Fatal("installed manager version is not qualified")
	}
	if err := testbin.RegisterExecutable(binary); err != nil {
		t.Fatal(err)
	}
	plugin, err := InstallManager(dir, "native-proof-conversation")
	if err != nil {
		t.Fatal(err)
	}
	broker, err := Start(dir, "native-proof", version, os.Getpid(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = broker.Close() }()
	harness, err := filepath.Abs("../../scripts/verification/claude_manager_provenance.py")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "python3", harness, binary, filepath.Join(dir, DescriptorFile), plugin)
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("native manager qualification: %v\n%s", err, output)
	}
	t.Log(string(output))
}
