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

// TestUpdate_ConnectionLost_SetsBannerAndPersistentState drives connectionLostMsg
// through Update and asserts both the immediate transient banner AND the
// persistent connectionLost state are set. The persistent state is what survives
// past bannerDuration (see TestConnectionLost_PersistsInStatusBarAndHaltsRepaintTick)
// — a transient banner alone would fade while the roster stays frozen, looking
// like false liveness. This does not invoke the returned command: setBanner's cmd
// is a real bannerDuration (4s) tea.Tick, and calling it would block the test
// wall-clock for no assertion value (waitForEvent is not called from this case at
// all — checked by inspection, not execution).
func TestUpdate_ConnectionLost_SetsBannerAndPersistentState(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "claude", "~/Code/x", "building", time.Minute))
	m := newModel(t, f, detectMixed())

	m2, _ := m.Update(connectionLostMsg{})

	if !m2.(rootModel).connectionLost {
		t.Fatal("connectionLostMsg must set the persistent connectionLost state")
	}
	v := view(m2)
	if !strings.Contains(v, "connection to daemon lost") {
		t.Fatalf("connectionLostMsg must set the transient banner; view:\n%s", v)
	}
}

// TestConnectionLost_PersistsInStatusBarAndHaltsRepaintTick proves the persistent
// indicator shows in generalStatus (so it survives past the transient banner's
// bannerDuration) and that the elapsed-time repaint tick is not re-armed once the
// connection is lost — an honest freeze instead of the elapsed column advancing
// over a roster that will never update again.
func TestConnectionLost_PersistsInStatusBarAndHaltsRepaintTick(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "claude", "~/Code/x", "building", time.Minute))
	m := newModel(t, f, detectMixed())

	m, _ = m.Update(connectionLostMsg{})

	status := m.(rootModel).generalStatus()
	if !strings.Contains(status, "daemon connection lost") {
		t.Fatalf("generalStatus must persistently show the connection-lost indicator, got %q", status)
	}

	_, cmd := m.Update(repaintMsg{})
	if cmd != nil {
		t.Fatal("repaintMsg must not re-arm the timer once the connection is lost (false liveness)")
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
