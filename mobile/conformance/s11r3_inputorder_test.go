package conformance_test

// Slice S11 REVIEW ROUND 3 -- FAILING-FIRST (TDD RED, GG-5) for the phone's INPUT bucket,
// which round 1 left standing when it fixed the gateway's reply bucket.
//
// THE DEFECT. mobile/commands.go sendInputFrame does three things with nothing spanning
// them: Sequencer.NextInput allocates a durable seq, SealInputData seals the frame under it,
// and relay.Client.MailboxAppend puts it on the wire. The producers driven here are the
// caller's goroutine (SendInput, Paste, Resize; PB-BIND-6 makes concurrent facade calls part
// of the contract) and drainHeldInput on time.AfterFunc's goroutine -- so a goroutine
// descheduled between the allocation and the append lets a LATER seq reach the relay first.
//
// ROUND 4 CORRECTION. This header used to say those were "two producers in production",
// full stop. They are not: they are the two INPUT producers. Commands draw from the same
// Sequencer (phonecore/input.go), so every command author is a producer on this bucket too,
// and a fix scoped to the input frames alone leaves the inversion standing. That claim is
// what let the defect survive round 3; s11r4_commandorder_test.go drives the pair this test
// never did.
//
// WHY IT COSTS TWO KEYSTROKES, NOT ONE. The machine's inbound guard is a single
// crypto.MailboxReceiver over one (sender, epoch) stream:
//
//   - the HIGH frame arrives first: crypto/envelope.go's `gap := seen && Seq > hi+1` marks
//     it, and remotegw/command_loop.go routeInput drops a gapped input frame silently. The
//     high-water advances anyway.
//   - the LOW frame arrives second: `seen && Seq <= hi` is ErrStaleSeq -- dropped too.
//
// So an inversion loses BOTH frames, mid-line, and MailboxAppend returned nil for each: the
// phone's undelivered ledger records nothing and PB-INPUT-1's "no keystroke is silently
// dropped" is broken in the one direction the app cannot see.
//
// WHY THE ASSERTION IS ON THE RAW ENVELOPE HEADERS. The order is what the defect IS, and it
// is observable with no key and no decryption (crypto.ParseEnvelope reads the authenticated
// header). Asserting only on the bytes that survived would measure the machine's tolerance
// for the defect rather than the defect, so both are asserted: the ORDER the relay accepted
// them in, and the LOSS the real gateway opener then takes.
//
// This file contains NO implementation.

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// s11r3PhoneSeqs reads the machine's mailbox WITHOUT the inbound seq guard and returns the
// sequence numbers of the phone's envelopes in the order the relay accepted them. It does
// not ack: this is an observation, not a delivery.
func s11r3PhoneSeqs(t *testing.T, h *harness, from uint64) []uint64 {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()

	var seqs []uint64
	cursor := from
	for {
		items, err := h.machineRelay.MailboxRead(h.ctx, cursor)
		if err != nil {
			t.Fatalf("machine mailbox read: %v", err)
		}
		if len(items) == 0 {
			return seqs
		}
		for _, it := range items {
			if it.Cursor > cursor {
				cursor = it.Cursor
			}
			env, err := crypto.ParseEnvelope(it.Envelope)
			if err != nil {
				t.Fatalf("parse envelope at cursor %d: %v", it.Cursor, err)
			}
			seqs = append(seqs, env.Header.Seq)
		}
	}
}

// TestS11R3_ConcurrentInputNeverReachesTheRelayOutOfSequence drives the two production
// producers at once and asserts the phone's frames land on the relay in the order it
// numbered them.
//
// The callers type and paste; typing arms the coalescer's one-shot drain (scheduleDrain), so
// the app's own AfterFunc goroutine is the second producer throughout -- no test-only
// thread is invented to manufacture the race.
func TestS11R3_ConcurrentInputNeverReachesTheRelayOutOfSequence(t *testing.T) {
	h, _ := s11rProxiedHarness(t)

	h.mu.Lock()
	from := h.cursor
	inputsBefore := len(h.Inputs)
	h.mu.Unlock()

	const callers, rounds = 3, 60
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < rounds; j++ {
				// A keystroke: buffered by the coalescer and released by the drain timer.
				if err := h.App.SendInput(testSession, []byte("k")); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
				// A paste: PB-INPUT-6 sends it immediately, flushing the buffer first, so it
				// is the other half of the pair that races the drain.
				if err := h.App.Paste(testSession, "p"); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	// Let the last window close so the tail is on the wire before anything is read.
	time.Sleep(4 * phonecore.InputFrameInterval)

	mu.Lock()
	sendErrs := errs
	mu.Unlock()
	if len(sendErrs) > 0 {
		t.Fatalf("%d of %d input calls failed, the first being %v -- the premise of this test is "+
			"that every call was accepted, so an inversion is the only way a frame can go missing",
			len(sendErrs), callers*rounds*2, sendErrs[0])
	}

	seqs := s11r3PhoneSeqs(t, h, from)
	if len(seqs) < rounds {
		t.Fatalf("only %d phone envelopes reached the relay for %d input calls; the race this test "+
			"is about needs real traffic to expose, so this run proves nothing", len(seqs), callers*rounds*2)
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
			"sendInputFrame allocates the seq, seals and appends with nothing spanning the three "+
			"steps, so a goroutine descheduled after Sequencer.NextInput lets a later seq overtake "+
			"it. The machine's inbound guard then drops BOTH frames -- the high one as a gap "+
			"(routeInput returns nil for f.Gap) and the low one as crypto.ErrStaleSeq -- while "+
			"MailboxAppend returned nil for each, so the phone's undelivered ledger records "+
			"nothing. Two keystrokes vanish mid-line with no signal anywhere (PB-INPUT-1).",
			inversions, len(seqs), first)
	}

	// The CONSEQUENCE, measured through the real gateway opener and the real shared inbound
	// seq guard: every frame the relay accepted must survive the guard. This is the half the
	// user feels, and it is asserted separately because a fix that reordered the frames back
	// into line at the machine would satisfy neither requirement.
	// Drain reads ONE relay page per call, so it is repeated until the mailbox stops
	// yielding: a short read would look exactly like the loss under test.
	delivered := 0
	for {
		h.Drain()
		h.mu.Lock()
		got := len(h.Inputs) - inputsBefore
		h.mu.Unlock()
		if got == delivered {
			break
		}
		delivered = got
	}
	if delivered < len(seqs) {
		t.Errorf("the machine opened %d of the %d input frames the relay accepted; %d were dropped "+
			"by the inbound seq guard. PB-INPUT-1 requires that no keystroke is silently dropped, "+
			"and these are invisible to the phone as well: MailboxAppend succeeded for every one.",
			delivered, len(seqs), len(seqs)-delivered)
	}
}

// describeInversion renders the offending neighbourhood of the sequence.
func describeInversion(seqs []uint64, i int) string {
	lo := i - 2
	if lo < 0 {
		lo = 0
	}
	hi := i + 3
	if hi > len(seqs) {
		hi = len(seqs)
	}
	parts := make([]string, 0, hi-lo)
	for j := lo; j < hi; j++ {
		mark := ""
		if j == i {
			mark = ">>"
		}
		parts = append(parts, mark+strconv.FormatUint(seqs[j], 10))
	}
	return strings.Join(parts, " ")
}
