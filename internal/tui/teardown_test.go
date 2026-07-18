package tui

// agents-tracker-1uq: waitForEvent's channel-closed path (Client.eventsCh closing
// on connection loss, protocol/client.go) must surface a "connection lost" banner
// instead of returning a bare nil tea.Msg, and must NOT be re-armed. Separately,
// attachDoneMsg.err was unconditionally discarded, so an attach failure never
// reached the user.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

// TestWaitForEvent_ChannelClosed_EmitsConnectionLostMsg proves the closed-channel
// path yields a distinct connectionLostMsg, not a bare nil.
func TestWaitForEvent_ChannelClosed_EmitsConnectionLostMsg(t *testing.T) {
	ch := make(chan protocol.Event)
	close(ch)

	cmd := waitForEvent(ch)
	if cmd == nil {
		t.Fatal("waitForEvent(closed channel) returned a nil command")
	}
	msg := cmd()
	if _, ok := msg.(connectionLostMsg); !ok {
		t.Fatalf("waitForEvent on a closed channel returned %#v, want connectionLostMsg", msg)
	}
}

// TestUpdate_ConnectionLost_SetsBannerAndDoesNotReArm drives connectionLostMsg
// through Update and asserts the banner is set and no waitForEvent command is
// returned (there is nothing left to wait on: the channel is closed forever).
func TestUpdate_ConnectionLost_SetsBannerAndDoesNotReArm(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "claude", "~/Code/x", "building", time.Minute))
	m := newModel(t, f, detectMixed())

	m2, cmd := m.Update(connectionLostMsg{})

	v := view(m2)
	if !strings.Contains(v, "connection to daemon lost") {
		t.Fatalf("connectionLostMsg must set a banner naming the lost connection; view:\n%s", v)
	}
	if cmd != nil {
		if msg := cmd(); msg != nil {
			if _, ok := msg.(eventMsg); ok {
				t.Fatal("connectionLostMsg must not re-arm waitForEvent")
			}
		}
	}
}

// TestUpdate_AttachDoneWithError_SetsBanner proves a non-nil attachDoneMsg.err
// reaches the user via the banner instead of being silently dropped.
func TestUpdate_AttachDoneWithError_SetsBanner(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "claude", "~/Code/x", "building", time.Minute))
	m := newModel(t, f, detectMixed())

	m2, _ := m.Update(attachDoneMsg{err: errors.New("dial: connection refused")})

	v := view(m2)
	if !strings.Contains(v, "attach failed") || !strings.Contains(v, "connection refused") {
		t.Fatalf("a non-nil attach error must surface on the banner; view:\n%s", v)
	}
}

// TestUpdate_AttachDoneWithoutError_NoBanner pins the existing nil-error path: no
// banner, straight back to the general board.
func TestUpdate_AttachDoneWithoutError_NoBanner(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "claude", "~/Code/x", "building", time.Minute))
	m := newModel(t, f, detectMixed())

	m2, _ := m.Update(attachDoneMsg{err: nil})

	v := view(m2)
	if strings.Contains(v, "attach failed") {
		t.Fatalf("a nil attach error must not raise a banner; view:\n%s", v)
	}
}
