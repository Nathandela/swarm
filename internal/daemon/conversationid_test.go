package daemon

// FAILING-FIRST (TDD RED, GG-5) for phase 1 of the hands-off handoff sweep:
// SetConversationID must VALIDATE BEFORE IT LATCHES.
//
// THE MEASURED DEFECT. Zero of seven real claude sessions on the owner's machine
// hold a usable conversation id: five are empty, and two hold the literal string
// "./cmd/swarm/" -- latched by the transcript-tail scraper (skeleton's
// captureConversationIDGated) out of a tail that merely looked extractable.
// Capture is write-once, so once junk holds the field the AUTHORITATIVE
// hook-sourced id can never correct it: the session is poisoned for its whole
// life, and every feature that resumes or hands off a conversation refuses.
//
// THE GUARD'S PLACEMENT IS THE WHOLE FIX. The check belongs BEFORE the
// write-once branch, never after. A rejected id is never stored, so the field
// stays EMPTY and the next capture -- the authoritative one -- can still win.
// Write-once semantics are untouched; only the set of values allowed to take the
// latch shrinks.
//
// THE GATE IS ON AGENT TYPE, exactly as skeleton's
// validateStoredResumeConversationID gates: only "codex" and "claude" have a
// characterized canonical id format. Every other provider keeps today's
// behaviour byte for byte, because silently refusing an uncharacterized
// provider's perfectly good id would be a regression, not a fix.
//
// WHAT A REJECTION RETURNS: nil. See
// TestSetConversationID_RejectionIsANoOpNotAnError for the reasoning.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// Two distinct canonical ids in the lowercase-UUID spelling
// adapter.IsCanonicalConversationID accepts, so write-once has a second value to
// refuse.
const (
	convCanonicalA = "4a7a2465-d8f0-4c05-a7a9-c44d8077b22b"
	convCanonicalB = "01a00335-9a50-79e2-8253-e08861d67c4d"
)

// The junk actually observed latched on the owner's machine, plus the shapes a
// scraper is most likely to hand over next. The traversal spellings are here
// because a stored id is later joined into a transcript path, where
// filepath.Join CLEANS -- so a stored ".." escapes the projects root. Refusing
// the value at the latch is the cheapest place to close that.
var nonCanonicalConversationIDs = []string{
	"./cmd/swarm/",
	"../../../../etc/passwd",
	"..",
	"conv-dedup-1",
	"4A7A2465-D8F0-4C05-A7A9-C44D8077B22B", // uppercase is not the canonical spelling
	"4a7a2465-d8f0-4c05-a7a9-c44d8077b22",  // one hex digit short
}

// seedConvSession registers a running session of a given provider with an empty
// conversation id -- the state every session starts in.
func seedConvSession(t *testing.T, d *Daemon, id, agentType string) {
	t.Helper()
	now := time.Now()
	if err := d.saveMeta(persist.Meta{
		ID:           id,
		AgentType:    agentType,
		CreatedAt:    now,
		LastActivity: now,
		Status:       status.Status{Process: status.ProcessRunning, Turn: status.TurnUnknown, Interaction: status.InteractionNone},
	}); err != nil {
		t.Fatalf("seed %s session: %v", agentType, err)
	}
}

// assertConversationID checks the in-memory registry AND the on-disk meta. Both
// matter: a rejection that only held in memory would still poison the field
// across a daemon restart, which is exactly the failure being fixed.
func assertConversationID(t *testing.T, d *Daemon, id, want string) {
	t.Helper()
	got, ok := d.Get(id)
	if !ok {
		t.Fatalf("Get(%s): not found", id)
	}
	if got.ConversationID != want {
		t.Fatalf("in-memory ConversationID = %q, want %q", got.ConversationID, want)
	}
	if disk := scanMetaByID(t, d, id); disk.ConversationID != want {
		t.Fatalf("persisted ConversationID = %q, want %q", disk.ConversationID, want)
	}
}

// TestSetConversationID_RejectsNonCanonicalIDForClaude: claude has a
// characterized id format, so a value that is not in it never takes the latch.
func TestSetConversationID_RejectsNonCanonicalIDForClaude(t *testing.T) {
	assertRejectsNonCanonical(t, "claude")
}

// TestSetConversationID_RejectsNonCanonicalIDForCodex: same gate, same format,
// second characterized provider.
func TestSetConversationID_RejectsNonCanonicalIDForCodex(t *testing.T) {
	assertRejectsNonCanonical(t, "codex")
}

func assertRejectsNonCanonical(t *testing.T, agentType string) {
	t.Helper()
	for _, junk := range nonCanonicalConversationIDs {
		t.Run(junk, func(t *testing.T) {
			var observed int
			cfg := daemonConfig(t)
			cfg.onMetaSave = func(persist.Meta) { observed++ }
			d := openDaemon(t, cfg)

			const id = "s1"
			seedConvSession(t, d, id, agentType)
			observed = 0 // drop the seed's own onMetaSave

			if err := d.SetConversationID(id, junk); err != nil {
				t.Fatalf("SetConversationID(%q) = %v; a rejected id is a no-op, not a failure", junk, err)
			}
			assertConversationID(t, d, id, "")
			if observed != 0 {
				t.Fatalf("rejecting %q fired onMetaSave %d times, want 0: nothing was written, "+
					"so no roster event may claim otherwise", junk, observed)
			}
		})
	}
}

// TestSetConversationID_RejectionLeavesTheFieldOpenForTheAuthoritativeCapture is
// the point of the whole change. Today the scraper's junk latches first and the
// hook's authoritative id is discarded forever. After the guard, the junk is
// simply not stored, so the hook still wins whenever it arrives.
func TestSetConversationID_RejectionLeavesTheFieldOpenForTheAuthoritativeCapture(t *testing.T) {
	var observed []persist.Meta
	cfg := daemonConfig(t)
	cfg.onMetaSave = func(m persist.Meta) { observed = append(observed, m) }
	d := openDaemon(t, cfg)

	const id = "s1"
	seedConvSession(t, d, id, "claude")
	observed = nil

	// The scraper gets there first with the junk it really produced.
	if err := d.SetConversationID(id, "./cmd/swarm/"); err != nil {
		t.Fatalf("SetConversationID(junk): %v", err)
	}
	assertConversationID(t, d, id, "")

	// The hook arrives later with the real thing. It must still latch.
	if err := d.SetConversationID(id, convCanonicalA); err != nil {
		t.Fatalf("SetConversationID(canonical) after a rejection: %v", err)
	}
	assertConversationID(t, d, id, convCanonicalA)

	if len(observed) != 1 || observed[0].ConversationID != convCanonicalA {
		t.Fatalf("onMetaSave observations = %+v, want exactly one carrying %q: the rejection writes "+
			"nothing and the canonical capture writes once", observed, convCanonicalA)
	}
}

// TestSetConversationID_CanonicalIDLatchesOnTheFirstCall pins the unchanged
// happy path: a well-formed id is stored on first sight, in memory and on disk.
func TestSetConversationID_CanonicalIDLatchesOnTheFirstCall(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))

	const id = "s1"
	seedConvSession(t, d, id, "claude")

	if err := d.SetConversationID(id, convCanonicalA); err != nil {
		t.Fatalf("SetConversationID: %v", err)
	}
	assertConversationID(t, d, id, convCanonicalA)
}

// TestSetConversationID_UncharacterizedProviderStillLatchesAnything: agy,
// opencode and the reserved fake have NO characterized id format, so the guard
// must not touch them. Whatever their producer captures is still what gets
// stored -- today's behaviour, byte for byte. Widening the gate to every
// provider would refuse ids that are perfectly valid for that provider.
func TestSetConversationID_UncharacterizedProviderStillLatchesAnything(t *testing.T) {
	for _, agentType := range []string{"opencode", "agy", "fake"} {
		t.Run(agentType, func(t *testing.T) {
			d := openDaemon(t, daemonConfig(t))

			const id = "s1"
			seedConvSession(t, d, id, agentType)

			// Deliberately the exact string the claude/codex gate refuses.
			if err := d.SetConversationID(id, "./cmd/swarm/"); err != nil {
				t.Fatalf("SetConversationID: %v", err)
			}
			assertConversationID(t, d, id, "./cmd/swarm/")
		})
	}
}

// TestSetConversationID_WriteOnceStillHoldsForCanonicalIDs: the guard shrinks
// the set of values that may take the latch; it does not reopen the latch. The
// first canonical id still wins over every later one, so the id never flaps.
func TestSetConversationID_WriteOnceStillHoldsForCanonicalIDs(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))

	const id = "s1"
	seedConvSession(t, d, id, "claude")

	if err := d.SetConversationID(id, convCanonicalA); err != nil {
		t.Fatalf("SetConversationID(A): %v", err)
	}
	if err := d.SetConversationID(id, convCanonicalB); err != nil {
		t.Fatalf("SetConversationID(B): %v", err)
	}
	assertConversationID(t, d, id, convCanonicalA)
}

// TestSetConversationID_RejectionIsANoOpNotAnError fixes the return contract.
//
// THE DECISION: a rejection returns nil, and an error from SetConversationID
// keeps meaning what it means today -- an infrastructural failure (unknown
// session, or a failed write). Three reasons:
//
//  1. The method ALREADY returns nil for "your value did not become the stored
//     id": that is exactly what the write-once branch and the empty-id guard do.
//     A rejection is the same class of outcome, so it gets the same answer.
//  2. Two of the four production callers log every error they get
//     (skeleton/interaction.go and skeleton/backend.go, "could not persist ...
//     conversation identity"). A refused scrape is a deliberate policy outcome,
//     not a fault, and the scraper retries on every transcript growth -- so an
//     error return would turn a working guard into a log flood describing
//     correct behaviour.
//  3. skeleton/resume_migration.go's refetch-the-winner logic is unaffected
//     either way: it only reaches SetConversationID with an id it has already
//     put through adapter.IsCanonicalConversationID, so this guard can never
//     fire on that path, and it refetches the actual winner regardless of the
//     returned error.
func TestSetConversationID_RejectionIsANoOpNotAnError(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))

	const id = "s1"
	seedConvSession(t, d, id, "claude")

	if err := d.SetConversationID(id, "./cmd/swarm/"); err != nil {
		t.Fatalf("a refused id returned %v, want nil: callers log every error, and a refusal is "+
			"correct behaviour, not a fault", err)
	}
	// nil AND not stored: the two halves of the contract only mean something together.
	assertConversationID(t, d, id, "")
	// An error still means an infrastructural failure, and only that.
	if err := d.SetConversationID("no-such-session", convCanonicalA); err == nil {
		t.Fatalf("SetConversationID on an unknown session must still error")
	}
}
