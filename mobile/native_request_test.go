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
