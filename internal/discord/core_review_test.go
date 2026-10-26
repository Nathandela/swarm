package discord

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestCoreResumeExclusionTracksLogicalAgent(t *testing.T) {
	s, w, _ := serviceRig(t)
	old := w.rows[0].Target
	if e := s.Store.SetOptOut(old, true); e != nil {
		t.Fatal(e)
	}
	w.rows[0].ID = "local/resumed"
	w.rows[0].Instance = "new-instance"
	w.rows[0].Supersedes = []string{old.ID}
	if rows, e := s.Roster(context.Background()); e != nil || len(rows) != 0 {
		t.Fatal("resumed exclusion lost", rows, e)
	}
	if e := s.Store.SetOptOut(w.rows[0].Target, false); e != nil {
		t.Fatal(e)
	}
	if rows, e := s.Roster(context.Background()); e != nil || len(rows) != 1 {
		t.Fatal("explicit include overwritten", rows, e)
	}
}
func TestCoreJoinedReplayKeepsAcceptedSnapshot(t *testing.T) {
	s, w, _ := serviceRig(t)
	in := incoming()
	if e := s.Join(in.Route, w.rows[0].Target); e != nil {
		t.Fatal(e)
	}
	original, e := s.Handle(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	chatter := in
	chatter.EventID = "later"
	chatter.Route.MessageID = "later"
	chatter.Text = "later context"
	chatter.Addressed = false
	if _, e = s.Handle(context.Background(), chatter); e != nil {
		t.Fatal(e)
	}
	replay, e := s.Handle(context.Background(), in)
	if e != nil || !reflect.DeepEqual(original.Context, replay.Context) || len(w.sends) != 1 {
		t.Fatal("replay changed frozen context", e, len(w.sends))
	}
	changed := in
	changed.Text = "edited source"
	if _, e = s.Handle(context.Background(), changed); !errors.Is(e, ErrConflict) {
		t.Fatal("changed source accepted", e)
	}
}
func TestCoreDistinctOwnerClicksEndorseOnlyOnce(t *testing.T) {
	s, w, m := serviceRig(t)
	in := incoming()
	m.original = in
	source, e := s.Handle(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	first, e := s.Endorse(context.Background(), Actor{ID: s.Config.OwnerID}, source.Ref, "click-one", time.Now())
	if e != nil {
		t.Fatal(e)
	}
	again, e := s.Endorse(context.Background(), Actor{ID: s.Config.OwnerID}, source.Ref, "click-two", time.Now().Add(time.Second))
	if e != nil || first.Ref != again.Ref || first.OperationID != again.OperationID || len(w.sends) != 2 {
		t.Fatal("duplicate endorsement dispatched", first.Ref, again.Ref, e, len(w.sends))
	}
}
func TestCorePreparedExpirySealsWithoutNativeEffect(t *testing.T) {
	s := openTestStore(t)
	input := sampleRequest()
	r := mustAdmit(t, s, input)
	if _, _, e := s.PreparePublication(r.Ref, "control:proposal", "canonical", testNow); e != nil {
		t.Fatal(e)
	}
	if e := s.Prune(testNow.Add(RequestTTL)); e != nil {
		t.Fatal(e)
	}
	if e := s.db.View(func(tx *bolt.Tx) error {
		record, e := loadRequest(tx, r.Ref)
		if e != nil {
			return e
		}
		if record.Request.DeliveryState != StateFailed || record.Request.Text != "" || len(record.Request.Context) != 0 {
			t.Fatal("unstarted request retained", record.Request)
		}
		p, e := loadPublication(tx, r.Ref, "control:proposal")
		if e != nil {
			return e
		}
		if p.State != StateFailed || p.Body != "" {
			t.Fatal("unstarted publication retained", p)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if got, _, e := s.Admit(input, testNow.Add(RequestTTL)); !errors.Is(e, ErrExpired) || got.Text != "" {
		t.Fatal("expired event guard reopened", got, e)
	}
	if e := s.Prune(testNow.Add(GuardTTL)); e != nil {
		t.Fatal(e)
	}
	if _, _, e := s.Admit(input, testNow.Add(GuardTTL)); !errors.Is(e, ErrEventAge) {
		t.Fatal("old event readmitted", e)
	}
}
