package skeleton

// T2.1.b (agents-tracker-vyd, R2.1.1) — the committee's L1 phantom-test discovery:
// no existing test binds gridPoll to end-to-end delivery latency
// (tui/liveness_test.go's cadence test is client-half only). This file is that
// binding, plus the interval pin (mirrors internal/tui/repaint_test.go's
// TestRepaint_IntervalIsOneSecond).
//
// TestTapLatency_GridChangeReachesSubscriberWithin1s drives the REAL tap path
// (gridPoll ticker -> tapOnce -> sampleGridAsync -> sampleGrid -> a real shim
// snapshot via the non-subscribing snapshot_req (C3; attach-based against an
// old shim) -> engine.OnOutput -> emit -> protocol fan-out) against a real
// assembled daemon and a real shim session, and measures from the moment the
// session's grid actually changes to the moment a real subscriber receives the
// resulting status event.
//
// The grid-change moment is observed by polling the session's ON-DISK transcript
// file directly (no attach): attaching would mark the session controlled and the
// tap SKIPS a controlled session (R1.3.7), which would defeat the point of timing
// the tap itself. The transcript is written by the same PTY-drain iteration that
// feeds the vt emulator (S9 non-blocking append), so its on-disk arrival is a
// tight, independent proxy for "the grid now reflects this content" — the same
// file captureConversationID itself reads.
import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

// TestGridPoll_IsFiveHundredMillis pins gridPoll at 500ms (R2.1.1: a 2.5x cut
// from 200ms). The committee REJECTED 1s: L1 is change->delivery < 1s, so a 1s
// poll cadence would leave zero headroom for fan-out itself.
func TestGridPoll_IsFiveHundredMillis(t *testing.T) {
	if gridPoll != 500*time.Millisecond {
		t.Fatalf("gridPoll = %s, want 500ms (R2.1.1)", gridPoll)
	}
}

func TestTapLatency_GridChangeReachesSubscriberWithin1s(t *testing.T) {
	sk := assemble(t)
	// The agent prints prose (ambiguous -> unknown, same as the launch baseline,
	// so no event fires yet), then settles behind a prompt sentinel with no
	// trailing newline (cursor parked on it) — a real grid CHANGE the heuristic
	// reads as a conclusive idle/none, mid-run rather than present from sample one.
	m := launchFake(t, sk, "print booting\nidle 150ms\nask >\n")

	c := dialClient(t, sk, "subscribe")
	events, err := c.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	transcriptPath := filepath.Join(sk.stateDir, m.ID, "transcript.log")

	deadline := time.Now().Add(5 * time.Second)
	var changedAt time.Time
	for {
		b, _ := os.ReadFile(transcriptPath)
		if bytes.Contains(b, []byte(">")) {
			changedAt = time.Now()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("transcript never showed the scripted prompt within 5s")
		}
		time.Sleep(2 * time.Millisecond)
	}

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("subscribe stream closed before the status event arrived")
			}
			if ev.Session.Status.Turn == status.TurnIdle && ev.Session.Status.Interaction == status.InteractionNone {
				latency := time.Since(changedAt)
				t.Logf("grid-change -> subscriber latency = %s (gridPoll=%s, budget < 1s)", latency, gridPoll)
				if latency >= time.Second {
					t.Fatalf("grid-change -> subscriber latency = %s, want < 1s (L1, R2.1.1)", latency)
				}
				return
			}
		case <-time.After(3 * time.Second):
			t.Fatal("no idle status event observed on the subscriber within 3s of the grid change")
		}
	}
}
