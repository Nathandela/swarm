package schema

import (
	"bytes"
	"testing"
	"time"
)

func TestNativeDecisionHashBindsExactChoiceAndRequest(t *testing.T) {
	expires := time.Unix(1700000000, 0).UTC()
	base := NativeDecisionReq{Session: "machine/worker", SessionInstance: "instance", RootID: "root", Generation: "generation", NativeSessionID: "child", RequestID: "request", RequestHash: "hash", ExpiresAt: &expires, Kind: "question_reply", Answers: [][]string{{"A", "custom"}, {}}}
	want := NativeDecisionContentHash(&base)
	if len(want) != 32 || !bytes.Equal(want, NativeDecisionContentHash(&base)) {
		t.Fatal("request hash is not deterministic SHA256")
	}
	for name, mutate := range map[string]func(*NativeDecisionReq){
		"session":           func(r *NativeDecisionReq) { r.Session += "x" },
		"instance":          func(r *NativeDecisionReq) { r.SessionInstance += "x" },
		"root":              func(r *NativeDecisionReq) { r.RootID += "x" },
		"generation":        func(r *NativeDecisionReq) { r.Generation += "x" },
		"target":            func(r *NativeDecisionReq) { r.NativeSessionID += "x" },
		"request":           func(r *NativeDecisionReq) { r.RequestID += "x" },
		"content":           func(r *NativeDecisionReq) { r.RequestHash += "x" },
		"expiry":            func(r *NativeDecisionReq) { r.ExpiresAt = nil },
		"kind":              func(r *NativeDecisionReq) { r.Kind = "question_reject" },
		"reply":             func(r *NativeDecisionReq) { r.Reply = "always" },
		"answers":           func(r *NativeDecisionReq) { r.Answers = [][]string{{"custom", "A"}, {}} },
		"question grouping": func(r *NativeDecisionReq) { r.Answers = [][]string{{"A"}, {"custom"}} },
	} {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			if bytes.Equal(want, NativeDecisionContentHash(&changed)) {
				t.Fatal("unsigned mutation")
			}
		})
	}
	if len(NativeDecisionContentHash(nil)) != 32 {
		t.Fatal("nil body did not hash safely")
	}
}
