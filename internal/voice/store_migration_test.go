package voice

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
	bolt "go.etcd.io/bbolt"
)

// The fixture contains the bbolt file, initialization marker and independent
// Snapshot from the unmodified 7c05deb4 writer using its public Store methods.
func restoreLegacyStore(t *testing.T) (State, string) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "controller-store-v2.json.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }()
	var fixture struct {
		Writer string `json:"writer"`
		DB     []byte `json:"db"`
		Marker []byte `json:"marker"`
		State  State  `json:"state"`
	}
	if err = json.NewDecoder(gz).Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Writer != "7c05deb4" {
		t.Fatal("unexpected v2 fixture writer:", fixture.Writer)
	}
	dir := t.TempDir()
	if err = os.WriteFile(filepath.Join(dir, "voice.db"), fixture.DB, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "voice.initialized"), fixture.Marker, 0600); err != nil {
		t.Fatal(err)
	}
	return fixture.State, dir
}

func TestVoiceStoreMigratesV2WithOriginalAuthorityAndHistory(t *testing.T) {
	state, dir := restoreLegacyStore(t)
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	got, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	state.Generation++
	state.Workers = map[string]WorkerAssociation{}
	conversation := state.Conversations[state.CurrentConversation]
	b := conversation.Binding
	conversation.CustodyLost = true
	conversation.SchedulerPaused = true
	conversation.LastSpeechEpoch = 4
	state.Conversations[b.ConversationID] = conversation
	activeKey := admissionKey(b.ConversationID, 2)
	active := state.Admissions[activeKey]
	active.Phase = "uncertain"
	active.Reason = "legacy admission has no recorded native observer"
	state.Admissions[activeKey] = active
	p := state.Permits[key(b.ConversationID, "prior-turn")]
	p.Revoked = true
	state.Permits[key(b.ConversationID, "prior-turn")] = p
	if !reflect.DeepEqual(got, state) {
		t.Fatalf("migration changed durable records or replay fences\ngot  %+v\nwant %+v", got, state)
	}
	if err = s.db.View(func(tx *bolt.Tx) error {
		h, err := readHeader(tx)
		if err == nil && h.Version != storeSchemaVersion {
			t.Fatalf("store was not rewritten: %+v", h)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	legacy := conversation.Binding
	legacy.Generation = state.Generation
	next := b
	next.SessionID = "machine/fresh"
	next.SessionInstance = "fresh-instance"
	next.NativeGeneration = "fresh-native"
	next.ObservationGeneration = "fresh-observer"
	refs := ManagerBridgeRefs{Socket: "/private/bridge.sock", AuthFile: "/private/bridge.auth"}
	base := &controllerRuntimeFixture{}
	runtime := &controllerCustodyRuntime{controllerRuntimeFixture: base, next: protocol.ManagerRuntimeStatus{
		Binding: runtimeBinding(next), ReadyForInput: true, BridgeSocket: refs.Socket, AuthFile: refs.AuthFile,
	}}
	controller, err := NewController(s, runtime, legacy, refs)
	if err != nil {
		t.Fatal(err)
	}
	if err = controller.Tick(context.Background()); !errors.Is(err, managerproof.ErrQuarantined) {
		t.Fatalf("migrated binding reached native dispatch: %v", err)
	}
	controller.workers = &controllerWorkerFixture{rows: []protocol.SessionView{
		{ID: legacy.SessionID, Role: "manager", Agent: legacy.Provider, ManagerConversationID: b.ConversationID,
			Status: status.Status{Process: status.ProcessExited}, Capabilities: &protocol.SessionCapabilities{SessionInstance: legacy.SessionInstance}},
		{ID: next.SessionID, Role: "manager", Agent: next.Provider, ManagerConversationID: b.ConversationID,
			Status: status.Status{Process: status.ProcessRunning}, Capabilities: &protocol.SessionCapabilities{SessionInstance: next.SessionInstance}},
	}}
	replacement, err := controller.ReplaceManager(context.Background(), next, refs)
	if err != nil || replacement.Binding().SessionID != next.SessionID {
		t.Fatalf("explicit fresh replacement failed: %v", err)
	}
	got, err = s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(base.sent) != 0 || !got.Admissions[activeKey].NativeBindingRetired || got.Admissions[activeKey].Phase != "uncertain" ||
		!reflect.DeepEqual(got.Actions, state.Actions) || got.Conversations[b.ConversationID].RetiredThrough != conversation.RetiredThrough ||
		got.Wakes[key(b.ConversationID, "wake")].State != "retired" {
		t.Fatal("fresh replacement lost old authority or replayed archived work")
	}

}

func TestVoiceStoreRefusesCorruptV2BeforeMigration(t *testing.T) {
	for _, corrupt := range []string{"header digest", "record digest", "record count", "logical bytes"} {
		t.Run(corrupt, func(t *testing.T) {
			st, dir := restoreLegacyStore(t)
			path := filepath.Join(dir, "voice.db")
			db, err := bolt.Open(path, 0600, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = db.Update(func(tx *bolt.Tx) error {
				h, err := readHeader(tx)
				if err != nil {
					return err
				}
				switch corrupt {
				case "header digest":
					return tx.Bucket([]byte("controller")).Put([]byte("digest"), []byte("broken"))
				case "record digest":
					h.RecordHash[0] ^= 1
				case "record count":
					h.Records++
				case "logical bytes":
					h.LogicalBytes++
				}
				return writeHeader(tx, h)
			})
			if err = errors.Join(err, db.Close()); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenStore(dir)
			if reopened != nil {
				_ = reopened.Close()
			}
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("invalid v2 authority migrated: %v", err)
			}
			db, err = bolt.Open(path, 0600, &bolt.Options{ReadOnly: true})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			if err = db.View(func(tx *bolt.Tx) error {
				var h storeHeader
				if err := json.Unmarshal(tx.Bucket([]byte("controller")).Get([]byte("state")), &h); err != nil {
					return err
				}
				if h.Version != 2 || h.Generation != st.Generation {
					t.Fatalf("rejected v2 store was rewritten: %+v", h)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
