package upgrade

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/accountcheck"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
)

func collectionGuardFixture(t *testing.T) (string, string, map[string]any) {
	t.Helper()
	state := t.TempDir()
	if err := os.Chmod(state, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := accounts.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	gc := filepath.Join(state, "accounts", accountcheck.CollectionDirectory)
	if err := os.Mkdir(gc, 0o700); err != nil {
		t.Fatal(err)
	}
	start, err := procstart.StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	ref := accountcheck.Ref{SchemaVersion: accountcheck.SchemaVersion, Generation: strings.Repeat("a", 32), Worker: processcontain.Identity{PID: os.Getpid(), StartTime: start + 1}, Binding: accounts.Binding{SchemaVersion: 1, Provider: accounts.ProviderCodex, AccountID: strings.Repeat("b", 32), Identity: strings.Repeat("c", 64), CredentialGeneration: 1, ConfigurationGeneration: 1}, Mode: accountcheck.ModeAuthStatus}
	intent := map[string]any{"schema_version": accountcheck.CollectionSchemaVersion, "ref": ref, "stopped": accountcheck.Stopped{SchemaVersion: accountcheck.SchemaVersion, Ref: ref, WritersStopped: true}, "files": []map[string]any{{"path": ".", "device": 1, "inode": 1, "directory": true}, {"path": accountcheck.WorkerFile, "device": 1, "inode": 2, "directory": false}, {"path": accountcheck.StoppedFile, "device": 1, "inode": 3, "directory": false}}}
	return state, filepath.Join(gc, ref.Generation+".json"), intent
}

func writeCollectionGuardFixture(t *testing.T, path string, intent map[string]any) {
	t.Helper()
	raw, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAccountGuardCollectionWithoutJournalOrChecks(t *testing.T) {
	state, path, intent := collectionGuardFixture(t)
	writeCollectionGuardFixture(t, path, intent)
	current := CurrentManifest("v0.15.0")
	legacy := current
	legacy.AccountWorker = 1
	if err := accountStateGuard(state, legacy); err == nil {
		t.Fatal("GC-only state ignored required Worker2 custody")
	}
	if err := accountStateGuard(state, current); err != nil {
		t.Fatal(err)
	}
	// Intent-only state is inactive, after deletion or before the move. It is
	// not a proof used to grant admission or native history transfer.
	if !accountcheck.WritersStoppedForBinding(state, intent["ref"].(accountcheck.Ref).Binding) {
		t.Fatal("valid inactive GC state blocked absent check inventory")
	}
	intent["schema_version"] = 999
	writeCollectionGuardFixture(t, path, intent)
	if err := accountStateGuard(state, current); err == nil {
		t.Fatal("future GC authority accepted")
	}
	if accountcheck.WritersStoppedForBinding(state, intent["ref"].(accountcheck.Ref).Binding) {
		t.Fatal("malformed moved custody invisible to transfer")
	}
}

func TestAccountGuardCollectionRefusesMalformedProofOrUnsafePaths(t *testing.T) {
	for _, mode := range []string{"worker-zero", "native-zero-mismatch", "not-stopped", "wrong-generation", "unknown-mode", "unproved-directory", "symlink", "hardlink"} {
		t.Run(mode, func(t *testing.T) {
			state, path, intent := collectionGuardFixture(t)
			ref := intent["ref"].(accountcheck.Ref)
			proof := intent["stopped"].(accountcheck.Stopped)
			switch mode {
			case "worker-zero":
				ref.Worker = processcontain.Identity{}
				proof.Ref = ref
				intent["ref"] = ref
				intent["stopped"] = proof
			case "native-zero-mismatch":
				proof.Native.StartTime = 1
				intent["stopped"] = proof
			case "not-stopped":
				proof.WritersStopped = false
				intent["stopped"] = proof
			case "wrong-generation":
				ref.Generation = strings.Repeat("d", 32)
				proof.Ref = ref
				intent["ref"] = ref
				intent["stopped"] = proof
			case "unknown-mode":
				ref.Mode = "unknown"
				proof.Ref = ref
				intent["ref"] = ref
				intent["stopped"] = proof
			case "unproved-directory":
				if err := os.Mkdir(strings.TrimSuffix(path, ".json"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			writeCollectionGuardFixture(t, path, intent)
			if mode == "symlink" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(state, "accounts", "registry.json"), path); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "hardlink" {
				if err := os.Link(path, filepath.Join(state, "alias")); err != nil {
					t.Fatal(err)
				}
			}
			if err := accountStateGuard(state, CurrentManifest("v0.15.0")); err == nil {
				t.Fatal("unknown GC custody accepted", mode)
			}
		})
	}
}
