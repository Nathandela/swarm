package daemon

// R3.1.2 (agents-tracker-tid, perf plan item 3.1): removes the redundant
// persist.FilterEnv calls in SetStatus, SetConversationID, saveMeta and
// finalizeTerminal — persist.Save (the disk-write trust boundary, pinned by
// persist.TestSaveFiltersEnvBeforePersist) is now the SOLE site that filters.
// This test pins the end-to-end property the dedup must not weaken: a secret
// in a session's launch env never reaches meta.json on disk, exercised through
// the exact daemon write paths that lost their own filtering (SetStatus,
// SetConversationID). Must pass unchanged before and after the dedup.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/status"
)

func TestSecretEnvNeverReachesDiskAcrossDaemonWrites(t *testing.T) {
	cfg := daemonConfig(t)
	d := openDaemon(t, cfg)

	const secret = "AWS_SECRET_ACCESS_KEY=topsecret"
	const allowed = "PATH=" + "/usr/bin:/bin"
	spec := LaunchSpec{
		AgentType: "fake",
		Argv:      []string{selfExe(t), markerAnnounce, filepath.Join(t.TempDir(), "pid")},
		Cwd:       t.TempDir(),
		ClientEnv: []string{allowed, secret},
		Cols:      80,
		Rows:      24,
	}
	m, err := d.Launch(spec)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	t.Cleanup(func() { _ = d.Kill(m.ID) })

	// Each of these goes through a daemon write path that used to re-filter Env
	// itself; only persist.Save filters now.
	if err := d.SetStatus(m.ID, status.Status{Turn: status.TurnActive, Interaction: status.InteractionNone}); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if err := d.SetConversationID(m.ID, "conv-dedup-1"); err != nil {
		t.Fatalf("SetConversationID: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(cfg.StateDir, m.ID, "meta.json"))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	if strings.Contains(string(raw), "AWS_SECRET_ACCESS_KEY") {
		t.Fatalf("non-allowlisted env reached disk after SetStatus/SetConversationID:\n%s", raw)
	}
	if !strings.Contains(string(raw), "PATH=") {
		t.Fatalf("allowlisted env was dropped:\n%s", raw)
	}
}
