package discord

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestReplayKeepsCapturedLabelsAfterRenames(t *testing.T) {
	s, w, _ := serviceRig(t)
	in := incoming()
	first, e := s.Handle(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	w.rows[0].Name = "Renamed worker"
	in.Actor.Name = "Renamed user"
	again, e := s.Handle(context.Background(), in)
	if e != nil || len(w.sends) != 1 || again.Ref != first.Ref || again.Target.Name != first.Target.Name || again.Author.Name != first.Author.Name {
		t.Fatal("display rename changed admission", again, e, len(w.sends))
	}
	in.Text = "changed task"
	if _, e = s.Handle(context.Background(), in); !errors.Is(e, ErrConflict) {
		t.Fatal("source edit accepted", e)
	}
}

func TestResumeAliasesPersistAndDoNotIncludeIndependentAgents(t *testing.T) {
	s, w, _ := serviceRig(t)
	in := incoming()
	first, e := s.Handle(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	old := w.rows[0].Target
	next := old
	next.ID = "local/next"
	next.Instance = "next-instance"
	if e = s.Store.FollowTarget(old, next); e != nil {
		t.Fatal(e)
	}
	if e = s.Store.SetOptOut(next, true); e != nil {
		t.Fatal(e)
	}
	dir := filepath.Dir(s.Store.db.Path())
	if e = s.Store.Close(); e != nil {
		t.Fatal(e)
	}
	s.Store, e = OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = s.Store.Close() })
	if _, e = s.Store.Request(first.Ref, s.Now()); !errors.Is(e, ErrOptedOut) {
		t.Fatal("old ref lost successor exclusion after restart", e)
	}
	if excluded, e := s.Store.IsOptedOut(Target{ID: "local/independent", Instance: "independent"}); e != nil || excluded {
		t.Fatal("independent worker inherited exclusion", excluded, e)
	}
	if e = s.Store.SetOptOut(old, false); e != nil {
		t.Fatal(e)
	}
	if e = s.Store.FollowTarget(old, next); e != nil {
		t.Fatal(e)
	}
	if excluded, e := s.Store.IsOptedOut(next); e != nil || excluded {
		t.Fatal("explicit include overwritten", excluded, e)
	}
}
func TestJoinedThreadFollowsUniqueResumeWithoutReplayingOldEvent(t *testing.T) {
	s, w, _ := serviceRig(t)
	in := incoming()
	old := w.rows[0].Target
	if e := s.Join(in.Route, old); e != nil {
		t.Fatal(e)
	}
	in.TargetID = ""
	first, e := s.Handle(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	w.rows[0].ID = "local/resumed"
	w.rows[0].Instance = "new-instance"
	w.rows[0].Supersedes = []string{old.ID}
	again, e := s.Handle(context.Background(), in)
	if e != nil || again.Ref != first.Ref || len(w.sends) != 1 || again.Target.ID != old.ID {
		t.Fatal("old event reinterpreted after resume", again, e, len(w.sends))
	}
	fresh := in
	fresh.EventID = "fresh-event"
	fresh.Route.MessageID = "fresh-message"
	fresh.Text = "new task"
	next, e := s.Handle(context.Background(), fresh)
	if e != nil || next.Target.ID != w.rows[0].ID || next.Target.Instance != "new-instance" || len(w.sends) != 2 {
		t.Fatal("thread binding did not follow explicit resume", next, e)
	}
	if e = s.Store.SetOptOut(next.Target, true); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Store.Request(first.Ref, s.Now()); !errors.Is(e, ErrOptedOut) {
		t.Fatal("successor exclusion left old ref active", e)
	}
	if e = s.Store.SetOptOut(old, false); e != nil {
		t.Fatal(e)
	}
	if rows, e := s.Roster(context.Background()); e != nil || len(rows) != 1 {
		t.Fatal("explicit include overwritten by resume", rows, e)
	}
}
func TestAmbiguousResumeCannotRouteThread(t *testing.T) {
	s, w, _ := serviceRig(t)
	old := w.rows[0].Target
	if e := s.Join(incoming().Route, old); e != nil {
		t.Fatal(e)
	}
	w.rows = []Worker{{Target: Target{ID: "local/a", Instance: "a"}, Supersedes: []string{old.ID}}, {Target: Target{ID: "local/b", Instance: "b"}, Supersedes: []string{old.ID}}}
	in := incoming()
	in.TargetID = ""
	if _, e := s.Handle(context.Background(), in); !errors.Is(e, ErrSelection) || len(w.sends) != 0 {
		t.Fatal("ambiguous resume routed", e, w.sends)
	}
}
