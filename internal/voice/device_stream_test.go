package voice

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/Nathandela/swarm/internal/voicewire"
)

func TestDeviceSessionNegotiationAndDurableGapReset(t *testing.T) {
	store, binding, dir := testStore(t)
	controller := &Controller{store: store, binding: binding}
	control := &managerControl{store: store, controller: controller, binding: binding}
	page := func(c *managerControl, msg voicewire.ClientMessage) (voicewire.DeviceStatusPage, error) {
		raw, err := json.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		return c.deviceStatusPage("alice", "place", raw)
	}
	const alice = "alice.00112233445566778899aabbccddeeff"
	msg := voicewire.ClientMessage{Type: voicewire.TypeHandshake, Version: 2, Sequence: 1, SessionID: alice}
	if _, err := page(control, msg); !errors.Is(err, ErrInvalid) {
		t.Fatal("wrong protocol version", err)
	}
	msg.Version = voicewire.ProtocolVersion
	msg.SessionID = "bob"
	if _, err := page(control, msg); !errors.Is(err, ErrInvalid) {
		t.Fatal("other device subscribed", err)
	}
	msg.SessionID = alice
	initial, err := page(control, msg)
	if err != nil || !initial.Reset || len(initial.Events) != 1 {
		t.Fatal(initial, err)
	}
	msg = voicewire.ClientMessage{Type: voicewire.TypeResume, Version: voicewire.ProtocolVersion, SessionID: alice, After: &initial.Head, Sequence: initial.Head.Sequence + 1}
	latest := control.statusStreams.devices["alice"]
	for i := 0; i <= MaxStreamEvents; i++ {
		if _, err := latest.replay.Append("status", initial.Events[0].Data); err != nil {
			t.Fatal(err)
		}
	}
	overflow, err := page(control, msg)
	if err != nil || !overflow.Reset || overflow.Events[0].Cursor.Sequence <= initial.Head.Sequence {
		t.Fatal(overflow, err)
	}
	// A restarted service reconstructs its snapshot from the ledger and owns a new stream namespace.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	nextBinding, err := reopened.BindManager(binding)
	if err != nil || nextBinding.Generation <= binding.Generation {
		t.Fatal("restart lost generation", err)
	}
	restart := &managerControl{store: reopened, controller: &Controller{store: reopened, binding: nextBinding}, binding: nextBinding}
	fresh, err := page(restart, msg)
	if err != nil || !fresh.Reset || fresh.Head.StreamID == initial.Head.StreamID {
		t.Fatal(fresh, err)
	}
	var resumed voicewire.DeviceStatus
	if err := json.Unmarshal(fresh.Events[0].Data, &resumed); err != nil || resumed.Generation != nextBinding.Generation {
		t.Fatal("snapshot does not identify reopened ledger", err)
	}
	msg.After = &fresh.Head
	msg.Sequence = fresh.Head.Sequence + 1
	unchanged, err := page(restart, msg)
	if err != nil || unchanged.Reset || len(unchanged.Events) != 0 || unchanged.Head != fresh.Head {
		t.Fatal(unchanged, err)
	}
}
