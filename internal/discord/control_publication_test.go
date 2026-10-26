package discord

import (
	"context"
	"errors"
	"testing"
)

func TestControlClaimsBindActionsAudienceAndNeverResend(t *testing.T) {
	s, _, m := serviceRig(t)
	r, e := s.Handle(context.Background(), incoming())
	if e != nil {
		t.Fatal(e)
	}
	out := Outbound{Route: r.Route, Text: "Accepted", Actions: []Action{{ID: "endorse:" + r.Ref, Label: "Endorse"}}}
	first, e := s.Control(context.Background(), r.Ref, "accepted", out)
	if e != nil {
		t.Fatal(e)
	}
	again, e := s.Control(context.Background(), r.Ref, "accepted", out)
	if e != nil || len(m.posts) != 1 || first.ID != again.ID || first.State != StateSent {
		t.Fatal("duplicate control posted", e, len(m.posts))
	}
	out.Actions[0].Label = "changed"
	if _, e = s.Control(context.Background(), r.Ref, "accepted", out); !errors.Is(e, ErrConflict) {
		t.Fatal("changed actions reused claim", e)
	}
	out.Route.ChannelID = "other"
	if _, e = s.Control(context.Background(), r.Ref, "other", out); !errors.Is(e, ErrInvalid) {
		t.Fatal("arbitrary audience accepted", e)
	}
	if _, e = s.Reply(context.Background(), r.Ref, "control:accepted", "worker"); !errors.Is(e, ErrInvalid) {
		t.Fatal("worker reused control namespace", e)
	}
	out = Outbound{Route: r.Route, Text: "unacknowledged"}
	m.err = errors.New("ambiguous transport")
	if _, e = s.Control(context.Background(), r.Ref, "uncertain", out); e == nil {
		t.Fatal("missing transport error")
	}
	m.err = nil
	if _, e = s.Control(context.Background(), r.Ref, "uncertain", out); !errors.Is(e, ErrDeliveryUncertain) || len(m.posts) != 2 {
		t.Fatal("ambiguous control resent", e, len(m.posts))
	}
}
