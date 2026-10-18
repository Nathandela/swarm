package swarmmobile

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func nativeDecisionCard(t *testing.T, kind string) string {
	t.Helper()
	expires := time.Now().UTC().Add(time.Minute)
	request := schema.NativeRequestView{Session: "m1/s1", SessionInstance: "instance", RootID: "ses_root", Generation: "generation", NativeSessionID: "ses_child", RequestID: "req_exact", RequestHash: strings.Repeat("a", 64), ExpiresAt: &expires, Kind: kind}
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
	if _, err := a.ReplyNativeRequest("m1/s1", "missing", "once", ""); err == nil || !strings.Contains(err.Error(), ErrClassNotFound) {
		t.Fatalf("missing native card: %v", err)
	}
	a.core.Router().Items().Apply(itemRecord(10, "m1/s1", nativeDecisionCard(t, "question")))
	for _, payload := range []string{"", "null", "[]", "[[]]", "[42]", "[\"flat\"]", "[[\"one\"]] trailing"} {
		if _, err := a.ReplyNativeRequest("m1/s1", "native-request", "question_reply", payload); err == nil || !strings.Contains(err.Error(), ErrClassInvalidRequest) {
			t.Fatalf("malformed question answers %q: %v", payload, err)
		}
	}
}

func TestReplyNativeRequestIsLiveOnlyAndNeverQueuesAnswers(t *testing.T) {
	for _, kind := range []string{"permission", "question"} {
		t.Run(kind, func(t *testing.T) {
			a := approveApp(t)
			a.core.Router().Items().Apply(itemRecord(10, "m1/s1", nativeDecisionCard(t, kind)))
			decision, answers := "once", ""
			if kind == "question" {
				decision, answers = "question_reply", `[["one"]]`
			}
			if _, err := a.ReplyNativeRequest("m1/s1", "native-request", decision, answers); err == nil || !strings.Contains(err.Error(), ErrClassOffline) {
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

func TestNativeAnswerRetainsOperationAfterMailboxResponseLoss(t *testing.T) {
	for _, replyFirst := range []bool{false, true} {
		a, sc := publicationApp(t, t.TempDir())
		appender := &scriptedAppender{failures: 1}
		sc.cl = appender
		body := schema.NativeDecisionReq{Session: "m1/s1", SessionInstance: "instance", RootID: "ses_root", NativeSessionID: "ses_child", Generation: "generation", RequestID: "question", Kind: "question_reply"}
		if replyFirst {
			appender.onAppend = func() {
				if err := a.core.RecordOutcome(schema.Control{Op: schema.ActionNativeDecide, OperationID: "native-lost", SessionID: body.Session, NativeDecisionReceipt: &schema.NativeDecisionReceipt{OperationID: "native-lost", Session: body.Session, State: "admitted"}}); err != nil {
					t.Error(err)
				}
			}
		}
		op, err := a.appendNativeDecision(a.core, sc, "native-lost", body, []byte("exact sealed envelope"))
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
