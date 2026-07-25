package conformance_test

// Slice S11 REVIEW ROUND 4 -- FAILING-FIRST (TDD RED, GG-5) for the half of the phone's
// mailbox bucket round 3 left unserialised.
//
// WHAT ROUND 3 GOT WRONG, and it was a statement about the code rather than a bug in it.
// s11r3_inputorder_test.go's own header says "two producers exist in production -- the
// caller's goroutine and drainHeldInput". That is false, and the file it is about says so:
// phonecore/input.go's Sequencer doc records that "Commands AND input frames draw from ONE
// Sequencer per epoch because they share a single MailboxReceiver key (SenderKeyID stays
// zero), so a private per-kind counter would collide". EVERY command author is a producer on
// this bucket. Round 3's a.inputMu therefore serialises one SLICE of the bucket -- the input
// frames -- on a stream that mobile/commands.go sealSignedCommand and unsignedCommand also
// write to, allocating their seq and appending with nothing spanning the steps.
//
// So the inversion survived the fix, and this test is the producer pair round 3 never drove:
// ONE command author racing the typists.
//
// THE CONSEQUENCE IS WORSE THAN THE INPUT-ONLY CASE, because the dropped frame may now be a
// COMMAND. The machine's inbound guard is a single crypto.MailboxReceiver over one
// (sender, epoch) stream, and the two kinds fail differently:
//
//   - an INPUT frame marked Gap is dropped silently (remotegw routeInput returns nil for
//     f.Gap), so an inversion between two keystrokes loses BOTH;
//   - a COMMAND frame marked Gap is EXECUTED (routeCommand ignores the bit and processBatch
//     advances past it), so an inversion involving a command loses exactly the LOW frame.
//
// When that low frame is the command, the op is gone with no signal on either side:
// MailboxAppend returned nil, so the phone shows an op in flight forever -- a take_control
// that never confirms and leaves the keyboard dead, or a kill that never runs.
//
// IT IS IN-CONTRACT CONCURRENCY, not a contrived race: typing while a screen opens its
// terminal peek, or while Release/TakeControl fires. PB-BIND-6 makes concurrent facade calls
// part of the published contract.
//
// This file contains NO implementation.

import (
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// s11r4CommandActions is the pair the command producer alternates. They are READS
// (unsignedCommand), so they need no reconcile, resolve no lease and mutate nothing on the
// machine -- the only thing under test is that they draw from the same Sequencer as the
// keystrokes and append to the same bucket.
var s11r4CommandActions = []string{schema.ActionTerminalWatch, schema.ActionTerminalUnwatch}

// TestS11R4_ACommandRacingTypistsNeverReachesTheRelayOutOfSequence drives the real producer
// pair -- a screen opening and closing its terminal peek while three callers type -- and
// asserts the phone's frames land on the relay in the order it numbered them, and that every
// one of them survives the machine's guard.
func TestS11R4_ACommandRacingTypistsNeverReachesTheRelayOutOfSequence(t *testing.T) {
	h, _ := s11rProxiedHarness(t)

	h.mu.Lock()
	from := h.cursor
	inputsBefore := len(h.Inputs)
	commandsBefore := len(h.Commands)
	h.mu.Unlock()

	const typists, rounds, peeks = 3, 40, 40
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)
	fail := func(err error) {
		mu.Lock()
		errs = append(errs, err)
		mu.Unlock()
	}

	for i := 0; i < typists; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < rounds; j++ {
				// A keystroke, buffered by the coalescer and released by the drain timer.
				if err := h.App.SendInput(testSession, []byte("k")); err != nil {
					fail(err)
				}
				// A paste: PB-INPUT-6 sends it immediately, flushing the buffer first.
				if err := h.App.Paste(testSession, "p"); err != nil {
					fail(err)
				}
			}
		}()
	}
	// THE PRODUCER ROUND 3 NEVER DROVE. One screen opening and closing its peek is enough:
	// the defect is that the command sealer allocates its seq outside any lock, so a single
	// command author descheduled between the draw and the append lets every keystroke the
	// typists number afterwards overtake it.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < peeks*len(s11r4CommandActions); j++ {
			var err error
			if s11r4CommandActions[j%len(s11r4CommandActions)] == schema.ActionTerminalWatch {
				err = h.App.TerminalWatch(testSession)
			} else {
				err = h.App.TerminalUnwatch(testSession)
			}
			if err != nil {
				fail(err)
			}
		}
	}()
	wg.Wait()
	// Let the last coalescing window close so the tail is on the wire before anything is read.
	time.Sleep(4 * phonecore.InputFrameInterval)

	mu.Lock()
	sendErrs := errs
	mu.Unlock()
	if len(sendErrs) > 0 {
		t.Fatalf("%d facade calls failed, the first being %v -- the premise of this test is that "+
			"every call was accepted, so an inversion is the only way a frame can go missing",
			len(sendErrs), sendErrs[0])
	}

	seqs := s11r3PhoneSeqs(t, h, from)
	if len(seqs) < peeks {
		t.Fatalf("only %d phone envelopes reached the relay; the race this test is about needs real "+
			"traffic to expose, so this run proves nothing", len(seqs))
	}

	var inversions int
	var first string
	for i := 1; i < len(seqs); i++ {
		if seqs[i] > seqs[i-1] {
			continue
		}
		inversions++
		if first == "" {
			first = describeInversion(seqs, i)
		}
	}
	if inversions > 0 {
		t.Errorf("%d of %d phone envelopes reached the relay OUT OF SEQUENCE (first: %s).\n"+
			"Commands and input draw from ONE Sequencer per epoch (phonecore/input.go), so the "+
			"phone -> machine bucket has as many producers as it has command authors. "+
			"sealSignedCommand and unsignedCommand allocate the seq, seal and append with nothing "+
			"spanning the three steps, so a command author descheduled after Sequencer.NextCommand "+
			"lets later keystrokes overtake it. The lock must cover the BUCKET, not the input "+
			"frames alone.", inversions, len(seqs), first)
	}

	// THE CONSEQUENCE, measured through the real gateway opener and the real shared inbound
	// seq guard. Drain reads ONE relay page per call, so it is repeated until the mailbox
	// stops yielding: a short read would look exactly like the loss under test.
	prev := -1
	for {
		h.Drain()
		h.mu.Lock()
		got := len(h.Inputs) - inputsBefore + len(h.Commands) - commandsBefore
		h.mu.Unlock()
		if got == prev {
			break
		}
		prev = got
	}
	h.mu.Lock()
	inputs := len(h.Inputs) - inputsBefore
	peeked := 0
	for _, c := range h.Commands[commandsBefore:] {
		if c.Action == schema.ActionTerminalWatch || c.Action == schema.ActionTerminalUnwatch {
			peeked++
		}
	}
	h.mu.Unlock()

	if inputs+peeked < len(seqs) {
		t.Errorf("the machine opened %d of the %d frames the relay accepted (%d input, %d command); "+
			"%d were dropped by the inbound seq guard while MailboxAppend returned nil for every one. "+
			"PB-INPUT-1 requires that no keystroke is silently dropped, and a COMMAND lost this way is "+
			"worse still: the op is never answered, so the phone shows it in flight forever.",
			inputs+peeked, len(seqs), inputs, peeked, len(seqs)-(inputs+peeked))
	}
	// ... and said as its own assertion, because a command is the frame whose loss the user
	// cannot recover from by typing again: routeCommand executes a Gap frame, so an inversion
	// kills exactly the LOW one, and when that is the command the op is pending forever.
	if peeked < peeks*len(s11r4CommandActions) {
		t.Errorf("the machine opened %d of the %d peek commands the phone sealed; %d vanished between "+
			"a successful MailboxAppend and the machine. The same hole eats a take_control (the "+
			"keyboard stays dead and the button does nothing visible) or a kill (the session lives on), "+
			"with the op pending forever on the phone.",
			peeked, peeks*len(s11r4CommandActions), peeks*len(s11r4CommandActions)-peeked)
	}
}
