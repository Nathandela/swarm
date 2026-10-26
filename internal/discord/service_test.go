package discord

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

type fakeWorkers struct {
	mu    sync.Mutex
	rows  []Worker
	sends []protocol.ComposerSendReq
	ops   []string
	err   error
}

func (f *fakeWorkers) Roster(context.Context) ([]Worker, error) {
	return append([]Worker(nil), f.rows...), nil
}
func (f *fakeWorkers) Submit(_ context.Context, op string, req protocol.ComposerSendReq) (protocol.SemanticMessageReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, req)
	f.ops = append(f.ops, op)
	return protocol.SemanticMessageReceipt{OperationID: op, Session: req.Session, State: "submitted"}, f.err
}

type fakeMessenger struct {
	mu       sync.Mutex
	posts    []Outbound
	original Input
	err      error
}

func (f *fakeMessenger) Post(_ context.Context, out Outbound) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts = append(f.posts, out)
	return "posted", f.err
}
func (f *fakeMessenger) Message(context.Context, Route) (Input, error) { return f.original, nil }
func serviceRig(t *testing.T) (*Service, *fakeWorkers, *fakeMessenger) {
	t.Helper()
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	w := &fakeWorkers{rows: []Worker{{Target: Target{ID: "local/one", Instance: "instance", Name: "Reviewer", Agent: "codex"}, State: "running"}}}
	m := &fakeMessenger{}
	c := Config{Version: 1, InstallationID: "installation", Name: "Atlas", ApplicationID: "100", OwnerID: "1", GuildIDs: []string{"10"}}
	return NewService(c, s, w, m), w, m
}
func incoming() Input {
	return Input{EventID: "event", Actor: Actor{ID: "2", Name: "Alice"}, Route: Route{GuildID: "10", ChannelID: "20", MessageID: "30"}, Text: "Why does this fail?", TargetID: "local/one", Addressed: true, At: time.Now()}
}
func TestServiceExternalInputKeepsAttributionAndDoesNotWaitForIdle(t *testing.T) {
	s, w, _ := serviceRig(t)
	w.rows[0].State = "working"
	in := incoming()
	r, err := s.Handle(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.sends) != 1 {
		t.Fatalf("native submissions=%d", len(w.sends))
	}
	req := w.sends[0]
	if req.Origin == nil || req.Origin.ActorID != "2" || req.Origin.Kind != "external" || req.SessionInstance != "instance" {
		t.Fatalf("wrong origin: %+v", req)
	}
	if !strings.Contains(req.Text, r.Ref) || len(req.Text) > protocol.MaxSendInputText {
		t.Fatalf("invalid envelope: %q", req.Text)
	}
	if _, err = s.Handle(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(w.sends) != 1 {
		t.Fatal("duplicate event submitted again")
	}
}
func TestServiceAdmissionAndOrdinaryThreadChatterDoNotInvokeWorker(t *testing.T) {
	s, w, _ := serviceRig(t)
	for _, alter := range []func(*Input){func(i *Input) { i.Route.GuildID = "99" }, func(i *Input) { i.Bot = true }, func(i *Input) { i.Addressed = false }} {
		in := incoming()
		alter(&in)
		_, _ = s.Handle(context.Background(), in)
	}
	if len(w.sends) != 0 {
		t.Fatalf("unexpected submissions=%d", len(w.sends))
	}
}
func TestServiceEndorsementIsOwnerMessageAndPreservesOriginalAuthor(t *testing.T) {
	s, w, m := serviceRig(t)
	in := incoming()
	m.original = in
	endorsedAt := time.Now()
	r, err := s.Handle(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Endorse(context.Background(), Actor{ID: "2"}, r.Ref, "click-wrong", time.Now()); err == nil {
		t.Fatal("colleague endorsed")
	}
	if len(w.sends) != 1 {
		t.Fatal("wrong owner caused native write")
	}
	if _, err = s.Endorse(context.Background(), Actor{ID: "1", Name: "Nathan"}, r.Ref, "click-owner", endorsedAt); err != nil {
		t.Fatal(err)
	}
	if len(w.sends) != 2 || w.sends[1].Origin.Kind != "owner_endorsed" || w.sends[1].Origin.ActorID != "2" || w.sends[1].Origin.EndorserID != "1" {
		t.Fatalf("incorrect endorsement: %+v", w.sends)
	}
	if _, err = s.Endorse(context.Background(), Actor{ID: "1"}, r.Ref, "click-owner", endorsedAt); err != nil {
		t.Fatal(err)
	}
	if len(w.sends) != 2 {
		t.Fatal("endorsement replayed")
	}
}
func TestServiceExplicitRepliesStayBoundAndLostAckNeverRunsWorkAgain(t *testing.T) {
	s, w, m := serviceRig(t)
	a := incoming()
	r, err := s.Handle(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	b := incoming()
	b.EventID = "other"
	b.Route.ChannelID = "21"
	b.Route.MessageID = "31"
	if _, err = s.Handle(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	before := len(m.posts)
	if _, err = s.Reply(context.Background(), r.Ref, "final", "Deliberate answer"); err != nil {
		t.Fatal(err)
	}
	if len(m.posts) != before+1 || m.posts[len(m.posts)-1].Route.ChannelID != "20" {
		t.Fatal("reply crossed request boundaries")
	}
	if _, err = s.Reply(context.Background(), r.Ref, "final", "Deliberate answer"); err != nil {
		t.Fatal(err)
	}
	if len(m.posts) != before+1 {
		t.Fatal("publication repeated")
	}
	m.err = errors.New("HTTP response lost")
	if _, err = s.Reply(context.Background(), r.Ref, "progress", "Working"); err == nil {
		t.Fatal("ambiguous publication claimed success")
	}
	if _, err = s.Reply(context.Background(), r.Ref, "progress", "Working"); err == nil {
		t.Fatal("uncertain publication became successful")
	}
	if len(w.sends) != 2 || len(m.posts) != before+2 {
		t.Fatal("uncertainty repeated model or publication")
	}
}
func TestServiceOptOutClosesNewContextAndPublicationWithoutStoppingWorker(t *testing.T) {
	s, w, _ := serviceRig(t)
	r, err := s.Handle(context.Background(), incoming())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Store.SetOptOut(r.Target, true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Context(r.Ref, 0); !errors.Is(err, ErrOptedOut) {
		t.Fatalf("context optout=%v", err)
	}
	if _, err = s.Reply(context.Background(), r.Ref, "final", "answer"); !errors.Is(err, ErrOptedOut) {
		t.Fatalf("reply optout=%v", err)
	}
	if len(w.sends) != 1 {
		t.Fatal("optout changed accepted native work")
	}
}
