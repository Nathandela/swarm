package phonecore

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestOperatorNamespacePersistsWithMachineAuthority(t *testing.T) {
	dir := t.TempDir()
	wake, content := phoneTestSealer(0x11), phoneTestSealer(0x22)
	core, err := Resume(Config{Dir: dir, Machine: "machine-a", WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := core.Mutate(func(st *State) {
		st.OperatorNamespace = "owner"
		st.MachineRelayAuthPub = bytes.Repeat([]byte{0x33}, ed25519.PublicKeySize)
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	restarted, err := Resume(Config{Dir: dir, Machine: "machine-a", WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("restart Resume: %v", err)
	}
	if got := restarted.State().OperatorNamespace; got != "owner" {
		t.Fatalf("OperatorNamespace after restart = %q, want owner", got)
	}
}

func TestCurrentPairedStateRejectsInvalidOperatorNamespace(t *testing.T) {
	for _, namespace := range []string{"", "Owner"} {
		t.Run(namespace, func(t *testing.T) {
			dir := t.TempDir()
			wake, content := phoneTestSealer(0x11), phoneTestSealer(0x22)
			core, err := Resume(Config{Dir: dir, Machine: "machine-a", WakeSealer: wake, ContentSealer: content})
			if err != nil {
				t.Fatalf("Resume: %v", err)
			}
			if err := core.Mutate(func(st *State) {
				st.OperatorNamespace = "owner"
				st.MachineRelayAuthPub = bytes.Repeat([]byte{0x33}, ed25519.PublicKeySize)
			}); err != nil {
				t.Fatalf("Mutate: %v", err)
			}
			path := filepath.Join(dir, StateFileName)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var blob map[string]any
			if err := json.Unmarshal(raw, &blob); err != nil {
				t.Fatal(err)
			}
			blob["operator_namespace"] = namespace
			raw, err = json.Marshal(blob)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Resume(Config{Dir: dir, Machine: "machine-a", WakeSealer: wake, ContentSealer: content}); err == nil {
				t.Fatalf("Resume accepted current paired state with operator namespace %q", namespace)
			}
		})
	}
}

func TestLegacyPairedStateRejectsMissingOperatorNamespaceAtFirstLoad(t *testing.T) {
	dir := t.TempDir()
	wake, content := phoneTestSealer(0x11), phoneTestSealer(0x22)
	core, err := Resume(Config{Dir: dir, Machine: "machine-a", WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := core.Mutate(func(st *State) {
		st.OperatorNamespace = "owner"
		st.MachineRelayAuthPub = bytes.Repeat([]byte{0x33}, ed25519.PublicKeySize)
	}); err != nil {
		t.Fatalf("Mutate: %v", err)
	}
	path := filepath.Join(dir, StateFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var blob map[string]any
	if err := json.Unmarshal(raw, &blob); err != nil {
		t.Fatal(err)
	}
	blob["schema_version"] = float64(21)
	delete(blob, "operator_namespace")
	raw, err = json.Marshal(blob)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Resume(Config{Dir: dir, Machine: "machine-a", WakeSealer: wake, ContentSealer: content}); err == nil {
		t.Fatal("Resume accepted a pre-v22 active pairing with no authenticated namespace")
	}
}

func TestSaveRefusesInvalidActiveNamespaceWithoutAdvancingDiskOrMemory(t *testing.T) {
	for _, namespace := range []string{"", "Owner"} {
		t.Run(namespace, func(t *testing.T) {
			dir := t.TempDir()
			core, err := Resume(Config{Dir: dir, Machine: "machine-a", WakeSealer: phoneTestSealer(0x11), ContentSealer: phoneTestSealer(0x22)})
			if err != nil {
				t.Fatal(err)
			}
			if err := core.Mutate(func(st *State) { st.MachineName = "before" }); err != nil {
				t.Fatalf("seed: %v", err)
			}
			path := filepath.Join(dir, StateFileName)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := core.Mutate(func(st *State) {
				st.OperatorNamespace = namespace
				st.MachineRelayAuthPub = bytes.Repeat([]byte{0x33}, ed25519.PublicKeySize)
			}); err == nil {
				t.Fatalf("Mutate accepted active relay authority with operator namespace %q", namespace)
			}
			if got := core.State(); len(got.MachineRelayAuthPub) != 0 || got.OperatorNamespace != "" {
				t.Fatalf("failed Mutate advanced memory: pub=%x namespace=%q", got.MachineRelayAuthPub, got.OperatorNamespace)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, before) {
				t.Fatal("failed Mutate changed the durable state")
			}
		})
	}
}

func TestDisownedStateMayRetainPublicKeyWithoutLiveNamespace(t *testing.T) {
	core, err := Resume(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := core.Mutate(func(st *State) {
		st.Disowned = true
		st.MachineRelayAuthPub = bytes.Repeat([]byte{0x33}, ed25519.PublicKeySize)
	}); err != nil {
		t.Fatalf("disowned Mutate: %v", err)
	}
}
