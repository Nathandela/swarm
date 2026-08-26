package skeleton

// FAILING-FIRST (TDD RED, GG-5) for Wave G item G.2's daemon half:
// docs/specifications/chat-surface-plan.md §9, "`phone control` becomes `phone sent HH:mm`".
// Bead: agents-tracker-tbpm.9. Evidence: docs/verification/chat-surface.md, Wave G.
//
// THE FACT WAS HERE AND WAS NOT SPENT. phoneActivityAt has been documented since Wave G as
// returning an INSTANT rather than a boolean "so the terminal can draw it" -- and until this
// change its only callers were phoneRecentlyActive and this package's own tests. The board
// row was handed a bare bool, so it could only say `phone`, while three comments in
// phonepresence.go asserted in the present tense that the terminal said "phone sent 09:41".
// It did not.
//
// WHAT THIS FILE OWNS, AND WHAT IT DELIBERATELY DOES NOT RE-TEST. The link under test is
// RECORD -> ROW: a session's activity instant reaching the roster row the board renders,
// through the real assembly, with the remote listener standing so the production wiring in
// serve.go is the thing being exercised rather than a hand-registered stand-in. The link in
// FRONT of it -- a delivered composer_send calling notePhoneActivity, and a refused one not
// calling it -- is already pinned in s0b_phonepresence_test.go and is not restated here;
// notePhoneActivity below is the very function composerSend calls on its delivery path. The
// link BEHIND it -- the row's words -- is pinned in internal/tui/g2_phonesentmarker_test.go.
//
// WHAT CROSSES, AND WHAT DELIBERATELY DOES NOT. SessionView gains RemoteActivityAt, carried
// ONLY while it is inside phoneActiveHorizon. The horizon stays server-side for the reason
// the Group does (E6.9: clients never re-derive what the daemon already decided) -- and
// because a client applying its own horizon to a raw instant could disagree with
// anyControlled about the same record, which is the gate ADR-010 Amendment 3 C3 rests on.
// Absent therefore means "no message in the window", and the row draws nothing.
//
// RemoteControlled is UNTOUCHED. It answers a different question for different consumers --
// that same supervision gate, and the roster poller's diff key -- and the two travel together
// in practice because no shipped client has taken a lease since R6 replaced take_control with
// composer_send (android/unbound-verbs.tsv, zero Kotlin callers).

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol"
)

// g2Rig is one live session on an assembly WITH THE REMOTE TIER STANDING, because the roster's
// remote-activity source is wired only where a remote listener exists -- and that wiring is
// exactly what this file is testing.
type g2Rig struct {
	sk      *Daemon
	local   string
	session string
	client  *protocol.Client
}

func newG2Rig(t *testing.T) *g2Rig {
	t.Helper()
	sk := assemble(t, func(c *Config) {
		c.RemoteSocketPath = filepath.Join(c.StateDir, "remote.sock")
	})
	sk.adapterFor = func(string) (adapter.Adapter, bool) {
		return &r7KeystrokeAdapter{Adapter: newPlainAdapter().Adapter}, true
	}
	m := launchFake(t, sk, r7StdinScript)
	return &g2Rig{
		sk:      sk,
		local:   m.ID,
		session: protocol.NamespacedID(sk.api.endpointID, m.ID),
		client:  dialClient(t, sk),
	}
}

// row reads this session's roster row the way the board does.
func (r *g2Rig) row(t *testing.T) protocol.SessionView {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		views, err := r.client.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, v := range views {
			if v.ID == r.session {
				return v
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("session %q never appeared on the owner's roster", r.session)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestG2_TheOwnersRosterCarriesTheInstantThePhoneSentAt is the link the finding is about:
// what the daemon RECORDED reaching the row the terminal DRAWS. Without it the board has
// nothing to say but a noun.
func TestG2_TheOwnersRosterCarriesTheInstantThePhoneSentAt(t *testing.T) {
	r := newG2Rig(t)

	if at := r.row(t).RemoteActivityAt; at != nil {
		t.Fatalf("an untouched session's row carries a phone instant %v; absent is how this row "+
			"says nothing, and it must be the resting state", at)
	}

	// notePhoneActivity is composerSend's own call on the delivery path (chat.go); that it is
	// reached on a delivery and skipped on a refusal is pinned in s0b_phonepresence_test.go.
	before := time.Now()
	r.sk.notePhoneActivity(r.local)

	at := r.row(t).RemoteActivityAt
	if at == nil {
		t.Fatalf("the owner's roster row carries NO instant after a message reached the session.\n" +
			"phoneActivityAt has held it all along -- documented as an instant rather than a " +
			"boolean precisely so the terminal could draw it -- and nothing spent it. A row that " +
			"knows only THAT a phone acted can say only \"phone\", which reads as a presence claim " +
			"nobody on this wire measures (plan G.5).")
	}
	if at.Before(before) || time.Since(*at) > time.Minute {
		t.Fatalf("the row's instant is %v, want the instant the message arrived (about %v)", *at, before)
	}
	if got := r.sk.phoneActivityAt(r.local); !at.Equal(got) {
		t.Fatalf("the row says %v and the record says %v; the row is the record or it is a second "+
			"opinion about the same event", *at, got)
	}
}

// TestG2_TheInstantAgesOutWithTheGate keeps the marker honest at the far end. The horizon is
// applied server-side, so a client never has to hold a clock contract -- and so the row and
// anyControlled can never disagree about the same record, which is the gate C3 rests on.
func TestG2_TheInstantAgesOutWithTheGate(t *testing.T) {
	r := newG2Rig(t)

	r.sk.setPhoneActivityForTest(r.local, time.Now())
	if r.row(t).RemoteActivityAt == nil {
		t.Fatalf("a fresh record did not reach the row at all")
	}

	r.sk.setPhoneActivityForTest(r.local, time.Now().Add(-phoneActiveHorizon-time.Second))
	if at := r.row(t).RemoteActivityAt; at != nil {
		t.Fatalf("a phone that sent %v ago still stamps %v onto the row. The marker is a fact with "+
			"a time on it and it has to expire with the gate beside it; a row that went on saying "+
			"\"phone sent 09:41\" all afternoon would be reporting something anyControlled has "+
			"already stopped believing", phoneActiveHorizon, *at)
	}
	if r.sk.anyControlled(r.local) {
		t.Fatalf("anyControlled and the row disagree about the same expired record")
	}
}
