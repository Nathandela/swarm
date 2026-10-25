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

func TestClaudeOwnedWriterProofBindsStoredZeroPIDReservation(t *testing.T) {
	for _, kind := range []string{"exact-stopped", "missing-stop", "wrong-cwd", "wrong-session", "wrong-generation", "uncontained"} {
		t.Run(kind, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			// No account/grant: this is a metadata/proof fixture, with only the
			// existing registry's operational schema floor adjusted test-locally.
			store, err := logins.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			registryPath := filepath.Join(root, "logins", "registry.json")
			registryRaw, err := os.ReadFile(registryPath)
			if err != nil {
				t.Fatal(err)
			}
			var registryFields map[string]json.RawMessage
			if err := json.Unmarshal(registryRaw, &registryFields); err != nil {
				t.Fatal(err)
			}
			registryFields["schema_version"], _ = json.Marshal(logins.ClaudeProfileCustodySchemaVersion)
			registryRaw, _ = json.Marshal(registryFields)
			if err := os.WriteFile(registryPath, registryRaw, 0600); err != nil {
				t.Fatal(err)
			}
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
			rt := logins.Runtime{Provider: logins.ProviderClaude, Epoch: 1, Identity: strings.Repeat("a", 64), Home: filepath.Join(root, "logins", "provider", "claude", "profiles", strings.Repeat("a", 64))}
			m := persist.Meta{SchemaVersion: persist.ClaudeSchemaVersion, ID: "owned", AgentType: logins.ProviderClaude, Cwd: "/project", LoginRuntime: &rt, CLIIdentity: &persist.CLIIdentity{Path: "/synthetic/claude", Version: "2.1.310", Fingerprint: strings.Repeat("b", 64)}, Status: status.Status{Process: status.ProcessLost}}
			native := logins.NativeProcessInfo{SchemaVersion: logins.ClaudeNativeWriterSchemaVersion, Generation: strings.Repeat("c", 32), ShimPID: child.Process.Pid, ShimStartTime: started, Provider: logins.ProviderClaude, Runtime: rt, SessionID: m.ID, Cwd: m.Cwd, CLIFingerprint: m.CLIIdentity.Fingerprint}
			proof := logins.NativeStoppedInfo{SchemaVersion: logins.ClaudeNativeWriterSchemaVersion, Native: native, WritersStopped: true, DescendantsContained: true}
			switch kind {
			case "wrong-cwd":
				native.Cwd = "/foreign"
			case "wrong-session":
				native.SessionID = "foreign"
			case "wrong-generation":
				proof.Native.Generation = strings.Repeat("d", 32)
			case "uncontained":
				proof.DescendantsContained = false
			}
			if kind == "wrong-cwd" || kind == "wrong-session" {
				proof.Native = native
			}
			if err := os.Mkdir(filepath.Join(root, m.ID), 0700); err != nil {
				t.Fatal(err)
			}
			values := map[string]any{"meta.json": m, logins.NativeProcessFile: native}
			if kind != "missing-stop" {
				values[logins.NativeStoppedFile] = proof
			}
			for name, value := range values {
				raw, _ := json.Marshal(value)
				if err := os.WriteFile(filepath.Join(root, m.ID, name), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			err = verifyLoginWritersStopped(root, m)
			if kind == "exact-stopped" {
				if err != nil {
					t.Fatalf("exact owned zeroPID scope refused: %v", err)
				}
			} else if err == nil {
				t.Fatal("mismatched owned proof accepted")
			}
		})
	}
}
