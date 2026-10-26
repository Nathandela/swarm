package discord

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func sampleRequest() Request {
	return Request{EventID: "event", MessageRevision: "1", EventAt: testNow, Author: Actor{ID: "alice", Name: "Alice"}, Authority: AuthorityExternal, Route: Route{GuildID: "guild", ChannelID: "channel", MessageID: "message"}, Target: Target{ID: "session", Instance: "instance"}, Text: "hello", Context: []ContextMessage{{ID: "message", Author: Actor{ID: "alice"}, Text: "original"}}}
}
func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, e := OpenStore(filepath.Join(t.TempDir(), "discord"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func mustAdmit(t *testing.T, s *Store, r Request) Request {
	t.Helper()
	got, fresh, e := s.Admit(r, testNow)
	if e != nil || !fresh {
		t.Fatalf("Admit = %+v, %v, %v", got, fresh, e)
	}
	return got
}

func TestAdmissionDurableReplayConflictAndExactContext(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "discord")
	s, e := OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	r := sampleRequest()
	r.Text = strings.Repeat("界🙂", 5000)
	r.Context[0].Text = r.Text
	saved := mustAdmit(t, s, r)
	r.Context[0].Text = "caller mutation"
	if saved.OperationID == "" || saved.Ref == "" {
		t.Fatal("missing stable native identity")
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	again, fresh, e := s.Admit(sampleWithText(saved), testNow)
	if e != nil || fresh || !reflect.DeepEqual(again, saved) {
		t.Fatalf("replay %+v %v %v", again, fresh, e)
	}
	for _, change := range []func(*Request){func(r *Request) { r.Text = "edit" }, func(r *Request) { r.Author.ID = "bob" }, func(r *Request) { r.Target.ID = "other" }} {
		r = sampleWithText(saved)
		change(&r)
		if _, _, e = s.Admit(r, testNow); !errors.Is(e, ErrConflict) {
			t.Fatalf("changed replay: %v", e)
		}
	}
	got, e := s.Request(saved.Ref, testNow)
	if e != nil || got.Context[0].Text != saved.Text {
		t.Fatal("context changed", e)
	}
	for _, path := range []string{dir, filepath.Join(dir, "discord.db")} {
		fi, e := os.Stat(path)
		if e != nil {
			t.Fatal(e)
		}
		want := os.FileMode(0600)
		if path == dir {
			want = 0700
		}
		if fi.Mode().Perm() != want {
			t.Fatalf("permissions %s: %v", path, fi.Mode())
		}
	}
	if extra, e := OpenStore(dir); e == nil {
		extra.Close()
		t.Fatal("second owner admitted")
	}
}
func sampleWithText(r Request) Request { return r }

func TestConcurrentClaimAndRevocation(t *testing.T) {
	s := openTestStore(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	fresh := 0
	for range 16 {
		wg.Go(func() {
			_, new, e := s.Admit(sampleRequest(), testNow)
			if e != nil {
				t.Error(e)
			}
			if new {
				mu.Lock()
				fresh++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if fresh != 1 {
		t.Fatal("claims", fresh)
	}
	r, _, _ := s.Admit(sampleRequest(), testNow)
	if e := s.SetOptOut(r.Target, true); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Request(r.Ref, testNow); !errors.Is(e, ErrOptedOut) {
		t.Fatal(e)
	}
	if _, _, e := s.BeginDelivery(r.Ref, testNow); !errors.Is(e, ErrOptedOut) {
		t.Fatal(e)
	}
	if _, _, e := s.PreparePublication(r.Ref, "", "answer", testNow); !errors.Is(e, ErrOptedOut) {
		t.Fatal(e)
	}
	other := sampleRequest()
	other.EventID = "new"
	if _, _, e := s.Admit(other, testNow); !errors.Is(e, ErrOptedOut) {
		t.Fatal(e)
	}
	other.Target.Instance = "independent"
	mustAdmit(t, s, other)
}

func TestExpiryAndUnresolvedRecovery(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "discord")
	s, e := OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	r := mustAdmit(t, s, sampleRequest())
	if _, fresh, e := s.BeginDelivery(r.Ref, testNow); e != nil || !fresh {
		t.Fatal(fresh, e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if got, fresh, e := s.BeginDelivery(r.Ref, testNow); e != nil || fresh || got.DeliveryState != StateUncertain {
		t.Fatal(got, fresh, e)
	}
	if e = s.Prune(testNow.Add(GuardTTL * 2)); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Request(r.Ref, testNow.Add(RequestTTL)); !errors.Is(e, ErrExpired) {
		t.Fatal(e)
	}
	if e = s.FinishDelivery(r.Ref, StateSent, "native receipt"); e != nil {
		t.Fatal("unresolved was evicted", e)
	}
	if _, _, e = s.BeginDelivery(r.Ref, testNow.Add(RequestTTL)); !errors.Is(e, ErrExpired) {
		t.Fatal(e)
	}
	for _, at := range []time.Time{testNow.Add(-EventMaxAge - time.Second), testNow.Add(EventFutureSkew + time.Second), {}} {
		r = sampleRequest()
		r.EventID = at.String()
		r.EventAt = at
		if _, _, e = s.Admit(r, testNow); !errors.Is(e, ErrEventAge) {
			t.Fatal(at, e)
		}
	}
}

func TestPublicationRetryAndCrashAreNotResent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "discord")
	s, e := OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	r := mustAdmit(t, s, sampleRequest())
	body := strings.Repeat("🙂界", 2500)
	pub, fresh, e := s.PreparePublication(r.Ref, "", body, testNow)
	if e != nil || !fresh || len(pub.Chunks) < 2 {
		t.Fatal(pub, fresh, e)
	}
	joined := ""
	for _, c := range pub.Chunks {
		joined += c.Body
		if c.ID == "" {
			t.Fatal("missing chunk id")
		}
	}
	if joined != body {
		t.Fatal("chunk corruption")
	}
	if _, fresh, e = s.BeginPublication(r.Ref, "", testNow); e != nil || !fresh {
		t.Fatal(fresh, e)
	}
	s.Close()
	s, e = OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	again, fresh, e := s.PreparePublication(r.Ref, "", body, testNow)
	if e != nil || fresh || again.State != StateUncertain || !reflect.DeepEqual(again.Chunks, pub.Chunks) {
		t.Fatal(again, fresh, e)
	}
	if _, fresh, e = s.BeginPublication(r.Ref, "", testNow); e != nil || fresh {
		t.Fatal("ambiguous send retried", fresh, e)
	}
	if _, _, e = s.PreparePublication(r.Ref, "", "changed", testNow); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	if e = s.FinishPublication(r.Ref, "", StateSent, []string{"discord-id"}, "REST receipt"); e != nil {
		t.Fatal(e)
	}
	receipt, _, e := s.PreparePublication(r.Ref, "", body, testNow)
	if e != nil || receipt.DiscordMessageIDs[0] != "discord-id" {
		t.Fatal(receipt, e)
	}
	if _, fresh, e = s.PreparePublication(r.Ref, "follow-up", "extra", testNow); e != nil || !fresh {
		t.Fatal(fresh, e)
	}
}

func TestContextPaginationBoundsAndExpiry(t *testing.T) {
	s := openTestStore(t)
	r := sampleRequest()
	r.Context = nil
	for i := range 90 {
		r.Context = append(r.Context, ContextMessage{ID: strings.Repeat("x", i+1), Text: strings.Repeat("🙂", 100)})
	}
	r = mustAdmit(t, s, r)
	offset := 0
	var messages []ContextMessage
	for {
		p, e := s.ContextPage(r.Ref, offset, 10, testNow)
		if e != nil {
			t.Fatal(e)
		}
		if len(p.Messages) > 10 {
			t.Fatal("page unbounded")
		}
		messages = append(messages, p.Messages...)
		if p.NextOffset < 0 {
			break
		}
		if p.NextOffset <= offset {
			t.Fatal("cursor did not advance")
		}
		offset = p.NextOffset
	}
	if !reflect.DeepEqual(messages, r.Context) {
		t.Fatal("snapshot pages changed")
	}
	if _, e := s.ContextPage(r.Ref, 0, 10, testNow.Add(RequestTTL)); !errors.Is(e, ErrExpired) {
		t.Fatal(e)
	}
}
