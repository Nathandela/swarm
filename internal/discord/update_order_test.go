package discord

import (
	"context"
	"github.com/bwmarrin/discordgo"
	"sync/atomic"
	"testing"
)

type reviewUpdateScope struct {
	*fakeMessenger
	calls            atomic.Int32
	entered, release chan struct{}
}

func (m *reviewUpdateScope) Resolve(ctx context.Context, r Route) (Route, error) {
	if m.calls.Add(1) == 1 {
		close(m.entered)
		select {
		case <-m.release:
		case <-ctx.Done():
			return Route{}, ctx.Err()
		}
	}
	return r, nil
}
func TestReviewOlderUpdateCannotOverwriteNewerUpdate(t *testing.T) {
	s, w, m := serviceRig(t)
	in := incoming()
	in.Addressed = false
	in.Text = "original"
	if e := s.Join(in.Route, w.rows[0].Target); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Handle(context.Background(), in); e != nil {
		t.Fatal(e)
	}
	gate := &reviewUpdateScope{fakeMessenger: m, entered: make(chan struct{}), release: make(chan struct{})}
	s.Messenger = gate
	c, _ := NewDiscordClient("test")
	b := NewBridge(s, c)
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.MessageUpdate(context.Background(), &discordgo.MessageUpdate{Message: &discordgo.Message{ID: "30", ChannelID: "20", Content: "older edit"}})
	}()
	<-gate.entered
	b.MessageUpdate(context.Background(), &discordgo.MessageUpdate{Message: &discordgo.Message{ID: "30", ChannelID: "20", Content: "latest edit"}})
	close(gate.release)
	<-done
	rows := s.threadContext("20")
	if len(rows) != 1 || rows[0].Text != "latest edit" {
		t.Fatalf("late old update overwrote latest context: %+v", rows)
	}
}
