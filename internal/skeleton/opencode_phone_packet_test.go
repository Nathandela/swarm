package skeleton

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// These tests use real phone signing/envelopes, gateway forwarding, remote-tier packet
// authorization, daemon custody and the shim. Only the SDK-side native bridge is a fixture.
func nativePhonePacketFixture(t *testing.T, question bool) (*openCodeSendFixture, *remotegw.Gateway, protocol.NativeDecisionReq) {
	t.Helper()
	d, socket := assembleWithRemote(t)
	f := newOpenCodeSendFixture(t, func(f *openCodeSendFixture) { f.d = d; f.nativeDecisionMode = true; f.nativeQuestionMode = question })
	owner, err := protocol.Dial(d.SocketPath(), []string{protocol.CapNativeRequests})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	list, err := owner.NativeRequests(protocol.NativeRequestsReq{Session: f.request.Session})
	if err != nil || len(list.Requests) != 1 {
		t.Fatalf("native list: %+v %v", list, err)
	}
	full, err := owner.NativeRequests(protocol.NativeRequestsReq{Session: f.request.Session, RequestID: list.Requests[0].RequestID, NativeSessionID: list.Requests[0].NativeSessionID})
	if err != nil || full.Request == nil {
		t.Fatalf("native card: %+v %v", full, err)
	}
	c := full.Request
	body := protocol.NativeDecisionReq{Session: c.Session, SessionInstance: c.SessionInstance, RootID: c.RootID, Generation: c.Generation, NativeSessionID: c.NativeSessionID, RequestID: c.RequestID, RequestHash: c.RequestHash, ExpiresAt: c.ExpiresAt, Kind: "permission_reply", Reply: "once"}
	if question {
		body.Kind = "question_reply"
		body.Reply = ""
		body.Answers = [][]string{{"A", "custom answer"}}
	}
	return f, remotegw.New(socket, nil), body
}

func sealedNativePhonePacket(t *testing.T, key crypto.ContentKey, signer crypto.KeyStore, machine, id string, seq uint64, body protocol.NativeDecisionReq, alter func(*protocol.NativeDecisionReq, *protocol.DeviceCommandAuth)) []byte {
	t.Helper()
	cmd, err := phonecore.SignNativeDecision(signer, phonecore.NativeDecisionInput{Machine: machine, OperationID: id, ExpiresAt: time.Now().Add(time.Minute), Request: body})
	if err != nil {
		t.Fatal(err)
	}
	if alter != nil {
		alter(&body, &cmd)
	}
	packet, err := phonecore.SealNativeDecisionEnvelope(key, 1, seq, cmd, &body)
	if err != nil {
		t.Fatal(err)
	}
	return packet
}
func forwardNativePhonePacket(t *testing.T, gw *remotegw.Gateway, recv *crypto.MailboxReceiver, key crypto.ContentKey, packet []byte) protocol.Control {
	t.Helper()
	frame, err := remotegw.OpenMailboxFrame(recv, key, packet)
	if err != nil || frame.Kind != remotegw.FrameCommand || frame.Command.Action != protocol.ActionNativeDecide || frame.Command.BodyVersion != 1 {
		t.Fatalf("native phone envelope: %+v %v", frame, err)
	}
	reply, err := gw.ForwardCommand(protocol.OpNativeDecide, frame.Command)
	if err != nil {
		t.Fatal(err)
	}
	return reply
}

func TestOpenCodePhonePacketRefusesTamperingAndReadOnlyBeforeNativeIO(t *testing.T) {
	f, gw, body := nativePhonePacketFixture(t, false)
	full := registerPhone(t, f.d, device.CapFull)
	readonly := registerPhone(t, f.d, device.CapReadOnly)
	key := crypto.ContentKey{1, 2, 3}
	recv := crypto.NewMailboxReceiver()
	cases := []struct {
		name   string
		signer crypto.KeyStore
		alter  func(*protocol.NativeDecisionReq, *protocol.DeviceCommandAuth)
	}{
		{name: "signed body changed", signer: full, alter: func(r *protocol.NativeDecisionReq, _ *protocol.DeviceCommandAuth) { r.Reply = "always" }},
		{name: "signature changed", signer: full, alter: func(_ *protocol.NativeDecisionReq, c *protocol.DeviceCommandAuth) {
			c.Sig = strings.Repeat("A", len(c.Sig))
		}},
		{name: "read only policy", signer: readonly},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			packet := sealedNativePhonePacket(t, key, tc.signer, f.d.api.endpointID, "phone-invalid-"+string(rune('a'+i)), uint64(i+1), body, tc.alter)
			reply := forwardNativePhonePacket(t, gw, recv, key, packet)
			if reply.Op != protocol.OpError || reply.ErrorCode != protocol.CodeNotAuthorized {
				t.Fatalf("untrusted native decision escaped auth: %+v", reply)
			}
			if f.calls.Load() != 0 {
				t.Fatal("refused phone command reached native SDK")
			}
		})
	}
	packet := sealedNativePhonePacket(t, key, full, f.d.api.endpointID, "phone-corrupt-ciphertext", 4, body, nil)
	packet[len(packet)-1] ^= 1
	if _, err := remotegw.OpenMailboxFrame(recv, key, packet); err == nil {
		t.Fatal("modified ciphertext authenticated")
	}
	if f.calls.Load() != 0 {
		t.Fatal("ciphertext forgery reached native SDK")
	}
}

func TestOpenCodePhonePacketPermissionAndGroupedQuestionAdmission(t *testing.T) {
	for _, question := range []bool{false, true} {
		t.Run(map[bool]string{false: "permission", true: "question"}[question], func(t *testing.T) {
			f, gw, body := nativePhonePacketFixture(t, question)
			signer := registerPhone(t, f.d, device.CapReadApprove)
			key := crypto.ContentKey{3, 4, 5}
			recv := crypto.NewMailboxReceiver()
			for seq := uint64(1); seq <= 2; seq++ {
				packet := sealedNativePhonePacket(t, key, signer, f.d.api.endpointID, "phone-native-admitted", seq, body, nil)
				reply := forwardNativePhonePacket(t, gw, recv, key, packet)
				r := reply.NativeDecisionReceipt
				if reply.Op != protocol.OpNativeDecide || r == nil || r.State != "admitted" || r.OperationID != "phone-native-admitted" || r.Session != body.Session || r.SessionInstance != body.SessionInstance || r.RootID != body.RootID || r.Generation != body.Generation || r.NativeSessionID != body.NativeSessionID || r.RequestID != body.RequestID || r.Kind != body.Kind {
					t.Fatalf("scoped phone native receipt: %+v", reply)
				}
			}
			if f.calls.Load() != 1 {
				t.Fatalf("same-operation delivery produced%d native effects", f.calls.Load())
			}
			if question {
				sent := f.lastDecisionDispatch.Load()
				raw, _ := json.Marshal(sent)
				if !strings.Contains(string(raw), `"answers":[["A","custom answer"]]`) {
					t.Fatalf("question groups changed: %s", raw)
				}
			}
		})
	}
}

func TestOpenCodePhonePacketLostNativeReplyNeverReposts(t *testing.T) {
	f, gw, body := nativePhonePacketFixture(t, true)
	signer := registerPhone(t, f.d, device.CapReadApprove)
	key := crypto.ContentKey{7, 8, 9}
	recv := crypto.NewMailboxReceiver()
	f.dropReply.Store(true)
	for seq := uint64(1); seq <= 2; seq++ {
		reply := forwardNativePhonePacket(t, gw, recv, key, sealedNativePhonePacket(t, key, signer, f.d.api.endpointID, "phone-native-lost", seq, body, nil))
		if reply.NativeDecisionReceipt == nil || reply.NativeDecisionReceipt.State != "uncertain" {
			t.Fatalf("lost native response became definite: %+v", reply)
		}
	}
	reply := forwardNativePhonePacket(t, gw, recv, key, sealedNativePhonePacket(t, key, signer, f.d.api.endpointID, "phone-native-replacement", 3, body, nil))
	if r := reply.NativeDecisionReceipt; r == nil || r.State != "rejected" || r.NativeEffect != "none" {
		t.Fatalf("new operation bypassed unknown custody: %+v", reply)
	}
	f.receiptAvailable.Store(true)
	reply = forwardNativePhonePacket(t, gw, recv, key, sealedNativePhonePacket(t, key, signer, f.d.api.endpointID, "phone-native-lost", 4, body, nil))
	if reply.NativeDecisionReceipt == nil || reply.NativeDecisionReceipt.State != "admitted" {
		t.Fatalf("same operation did not recover exact late receipt: %+v", reply)
	}
	if f.calls.Load() != 1 {
		t.Fatalf("response loss/recovery caused%d native writes", f.calls.Load())
	}
}

// The in-memory transport carries the production RelaySink's sealed journal envelopes
// into the durable phone receive path. It does not stand in for any authorization decision.
type nativePhoneJournalMailbox struct {
	core   *phonecore.Core
	cursor atomic.Uint64
}

func (m *nativePhoneJournalMailbox) MailboxAppend(_ context.Context, _ string, raw []byte) (uint64, error) {
	cursor := m.cursor.Add(1)
	_, err := m.core.Router().AcceptCommit(raw, cursor)
	return cursor, err
}

func TestOpenCodePhonePacketComposerEchoSettlesDurableBubble(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		lost       bool
	}{
		{"plain", "genuine phone native input", false},
		{"clipped_full_hash", strings.Repeat("<", 4096), false},
		{"lost_reply", "genuine phone native input", true},
		{"clipped_lost_reply", strings.Repeat("<", 4096), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := tc.text
			d, socket := assembleWithRemote(t)
			f := newOpenCodeSendFixture(t, func(f *openCodeSendFixture) { f.d = d })
			caps, ok := d.sessionCapabilities(f.binding.WorkerID)
			if !ok || !caps.NativeComposer || caps.StructuredChat {
				t.Fatalf("native composer lacks qualified production proof: %+v", caps)
			}
			phone, err := phonecore.Resume(phonecore.Config{Dir: t.TempDir(), Machine: d.api.endpointID, WakeSealer: s18NewSealer(t), ContentSealer: s18NewSealer(t)})
			if err != nil {
				t.Fatal(err)
			}
			signer := phone.KeyStore()
			if err := d.api.devices.Add(device.Record{DeviceID: device.DeviceIDFor(signer.CommandSigningPublic()), Name: "native packet phone", NoiseStaticPub: make([]byte, 32), RelayAuthPub: make([]byte, 32), CommandSignPub: signer.CommandSigningPublic(), RecipientPub: signer.RecipientPublic(), Capability: device.CapFull, PairedAt: time.Now(), GrantedEpoch: 1}); err != nil {
				t.Fatal(err)
			}
			keys, err := crypto.NewEpochKeys()
			if err != nil {
				t.Fatal(err)
			}
			authority := bytes.Repeat([]byte{0x44}, 32)
			if err := phone.Mutate(func(st *phonecore.State) {
				st.EpochID = 1
				st.Keys = keys
				st.MachineRelayAuthPub = authority
				st.OperatorNamespace = "owner"
				st.RoutingID = "native-phone-packet"
			}); err != nil {
				t.Fatal(err)
			}
			body := f.request
			body.Text = text
			const operation = "phone:packet-composer"
			cmd, err := phonecore.SignComposerSend(signer, phonecore.ComposerSendInput{Machine: d.api.endpointID, OperationID: operation, ExpiresAt: time.Now().Add(time.Minute), Session: body.Session, SessionInstance: body.SessionInstance, Text: body.Text})
			if err != nil {
				t.Fatal(err)
			}
			publication := phonecore.PendingPublication{LogicalID: operation, OperationID: operation, Kind: phonecore.PublicationComposer, SessionID: body.Session, SessionInstance: body.SessionInstance, Text: body.Text, Machine: d.api.endpointID, EpochID: 1, Target: "machine-mailbox", AuthorityPub: authority, Command: cmd, Composer: &body, CreatedAt: time.Now()}
			if err := phone.PreparePublication(publication); err != nil {
				t.Fatal(err)
			}
			seq, err := phone.Seq().NextCommand()
			if err != nil {
				t.Fatal(err)
			}
			packet, err := phonecore.SealComposerSendEnvelope(keys.ContentKey, 1, seq, cmd, &body)
			if err != nil {
				t.Fatal(err)
			}
			if err := phone.SealPublication(operation, seq, packet); err != nil {
				t.Fatal(err)
			}
			mailbox := &nativePhoneJournalMailbox{core: phone}
			sink := remotegw.NewRelaySink(remotegw.RelayConfig{Appender: mailbox, Target: "phone-mailbox", Machine: d.api.endpointID, EpochID: 1, Key: keys.ContentKey, SenderKeyID: [8]byte{9}})
			gw := remotegw.New(socket, sink)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- gw.RunJournal(ctx) }()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("native journal gateway did not stop")
				}
			})
			frame, err := remotegw.OpenMailboxFrame(crypto.NewMailboxReceiver(), keys.ContentKey, packet)
			if err != nil || frame.Command.Action != protocol.ActionComposerSend {
				t.Fatalf("phone composer envelope: %+v %v", frame, err)
			}
			f.dropReply.Store(tc.lost)
			for attempt := 0; attempt < 2; attempt++ {
				reply, err := gw.ForwardCommand(protocol.OpComposerSend, frame.Command)
				if err != nil || (tc.lost && (reply.Op != protocol.OpError || reply.ErrorCode != protocol.CodeOutcomeUnknown)) || (!tc.lost && reply.Op == protocol.OpError) {
					t.Fatalf("signed phone composer lost=%v: %+v %v", tc.lost, reply, err)
				}
			}
			if f.calls.Load() != 1 || len(phone.PendingPublications()) != 1 {
				t.Fatal("delivery acknowledgement fabricated native echo settlement")
			}
			record, exists := d.core.ComposerOperation(operation)
			var intent openCodeSendIntent
			if !exists || json.Unmarshal(record.Request, &intent) != nil || intent.Source != adapter.SourcePhone {
				t.Fatalf("phone source was lost: %+v", intent)
			}
			f.rowAvailable.Store(true)
			row := f.nativeRow.Load().(json.RawMessage)
			var native struct {
				Info json.RawMessage `json:"info"`
			}
			if err := json.Unmarshal(row, &native); err != nil {
				t.Fatal(err)
			}
			props, _ := json.Marshal(map[string]json.RawMessage{"info": native.Info})
			f.nativeEvents <- opencodebridge.Event{Type: "message.updated", Properties: props}
			until := time.Now().Add(5 * time.Second)
			for len(phone.PendingPublications()) != 0 && time.Now().Before(until) {
				time.Sleep(10 * time.Millisecond)
			}
			if len(phone.PendingPublications()) != 0 {
				t.Fatalf("native user journal did not settle phone bubble; sink error=%v", sink.Err())
			}
			wanted := sha256.Sum256([]byte(text))
			found := false
			for _, item := range phone.Router().Items().Session(body.Session) {
				var wire struct {
					Role, Source, Provenance string
					OperationID              string `json:"operation_id"`
					Hash                     string `json:"input_sha256"`
					Truncated                bool
				}
				if json.Unmarshal(item.Body, &wire) == nil && wire.OperationID == operation {
					if wire.Role != "user" || wire.Source != "phone" || wire.Provenance != "live" || wire.Hash != hex.EncodeToString(wanted[:]) || wire.Truncated != (len(text) == 4096) {
						t.Fatalf("native phone echo lost exact source/body authority: %+v", wire)
					}
					found = true
				}
			}
			if !found || f.calls.Load() != 1 {
				t.Fatal("native phone settlement had no exact native user row or wrote twice")
			}
		})
	}
}

func TestOpenCodePhonePacketSlowComposerAdmissionUsesNativeBudget(t *testing.T) {
	d, socket := assembleWithRemote(t)
	f := newOpenCodeSendFixture(t, func(f *openCodeSendFixture) { f.d = d })
	signer := registerPhone(t, d, device.CapFull)
	key := crypto.ContentKey{1, 4, 8}
	const operation = "phone:slow-native-composer"
	cmd, err := phonecore.SignComposerSend(signer, phonecore.ComposerSendInput{Machine: d.api.endpointID, OperationID: operation, ExpiresAt: time.Now().Add(time.Minute), Session: f.request.Session, SessionInstance: f.request.SessionInstance, Text: f.request.Text})
	if err != nil {
		t.Fatal(err)
	}
	packet, err := phonecore.SealComposerSendEnvelope(key, 1, 1, cmd, &f.request)
	if err != nil {
		t.Fatal(err)
	}
	frame, err := remotegw.OpenMailboxFrame(crypto.NewMailboxReceiver(), key, packet)
	if err != nil {
		t.Fatal(err)
	}
	f.blockDispatch.Store(true)
	released := false
	defer func() {
		if !released {
			close(f.dispatchRelease)
		}
	}()
	gw := remotegw.New(socket, nil)
	type result struct {
		reply protocol.Control
		err   error
	}
	done := make(chan result, 1)
	go func() {
		reply, err := gw.ForwardCommand(protocol.OpComposerSend, frame.Command)
		done <- result{reply, err}
	}()
	select {
	case <-f.dispatchEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("signed slow phone input never reached native dispatch")
	}
	select {
	case result := <-done:
		t.Fatalf("gateway ended before legitimate native admission: %+v %v", result.reply, result.err)
	case <-time.After(11 * time.Second):
	}
	close(f.dispatchRelease)
	released = true
	select {
	case result := <-done:
		if result.err != nil || result.reply.Op == protocol.OpError {
			t.Fatalf("slow native admission: %+v %v", result.reply, result.err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("native admission did not return inside its remaining budget")
	}
	reply, err := gw.ForwardCommand(protocol.OpComposerSend, frame.Command)
	if err != nil || reply.Op == protocol.OpError || f.calls.Load() != 1 {
		t.Fatalf("slow admission duplicate: %+v %v calls=%d", reply, err, f.calls.Load())
	}
}
