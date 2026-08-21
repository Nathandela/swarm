package skeleton

// WAVE R8 / SLICES S1 + S2 -- SESSION INSTANCE IDENTITY, AND CAPABILITY PUBLICATION.
// Failing-first (TDD RED, GG-5).
//
// TWO GAPS, ONE FILE, BECAUSE THE SECOND IS UNENFORCEABLE WITHOUT THE FIRST.
//
// (1) T8-a. ADR-017 binds the capability record, the control generation and every snapshot
// to "the session instance", and makes session replacement a synchronous severance trigger
// (T8's last row). The repository has no such identifier: `grep -rn "SessionInstance|
// InstanceID|instance_id" internal/ mobile/` finds five hits, every one of them a test name
// or a comment, and ZERO production identifiers. What exists is a session ID that survives a
// shim restart, a resume and a daemon restart -- so "a generation dies with its instance" is
// today a sentence with no referent, and a generation minted against one PTY authorises raw
// bytes into its replacement.
//
// (2) T2-a / OPEN-C0. `deriveSessionCapabilities` has NO PRODUCTION CALLER. capability.go
// says so at :334-344 -- "THIS FUNCTION IS DEAD CODE TODAY ... no live session has a
// capability record at all and the derivation below runs only under test" -- and
// `Daemon.sessionDegraded`'s comment says the same at :227-240. So T2 rule 3's "the phone
// renders from that record" has no record to render from, on any session, today. R8 cannot
// route three destinations off a record nothing authors.
//
// THE SEAMS (undefined symbols -> compile-fail RED):
//
//	func mintSessionInstance() string
//	func (d *Daemon) recordSessionInstance(sessionID, instance string) error
//	func (d *Daemon) sessionInstance(sessionID string) (string, bool)
//	func (d *Daemon) authorSessionCapabilities(sessionID, instance, provider string,
//	        a adapter.Adapter, providerVersion, adapterRevision string, liveBackend bool) (protocol.SessionCapabilities, error)
//
// `deriveSessionCapabilities` is REUSED AS IS, not rewritten: R7 landed its per-instance
// correction (capability.go:345-380) and R8 wires it. `authorSessionCapabilities` is the one
// authoring entry point -- mint or adopt the instance, derive, VALIDATE, register, persist --
// so that "every session-creation path authors a record" is a statement about one function
// with four call sites rather than four independent implementations of the same rule.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/adapter/opencode"
	"github.com/Nathandela/swarm/internal/protocol"
)

// TestR8Instance_IsMintedPerIncarnationAndIsNotDerivedFromTheSessionID is the mutation fence
// D-INSTANCE names: bind the generation to the session id instead of the instance and a
// replacement authorises a stale generation against a new PTY.
//
// The instance must therefore be UNGUESSABLE FROM the session id: if it is a function of the
// session id (a hash, a prefix, the id itself), then a replacement produces the same value
// and the binding is decorative.
func TestR8Instance_IsMintedPerIncarnationAndIsNotDerivedFromTheSessionID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		got := mintSessionInstance()
		if got == "" {
			t.Fatalf("mintSessionInstance returned the empty string; an empty instance binds nothing")
		}
		if seen[got] {
			t.Fatalf("mintSessionInstance repeated %q after %d mints. A repeated instance lets a "+
				"generation minted against one incarnation authorise bytes into another.", got, i)
		}
		seen[got] = true
	}
}

// TestR8Instance_SurvivesADaemonRestartButNotAReplacement is the distinction the whole rule
// turns on, and it is the one an implementation is most likely to collapse.
//
//   - A DAEMON RESTART re-adopts the SAME incarnation: the shim is still running, the PTY is
//     the same PTY, and the phone's view must not reset. The instance is read back from the
//     session's own directory.
//   - A SESSION REPLACEMENT is a new incarnation: new shim, new PTY. The instance changes, and
//     T4-a requires that the phone meets it as an EPOCH RESET WITH A CHANGED INSTANCE rather
//     than as a seamless continuation -- which is what makes the watcher's silent supervised
//     reconnect (terminal_watcher.go:149-165) safe to keep.
func TestR8Instance_SurvivesADaemonRestartButNotAReplacement(t *testing.T) {
	dir := r8StateDir(t)
	const sessionID = "sess-r8-instance"

	d1 := assembleAt(t, dir)
	// ROUND 3: the incarnation the instance is minted for is the SHIM's pid, so a daemon
	// restart (same pid) adopts and a replacement (new pid) re-mints. It is carried here
	// rather than left at zero because zero means UNKNOWN, and an unknown incarnation adopts
	// unconditionally -- which would make the replacement arm below vacuous.
	const firstShim, replacementShim = 4242, 9999
	first := mintSessionInstance()
	if err := d1.recordSessionInstance(sessionID, first, firstShim); err != nil {
		t.Fatalf("recordSessionInstance: %v", err)
	}
	if got, ok := d1.sessionInstance(sessionID); !ok || got != first {
		t.Fatalf("sessionInstance = (%q, %v); want (%q, true)", got, ok, first)
	}

	// A SECOND daemon incarnation over the same state dir: the shim did not restart, so the
	// instance is the same one. The first is CLOSED first, which is what a daemon restart
	// is -- and it also makes the assertion stronger, because the second incarnation holds
	// no in-memory cache and must read the instance back off disk.
	if err := d1.Close(); err != nil {
		t.Fatalf("close the first daemon incarnation: %v", err)
	}
	d2 := assembleAt(t, dir)
	got, ok := d2.sessionInstance(sessionID)
	if !ok {
		t.Fatalf("ADR-017 T8-a: a fresh daemon over the same state dir found no session instance. " +
			"The instance is minted at shim spawn and lives in the session's own 0700 dir beside " +
			"meta.json / shim-launch.json / capabilities.json; a daemon restart re-adopts it, it does " +
			"not re-mint it -- re-minting would reset the phone's view on every daemon upgrade.")
	}
	if got != first {
		t.Errorf("ADR-017 T8-a: a daemon restart changed the session instance (%q -> %q). The shim did "+
			"not restart, so the phone must not see an epoch reset.", first, got)
	}

	// A REPLACEMENT: a new incarnation of the same session id.
	// ROUND 3 STRENGTHENING: the replacement arm is reached through the PRODUCTION resolver,
	// driven with a different shim, rather than by the test calling the recorder. Round 2
	// exercised the helper and left the production call site unfenced -- the wave's own
	// defect class (5), verbatim.
	second, err := d2.adoptOrMintSessionInstance(sessionID, replacementShim)
	if err != nil {
		t.Fatalf("adoptOrMintSessionInstance (replacement): %v", err)
	}
	if second == first {
		t.Fatalf("ADR-017 T8: a REPLACEMENT shim kept the previous instance %q", first)
	}
	if got, _ := d2.sessionInstance(sessionID); got != second {
		t.Errorf("ADR-017 T8: a session replacement must publish the NEW instance (%q), got %q. A "+
			"generation bound to the old instance is what must now be refused.", second, got)
	}
}

// TestR8Instance_IsStoredInTheSessionsOwn0700Dir pins WHERE, because the location is the
// lifetime: capability.go's own reasoning for the record ("the side-file has the session's
// own lifetime by construction", :70-75) applies identically to the instance, and a per-
// daemon or per-process store would hand a restarted daemon no way to tell adoption from
// replacement.
func TestR8Instance_IsStoredInTheSessionsOwn0700Dir(t *testing.T) {
	dir := r8StateDir(t)
	const sessionID = "sess-r8-loc"
	d := assembleAt(t, dir)
	if err := d.recordSessionInstance(sessionID, mintSessionInstance(), 1234); err != nil {
		t.Fatalf("recordSessionInstance: %v", err)
	}
	sessionDir := filepath.Join(dir, sessionID)
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		t.Fatalf("ADR-017 T8-a: the session instance was not written under the session's own dir %s: %v",
			sessionDir, err)
	}
	var found bool
	for _, e := range entries {
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		if strings.Contains(e.Name(), "instance") {
			found = true
			if perm := info.Mode().Perm(); perm&0o077 != 0 {
				t.Errorf("%s is mode %v; daemon-authored session state is 0600 inside a 0700 dir",
					e.Name(), perm)
			}
		}
	}
	if !found {
		t.Errorf("ADR-017 T8-a: no instance file in %s (%v). It is the sibling of capabilities.json and "+
			"shares the session's lifetime, so it is retired with the session dir itself.", sessionDir, entries)
	}
}

// TestR8Publication_AuthoringValidatesAndRefusesAnInconsistentRecord is amendment T2-b at the
// AUTHORING seam, the first of its three.
//
// Validation at the author is what makes the other two seams a defence in depth rather than
// the only defence: a record that never existed in the inconsistent state cannot be decoded
// out of a stale capabilities.json a year later.
func TestR8Publication_AuthoringValidatesAndRefusesAnInconsistentRecord(t *testing.T) {
	d := assembleAt(t, r8StateDir(t))
	const sessionID = "sess-r8-invalid"

	// No instance recorded -> the record cannot bind a generation -> authoring must refuse,
	// and must NOT leave a partial record behind.
	if _, err := d.authorSessionCapabilities(sessionID, "", "opencode", opencode.New(), "1.2.3", "rev", false); err == nil {
		t.Errorf("ADR-017 T8-a: authoring a capability record with no session instance succeeded. Every " +
			"generation, every snapshot and the record itself bind to the instance; a record with none " +
			"is a router with nothing to route.")
	}
	if _, ok := d.sessionCapabilities(sessionID); ok {
		t.Errorf("a refused authoring left a record behind. A partially-authored record is exactly the " +
			"inconsistent state T2-b makes unrepresentable.")
	}
}

// TestR8Publication_AStructuredAdapterGetsNoFallbackAndNoControl is T2 rule 4 asserted at the
// AUTHORING seam rather than at the derivation, which is the level the router reads.
//
// `capability_test.go`'s two existing fences stay exactly as they are: this does not replace
// them, it adds the layer above them, because a derivation that is right and an authoring
// path that overrides it would leave both green and the phone wrong.
func TestR8Publication_AStructuredAdapterGetsNoFallbackAndNoControl(t *testing.T) {
	d := assembleAt(t, r8StateDir(t))
	inst := mintSessionInstance()

	rec, err := d.authorSessionCapabilities("sess-claude", inst, "claude", claude.New(), "1.0.0", "rev", false)
	if err != nil {
		t.Fatalf("authorSessionCapabilities(claude): %v", err)
	}
	if !rec.StructuredChat {
		t.Fatalf("claude must author structured_chat=true, or the wave's exit is vacuous")
	}
	if rec.TerminalFallback {
		t.Errorf("ADR-017 T2 rule 4: a healthy structured session was authored terminal_fallback=true. " +
			"There is no route to the fallback from a healthy structured session -- no power-user escape " +
			"hatch, no long-press, no debug toggle in a release build.")
	}
	if rec.TerminalControl {
		t.Errorf("ADR-017 T6: a structured session was authored terminal_control=true")
	}
	if rec.SessionInstance != inst {
		t.Errorf("the authored record must carry the instance it was authored for: got %q want %q",
			rec.SessionInstance, inst)
	}

	fb, err := d.authorSessionCapabilities("sess-opencode", mintSessionInstance(), "opencode", opencode.New(), "0.9", "rev", false)
	if err != nil {
		t.Fatalf("authorSessionCapabilities(opencode): %v", err)
	}
	if !fb.TerminalFallback || fb.StructuredChat {
		t.Errorf("playbook:649 / RC-D4: opencode ships in R8 as a terminal_fallback provider; got %+v", fb)
	}
	if !fb.TerminalControl {
		t.Errorf("ADR-017 T6-b: terminal_control must be authored TRUE AT LAUNCH for a provider whose " +
			"fallback was not produced by a degrade. Authoring it false everywhere would make R8b's " +
			"control half unreachable; deriving it on the phone from terminal_fallback would grant it to " +
			"the degraded sessions T6-b withholds it from.")
	}
}

// TestR8Publication_ReRegistrationAfterADegradeCannotReGrantControl is D-DEGRADE-ORIGIN's
// SECOND fence, and it is the one the existing merge is one line away from failing.
//
// `registerSessionCapabilities` (capability.go:103-125) merges an incoming record over an
// existing one and re-derives the structured/fallback pair from `SetStructuredChat` alone,
// letting every OTHER field of the incoming record win so that "provider_version,
// adapter_revision, ... can still refresh". `terminal_control` is one of those other fields.
// A daemon-restart reconcile re-registers every reconnected session -- which is precisely
// what that merge exists for -- so an unchanged reconcile silently re-grants control over a
// session that proved a structured gap.
func TestR8Publication_ReRegistrationAfterADegradeCannotReGrantControl(t *testing.T) {
	dir := r8StateDir(t)
	const sessionID = "sess-r8-degrade"
	d := assembleAt(t, dir)
	inst := mintSessionInstance()
	if err := d.recordSessionInstance(sessionID, inst, 1234); err != nil {
		t.Fatalf("recordSessionInstance: %v", err)
	}

	// A healthy Claude session, then a proven structured gap.
	if _, err := d.authorSessionCapabilities(sessionID, inst, "claude", claude.New(), "1.0.0", "rev", false); err != nil {
		t.Fatalf("author: %v", err)
	}
	d.markSessionDegraded(sessionID)
	degraded, ok := d.sessionCapabilities(sessionID)
	if !ok {
		t.Fatalf("no record after degrade")
	}
	if degraded.StructuredChat || !degraded.TerminalFallback {
		t.Fatalf("a degrade must produce {structured:false, fallback:true}; got %+v", degraded)
	}
	if degraded.TerminalControl {
		t.Errorf("ADR-017 T6-b: the degrade granted CONTROL. A degraded Claude session still has a live " +
			"TUI whose input region is uncharacterised (the expected_input_revision gap ADR-017 T9 " +
			"discloses as open), so raw bytes there concatenate onto an owner's half-typed line.")
	}

	// The reconcile: a fresh daemon re-registers what looks like a healthy record. The
	// first incarnation is closed first, which is what a daemon restart is -- and it means
	// the merge below runs against the record read back OFF DISK, not a warm cache.
	if err := d.Close(); err != nil {
		t.Fatalf("close the first daemon incarnation: %v", err)
	}
	d2 := assembleAt(t, dir)
	d2.registerSessionCapabilities(sessionID, protocol.SessionCapabilities{
		Provider: "claude", ProviderVersion: "1.0.1", AdapterRevision: "rev2",
		SessionInstance: inst, StructuredChat: true, TerminalControl: true,
	})
	after, ok := d2.sessionCapabilities(sessionID)
	if !ok {
		t.Fatalf("no record after re-registration")
	}
	if after.StructuredChat {
		t.Errorf("ADR-017 T2 rule 2: a re-registration resurrected structured_chat over a proven gap")
	}
	if after.TerminalControl {
		t.Errorf("D-DEGRADE-ORIGIN fence 2: a re-registration after a degrade RE-GRANTED terminal_control. " +
			"The merge lets every field other than the structured/fallback pair refresh, and a daemon " +
			"restart's reconcile re-registers every reconnected session -- so this is the normal path, " +
			"not an edge case.")
	}
	if after.ProviderVersion != "1.0.1" {
		t.Errorf("the merge must still let non-authority fields refresh; provider_version = %q", after.ProviderVersion)
	}
}

// TestR8Publication_EverySessionCreationPathAuthorsARecord is D-NIL's enumeration, and it is
// a SOURCE-LEVEL gate on purpose: the obligation is "no creation path is left without one",
// which is a statement about the set of call sites and cannot be observed from any single
// call. It is written so it CANNOT be satisfied by the declaration itself (rule 4): the
// declaring file is excluded, and the gate demands a call in each named path.
//
// The five paths, per D-NIL:
//  1. the owner-tier TUI launch;
//  2. the R5 remote session_launch;
//  3. the daemon-restart reconcile's re-adoption;
//  4. resume / re-attach of an existing session dir;
//  5. anything else -> nil, and therefore the status card, which is the honest answer rather
//     than a hole. That fifth row is what the fail-closed default in schema/r8_record_test.go
//     covers, and it is why this gate's failure is a defect the user is CONTAINED from rather
//     than exposed to.
func TestR8Publication_EverySessionCreationPathAuthorsARecord(t *testing.T) {
	call := regexp.MustCompile(`authorSessionCapabilities\s*\(`)
	paths := map[string]string{
		"api.go":        "the owner-tier launch/resume seam (coreAPI.Launch -> composeLaunchSpec)",
		"serve.go":      "daemon assembly and the restart reconcile that re-adopts live sessions",
		"backend.go":    "the backend-connect path R7 made structured chat depend on",
		"sessiontap.go": "the tap/attach path a resumed session reaches the daemon through",
		"capability.go": "the declaring file -- excluded, see below",
	}
	var authored int
	for name, why := range paths {
		if name == "capability.go" {
			continue // the declaration is not a call: rule 4
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if call.Match(b) {
			authored++
			continue
		}
		t.Errorf("ADR-017 T2-a / D-NIL: %s (%s) never calls authorSessionCapabilities. Every "+
			"session-creation path authors a record; a path that does not leaves the phone with a nil "+
			"record, which by T2-a is the honest status card -- so the user is contained, and the "+
			"session is silently unroutable to the surface it earned.", name, why)
	}
	if authored == 0 {
		t.Errorf("no production path in internal/skeleton authors a capability record at all, which is " +
			"the state capability.go:334-344 discloses today: deriveSessionCapabilities is dead code and " +
			"no live session has a record. R8's routing has nothing to route on until this changes.")
	}
}

// r8StateDir is a SHORT state dir. assembleAt binds a UNIX socket inside it, and macOS caps
// a sockaddr_un path near 104 bytes -- t.TempDir()'s per-test path blows through that and
// Serve fails with "bind: invalid argument" before any assertion runs. Every other suite in
// this package that assembles a real daemon does the same thing (convscan_test.go,
// killswitch_state_test.go, r6_fixpack_test.go).
func r8StateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "swsk-r8")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
