package protocol

// FAILING-FIRST (TDD RED, GG-5) for the Wave R5 round-4 review fix-pack (bead
// agents-tracker-hggx.6), wire half of MAJOR 2.
//
// The daemon now answers the concurrent double-driver's LOSER undecidably
// (daemon.ErrLaunchOutcomeUnknown) instead of handing it the winner's phase-1
// reservation as an authoritative success. That answer must survive the wire with its
// MEANING intact: a launch whose outcome the machine cannot decide is neither an
// APPLIED session nor a flat refusal. Replied as CodePolicy it would render on the
// phone as REFUSED ("the machine refused this launch"), which is a different lie from
// the one round 4 is fixing -- the launch may well be running.
//
// ADR-017 T9 already owns the vocabulary and LaunchDeliveryNotice.OUTCOME_UNKNOWN
// already exists on the phone with nothing producing it on the launch reply; this is
// the code that does.
//
// It must fail (undefined: schema.CodeOutcomeUnknown) until the GREEN slice lands.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// TestR5Round4_UndecidableLaunchRepliesOutcomeUnknownNotAFlatRefusal: the daemon's
// undecidable answer reaches the phone as outcome_unknown, and the D10 activity record
// says the same thing rather than claiming the launch was applied.
func TestR5Round4_UndecidableLaunchRepliesOutcomeUnknownNotAFlatRefusal(t *testing.T) {
	p := r5PresetView(t, "preset-api")
	b := newR5Backend(p)
	b.launchErr = daemon.ErrLaunchOutcomeUnknown
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	rc.writeControl(r5Frame(rep, "devA:01JR4UNK", &schema.SessionLaunchReq{
		PresetID: "preset-api", PresetRevision: "rev-1", Cols: 80, Rows: 24,
	}))
	got := rc.readControl()

	if got.Op == OpSessionLaunch {
		t.Fatalf("an undecidable launch replied the SUCCESS op (session %v); the phone renders "+
			"that as APPLIED -- 'the session was created on the machine' -- for a launch the "+
			"machine cannot prove happened", got.Session)
	}
	if got.ErrorCode != schema.CodeOutcomeUnknown {
		t.Errorf("undecidable launch reply code = %q, want %q: replied as a generic policy refusal "+
			"the phone tells the user the machine REFUSED a launch that may be running (ADR-017 T9's "+
			"delivery vocabulary names this state, and it is not 'refused')",
			got.ErrorCode, schema.CodeOutcomeUnknown)
	}

	recs := b.activityRecords()
	if len(recs) != 1 {
		t.Fatalf("activity records = %d, want 1 (an undecidable launch is still an audited event)", len(recs))
	}
	if recs[0].Outcome == schema.OutcomeApplied {
		t.Errorf("activity outcome = %q for an undecidable launch; the log must not claim applied",
			recs[0].Outcome)
	}
}

// TestR5Round4_OrdinaryPolicyRefusalKeepsItsCode: the new code is reserved for the
// undecidable answer alone -- an ordinary daemon-side refusal still reads `policy`, so
// the phone's refusal states do not collapse into one.
func TestR5Round4_OrdinaryPolicyRefusalKeepsItsCode(t *testing.T) {
	p := r5PresetView(t, "preset-api")
	b := newR5Backend(p)
	b.launchErr = errNotUnderRoot
	sock := serveRemoteAPI(t, b)
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	rc.writeControl(r5Frame(rep, "devA:01JR4POL", &schema.SessionLaunchReq{
		PresetID: "preset-api", PresetRevision: "rev-1", Cols: 80, Rows: 24,
	}))
	got := rc.readControl()
	if got.ErrorCode != CodePolicy {
		t.Errorf("ordinary daemon refusal code = %q, want %q", got.ErrorCode, CodePolicy)
	}
}
