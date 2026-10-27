package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	bolt "go.etcd.io/bbolt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOwnBotGatewayProofSettlesLostAckWithoutRepost(t *testing.T) {
	s, w, m := serviceRig(t)
	r, e := s.Handle(context.Background(), incoming())
	if e != nil {
		t.Fatal(e)
	}
	m.err = errors.New("lost HTTP acknowledgement")
	p, e := s.Reply(context.Background(), r.Ref, "", "answer")
	if e == nil || p.State != StateUncertain {
		t.Fatal(p, e)
	}
	message := PublicationMessage{AuthorID: s.Config.ApplicationID, ChannelID: r.Route.ChannelID, MessageID: "gateway", Nonce: p.Chunks[0].ID, Text: "answer"}
	if e = s.ObservePublication(context.Background(), message); e != nil {
		t.Fatal(e)
	}
	m.err = nil
	again, e := s.Reply(context.Background(), r.Ref, "", "answer")
	if e != nil || again.State != StateSent || len(m.posts) != 1 || len(w.sends) != 1 {
		t.Fatal("gateway proof caused repost/native input", again, e, len(m.posts), len(w.sends))
	}
	w.rows = append(w.rows, Worker{Target: Target{ID: "local/other", Instance: "other", Name: r.Target.Name, Agent: "codex"}, State: "running"})
	target, e := s.TargetForReply(context.Background(), r.Route.ChannelID, message.MessageID)
	if e != nil || target.ID != r.Target.ID {
		t.Fatal(target, e)
	}
	if _, e = s.TargetForReply(context.Background(), "other-channel", message.MessageID); !errors.Is(e, ErrNotFound) {
		t.Fatal("crosschannel lookup", e)
	}
	if e = s.Store.SetOptOut(r.Target, true); e != nil {
		t.Fatal(e)
	}
	if _, e = s.TargetForReply(context.Background(), r.Route.ChannelID, message.MessageID); !errors.Is(e, ErrOptedOut) {
		t.Fatal("excluded reply target available", e)
	}
}

func TestPublicationChunkCapacityRefusesBeforePost(t *testing.T) {
	s, _, m := serviceRig(t)
	r, e := s.Handle(context.Background(), incoming())
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Store.db.Update(func(tx *bolt.Tx) error {
		reserved := Publication{RequestRef: r.Ref, ID: "reserved", State: StatePrepared}
		for i := range MaxPublicationIndex {
			reserved.Chunks = append(reserved.Chunks, Chunk{ID: fmt.Sprint(i), Body: "x", State: StatePrepared})
		}
		raw, e := json.Marshal(reserved)
		if e != nil {
			return e
		}
		return tx.Bucket([]byte("publications")).Put(publicationKey(r.Ref, "reserved"), raw)
	}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Reply(context.Background(), r.Ref, "", "answer"); !errors.Is(e, ErrCapacity) || len(m.posts) != 0 {
		t.Fatal("capacity caused publication side effect", e, len(m.posts))
	}
}
func TestPublicationRecoveryBeforeChunkClaimIsSafeToContinue(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "discord")
	store, e := OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	r := mustAdmit(t, store, sampleRequest())
	p, _, e := store.PreparePublication(r.Ref, "", "answer", testNow)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = store.BeginPublication(r.Ref, "", testNow); e != nil {
		t.Fatal(e)
	}
	if e = store.Close(); e != nil {
		t.Fatal(e)
	}
	store, e = OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = store.Close() }()
	after, fresh, e := store.BeginPublication(r.Ref, "", testNow)
	if e != nil || !fresh || after.Chunks[0].ID != p.Chunks[0].ID {
		t.Fatal("no-effect preparation incorrectly blocked", after.State, fresh, e)
	}
}
func TestPublicationObservationRejectsUnmatchedEvidence(t *testing.T) {
	s, _, m := serviceRig(t)
	r, e := s.Handle(context.Background(), incoming())
	if e != nil {
		t.Fatal(e)
	}
	m.err = errors.New("lost ack")
	p, _ := s.Reply(context.Background(), r.Ref, "", "answer")
	base := PublicationMessage{AuthorID: s.Config.ApplicationID, ChannelID: r.Route.ChannelID, MessageID: "gateway", Nonce: p.Chunks[0].ID, Text: "answer"}
	for _, mutate := range []func(*PublicationMessage){func(m *PublicationMessage) { m.AuthorID = "other-bot" }, func(m *PublicationMessage) { m.WebhookID = "webhook" }, func(m *PublicationMessage) { m.ChannelID = "other" }, func(m *PublicationMessage) { m.Text = "changed" }, func(m *PublicationMessage) { m.Nonce = "other" }} {
		message := base
		mutate(&message)
		_ = s.ObservePublication(context.Background(), message)
		got, e := s.Store.publication(r.Ref, "")
		if e != nil || got.State == StateSent {
			t.Fatal("unmatched evidence settled publication", got, e)
		}
	}
}

type observingMessenger struct {
	*fakeMessenger
	s       *Service
	postErr error
}

func (m observingMessenger) Post(ctx context.Context, out Outbound) (string, error) {
	id, _ := m.fakeMessenger.Post(ctx, out)
	e := m.s.ObservePublication(ctx, PublicationMessage{AuthorID: m.s.Config.ApplicationID, ChannelID: out.Route.ChannelID, MessageID: id, Nonce: out.Nonce, Text: out.Text})
	if e != nil {
		return "", e
	}
	return id, m.postErr
}
func TestGatewayEvidenceWinsRacingRestFailure(t *testing.T) {
	s, _, m := serviceRig(t)
	r, e := s.Handle(context.Background(), incoming())
	if e != nil {
		t.Fatal(e)
	}
	s.Messenger = observingMessenger{fakeMessenger: m, s: s, postErr: errors.New("lost ack after gateway")}
	p, e := s.Reply(context.Background(), r.Ref, "", "answer")
	if e != nil || p.State != StateSent {
		t.Fatal("monotone evidence returned false error", p, e)
	}
	out := Outbound{Route: r.Route, Text: "Accepted", Actions: []Action{{ID: "endorse:" + r.Ref, Label: "Endorse"}}}
	p, e = s.Control(context.Background(), r.Ref, "accepted", out)
	if e != nil || p.State != StateSent || len(p.Chunks) != 1 {
		t.Fatal("control canonical body was mistaken for posted text", p, e)
	}
}
func TestPartialChunkProofNeverClaimsWholeBodySent(t *testing.T) {
	s, _, m := serviceRig(t)
	r, e := s.Handle(context.Background(), incoming())
	if e != nil {
		t.Fatal(e)
	}
	m.err = errors.New("lost first chunk ack")
	body := strings.Repeat("x", 4000)
	p, _ := s.Reply(context.Background(), r.Ref, "", body)
	if len(p.Chunks) < 2 {
		t.Fatal("fixture not chunked")
	}
	if e = s.ObservePublication(context.Background(), PublicationMessage{AuthorID: s.Config.ApplicationID, ChannelID: r.Route.ChannelID, MessageID: "first", Nonce: p.Chunks[0].ID, Text: p.Chunks[0].Body}); e != nil {
		t.Fatal(e)
	}
	partial, e := s.Store.publication(r.Ref, "")
	if e != nil || partial.State == StateSent || len(partial.DiscordMessageIDs) != 1 {
		t.Fatal("partial chunk proof claimed complete", partial, e)
	}
	m.err = nil
	done, e := s.Reply(context.Background(), r.Ref, "", body)
	if e != nil || done.State != StateSent || len(m.posts) != len(p.Chunks) {
		t.Fatal("continuation reposted proven first chunk", done, e, len(m.posts), len(p.Chunks))
	}
}

func TestConcurrentRestAndGatewayReceiptsAreMonotone(t *testing.T) {
	s, w, _ := serviceRig(t)
	r, e := s.Handle(context.Background(), incoming())
	if e != nil {
		t.Fatal(e)
	}
	p, _, e := s.Store.PreparePublication(r.Ref, "", "answer", s.Now())
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.Store.BeginPublication(r.Ref, "", s.Now()); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.Store.beginPublicationChunk(r.Ref, "", p.Chunks[0].ID, s.Now()); e != nil {
		t.Fatal(e)
	}
	var group sync.WaitGroup
	group.Go(func() {
		if e := s.ObservePublication(context.Background(), PublicationMessage{AuthorID: s.Config.ApplicationID, ChannelID: r.Route.ChannelID, MessageID: "message", Nonce: p.Chunks[0].ID, Text: "answer"}); e != nil {
			t.Error(e)
		}
	})
	group.Go(func() {
		if _, e := s.Store.finishPublicationChunk(r.Ref, "", p.Chunks[0].ID, StateSent, "message", "REST ack"); e != nil {
			t.Error(e)
		}
	})
	group.Wait()
	done, e := s.Store.publication(r.Ref, "")
	if e != nil || done.State != StateSent || len(done.DiscordMessageIDs) != 1 || len(w.sends) != 1 {
		t.Fatal(done, e)
	}
	next := w.rows[0].Target
	next.ID = "local/resumed"
	next.Instance = "new-instance"
	w.rows[0].Supersedes = []string{w.rows[0].ID}
	w.rows[0].Target = next
	target, e := s.TargetForReply(context.Background(), r.Route.ChannelID, "message")
	if e != nil || target.ID != next.ID || target.Instance != next.Instance {
		t.Fatal("reply did not follow unique explicit resume", target, e)
	}
	s.Now = func() time.Time { return r.ExpiresAt }
	if _, e = s.TargetForReply(context.Background(), r.Route.ChannelID, "message"); !errors.Is(e, ErrExpired) {
		t.Fatal("reply index resurrected expired context", e)
	}
	if e = s.Store.Prune(r.ExpiresAt.Add(GuardTTL)); e != nil {
		t.Fatal(e)
	}
	if _, e = s.TargetForReply(context.Background(), r.Route.ChannelID, "message"); !errors.Is(e, ErrNotFound) {
		t.Fatal("expired routing index retained", e)
	}
}
