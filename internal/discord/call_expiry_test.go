package discord

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// The media deadline must be independent of the admission goroutine's progress.
func TestTeamConsentExpiresWhileManagerIsBlocked(t *testing.T) {
	f := newDispatcherCallFixture(t, true)
	f.message(f.marker)
	msg := callFixtureReceive(t, f.ctx, f.native.accepted, "written marker")
	f.waitDiscussionOpened(msg.Discussion)
	f.send("GUILD_CREATE", json.RawMessage(callRosterGuild(callRosterVoice("70", "20"), callRosterVoice("71", "20"))))
	f.command("101", "30", "70", "join", 0)
	expires := time.Now().Add(2 * time.Second)
	f.calls.mu.Lock()
	f.calls.consent["70"] = expires
	f.calls.mu.Unlock()
	f.command("102", "30", "71", "consent", 0)
	f.expectRoom("20")
	f.send("VOICE_STATE_UPDATE", json.RawMessage(callRosterDelta(callRosterVoice("99", "20"))))
	f.send("VOICE_SERVER_UPDATE", json.RawMessage(`{"guild_id":"10","token":"fixture-voice","endpoint":"example.invalid"}`))
	speech := callFixtureReceive(t, f.ctx, f.ack, "live session")
	if deadline, ok := speech.ctx.Deadline(); !ok || !deadline.Equal(expires) {
		t.Fatalf("wrong team media deadline: %v", deadline)
	}
	// An active participant cannot extend the original limiting deadline.
	f.command("103", "30", "70", "consent", 0)
	f.native.mu.Lock()
	f.audio(3)
	select {
	case <-speech.ctx.Done():
	case <-f.ctx.Done():
		f.native.mu.Unlock()
		t.Fatal("a blocked manager kept expired media authorized")
	}
	f.calls.mu.Lock()
	oldConsent := f.calls.consent["70"]
	f.calls.mu.Unlock()
	f.command("104", "30", "70", "join", 20*time.Second)
	f.calls.mu.Lock()
	if !f.calls.consent["70"].Equal(oldConsent) || time.Now().Before(f.calls.invitedUntil) {
		t.Error("pre-expiry join renewed consent or invitation during blocked teardown")
	}
	f.calls.mu.Unlock()
	err := speech.cfg.Play(f.ctx, make([]byte, callMediaPCMBytes))
	f.native.mu.Unlock()
	if !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("expired participant heard new media: %v", err)
	}
	f.expectRoom("")
	f.send("VOICE_STATE_UPDATE", json.RawMessage(callRosterDelta(callRosterVoice("99", ""))))
	f.command("105", "30", "71", "join", 20*time.Second)
	f.expectWaiting()
	f.command("106", "30", "70", "join", 0)
	f.expectRoom("20")
	select {
	case <-f.native.accepted:
		t.Fatal("expired voice request reached native admission")
	default:
	}
}

func TestAbruptMediaExitRevokesPendingNativeInput(t *testing.T) {
	f := newDispatcherCallFixture(t, false)
	speech, initial := f.begin()
	f.native.mu.Lock()
	defer f.native.mu.Unlock()
	f.audio(3)
	turnID := digest([]string{initial.Voice.CallID, "3"})
	op := callAdmissionID(initial.Discussion.InstallationID, initial.Voice.CallID, turnID)
	tick := time.NewTicker(time.Millisecond * 10)
	defer tick.Stop()
	for {
		_, err := f.connector.callAdmission(initial.Discussion, op)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
		callFixtureReceive(t, f.ctx, tick.C, "durable pending input reaching blocked native service")
	}
	close(f.mediaAudio)
	select {
	case <-speech.ctx.Done():
	case <-f.ctx.Done():
		t.Fatal("media helper exit did not immediately revoke pending native input")
	}
	origin := initial.Voice
	origin.TurnID = turnID
	if err := f.calls.verify(f.ctx, initial.Discussion, origin, true); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("ended call could admit queued native input: %v", err)
	}
}

func TestTeamConsentLifetimeStartsNoLaterThanReceipt(t *testing.T) {
	f := newDispatcherCallFixture(t, true)
	f.begin()
	for _, tc := range []struct {
		id   string
		skew time.Duration
	}{{"105", -15 * time.Second}, {"106", time.Second}} {
		msg := teammateTestMessage(tc.id, "30", "71", "<@99> voice consent", true)
		msg.GuildID = "10"
		receivedFrom := time.Now()
		msg.Timestamp = receivedFrom.Add(tc.skew)
		f.message(msg)
		receivedThrough := time.Now()
		f.calls.mu.Lock()
		start := f.calls.consent["71"].Add(-30 * time.Minute)
		f.calls.mu.Unlock()
		if tc.skew < 0 && !start.Equal(msg.Timestamp) || tc.skew > 0 && (start.Before(receivedFrom) || start.After(receivedThrough)) {
			t.Errorf("client timestamp skew %v yielded a grant starting %v after message issue", tc.skew, start.Sub(msg.Timestamp))
		}
	}
}
