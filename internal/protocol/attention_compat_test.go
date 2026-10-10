package protocol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

func TestAttentionErrorPersistsAndCrossesVersionOneRosterWire(t *testing.T) {
	root := t.TempDir()
	store, err := persist.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	want := statusMeta("attention-session", status.TurnIdle, status.InteractionError)
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, want.ID, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	// The existing string-based reader requires no new schema to retain the value.
	var legacy struct {
		SchemaVersion int `json:"schema_version"`
		Status        struct{ Process, Turn, Interaction string }
	}
	if err := json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.SchemaVersion != 1 || legacy.Status.Interaction != "error" {
		t.Fatalf("existing metadata schema lost additive error: %+v", legacy)
	}
	reopened, err := persist.NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reopened.Load(want.ID)
	if err != nil || loaded.Status != want.Status {
		t.Fatalf("reloaded error status=%+v, err=%v", loaded.Status, err)
	}
	stub := newStubDaemon()
	stub.setMetas(loaded)
	client := rawDial(t, serveStub(t, stub))
	hello := client.hello(1, []string{CapSubscribe})
	if hello.Op != OpHello || hello.ProtocolVersion != 1 {
		t.Fatalf("existing version-one handshake refused: %+v", hello)
	}
	client.writeControl(Control{Op: OpList, EndpointID: hello.EndpointID})
	list := client.readControl()
	if list.Op != OpList || len(list.Sessions) != 1 {
		t.Fatalf("roster reply: %+v", list)
	}
	assertView := func(view SessionView) {
		t.Helper()
		if view.ID != NamespacedID(hello.EndpointID, want.ID) || view.Group != status.GroupNeedsInput || view.Status != want.Status {
			t.Fatalf("wire lost error dimensions or existing Needs input group: %+v", view)
		}
	}
	assertView(list.Sessions[0])
	client.writeControl(Control{Op: OpSubscribe, EndpointID: hello.EndpointID})
	if ack := client.readControl(); ack.Op != OpOK {
		t.Fatalf("subscribe refused: %+v", ack)
	}
	stub.pushStatus(loaded)
	event := client.readControl()
	if event.Op != OpEvent || event.Session == nil {
		t.Fatalf("event reply: %+v", event)
	}
	assertView(*event.Session)
}
