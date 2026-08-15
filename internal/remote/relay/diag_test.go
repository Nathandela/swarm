package relay

// R2-B: `swarm relay doctor` (playbook 4.1/6.5, R2 hggx.3.2) FAILING-FIRST
// tests (TDD RED, GG-5) for the ephemeral diagnostic capability: an
// operator-minted, short-lived (<= 5 min), single-use credential that unlocks
// exactly one scoped op family -- create/use/delete ONE ephemeral diagnostic
// route on the caller's OWN authenticated connection. None of
// MintDiagnosticCapability, Client.DiagOpen/DiagAppend/DiagRead/DiagClose,
// Server.diagUsedNonces, or the "diag_*" wire ops exist yet: this file does
// not compile until they do.
//
// DESIGN INVARIANT UNDER TEST THROUGHOUT: the public protocol gains NO
// privileged UNAUTHENTICATED endpoint. Every diag_* op requires the SAME
// auth_init/auth_resp handshake every other op does (requireAuth); what makes
// it "diagnostic" is that the capability -- mintable only by whoever holds
// the relay's operator secret -- is what unlocks it, not a new dial path.

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// diagOperatorSecret is a fixture operator secret: fixed so failures are
// reproducible, long enough to be a plausible EnsureOperatorSecret output.
var diagOperatorSecret = []byte("this-is-a-fixture-operator-secret-not-a-real-one")

// startDiagTestRelay is startTestRelayOpts with the operator secret installed,
// which is what every diag_* test needs to reach past "diagnostics_disabled".
// extra lets a test (e.g. the storage-status ones) layer on further Options
// such as WithDiskFreeFunc without duplicating this fixture.
func startDiagTestRelay(t *testing.T, extra ...Option) (*Server, *fakeClock) {
	t.Helper()
	opts := append([]Option{WithOperatorSecret(diagOperatorSecret)}, extra...)
	srv, _, _, clk := startTestRelayOpts(t, nil, opts...)
	return srv, clk
}

// --- pure capability mint/parse tests (no server) --------------------------

// diagFixtureRID returns a throwaway, syntactically-valid routing id for
// tests that exercise the capability's SHAPE/MAC only and do not care which
// identity it ends up bound to.
func diagFixtureRID(t *testing.T) string {
	t.Helper()
	pub, _ := newRelayAuthKey(t)
	return RoutingID(pub)
}

func TestDiagCapability_MintThenParseRoundTrips(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	rid := diagFixtureRID(t)
	token, err := MintDiagnosticCapability(diagOperatorSecret, now, rid)
	if err != nil {
		t.Fatalf("MintDiagnosticCapability: %v", err)
	}
	nonce, issuedAt, gotRID, err := parseDiagCap(diagOperatorSecret, token)
	if err != nil {
		t.Fatalf("parseDiagCap on a freshly minted capability: %v", err)
	}
	if nonce == "" {
		t.Fatalf("parseDiagCap returned an empty nonce")
	}
	if !issuedAt.Equal(now) {
		t.Fatalf("parseDiagCap issuedAt = %v, want %v", issuedAt, now)
	}
	if gotRID != rid {
		t.Fatalf("parseDiagCap rid = %q, want %q", gotRID, rid)
	}
}

func TestDiagCapability_MintingWithNoSecretIsRefused(t *testing.T) {
	if _, err := MintDiagnosticCapability(nil, time.Now(), diagFixtureRID(t)); !errors.Is(err, ErrDiagnosticsDisabled) {
		t.Fatalf("MintDiagnosticCapability(nil secret): got %v, want ErrDiagnosticsDisabled", err)
	}
}

func TestDiagCapability_WrongSecretIsRejected(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	token, err := MintDiagnosticCapability(diagOperatorSecret, now, diagFixtureRID(t))
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, _, _, err := parseDiagCap([]byte("a different operator secret entirely"), token); !errors.Is(err, ErrDiagnosticCapabilityInvalid) {
		t.Fatalf("parseDiagCap under the WRONG secret: got %v, want ErrDiagnosticCapabilityInvalid", err)
	}
}

func TestDiagCapability_TamperedBytesAreRejected(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	token, err := MintDiagnosticCapability(diagOperatorSecret, now, diagFixtureRID(t))
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	tampered := append([]byte(nil), token...)
	tampered[len(tampered)-1] ^= 0xFF // flip a MAC byte
	if _, _, _, err := parseDiagCap(diagOperatorSecret, tampered); !errors.Is(err, ErrDiagnosticCapabilityInvalid) {
		t.Fatalf("parseDiagCap on a tampered capability: got %v, want ErrDiagnosticCapabilityInvalid", err)
	}
}

// --- wire-level: the happy path ---------------------------------------------

// TestDiag_RoundTripOverAnAuthenticatedConnection is the whole feature end to
// end: mint, open, append, read back, close -- and confirms the route is dead
// afterward, exactly as playbook 6.5 describes the doctor's own sequence.
func TestDiag_RoundTripOverAnAuthenticatedConnection(t *testing.T) {
	srv, clk := startDiagTestRelay(t)
	ctx := testCtx(t)
	party := newB25Party(t, srv)

	token, err := MintDiagnosticCapability(diagOperatorSecret, clk.Now(), party.rid)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if err := party.cl.DiagOpen(ctx, token); err != nil {
		t.Fatalf("DiagOpen with a fresh, valid capability: %v", err)
	}

	payload := []byte("opaque ciphertext the relay must never interpret")
	if err := party.cl.DiagAppend(ctx, payload); err != nil {
		t.Fatalf("DiagAppend: %v", err)
	}
	items, err := party.cl.DiagRead(ctx)
	if err != nil {
		t.Fatalf("DiagRead: %v", err)
	}
	if len(items) != 1 || string(items[0].Envelope) != string(payload) {
		t.Fatalf("DiagRead = %v, want exactly the one appended envelope %q", items, payload)
	}

	if err := party.cl.DiagClose(ctx); err != nil {
		t.Fatalf("DiagClose: %v", err)
	}
	if err := party.cl.DiagAppend(ctx, payload); !errors.Is(err, ErrDiagRouteNotOpen) {
		t.Fatalf("DiagAppend after DiagClose: got %v, want ErrDiagRouteNotOpen (the route is gone)", err)
	}
	if _, err := party.cl.DiagRead(ctx); !errors.Is(err, ErrDiagRouteNotOpen) {
		t.Fatalf("DiagRead after DiagClose: got %v, want ErrDiagRouteNotOpen", err)
	}
}

// TestDiag_StatusRequiresAnAuthenticatedConnection mirrors
// TestDiag_RequiresAnAuthenticatedConnection for the new diag_status op: it
// must never join the unauthenticated rendezvous ops.
func TestDiag_StatusRequiresAnAuthenticatedConnection(t *testing.T) {
	srv, _ := startDiagTestRelay(t)
	conn := dialRaw(t, srv.URL())
	if err := conn.WriteMsg(MsgRelay, mustJSON(t, map[string]any{"op": "diag_status"})); err != nil {
		t.Fatalf("write diag_status on a raw connection: %v", err)
	}
	tag, payload, err := conn.ReadMsg()
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if tag != MsgError {
		t.Fatalf("diag_status on an UNAUTHENTICATED connection got tag %v (payload %s), want MsgError", tag, payload)
	}
}

func TestDiag_RequiresAnAuthenticatedConnection(t *testing.T) {
	srv, clk := startDiagTestRelay(t)
	token, err := MintDiagnosticCapability(diagOperatorSecret, clk.Now(), diagFixtureRID(t))
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	// A raw, unauthenticated connection (no auth_init/auth_resp) is the shape
	// the pairing rendezvous ops alone are allowed to use. diag_open must NOT
	// join them -- it is presented over the EXISTING AUTHENTICATED surface, per
	// design, never a new unauthenticated one.
	conn := dialRaw(t, srv.URL())
	if err := conn.WriteMsg(MsgRelay, mustJSON(t, map[string]any{"op": "diag_open", "capability": token})); err != nil {
		t.Fatalf("write diag_open on a raw connection: %v", err)
	}
	tag, payload, err := conn.ReadMsg()
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if tag != MsgError {
		t.Fatalf("diag_open on an UNAUTHENTICATED connection got tag %v (payload %s), want MsgError", tag, payload)
	}
}

// --- capability adversarial mechanics ---------------------------------------

func TestDiag_DisabledWhenNoOperatorSecretConfigured(t *testing.T) {
	srv, _, _, clk := startTestRelay(t, nil) // no WithOperatorSecret at all
	ctx := testCtx(t)
	party := newB25Party(t, srv)

	// Even a capability that WOULD be valid against some secret must be refused:
	// there is no operator secret configured, so nothing can ever verify here.
	token, err := MintDiagnosticCapability(diagOperatorSecret, clk.Now(), party.rid)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if err := party.cl.DiagOpen(ctx, token); !errors.Is(err, ErrDiagnosticsDisabled) {
		t.Fatalf("DiagOpen with diagnostics never configured: got %v, want ErrDiagnosticsDisabled", err)
	}
}

func TestDiag_CapabilityMintedWithTheWrongOperatorSecretIsRefused(t *testing.T) {
	srv, clk := startDiagTestRelay(t) // configured with diagOperatorSecret
	ctx := testCtx(t)
	party := newB25Party(t, srv)

	token, err := MintDiagnosticCapability([]byte("an impostor's own secret, not the relay's"), clk.Now(), party.rid)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if err := party.cl.DiagOpen(ctx, token); !errors.Is(err, ErrDiagnosticCapabilityInvalid) {
		t.Fatalf("DiagOpen with a capability minted under the WRONG secret: got %v, want ErrDiagnosticCapabilityInvalid", err)
	}
}

func TestDiag_ExpiredCapabilityIsRefused(t *testing.T) {
	srv, clk := startDiagTestRelay(t)
	ctx := testCtx(t)
	party := newB25Party(t, srv)

	token, err := MintDiagnosticCapability(diagOperatorSecret, clk.Now(), party.rid)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	clk.Advance(DiagnosticCapabilityTTL + time.Second)
	if err := party.cl.DiagOpen(ctx, token); !errors.Is(err, ErrDiagnosticCapabilityInvalid) {
		t.Fatalf("DiagOpen past DiagnosticCapabilityTTL: got %v, want ErrDiagnosticCapabilityInvalid", err)
	}
}

// TestDiag_CapabilityIsSingleUse presents the identical capability bytes
// twice from the SAME identity (a second connection always takes over a
// routing id's live session -- lifecycle_test.go
// TestRelay_DuplicateConnectionResolved) so the failure can only be
// single-use enforcement, never the separate identity-binding check
// (TestDiag_CapabilityBoundToADifferentIdentityIsRefused, below) tripping
// for an unrelated reason.
func TestDiag_CapabilityIsSingleUse(t *testing.T) {
	srv, clk := startDiagTestRelay(t)
	ctx := testCtx(t)
	pub, priv := newRelayAuthKey(t)
	rid := RoutingID(pub)

	token, err := MintDiagnosticCapability(diagOperatorSecret, clk.Now(), rid)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	first := dialAuthed(t, srv.URL(), authFor(pub, priv))
	if err := first.DiagOpen(ctx, token); err != nil {
		t.Fatalf("first DiagOpen with a fresh capability: %v", err)
	}
	// A second connection, authenticated as the SAME identity, presenting the
	// IDENTICAL bytes -- still well inside the TTL, still MAC-valid, still
	// bound to the right rid -- must be refused: "single-use" means spent by
	// the first successful open, not merely by expiry.
	second := dialAuthed(t, srv.URL(), authFor(pub, priv))
	if err := second.DiagOpen(ctx, token); !errors.Is(err, ErrDiagnosticCapabilityInvalid) {
		t.Fatalf("DiagOpen replaying an ALREADY-SPENT capability from the same identity: got %v, want ErrDiagnosticCapabilityInvalid", err)
	}
}

// TestDiag_CapabilityBoundToADifferentIdentityIsRefused is the R2 review LOW
// (design) fix: a capability minted for alice's routing id must be refused
// when presented by bob's authenticated connection, even though the bytes are
// unexpired, unspent, and MAC-valid under the right secret. Without this, ANY
// endpoint the capability is ever shown to (a typo'd URL, a hijacked DNS
// record) could replay it against the REAL relay under a throwaway identity
// of its own choosing, inside the TTL -- the review's own probe.
func TestDiag_CapabilityBoundToADifferentIdentityIsRefused(t *testing.T) {
	srv, clk := startDiagTestRelay(t)
	ctx := testCtx(t)
	alice := newB25Party(t, srv)
	bob := newB25Party(t, srv)

	token, err := MintDiagnosticCapability(diagOperatorSecret, clk.Now(), alice.rid)
	if err != nil {
		t.Fatalf("mint for alice: %v", err)
	}
	if err := bob.cl.DiagOpen(ctx, token); !errors.Is(err, ErrDiagnosticCapabilityInvalid) {
		t.Fatalf("bob presenting a capability minted for alice's rid: got %v, want ErrDiagnosticCapabilityInvalid", err)
	}
	// The wrong-identity attempt above must not have spent it: the rightful
	// holder can still use it.
	if err := alice.cl.DiagOpen(ctx, token); err != nil {
		t.Fatalf("alice presenting her OWN capability after bob's refused attempt: %v", err)
	}
}

// TestDiag_RouteOpsAreRefusedOnceTheCapabilityTTLPasses is the R2 review
// LOW-MEDIUM fix, reproducing the review's own probe: DiagnosticCapabilityTTL
// previously bounded only diag_open -- once a route was open, diag_status
// stayed a live storage-health oracle (and diag_append/diag_read a live ~1
// MiB scratch buffer) for the rest of the connection, no matter how far past
// the capability's TTL the clock moved.
func TestDiag_RouteOpsAreRefusedOnceTheCapabilityTTLPasses(t *testing.T) {
	srv, clk := startDiagTestRelay(t)
	ctx := testCtx(t)
	party := newB25Party(t, srv)

	token, err := MintDiagnosticCapability(diagOperatorSecret, clk.Now(), party.rid)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if err := party.cl.DiagOpen(ctx, token); err != nil {
		t.Fatalf("DiagOpen: %v", err)
	}

	// Mirrors the review's own probe: 100x the TTL, then confirm the ROUTE --
	// not just a fresh diag_open -- is now refused.
	clk.Advance(100 * DiagnosticCapabilityTTL)

	if _, err := party.cl.DiagStatus(ctx); !errors.Is(err, ErrDiagRouteNotOpen) {
		t.Fatalf("DiagStatus long past the capability's TTL: got %v, want ErrDiagRouteNotOpen", err)
	}
	if err := party.cl.DiagAppend(ctx, []byte("x")); !errors.Is(err, ErrDiagRouteNotOpen) {
		t.Fatalf("DiagAppend long past the capability's TTL: got %v, want ErrDiagRouteNotOpen", err)
	}
	if _, err := party.cl.DiagRead(ctx); !errors.Is(err, ErrDiagRouteNotOpen) {
		t.Fatalf("DiagRead long past the capability's TTL: got %v, want ErrDiagRouteNotOpen", err)
	}
	if err := party.cl.DiagClose(ctx); !errors.Is(err, ErrDiagRouteNotOpen) {
		t.Fatalf("DiagClose long past the capability's TTL: got %v, want ErrDiagRouteNotOpen", err)
	}
}

// TestDiag_RouteExpiresRelativeToCapabilityIssuedAtNotOpenTime guards the
// exact anchor the fix must use: sc.diagExpiresAt is issuedAt +
// DiagnosticCapabilityTTL, not now (open time) + DiagnosticCapabilityTTL. A
// capability opened near the end of its own window must not grant a fresh
// TTL's worth of route lifetime just because diag_open happened to run late.
func TestDiag_RouteExpiresRelativeToCapabilityIssuedAtNotOpenTime(t *testing.T) {
	srv, clk := startDiagTestRelay(t)
	ctx := testCtx(t)
	party := newB25Party(t, srv)

	issuedAt := clk.Now()
	token, err := MintDiagnosticCapability(diagOperatorSecret, issuedAt, party.rid)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	// Open with 1s left on the capability's own TTL window.
	clk.Advance(DiagnosticCapabilityTTL - time.Second)
	if err := party.cl.DiagOpen(ctx, token); err != nil {
		t.Fatalf("DiagOpen with 1s left on the capability's TTL: %v", err)
	}
	// If the route's expiry were anchored to THIS moment instead of issuedAt,
	// it would still have almost a full DiagnosticCapabilityTTL left. It does not.
	clk.Advance(2 * time.Second)
	if _, err := party.cl.DiagStatus(ctx); !errors.Is(err, ErrDiagRouteNotOpen) {
		t.Fatalf("DiagStatus 2s after opening with 1s left on the capability's TTL: got %v, want ErrDiagRouteNotOpen (route expiry must track issuedAt, not open time)", err)
	}
}

// --- adversarial: never a real mailbox, never enumerable --------------------

// TestDiag_CannotReadARealMailboxThroughDiagRead is adversarial fence (1): the
// diagnostic surface must never surface real mailbox content, even for the
// caller's OWN identity and even though B61 already refuses self-pairing.
func TestDiag_CannotReadARealMailboxThroughDiagRead(t *testing.T) {
	srv, clk := startDiagTestRelay(t)
	ctx := testCtx(t)

	machine, phone := b25LegitPair(t, srv, clk) // phone's REAL mailbox now holds "m->p"
	_ = machine

	token, err := MintDiagnosticCapability(diagOperatorSecret, clk.Now(), phone.rid)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if err := phone.cl.DiagOpen(ctx, token); err != nil {
		t.Fatalf("DiagOpen: %v", err)
	}
	items, err := phone.cl.DiagRead(ctx)
	if err != nil {
		t.Fatalf("DiagRead: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("DiagRead on a FRESHLY OPENED diagnostic route returned %d items, want 0.\n"+
			"  This connection's REAL mailbox holds a legitimately delivered item -- if diag_read can "+
			"see it, the diagnostic surface is not the separate ephemeral store the design requires, "+
			"it is a second name for the real one.", len(items))
	}

	// And the reverse: what diag_append writes must not land in the REAL
	// mailbox mailbox_read serves.
	if err := phone.cl.DiagAppend(ctx, []byte("diagnostic-only bytes")); err != nil {
		t.Fatalf("DiagAppend: %v", err)
	}
	realItems, err := phone.cl.MailboxRead(ctx, 0)
	if err != nil {
		t.Fatalf("MailboxRead (the real mailbox): %v", err)
	}
	for _, it := range realItems {
		if string(it.Envelope) == "diagnostic-only bytes" {
			t.Fatalf("a diag_append landed in the REAL mailbox mailbox_read serves: %v", realItems)
		}
	}
}

// TestDiag_RouteOpsAreRefusedWithoutAnOpenCapability is adversarial fence (2):
// an authenticated caller that never presented a valid capability gets NOTHING
// from the diag_* surface -- no state to probe, nothing to enumerate, no
// distinguishable "does a route exist" oracle.
func TestDiag_RouteOpsAreRefusedWithoutAnOpenCapability(t *testing.T) {
	srv, _ := startDiagTestRelay(t)
	ctx := testCtx(t)
	party := newB25Party(t, srv)

	if err := party.cl.DiagAppend(ctx, []byte("x")); !errors.Is(err, ErrDiagRouteNotOpen) {
		t.Fatalf("DiagAppend with no diag_open ever called: got %v, want ErrDiagRouteNotOpen", err)
	}
	if _, err := party.cl.DiagRead(ctx); !errors.Is(err, ErrDiagRouteNotOpen) {
		t.Fatalf("DiagRead with no diag_open ever called: got %v, want ErrDiagRouteNotOpen", err)
	}
	if err := party.cl.DiagClose(ctx); !errors.Is(err, ErrDiagRouteNotOpen) {
		t.Fatalf("DiagClose with no diag_open ever called: got %v, want ErrDiagRouteNotOpen", err)
	}
	if _, err := party.cl.DiagStatus(ctx); !errors.Is(err, ErrDiagRouteNotOpen) {
		t.Fatalf("DiagStatus with no diag_open ever called: got %v, want ErrDiagRouteNotOpen", err)
	}
}

// TestDiag_RoutesAreIsolatedPerConnection is adversarial fence (2)'s other
// half: two DIFFERENT capability holders, each with their own diagnostic
// route, cannot see one another's -- there is no id or target field anywhere
// in the diag_* wire shape that could name someone else's route, and this
// proves the server-side state actually honours that.
func TestDiag_RoutesAreIsolatedPerConnection(t *testing.T) {
	srv, clk := startDiagTestRelay(t)
	ctx := testCtx(t)
	alice := newB25Party(t, srv)
	bob := newB25Party(t, srv)

	tokenA, err := MintDiagnosticCapability(diagOperatorSecret, clk.Now(), alice.rid)
	if err != nil {
		t.Fatalf("mint A: %v", err)
	}
	if err := alice.cl.DiagOpen(ctx, tokenA); err != nil {
		t.Fatalf("alice DiagOpen: %v", err)
	}
	if err := alice.cl.DiagAppend(ctx, []byte("alice's diagnostic bytes")); err != nil {
		t.Fatalf("alice DiagAppend: %v", err)
	}

	tokenB, err := MintDiagnosticCapability(diagOperatorSecret, clk.Now(), bob.rid)
	if err != nil {
		t.Fatalf("mint B: %v", err)
	}
	if err := bob.cl.DiagOpen(ctx, tokenB); err != nil {
		t.Fatalf("bob DiagOpen: %v", err)
	}
	items, err := bob.cl.DiagRead(ctx)
	if err != nil {
		t.Fatalf("bob DiagRead: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("bob's diag_read saw %d item(s) from a route he never wrote to -- alice's route leaked "+
			"across connections: %v", len(items), items)
	}
}

// TestDiag_AppendCountIsBounded guards the ephemeral store itself: the
// diagnostic route is for a handful of round-trip bytes, not unbounded
// storage riding an operator capability.
func TestDiag_AppendCountIsBounded(t *testing.T) {
	srv, clk := startDiagTestRelay(t)
	ctx := testCtx(t)
	party := newB25Party(t, srv)

	token, err := MintDiagnosticCapability(diagOperatorSecret, clk.Now(), party.rid)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if err := party.cl.DiagOpen(ctx, token); err != nil {
		t.Fatalf("DiagOpen: %v", err)
	}
	var lastErr error
	for i := 0; i < maxDiagItems+1; i++ {
		if lastErr = party.cl.DiagAppend(ctx, []byte("x")); lastErr != nil {
			break
		}
	}
	if !errors.Is(lastErr, ErrQuotaExceeded) {
		t.Fatalf("appending past maxDiagItems (%d): got %v, want ErrQuotaExceeded", maxDiagItems, lastErr)
	}
}

// TestDiag_AppendRefusesWhenTotalBytesWouldExceedThePageBudget is the R2
// review MEDIUM fix: handleDiagRead returns EVERY item on this connection's
// route in ONE reply, with no pagination to fall back on (unlike
// mailbox_read, which pages under mailboxPageByteBudget -- server.go:1158).
// Two items well under maxDiagItems (32) and each individually small enough
// to append can still, TOGETHER, serialize past MaxFrame and tear the read
// connection permanently (the exact failure CR-4's mailbox guard,
// server.go:1123, exists to prevent). The 600 KiB payload size mirrors the
// review's own proof-of-concept probe.
func TestDiag_AppendRefusesWhenTotalBytesWouldExceedThePageBudget(t *testing.T) {
	srv, clk := startDiagTestRelay(t)
	ctx := testCtx(t)
	party := newB25Party(t, srv)

	token, err := MintDiagnosticCapability(diagOperatorSecret, clk.Now(), party.rid)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if err := party.cl.DiagOpen(ctx, token); err != nil {
		t.Fatalf("DiagOpen: %v", err)
	}

	big := make([]byte, 600*1024) // 600 KiB
	if err := party.cl.DiagAppend(ctx, big); err != nil {
		t.Fatalf("first 600 KiB append (well within the page budget alone): %v", err)
	}
	// A second 600 KiB append pushes this connection's TOTAL retained bytes past
	// mailboxPageByteBudget -- the same ceiling handleMailboxRead pages a reply
	// under -- and must be refused up front, not accepted and torn on read.
	if err := party.cl.DiagAppend(ctx, big); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("second 600 KiB append (pushes the connection's total past the page budget): got %v, want ErrQuotaExceeded", err)
	}
	// The connection must stay alive and still serve the ONE accepted item: the
	// failure this guards against is DiagRead tearing the connection, not merely
	// a rejected append.
	items, err := party.cl.DiagRead(ctx)
	if err != nil {
		t.Fatalf("DiagRead after the refused second append (connection must still be alive): %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("DiagRead = %d item(s), want exactly the 1 accepted append", len(items))
	}
}

// --- storage status (R2 review MEDIUM: the doctor's missing storage step) --

// TestDiag_StatusReportsStorageHealth proves diag_status carries a REAL
// storage-health snapshot -- the same checks /readyz reports (checkStorage,
// health.go) -- rather than a hardcoded ok, which is exactly what the review
// found missing from the doctor's mailbox round-trip (an in-memory route that
// never touches bbolt).
func TestDiag_StatusReportsStorageHealth(t *testing.T) {
	const tenGiB = 10 * 1024 * 1024 * 1024
	srv, clk := startDiagTestRelay(t, WithDiskFreeFunc(func() (uint64, error) {
		return tenGiB, nil // comfortably above DefaultConfig's threshold; deterministic, not the real host disk
	}))
	ctx := testCtx(t)
	party := newB25Party(t, srv)

	token, err := MintDiagnosticCapability(diagOperatorSecret, clk.Now(), party.rid)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if err := party.cl.DiagOpen(ctx, token); err != nil {
		t.Fatalf("DiagOpen: %v", err)
	}
	status, err := party.cl.DiagStatus(ctx)
	if err != nil {
		t.Fatalf("DiagStatus: %v", err)
	}
	if !status.StoreOK {
		t.Fatalf("DiagStatus.StoreOK = false against a healthy store, want true (StoreError=%q)", status.StoreError)
	}
	if !status.DiskCheckEnabled {
		t.Fatalf("DiagStatus.DiskCheckEnabled = false with DefaultConfig's positive disk_free_min_bytes, want true")
	}
	if !status.DiskOK {
		t.Fatalf("DiagStatus.DiskOK = false with %d bytes free, want true", tenGiB)
	}
	if status.DiskFreeBytes != tenGiB {
		t.Fatalf("DiagStatus.DiskFreeBytes = %d, want the injected %d", status.DiskFreeBytes, tenGiB)
	}
}

// TestDiag_StatusReflectsAnUnwritableStore is the review's MEDIUM finding
// proven at the protocol layer: a relay whose bbolt store is broken must
// report it here rather than silently passing. White-box, same technique as
// TestReadyz_UnreadyWhenStoreUnwritable (health_test.go): closes the
// underlying store without tearing down the whole server.
func TestDiag_StatusReflectsAnUnwritableStore(t *testing.T) {
	srv, clk := startDiagTestRelay(t)
	ctx := testCtx(t)
	party := newB25Party(t, srv)

	token, err := MintDiagnosticCapability(diagOperatorSecret, clk.Now(), party.rid)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if err := party.cl.DiagOpen(ctx, token); err != nil {
		t.Fatalf("DiagOpen: %v", err)
	}
	if err := srv.st.close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	status, err := party.cl.DiagStatus(ctx)
	if err != nil {
		t.Fatalf("DiagStatus against an unwritable store: %v", err)
	}
	if status.StoreOK {
		t.Fatalf("DiagStatus.StoreOK = true with the store closed, want false")
	}
	if status.StoreError == "" {
		t.Fatalf("DiagStatus.StoreError is empty on a store failure; want an actionable message")
	}
}

// mustJSON is a tiny test-only helper for the one adversarial test that talks
// to a raw, unauthenticated Conn directly (dialAuthed's Client is not
// available pre-auth).
func mustJSON(t *testing.T, v map[string]any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
