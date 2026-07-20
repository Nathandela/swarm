package relay

// Relay Hardening R1 — FAILING-FIRST (TDD RED, GG-5) tests remediating the
// audit-committee findings CR-1, CR-2, HI-1 from
// docs/verification/remote-phase1-relay-review.md.
//
// These tests encode the REQUIRED post-fix behavior. They are RED against the
// reviewed relay (commit 8664f3b) in two ways:
//
//   1. COMPILE-LEVEL RED (undefined symbols) — the CONTRACT the implementer must
//      supply. These new fields/sentinels do not exist yet:
//        - Config.HandshakeTimeout            time.Duration
//        - Quotas.MaxConcurrentConnections    int   (<= 0 means unlimited)
//        - Quotas.OpsPerMin                   int   (per-source cap on every
//                                                    state-touching control op)
//        - ErrRendezvousExists (+ codeRendezvousExists) — clean refusal when a
//          live/burned rendezvous id is (re)created.
//      Because Go builds one test binary per package, referencing these
//      undefined symbols fails the whole relay test build — the same
//      undefined-symbol RED style used by relay-red.txt / the harness header.
//
//   2. BEHAVIORAL RED (once the contract symbols exist) — the assertions below
//      fail against the current logic: one GLOBAL auth counter (CR-1), 14 of 16
//      unlimited ops (CR-2), and blind-overwrite / no-participant-check
//      rendezvous (HI-1).
//
// R1b amendment (ADR-007, "Amendment 2026-07-20 - Relay pre-authentication
// rate-limiting model", remediating relay-review findings R1-H1/H2/H3): the
// original TestRelay_AuthRatePerSource asserted per-UNPROVEN-key independence,
// the exact premise the amendment rejects (auth_init carries an unsigned pubkey,
// so keying a pre-auth window by it lets an attacker exhaust a victim's window
// and lets attacker-chosen keys create unbounded state). It is REPLACED below by
// TestRelay_AuthInitNotPoisonableByPresentedPubkey (pre-signature limiting keyed
// by TRANSPORT SOURCE, never by the presented pubkey) and
// TestRelay_PostAuthPerKeyOpBudgetIndependent (per-key limits are legitimate only
// AFTER signature verification). TestRelay_ConnRateLimited (abuse_test.go) is
// reframed comment-only as the per-source(IP) pre-auth window. No other test is
// modified and this file contains NO implementation.

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// discard drops a control/roundtrip reply body and returns only its error, so a
// table entry can report just "was this op refused, and how".
func discard(_ json.RawMessage, err error) error { return err }

// ---------------------------------------------------------------------------
// CR-1 — admission control + per-source auth rate.
// ---------------------------------------------------------------------------

// TestRelay_ConcurrentConnCapEnforced (CR-1) asserts a GLOBAL concurrent-
// connection cap: once MaxConcurrentConnections live sockets are held, one more
// is cleanly refused/closed rather than accepted into an unbounded goroutine/fd
// pool. Against the reviewed relay (server.go:213-220 accepts unlimited
// websockets) the over-cap connection is served, so this FAILS.
func TestRelay_ConcurrentConnCapEnforced(t *testing.T) {
	const capN = 4
	srv, _, _, _ := startTestRelay(t, func(c *Config) {
		c.Quotas.MaxConcurrentConnections = capN
	})

	// Fill the cap with live raw connections; each must be usable (proving the
	// cap is exactly capN, not smaller). dialRaw holds them open until cleanup.
	for i := 0; i < capN; i++ {
		conn := dialRaw(t, srv.URL())
		if _, _, err := conn.Hello(testCtx(t), ProtocolVersion, nil); err != nil {
			t.Fatalf("Hello on in-cap connection #%d: %v", i, err)
		}
	}

	// One connection beyond the cap must be cleanly refused: either the dial
	// fails, or the relay never serves a frame on it (the first round-trip
	// errors). The relay must not admit an unbounded (capN+1)th socket.
	over, err := DialRaw(testCtx(t), srv.URL())
	if err == nil {
		t.Cleanup(func() { _ = over.Close() })
		_, _, err = over.Hello(testCtx(t), ProtocolVersion, nil)
	}
	if err == nil {
		t.Fatalf("a connection beyond MaxConcurrentConnections=%d was served; want a clean refusal (CR-1)", capN)
	}
}

// TestRelay_IdleHandshakeTimeout (CR-1) asserts a bounded read/handshake
// deadline: a connection that completes the ws handshake but sends no frame is
// closed by the relay within HandshakeTimeout, so an attacker cannot park N
// idle sockets (slowloris / goroutine+fd exhaustion). Against the reviewed relay
// (readFrame at server.go:284-293 blocks on sc.ws.Read with no deadline) the
// connection is held forever and this FAILS on our own 2s guard timer.
func TestRelay_IdleHandshakeTimeout(t *testing.T) {
	const idle = 200 * time.Millisecond
	srv, _, _, _ := startTestRelay(t, func(c *Config) {
		c.HandshakeTimeout = idle
	})

	// Open the socket, complete the ws handshake, then send NOTHING.
	conn := dialRaw(t, srv.URL())

	// A blocking read must unblock (with an error) when the relay closes the idle
	// connection. If it never closes, the read blocks forever — the unbounded
	// idle read CR-1 forbids — and our timer fires the failure.
	done := make(chan error, 1)
	go func() {
		_, _, err := conn.ReadMsg()
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("idle connection read returned no error; want a server-initiated close after HandshakeTimeout (CR-1)")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("idle connection not closed within HandshakeTimeout+grace (%v); unbounded idle read (CR-1)", idle)
	}
}

// TestRelay_AuthInitNotPoisonableByPresentedPubkey (ADR-007 amendment 2026-07-20,
// remediating R1-H1) asserts pre-signature auth rate limiting is keyed by the
// TRANSPORT SOURCE, NEVER by the UNPROVEN presented relay-auth pubkey. An attacker
// on one source floods auth_init presenting a VICTIM's pubkey until that source's
// per-source budget is spent; a victim on a DISTINCT source must then still
// complete a FULL authenticated Dial (auth_init+auth_resp) with that same pubkey.
// If the attacker's flood had been charged to the victim's presented identity (the
// current server.go:489-499 authRate[RoutingID(presentedPub)] keying), the victim's
// window would already be spent and its Dial refused — the R1-H1 targeted lockout.
//
// SEAM the implementer MUST add (compile-level RED today; see the RED evidence):
//
//	WithSourceKeyFunc(func(remoteAddr string) string) Option
//	  Installs the pre-auth source-key deriver. The relay evaluates it ONCE per
//	  accepted connection (passing that connection's RemoteAddr) and uses the
//	  result as the rate key for every PRE-SIGNATURE op (auth_init and the
//	  unauthenticated rendezvous ops), REPLACING today's
//	  authRate[RoutingID(presentedPub)] keying. The DEFAULT (no option) derives the
//	  IP host of RemoteAddr (port stripped), so all localhost connections collapse
//	  to one source — which is why this test cannot rely on the default: two real
//	  client IPs are unavailable on loopback. It injects an IDENTITY deriver so the
//	  source key is the full RemoteAddr (ephemeral port included), giving each
//	  CONNECTION a distinct, controllable source. The attacker floods on a single
//	  connection (one source); the victim dials on another (a second source).
//
// GREEN once pre-auth keying is by source: the attacker's flood is charged to the
// attacker's source, and the victim (a different source) keeps its own budget.
func TestRelay_AuthInitNotPoisonableByPresentedPubkey(t *testing.T) {
	const budget = 3

	cfg := DefaultConfig()
	cfg.Listen = "127.0.0.1:0"
	cfg.TLSMode = "off"
	cfg.DBPath = filepath.Join(t.TempDir(), "relay.db")
	cfg.Quotas.ConnPerMin = budget
	cfg.Quotas.MaxConcurrentConnections = 0 // unlimited: the flood + victim open several sockets

	clk := newFakeClock()
	// identitySource makes each CONNECTION its own transport source (full
	// RemoteAddr, ephemeral port included), so the attacker's flood connection and
	// the victim's dial connection are distinct sources on one loopback host — the
	// per-connection seam the amendment's source keying needs.
	identitySource := func(remoteAddr string) string { return remoteAddr }
	srv, err := New(cfg, WithClock(clk), WithAPNsSink(&mockAPNs{}), WithSourceKeyFunc(identitySource))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	victimPub, victimPriv := newRelayAuthKey(t)

	// The attacker floods auth_init presenting the VICTIM's pubkey on ONE connection
	// (one transport source), spending that source's whole per-source budget.
	attacker := dialRaw(t, srv.URL())
	for i := 0; i < budget; i++ {
		if err := discard(attacker.control(testCtx(t), "auth_init", map[string]any{"relay_auth_pub": []byte(victimPub)})); err != nil {
			t.Fatalf("attacker auth_init #%d within the attacker source budget: got %v, want nil", i, err)
		}
	}
	// One more from the attacker's source is refused: the attacker exhausted the
	// ATTACKER'S OWN source budget — not the victim's identity.
	if err := discard(attacker.control(testCtx(t), "auth_init", map[string]any{"relay_auth_pub": []byte(victimPub)})); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("attacker auth_init over the attacker source budget: got %v, want ErrQuotaExceeded", err)
	}

	// The victim, on a DISTINCT source, completes a full Dial with the very pubkey
	// the attacker was flooding. It must succeed: pre-auth limiting is keyed by
	// source, so the attacker's flood never touched the victim's budget.
	victim, err := Dial(testCtx(t), srv.URL(), authFor(victimPub, victimPriv))
	if err != nil {
		t.Fatalf("victim Dial with the flooded pubkey from a distinct source: got %v, want success (auth_init must be keyed by transport source, not the presented pubkey — ADR-007 amendment R1-H1)", err)
	}
	t.Cleanup(func() { _ = victim.Close() })
}

// TestRelay_PostAuthPerKeyOpBudgetIndependent (ADR-007 amendment 2026-07-20,
// point 4) asserts POST-authentication per-key (per-routing-id) op budgets are
// INDEPENDENT: once a key has PROVEN its identity by completing the signed
// handshake, its OpsPerMin window is its own, and one authenticated key exhausting
// its budget must NOT limit another. Per-key rate limits are legitimate ONLY here —
// after signature verification — the counterpart to pre-signature limiting being
// keyed by source (never by the unproven pubkey).
//
// This guards the correct half of the model: the implementer must move the unproven
// pre-auth keying to source WITHOUT collapsing the proven post-auth per-key
// fairness. It compiles only once the WithSourceKeyFunc contract above exists (the
// package builds as one binary), then runs GREEN.
func TestRelay_PostAuthPerKeyOpBudgetIndependent(t *testing.T) {
	const quota = 5
	srv, _, _, _ := startTestRelay(t, func(c *Config) {
		c.Quotas.OpsPerMin = quota
		c.Quotas.MaxConcurrentConnections = 0
	})

	aPub, aPriv := newRelayAuthKey(t)
	bPub, bPriv := newRelayAuthKey(t)
	keyA := dialAuthed(t, srv.URL(), authFor(aPub, aPriv))
	keyB := dialAuthed(t, srv.URL(), authFor(bPub, bPriv))

	// Key A exhausts its OWN post-auth op budget on a post-auth op (mailbox_read).
	sawA := false
	for i := 0; i < quota+3; i++ {
		_, err := keyA.MailboxRead(testCtx(t), 0)
		if errors.Is(err, ErrQuotaExceeded) {
			sawA = true
			break
		}
		if err != nil {
			t.Fatalf("key A mailbox_read #%d: unexpected %v", i, err)
		}
	}
	if !sawA {
		t.Fatalf("key A never hit its own post-auth OpsPerMin budget after %d reads; a proven key needs its own per-key window (ADR-007 amendment point 4)", quota+3)
	}

	// Key B — a DISTINCT proven identity — must still have its own budget: A's
	// exhaustion must not limit B. B completes a post-auth op successfully.
	if _, err := keyB.MailboxRead(testCtx(t), 0); err != nil {
		t.Fatalf("key B post-auth op after key A exhausted its budget: got %v, want success (post-auth per-key windows are independent — ADR-007 amendment point 4)", err)
	}
}

// ---------------------------------------------------------------------------
// CR-2 — a per-source quota on EVERY state-touching op (R-REL.8), not just
// mailbox_append + push_trigger.
// ---------------------------------------------------------------------------

// TestRelay_EveryOpRateLimited (CR-2) enumerates every state-touching op and
// asserts each refuses past a per-source quota with a clean ErrQuotaExceeded.
// Each subtest boots its own relay + fixture so mutating ops (device_revoke,
// token_delete, ...) cannot cross-contaminate. mailbox_append and push_trigger
// already have limits (they must STILL cap); the other ops are unlimited in the
// reviewed relay and therefore FAIL (no ErrQuotaExceeded ever observed).
//
// The quota is metered PER SOURCE and independent of whether the op otherwise
// succeeds or returns its own benign error (e.g. device_revoke on an unpaired
// target): abuse metering must not depend on the request doing useful work.
func TestRelay_EveryOpRateLimited(t *testing.T) {
	const quota = 3

	// dummyDevicePub is a well-formed 32-byte key for authorize_device.
	dummyDevicePub := make([]byte, ed25519.PublicKeySize)
	for i := range dummyDevicePub {
		dummyDevicePub[i] = 0x2a
	}

	cases := []struct {
		name    string
		limited bool // true = the reviewed relay ALREADY limits it (expected GREEN)
		call    func(t *testing.T, m *Client, devRID string, sp sealParty, clk *fakeClock, seq uint64) error
	}{
		{"auth_resp", false, func(t *testing.T, m *Client, _ string, _ sealParty, _ *fakeClock, _ uint64) error {
			return discard(m.conn.control(testCtx(t), "auth_resp", map[string]any{"signature": make([]byte, ed25519.SignatureSize)}))
		}},
		{"authorize_device", false, func(t *testing.T, m *Client, _ string, _ sealParty, _ *fakeClock, _ uint64) error {
			return discard(m.conn.control(testCtx(t), "authorize_device", map[string]any{"device_pub": dummyDevicePub}))
		}},
		{"mailbox_read", false, func(t *testing.T, m *Client, _ string, _ sealParty, _ *fakeClock, _ uint64) error {
			return discard(m.conn.control(testCtx(t), "mailbox_read", map[string]any{"cursor": 0}))
		}},
		{"mailbox_ack", false, func(t *testing.T, m *Client, _ string, _ sealParty, _ *fakeClock, _ uint64) error {
			return discard(m.conn.control(testCtx(t), "mailbox_ack", map[string]any{"cursor": 0}))
		}},
		{"token_register", false, func(t *testing.T, m *Client, _ string, _ sealParty, _ *fakeClock, _ uint64) error {
			return discard(m.conn.control(testCtx(t), "token_register", map[string]any{"token": "tok"}))
		}},
		{"token_delete", false, func(t *testing.T, m *Client, _ string, _ sealParty, _ *fakeClock, _ uint64) error {
			return discard(m.conn.control(testCtx(t), "token_delete", nil))
		}},
		{"presence", false, func(t *testing.T, m *Client, devRID string, _ sealParty, _ *fakeClock, _ uint64) error {
			return discard(m.conn.control(testCtx(t), "presence", map[string]any{"target": devRID}))
		}},
		{"device_revoke", false, func(t *testing.T, m *Client, devRID string, _ sealParty, _ *fakeClock, _ uint64) error {
			return discard(m.conn.control(testCtx(t), "device_revoke", map[string]any{"target": devRID}))
		}},
		// Already-limited ops — asserted here so they must STILL cap.
		{"mailbox_append", true, func(t *testing.T, m *Client, devRID string, sp sealParty, clk *fakeClock, seq uint64) error {
			env := sp.sealMailbox(t, seq, []byte("x"), clk)
			return discard(m.conn.roundtrip(testCtx(t), MsgMailboxAppend, map[string]any{"target": devRID, "envelope": env}))
		}},
		{"push_trigger", true, func(t *testing.T, m *Client, devRID string, sp sealParty, clk *fakeClock, seq uint64) error {
			env := sp.sealMailbox(t, seq, []byte("wake"), clk)
			return discard(m.conn.control(testCtx(t), "push_trigger", map[string]any{"target": devRID, "envelope": env}))
		}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			srv, _, _, clk := startTestRelay(t, func(c *Config) {
				c.Quotas.OpsPerMin = quota
				c.Quotas.MailboxAppendPerMin = quota
				c.Quotas.PushPerMin = quota
				c.Quotas.MaxConcurrentConnections = 0
			})
			machine, _, devRID, sp := mailboxFixture(t, srv, clk)

			sawQuota := false
			for i := uint64(0); i < quota+3; i++ {
				if errors.Is(tc.call(t, machine, devRID, sp, clk, i+1), ErrQuotaExceeded) {
					sawQuota = true
					break
				}
			}
			if !sawQuota {
				t.Errorf("op %q admitted %d calls from one source with no ErrQuotaExceeded; every state-touching op needs a per-source quota (CR-2 / R-REL.8)", tc.name, quota+3)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// HI-1 — rendezvous integrity (no blind overwrite, participant-only burn/send)
// + per-source rate.
// ---------------------------------------------------------------------------

// TestRendezvous_CreateExistingRefused (HI-1) asserts rendezvous_create on a
// LIVE id is refused rather than blindly overwriting the original creator's slot
// (server.go:701 does s.rendezvous[id] = ... with no existence check). The
// original creator must keep the slot. Against the reviewed relay the second
// create succeeds and orphans creator A — so this FAILS.
func TestRendezvous_CreateExistingRefused(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, func(c *Config) { c.Quotas.MaxConcurrentConnections = 0 })
	const id = "rdv-collide"

	a := dialRaw(t, srv.URL())
	if err := a.RendezvousCreate(testCtx(t), id); err != nil {
		t.Fatalf("first create: %v", err)
	}
	// A different party creating the SAME live id is refused.
	b := dialRaw(t, srv.URL())
	if err := b.RendezvousCreate(testCtx(t), id); !errors.Is(err, ErrRendezvousExists) {
		t.Fatalf("second create on a live id: got %v, want ErrRendezvousExists (HI-1)", err)
	}
	// A's slot survived: a claimer joins A (not B) and exchanges bytes with A.
	claimer := dialRaw(t, srv.URL())
	if err := claimer.RendezvousClaim(testCtx(t), id); err != nil {
		t.Fatalf("claim of A's surviving slot: %v", err)
	}
	msg := []byte("to-the-original-creator")
	if err := claimer.RendezvousSend(testCtx(t), id, msg); err != nil {
		t.Fatalf("claimer send: %v", err)
	}
	got, err := a.RendezvousRecv(testCtx(t))
	if err != nil {
		t.Fatalf("original creator recv (proves A still owns the slot): %v", err)
	}
	if string(got) != string(msg) {
		t.Fatalf("original creator got %q, want %q (B hijacked A's slot)", got, msg)
	}
}

// TestRendezvous_CreateBurnedRefused (HI-1) asserts a BURNED (completed, single-
// use) id cannot be recreated. Against the reviewed relay create ignores the
// burned set entirely (server.go:687-706) so recreation succeeds — this FAILS.
func TestRendezvous_CreateBurnedRefused(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, func(c *Config) { c.Quotas.MaxConcurrentConnections = 0 })
	const id = "rdv-burned-recreate"

	creator := dialRaw(t, srv.URL())
	if err := creator.RendezvousCreate(testCtx(t), id); err != nil {
		t.Fatalf("create: %v", err)
	}
	claimer := dialRaw(t, srv.URL())
	if err := claimer.RendezvousClaim(testCtx(t), id); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := creator.RendezvousComplete(testCtx(t), id); err != nil {
		t.Fatalf("complete: %v", err)
	}

	// Recreating the burned id is refused (id is single-use for its lifetime).
	reuse := dialRaw(t, srv.URL())
	err := reuse.RendezvousCreate(testCtx(t), id)
	if !errors.Is(err, ErrRendezvousExists) && !errors.Is(err, ErrRendezvousBurned) {
		t.Fatalf("create of a burned id: got %v, want ErrRendezvousExists or ErrRendezvousBurned (HI-1)", err)
	}
}

// TestRendezvous_CompleteByNonParticipantRefused (HI-1) asserts an anonymous
// third party cannot burn a victim's in-flight rendezvous. Against the reviewed
// relay handleRendezvousComplete (server.go:778-790) burns ANY id with no
// participant check, so the attacker's complete succeeds — this FAILS.
func TestRendezvous_CompleteByNonParticipantRefused(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, func(c *Config) { c.Quotas.MaxConcurrentConnections = 0 })
	const id = "rdv-victim-complete"

	creator := dialRaw(t, srv.URL())
	if err := creator.RendezvousCreate(testCtx(t), id); err != nil {
		t.Fatalf("create: %v", err)
	}
	claimer := dialRaw(t, srv.URL())
	if err := claimer.RendezvousClaim(testCtx(t), id); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// A third party (neither creator nor claimer) must NOT burn the rendezvous.
	attacker := dialRaw(t, srv.URL())
	if err := attacker.RendezvousComplete(testCtx(t), id); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("non-participant complete: got %v, want ErrNotAuthorized (HI-1)", err)
	}

	// The rendezvous survived: the legit pair still exchanges bytes.
	msg := []byte("still-alive-after-attack")
	if err := creator.RendezvousSend(testCtx(t), id, msg); err != nil {
		t.Fatalf("creator send after failed attack: %v", err)
	}
	got, err := claimer.RendezvousRecv(testCtx(t))
	if err != nil {
		t.Fatalf("claimer recv after failed attack: %v", err)
	}
	if string(got) != string(msg) {
		t.Fatalf("post-attack forward: got %q, want %q", got, msg)
	}
}

// TestRendezvous_SendByNonParticipantRefused (HI-1) asserts a third party cannot
// inject into a rendezvous it is not part of. Against the reviewed relay
// handleRendezvousSend (server.go:738-761) silently no-ops for a non-participant
// and returns OK, so the attacker is told success — this FAILS (want a clean
// ErrNotAuthorized refusal).
func TestRendezvous_SendByNonParticipantRefused(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, func(c *Config) { c.Quotas.MaxConcurrentConnections = 0 })
	const id = "rdv-victim-send"

	creator := dialRaw(t, srv.URL())
	if err := creator.RendezvousCreate(testCtx(t), id); err != nil {
		t.Fatalf("create: %v", err)
	}
	claimer := dialRaw(t, srv.URL())
	if err := claimer.RendezvousClaim(testCtx(t), id); err != nil {
		t.Fatalf("claim: %v", err)
	}

	attacker := dialRaw(t, srv.URL())
	if err := attacker.RendezvousSend(testCtx(t), id, []byte("inject")); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("non-participant send: got %v, want ErrNotAuthorized (HI-1)", err)
	}
}

// TestRendezvous_RateLimitedPerSource (HI-1) asserts rendezvous ops are rate-
// limited per source, independent of the MaxConcurrentRendezvous slot cap
// (distinct ids here so the slot cap is NOT what refuses us). Against the
// reviewed relay no rendezvous op consults any rate window (only the concurrent
// cap at server.go:697) so a single source can create unboundedly — this FAILS.
func TestRendezvous_RateLimitedPerSource(t *testing.T) {
	const quota = 3
	srv, _, _, _ := startTestRelay(t, func(c *Config) {
		c.Quotas.OpsPerMin = quota
		c.Quotas.MaxConcurrentConnections = 0
		c.Quotas.MaxConcurrentRendezvous = 4096 // keep the slot cap out of the way
	})

	conn := dialRaw(t, srv.URL())
	sawQuota := false
	for i := 0; i < quota+3; i++ {
		if errors.Is(conn.RendezvousCreate(testCtx(t), fmt.Sprintf("rdv-rate-%d", i)), ErrQuotaExceeded) {
			sawQuota = true
			break
		}
	}
	if !sawQuota {
		t.Fatalf("rendezvous_create admitted %d creates from one source with no ErrQuotaExceeded; rendezvous needs a per-source rate window (HI-1 / R-REL.8)", quota+3)
	}
}
