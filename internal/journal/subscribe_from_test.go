package journal

import (
	"fmt"
	"testing"
	"time"
)

func TestJournal_SubscribeFromReturnsAtomicBacklogThenLiveBoundary(t *testing.T) {
	j, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	first, err := j.Append(Record{SessionID: "s", Type: TypeLaunched})
	if err != nil {
		t.Fatal(err)
	}
	res, live, cancel, err := j.SubscribeFrom(0)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if res.Cursor != first.Cursor || len(res.Events) != 1 || res.Events[0].Cursor != first.Cursor {
		t.Fatalf("resume = %#v, want exactly cursor %d as backlog", res, first.Cursor)
	}
	second, err := j.Append(Record{SessionID: "s", Type: TypeSessionState})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-live:
		if got.Cursor != second.Cursor || got.Cursor <= res.Cursor {
			t.Fatalf("live cursor = %d, want %d strictly after boundary %d", got.Cursor, second.Cursor, res.Cursor)
		}
	case <-time.After(time.Second):
		t.Fatal("append after atomic boundary did not reach live feed")
	}
}

func TestJournal_OverflowDisconnectsAndReplayRecoversDroppedTail(t *testing.T) {
	j, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	_, live, cancel, err := j.SubscribeFrom(0)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	for i := 0; i < subscriberBuffer+1; i++ {
		if _, err := j.Append(Record{SessionID: "s", Type: TypePresence, Payload: []byte(fmt.Sprint(i))}); err != nil {
			t.Fatal(err)
		}
	}
	var buffered []Record
	for rec := range live {
		buffered = append(buffered, rec)
	}
	if len(buffered) != subscriberBuffer {
		t.Fatalf("closed overflow feed retained %d records, want bounded prefix %d", len(buffered), subscriberBuffer)
	}
	res, replay, cancelReplay, err := j.SubscribeFrom(buffered[len(buffered)-1].Cursor)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelReplay()
	if len(res.Events) != 1 || res.Events[0].Cursor != uint64(subscriberBuffer+1) {
		t.Fatalf("replay after overflow = %#v, want dropped durable tail cursor %d", res.Events, subscriberBuffer+1)
	}
	cancelReplay()
	select {
	case _, ok := <-replay:
		if ok {
			t.Fatal("cancel did not close replay feed")
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not terminate replay feed")
	}
	cancelReplay() // idempotent
}
