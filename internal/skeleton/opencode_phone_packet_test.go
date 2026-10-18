package skeleton

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

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
