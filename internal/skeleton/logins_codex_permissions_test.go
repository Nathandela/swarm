package skeleton

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/codexnative"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/loginworker"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

func TestCodexMigrationUnsafeNPMRefusesBeforeJournalAndCredentialTransfer(t *testing.T) {
	for _, unsafe := range []string{"manifest", "payload"} {
		t.Run(unsafe, func(t *testing.T) {
			root, _, native := nativeCodexNPMFixture(t)
			path, mode := filepath.Join(root, "package.json"), os.FileMode(0664)
			if unsafe == "payload" {
				path, mode = native, 0775
			}
			for _, phase := range []string{"before-journal", "before-transfer"} {
				t.Run(phase, func(t *testing.T) {
					if err := os.Chmod(path, 0700); err != nil {
						t.Fatal(err)
					}
					f := codexMigrationFixture(t)
					for id, row := range f.rows {
						row.Env = []string{"PATH=" + root, "SWARM_TEST_CODEX_VERSION=0.163.0"}
						f.rows[id] = row
					}
					f.m.qualifyCodexOwnership = qualifyCodexOwnershipSources
					if phase == "before-transfer" {
						if _, err := f.m.BeginSwitch(logins.ProviderCodex, f.second.ID, "explicit"); err != nil {
							t.Fatal(err)
						}
						for i := 0; i < 8; i++ {
							r, _ := f.m.store.Snapshot()
							if r.Switches[logins.ProviderCodex].Phase == "captured" {
								break
							}
							f.m.step()
						}
					}
					before, err := os.ReadFile(filepath.Join(f.runtime.Home, "auth.json"))
					if err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(path, mode); err != nil {
						t.Fatal(err)
					}
					if phase == "before-journal" {
						_, err = f.m.BeginSwitch(logins.ProviderCodex, f.second.ID, "explicit")
					} else {
						f.m.step()
						r, _ := f.m.store.Snapshot()
						op := r.Switches[logins.ProviderCodex]
						if op.Phase != "captured" {
							t.Fatal("unsafe native installation advanced credential transfer")
						}
						err = errors.New(op.LastError)
					}
					if err == nil || !strings.Contains(err.Error(), "reinstall") || !strings.Contains(err.Error(), "write permissions") {
						t.Fatalf("unsafe native preflight did not explain remediation: %v", err)
					}
					r, _ := f.m.store.Snapshot()
					after, readErr := os.ReadFile(filepath.Join(f.runtime.Home, "auth.json"))
					if readErr != nil || !bytes.Equal(before, after) || r.Providers[logins.ProviderCodex].AuthOwnerMode != "" || len(f.launched) != 0 || phase == "before-journal" && len(r.Switches) != 0 {
						t.Fatal("unsafe native preflight changed credentials or ownership")
					}
				})
			}
		})
	}
}

func TestCodexEnrollmentRefusesUnsafeKnownNPMDependency(t *testing.T) {
	root, launcher, native := nativeCodexNPMFixture(t)
	t.Setenv("PATH", root)
	if err := os.Chmod(native, 0775); err != nil {
		t.Fatal(err)
	}
	if _, err := newLoginWorkerConfig(t.TempDir(), logins.ProviderCodex, "synthetic", ""); !errors.Is(err, codexnative.ErrUnsafe) {
		t.Fatalf("unsafe known npm dependency fell back to wrapper enrollment: %v", err)
	}
	f := newLoginFixture(t)
	r, _ := f.m.store.Snapshot()
	if _, err := f.m.d.api.Logins(protocol.LoginsReq{Action: "start", Provider: logins.ProviderCodex, Label: "synthetic", ExpectedRevision: r.Revision}); !errors.Is(err, protocol.ErrLoginsUnsafeNativeCLI) {
		t.Fatalf("known unsafe dependency lost closed actionable UI error: %v", err)
	}
	if jobs, err := loginworker.Jobs(f.m.store.StateRoot()); err != nil || len(jobs) != 0 {
		t.Fatal("unsafe native enrollment started a worker")
	}
	if ok, err := qualifyCodexOwnershipSources([]persist.Meta{{Env: []string{"PATH=" + root}, Cwd: root}}); ok || err == nil {
		t.Fatal("unsafe payload qualified ownership")
	}
	if info, err := os.Stat(launcher); err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("preflight rewrote installation permissions")
	}
}
