package idempotency

import (
	"testing"
	"time"
)

func TestUnresolvedNativeDecisionSurvivesTTLAndEvidenceCannotFail(t *testing.T) {
	now := time.Now()
	s, err := OpenWithOptions(t.TempDir(), Options{TTL: time.Minute, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.f.Close() }()
	if _, _, err = s.PrepareBound("decision", "native_decide", "worker", "instance", "hash"); err != nil {
		t.Fatal(err)
	}
	if err = s.SetRequest("decision", []byte(`{"request_id":"per_exact"}`)); err != nil {
		t.Fatal(err)
	}
	if err = s.Begin("decision"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if err = s.Compact(); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("decision"); !ok {
		t.Fatal("unresolved native decision lost its replay guard")
	}
	if ids := s.PendingRequestOperations("native_decide", "worker", "instance", 128); len(ids) != 1 || ids[0] != "decision" {
		t.Fatalf("pending operations: %v", ids)
	}
	if err = s.SetEvidence("decision", []byte(`{"status":"admitted"}`)); err != nil {
		t.Fatal(err)
	}
	if err = s.Fail("decision", []byte(`{"state":"rejected"}`)); err == nil {
		t.Fatal("admitted native decision became rejection")
	}
	if ids := s.PendingRequestOperations("native_decide", "worker", "instance", 128); len(ids) != 0 {
		t.Fatal("confirmed native effect remained unobserved")
	}
}
