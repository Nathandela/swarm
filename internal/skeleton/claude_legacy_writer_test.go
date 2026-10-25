package skeleton

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/status"
)

func legacyClaudeWriterFixture(t *testing.T, floor int) (string, persist.Meta, *persist.Store) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := logins.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	path := filepath.Join(root, "logins", "registry.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["schema_version"], _ = json.Marshal(floor)
	raw, _ = json.Marshal(fields)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	m := persist.Meta{SchemaVersion: persist.LoginSchemaVersion, ID: "legacy", AgentType: logins.ProviderClaude, Cwd: "/project", LoginRuntime: &logins.Runtime{Provider: logins.ProviderClaude, Epoch: 1, Identity: strings.Repeat("a", 64), Home: store.Home(logins.ProviderClaude)}, Status: status.Status{Process: status.ProcessLost}}
	disk, err := persist.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := disk.Save(m); err != nil {
		t.Fatal(err)
	}
	return root, m, disk
}

func TestClaudeLegacyWriterProofUsesStoredRetirementAtOperationalFloor(t *testing.T) {
	root, stale, disk := legacyClaudeWriterFixture(t, logins.ClaudeProfileCustodySchemaVersion)
	// This private fixture never forked a shim for the exact reservation.
	if _, err := disk.RetireClaudeReservation(stale.ID, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := verifyLoginWritersStopped(root, stale); err != nil {
		t.Fatalf("stale common4 caller ignored exact stored terminal marker: %v", err)
	}
}

func TestClaudeLegacyWriterProofOldFloorsAndMetadataErrors(t *testing.T) {
	for _, kind := range []string{"mode1", "mode9", "metadata-error"} {
		t.Run(kind, func(t *testing.T) {
			floor := 9
			if kind == "mode1" {
				floor = 1
			}
			if kind == "metadata-error" {
				floor = logins.ClaudeProfileCustodySchemaVersion
			}
			root, m, disk := legacyClaudeWriterFixture(t, floor)
			child := exec.Command("/bin/sleep", "30")
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			started, err := procstart.StartTime(child.Process.Pid)
			_ = child.Process.Kill()
			_ = child.Wait()
			if err != nil {
				t.Fatal(err)
			}
			m.ShimPID, m.ShimStartTime = child.Process.Pid, started
			if err := disk.Save(m); err != nil {
				t.Fatal(err)
			}
			native := logins.NativeProcessInfo{SchemaVersion: logins.NativeWriterSchemaVersion, Generation: strings.Repeat("b", 32), ShimPID: m.ShimPID, ShimStartTime: m.ShimStartTime, Provider: logins.ProviderClaude, Runtime: *m.LoginRuntime}
			stopped := logins.NativeStoppedInfo{SchemaVersion: logins.NativeWriterSchemaVersion, Native: native, WritersStopped: true, DescendantsContained: true}
			for name, value := range map[string]any{logins.NativeProcessFile: native, logins.NativeStoppedFile: stopped} {
				raw, _ := json.Marshal(value)
				if err := os.WriteFile(filepath.Join(root, m.ID, name), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "metadata-error" {
				if err := os.WriteFile(filepath.Join(root, m.ID, "meta.json"), []byte("corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err = verifyLoginWritersStopped(root, m)
			if kind != "metadata-error" {
				if err != nil {
					t.Fatalf("old-floor positive native3 behavior changed: %v", err)
				}
			} else if err == nil {
				t.Fatal("corrupt stored metadata became positive stop proof")
			}
		})
	}
}
