package upgrade

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
)

func TestCurrentManifestCoversPersistedHermesRuntime(t *testing.T) {
	state := t.TempDir()
	store, err := persist.NewStore(state)
	if err != nil {
		t.Fatal(err)
	}
	meta := persist.Meta{ID: "native-hermes", AgentType: "hermes", HermesNative: true, CLIIdentity: &persist.CLIIdentity{Version: "0.22.5"}}
	if err = store.Save(meta); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Load(meta.ID); err != nil {
		t.Fatal(err)
	}
	need, _, err := maxPersistedSchema(state)
	if err != nil {
		t.Fatal(err)
	}
	card := CurrentManifest("v0.24.0")
	if card.Schema < need {
		t.Fatalf("release card refuses its own native record: supports=%d persisted=%d", card.Schema, need)
	}
	if persist.LoginSchemaVersion >= need {
		t.Fatal("old compatibility card unexpectedly accepts native ownership metadata")
	}
}

func TestHermesRuntimeUpgradeAndRollbackSchemaGuards(t *testing.T) {
	for _, old := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "old-reader"}[old], func(t *testing.T) {
			state := t.TempDir()
			store, err := persist.NewStore(state)
			if err != nil {
				t.Fatal(err)
			}
			if err = store.Save(persist.Meta{ID: "native", AgentType: "hermes", HermesNative: true, CLIIdentity: &persist.CLIIdentity{Version: "0.22.5"}}); err != nil {
				t.Fatal(err)
			}
			card := CurrentManifest("v9.9.9")
			if old {
				card.Schema = persist.LoginSchemaVersion
			}
			stageBuild(t, state, "v9.9.9", &card)
			bin := installTarget(t)
			calls := captureExec(t)
			result, err := Activate(ActivateOptions{StateDir: state, BinPath: bin, DaemonAlive: noDaemon()})
			if err != nil {
				t.Fatal(err)
			}
			want := "activated"
			wantCalls := 1
			if old {
				want = "refused-schema"
				wantCalls = 0
			}
			if result.Outcome != want || len(*calls) != wantCalls {
				t.Fatal(result, *calls)
			}
			if old {
				return
			}
			previous := PrevDir(state)
			legacy := CurrentManifest("v0.23.1")
			legacy.Schema = persist.LoginSchemaVersion
			raw, _ := json.Marshal(legacy)
			if err = os.WriteFile(filepath.Join(previous, "compat.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			*calls = nil
			result, err = Rollback(ActivateOptions{StateDir: state, BinPath: bin})
			if err != nil {
				t.Fatal(err)
			}
			if result.Outcome != "refused-schema" || len(*calls) != 0 {
				t.Fatal("native workspace could be handed to an old reader", result, *calls)
			}
		})
	}
}
