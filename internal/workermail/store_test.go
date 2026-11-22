package workermail

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestDelayedOrderedNotesAndRetainedReply(t *testing.T) {
	dir := t.TempDir()
	b := Binding{"discussion", "parent", "ep/worker", "instance1", "attempt", "codex", "/project", ""}
	id := fmt.Sprintf("semantic:%d:00112233445566778899aabbccddeeff", time.Now().Add(-2*time.Hour).UnixMilli())
	q, err := Submit(dir, b, id, "needs-input", "early", true)
	if err != nil {
		t.Fatal(err)
	}
	end, err := Submit(dir, b, "late", "complete", "late", true)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r, e := Submit(dir, b, id, "needs-input", "early", false); e != nil || r.Sequence != q.Sequence {
				t.Errorf("retry %+v %v", r, e)
			}
		}()
	}
	wg.Wait()
	if _, err = Submit(dir, b, id, "complete", "early", false); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed payload %v", err)
	}
	if _, err = Submit(dir, b, "expired", "complete", "late", false); err == nil {
		t.Fatal("accepted expired initial submission")
	}
	discussions, err := Discussions(dir)
	if err != nil || !discussions[b.Discussion] {
		t.Fatalf("dormant discovery %+v %v", discussions, err)
	}
	notes, err := Pending(dir, b.Discussion)
	if err != nil || len(notes) != 2 || notes[0].Sequence != q.Sequence || notes[1].Sequence != end.Sequence {
		t.Fatalf("order %+v %v", notes, err)
	}
	if err = Acknowledge(dir, q, ""); err != nil {
		t.Fatal(err)
	}
	if err = Acknowledge(dir, q, "answer"); err != nil {
		t.Fatal(err)
	}
	if err = Acknowledge(dir, q, "answer"); err != nil {
		t.Fatal(err)
	}
	if err = Acknowledge(dir, q, "conflict"); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed answer: %v", err)
	}
	if err = Acknowledge(dir, end, ""); err != nil {
		t.Fatal(err)
	}
	n, err := Lookup(dir, b, id)
	if err != nil || n.Answer != "answer" {
		t.Fatalf("retained reply %+v %v", n, err)
	}
	other := b
	other.Instance = "instance2"
	if _, err := Lookup(dir, other, id); !errors.Is(err, ErrAbsent) {
		t.Fatal("wrong incarnation returned a note")
	}
}

func TestSettlementRetentionPreservesPendingAndUnreadQuestions(t *testing.T) {
	dir := t.TempDir()
	b := Binding{"discussion", "parent", "ep/worker", "i", "attempt", "codex", "/project", ""}
	final, err := Submit(dir, b, "complete", "complete", "finished", true)
	if err != nil {
		t.Fatal(err)
	}
	unread, err := Submit(dir, b, "question", "needs-input", "help", true)
	if err != nil {
		t.Fatal(err)
	}
	if err = Acknowledge(dir, final, ""); err != nil {
		t.Fatal(err)
	}
	if err = Acknowledge(dir, unread, "answer"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(8 * 24 * time.Hour)
	prune := func() {
		t.Helper()
		if err = transact(dir, true, func(tx *bolt.Tx) error { return pruneSettled(tx, now) }); err != nil {
			t.Fatal(err)
		}
	}
	prune()
	if _, err = Lookup(dir, b, final.ID); !errors.Is(err, ErrAbsent) {
		t.Fatal("settled note retained beyond horizon")
	}
	answer, err := Lookup(dir, b, unread.ID)
	if err != nil || answer.Answer != "answer" {
		t.Fatal("unread answer discarded")
	}
	if err = MarkAnswerRead(dir, answer); err != nil {
		t.Fatal(err)
	}
	prune()
	if _, err = Lookup(dir, b, unread.ID); !errors.Is(err, ErrAbsent) {
		t.Fatal("delivered answer never pruned")
	}
}

func TestFullPendingMailboxPreservesAcceptedReceiptAndRefusesNewNote(t *testing.T) {
	dir := t.TempDir()
	b := Binding{"discussion", "parent", "ep/worker", "i", "attempt", "codex", "/project", ""}
	first, err := Submit(dir, b, "first", "needs-input", "which file?", true)
	if err != nil {
		t.Fatal(err)
	}
	if err = transact(dir, true, func(tx *bolt.Tx) error {
		p := tx.Bucket([]byte("pending"))
		for i := 1; i < 4096; i++ {
			if err := p.Put([]byte(fmt.Sprintf("fixture-%04d", i)), []byte{1}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = Submit(dir, b, "next", "complete", "finished", true); err == nil {
		t.Fatal("accepted a new note into a full pending queue")
	}
	again, err := Submit(dir, b, "first", "needs-input", "which file?", false)
	if err != nil || again.Sequence != first.Sequence {
		t.Fatalf("full queue lost prior receipt: %+v, %v", again, err)
	}
	notes, err := Pending(dir, b.Discussion)
	if err != nil || len(notes) != 1 || notes[0].ID != first.ID {
		t.Fatalf("missing accepted note: %+v, %v", notes, err)
	}
}
