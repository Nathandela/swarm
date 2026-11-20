package callspeech_test

import (
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/voice"
	"github.com/Nathandela/swarm/internal/voice/callspeech"
	"github.com/Nathandela/swarm/internal/voice/openailive"
)

const (
	hostedFrameBytes = 960
	hostedFrameTime  = 20 * time.Millisecond
)

// TestHostedDispatcherSpeech uses synthetic speech only. It explicitly opts
// into separate provider work from the existing one-session preview probe.
func TestHostedDispatcherSpeech(t *testing.T) {
	dir := os.Getenv("SWARM_TEST_HOSTED_DISPATCHER_CALL_DIR")
	if dir == "" {
		t.Skip("set SWARM_TEST_HOSTED_DISPATCHER_CALL_DIR to an approved local Swarm voice test settings directory")
	}
	key, err := voice.LoadOpenAIKey(dir)
	if err != nil {
		t.Fatal("approved test speech credential unavailable")
	}
	for _, test := range []struct {
		name string
		team bool
	}{
		{name: "personal"},
		{name: "team", team: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			positive := syntheticPCM(t, test.name)
			var chatter []byte
			if test.team {
				chatter = syntheticPCM(t, "chatter")
			}
			qualifyHostedLane(t, key, test.team, positive, chatter)
		})
	}
}

// Fixtures are offline eSpeak syntheses. No microphone, system voice, or paid service is used for input.
func syntheticPCM(t *testing.T, name string) []byte {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name+".pcm.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }()
	pcm, err := io.ReadAll(io.LimitReader(gz, 480001))
	if err != nil || len(pcm) == 0 || len(pcm) > 480000 || len(pcm)%2 != 0 {
		t.Fatalf("invalid %s synthetic speech", name)
	}
	return pcm
}

func qualifyHostedLane(t *testing.T, key string, team bool, positive, chatter []byte) {
	t.Helper()
	rounds := 1
	if raw := os.Getenv("SWARM_TEST_HOSTED_DISPATCHER_CALL_TURNS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 10 {
			t.Fatal("hosted turns must be 1 to 10")
		}
		rounds = n
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Duration(rounds)*45*time.Second+20*time.Second)
	defer cancel()
	turns := make(chan callspeech.Turn, 2)
	played := make(chan []byte, 512)
	failed := make(chan error, 1)
	sessionStart := time.Now()
	lane := callspeech.New(ctx, callspeech.Config{Key: key, Language: "en", Team: team,
		Turn: func(ctx context.Context, turn callspeech.Turn) {
			select {
			case turns <- turn:
			case <-ctx.Done():
			}
		},
		Play: func(ctx context.Context, pcm []byte) error {
			select {
			case played <- pcm:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		Failed: func(err error) { failed <- err },
	})
	defer func() { lane.Close(); <-lane.Done() }()
	select {
	case <-lane.Ready():
	case err := <-failed:
		t.Fatalf("provider startup failed: %v", err)
	case <-ctx.Done():
		t.Fatal("provider ready deadline")
	}
	startup := time.Since(sessionStart)

	if team {
		pushHostedPCM(t, ctx, lane, "synthetic-chatter", chatter)
		select {
		case <-turns:
			t.Fatal("unaddressed team speech admitted")
		case err := <-failed:
			t.Fatalf("team recognition failed: %v", err)
		case <-time.After(2 * time.Second):
		}
	}
	seen := make(map[string]bool, rounds)
	for round := 1; round <= rounds; round++ {
		pushHostedPCM(t, ctx, lane, "synthetic-owner", positive)
		lastInput := time.Now()
		var turn callspeech.Turn
		select {
		case turn = <-turns:
		case err := <-failed:
			t.Fatalf("speech provider failed: %v", err)
		case <-ctx.Done():
			t.Fatal("native input deadline")
		}
		admission := time.Since(lastInput)
		if turn.Speaker != "synthetic-owner" || turn.ID == "" || seen[turn.ID] || !strings.Contains(strings.ToLower(turn.Text), "blue notebook") {
			t.Fatal("synthetic native marker or attribution missing")
		}
		seen[turn.ID] = true
		const answer = "The orange lantern is ready."
		if err := lane.PublishForTurn(turn.ID, answer); err != nil {
			t.Fatal(err)
		}

		var pcm []byte
		var first time.Duration
		idle := time.NewTimer(25 * time.Second)
		defer idle.Stop()
	collect:
		for {
			select {
			case frame := <-played:
				if len(pcm) == 0 {
					first = time.Since(lastInput)
				}
				pcm = append(pcm, frame...)
				idle.Reset(600 * time.Millisecond)
			case err := <-failed:
				t.Fatalf("return speech failed: %v", err)
			case <-idle.C:
				break collect
			case <-ctx.Done():
				t.Fatal("return speech deadline")
			}
		}
		idle.Stop()
		liveElapsed := time.Since(sessionStart)

		if len(pcm) == 0 {
			t.Fatal("verified synthetic publication produced no PCM")
		}
		heard, err := openailive.Transcribe(ctx, key, pcm, "en")
		if err != nil {
			t.Fatalf("independent output check failed: %v", err)
		}
		matched := strings.Contains(strings.ToLower(heard), "orange lantern")
		t.Logf("round=%d startup_ms=%d last_input_to_admission_ms=%d last_input_to_playback_ms=%d live_session_wall_ms=%d input_pcm_ms=%d output_pcm_ms=%d output_marker=%t (duration is a usage proxy, billing unverified)",
			round, startup.Milliseconds(), admission.Milliseconds(), first.Milliseconds(), liveElapsed.Milliseconds(),
			(len(positive)+len(chatter))/48, len(pcm)/48, matched)
		if !matched {
			t.Fatalf("independent output transcription missed synthetic marker: %q", heard)
		}
		select {
		case <-turns:
			t.Fatal("duplicate native turn")
		default:
		}
	}
}

func pushHostedPCM(t *testing.T, ctx context.Context, lane *callspeech.Lane, speaker string, pcm []byte) {
	t.Helper()
	clock := time.NewTicker(hostedFrameTime)
	defer clock.Stop()
	for len(pcm) != 0 {
		var frame [hostedFrameBytes]byte
		n := copy(frame[:], pcm)
		if err := lane.Push(speaker, frame[:]); err != nil {
			t.Fatal(err)
		}
		pcm = pcm[n:]
		select {
		case <-ctx.Done():
			t.Fatal("input deadline")
		case <-clock.C:
		}
	}
}
