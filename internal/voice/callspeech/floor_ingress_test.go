package callspeech

import (
	"context"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/voice/openailive"
)

// This socket accepts one write but withholds its return until released.
type occupiedSocket struct {
	*fakeLive
	entered chan struct{}
	release chan struct{}
}

func (s *occupiedSocket) SendAudio(ctx context.Context, pcm []byte) error {
	select {
	case <-s.entered:
	default:
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.fakeLive.SendAudio(ctx, pcm)
}

func TestNewTeamResultYieldsWhileProviderWriteIsBlocked(t *testing.T) {
	s := &occupiedSocket{&fakeLive{events: make(chan openailive.Event)}, make(chan struct{}), make(chan struct{})}
	played := make(chan time.Time, 1)
	rendered := make(chan struct{}, 1)
	l := newLane(t.Context(), Config{Team: true, Play: func(context.Context, []byte) error {
		played <- time.Now()
		return nil
	}}, provider{
		open: func(context.Context, string, string) (liveSession, error) { return s, nil },
		render: func(context.Context, string, string, string) ([]byte, error) {
			rendered <- struct{}{}
			return []byte{0, 3}, nil
		},
		transcribe: func(context.Context, string, []byte, string) (string, error) { return "hello", nil },
	})
	t.Cleanup(func() { l.Close(); await(t, l.Done()) })
	await(t, s.entered)
	at := time.Now()
	if err := l.Push("alice", playbackTestFrame()); err != nil {
		t.Fatal(err)
	}
	l.registerTurn("verified")
	if err := l.PublishForTurn("verified", "completed"); err != nil {
		t.Fatal(err)
	}
	await(t, rendered)
	select {
	case <-played:
		t.Fatal("pending speech ignored a frame already accepted by the bridge")
	case <-time.After(2 * framePeriod):
	}
	close(s.release)
	if first := await(t, played); first.Sub(at) < quietTime {
		t.Fatal("rendered reply started before the shared floor was quiet")
	}
}
