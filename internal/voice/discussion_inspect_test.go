package voice

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestInspectDiscussionKeepsParkedStoreReadOnly(t *testing.T) {
	id := strings.Repeat("a", 64)
	dir := filepath.Join(t.TempDir(), id)
	if _, err := InspectDiscussion(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing ledger: %v", err)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	b, err := s.BindManager(ManagerBinding{ConversationID: DiscussionConversationPrefix + id, Provider: "codex", SessionID: "host/manager", SessionInstance: "instance", NativeSessionID: "native", NativeGeneration: "native-gen", ObservationGeneration: "obs", LoginEpoch: "login"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.update(func(st *State) error {
		c := st.Conversations[b.ConversationID]
		c.Parked, c.Project = true, "/checkout"
		putRecord(st, "conversations", &st.Conversations, b.ConversationID, c)
		putRecord(st, "actions", &st.Actions, key(b.ConversationID, "pending"), Action{ID: "pending", ConversationID: b.ConversationID, State: "executing"})
		created := time.Now().UTC()
		for i := range 17 {
			id := fmt.Sprintf("queued-%02d", i)
			meta, err := json.Marshal(workerNoteMetadata{NoteID: id, Created: &created})
			if err != nil {
				return err
			}
			putRecord(st, "events", &st.Events, key(b.ConversationID, id), Event{ID: id, ConversationID: b.ConversationID, WorkerMessage: true, Metadata: meta})
			putRecord(st, "wakes", &st.Wakes, key(b.ConversationID, id), Wake{ID: id, ConversationID: b.ConversationID, EventID: id, State: "queued"})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectDiscussion(dir); !errors.Is(err, bolt.ErrTimeout) {
		t.Fatalf("leased store: %v", err)
	}
	controller := DiscussionController{dir: dir, cfg: Config{ConversationID: b.ConversationID}, store: s, now: time.Now}
	normal, err := controller.Status()
	if err != nil || len(normal.Notes) != 16 {
		t.Fatalf("normal status: %d notes, %v", len(normal.Notes), err)
	}
	all, err := controller.CheckoutStatus()
	if err != nil || len(all.Notes) != 17 {
		t.Fatalf("open ledger inventory: %d notes, %v", len(all.Notes), err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	ledger := filepath.Join(dir, "voice.db")
	before, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	view, err := InspectDiscussion(dir)
	if err != nil || view.State != "parked" || view.ManagerSession != b.SessionID || view.ManagerInstance != b.SessionInstance || view.Project != "/checkout" || view.PendingActions != 1 || len(view.Notes) != 17 {
		t.Fatalf("bad retained ownership %#v, %v", view, err)
	}
	after, err := os.ReadFile(ledger)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("inspection changed ledger", err)
	}
	other := filepath.Join(filepath.Dir(dir), strings.Repeat("b", 64))
	if err := os.Rename(dir, other); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectDiscussion(other); !errors.Is(err, ErrStale) {
		t.Fatalf("wrong discussion directory: %v", err)
	}
}
