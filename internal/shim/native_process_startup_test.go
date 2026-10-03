//go:build linux

package shim

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
)

func startupFixtureBinding() accounts.Binding {
	return accounts.Binding{Provider: accounts.ProviderCodex, AccountID: "startup-fixture", CredentialGeneration: 1, ConfigurationGeneration: 1, Identity: "synthetic-identity"}
}

func init() {
	if len(os.Args) != 3 || (os.Args[1] != "fixture-managed-selector-refusal" && os.Args[1] != "fixture-managed-unrecorded-child") {
		return
	}
	dir := os.Args[2]
	binding := startupFixtureBinding()
	cfg := Config{SessionDir: dir, AccountBinding: &binding, Argv: []string{os.Args[0]}}
	if os.Args[1] == "fixture-managed-selector-refusal" {
		// No account registry exists: failure must precede every native spawn,
		// but the dedicated shim still proves a clean, retryable exit.
		if _, err := Run(cfg); err == nil {
			os.Exit(101)
		}
		os.Exit(0)
	}
	scope, err := beginManagedNativeScope(cfg)
	if err != nil {
		os.Exit(102)
	}
	child := exec.Command(os.Args[0], "fixture-managed-fork", dir)
	child.Env = []string{}
	if child.Run() != nil {
		os.Exit(103)
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		if _, err := os.Stat(filepath.Join(dir, "writer.json")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = scope.finish()
			os.Exit(105)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := scope.finish(); err != nil {
		os.Exit(104)
	}
	os.Exit(0)
}

func TestManagedStartupFailurePublishesExactCleanScopeProof(t *testing.T) {
	for _, mode := range []string{"fixture-managed-selector-refusal", "fixture-managed-unrecorded-child"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			t.Cleanup(func() {
				var child processcontain.Identity
				raw, err := os.ReadFile(filepath.Join(dir, "writer.json"))
				if err == nil && json.Unmarshal(raw, &child) == nil && child.Alive() {
					_ = processcontain.SignalIdentity(child, syscall.SIGKILL)
				}
			})
			cmd := exec.Command(selfExe(t), mode, dir)
			cmd.Env = []string{}
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("startup fixture: %v: %s", err, output)
			}
			var native NativeProcessInfo
			raw, err := os.ReadFile(filepath.Join(dir, NativeProcessFile))
			if err != nil || json.Unmarshal(raw, &native) != nil {
				t.Fatal("clean startup failure has no native scope record")
			}
			var proof NativeStoppedInfo
			raw, err = os.ReadFile(filepath.Join(dir, NativeStoppedFile))
			if err != nil || json.Unmarshal(raw, &proof) != nil {
				t.Fatal("clean startup failure has no stopped proof")
			}
			if native.SchemaVersion != ManagedWriterSchemaVersion || native.PID != 0 || native.PGID != 0 || native.StartTime != 0 || native.ShimPID != cmd.Process.Pid || native.ShimStartTime <= 0 || len(native.Generation) != 32 || native.Binding != startupFixtureBinding() || proof.SchemaVersion != ManagedWriterSchemaVersion || !proof.WritersStopped || proof.Native != native {
				t.Fatal("startup proof does not match the exact clean shim scope")
			}
			if mode == "fixture-managed-unrecorded-child" {
				var child processcontain.Identity
				raw, err = os.ReadFile(filepath.Join(dir, "writer.json"))
				if err != nil || json.Unmarshal(raw, &child) != nil || child.PID <= 0 {
					t.Fatal("unrecorded escaped writer was not exercised")
				}
				if _, err := procstart.StartTime(child.PID); !os.IsNotExist(err) {
					t.Fatal("unrecorded escaped writer was not reaped before proof")
				}
			}
		})
	}
}
