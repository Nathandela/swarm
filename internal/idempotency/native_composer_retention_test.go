package idempotency

import (
	"errors"
	"testing"
	"time"
)

func TestNativeComposerCustodySurvivesExpiryWithoutReopeningTheWrite(t *testing.T) {
	now := time.Now().UTC()
	opts := Options{TTL: time.Hour, MaxEntries: 1, Clock: func() time.Time { return now }}
	dir := t.TempDir()
	s, err := OpenWithOptions(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.f.Close() })
	if _, _, err := s.PrepareBound("phone", "composer_send", "session", "instance", "hash"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRequest("phone", []byte(`{"message_id":"msg_native"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Begin("phone"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if err := s.Compact(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.PrepareBound("other", "semantic_send", "session", "instance", "new-hash"); !errors.Is(err, ErrCapacity) {
		t.Fatalf("unconfirmed native phone input aged out: %v", err)
	}
	if err := s.f.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenWithOptions(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	record, existed, err := s.PrepareBound("phone", "composer_send", "session", "instance", "hash")
	if err != nil || !existed || record.Phase != PhaseExecuting {
		t.Fatalf("native phone write became fresh after restart: record=%+v existed=%v err=%v", record, existed, err)
	}
	if err := s.SetEvidence("phone", []byte(`{"status":"admitted"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Fail("phone", []byte(`{"ok":false}`)); err == nil {
		t.Fatal("proven native phone admission became a definite rejected write")
	}
}
