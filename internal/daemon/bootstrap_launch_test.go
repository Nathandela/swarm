package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/persist"
)

func bootstrapLaunchRig(t *testing.T) *Daemon {
	t.Helper()
	root := t.TempDir()
	store, err := persist.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := idempotency.Open(filepath.Join(root, "idempotency"))
	if err != nil {
		t.Fatal(err)
	}
	feed, err := journal.Open(filepath.Join(root, "journal"), journal.Options{MaxBytes: 4096, MaxFiles: 2, MaxTotalBytes: 8192, LiveBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = feed.Close() })
	return &Daemon{journal: feed, cfg: Config{StateDir: root, MaxSessions: 8, ShimBinary: "/does/not/exist", LogPath: filepath.Join(root, "daemon.log")}, sessions: make(map[string]*session), store: store, idem: guard}
}

func TestBootstrapLaunchNoStartProofIsFreshDirectCallOnly(t *testing.T) {
	d := bootstrapLaunchRig(t)
	d.cfg.PreLaunch = func(string, LaunchSpec) (string, error) { return "", errors.New("private refusal detail") }
	spec := LaunchSpec{Cwd: t.TempDir(), OperationID: "fresh-bootstrap-nonce", BootstrapFirstAttempt: true}
	_, err := d.Launch(spec)
	if !BootstrapLaunchDidNotStart(err, spec.OperationID) {
		t.Fatal("fresh direct pre-fork refusal lost proof")
	}
	if err.Error() != "bootstrap_launch_not_started" || BootstrapLaunchDidNotStart(err, "other-nonce") {
		t.Fatal("no-start proof leaked detail or lost nonce binding")
	}
	_, err = d.Launch(spec)
	if err == nil || BootstrapLaunchDidNotStart(err, spec.OperationID) {
		t.Fatal("replay/redrive returned fresh no-start authority")
	}
	spec.OperationID = "ordinary-launch"
	spec.BootstrapFirstAttempt = false
	_, err = d.Launch(spec)
	if err == nil || BootstrapLaunchDidNotStart(err, spec.OperationID) {
		t.Fatal("ordinary launch acquired bootstrap proof")
	}
}

func TestBootstrapLaunchNoStartProofStopsAtStartAttempt(t *testing.T) {
	for _, before := range []bool{true, false} {
		t.Run(map[bool]string{true: "before_start", false: "start_attempted"}[before], func(t *testing.T) {
			d := bootstrapLaunchRig(t)
			if before {
				block := filepath.Join(d.cfg.StateDir, "block")
				if err := os.WriteFile(block, nil, 0600); err != nil {
					t.Fatal(err)
				}
				d.cfg.LogPath = filepath.Join(block, "log")
			}
			spec := LaunchSpec{Cwd: t.TempDir(), OperationID: "fresh-bootstrap-start", BootstrapFirstAttempt: true}
			_, err := d.Launch(spec)
			if err == nil || BootstrapLaunchDidNotStart(err, spec.OperationID) != before {
				t.Fatalf("no-start proof=%v, start not attempted=%v", BootstrapLaunchDidNotStart(err, spec.OperationID), before)
			}
		})
	}
}

func TestBootstrapLaunchExistingOperationHasNoEarlyRefusalAuthority(t *testing.T) {
	d := bootstrapLaunchRig(t)
	if _, _, err := d.idem.Prepare("existing-bootstrap", "launch", "previous-session"); err != nil {
		t.Fatal(err)
	}
	_, err := d.Launch(LaunchSpec{Cwd: "/does/not/exist", OperationID: "existing-bootstrap", BootstrapFirstAttempt: true})
	if err == nil || BootstrapLaunchDidNotStart(err, "existing-bootstrap") {
		t.Fatal("existing operation gained no-effect authority before Prepare")
	}
}

func TestBootstrapLaunchPostForkFailureRemainsUnknown(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	spec := announceSpec(t, filepath.Join(t.TempDir(), "agent.pid"))
	spec.OperationID = "post-fork-bootstrap"
	spec.BootstrapFirstAttempt = true
	meta, err := d.launch(spec, func(phase launchPhase, _ persist.Meta) error {
		if phase == phaseSpawned {
			return errors.New("injected post-fork interruption")
		}
		return nil
	})
	if err == nil || meta.ShimPID == 0 || BootstrapLaunchDidNotStart(err, spec.OperationID) {
		t.Fatal("actual shim birth was classified as no-start")
	}
}
