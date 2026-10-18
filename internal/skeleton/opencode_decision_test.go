package skeleton

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/idempotency"
	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
)

func newNativeDecisionFixture(t *testing.T, question ...bool) (*openCodeSendFixture, protocol.NativeDecisionReq) {
	t.Helper()
	f := newOpenCodeSendFixture(t, func(f *openCodeSendFixture) {
		f.nativeDecisionMode = true
		f.nativeQuestionMode = len(question) > 0 && question[0]
	})
	_ = f.client.Close()
	var err error
	f.client, err = protocol.Dial(f.d.SocketPath(), []string{protocol.CapNativeRequests})
	if err != nil {
		t.Fatal(err)
	}
	list, err := f.client.NativeRequests(protocol.NativeRequestsReq{Session: f.request.Session})
	if err != nil || len(list.Requests) != 1 {
		t.Fatalf("native pending list: %+v %v", list, err)
	}
	full, err := f.client.NativeRequests(protocol.NativeRequestsReq{Session: f.request.Session, RequestID: list.Requests[0].RequestID, NativeSessionID: list.Requests[0].NativeSessionID})
	if err != nil || full.Request == nil {
		t.Fatalf("native pending card: %+v %v", full, err)
	}
	c := full.Request
	body := protocol.NativeDecisionReq{Session: c.Session, SessionInstance: c.SessionInstance, RootID: c.RootID, Generation: c.Generation, NativeSessionID: c.NativeSessionID, RequestID: c.RequestID, RequestHash: c.RequestHash, ExpiresAt: c.ExpiresAt, Kind: "permission_reply", Reply: "once"}
	if f.nativeQuestionMode {
		body.Kind = "question_reply"
		body.Reply = ""
		body.Answers = [][]string{{"A", "custom answer"}}
	}
	return f, body
}

func TestOpenCodeNativeDecisionDurableReplyAndExactScope(t *testing.T) {
	for _, reply := range []string{"once", "always", "reject"} {
		t.Run(reply, func(t *testing.T) {
			f, body := newNativeDecisionFixture(t)
			body.Reply = reply
			for i := 0; i < 2; i++ {
				receipt, err := f.client.NativeDecide("native-decision-"+reply, body)
				if err != nil || receipt.State != "admitted" {
					t.Fatalf("decision: %+v %v", receipt, err)
				}
				if reply == "reject" && receipt.NativeEffect != "reject_session_pending_permissions" {
					t.Fatalf("scope: %+v", receipt)
				}
			}
			rec, ok := f.d.core.ComposerOperation("native-decision-" + reply)
			if !ok || rec.Phase != idempotency.PhaseCompleted || len(rec.Request) == 0 || len(rec.Evidence) == 0 || f.calls.Load() != 1 {
				t.Fatalf("custody: %+v calls%d", rec, f.calls.Load())
			}
			body.Reply = "reject"
			if reply == "reject" {
				body.Reply = "once"
			}
			if _, err := f.client.NativeDecide("native-decision-"+reply, body); err == nil || f.calls.Load() != 1 {
				t.Fatal("changed answer replayed another receipt")
			}
		})
	}
}

func TestOpenCodeNativeDecisionLostReplyNeverRepostsAndCanReconcile(t *testing.T) {
	f, body := newNativeDecisionFixture(t)
	f.dropReply.Store(true)
	for i := 0; i < 2; i++ {
		receipt, err := f.client.NativeDecide("native-unknown", body)
		if err != nil || receipt.State != "uncertain" {
			t.Fatalf("lost reply: %+v %v", receipt, err)
		}
	}
	if _, err := f.client.NativeDecide("native-bypass", body); err == nil || f.calls.Load() != 1 {
		t.Fatal("new operation bypassed unresolved same-request custody")
	}
	f.pendingNative.Store([]json.RawMessage{})
	if receipt, err := f.client.NativeDecisionStatus(body.Session, "native-unknown"); err != nil || receipt.State != "uncertain" {
		t.Fatalf("disappearance became acknowledgement: %+v %v", receipt, err)
	}
	f.receiptAvailable.Store(true)
	if receipt, err := f.client.NativeDecisionStatus(body.Session, "native-unknown"); err != nil || receipt.State != "admitted" {
		t.Fatalf("late exact native receipt: %+v %v", receipt, err)
	}
	if _, err := f.d.NativeDecisionStatus("another-worker", "native-unknown"); err == nil {
		t.Fatal("receipt escaped its worker scope")
	}
	if f.calls.Load() != 1 {
		t.Fatal("receipt reconciliation wrote native request again")
	}
}

func TestOpenCodeNativeDecisionRefusesStaleAuthorityAndEndingOwner(t *testing.T) {
	f, body := newNativeDecisionFixture(t)
	for i, change := range []func(*protocol.NativeDecisionReq){func(r *protocol.NativeDecisionReq) { r.RequestHash = strings.Repeat("b", 64) }, func(r *protocol.NativeDecisionReq) { r.Generation = "stale" }, func(r *protocol.NativeDecisionReq) { r.NativeSessionID = "ses_other" }, func(r *protocol.NativeDecisionReq) { v := time.Now().Add(-time.Second); r.ExpiresAt = &v }} {
		changed := body
		change(&changed)
		if _, err := f.client.NativeDecide("native-stale-"+string(rune('a'+i)), changed); err == nil {
			t.Fatal("stale request authority accepted")
		}
	}
	if err := f.d.withOwnerSessionEnd(f.binding.WorkerID, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := f.client.NativeDecide("native-after-end", body); err == nil {
		t.Fatal("owner-ending request reached native")
	}
	if f.calls.Load() != 0 {
		t.Fatal("stale/ending request produced an effect")
	}
}

func TestOpenCodeNativeQuestionPreservesMultipleAndCustomAnswers(t *testing.T) {
	f, body := newNativeDecisionFixture(t, true)
	receipt, err := f.client.NativeDecide("native-question", body)
	if err != nil || receipt.State != "admitted" || receipt.NativeEffect != "single_request" {
		t.Fatalf("question: %+v %v", receipt, err)
	}
	sent := f.lastDecisionDispatch.Load().(opencodebridge.DispatchRequest)
	if string(mustNativeDecisionJSON(t, sent.Answers)) != `[["A","custom answer"]]` || len(sent.ExpectedRequest) == 0 {
		t.Fatal("question answer/proof changed")
	}
	falseValue := false
	pending := nativeRequestFixture(t, body.NativeSessionID, body.RequestID, "question")
	pending.Questions[0].Custom = &falseValue
	if nativeAnswersMatch(pending, body) == nil {
		t.Fatal("custom answer accepted where native custom=false")
	}
	body.Answers = [][]string{{"A", "A"}}
	if nativeAnswersMatch(pending, body) == nil {
		t.Fatal("duplicate answer accepted")
	}
	body.Kind = "question_reject"
	body.Answers = nil
	if nativeAnswersMatch(pending, body) != nil {
		t.Fatal("valid question rejection refused")
	}
}
func mustNativeDecisionJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestOpenCodeNativeDecisionRespectsOwnerControllerAndDirtyInput(t *testing.T) {
	f, body := newNativeDecisionFixture(t)
	owner, err := protocol.Dial(f.d.SocketPath(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close() }()
	attachment, err := owner.Attach(body.Session)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.client.NativeDecide("native-controlled", body); err == nil {
		t.Fatal("controlled native worker accepted decision")
	}
	if err := attachment.Input([]byte("owner draft")); err != nil {
		t.Fatal(err)
	}
	if err := attachment.Detach(); err != nil {
		t.Fatal(err)
	}
	receipt, err := f.client.NativeDecide("native-dirty", body)
	if err != nil || receipt.State != "rejected" || receipt.NativeEffect != "none" {
		t.Fatalf("dirty input: %+v %v", receipt, err)
	}
	if f.calls.Load() != 0 {
		t.Fatal("controller or dirty input reached native SDK")
	}
}

func TestNativeDecisionUsesApprovalPolicyAndSignedAnswers(t *testing.T) {
	for _, capability := range []device.Capability{device.CapReadOnly, device.CapReadApprove, device.CapFull} {
		reg, key, _, id := authFixture(t, capability)
		now := time.Now()
		expires := now.Add(time.Minute)
		body := protocol.NativeDecisionReq{Session: "machine1/worker", SessionInstance: "instance", RootID: "root", Generation: "gen", NativeSessionID: "child", RequestID: "question", RequestHash: strings.Repeat("a", 64), ExpiresAt: &expires, Kind: "question_reply", Answers: [][]string{{"A"}}}
		hash := schema.NativeDecisionContentHash(&body)
		canonical, err := (crypto.Command{Action: protocol.ActionNativeDecide, Machine: "machine1", Session: body.Session, OperationID: "native-policy", ExpiresAt: expires.Unix(), ContentHash: hash}).Canonical()
		if err != nil {
			t.Fatal(err)
		}
		auth := protocol.DeviceCommandAuth{DeviceID: id, Action: protocol.ActionNativeDecide, Machine: "machine1", Session: body.Session, OperationID: "native-policy", ExpiresAt: expires, ContentHash: hash, Sig: base64.StdEncoding.EncodeToString(mustSignCommand(t, key, canonical))}
		err = authorizeCommand(reg, now, auth)
		if (err == nil) != (capability != device.CapReadOnly) {
			t.Fatalf("capability %v: %v", capability, err)
		}
		body.Answers = [][]string{{"changed"}}
		auth.ContentHash = schema.NativeDecisionContentHash(&body)
		if authorizeCommand(reg, now, auth) == nil {
			t.Fatal("changed answers retained a valid device signature")
		}
	}
}
