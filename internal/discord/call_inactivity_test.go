package discord

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/voice/callspeech"
)

func TestSilentMediaPreservesCallInactivity(t *testing.T) {
	for _, route := range []string{"personal", "team"} {
		t.Run(route, func(t *testing.T) {
			f := newDispatcherCallFixture(t, route == "team")
			speech, first := f.begin()
			if f.team {
				f.command("105", "30", "71", "consent", 0)
				f.command("106", "30", "70", "join", 0)
				if len(f.calls.wake) != 0 {
					t.Error("a command during an active call queued a replacement")
				}
			}
			inactiveSince := time.Now().Add(-time.Minute)
			f.calls.mu.Lock()
			f.calls.active.lastSpeech = inactiveSince
			f.calls.mu.Unlock()
			f.mediaAudio <- callMediaEvent{Type: "audio", UserID: f.marker.Author.ID, PCM: make([]byte, callMediaPCMBytes)}
			callFixtureReceive(t, f.ctx, f.native.accepted, "fixture silence processed")
			f.calls.mu.Lock()
			lastSpeech := f.calls.active.lastSpeech
			f.calls.mu.Unlock()
			if !lastSpeech.Equal(inactiveSince) {
				t.Errorf("silent frame reset AFK age from 60s to %s", time.Since(lastSpeech))
			}
			f.audio(2)
			callFixtureReceive(t, f.ctx, f.native.accepted, "speech after silence")
			f.calls.mu.Lock()
			voicedAt := f.calls.active.lastSpeech
			f.calls.mu.Unlock()
			if !voicedAt.After(lastSpeech) {
				t.Fatal("voiced audio did not extend activity")
			}

			f.native.mu.Lock()
			unlock := sync.OnceFunc(f.native.mu.Unlock)
			defer unlock()
			f.audio(3)
			op := callAdmissionID(first.Discussion.InstallationID, first.Voice.CallID, digest([]string{first.Voice.CallID, "3"}))
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			for {
				_, err := f.connector.callAdmission(first.Discussion, op)
				if err == nil {
					break
				}
				if !errors.Is(err, ErrNotFound) {
					t.Fatal(err)
				}
				callFixtureReceive(t, f.ctx, tick.C, "native admission blocked")
			}
			// Advance only the generation's inactivity clock and deliver its scheduled wakeup.
			f.calls.mu.Lock()
			f.calls.active.lastSpeech = time.Now().Add(-callIdleTimeout - 10*time.Millisecond)
			f.calls.active.idleTimer.Reset(0)
			f.calls.mu.Unlock()
			callFixtureReceive(t, f.ctx, speech.ctx.Done(), "idle authority revoked during admission")
			if err := speech.cfg.Play(f.ctx, make([]byte, callMediaPCMBytes)); !errors.Is(err, ErrNotAuthorized) {
				t.Fatalf("expired media accepted playback: %v", err)
			}
			unlock()
			f.expectRoom("")
			f.send("VOICE_STATE_UPDATE", json.RawMessage(callRosterDelta(callRosterVoice("99", ""))))
			if f.team {
				f.command("107", "30", "71", "join", 20*time.Second)
				f.expectWaiting()
				f.command("108", "30", "70", "join", 0)
			} else {
				f.send("VOICE_STATE_UPDATE", json.RawMessage(callRosterDelta(callRosterVoice("70", ""))))
				f.send("VOICE_STATE_UPDATE", json.RawMessage(callRosterDelta(callRosterVoice("70", "20"))))
			}
			f.completeJoinHandshake()
			callFixtureReceive(t, f.ctx, f.ack, "resumed after idle")
			f.audio(4)
			resumed := callFixtureReceive(t, f.ctx, f.native.accepted, "resumed turn")
			if resumed.Voice.Generation <= first.Voice.Generation || resumed.Text != "read the discussion marker 4" {
				t.Fatal("blocked admission leaked across AFK resumption")
			}
			if f.team {
				f.command("109", "30", "70", "leave", 0)
				f.expectConsent()
			} else {
				f.calls.stopCall()
			}
			f.expectRoom("")
		})
	}
}

// Delivery can precede the idle timer callback even when the author sent the command after expiry.
func TestFreshTeamJoinSurvivesRetiringIdleGeneration(t *testing.T) {
	f := newDispatcherCallFixture(t, true)
	speech, first := f.begin()
	f.calls.mu.Lock()
	f.calls.active.lastSpeech = time.Now().Add(-callIdleTimeout - time.Second)
	f.calls.mu.Unlock()
	f.command("105", "30", "70", "join", 0)
	callFixtureReceive(t, f.ctx, speech.ctx.Done(), "command observed the elapsed idle deadline")
	f.expectRoom("")
	f.send("VOICE_STATE_UPDATE", json.RawMessage(callRosterDelta(callRosterVoice("99", ""))))
	f.completeJoinHandshake()
	callFixtureReceive(t, f.ctx, f.ack, "fresh join survived the previous call cleanup")
	f.audio(2)
	second := callFixtureReceive(t, f.ctx, f.native.accepted, "fresh generation accepted one turn")
	if second.Voice.Generation != first.Voice.Generation+1 {
		t.Fatalf("new generation %d after %d", second.Voice.Generation, first.Voice.Generation)
	}
}

func TestExpiredCallRejectsWorkBeforeIdleTimerRuns(t *testing.T) {
	for _, operation := range []string{"audio", "turn", "play", "admission"} {
		t.Run(operation, func(t *testing.T) {
			f := newDispatcherCallFixture(t, false)
			speech, first := f.begin()
			expired := time.Now().Add(-callIdleTimeout - time.Second)
			f.calls.mu.Lock()
			c := f.calls.active
			c.lastSpeech = expired
			frames := c.metrics.InputFrames
			f.calls.mu.Unlock()
			switch operation {
			case "audio":
				f.audio(3)
			case "turn":
				speech.cfg.Turn(f.ctx, callspeech.Turn{ID: "late", Speaker: "70", Text: "late turn"})
			case "play":
				if err := speech.cfg.Play(f.ctx, make([]byte, callMediaPCMBytes)); !errors.Is(err, ErrNotAuthorized) {
					t.Fatalf("late playback returned %v", err)
				}
			case "admission":
				if err := f.calls.verify(f.ctx, first.Discussion, first.Voice, true); !errors.Is(err, ErrNotAuthorized) {
					t.Fatalf("late admission check returned %v", err)
				}
			}
			select {
			case <-speech.ctx.Done():
			case <-f.native.accepted:
				t.Fatal("work crossed the effective idle deadline")
			case <-f.ctx.Done():
				t.Fatal("expired generation retained media authority")
			}
			f.expectRoom("")
			f.calls.mu.Lock()
			defer f.calls.mu.Unlock()
			if c.metrics.InputFrames != frames || !c.lastSpeech.Equal(expired) {
				t.Fatal("an expired call consumed audio or extended its activity")
			}
		})
	}
}
