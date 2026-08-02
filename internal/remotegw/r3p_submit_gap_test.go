package remotegw

// Bead agents-tracker-r3p -- FAILING-FIRST (TDD RED, GG-5) tests for the MACHINE half of
// the paste-submit defect (docs/verification/spike-SA.md finding #1).
//
// WHY THERE IS A MACHINE HALF AT ALL. The phone already refuses to put text and the submit
// that follows it in one frame (internal/phonecore, TestR3PCoalescer_*), and it spaces its
// own frames by a 125 ms window. Neither survives the relay. The relay is store-and-forward:
// the gateway's inbound wait returns a BATCH and processBatch walks it serially, so two
// frames the phone appended 125 ms apart are opened and routed microseconds apart here.
// From this point on nothing re-introduces a gap and nothing merges the frames either --
// the daemon's clientConn.handleDataIn forwards one wire frame per frame and the shim does
// one s.ptyIn.Write(payload) per frame, all on localhost. So the PTY sees two writes with
// no measurable gap between them, the CLI reads both in one tick, and its paste heuristic
// fires exactly as it did for the single-write case: the CR is inserted as a literal
// newline and the prompt is never submitted.
//
// This is therefore the LAST place a gap can be created, and the first place it survives
// to the PTY.
//
// WHY THE GATEWAY SPACES BUT NEVER SPLITS. A sealed input frame carries no
// keystroke-vs-paste marker -- InputFrame.T is "data" for both App.SendInput and App.Paste
// -- so a gateway that split payloads at their newlines would chop a legitimate multi-line
// paste into N separate submits. The split belongs to the phone, which knows which one it
// is; the gateway may only space a frame that is ALREADY nothing but submit bytes. Hence
// the second and third cases below, which are as load-bearing as the first: a blanket
// inter-frame sleep would pass the first assertion and pace every 4 KiB chunk of a large
// paste, which is a different way to break the same feature.
//
// WHY LeaseConn.WriteDataIn AND NOT THE SHIM. The shim is closer to the PTY and would cover
// the local attach lane too, but its ptyWriter is shared with the emulator's reply pump:
// sleeping while holding it stalls the DSR/CPR replies the CLI is blocking on, which is a
// hang rather than a latency cost.
//
// THE COST, stated rather than hidden: up to submitGap on a submit that closely follows
// text -- once per prompt, not once per keystroke -- and the same bound on whatever else
// shares that inbound batch, since processBatch is serial. §6.0's 150 ms p50 budget is a
// keystroke-echo budget and is not touched.
//
// This file contains NO implementation.

import (
	"net"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/wire"
)

const (
	// r3pWantGap is spike S-A's own measured value: the harness that made real Claude Code
	// submit reliably wrote the text, slept 150 ms, then wrote the CR. Nothing establishes
	// that a shorter gap is enough, so this is the number the fix owes.
	r3pWantGap = 150 * time.Millisecond

	// r3pSkew absorbs the race between the writer stamping its own send and the reader
	// returning from ReadFrame. It is not slack in the requirement: the assertions below
	// would still fail by two orders of magnitude on today's behaviour, which spaces
	// nothing at all.
	r3pSkew = 5 * time.Millisecond

	// r3pUnpacedMax is what "not delayed" means. Anything the gateway does NOT space must
	// leave well inside this, or a blanket sleep is passing as a targeted one.
	r3pUnpacedMax = 40 * time.Millisecond
)

// r3pArrival is one frame as the daemon side saw it, with the instant it was fully read.
type r3pArrival struct {
	payload string
	at      time.Time
}

// r3pLeaseConn returns a LeaseConn writing into an in-memory daemon connection, plus the
// channel on which that daemon side reports what it received and when. net.Pipe is
// synchronous and unbuffered, so an arrival instant IS the instant the gateway released the
// frame -- there is no kernel buffer here to hide a delay in or invent one.
func r3pLeaseConn(t *testing.T) (*LeaseConn, <-chan r3pArrival) {
	t.Helper()
	gwSide, daemonSide := net.Pipe()
	t.Cleanup(func() { gwSide.Close(); daemonSide.Close() })

	arrivals := make(chan r3pArrival, 8)
	go func() {
		defer close(arrivals)
		for {
			typ, payload, err := wire.ReadFrame(daemonSide)
			if err != nil {
				return
			}
			if typ != wire.TDataIn {
				continue
			}
			arrivals <- r3pArrival{payload: string(payload), at: time.Now()}
		}
	}()
	return &LeaseConn{dc: &daemonConn{conn: gwSide}, session: "m1/s1"}, arrivals
}

// r3pNext reads one arrival or fails the test.
func r3pNext(t *testing.T, arrivals <-chan r3pArrival) r3pArrival {
	t.Helper()
	select {
	case a, ok := <-arrivals:
		if !ok {
			t.Fatal("the daemon side closed before the frame arrived")
		}
		return a
	case <-time.After(2 * time.Second):
		t.Fatal("no frame reached the daemon side within 2s")
	}
	return r3pArrival{}
}

// TestR3PLeaseConn_ASubmitDoesNotLandInTheSameReadTickAsTheTextBeforeIt is the defect.
// Both frames are already separate -- the phone did its half -- and today they still reach
// the PTY microseconds apart, which is all the CLI's paste heuristic needs.
func TestR3PLeaseConn_ASubmitDoesNotLandInTheSameReadTickAsTheTextBeforeIt(t *testing.T) {
	lc, arrivals := r3pLeaseConn(t)

	if err := lc.WriteDataIn([]byte("git status")); err != nil {
		t.Fatalf("write text: %v", err)
	}
	text := r3pNext(t, arrivals)
	if err := lc.WriteDataIn([]byte("\r")); err != nil {
		t.Fatalf("write submit: %v", err)
	}
	submit := r3pNext(t, arrivals)

	if text.payload != "git status" || submit.payload != "\r" {
		t.Fatalf("the daemon side received %q then %q, want %q then a lone CR -- the gateway must space these frames, never alter or merge them", text.payload, submit.payload, "git status")
	}
	if gap := submit.at.Sub(text.at); gap+r3pSkew < r3pWantGap {
		t.Fatalf("the submit followed the text by %v, want at least %v -- with no gap the PTY hands the CLI both writes in one read tick and Claude Code inserts the CR as a literal newline instead of submitting: the prompt sits there unsent and nothing reports it (spike-SA finding #1)", gap, r3pWantGap)
	}
}

// TestR3PLeaseConn_AdjacentTextFramesAreNotSpaced is the counterweight, and it is GREEN
// today: it is what must STILL be true after the fix. A paste over the 4 KiB frame cap
// arrives as several adjacent data frames, and spacing those by 150 ms each would stretch a
// 40 KiB paste to a second and a half and break it into pieces the CLI reads as separate
// input. Only a submit-only frame may be delayed.
func TestR3PLeaseConn_AdjacentTextFramesAreNotSpaced(t *testing.T) {
	lc, arrivals := r3pLeaseConn(t)

	if err := lc.WriteDataIn([]byte("first chunk of a paste")); err != nil {
		t.Fatalf("write first chunk: %v", err)
	}
	first := r3pNext(t, arrivals)
	if err := lc.WriteDataIn([]byte("second chunk of the same paste")); err != nil {
		t.Fatalf("write second chunk: %v", err)
	}
	second := r3pNext(t, arrivals)

	if gap := second.at.Sub(first.at); gap > r3pUnpacedMax {
		t.Fatalf("two adjacent text frames were spaced by %v, want under %v -- pacing everything would tear a multi-chunk paste apart, which is the same failure this bead is about pointed the other way", gap, r3pUnpacedMax)
	}
}

// TestR3PLeaseConn_ASubmitAfterAPauseIsNotDelayed is the latency half. The gap exists to
// separate a submit from text that arrived with it; when the user's last keystroke is
// already old, there is nothing to separate it from and holding the CR would add 150 ms to
// the most latency-visible action on the phone for no reason at all.
func TestR3PLeaseConn_ASubmitAfterAPauseIsNotDelayed(t *testing.T) {
	lc, arrivals := r3pLeaseConn(t)

	if err := lc.WriteDataIn([]byte("git status")); err != nil {
		t.Fatalf("write text: %v", err)
	}
	r3pNext(t, arrivals)
	time.Sleep(r3pWantGap + 10*time.Millisecond)

	start := time.Now()
	if err := lc.WriteDataIn([]byte("\r")); err != nil {
		t.Fatalf("write submit: %v", err)
	}
	submit := r3pNext(t, arrivals)

	if held := submit.at.Sub(start); held > r3pUnpacedMax {
		t.Fatalf("a submit %v after the last keystroke was still held for %v, want under %v -- the gap is a separation from text that arrived with it, not a tax on every Enter", r3pWantGap, held, r3pUnpacedMax)
	}
}

// TestR3PLeaseConn_ASubmitFollowingASubmitIsNotDelayed resolves the rate mismatch the gap
// would otherwise introduce, and it is a correctness statement before it is a throughput one.
//
// THE HEURISTIC IS ABOUT TEXT-THEN-SUBMIT. A submit arriving after another submit carries no
// text for the CLI to swallow: the worst a CLI can make of a read tick holding only carriage
// returns is a blank line. There is nothing to separate, so there is nothing to wait for.
//
// AND THE RATE WOULD NOT CLOSE. The phone's coalescer releases up to 8 frames/s (one per
// 125 ms window); a gap applied to EVERY submit would drain them at 6.67/s. A held Enter --
// ~30 Hz into the coalescer, which is exactly what PB-INPUT-5 exists to survive -- would then
// arrive faster than this hop forwards, and the lag would grow for as long as the key was
// held. Bounding that would be a second pacing rule; keying the gap on the last TEXT write
// removes it instead, and the gap still applies to the submit that actually needs it.
func TestR3PLeaseConn_ASubmitFollowingASubmitIsNotDelayed(t *testing.T) {
	lc, arrivals := r3pLeaseConn(t)

	if err := lc.WriteDataIn([]byte("make test")); err != nil {
		t.Fatalf("write text: %v", err)
	}
	text := r3pNext(t, arrivals)
	if err := lc.WriteDataIn([]byte("\r")); err != nil {
		t.Fatalf("write first submit: %v", err)
	}
	first := r3pNext(t, arrivals)
	if gap := first.at.Sub(text.at); gap+r3pSkew < r3pWantGap {
		t.Fatalf("premise broken: the submit after the text was spaced by only %v, so this test proves nothing about the one after it", gap)
	}

	if err := lc.WriteDataIn([]byte("\r")); err != nil {
		t.Fatalf("write second submit: %v", err)
	}
	second := r3pNext(t, arrivals)

	if gap := second.at.Sub(first.at); gap > r3pUnpacedMax {
		t.Fatalf("a submit following a submit was held %v, want under %v -- the coalescer releases up to 8 frames/s and a gap on every submit drains 6.67/s, so a held Enter would fall further behind for as long as it was held; and a read tick of nothing but carriage returns has no text for the paste heuristic to swallow", gap, r3pUnpacedMax)
	}
}

// TestR3PLeaseConn_ARunOfSubmitsIsOneFrameAndIsSpacedOnce pins the shape the phone's
// coalescer emits for a held Enter: the run arrives as ONE frame, and the gateway treats it
// as the submit it is -- spaced from the text before it, and passed through byte for byte.
// A gateway that inspected only the first byte, or that split the run, would either miss
// this frame or turn one held key into several separately-paced writes.
func TestR3PLeaseConn_ARunOfSubmitsIsOneFrameAndIsSpacedOnce(t *testing.T) {
	lc, arrivals := r3pLeaseConn(t)

	if err := lc.WriteDataIn([]byte("make test")); err != nil {
		t.Fatalf("write text: %v", err)
	}
	text := r3pNext(t, arrivals)
	if err := lc.WriteDataIn([]byte("\r\r\r")); err != nil {
		t.Fatalf("write submit run: %v", err)
	}
	run := r3pNext(t, arrivals)

	if run.payload != "\r\r\r" {
		t.Fatalf("the submit run arrived as %q, want it forwarded verbatim as one frame", run.payload)
	}
	if gap := run.at.Sub(text.at); gap+r3pSkew < r3pWantGap {
		t.Fatalf("a run of submits followed the text by %v, want at least %v -- a frame of nothing but submit bytes is a submit whatever its length", gap, r3pWantGap)
	}
}
