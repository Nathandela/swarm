package protocol

// WAVE R8 / CLOSING ROUND -- ADR-017 T8's SEVERANCE TRIGGERS, MADE TRUE AGAIN
// (closing review, finding 8).
//
// THE FINDING. Round 3 correctly moved generations to a SERVER-WIDE registry, because the
// gateway dials a fresh daemon connection per command and a connection-scoped generation was
// one no phone could ever use. But that move removed the connection binding that made a
// dropped transport sever immediately, and `severTerminalControl` was left with exactly two
// callers: the kill switch and device revocation. ADR-017 T8 lists DISCONNECT and SESSION
// REPLACEMENT as synchronous at the daemon as well, and no test asserted either. The system
// is fail-closed in effect -- a replaced instance is refused on every frame -- but "refused
// per frame" is not severance, and the ruling was false as written.
//
// WHAT THIS FILE FENCES, and what it deliberately does not:
//
//   - SESSION KILL and SESSION DELETE sever synchronously. Both are explicit daemon ops with
//     a handler, so there is nothing to observe and nothing to poll: the generation over that
//     session is gone when the reply is written. Fenced here.
//   - SESSION REPLACEMENT severs on the SERVER'S OWN CLOCK, in the sweep that already runs
//     for T6-c. The daemon has no notification seam that tells the protocol server an
//     incarnation was re-minted, so "synchronous at the instant of replacement" is not
//     buildable without one; what IS buildable, and what T6-c's sweep already provides, is
//     severance that never waits for a phone frame. Fenced here, and ADR-017 T8 is amended
//     to say exactly this rather than to keep claiming the stronger thing.
//   - DISCONNECT is amended out, not fenced. Under the per-command-connection gateway there
//     is no persistent phone connection on the control plane at all; the connection that
//     mints a generation is closed before the first byte is typed. The phone's liveness is
//     the KEEPALIVE clock (T8's own missing-keepalive wall) plus T8-b's phone-side direct
//     severance. See the amendment.

import (
	"testing"
	"time"
)

// r8r4Gen installs one live generation over local, signed by deviceID, bound to instance.
func r8r4Gen(t *testing.T, s *Server, local, deviceID, instance string) string {
	t.Helper()
	id := mintTerminalGeneration()
	gen := &terminalGeneration{
		id:        id,
		session:   local,
		instance:  instance,
		deviceID:  deviceID,
		horizon:   time.Now().Add(TerminalControlTTL),
		keepalive: time.Now().Add(TerminalKeepaliveTTL),
	}
	if !s.publishTerminalGenerationIfCurrent(s.termSeverGen.Load(), gen) {
		t.Fatalf("could not publish the test generation")
	}
	return id
}

// TestR8R4Sever_KillingASessionSeversItsControlGeneration is T8's session-kill trigger.
func TestR8R4Sever_KillingASessionSeversItsControlGeneration(t *testing.T) {
	s := &Server{}
	r8r4Gen(t, s, "sess1", "devA", "inst-1")
	other := r8r4Gen(t, s, "sess2", "devA", "inst-9")

	s.severTerminalControlForSession("sess1")

	if _, ok := s.terminalGenerationByID(other); !ok {
		t.Errorf("severing sess1 also dropped sess2's generation; severance is per SESSION")
	}
	if s.anyLiveTerminalGenerationFor("sess1") {
		t.Errorf("ADR-017 T8: a generation over a KILLED session is still live in the server's own "+
			"registry. `Refused on the next frame` is not severance: the phone may send nothing at "+
			"all, and a generation that outlives its session is one the next incarnation under the "+
			"same id inherits.")
	}
}

// TestR8R4Sever_AReplacedIncarnationIsSweptOnTheServersOwnClock is the replacement trigger,
// at the strength the daemon can actually provide.
func TestR8R4Sever_AReplacedIncarnationIsSweptOnTheServersOwnClock(t *testing.T) {
	backend := &r8r4CapBackend{records: map[string]SessionCapabilities{
		"sess1": {
			Provider: "opencode", ProviderVersion: "0.9", AdapterRevision: "rev",
			SessionInstance: "inst-1", TerminalFallback: true, TerminalControl: true,
		},
	}}
	s := &Server{d: backend, remoteTier: true}
	id := r8r4Gen(t, s, "sess1", "devA", "inst-1")

	// A sweep while the incarnation is UNCHANGED must not touch it, or the fence below
	// would pass on a sweep that drops everything.
	s.sweepTerminalControl()
	if _, ok := s.terminalGenerationByID(id); !ok {
		t.Fatalf("the sweep dropped a generation bound to the CURRENT incarnation")
	}

	// THE REPLACEMENT: same session id, a new incarnation.
	backend.set("sess1", SessionCapabilities{
		Provider: "opencode", ProviderVersion: "0.9", AdapterRevision: "rev",
		SessionInstance: "inst-2", TerminalFallback: true, TerminalControl: true,
	})
	s.sweepTerminalControl()
	if _, ok := s.terminalGenerationByID(id); ok {
		t.Errorf("ADR-017 T8/T8-a: a generation bound to incarnation inst-1 survived the sweep after "+
			"the session was REPLACED by inst-2 under the same id. The per-frame stale_instance "+
			"refusal is defence in depth, not severance -- it requires the phone to send something, "+
			"and the case severance exists for is the one where it never will.")
	}
}

// r8r4CapBackend is a DaemonAPI whose only interesting seam is the capability lookup the
// sweep's incarnation check reads. It embeds the package's own stub so that adding a method
// to DaemonAPI does not silently turn this fixture into a compile error the fence's meaning
// depends on.
type r8r4CapBackend struct {
	stubDaemon
	records map[string]SessionCapabilities
}

func (b *r8r4CapBackend) SessionCapabilities(local string) (SessionCapabilities, bool) {
	r, ok := b.records[local]
	return r, ok
}

func (b *r8r4CapBackend) set(local string, r SessionCapabilities) { b.records[local] = r }
