package upgrade

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shimwire"
)

func writeClaudeChatFutureHeader(t *testing.T, state string) string {
	t.Helper()
	dir := filepath.Join(state, "chat-six")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "meta.json")
	// Deliberately not an accepted managed row: updater discovery must inspect
	// the future header even when the normal registry reader omits the row.
	if err := os.WriteFile(path, []byte(`{"schema_version":6,"id":"chat-six","required_runtime":"claude-chat"}`), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestClaudeChatCompatibilityCardAdvertisesSix(t *testing.T) {
	if card := CurrentManifest("test"); card.Schema != 6 {
		t.Fatalf("card does not advertise the new managed chat reader: schema=%d", card.Schema)
	}
}

func TestClaudeChatRawHeaderDiscoveryDoesNotDependOnScan(t *testing.T) {
	state := t.TempDir()
	writeClaudeChatFutureHeader(t, state)
	store, err := persist.NewStore(state)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.Scan()
	if err != nil || len(rows) != 0 {
		t.Fatalf("unsupported unmanaged6 unexpectedly became accepted: rows=%d err=%v", len(rows), err)
	}
	need, id, err := maxPersistedSchema(state)
	if err != nil || need != 6 || id != "chat-six" {
		t.Fatalf("future header hidden from updater: need=%d id=%q err=%v", need, id, err)
	}
}

func TestClaudeChatActivationAndRollbackRefuseOlderCard(t *testing.T) {
	for _, action := range []string{"activate", "rollback"} {
		t.Run(action, func(t *testing.T) {
			state := t.TempDir()
			bin := installTarget(t)
			before, err := os.ReadFile(bin)
			if err != nil {
				t.Fatal(err)
			}
			writeClaudeChatFutureHeader(t, state)
			card := CurrentManifest("v0.0.9")
			card.Schema, card.Shimwire = 5, shimwire.Version
			if action == "activate" {
				stageBuild(t, state, "v9.9.9", &card)
			} else {
				prev := PrevDir(state)
				if err := os.MkdirAll(prev, 0700); err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(card)
				if err != nil {
					t.Fatal(err)
				}
				for name, body := range map[string][]byte{"swarm": []byte("unused old binary"), "VERSION": []byte("v0.0.9"), "compat.json": raw} {
					if err := os.WriteFile(filepath.Join(prev, name), body, 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			calls := captureExec(t)
			opts := ActivateOptions{StateDir: state, BinPath: bin, DaemonAlive: func() bool { return false }}
			var result State
			if action == "activate" {
				result, err = Activate(opts)
			} else {
				result, err = Rollback(opts)
			}
			if err != nil || result.Outcome != "refused-schema" || len(*calls) != 0 {
				t.Fatalf("older card reached installation/handoff: outcome=%q calls=%d err=%v", result.Outcome, len(*calls), err)
			}
			after, err := os.ReadFile(bin)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("schema refusal changed installed bytes: %v", err)
			}
		})
	}
}

func TestClaudeChatSchemaDiscoveryKeepsOrdinaryHeader(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "ordinary")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"schema_version":1,"id":"ordinary"}`), 0600); err != nil {
		t.Fatal(err)
	}
	need, id, err := maxPersistedSchema(state)
	if err != nil || need != 1 || id != "ordinary" {
		t.Fatalf("ordinary header changed: need=%d id=%q err=%v", need, id, err)
	}
}
