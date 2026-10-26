package discord

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/testbin"
	bolt "go.etcd.io/bbolt"
)

func TestMain(m *testing.M) {
	if code, done := testbin.Supervise(); done {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

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
	defer func() { _ = s.Close() }()
	again, fresh, e := s.Admit(saved, testNow)
	if e != nil || fresh || !reflect.DeepEqual(again, saved) {
		t.Fatalf("replay %+v %v %v", again, fresh, e)
	}
	for _, change := range []func(*Request){func(r *Request) { r.Text = "edit" }, func(r *Request) { r.Author.ID = "bob" }, func(r *Request) { r.Target.ID = "other" }} {
		r = saved
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
		_ = extra.Close()
		t.Fatal("second owner admitted")
	}
}

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
	other.Target.ID = "independent"
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
	defer func() { _ = s.Close() }()
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
	if _, fresh, e = s.beginPublicationChunk(r.Ref, "", pub.Chunks[0].ID, testNow); e != nil || !fresh {
		t.Fatal(fresh, e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	again, fresh, e := s.PreparePublication(r.Ref, "", body, testNow)
	if e != nil || fresh || again.State != StateUncertain {
		t.Fatal(again.State, fresh, e)
	}
	for i, chunk := range again.Chunks {
		if chunk.ID != pub.Chunks[i].ID || chunk.Body != pub.Chunks[i].Body {
			t.Fatal("chunk identity changed after restart")
		}
	}
	if _, fresh, e = s.BeginPublication(r.Ref, "", testNow); e != nil || fresh {
		t.Fatal("ambiguous send retried", fresh, e)
	}
	if _, _, e = s.PreparePublication(r.Ref, "", "changed", testNow); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	ids := make([]string, len(pub.Chunks))
	for i := range ids {
		ids[i] = fmt.Sprint("discord-", i)
	}
	if e = s.FinishPublication(r.Ref, "", StateSent, ids, "REST receipt"); e != nil {
		t.Fatal(e)
	}
	receipt, _, e := s.PreparePublication(r.Ref, "", body, testNow)
	if e != nil || !reflect.DeepEqual(receipt.DiscordMessageIDs, ids) {
		t.Fatal(receipt, e)
	}
	if _, fresh, e = s.PreparePublication(r.Ref, "follow-up", "extra", testNow); e != nil || !fresh {
		t.Fatal(fresh, e)
	}
}

func TestDeniedContextReturnsNoBodyAndLongPagesStayBounded(t *testing.T) {
	s := openTestStore(t)
	r := sampleRequest()
	r.Context[0].Text = strings.Repeat("界🙂", 10000)
	r = mustAdmit(t, s, r)
	var body strings.Builder
	for offset := 0; ; {
		p, e := s.ContextPage(r.Ref, offset, MaxContextPageSize, testNow)
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := json.Marshal(p)
		if len(raw) > MaxContextPageBytes+1024 {
			t.Fatal("page exceeds byte bound", len(raw))
		}
		for _, m := range p.Messages {
			body.WriteString(m.Text)
		}
		if p.NextOffset < 0 {
			break
		}
		offset = p.NextOffset
	}
	if body.String() != r.Context[0].Text {
		t.Fatal("large Unicode source changed")
	}
	for _, revoke := range []bool{false, true} {
		if revoke {
			if e := s.SetOptOut(r.Target, true); e != nil {
				t.Fatal(e)
			}
		}
		at := testNow
		if !revoke {
			at = at.Add(RequestTTL)
		}
		got, e := s.Request(r.Ref, at)
		if e == nil || got.Text != "" || len(got.Context) != 0 {
			t.Fatal("denied request exposed body", e)
		}
	}
}

func TestEndorsementSnapshotResumeAndInFlightRevocation(t *testing.T) {
	s := openTestStore(t)
	r := sampleRequest()
	r.Authority = AuthorityOwnerEndorsed
	r.EndorserID = "owner"
	r = mustAdmit(t, s, r)
	if r.Author.ID != "alice" || r.EndorserID != "owner" {
		t.Fatal("endorser replaced source author")
	}
	if _, fresh, e := s.BeginDelivery(r.Ref, testNow); e != nil || !fresh {
		t.Fatal(fresh, e)
	}
	p, _, e := s.PreparePublication(r.Ref, "", "answer", testNow)
	if e != nil {
		t.Fatal(e)
	}
	if _, fresh, e := s.BeginPublication(r.Ref, "", testNow); e != nil || !fresh {
		t.Fatal(fresh, e)
	}
	if e = s.SetOptOut(r.Target, true); e != nil {
		t.Fatal(e)
	}
	if e = s.FinishDelivery(r.Ref, StateSent, "native receipt"); e != nil {
		t.Fatal("inflight cannot finish", e)
	}
	if e = s.FinishPublication(r.Ref, "", StateSent, []string{"message"}, "REST receipt"); e != nil {
		t.Fatal("inflight publication cannot finish", e)
	}
	next := r.Target
	next.Instance = "resumed"
	if e = s.FollowTarget(r.Target, next); e != nil {
		t.Fatal(e)
	}
	if out, e := s.IsOptedOut(next); e != nil || !out {
		t.Fatal("explicit resume lost optout", out, e)
	}
	if p.Body != "answer" {
		t.Fatal("publication changed")
	}
}

func TestBoundsAndFailedTransactionsDoNotCreateClaims(t *testing.T) {
	t.Run("count", func(t *testing.T) {
		s := openTestStore(t)
		if e := s.db.Update(func(tx *bolt.Tx) error {
			for i := range MaxRequests {
				if e := tx.Bucket([]byte("requests")).Put([]byte(fmt.Sprint(i)), []byte("{}")); e != nil {
					return e
				}
			}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
		if _, _, e := s.Admit(sampleRequest(), testNow); !errors.Is(e, ErrCapacity) {
			t.Fatal(e)
		}
	})
	t.Run("bytes", func(t *testing.T) {
		s := openTestStore(t)
		if e := s.db.Update(func(tx *bolt.Tx) error {
			return tx.Bucket([]byte("publications")).Put([]byte("full"), []byte(strings.Repeat("x", MaxStoreBytes-32)))
		}); e != nil {
			t.Fatal(e)
		}
		if _, _, e := s.Admit(sampleRequest(), testNow); !errors.Is(e, ErrCapacity) {
			t.Fatal(e)
		}
		if e := s.db.View(func(tx *bolt.Tx) error {
			for _, name := range []string{"requests", "events", "operations"} {
				if tx.Bucket([]byte(name)).Stats().KeyN != 0 {
					t.Fatal("failed admission left partial identity", name)
				}
			}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("outbox", func(t *testing.T) {
		s := openTestStore(t)
		r := mustAdmit(t, s, sampleRequest())
		if e := s.db.Update(func(tx *bolt.Tx) error {
			for i := range MaxPublications {
				if e := tx.Bucket([]byte("publications")).Put([]byte(fmt.Sprint(i)), []byte("{}")); e != nil {
					return e
				}
			}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
		if _, _, e := s.PreparePublication(r.Ref, "", "answer", testNow); !errors.Is(e, ErrCapacity) {
			t.Fatal(e)
		}
		if _, _, e := s.BeginPublication(r.Ref, "", testNow); !errors.Is(e, ErrNotFound) {
			t.Fatal("capacity left publication claim", e)
		}
	})
	t.Run("closed disk", func(t *testing.T) {
		s := openTestStore(t)
		if e := s.Close(); e != nil {
			t.Fatal(e)
		}
		if _, _, e := s.Admit(sampleRequest(), testNow); e == nil {
			t.Fatal("closed store admitted")
		}
	})
}

func TestPruneSettledBodiesRetainsGuardsAndUnresolved(t *testing.T) {
	s := openTestStore(t)
	original := sampleRequest()
	r := mustAdmit(t, s, original)
	if _, _, e := s.BeginDelivery(r.Ref, testNow); e != nil {
		t.Fatal(e)
	}
	if e := s.FinishDelivery(r.Ref, StateSent, "native"); e != nil {
		t.Fatal(e)
	}
	if e := s.Prune(testNow.Add(RequestTTL)); e != nil {
		t.Fatal(e)
	}
	if _, fresh, e := s.Admit(original, testNow.Add(RequestTTL)); !errors.Is(e, ErrExpired) || fresh {
		t.Fatal("expired guard lost", fresh, e)
	}
	if e := s.db.View(func(tx *bolt.Tx) error {
		record, e := loadRequest(tx, r.Ref)
		if e != nil {
			return e
		}
		if record.Request.Text != "" || len(record.Request.Context) != 0 {
			t.Fatal("settled body retained")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if e := s.Prune(testNow.Add(GuardTTL)); e != nil {
		t.Fatal(e)
	}
	if _, fresh, e := s.Admit(original, testNow.Add(GuardTTL)); !errors.Is(e, ErrEventAge) || fresh {
		t.Fatal("expired source readmitted", fresh, e)
	}
}

func TestConflictingConcurrentClaimsAndOperationBinding(t *testing.T) {
	s := openTestStore(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	won, conflicts := 0, 0
	for i := range 2 {
		wg.Go(func() {
			r := sampleRequest()
			r.Text = fmt.Sprint(i)
			_, fresh, e := s.Admit(r, testNow)
			mu.Lock()
			defer mu.Unlock()
			if e == nil && fresh {
				won++
			} else if errors.Is(e, ErrConflict) {
				conflicts++
			} else {
				t.Errorf("unexpected claim %v %v", fresh, e)
			}
		})
	}
	wg.Wait()
	if won != 1 || conflicts != 1 {
		t.Fatal(won, conflicts)
	}
	original := sampleRequest()
	original.EventID = "original-op"
	original.OperationID = "explicit-operation"
	saved := mustAdmit(t, s, original)
	changed := original
	changed.EventID = "different-event"
	if _, _, e := s.Admit(changed, testNow); !errors.Is(e, ErrConflict) {
		t.Fatal("native operation reused", e)
	}
	replay, fresh, e := s.Admit(original, testNow.Add(time.Hour))
	if e != nil || fresh || replay.OperationID != saved.OperationID || replay.Ref != saved.Ref {
		t.Fatal("retry changed native identity", replay, fresh, e)
	}
}

func TestSettledPublicationPrunesBodyAndUnresolvedPublicationProtectsSource(t *testing.T) {
	s := openTestStore(t)
	r := mustAdmit(t, s, sampleRequest())
	if _, _, e := s.BeginDelivery(r.Ref, testNow); e != nil {
		t.Fatal(e)
	}
	if e := s.FinishDelivery(r.Ref, StateSent, "native"); e != nil {
		t.Fatal(e)
	}
	p, _, e := s.PreparePublication(r.Ref, "", "answer", testNow)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.BeginPublication(r.Ref, "", testNow); e != nil {
		t.Fatal(e)
	}
	if e = s.Prune(testNow.Add(GuardTTL)); e != nil {
		t.Fatal(e)
	}
	if e = s.db.View(func(tx *bolt.Tx) error {
		record, e := loadRequest(tx, r.Ref)
		if e != nil {
			return e
		}
		if record.Request.Text == "" {
			t.Fatal("unresolved outbox lost source")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.BeginPublication(r.Ref, "", testNow); e != nil {
		t.Fatal(e)
	}
	if e = s.FinishPublication(r.Ref, "", StateSent, []string{"id"}, "REST"); e != nil {
		t.Fatal(e)
	}
	if e = s.Prune(testNow.Add(RequestTTL)); e != nil {
		t.Fatal(e)
	}
	if e = s.db.View(func(tx *bolt.Tx) error {
		got, e := loadPublication(tx, r.Ref, "")
		if e != nil {
			return e
		}
		if got.Body != "" || len(got.Chunks) != 0 || got.BodyHash != p.BodyHash || len(got.DiscordMessageIDs) != 1 {
			t.Fatal("incorrect guard pruning", got)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if e = s.Prune(testNow.Add(GuardTTL)); e != nil {
		t.Fatal(e)
	}
	if e = s.db.View(func(tx *bolt.Tx) error {
		_, e := loadPublication(tx, r.Ref, "")
		if !errors.Is(e, ErrNotFound) {
			t.Fatal("guard not expired", e)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}

func TestResumeCannotBypassOptOutBound(t *testing.T) {
	s := openTestStore(t)
	previous := sampleRequest().Target
	if e := s.SetOptOut(previous, true); e != nil {
		t.Fatal(e)
	}
	if e := s.db.Update(func(tx *bolt.Tx) error {
		for i := range MaxRequests - 1 {
			if e := tx.Bucket([]byte("optouts")).Put([]byte(fmt.Sprint(i)), []byte("true")); e != nil {
				return e
			}
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	next := previous
	next.ID = "next"
	if e := s.FollowTarget(previous, next); !errors.Is(e, ErrCapacity) {
		t.Fatal("resume bypassed optout cap", e)
	}
}

func TestRevisionCannotReadmitAcceptedEvent(t *testing.T) {
	s := openTestStore(t)
	r := sampleRequest()
	mustAdmit(t, s, r)
	r.MessageRevision = "2"
	r.Text = "edited source"
	if got, fresh, e := s.Admit(r, testNow); !errors.Is(e, ErrConflict) || fresh || got.Text != "" {
		t.Fatal("revision readmitted event", got, fresh, e)
	}
}
func TestAdmissionReplayCannotExposeRevokedOrExpiredSource(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprint(revoke), func(t *testing.T) {
			s := openTestStore(t)
			r := sampleRequest()
			mustAdmit(t, s, r)
			at := testNow.Add(RequestTTL)
			want := ErrExpired
			if revoke {
				at = testNow
				want = ErrOptedOut
				if e := s.SetOptOut(r.Target, true); e != nil {
					t.Fatal(e)
				}
			}
			got, fresh, e := s.Admit(r, at)
			if !errors.Is(e, want) || fresh || got.Text != "" || len(got.Context) != 0 {
				t.Fatal("denied replay exposed source", got, fresh, e)
			}
		})
	}
}
func TestExplicitIncludeSurvivesRepeatedResumeAndRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "discord")
	s, e := OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	previous := sampleRequest().Target
	next := previous
	next.ID = "resumed"
	for _, e := range []error{s.SetOptOut(previous, true), s.FollowTarget(previous, next), s.SetOptOut(next, false)} {
		if e != nil {
			t.Fatal(e)
		}
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = s.Close() }()
	for range 3 {
		if e = s.FollowTarget(previous, next); e != nil {
			t.Fatal(e)
		}
	}
	if out, e := s.IsOptedOut(next); e != nil || out {
		t.Fatal("explicit include overwritten", out, e)
	}
}
func TestStoreRejectsForeignHardlinkWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	foreign := filepath.Join(dir, "foreign")
	state := filepath.Join(dir, "state")
	if e := os.Mkdir(state, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(foreign, []byte("foreign data"), 0644); e != nil {
		t.Fatal(e)
	}
	if e := os.Link(foreign, filepath.Join(state, "discord.db")); e != nil {
		t.Fatal(e)
	}
	if s, e := OpenStore(state); e == nil {
		_ = s.Close()
		t.Fatal("hardlinked file accepted")
	}
	raw, e := os.ReadFile(foreign)
	if e != nil {
		t.Fatal(e)
	}
	info, e := os.Stat(foreign)
	if e != nil {
		t.Fatal(e)
	}
	if string(raw) != "foreign data" || info.Mode().Perm() != 0644 {
		t.Fatal("foreign inode changed", string(raw), info.Mode())
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
