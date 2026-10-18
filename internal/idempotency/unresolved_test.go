package idempotency

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeEvidenceDoesNotSettleUncommittedExecution(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Prepare("op", "semantic_send", "worker"); err != nil {
		t.Fatal(err)
	}
	if err := s.Begin("op"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEvidence("op", []byte(`{"status":"admitted"}`)); err != nil {
		t.Fatal(err)
	}
	if !s.UnobservedExecution("semantic_send", "worker", "later") {
		t.Fatal("unverified or not-yet-committed native evidence let a later input overtake delivery reconciliation")
	}
	if err := s.Complete("op", []byte(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	if s.UnobservedExecution("semantic_send", "worker", "later") {
		t.Fatal("completed delivery kept blocking later input")
	}
}

func TestUnknownStoredPhaseCannotAuthorizeLaterSemanticWrites(t *testing.T) {
	dir := t.TempDir()
	rec := Record{OperationID: "op", Action: "semantic_send", SessionID: "worker", Phase: Phase("unqualified"), CreatedAt: time.Now().Add(-48 * time.Hour)}
	raw, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(dir, logFile), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenWithOptions(dir, Options{TTL: time.Hour, MaxEntries: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !s.UnobservedExecution("semantic_send", "worker", "later") {
		t.Fatal("unqualified durable phase was treated as settled")
	}
	if _, _, err := s.Prepare("later", "semantic_send", "worker"); !errors.Is(err, ErrCapacity) {
		t.Fatalf("unqualified prior execution expired out of write custody: %v", err)
	}
}
