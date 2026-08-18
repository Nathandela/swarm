package skeleton

// ADR-010 Amendment 3 slice 2, the ASSEMBLY proof: through the production wiring
// (serve.go: registerSession arms, emitStatus/endSession signal, the owner-tier Server's
// SendInput types), a passive handoff child's attention state ends up as ONE submitted
// message in the SOURCE session's PTY, the child's roster row carries the mode and the live
// pending flag, and a busy source is not interrupted until it is idle again.
//
// The source is a scripted fake blocked at an `ask` prompt: it reads one stdin line and
// echoes `got: <line>`, a string the session cannot produce on its own, so its presence on
// the screen is proof the notification was typed AND submitted. Its second `ask` catches
// the second delivery. The source's own status is left to the grid tap (its `>` prompt reads
// idle/none, the safe state), so the engine has committed idle before the test drives the
// source busy by hand and no later tap read can undo that. The child's status is driven with
// emitStatus (the engine's own emission seam), never by hand-editing meta.
//
// Real sockets and state dir under /tmp (sun_path cap, serve_test.go). Bounded well under
// 30s. Nothing is skipped: the assembly either delivers or this fails.

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// launchPassiveChild launches a scripted fake as a passive handoff child of source, on the
// core daemon exactly as launchFake does (the lineage fields are the only difference).
func launchPassiveChild(t *testing.T, sk *Daemon, source, script string) persist.Meta {
	t.Helper()
	m, err := sk.Core().Launch(daemon.LaunchSpec{
		AgentType:   "fake",
		Argv:        []string{fakeAgentBin, mustScript(t, script)},
		Cwd:         t.TempDir(),
		ClientEnv:   []string{"PATH=" + os.Getenv("PATH")},
		Cols:        80,
		Rows:        24,
		SpawnedFrom: source,
		SpawnIntent: protocol.SpawnIntentHandoff,
		Supervision: protocol.SupervisionPassive,
	})
	if err != nil {
		t.Fatalf("core Launch of the passive child: %v", err)
	}
	t.Cleanup(func() {
		if m.ShimPID > 0 {
			_ = syscall.Kill(m.ShimPID, syscall.SIGTERM)
		}
	})
	return m
}

// awaitView polls List until the row for id satisfies ok, or fails after within.
func awaitView(t *testing.T, c *protocol.Client, id, why string, within time.Duration, ok func(protocol.SessionView) bool) protocol.SessionView {
	t.Helper()
	deadline := time.Now().Add(within)
	var last protocol.SessionView
	for time.Now().Before(deadline) {
		views, err := c.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, v := range views {
			if v.ID == id {
				last = v
				if ok(v) {
					return v
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s: row %s never satisfied the condition within %s; last row %+v", why, id, within, last)
	return protocol.SessionView{}
}

// peekScreen returns the session's current screen as one string via the owner-tier peek (a
// read-only tap: it takes no controller lease, so it never blocks a delivery).
func peekScreen(t *testing.T, c *protocol.Client, id string) string {
	t.Helper()
	snap, err := c.TerminalSnapshot(id)
	if err != nil {
		t.Fatalf("TerminalSnapshot %s: %v", id, err)
	}
	return strings.Join(snap.Lines, "\n")
}

// awaitScreen polls the peek until ok(screen), or fails after within.
func awaitScreen(t *testing.T, c *protocol.Client, id, why string, within time.Duration, ok func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(within)
	last := ""
	for time.Now().Before(deadline) {
		last = peekScreen(t, c, id)
		if ok(last) {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s: the screen never satisfied the condition within %s; last screen:\n%s", why, within, last)
	return ""
}

const gotNotification = "got: [swarm supervision"

func TestSupervision_PassiveChildNotifiesSourceThroughAssembly(t *testing.T) {
	sk := assemble(t)
	source := launchFake(t, sk, "ask >\nask >\nidle 30s\n")
	child := launchPassiveChild(t, sk, source.ID, "idle 30s\n")

	c := dialClient(t, sk, "attach", "subscribe")
	childID := protocol.NamespacedID(c.EndpointID(), child.ID)
	sourceID := protocol.NamespacedID(c.EndpointID(), source.ID)

	// C5: the child's row carries the persisted mode; C4: the record lives under the state dir.
	v := awaitView(t, c, childID, "child listed", 10*time.Second, func(protocol.SessionView) bool { return true })
	if v.Supervision != protocol.SupervisionPassive {
		t.Fatalf("child row Supervision = %q, want %q", v.Supervision, protocol.SupervisionPassive)
	}
	if v.SupervisionPending {
		t.Fatal("child row SupervisionPending = true right after launch; nothing has happened yet")
	}
	recordFile := filepath.Join(sk.stateDir, "supervision", child.ID+".json")
	if _, err := os.Stat(recordFile); err != nil {
		t.Fatalf("registerSession did not arm the passive child: %v", err)
	}

	// The source settles at its prompt: the grid tap reads `>` as idle/none (C3's safe state).
	awaitView(t, c, sourceID, "source idle at its prompt", 10*time.Second, func(v protocol.SessionView) bool {
		return v.Status.Turn == status.TurnIdle && v.Status.Interaction != status.InteractionPermission
	})

	// Working, then idle: ready_for_review after working is an attention event, and the source
	// is safe, so the notification is typed and submitted into the source's PTY.
	sk.emitStatus(child.ID, stWorking)
	sk.emitStatus(child.ID, stReady)
	awaitScreen(t, c, sourceID, "first notification delivered", 8*time.Second, func(s string) bool {
		return strings.Count(s, gotNotification) == 1
	})
	awaitView(t, c, childID, "pending cleared after delivery", 5*time.Second, func(v protocol.SessionView) bool {
		return !v.SupervisionPending
	})

	// A busy source is not interrupted: the next attention event waits, visibly.
	sk.emitStatus(source.ID, stWorking)
	sk.emitStatus(child.ID, stWorking)
	sk.emitStatus(child.ID, stPrompt)
	awaitView(t, c, childID, "pending shown while the source is busy", 5*time.Second, func(v protocol.SessionView) bool {
		return v.SupervisionPending
	})
	time.Sleep(1500 * time.Millisecond) // longer than any sane retry cadence
	if s := peekScreen(t, c, sourceID); strings.Count(s, gotNotification) != 1 {
		t.Fatalf("a busy source received a second notification; screen:\n%s", s)
	}
	awaitView(t, c, childID, "still pending while the source is busy", time.Second, func(v protocol.SessionView) bool {
		return v.SupervisionPending
	})

	// The source turns idle: the queued event is delivered once, as seq 2.
	sk.emitStatus(source.ID, stReady)
	awaitScreen(t, c, sourceID, "second notification delivered once the source is idle", 8*time.Second, func(s string) bool {
		return strings.Count(s, gotNotification) == 2 && strings.Contains(s, gotNotification+" "+child.ID+"#2]")
	})
	awaitView(t, c, childID, "pending cleared after the second delivery", 5*time.Second, func(v protocol.SessionView) bool {
		return !v.SupervisionPending
	})
}
