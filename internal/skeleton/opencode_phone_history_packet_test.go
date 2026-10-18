package skeleton

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// Real unsigned/sealed phone read, gateway remote socket, native producer and
// durable sealed reply fold. Only provider snapshots and epoch enrollment are fixtures.
func TestOpenCodePhoneHistoryPacketUsesNativeCursorWithoutJournalAuthority(t *testing.T) {
	d, socket := assembleWithRemote(t)
	f := newOpenCodeSendFixture(t, func(f *openCodeSendFixture) { f.d = d })
	caps, ok := d.sessionCapabilities(f.binding.WorkerID)
	if !ok || caps.NativeInteractionVersion != 2 {
		t.Fatal("missing current native read proof")
	}
	registerPhone(t, d, device.CapReadOnly)
	d.stopOpenCodeObservation(f.binding.WorkerID) // history must not need a live writer/projector
	meta, _ := d.core.Get(f.binding.WorkerID)
	source := newNativePageReadFake(t)
	source.meta = meta
	source.rows = []json.RawMessage{source.row(1000, 1, 20), source.row(2000, 1, 20), source.row(3000, 1, 20)}
	var sdkReads atomic.Int32
	f.historyPage.Store(func(limit int, before string) (opencodebridge.Snapshot, error) {
		sdkReads.Add(1)
		page, err := source.read(context.Background(), limit, before)
		page.Session = json.RawMessage(strings.ReplaceAll(string(page.Session), `"created":1`, `"created":123`))
		return opencodebridge.Snapshot{Identity: f.identity, Session: page.Session, Messages: page.Messages, NextCursor: page.NextCursor, Statuses: map[string]json.RawMessage{}, Permissions: []json.RawMessage{}, Questions: []json.RawMessage{}, Children: []json.RawMessage{}, Descendants: []json.RawMessage{}, HierarchyComplete: true, HierarchyScope: openCodeHierarchyScope}, err
	})
	phone, err := phonecore.Resume(phonecore.Config{Dir: t.TempDir(), Machine: d.api.endpointID, WakeSealer: s18NewSealer(t), ContentSealer: s18NewSealer(t)})
	if err != nil {
		t.Fatal(err)
	}
	keys, err := crypto.NewEpochKeys()
	if err != nil {
		t.Fatal(err)
	}
	authority := bytes.Repeat([]byte{0x44}, 32)
	journalBucket := phonecore.Bucket{Sender: [8]byte{9}, Epoch: 1}
	if err := phone.Mutate(func(st *phonecore.State) {
		st.EpochID = 1
		st.Keys = keys
		st.MachineRelayAuthPub = authority
		st.OperatorNamespace = "owner"
		st.RoutingID = "native-history-phone"
		st.Sessions = []phonecore.CachedSession{{SessionID: f.request.Session, Capabilities: &caps}}
		st.Receive = map[phonecore.Bucket]uint64{journalBucket: 77}
	}); err != nil {
		t.Fatal(err)
	}
	beforeJournal, err := d.core.JournalReadFrom(0)
	if err != nil {
		t.Fatal(err)
	}
	gw := remotegw.New(socket, nil)
	recv := crypto.NewMailboxReceiver()
	read := func(id, before string, sequence uint64) schema.NativeHistoryPage {
		t.Helper()
		req := phone.HistoryRequest(f.request.Session, before, 1)
		if !req.Native {
			t.Fatal("held native content selected journal fallback")
		}
		if sequence == 2 && req.NativeCursor != phone.State().NativeHistory[f.request.Session].Cursor {
			t.Fatal("continuation did not use exact retained native cursor")
		}
		auth := schema.DeviceCommandAuth{Action: schema.ActionInteractionHistory, Machine: d.api.endpointID, Session: f.request.Session, OperationID: id}
		p := phonecore.PendingPublication{LogicalID: id, OperationID: id, Kind: phonecore.PublicationHistory, SessionID: req.Session, Machine: d.api.endpointID, EpochID: 1, Target: "machine-mailbox", AuthorityPub: authority, Command: auth, History: &req, CreatedAt: time.Now()}
		if err := phone.PreparePublication(p); err != nil {
			t.Fatal(err)
		}
		packet, err := phonecore.SealInteractionHistoryEnvelope(keys.ContentKey, 1, sequence, auth, &req)
		if err != nil {
			t.Fatal(err)
		}
		if err := phone.SealPublication(id, sequence, packet); err != nil {
			t.Fatal(err)
		}
		frame, err := remotegw.OpenMailboxFrame(recv, keys.ContentKey, packet)
		if err != nil || frame.Command.History == nil || frame.Command.Sig != "" || frame.Command.History.NativeCursor != req.NativeCursor {
			t.Fatalf("unsigned native read changed: %+v %v", frame, err)
		}
		reply, err := gw.ForwardCommand(protocol.OpInteractionHistory, frame.Command)
		if err != nil || reply.Op != protocol.OpInteractionHistory || reply.NativeHistory == nil {
			t.Fatalf("connected native history refused: %+v %v", reply, err)
		}
		page := *reply.NativeHistory
		sealed, err := remotegw.SealControlReply(keys.ContentKey, 1, sequence, reply)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := phone.Router().AcceptCommit(sealed, sequence); err != nil {
			t.Fatal(err)
		}
		if _, ok := phone.State().OpOutcomes[id]; !ok || phone.State().OpOutcomes[id].NativeHistory != nil {
			t.Fatal("native page was not atomically folded with payload-free read verdict")
		}
		return page
	}
	first := read("native-history-first", "", 1)
	held := phone.Router().Items().Session(f.request.Session)
	if len(held) != 1 || first.Floor || first.NextCursor == "" || phone.State().NativeHistory[f.request.Session].Cursor != first.NextCursor {
		t.Fatalf("first native page/cursor not retained: %+v", held)
	}
	if !strings.Contains(held[0].Text, "row3000:") {
		t.Fatal("initial native page was not latest")
	}
	second := read("native-history-second", held[0].ItemID, 2)
	held = phone.Router().Items().Session(f.request.Session)
	if len(held) != 2 || second.Scope != first.Scope || second.NextCursor == first.NextCursor || !strings.Contains(held[0].Text, "row2000:") {
		t.Fatalf("native continuation repeated or reordered history: %+v", held)
	}
	for _, item := range held {
		var fields map[string]any
		if json.Unmarshal(item.Body, &fields) != nil {
			t.Fatal("invalid imported item")
		}
		if item.Cursor != 0 || item.LastCursor != 0 || !item.Backfilled || item.OperationID != "" || item.Source == "phone" || item.Source == "owner" || fields["provenance"] != "imported" || fields["authority_valid"] == true || strings.Contains(item.Kind, "request") {
			t.Fatalf("history acquired journal/owner/request authority: %+v", item)
		}
	}
	if phone.State().Receive[journalBucket] != 77 || f.calls.Load() != 0 {
		t.Fatal("readonly native history changed live journal highwater or submitted input")
	}
	afterJournal, err := d.core.JournalReadFrom(0)
	if err != nil || afterJournal.Cursor != beforeJournal.Cursor {
		t.Fatal("native page synthesis published a journal record")
	}
	baseline := sdkReads.Load()
	// Exact existing capability gate, without a signing shortcut or provider request.
	remote := dialRemote(t, socket)
	req := schema.InteractionHistoryReq{Session: f.request.Session, Native: true, Limit: 1}
	remote.write(protocol.Control{Op: protocol.OpInteractionHistory, EndpointID: remote.endpointID, SessionID: req.Session, OperationID: "native-no-cap", History: &req})
	if got := remote.read(5 * time.Second); got.Op != protocol.OpError {
		t.Fatalf("unnegotiated native read passed: %+v", got)
	}
	if sdkReads.Load() != baseline {
		t.Fatal("missing journal capability reached native SDK")
	}
	if err := remoteControlSetterOf(t, d).SetRemoteControl(false); err != nil {
		t.Fatal(err)
	}
	reply, err := gw.ForwardCommand(protocol.OpInteractionHistory, protocol.RemoteCommand{DeviceCommandAuth: schema.DeviceCommandAuth{Action: schema.ActionInteractionHistory, Machine: d.api.endpointID, Session: req.Session, OperationID: "native-off"}, History: &req})
	if err != nil || reply.Op != protocol.OpError || reply.ErrorCode != protocol.CodeKillSwitch || sdkReads.Load() != baseline {
		t.Fatalf("remote off reached native read: %+v %v reads%d/%d", reply, err, sdkReads.Load(), baseline)
	}
}
