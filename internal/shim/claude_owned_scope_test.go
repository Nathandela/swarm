package shim

import (
	"encoding/json"
	"fmt"
	"github.com/Nathandela/swarm/internal/persist"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/logins"
)

// Native schema4 is the owned-Claude wire contract. Scope preparation, refusal,
// and a failed/uncertain preclaim confer no authority to publish stop evidence.
func TestClaudeOwnedScopeUnadmittedFinishPreservesWinner(t *testing.T) {
	dir := t.TempDir()
	winner := NativeProcessInfo{SchemaVersion: 3, Generation: "11111111111111111111111111111111", Provider: logins.ProviderClaude}
	native, _ := json.Marshal(winner)
	stopped := []byte("retained winner stop bytes")
	if err := os.WriteFile(filepath.Join(dir, NativeProcessFile), native, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, NativeStoppedFile), stopped, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	loser := &managedNativeScope{root: root, info: NativeProcessInfo{SchemaVersion: 4, Generation: "22222222222222222222222222222222", Provider: logins.ProviderClaude}}
	if err := loser.finish(); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]byte{NativeProcessFile: native, NativeStoppedFile: stopped} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != string(want) {
			t.Fatalf("refused shim changed %s: %v", name, err)
		}
	}
}

func TestClaudeOwnedScopeWithoutDurablePreclaimCannotPublishStop(t *testing.T) {
	for _, visible := range []bool{false, true} {
		t.Run(map[bool]string{false: "prepared", true: "uncertain-preclaim"}[visible], func(t *testing.T) {
			dir := t.TempDir()
			info := NativeProcessInfo{SchemaVersion: 4, Generation: "33333333333333333333333333333333", Provider: logins.ProviderClaude}
			raw, _ := json.Marshal(info)
			if visible {
				if err := os.WriteFile(filepath.Join(dir, NativeProcessFile), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := (&managedNativeScope{root: root, info: info}).finish(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(filepath.Join(dir, NativeStoppedFile)); !os.IsNotExist(err) {
				t.Fatalf("unadmitted scope published stopped proof: %v", err)
			}
			got, err := os.ReadFile(filepath.Join(dir, NativeProcessFile))
			if visible {
				if err != nil || string(got) != string(raw) {
					t.Fatalf("uncertain preclaim was replaced: %v", err)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("prepared scope published native proof: %v", err)
			}
		})
	}
}

func TestClaudeOwnedScopePreparationDoesNotRemoveWinnerProof(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "owned")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	before := map[string][]byte{NativeProcessFile: []byte("retained winner native bytes"), NativeStoppedFile: []byte("retained winner stop bytes")}
	for name, raw := range before {
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(self, "-test.run=^TestClaudeOwnedPrepareHelper$")
	child.Env = append(os.Environ(), "SWARM_TEST_CLAUDE_SCOPE_ROOT="+root)
	if out, err := child.CombinedOutput(); err != nil {
		t.Fatalf("scope preparation child: %v %s", err, out)
	}
	for name, want := range before {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != string(want) {
			t.Fatalf("preparation changed winner %s: %v", name, err)
		}
	}
}

func TestClaudeOwnedPrepareHelper(t *testing.T) {
	root := os.Getenv("SWARM_TEST_CLAUDE_SCOPE_ROOT")
	if root == "" {
		t.Skip("dedicated subreaper test child only")
	}
	identity := strings.Repeat("a", 64)
	cfg := Config{NativeProvider: logins.ProviderClaude, LoginStateRoot: root, SessionID: "owned", SessionDir: filepath.Join(root, "owned"), Cwd: "/project", Argv: []string{"/synthetic/claude"}, LoginRuntime: &logins.Runtime{Provider: logins.ProviderClaude, Epoch: 1, Identity: identity, Home: filepath.Join(root, "logins", "provider", "claude", "profiles", identity)}, CLIIdentity: &persist.CLIIdentity{Path: "/synthetic/claude", Version: "2.1.310", Fingerprint: strings.Repeat("b", 64)}}
	scope, err := beginManagedNativeScope(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := scope.finish(); err != nil {
		t.Fatal(err)
	}
	fmt.Println("prepared and closed without admission")
}

func TestClaudeOwnedScopeUnclaimedRecordPreservesWinner(t *testing.T) {
	dir := t.TempDir()
	winner := []byte("retained winner")
	if err := os.WriteFile(filepath.Join(dir, NativeProcessFile), winner, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	scope := &managedNativeScope{root: root, info: NativeProcessInfo{SchemaVersion: ClaudeNativeWriterSchemaVersion}}
	if err := scope.record(os.Getpid(), nil); err == nil {
		t.Error("unclaimed scope published native identity")
	}
	_ = scope.finish()
	got, err := os.ReadFile(filepath.Join(dir, NativeProcessFile))
	if err != nil || string(got) != string(winner) {
		t.Fatalf("unclaimed record changed winner: %v", err)
	}
}

func TestClaudeOwnedScopeFinishRejectsChangedPreclaim(t *testing.T) {
	dir := t.TempDir()
	foreign := []byte("foreign claimed scope")
	if err := os.WriteFile(filepath.Join(dir, NativeProcessFile), foreign, 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	scope := &managedNativeScope{root: root, ownPreclaimDurable: true, info: NativeProcessInfo{SchemaVersion: ClaudeNativeWriterSchemaVersion, Generation: strings.Repeat("a", 32)}}
	if err := scope.finish(); err == nil {
		t.Error("changed preclaim granted stop publication")
	}
	got, err := os.ReadFile(filepath.Join(dir, NativeProcessFile))
	if err != nil || string(got) != string(foreign) {
		t.Fatalf("finish changed foreign scope: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, NativeStoppedFile)); !os.IsNotExist(err) {
		t.Fatal("changed preclaim published stop proof")
	}
}
