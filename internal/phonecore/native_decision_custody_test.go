package phonecore

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func nativeCustodyRequest() schema.NativeDecisionReq {
	expires := time.Now().UTC().Add(time.Minute)
	return schema.NativeDecisionReq{Session: "m1/s1", SessionInstance: "instance", RootID: "ses_root", Generation: "generation", NativeSessionID: "ses_child", RequestID: "req_one", RequestHash: fmt.Sprintf("%064x", 1), ExpiresAt: &expires, Kind: "permission_reply", Reply: "once"}
}
func TestNativeDecisionCustodyPrecedesPublicationAndSurvivesRestart(t *testing.T) {
	c := publicationCore(t)
	body := nativeCustodyRequest()
	id, fresh, err := c.BeginNativeDecision("first", "item", body, time.Now())
	if err != nil || !fresh || id != "first" {
		t.Fatalf("initial custody: %s %v %v", id, fresh, err)
	}
	out, ok := c.NativeDecisionOutcome(id, time.Now())
	if !ok || out.NativeDecisionReceipt.State != "pending" {
		t.Fatal("live pending attempt became immediate uncertainty")
	}
	saved := c.store.(*fileStore)
	reopened, openErr := OpenStore(saved.path, "m1", saved.wake, saved.content)
	if openErr != nil {
		t.Fatal(openErr)
	}
	c2, err := Resume(Config{Machine: "m1", State: reopened})
	if err != nil {
		t.Fatal(err)
	}
	out, ok = c2.NativeDecisionOutcome(id, time.Now())
	if !ok || out.NativeDecisionReceipt.State != "uncertain" {
		t.Fatal("restart lost ambiguous native attempt")
	}
	got, created, err := c2.BeginNativeDecision("replacement", "item", body, time.Now().Add(time.Hour))
	if err != nil || created || got != id {
		t.Fatalf("expiry/restart authorized new operation: %s %v %v", got, created, err)
	}
	if len(c2.State().PendingPublications) != 0 || len(c2.State().PendingOps) != 0 {
		t.Fatal("native answer became replayable queued work")
	}
	if operations := c2.NativeDecisionOperations(body.Session); operations["item"] != id {
		t.Fatal("fresh screen cannot restore exact card lock")
	}
}
func TestNativeDecisionCustodySerializesSameRequestAndPreservesAdmission(t *testing.T) {
	c := publicationCore(t)
	body := nativeCustodyRequest()
	var wg sync.WaitGroup
	var mu sync.Mutex
	freshCount := 0
	ids := map[string]bool{}
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id, fresh, err := c.BeginNativeDecision(fmt.Sprint("op", n), "item", body, time.Now())
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			ids[id] = true
			if fresh {
				freshCount++
			}
			mu.Unlock()
		}(n)
	}
	wg.Wait()
	if freshCount != 1 || len(ids) != 1 {
		t.Fatal("concurrent native answers acquired multiple operations")
	}
	var id string
	for value := range ids {
		id = value
	}
	ctrl, _ := c.NativeDecisionOutcome(id, time.Now())
	ctrl.NativeDecisionReceipt.State = "admitted"
	if err := c.RecordOutcome(ctrl); err != nil {
		t.Fatal(err)
	}
	uncertain := ctrl
	copy := *ctrl.NativeDecisionReceipt
	copy.State = "uncertain"
	uncertain.NativeDecisionReceipt = &copy
	if err := c.RecordOutcomeIfAbsent(uncertain); err != nil {
		t.Fatal(err)
	}
	got, _ := c.NativeDecisionOutcome(id, time.Now())
	if got.NativeDecisionReceipt.State != "admitted" {
		t.Fatal("local response loss replaced known admission")
	}
}
func TestNativeDecisionCustodyOnlyReleasesQualifiedResolutionOrRejection(t *testing.T) {
	c := publicationCore(t)
	body := nativeCustodyRequest()
	_, _, err := c.BeginNativeDecision("first", "item", body, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	st := c.State()
	set := map[string]any{"v": 2, "kind": "request_set", "item_id": "set", "ts": time.Now().UTC(), "provenance": "reconciled", "complete": false, "session_instance": body.SessionInstance, "root_id": body.RootID, "generation": body.Generation, "requests": map[string]string{}}
	raw, _ := json.Marshal(set)
	st.Items = []Item{{SessionID: body.Session, ItemID: "set", Kind: "request_set", LastCursor: 1, Body: raw}}
	if err := c.Save(st); err != nil {
		t.Fatal(err)
	}
	if len(c.NativeDecisionOperations(body.Session)) != 1 {
		t.Fatal("gap/withdrawn authority released uncertainty")
	}
	set["complete"] = true
	set["generation"] = "foreign"
	raw, _ = json.Marshal(set)
	st = c.State()
	st.Items[0].Body = raw
	st.Items[0].LastCursor = 2
	if err := c.Save(st); err != nil {
		t.Fatal(err)
	}
	if len(c.NativeDecisionOperations(body.Session)) != 1 {
		t.Fatal("foreign scope released uncertainty")
	}
	set["generation"] = body.Generation
	raw, _ = json.Marshal(set)
	st = c.State()
	st.Items[0].Body = raw
	st.Items[0].LastCursor = 3
	if err := c.Save(st); err != nil {
		t.Fatal(err)
	}
	if len(c.NativeDecisionOperations(body.Session)) != 0 {
		t.Fatal("qualified native absence did not release card")
	}
	set["requests"] = map[string]string{"item": body.RequestHash}
	raw, _ = json.Marshal(set)
	st = c.State()
	st.Items[0].Body = raw
	st.Items[0].LastCursor = 4
	if err := c.Save(st); err != nil {
		t.Fatal(err)
	}
	_, fresh, err := c.BeginNativeDecision("next", "item", body, time.Now())
	if err != nil || !fresh {
		t.Fatal("released request remained locked")
	}
	ctrl, _ := c.NativeDecisionOutcome("next", time.Now())
	ctrl.NativeDecisionReceipt.State = "rejected"
	ctrl.NativeDecisionReceipt.NativeEffect = "none"
	if err := c.RecordOutcome(ctrl); err != nil {
		t.Fatal(err)
	}
	if len(c.NativeDecisionOperations(body.Session)) != 0 {
		t.Fatal("definite rejection did not release custody")
	}
}

func TestNativeDecisionAdmissionSurvivesLateErrorAfterResolution(t *testing.T) {
	c := publicationCore(t)
	body := nativeCustodyRequest()
	if _, _, err := c.BeginNativeDecision("op", "item", body, time.Now()); err != nil {
		t.Fatal(err)
	}
	ctrl, _ := c.NativeDecisionOutcome("op", time.Now())
	ctrl.NativeDecisionReceipt.State = "admitted"
	if err := c.RecordOutcome(ctrl); err != nil {
		t.Fatal(err)
	}
	st := c.State()
	raw, _ := json.Marshal(map[string]any{"v": 2, "kind": "request_set", "item_id": "set", "ts": time.Now().UTC(), "provenance": "reconciled", "complete": true, "session_instance": body.SessionInstance, "root_id": body.RootID, "generation": body.Generation, "requests": map[string]string{}})
	st.Items = []Item{{SessionID: body.Session, ItemID: "set", Kind: "request_set", LastCursor: 1, Body: raw}}
	if err := c.Save(st); err != nil {
		t.Fatal(err)
	}
	if len(c.State().NativeDecisions) != 0 || len(c.nativeDecisionLive) != 0 {
		t.Fatal("qualified resolution did not retire bounded custody")
	}
	if err := c.RecordOutcome(schema.Control{Op: "error", OperationID: "op", SessionID: body.Session, Error: "late transport error"}); err != nil {
		t.Fatal(err)
	}
	got := c.State().OpOutcomes["op"].NativeDecisionReceipt
	if got == nil || got.State != "admitted" {
		t.Fatal("late generic reply downgraded admitted operation after request resolution")
	}
}

func TestNativeDecisionIgnoresWrongScopeAndRequiresNoEffectForRejection(t *testing.T) {
	c := publicationCore(t)
	body := nativeCustodyRequest()
	if _, _, err := c.BeginNativeDecision("op", "item", body, time.Now()); err != nil {
		t.Fatal(err)
	}
	ctrl, _ := c.NativeDecisionOutcome("op", time.Now())
	ctrl.NativeDecisionReceipt.State = "rejected"
	ctrl.NativeDecisionReceipt.NativeEffect = "none"
	ctrl.NativeDecisionReceipt.Generation = "foreign"
	if err := c.RecordOutcome(ctrl); err != nil {
		t.Fatal(err)
	}
	ctrl.NativeDecisionReceipt.Generation = body.Generation
	ctrl.NativeDecisionReceipt.NativeEffect = "unknown"
	if err := c.RecordOutcome(ctrl); err != nil {
		t.Fatal(err)
	}
	if len(c.NativeDecisionOperations(body.Session)) != 1 {
		t.Fatal("unqualified rejection released custody")
	}
	if err := c.RecordOutcome(schema.Control{Op: "error", OperationID: "op", SessionID: body.Session}); err != nil {
		t.Fatal(err)
	}
	got, _ := c.NativeDecisionOutcome("op", time.Now())
	if got.NativeDecisionReceipt.State != "uncertain" {
		t.Fatal("generic failure claimed known no-effect rejection")
	}
}
func TestNativeDecisionCustodyIsBoundedWithoutEvictingUncertainWork(t *testing.T) {
	c := publicationCore(t)
	for n := 0; n < 256; n++ {
		body := nativeCustodyRequest()
		body.RequestID = fmt.Sprint("req", n)
		if _, fresh, err := c.BeginNativeDecision(fmt.Sprint("op", n), fmt.Sprint("item", n), body, time.Now()); err != nil || !fresh {
			t.Fatalf("record%d: %v", n, err)
		}
	}
	body := nativeCustodyRequest()
	body.RequestID = "overflow"
	if _, _, err := c.BeginNativeDecision("overflow", "overflow", body, time.Now()); err == nil {
		t.Fatal("native custody overflow accepted")
	}
	if len(c.NativeDecisionOperations(body.Session)) != 256 {
		t.Fatal("capacity eviction lost uncertain native attempts")
	}
}
