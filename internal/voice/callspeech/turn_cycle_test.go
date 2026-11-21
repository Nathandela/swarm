package callspeech

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"testing"
	"time"
)

// Replay at the media clock. Fixture signatures stand in for recognition so
// callback timings exclude network, provider inference, and device playback.
func TestLocalFixtureTurnCycle(t *testing.T) {
	if os.Getenv("SWARM_TEST_LOCAL_DISPATCHER_TIMING") != "1" {
		t.Skip("set SWARM_TEST_LOCAL_DISPATCHER_TIMING=1 for wall-clock fixture replay")
	}
	for _, name := range []string{"personal", "team"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			team := name == "team"
			fixture := func(name string) []byte {
				t.Helper()
				f, err := os.Open("testdata/" + name + ".pcm.gz")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = f.Close() })
				z, err := gzip.NewReader(f)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = z.Close() })
				pcm, err := io.ReadAll(z)
				if err != nil {
					t.Fatal(err)
				}
				return pcm
			}
			input, chatter := fixture(name), fixture("chatter")
			type admission struct {
				Turn
				at time.Time
			}
			turns := make(chan admission, 8)
			stops, played := make(chan time.Time, 16), make(chan time.Time, 16)
			failed := make(chan error, 1)
			l := newLane(t.Context(), Config{Team: team,
				Turn: func(_ context.Context, u Turn) { turns <- admission{u, time.Now()} },
				Play: func(ctx context.Context, _ []byte) error {
					played <- time.Now()
					<-ctx.Done()
					return ctx.Err()
				},
				Stop:   func() { stops <- time.Now() },
				Failed: func(err error) { failed <- err },
			}, provider{
				open:   func(context.Context, string, string) (liveSession, error) { return &fakeLive{}, nil },
				render: func(context.Context, string, string, string) ([]byte, error) { return playbackTestFrame(), nil },
				transcribe: func(_ context.Context, _ string, pcm []byte, _ string) (string, error) {
					switch {
					case bytes.Contains(pcm, input[10*frameBytes:15*frameBytes]):
						return "Dispatcher, blue notebook.", nil
					case bytes.Contains(pcm, chatter[10*frameBytes:15*frameBytes]):
						return "Team chatter.", nil
					default:
						return "", fmt.Errorf("fixture signature missing")
					}
				},
			})
			t.Cleanup(func() { l.Close(); await(t, l.Done()) })
			await(t, l.Ready())
			l.registerTurn("prior")
			if err := l.PublishForTurn("prior", "prior answer"); err != nil {
				t.Fatal(err)
			}
			await(t, played)
			push := func(who string, pcm []byte) {
				t.Helper()
				if err := l.Push(who, pcm); err != nil {
					t.Fatal(err)
				}
			}
			replay := func(who string, pcm []byte, overlap bool) time.Time {
				t.Helper()
				ticker := time.NewTicker(framePeriod)
				defer ticker.Stop()
				var first time.Time
				for len(pcm) > 0 {
					var frame [frameBytes]byte
					n := copy(frame[:], pcm)
					if first.IsZero() && HasSignal(frame[:]) {
						first = time.Now()
					}
					push(who, frame[:])
					if overlap && HasSignal(frame[:]) {
						push("interloper", frame[:])
					}
					pcm = pcm[n:]
					<-ticker.C
				}
				return first
			}
			if team {
				replay("alice", chatter, false)
				time.Sleep(quietTime)
				replay("alice", input, true)
				time.Sleep(quietTime)
				select {
				case <-stops:
					t.Fatal("chatter or overlap interrupted")
				case <-turns:
					t.Fatal("chatter or overlap admitted")
				default:
				}
			}
			seen := map[string]bool{}
			for i := range 2 {
				speaker := "owner"
				if team {
					speaker = []string{"alice", "bob"}[i]
				}
				first := replay(speaker, input, false)
				var u admission
				select {
				case u = <-turns:
				case err := <-failed:
					t.Fatal(err)
				case <-time.After(3 * time.Second):
					t.Fatal("missing admission")
				}
				admit := u.at
				if u.Speaker != speaker || u.ID == "" || seen[u.ID] {
					t.Fatalf("duplicate or misattributed admission for %s", speaker)
				}
				seen[u.ID] = true
				stop := await(t, stops)
				if err := l.PublishForTurn(u.ID, "next answer"); err != nil {
					t.Fatal(err)
				}
				start := await(t, played)
				t.Logf("speaker=%s last_voice_to_admission_ms=%d last_voice_to_play_ms=%d first_voice_to_stop_ms=%d", speaker, admit.Sub(u.LastVoicedAt).Milliseconds(), start.Sub(u.LastVoicedAt).Milliseconds(), stop.Sub(first).Milliseconds())
			}
		})
	}
}

func TestPersonalInterruptionPulse(t *testing.T) {
	for _, ms := range []int{20, 40, 80, 240} {
		for _, offset := range []int{0, 10} {
			t.Run(fmt.Sprintf("%dms_offset_%dms", ms, offset), func(t *testing.T) {
				t.Parallel()
				started := make(chan struct{})
				canceled := make(chan time.Time, 1)
				l := newLane(t.Context(), Config{Play: func(ctx context.Context, _ []byte) error {
					close(started)
					<-ctx.Done()
					canceled <- time.Now()
					return ctx.Err()
				}}, provider{
					open:       func(context.Context, string, string) (liveSession, error) { return &fakeLive{}, nil },
					render:     func(context.Context, string, string, string) ([]byte, error) { return playbackTestFrame(), nil },
					transcribe: func(context.Context, string, []byte, string) (string, error) { return "", nil },
				})
				t.Cleanup(func() { l.Close(); await(t, l.Done()) })
				await(t, l.Ready())
				l.registerTurn("prior")
				if err := l.PublishForTurn("prior", "prior answer"); err != nil {
					t.Fatal(err)
				}
				await(t, started)
				pcm := make([]byte, ((offset+ms+19)/20)*frameBytes)
				for i := offset * 48; i < (offset+ms)*48; i += 2 {
					pcm[i+1] = 6
				}
				start := time.Now().Add(time.Duration(offset) * time.Millisecond)
				ticker := time.NewTicker(framePeriod)
				defer ticker.Stop()
				for len(pcm) > 0 {
					if err := l.Push("owner", pcm[:frameBytes]); err != nil {
						t.Fatal(err)
					}
					pcm = pcm[frameBytes:]
					<-ticker.C
				}
				wantCancel := ms > 20
				select {
				case at := <-canceled:
					if !wantCancel {
						t.Error("short impulse interrupted playback")
					}
					t.Logf("canceled=true delay_ms=%d", at.Sub(start).Milliseconds())
				case <-time.After(4 * framePeriod):
					if wantCancel {
						t.Error("sustained signal did not interrupt playback")
					}
					t.Log("canceled=false")
				}
			})
		}
	}
}

// Alice's address check finishes while one or two other people are speaking.
// Queue a verified result while that second utterance is still open.
func TestTeamPendingPublicationYields(t *testing.T) {
	for _, overlap := range []bool{false, true} {
		t.Run(fmt.Sprintf("overlap_%t", overlap), func(t *testing.T) {
			t.Parallel()
			listening := make(chan struct{})
			release := make(chan struct{})
			played := make(chan time.Time, 1)
			stopped := make(chan struct{}, 1)
			first := true
			l := newLane(t.Context(), Config{
				Team: true,
				Stop: func() { stopped <- struct{}{} },
				Play: func(context.Context, []byte) error { played <- time.Now(); return nil },
			}, provider{
				open: func(context.Context, string, string) (liveSession, error) { return &fakeLive{}, nil },
				transcribe: func(ctx context.Context, _ string, _ []byte, _ string) (string, error) {
					if !first {
						return "unaddressed chatter", nil
					}
					first = false
					close(listening)
					select {
					case <-ctx.Done():
						return "", ctx.Err()
					case <-release:
						return "Dispatcher, confirm.", nil
					}
				},
				render: func(context.Context, string, string, string) ([]byte, error) { return []byte{0, 1}, nil },
			})
			t.Cleanup(func() { l.Close(); await(t, l.Done()) })
			await(t, l.Ready())
			pushTestSpeech(t, l, "alice")
			await(t, listening)
			l.registerTurn("earlier")
			pushTestSpeech(t, l, "bob")
			if err := l.PublishForTurn("earlier", "Earlier result."); err != nil {
				t.Fatal(err)
			}
			if overlap {
				pushTestSpeech(t, l, "carol")
			}
			close(release)
			await(t, stopped)
			last := time.Now()
			if err := l.Push("bob", playbackTestFrame()); err != nil {
				t.Fatal(err)
			}
			delay := await(t, played).Sub(last)
			t.Logf("last_input_to_first_pcm_ms=%d", delay.Milliseconds())
			if delay < quietTime {
				t.Error("queued result started before the room was quiet")
			}
		})
	}
}

func TestLateTeamRecognitionPreservesNewPlayback(t *testing.T) {
	for _, text := range []string{"Dispatcher, check the build.", "Unaddressed chatter."} {
		t.Run(text, func(t *testing.T) {
			t.Parallel()
			recognizing, release := make(chan struct{}), make(chan struct{})
			playing, canceled := make(chan struct{}), make(chan struct{})
			admitted, stopped := make(chan struct{}, 1), make(chan struct{}, 1)
			l := newLane(t.Context(), Config{
				Team: true,
				Turn: func(context.Context, Turn) { admitted <- struct{}{} },
				Stop: func() { stopped <- struct{}{} },
				Play: func(ctx context.Context, _ []byte) error {
					close(playing)
					<-ctx.Done()
					close(canceled)
					return ctx.Err()
				},
			}, provider{
				open: func(context.Context, string, string) (liveSession, error) { return &fakeLive{}, nil },
				transcribe: func(ctx context.Context, _ string, _ []byte, _ string) (string, error) {
					close(recognizing)
					select {
					case <-release:
						return text, nil
					case <-ctx.Done():
						return "", ctx.Err()
					}
				},
				render: func(context.Context, string, string, string) ([]byte, error) { return playbackTestFrame(), nil },
			})
			t.Cleanup(func() { l.Close(); await(t, l.Done()) })
			await(t, l.Ready())
			pushTestSpeech(t, l, "alice")
			l.registerTurn("earlier")
			if err := l.PublishForTurn("earlier", "Newly available result."); err != nil {
				t.Fatal(err)
			}
			await(t, recognizing)
			select {
			case <-playing:
			case <-time.After(time.Second):
				t.Fatal("stalled recognition held playback after the room went quiet")
			}
			close(release)
			if addressedText(text) != "" {
				await(t, admitted)
			}
			select {
			case <-admitted:
				t.Fatal("unexpected additional or unaddressed admission")
			case <-stopped:
				t.Fatal("earlier speech cleared the media bridge after a newer result started")
			case <-canceled:
				t.Fatal("earlier speech canceled a newer result")
			case <-time.After(4 * framePeriod):
			}
		})
	}
}

func TestOnsetBudgetAcrossSampleOffsets(t *testing.T) {
	for offset := 0; offset < frameBytes/2; offset++ {
		for _, samples := range []int{480, 1920, 5760} {
			occupied, first, stopped := 0, -1, -1
			for start := 0; start < offset+samples; start += frameBytes / 2 {
				var frame [frameBytes]byte
				for i := 0; i < len(frame)/2; i++ {
					if start+i >= offset && start+i < offset+samples {
						frame[2*i+1] = 4
					}
				}
				active := signalSamples(frame[:])
				if active == 0 {
					occupied = 0
					continue
				}
				if first < 0 {
					first = start
				}
				occupied += active
				if occupied > frameBytes/2 {
					stopped = start
					break
				}
			}
			if samples == 480 && stopped >= 0 || samples > 480 && (stopped < 0 || stopped-first > frameBytes/2) {
				t.Fatalf("offset=%d samples=%d first=%d stop=%d", offset, samples, first, stopped)
			}
		}
	}
}
