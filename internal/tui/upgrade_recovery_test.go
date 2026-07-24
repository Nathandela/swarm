package tui

// deployment-committee 3/3 (HIGH ship-blocker): a successful daemon auto-upgrade
// permanently tripped the connection-lost state. Init arms waitForEvent on the OLD
// daemon's subscribe stream; the restart kills that daemon, its read loop closes
// eventsCh, and the stale waitForEvent returns a connectionLostMsg. Nothing cleared
// m.connectionLost, so the board froze into a false "connection lost" after every
// successful auto-upgrade.
//
// Two layers, exercised in both orderings:
//   (a) stale-source guard: a connectionLostMsg is ignored when its from-channel is
//       no longer m.events (a pre-upgrade loss never poisons the fresh subscription).
//   (b) restart reset: daemonRestartedMsg's success branch clears connectionLost and
//       resumes the repaint tick (covers the loss landing BEFORE the restart finishes).

import (
	"strings"
	"testing"
	"time"
)

// (i) The stale loss lands BEFORE the restart completes: connectionLostMsg on the
// then-current channel sets the state, but the successful daemonRestartedMsg must
// clear it and let the repaint tick resume — no permanent false freeze.
func TestUpgrade_StaleLossBeforeRestart_ClearsOnSuccess(t *testing.T) {
	oldClient := newFakeClient(sWorking("endpoint/s1", "claude", "~/Code/x", "building", time.Minute))
	m := newModel(t, oldClient, detectMixed())
	oldCh := m.(rootModel).events

	// The old subscription's loss arrives while the upgrade is still in flight.
	m, _ = m.Update(connectionLostMsg{from: oldCh})
	// A repaint tick fires meanwhile and, seeing the loss, halts itself.
	m, _ = m.Update(repaintMsg{})

	// The restart completes successfully with a fresh daemon/client.
	m, _ = m.Update(daemonRestartedMsg{client: newFakeClient()})

	rm := m.(rootModel)
	if rm.connectionLost {
		t.Fatal("a successful restart must clear the connectionLost state")
	}
	if strings.Contains(rm.generalStatus(), "daemon connection lost") {
		t.Fatalf("status bar must not show connection-lost after a successful upgrade; got %q", rm.generalStatus())
	}
	// The elapsed-column tick must resume: the next repaintMsg re-arms it.
	_, cmd := rm.Update(repaintMsg{})
	if cmd == nil {
		t.Fatal("after a successful upgrade the repaint tick must resume (next repaintMsg re-arms)")
	}
}

// (ii) Reverse order: the restart completes first (swapping to a fresh subscription),
// THEN the old subscription's stale loss lands. The stale-source guard must drop it so
// the fresh board stays live.
func TestUpgrade_StaleLossAfterRestart_Ignored(t *testing.T) {
	oldClient := newFakeClient(sWorking("endpoint/s1", "claude", "~/Code/x", "building", time.Minute))
	m := newModel(t, oldClient, detectMixed())
	oldCh := m.(rootModel).events

	// The restart completes and swaps to a fresh event stream.
	m, _ = m.Update(daemonRestartedMsg{client: newFakeClient()})
	if m.(rootModel).events == oldCh {
		t.Fatal("test precondition: the restart must swap to a distinct event stream")
	}

	// The now-stale loss from the pre-upgrade subscription lands afterward.
	m, _ = m.Update(connectionLostMsg{from: oldCh})

	rm := m.(rootModel)
	if rm.connectionLost {
		t.Fatal("a stale loss from the pre-upgrade subscription must not freeze the fresh board")
	}
	if strings.Contains(rm.generalStatus(), "daemon connection lost") {
		t.Fatalf("board must stay live after a stale post-upgrade loss; got %q", rm.generalStatus())
	}
}
