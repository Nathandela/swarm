package attach

import (
	"bytes"
	"os"
	"syscall"
	"testing"
	"time"
)

// runInBackground starts Run in a goroutine and returns a channel yielding its
// result, so a test can drive the fake seams then assert the return.
type runResult struct {
	reason Reason
	err    error
}

func runInBackground(cfg Config) <-chan runResult {
	ch := make(chan runResult, 1)
	go func() {
		r, err := Run(cfg)
		ch <- runResult{r, err}
	}()
	return ch
}

func waitResult(t *testing.T, ch <-chan runResult) runResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("attach.Run did not return within 3s")
		return runResult{}
	}
}

// eventually polls cond until true or the deadline, so tests observe the
// passthrough's concurrent pumps without sleeping on a fixed timer.
func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}

// E8.1 / A-1 / A-4 / S10 — on attach the terminal is put in raw mode, then the
// snapshot is painted EXACTLY ONCE before any live frame reaches the terminal.
func TestPassthrough_SnapshotPaintedBeforeLiveFrames(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(mustSnap(t, "SNAPSHOT-GRID"))
	ch := runInBackground(Config{Term: term, Session: sess})

	// The snapshot must be on the terminal before any frame is sent.
	eventually(t, func() bool { return bytes.Contains(term.outBytes(), []byte("SNAPSHOT-GRID")) })

	sess.pushFrame([]byte("LIVE-FRAME-1"))
	eventually(t, func() bool { return bytes.Contains(term.outBytes(), []byte("LIVE-FRAME-1")) })

	out := term.outBytes()
	snapAt := bytes.Index(out, []byte("SNAPSHOT-GRID"))
	frameAt := bytes.Index(out, []byte("LIVE-FRAME-1"))
	if snapAt < 0 || frameAt < 0 || snapAt >= frameAt {
		t.Fatalf("snapshot must precede the first live frame (S10): snapAt=%d frameAt=%d", snapAt, frameAt)
	}
	if n := bytes.Count(out, []byte("SNAPSHOT-GRID")); n != 1 {
		t.Fatalf("snapshot painted %d times, want exactly one (S10)", n)
	}

	// MakeRaw must have happened before the first Out write (raw-then-paint).
	if _, _, rawFirst := term.counts(); !rawFirst {
		t.Fatal("terminal must be put in raw mode before painting (A-1)")
	}

	sess.endSession()
	_ = waitResult(t, ch)
}

// E8.1 / A-1 — keystrokes are forwarded to the session as PTY input.
func TestPassthrough_KeystrokesForwardedToSession(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession([]byte("S"))
	ch := runInBackground(Config{Term: term, Session: sess})

	term.feed([]byte("hello"))
	eventually(t, func() bool { return bytes.Equal(sess.inputBytes(), []byte("hello")) })

	sess.endSession()
	_ = waitResult(t, ch)
}

// E8.2 / A-2 — the detach key (default Ctrl+q = 0x11) detaches: Run returns
// ReasonDetached, the key is NOT forwarded to the session, and the terminal is
// restored.
func TestPassthrough_DetachKeyDetachesAndIsNotForwarded(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession([]byte("S"))
	ch := runInBackground(Config{Term: term, Session: sess})

	term.feed([]byte("ab"))
	eventually(t, func() bool { return bytes.Equal(sess.inputBytes(), []byte("ab")) })

	term.feed([]byte{DefaultDetachKey}) // Ctrl+q

	res := waitResult(t, ch)
	if res.reason != ReasonDetached {
		t.Fatalf("reason = %v, want ReasonDetached", res.reason)
	}
	if bytes.Contains(sess.inputBytes(), []byte{DefaultDetachKey}) {
		t.Fatal("the detach key must not be forwarded to the session (A-1/A-2)")
	}
	if _, restore, _ := term.counts(); restore == 0 {
		t.Fatal("termios must be restored on detach (E8.2)")
	}
	// Detach must release the lease upstream (P-4/L3).
	if sess.detachCalls == 0 {
		t.Fatal("Session.Detach must be called on detach so the lease/stream is released (P-4)")
	}
}

// E8.2 — a configurable detach key is honored (Ctrl+] here, 0x1d).
func TestPassthrough_ConfigurableDetachKey(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession([]byte("S"))
	ch := runInBackground(Config{Term: term, Session: sess, DetachKey: 0x1d})

	// The default key must now be forwarded as ordinary input, not treated as detach.
	term.feed([]byte{DefaultDetachKey})
	eventually(t, func() bool { return bytes.Contains(sess.inputBytes(), []byte{DefaultDetachKey}) })

	term.feed([]byte{0x1d}) // the configured detach key
	res := waitResult(t, ch)
	if res.reason != ReasonDetached {
		t.Fatalf("reason = %v, want ReasonDetached on the configured key", res.reason)
	}
}

// E8.3 / A-3 — a terminal resize propagates to the session PTY with the current
// terminal size (resize authority follows the lease; the Attachment stamps the
// generation).
func TestPassthrough_ResizePropagatesCurrentSize(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession([]byte("S"))
	ch := runInBackground(Config{Term: term, Session: sess})

	term.setSize(120, 40)
	eventually(t, func() bool {
		for _, r := range sess.resizeCalls() {
			if r == [2]int{120, 40} {
				return true
			}
		}
		return false
	})

	sess.endSession()
	_ = waitResult(t, ch)
}

// E8.4 / G3 — a completed/lost session renders READ-ONLY: the persisted final
// snapshot is painted, but keystrokes are NOT forwarded as input.
func TestPassthrough_ReadOnlyForwardsNoInput(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(mustSnap(t, "FINAL-SNAPSHOT"))
	ch := runInBackground(Config{Term: term, Session: sess, ReadOnly: true})

	eventually(t, func() bool { return bytes.Contains(term.outBytes(), []byte("FINAL-SNAPSHOT")) })

	term.feed([]byte("typing should be ignored"))
	// Give the loop time to (wrongly) forward, then assert nothing was forwarded.
	time.Sleep(100 * time.Millisecond)
	if len(sess.inputBytes()) != 0 {
		t.Fatalf("read-only attach forwarded input %q; G3 completed rows are read-only", sess.inputBytes())
	}

	// The detach key still detaches out of a read-only view.
	term.feed([]byte{DefaultDetachKey})
	res := waitResult(t, ch)
	if res.reason != ReasonDetached {
		t.Fatalf("reason = %v, want ReasonDetached", res.reason)
	}
}

// E8.1 — when the live stream closes (session ended / superseded), Run returns
// ReasonSessionEnd and restores the terminal.
func TestPassthrough_SessionEndReturnsAndRestores(t *testing.T) {
	term := newFakeTerm(80, 24)
	sess := newFakeSession(mustSnap(t, "S"))
	ch := runInBackground(Config{Term: term, Session: sess})

	eventually(t, func() bool { return bytes.Contains(term.outBytes(), []byte("S")) })
	sess.endSession()

	res := waitResult(t, ch)
	if res.reason != ReasonSessionEnd {
		t.Fatalf("reason = %v, want ReasonSessionEnd on stream close", res.reason)
	}
	if _, restore, _ := term.counts(); restore == 0 {
		t.Fatal("termios must be restored when the session ends")
	}
}

// E8.4 / A-5 — chrome is a single toggleable line. With Chrome the session name
// and detach hint appear; without it, no chrome line is painted.
func TestPassthrough_ChromeToggle(t *testing.T) {
	// Chrome on: name + detach hint present.
	term := newFakeTerm(80, 24)
	sess := newFakeSession(mustSnap(t, "GRID"))
	ch := runInBackground(Config{Term: term, Session: sess, Chrome: true, Name: "claude"})
	eventually(t, func() bool {
		out := term.outBytes()
		return bytes.Contains(out, []byte("claude")) && bytes.Contains(out, []byte("detach"))
	})
	sess.endSession()
	_ = waitResult(t, ch)

	// Chrome off: the name must not be painted as chrome (only the grid).
	term2 := newFakeTerm(80, 24)
	sess2 := newFakeSession(mustSnap(t, "GRID"))
	ch2 := runInBackground(Config{Term: term2, Session: sess2, Chrome: false, Name: "claude"})
	eventually(t, func() bool { return bytes.Contains(term2.outBytes(), []byte("GRID")) })
	time.Sleep(50 * time.Millisecond)
	if bytes.Contains(term2.outBytes(), []byte("detach")) {
		t.Fatal("chrome-off attach must not paint the detach-hint line (A-5)")
	}
	sess2.endSession()
	_ = waitResult(t, ch2)
}

// E8.2 — a panic inside the passthrough restores the terminal and surfaces as
// (ReasonError, err): a raw-mode fault returns the user to a sane terminal and the
// general view, never crashing the whole TUI with a wrecked terminal behind it.
func TestPassthrough_PanicRestoresTerminalAndReturnsError(t *testing.T) {
	term := newFakeTerm(80, 24)
	term.panicOnOut = true // the snapshot paint faults mid-write
	sess := newFakeSession(mustSnap(t, "SNAP"))

	reason, err := Run(Config{Term: term, Session: sess})

	if _, restore, _ := term.counts(); restore == 0 {
		t.Fatal("termios must be restored on a panic (E8.2)")
	}
	if reason != ReasonError || err == nil {
		t.Fatalf("a recovered panic must surface as (ReasonError, err); got (%v, %v)", reason, err)
	}
}

// E8.2 — a SIGINT/SIGTERM/SIGHUP delivered while attached restores the terminal
// before the loop tears down (unit-level: the injected signal channel drives it;
// the real termios restore under a hard kill is proven in pty_test.go).
func TestPassthrough_SignalRestoresTerminal(t *testing.T) {
	for _, sig := range []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		term := newFakeTerm(80, 24)
		sess := newFakeSession(mustSnap(t, "S"))
		ch := runInBackground(Config{Term: term, Session: sess})
		eventually(t, func() bool { return bytes.Contains(term.outBytes(), []byte("S")) })

		term.sigCh <- sig // injected termination signal

		res := waitResult(t, ch)
		if _, restore, _ := term.counts(); restore == 0 {
			t.Fatalf("termios must be restored on %v (E8.2); reason=%v", sig, res.reason)
		}
	}
}
