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
// This file ADDS tests only. It modifies NO existing test and contains NO
// implementation. In particular it does NOT touch TestRelay_ConnRateLimited,
// which documents the CURRENT (incorrect) global-bucket behavior;
// TestRelay_AuthRatePerSource below asserts the CORRECT per-source behavior.

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
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

// TestRelay_AuthRatePerSource (CR-1) asserts the auth rate window is keyed PER
// SOURCE (the presented relay-auth pubkey / its routing id), not one process-
// wide counter: key A exhausting its own budget must NOT lock out key B.
//
// This is the CORRECT counterpart to TestRelay_ConnRateLimited (which documents
// the current global-bucket behavior with three keys sharing one counter and is
// left UNMODIFIED). Against the reviewed relay (single s.connRate at
// server.go:128, consumed server.go:422) A's flood drains the shared counter and
// B is refused — so this FAILS.
func TestRelay_AuthRatePerSource(t *testing.T) {
	const budget = 3
	srv, _, _, _ := startTestRelay(t, func(c *Config) {
		c.Quotas.ConnPerMin = budget
		c.Quotas.MaxConcurrentConnections = 0 // unlimited: this test opens many sockets
	})

	keyAPub, _ := newRelayAuthKey(t)
	keyBPub, _ := newRelayAuthKey(t)

	// authInit presents a relay-auth pubkey via a fresh raw auth_init and reports
	// whether the relay issued a challenge (nil) or refused (ErrQuotaExceeded).
	authInit := func(pub ed25519.PublicKey) error {
		conn := dialRaw(t, srv.URL())
		return discard(conn.control(testCtx(t), "auth_init", map[string]any{"relay_auth_pub": []byte(pub)}))
	}

	// Key A spends its ENTIRE per-source budget.
	for i := 0; i < budget; i++ {
		if err := authInit(keyAPub); err != nil {
			t.Fatalf("auth_init A #%d within A's own budget: got %v, want nil", i, err)
		}
	}
	// A's next auth_init is refused: A exhausted A's OWN budget.
	if err := authInit(keyAPub); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("auth_init A over A's budget: got %v, want ErrQuotaExceeded", err)
	}
	// Key B must still have its FULL budget — A's flood must not consume B's.
	for i := 0; i < budget; i++ {
		if err := authInit(keyBPub); err != nil {
			t.Fatalf("auth_init B #%d must be unaffected by A's flood (per-source keying): got %v, want nil (CR-1)", i, err)
		}
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
