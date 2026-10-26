package discord

import (
	"context"
	"testing"
)

func TestJoinedDiscussionKeepsContextButOnlyAddressedMessagesRun(t *testing.T) {
	s, w, _ := serviceRig(t)
	in := incoming()
	if err := s.Join(in.Route, w.rows[0].Target); err != nil {
		t.Fatal(err)
	}
	in.Addressed = false
	in.Text = "The retry deadline is five seconds."
	if _, err := s.Handle(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(w.sends) != 0 {
		t.Fatal("ordinary chatter invoked model")
	}
	in.EventID = "question"
	in.Route.MessageID = "31"
	in.Text = "Does this explain it?"
	in.Addressed = true
	in.TargetID = ""
	r, err := s.Handle(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	page, err := s.Context(r.Ref, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.sends) != 1 || len(page.Messages) != 2 || page.Messages[0].Text != "The retry deadline is five seconds." {
		t.Fatalf("joined context=%+v", page)
	}
}
func TestNamesAreLabelsAndAmbiguousNamesNeedSelection(t *testing.T) {
	s, w, _ := serviceRig(t)
	w.rows = append(w.rows, Worker{Target: Target{ID: "local/two", Instance: "other", Name: "Reviewer", Agent: "claude"}, State: "running"})
	in := incoming()
	in.TargetID = "Reviewer"
	if _, err := s.Handle(context.Background(), in); err == nil {
		t.Fatal("duplicate display name silently selected")
	}
	in.TargetID = "local/two"
	if _, err := s.Handle(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(w.sends) != 1 || w.sends[0].Session != "local/two" {
		t.Fatal("stable ID did not select intended agent")
	}
}
