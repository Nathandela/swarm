package callspeech

import (
	"context"
	"encoding/binary"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/voice/openailive"
)

type fakeLive struct {
	events chan openailive.Event
	writes atomic.Int64
	signal chan int64
	update chan [2]string
}

func (s *fakeLive) SendAudio(_ context.Context, pcm []byte) error {
	offset := s.writes.Add(20) - 20
	if hasSignal(pcm) && s.signal != nil {
		s.signal <- offset
	}
	return nil
}

func (s *fakeLive) Next(ctx context.Context) (openailive.Event, error) {
	select {
	case <-ctx.Done():
		return openailive.Event{}, ctx.Err()
	case e := <-s.events:
		return e, nil
	}
}

func (s *fakeLive) SpeakFor(ctx context.Context, id, text string) error {
	if s.update != nil {
		select {
		case s.update <- [2]string{id, text}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (s *fakeLive) Close() error { return nil }

func playbackTestFrame() []byte {
	pcm := make([]byte, frameBytes)
	for i := 0; i < len(pcm); i += 2 {
		binary.LittleEndian.PutUint16(pcm[i:], 900)
	}
	return pcm
}

func await[T any](t *testing.T, c <-chan T) T {
	t.Helper()
	select {
	case value := <-c:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("speech lane did not progress")
		var zero T
		return zero
	}
}

func pushTestSpeech(t *testing.T, l *Lane, speaker string) {
	t.Helper()
	for range 5 {
		if err := l.Push(speaker, playbackTestFrame()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(framePeriod)
	}
}

func TestTeamAddressGateAndDelegation(t *testing.T) {
	live := &fakeLive{
		events: make(chan openailive.Event, 8), signal: make(chan int64, 8),
		update: make(chan [2]string, 1),
	}
	turns := make(chan Turn, 4)
	transcribed := make(chan struct{}, 2)
	played := make(chan []byte, 4)
	var requests atomic.Int64
	l := newLane(t.Context(), Config{
		Team: true,
		Turn: func(_ context.Context, turn Turn) { turns <- turn },
		Play: func(_ context.Context, pcm []byte) error { played <- pcm; return nil },
	}, provider{
		open: func(context.Context, string, string) (liveSession, error) { return live, nil },
		transcribe: func(_ context.Context, _ string, pcm []byte, _ string) (string, error) {
			if len(pcm) != 5*frameBytes {
				return "", errors.New("utterance had wrong length")
			}
			transcribed <- struct{}{}
			if requests.Add(1) == 1 {
				return "Tell the dispatcher later", nil
			}
			return "Dispatcher, verify the latest build.", nil
		},
		render: func(_ context.Context, _, _, text string) ([]byte, error) {
			if text != "Verified publication." {
				return nil, errors.New("unexpected text rendered")
			}
			return []byte{0xfe, 0x13}, nil
		},
	})
	t.Cleanup(func() { l.Close(); await(t, l.Done()) })
	await(t, l.Ready())
	pushTestSpeech(t, l, "alice")
	await(t, transcribed)

	// Unsolicited model output and unaddressed room audio never reach playback or Live input.
	live.events <- openailive.Event{Type: "session.output_audio.delta", Audio: playbackTestFrame()}
	select {
	case <-live.signal:
		t.Fatal("unaddressed audio entered Live")
	case <-played:
		t.Fatal("unsolicited output reached playback")
	case <-time.After(60 * time.Millisecond):
	}

	pushTestSpeech(t, l, "bob")
	await(t, transcribed)
	turn := await(t, turns)
	if turn.ID == "" || turn.Speaker != "bob" || turn.Text != "verify the latest build." {
		t.Fatalf("wrong native turn: %+v", turn)
	}
	// Native dispatch and isolated publication can precede the provider's handoff.
	if err := l.PublishForTurn("stale", "Verified publication."); !errors.Is(err, ErrPublication) {
		t.Fatalf("stale turn accepted: %v", err)
	}
	if err := l.PublishForTurn(turn.ID, "Verified publication."); err != nil {
		t.Fatal(err)
	}
	offset := await(t, live.signal)
	delegation := openailive.Event{Type: "session.delegation.created", EventID: "delegate-1", OffsetMS: offset, DelegationID: "item-1"}
	live.events <- delegation
	live.events <- delegation
	if got := await(t, live.update); got != [2]string{"item-1", "Verified publication."} {
		t.Fatalf("wrong scoped update: %v", got)
	}
	if got := await(t, played); len(got) != frameBytes || !slices.Equal(got[:2], []byte{0xfe, 0x13}) {
		t.Fatal("expected isolated rendered PCM")
	}
	select {
	case <-turns:
		t.Fatal("duplicate admission")
	default:
	}
}

func TestLateUnicodeCaptionDoesNotAuthorizeNativeText(t *testing.T) {
	now := time.Now()
	l := &Lane{turns: make(chan utterance, 2), scopes: make(map[string]turnScope)}
	c := &candidate{turn: Turn{Speaker: "owner", ID: "local"}, start: 400, end: 900, last: now, voicedFrames: 10}
	tr := transcript{current: c, events: make(map[string]struct{}), delegations: make(map[string]struct{})}
	add := func(event openailive.Event, offset time.Duration) {
		if err := tr.add(event, now.Add(offset)); err != nil {
			t.Fatal(err)
		}
	}
	add(openailive.Event{Type: "session.input_transcript.delta", EventID: "p1", Text: "Inspect ca", StartMS: 400, EndMS: 620}, 0)
	add(openailive.Event{Type: "session.delegation.created", EventID: "d1", DelegationID: "item-1", OffsetMS: 600}, 100*time.Millisecond)
	tr.closeCurrent(l)
	if got := await(t, l.turns).turn; got.Text != "" || got.ID != "local" {
		t.Fatal("incremental caption escaped as authoritative text")
	}
	tail := openailive.Event{Type: "session.input_transcript.delta", EventID: "p2", Text: "fé ☕", StartMS: 620, EndMS: 900}
	add(tail, tailTime+20*time.Millisecond)
	add(tail, tailTime+30*time.Millisecond)
	tr.settle(l, now.Add(tailTime+stableTime+30*time.Millisecond))
	if got := c.text.String(); got != "Inspect café ☕" || l.scopes["local"].delegationID != "item-1" {
		t.Fatal("late Unicode caption or original delegation was lost")
	}
}

func TestTeamChatterAndOverlapDoNotCancelPlayback(t *testing.T) {
	live := &fakeLive{events: make(chan openailive.Event)}
	rendering := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	stopped := make(chan struct{}, 1)
	transcribed := make(chan struct{}, 1)
	var requests atomic.Int64
	l := newLane(t.Context(), Config{Team: true,
		Stop: func() { stopped <- struct{}{} },
		Play: func(context.Context, []byte) error { return nil },
	}, provider{
		open: func(context.Context, string, string) (liveSession, error) { return live, nil },
		transcribe: func(context.Context, string, []byte, string) (string, error) {
			transcribed <- struct{}{}
			if requests.Add(1) == 1 {
				return "This is unrelated.", nil
			}
			return "Dispatcher, check the build.", nil
		},
		render: func(ctx context.Context, _, _, _ string) ([]byte, error) {
			rendering <- struct{}{}
			<-ctx.Done()
			canceled <- struct{}{}
			return nil, ctx.Err()
		},
	})
	t.Cleanup(func() { l.Close(); await(t, l.Done()) })
	await(t, l.Ready())
	l.registerTurn("existing")
	if err := l.PublishForTurn("existing", "Verified publication."); err != nil {
		t.Fatal(err)
	}
	await(t, rendering)
	for _, speaker := range []string{"alice", "bob", "alice", "bob", "bob"} {
		if err := l.Push(speaker, playbackTestFrame()); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-transcribed:
		t.Fatal("overlapped speech entered the provider")
	case <-canceled:
		t.Fatal("overlap canceled verified playback")
	case <-time.After(quietTime + 60*time.Millisecond):
	}
	pushTestSpeech(t, l, "alice")
	await(t, transcribed)
	select {
	case <-canceled:
		t.Fatal("unaddressed chatter canceled verified playback")
	case <-time.After(60 * time.Millisecond):
	}
	pushTestSpeech(t, l, "alice")
	await(t, transcribed)
	await(t, stopped)
	await(t, canceled)
}

func TestAddressRequiresVocative(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"Dispatcher, show the build.", "show the build."},
		{"Dispatcher. Show the build.", "Show the build."},
		{"Dispatcher wants a build.", ""},
		{"Dispatcher’s job is done.", ""},
		{"I called Dispatcher, yesterday.", ""},
		{"Dispatchers, build.", ""},
		{"Dispatcher!", ""},
	} {
		if got := addressedText(tc.text); got != tc.want {
			t.Errorf("%q gave %q, want %q", tc.text, got, tc.want)
		}
	}
}

func TestAmbiguousHandoffFallsBackWithLocalIdentifiers(t *testing.T) {
	now := time.Now()
	l := &Lane{turns: make(chan utterance, 2), scopes: make(map[string]turnScope)}
	first := &candidate{turn: Turn{Speaker: "one", ID: "local-1"}, start: 100, end: 500, last: now}
	second := &candidate{turn: Turn{Speaker: "two", ID: "local-2"}, start: 950, end: 1050, last: now}
	tr := transcript{pending: []*candidate{first, second}, events: make(map[string]struct{}), delegations: make(map[string]struct{})}
	event := openailive.Event{Type: "session.delegation.created", EventID: "ambiguous", DelegationID: "item-ambiguous", OffsetMS: 1000}
	if err := tr.add(event, now); err != nil {
		t.Fatal(err)
	}
	l.registerTurn("local-1")
	l.registerTurn("local-2")
	tr.settle(l, now.Add(hardTail+time.Millisecond))
	if !l.scopes["local-1"].settled || !l.scopes["local-2"].settled ||
		l.scopes["local-1"].delegationID != "" || l.scopes["local-2"].delegationID != "" {
		t.Fatal("ambiguous event acquired a provider scope")
	}
}

func TestPersonalFinalTranscriptionOverridesPartialLiveCaption(t *testing.T) {
	live := &fakeLive{events: make(chan openailive.Event, 2), signal: make(chan int64, 16)}
	turns := make(chan Turn, 2)
	l := newLane(t.Context(), Config{Turn: func(_ context.Context, turn Turn) { turns <- turn }},
		provider{
			open: func(context.Context, string, string) (liveSession, error) { return live, nil },
			transcribe: func(_ context.Context, _ string, pcm []byte, _ string) (string, error) {
				if len(pcm) < 8*frameBytes || !hasSignal(pcm) {
					return "", errors.New("isolated utterance audio missing")
				}
				return "Check the service, do not restart it.", nil
			},
		})
	t.Cleanup(func() { l.Close(); await(t, l.Done()) })
	await(t, l.Ready())
	for range 10 {
		if err := l.Push("owner", playbackTestFrame()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(framePeriod)
	}
	offset := await(t, live.signal)
	live.events <- openailive.Event{Type: "session.input_transcript.delta", EventID: "caption", StartMS: offset,
		EndMS: offset + 180, Text: "Check the service."}
	live.events <- openailive.Event{Type: "session.delegation.created", EventID: "delegation", OffsetMS: offset + 160,
		DelegationID: "live-item"}
	turn := await(t, turns)
	if turn.ID == "" || turn.Speaker != "owner" || turn.Text != "Check the service, do not restart it." {
		t.Fatal("native turn did not preserve the authoritative qualifier")
	}
}

func TestClippedUtteranceNeverQueuesNativeInput(t *testing.T) {
	l := &Lane{turns: make(chan utterance, 1), scopes: make(map[string]turnScope)}
	c := &candidate{
		turn:    Turn{Speaker: "owner", ID: "clipped"},
		invalid: true, voicedFrames: maxSamples,
		pcm: playbackTestFrame(), delegationID: "live",
	}
	tr := transcript{current: c}
	tr.closeCurrent(l)
	if len(l.turns) != 0 || len(l.scopes) != 0 {
		t.Fatal("clipped utterance acquired native admission or commentary scope")
	}
}
