package swarmmobile

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func nativeDecisionCard(t *testing.T, kind string) string {
	t.Helper()
	expires := time.Now().UTC().Add(time.Minute)
	request := schema.NativeRequestView{Session: "m1/s1", SessionInstance: "instance", RootID: "ses_root", Generation: "generation", NativeSessionID: "ses_child", RequestID: "req_exact", RequestHash: strings.Repeat("a", 64), ExpiresAt: &expires, Kind: kind, DetailsJSON: `{"id":"req_exact","sessionID":"ses_child"}`}
	if kind == "question" {
		request.Questions = []schema.NativeQuestionView{{Header: "Choice", Question: "Which?", Custom: true, Options: []schema.NativeQuestionOption{{Label: "one"}}}}
	} else {
		request.Permission = "bash"
		request.Patterns = []string{"printf safe"}
		request.Decisions = []schema.NativeDecisionOption{{ID: "once", Label: "Allow once"}, {ID: "reject", Label: "Reject"}}
	}
	raw, err := json.Marshal(map[string]any{"v": 2, "item_id": "native-request", "ts": time.Now().UTC(), "kind": kind + "_request", "status": "in_progress", "authority_valid": true, "provenance": "live", "request": request})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestReplyNativeRequestRefusesMissingCardAndMalformedAnswers(t *testing.T) {
	a := approveApp(t)
	if _, err := a.ReplyNativeRequest("m1/s1", "missing", strings.Repeat("a", 64), "once", ""); err == nil || !strings.Contains(err.Error(), ErrClassNotFound) {
		t.Fatalf("missing native card: %v", err)
	}
	applyMobileNativeCard(t, a, 10, nativeDecisionCard(t, "question"))
	for _, payload := range []string{"", "null", "[]", "[[]]", "[42]", "[\"flat\"]", "[[\"one\"]] trailing"} {
		if _, err := a.ReplyNativeRequest("m1/s1", "native-request", strings.Repeat("a", 64), "question_reply", payload); err == nil || !strings.Contains(err.Error(), ErrClassInvalidRequest) {
			t.Fatalf("malformed question answers %q: %v", payload, err)
		}
	}
}

func TestReplyNativeRequestIsLiveOnlyAndNeverQueuesAnswers(t *testing.T) {
	for _, kind := range []string{"permission", "question"} {
		t.Run(kind, func(t *testing.T) {
			a := approveApp(t)
			applyMobileNativeCard(t, a, 10, nativeDecisionCard(t, kind))
			decision, answers := "once", ""
			if kind == "question" {
				decision, answers = "question_reply", `[["one"]]`
			}
			if _, err := a.ReplyNativeRequest("m1/s1", "native-request", strings.Repeat("a", 64), decision, answers); err == nil || !strings.Contains(err.Error(), ErrClassOffline) {
				t.Fatalf("offline native answer: %v", err)
			}
			n, err := a.PendingOpCount()
			if err != nil || n != 0 {
				t.Fatalf("offline native answer left queued work: %d, %v", n, err)
			}
		})
	}
}

func TestNativeDecisionOutcomeDistinguishesAdmissionAndUncertainty(t *testing.T) {
	pending := outcomeOf(schema.Control{Op: schema.ActionNativeDecide, OperationID: "op", SessionID: "m/s", NativeDecisionReceipt: &schema.NativeDecisionReceipt{OperationID: "op", Session: "m/s", State: "pending"}})
	if pending.Resolved || pending.Code != "" || pending.Message != "" {
		t.Fatalf("ordinary in-flight answer displayed uncertainty: %+v", pending)
	}
	for _, state := range []string{"admitted", "uncertain", "rejected", "unknown"} {
		ctrl := schema.Control{Op: schema.ActionNativeDecide, OperationID: "op", SessionID: "m/s", NativeDecisionReceipt: &schema.NativeDecisionReceipt{OperationID: "op", Session: "m/s", State: state}}
		got := outcomeOf(ctrl)
		want := "outcome_unknown"
		if state == "admitted" {
			want = "native_admitted"
		}
		if state == "rejected" {
			want = "native_rejected"
		}
		if got.Code != want {
			t.Errorf("native %s became %s", state, got.Code)
		}
		ctrl.NativeDecisionReceipt.OperationID = "another-op"
		if got := outcomeOf(ctrl); got.Code != "outcome_unknown" {
			t.Fatal("uncorrelated native reply claimed an answer")
		}
	}
}

func TestNativeAnswerSuccessfulAppendLostReplySurvivesNewApp(t *testing.T) {
	dir := t.TempDir()
	a, sc := publicationApp(t, dir)
	applyMobileNativeCard(t, a, 10, nativeDecisionCard(t, "question"))
	if err := a.core.Mutate(func(st *phonecore.State) { st.Items = a.core.Router().Items().All() }); err != nil {
		t.Fatal(err)
	}
	body, ok := a.core.Router().Items().PendingNativeRequest("m1/s1", "native-request")
	if !ok {
		t.Fatal("fixture has no current authority")
	}
	body.Request.Answers = [][]string{{"one"}}
	appender := &scriptedAppender{onAppend: func() {
		if a.core.NativeDecisionOperations("m1/s1")["native-request"] != "native-first" {
			t.Error("mailbox append preceded durable custody")
		}
	}}
	sc.cl = appender
	op, err := a.appendNativeDecision(a.core, sc, "native-first", "native-request", body.Request, []byte("sealed first answer"))
	if err != nil || op == nil {
		t.Fatalf("append: %+v %v", op, err)
	}
	if out, err := a.Outcome(op.OperationID); err != nil || out.Resolved {
		t.Fatalf("live normal attempt: %+v %v", out, err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a2, sc2 := publicationApp(t, dir)
	sc2.cl = appender
	if out, err := a2.Outcome(op.OperationID); err != nil || out.Code != "outcome_unknown" {
		t.Fatalf("fresh app forgot lost reply: %+v %v", out, err)
	}
	op2, err := a2.appendNativeDecision(a2.core, sc2, "native-second", "native-request", body.Request, []byte("would duplicate answer"))
	if err != nil || op2.OperationID != op.OperationID || len(appender.envelopes()) != 1 {
		t.Fatalf("restart republished answer: %+v %v", op2, err)
	}
	page, err := a2.ReadTranscript("m1/s1", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	item, err := page.At(0)
	if err != nil || item.OperationID != op.OperationID {
		t.Fatalf("fresh screen lost card lock: %+v %v", item, err)
	}
}

func TestNativeAnswerRetainsOperationAfterMailboxResponseLoss(t *testing.T) {
	for _, replyFirst := range []bool{false, true} {
		a, sc := publicationApp(t, t.TempDir())
		appender := &scriptedAppender{failures: 1}
		sc.cl = appender
		expiry := time.Now().Add(time.Minute)
		body := schema.NativeDecisionReq{Session: "m1/s1", SessionInstance: "instance", RootID: "ses_root", NativeSessionID: "ses_child", Generation: "generation", RequestID: "question", RequestHash: strings.Repeat("a", 64), ExpiresAt: &expiry, Kind: "question_reply"}
		if replyFirst {
			appender.onAppend = func() {
				if err := a.core.RecordOutcome(schema.Control{Op: schema.ActionNativeDecide, OperationID: "native-lost", SessionID: body.Session, NativeDecisionReceipt: &schema.NativeDecisionReceipt{OperationID: "native-lost", Session: body.Session, SessionInstance: body.SessionInstance, RootID: body.RootID, Generation: body.Generation, NativeSessionID: body.NativeSessionID, RequestID: body.RequestID, Kind: body.Kind, State: "admitted"}}); err != nil {
					t.Error(err)
				}
			}
		}
		op, err := a.appendNativeDecision(a.core, sc, "native-lost", "native-request", body, []byte("exact sealed envelope"))
		if err != nil || op == nil || op.OperationID != "native-lost" {
			t.Fatalf("ambiguous append discarded its operation: %+v %v", op, err)
		}
		out, err := a.Outcome(op.OperationID)
		want := "outcome_unknown"
		if replyFirst {
			want = "native_admitted"
		}
		if err != nil || out.Code != want || len(appender.envelopes()) != 1 {
			t.Fatalf("ambiguous native answer retried or overwrote proof: %+v %v", out, err)
		}
		if pubs := a.core.State().PendingPublications; len(pubs) != 0 {
			t.Fatal("uncertain native answer was queued for later submission")
		}
	}
}

func TestReplyNativeRequestRejectsChangedDisplayedContentBeforeSigning(t *testing.T) {
	a := approveApp(t)
	raw := nativeDecisionCard(t, "permission")
	applyMobileNativeCard(t, a, 10, raw)
	applyMobileNativeCard(t, a, 20, strings.Replace(raw, strings.Repeat("a", 64), strings.Repeat("b", 64), 1))
	for _, shown := range []string{strings.Repeat("a", 64), ""} {
		if _, err := a.ReplyNativeRequest("m1/s1", "native-request", shown, "once", ""); err == nil || !strings.Contains(err.Error(), ErrClassInvalidRequest) {
			t.Fatalf("displayed authority silently rebound: %v", err)
		}
	}
	if _, err := a.ReplyNativeRequest("m1/s1", "native-request", strings.Repeat("b", 64), "once", ""); err == nil || !strings.Contains(err.Error(), ErrClassOffline) {
		t.Fatalf("exact current displayed authority did not reach live-only gate: %v", err)
	}
}

func TestNativeAnswerMatchesNativeQuestionBounds(t *testing.T) {
	answers := make([]string, 65)
	for i := range answers {
		answers[i] = strings.Repeat("x", i+1)
	}
	raw, _ := json.Marshal([][]string{answers})
	if _, err := nativeAnswer(schema.NativeDecisionReq{Kind: "question_reply"}, "question_reply", string(raw)); err != nil {
		t.Fatalf("64 options plus custom answer refused: %v", err)
	}
	groups := make([][]string, 17)
	for i := range groups {
		groups[i] = []string{"one"}
	}
	raw, _ = json.Marshal(groups)
	if _, err := nativeAnswer(schema.NativeDecisionReq{Kind: "question_reply"}, "question_reply", string(raw)); err == nil {
		t.Fatal("more than16 question groups accepted before publication")
	}
	raw, _ = json.Marshal([][]string{{strings.Repeat("x", 4096), strings.Repeat("y", 4096), strings.Repeat("z", 4096), strings.Repeat("w", 4096)}})
	if _, err := nativeAnswer(schema.NativeDecisionReq{Kind: "question_reply"}, "question_reply", string(raw)); err == nil {
		t.Fatal("encoded answers above native16KiB boundary accepted")
	}
}

func applyMobileNativeCard(t *testing.T, a *App, cursor uint64, raw string) {
	t.Helper()
	a.core.Router().Items().Apply(itemRecord(cursor, "m1/s1", raw))
	var item struct {
		Request schema.NativeRequestView `json:"request"`
	}
	if json.Unmarshal([]byte(raw), &item) != nil {
		t.Fatal("bad card")
	}
	req := item.Request
	set, _ := json.Marshal(map[string]any{"v": 2, "kind": "request_set", "item_id": "set", "ts": time.Now().UTC(), "provenance": "reconciled", "complete": true, "session_instance": req.SessionInstance, "root_id": req.RootID, "generation": req.Generation, "requests": map[string]string{"native-request": req.RequestHash}})
	a.core.Router().Items().Apply(itemRecord(cursor+1, "m1/s1", string(set)))
}
