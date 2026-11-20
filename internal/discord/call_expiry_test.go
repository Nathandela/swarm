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
	f.native.mu.Lock()
	f.audio(3)
	select {
	case <-speech.ctx.Done():
	case <-f.ctx.Done():
		f.native.mu.Unlock()
		t.Fatal("a blocked manager kept expired media authorized")
	}
	err := speech.cfg.Play(f.ctx, make([]byte, callMediaPCMBytes))
	f.native.mu.Unlock()
	if !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("expired participant heard new media: %v", err)
	}
	f.expectRoom("")
	select {
	case <-f.native.accepted:
		t.Fatal("expired voice request reached native admission")
	default:
	}
}
