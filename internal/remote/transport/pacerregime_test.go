package transport

// DrainPacer's REGIME DECISION, asserted directly (ADR-007 B101's third shape: a live object
// that was unfenceable from where a fence was allowed to live).
//
// WHAT WAS ALREADY COVERED AND WHY IT IS NOT THIS. drainbudget_test.go pins
// MaxDrainReadsPerSec and MaxDrainAcksPerSec and the arithmetic against the relay's OpsPerMin
// window -- the NUMBERS the pacer is built from. The pacer's actual job is choosing BETWEEN
// TWO REGIMES on evidence, twenty lines of reasoning that nothing exercised. Pinning the
// number a thing is built from looks exactly like testing the thing.
//
// WHY THIS TEST IS WHITE-BOX, stated because the alternative is tempting and wrong. `batching`,
// `spaced` and `idle` are unexported and Pace's only output is HOW LONG IT BLOCKS, so from
// outside the package the state machine has exactly one observable: elapsed time. Fencing it
// from there means asserting that a call returned *fast* -- an upper-bound wall-clock
// assertion, which is what B106 says this host deletes. The other exit, exporting a regime
// accessor so a test can watch it, is worse than no fence: widening a type's API so a test can
// observe it makes the observable the contract. So the fence sits inside the package, costs
// the shipped API nothing, and asserts the decision itself.
//
// NOT ONE ASSERTION BELOW IS A DURATION. Observe is arithmetic over three fields with no clock
// in it; the one Pace call here is the fresh-pacer path, which sleeps for zero by construction.
// The pacer's own doc refuses a clock seam on purpose -- it "deliberately does NOT go through
// Options.Sleep… collapsing a §6.0 rate budget would silently defeat it" -- and this fence does
// not need one.

import (
	"context"
	"testing"
	"time"
)

// spacedPacer returns a pacer in the batching regime whose last admitted read WAS delayed by
// the spacing, which is the only state in which a single-item read is evidence of anything.
// Setting the field is the point of being in-package: reaching this state through Pace would
// cost a real 333 ms spacing per step and turn a deterministic test into a timing one.
func spacedPacer() *DrainPacer {
	p := NewDrainPacer()
	p.spaced = true
	return p
}

// TestDrainPacer_OneSpacedSingleIsAFluctuationNotARegime is the half of the two-strike rule
// that a test written to the other half would silently lose.
//
// The rule is "one is a fluctuation, two in a row is the regime": a single slow append can
// widen one gather window past the spacing, and dropping to the interactive regime on that one
// observation lets a hiccup strand the drain there. An implementation that left batching on
// the FIRST strike would satisfy any test that only checked the state after two -- which is why
// the first strike is asserted here on its own, with a fixed count that does not follow
// regimeEvidence. If regimeEvidence were lowered to 1, this test fails; that is deliberate.
func TestDrainPacer_OneSpacedSingleIsAFluctuationNotARegime(t *testing.T) {
	p := spacedPacer()

	p.Observe(1)

	if !p.batching {
		t.Errorf("one spaced single-item read left the batching regime. One observation is a "+
			"FLUCTUATION -- a single slow append widens one gather window past the spacing -- and "+
			"acting on it strands the drain in the interactive regime on a hiccup (regimeEvidence "+
			"= %d)", regimeEvidence)
	}
	if p.idle != 1 {
		t.Errorf("idle = %d after one spaced single, want 1: the evidence must be COUNTED even "+
			"though it is not yet acted on, or the second strike has nothing to add to", p.idle)
	}
}

// TestDrainPacer_ConsecutiveSpacedSinglesLeaveTheBatchingRegime is the other half: once the
// spacing has demonstrably bought nothing, holding it costs pure latency.
//
// Read as a flat rate the ceiling contradicts the input budget -- 3 reads/s means reading every
// 333 ms, ~167 ms of mean queueing against a 150 ms p50 budget for the whole path. The escape
// is that when a SPACED read still comes back with one item, the producer rather than the drain
// is the limit, so reads/s can only ever equal arrivals/s and the spacing is pure cost.
//
// The loop follows regimeEvidence rather than restating 2: the property is "consecutive
// evidence is what decides", for whatever the constant says.
func TestDrainPacer_ConsecutiveSpacedSinglesLeaveTheBatchingRegime(t *testing.T) {
	p := spacedPacer()

	for i := 0; i < regimeEvidence; i++ {
		p.Observe(1)
	}

	if p.batching {
		t.Errorf("still batching after %d consecutive spaced single-item reads. The spacing "+
			"produced no batch %d times running, so it bought nothing but latency: at 1/%d s "+
			"between reads that is ~167 ms of mean queueing against a 150 ms p50 budget for the "+
			"WHOLE phone -> PTY path", regimeEvidence, regimeEvidence, MaxDrainReadsPerSec)
	}
}

// TestDrainPacer_AnUnspacedReadIsNoEvidenceEitherWay covers the guard that makes the evidence
// mean anything.
//
// A read that was NOT delayed by the spacing says nothing about whether spacing would have
// produced a batch -- the drain never waited, so of course nothing accumulated. Counting those
// would make the pacer leave the batching regime after any two reads at all, which is the
// ceiling silently disabled: the live tail then dies with codeQuotaExceeded partway through
// the first minute, and nothing before that point looks wrong.
//
// The fixture feeds MORE unspaced reads than the rule needs, so an implementation that ignored
// the guard has crossed the threshold twice over by the end.
func TestDrainPacer_AnUnspacedReadIsNoEvidenceEitherWay(t *testing.T) {
	p := NewDrainPacer() // spaced is false: no read has been delayed

	for i := 0; i < regimeEvidence*2+1; i++ {
		p.Observe(1)
	}

	if p.idle != 0 {
		t.Errorf("idle = %d after %d UNSPACED single-item reads, want 0. An unspaced read is not "+
			"evidence: the drain never waited, so nothing had a chance to accumulate",
			p.idle, regimeEvidence*2+1)
	}
	if !p.batching {
		t.Error("unspaced single-item reads left the batching regime, which disables §6.0's " +
			"ceiling on the strength of observations that cannot support it -- and the failure " +
			"that follows is the live tail refused with codeQuotaExceeded mid-session")
	}
}

// TestDrainPacer_ABacklogRestoresBatchingAndResetsTheEvidence is the arm a naive test misses,
// and it is the same trap the ack batcher's fixture was built around: the obvious assertion
// passes on a broken implementation.
//
// A read returning more than one item is what a backlog actually looks like -- the spacing is
// productive and the ceiling binds -- so the pacer returns to batching. It must ALSO discard
// the evidence it had accumulated. An implementation that set batching and forgot `idle = 0`
// would pass any test asserting only that batching came back, and would then fall out of the
// regime again on the very next single-item read: the two-strike rule silently becomes a
// one-strike rule for the rest of the session, after any backlog at all.
//
// So the assertion is not "batching is back", it is "batching is back AND survives one more
// strike" -- which is only true if the counter was cleared.
func TestDrainPacer_ABacklogRestoresBatchingAndResetsTheEvidence(t *testing.T) {
	p := spacedPacer()

	// Accumulate real evidence and leave the regime, so the reset has something to undo.
	for i := 0; i < regimeEvidence; i++ {
		p.Observe(1)
	}
	if p.batching {
		t.Fatalf("premise: the pacer should have left the batching regime after %d spaced "+
			"singles", regimeEvidence)
	}

	p.Observe(5) // a backlog

	if !p.batching {
		t.Fatal("a read returning several items did not restore the batching regime. A backlog " +
			"is exactly the case where the spacing is productive and §6.0's ceiling binds")
	}
	if p.idle != 0 {
		t.Errorf("idle = %d after a backlog, want 0", p.idle)
	}

	// THE DISCRIMINATOR. One further strike must not be enough, because the evidence was
	// cleared. If it is, the two-strike rule has quietly become a one-strike rule.
	p.Observe(1)
	if !p.batching {
		t.Errorf("ONE spaced single after a backlog left the batching regime again. The backlog "+
			"must clear the accumulated evidence, not merely set the flag: otherwise every "+
			"subsequent single-item read is a %dth strike and the drain leaves the regime on one "+
			"observation for the rest of the session", regimeEvidence+1)
	}
}

// TestDrainPacer_TheFirstReadOfAConnectionIsNotSpaced ties the state machine to the one input
// only Pace can set, without measuring anything.
//
// A fresh pacer has no previous read to space against, so its first admitted read is issued
// immediately and is NOT evidence. That matters per RECONNECT, not once: every reconnect builds
// a new pacer, and a first read wrongly marked spaced would make the drain leave the batching
// regime one observation earlier than the rule allows, every time a handset flaps.
//
// This calls Pace for real and it is still not a timing test: with no previous read and a full
// token bucket the computed delay is zero, so nothing sleeps.
func TestDrainPacer_TheFirstReadOfAConnectionIsNotSpaced(t *testing.T) {
	p := NewDrainPacer()

	started := time.Now()
	if err := p.Pace(context.Background()); err != nil {
		t.Fatalf("Pace on a fresh pacer: %v", err)
	}
	if took := time.Since(started); took > time.Second {
		t.Fatalf("Pace on a fresh pacer took %v; it has no previous read to space against and a "+
			"full window, so it must be admitted immediately", took)
	}

	if p.spaced {
		t.Error("the first read of a connection was marked spaced. Nothing delayed it -- there " +
			"was no previous read -- so treating it as evidence spends one of the two strikes " +
			"the regime rule requires, on every reconnect")
	}
	p.Observe(1)
	if p.idle != 0 {
		t.Errorf("idle = %d after the first read of a connection returned one item, want 0", p.idle)
	}
}

// TestDrainPacer_TheWindowBudgetIsCappedAtOneMinute is what makes the regime switching safe to
// do at all.
//
// The regimes are a latency trade; the token bucket is the guarantee underneath them -- the
// SUSTAINED average cannot exceed the ceiling "however the regime flaps". The cap is the part
// that is easy to omit and impossible to notice: without it an idle handset banks credit for
// as long as it sits in a pocket, and the first burst after an hour spends thousands of reads
// at once against a 600/min relay window. Refill is rate-proportional up to the cap and not
// past it, which is what "a one-minute window" means operationally.
func TestDrainPacer_TheWindowBudgetIsCappedAtOneMinute(t *testing.T) {
	p := NewDrainPacer()
	if p.capacity != MaxDrainReadsPerSec*60 {
		t.Fatalf("premise: capacity = %v, want one minute at the ceiling", p.capacity)
	}

	// Spend the window, then idle for an hour.
	now := time.Now()
	p.tokens = 0
	p.refilled = now.Add(-time.Hour)
	p.refillTo(now)

	if p.tokens > p.capacity {
		t.Errorf("an hour idle refilled %v tokens against a %v capacity. An uncapped bucket lets "+
			"a handset bank credit while it sits in a pocket and spend it as one burst, which the "+
			"relay answers with codeQuotaExceeded -- the failure the ceiling exists to prevent, "+
			"reached by the mechanism that enforces it", p.tokens, p.capacity)
	}
	if p.tokens != p.capacity {
		t.Errorf("tokens = %v after an hour idle, want the full window %v: refill is "+
			"rate-proportional up to the cap", p.tokens, p.capacity)
	}

	// And a PARTIAL idle refills proportionally rather than jumping to full.
	q := NewDrainPacer()
	q.tokens = 0
	q.refilled = now.Add(-10 * time.Second)
	q.refillTo(now)
	if want := float64(MaxDrainReadsPerSec) * 10; q.tokens != want {
		t.Errorf("tokens = %v after 10 s idle, want %v (rate * elapsed); a bucket that refills "+
			"to full on any gap is not a rate limit", q.tokens, want)
	}
}
