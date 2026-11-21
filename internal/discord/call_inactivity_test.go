package discord

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestSilentMediaPreservesCallInactivity(t *testing.T) {
	for _, route := range []string{"personal", "team"} {
		t.Run(route, func(t *testing.T) {
			f := newDispatcherCallFixture(t, route == "team")
			speech, first := f.begin()
			inactiveSince := time.Now().Add(-3 * time.Minute)
			f.calls.mu.Lock()
			f.calls.active.lastSpeech = inactiveSince
			f.calls.mu.Unlock()
			f.mediaAudio <- callMediaEvent{Type: "audio", UserID: f.marker.Author.ID, PCM: make([]byte, callMediaPCMBytes)}
			callFixtureReceive(t, f.ctx, f.native.accepted, "fixture silence processed")
			f.calls.mu.Lock()
			lastSpeech := f.calls.active.lastSpeech
			f.calls.mu.Unlock()
			if !lastSpeech.Equal(inactiveSince) {
				t.Errorf("silent frame reset AFK age from 180s to %s", time.Since(lastSpeech))
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
			f.calls.active.lastSpeech = inactiveSince
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
				f.command("105", "30", "70", "join", 0)
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
				f.command("106", "30", "70", "leave", 0)
				f.expectConsent()
			} else {
				f.calls.stopCall()
			}
			f.expectRoom("")
		})
	}
}
