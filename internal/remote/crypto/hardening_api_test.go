// Remediation RED (fix wave) — new-API tests that reference symbols the fix
// introduces. Against the un-fixed package these fail to COMPILE (undefined /
// signature-mismatch), the canonical GG-5 RED for API-adding findings.
//
// Findings pinned here: F1 (pairing unpinned opt-in), PSK seam, F3 (grant
// authentication + anti-replay), F4 (mailbox high-water seeding), F7 (rekey
// thresholds enforced), F9 (issued_at freshness), F10 (typed wake/content
// keys), F12 (unknown type rejected at parse), F13 (command canonicalization).
package crypto

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"
)

func machineSigner(seed byte) (ed25519.PrivateKey, ed25519.PublicKey) {
	s := fill(seed)
	priv := ed25519.NewKeyFromSeed(s[:])
	return priv, priv.Public().(ed25519.PublicKey)
}

// F1 — pairing explicitly opts into an unpinned peer (it learns the static via
// the SAS-confirmed handshake); this is the ONLY unpinned path, and it is
// mechanically pairing-only because unpinned REQUIRES a 32-byte PSK.
func TestPairing_UnpinnedPeerOptIn(t *testing.T) {
	a, _ := GenerateIdentity()
	b, _ := GenerateIdentity()
	psk := fill(0x77)
	ini, err := NewNoise(NoiseConfig{
		Initiator: true, Static: a.NoiseStatic(), AllowUnpinnedPeer: true,
		PSK: psk[:], Prologue: PairPrologue([]byte("rv")),
	})
	if err != nil {
		t.Fatalf("pairing initiator: %v", err)
	}
	resp, err := NewNoise(NoiseConfig{
		Initiator: false, Static: b.NoiseStatic(), AllowUnpinnedPeer: true,
		PSK: psk[:], Prologue: PairPrologue([]byte("rv")),
	})
	if err != nil {
		t.Fatalf("pairing responder: %v", err)
	}
	if err := driveXX(ini, resp); err != nil {
		t.Fatalf("pairing handshake: %v", err)
	}
	if !bytes.Equal(ini.PeerStatic(), b.NoiseStaticPublic()) {
		t.Error("pairing initiator did not learn the peer static")
	}
}

// F1 — an unpinned session WITHOUT a PSK is refused, so a live session (live
// prologue, no PSK) can never opt out of static pinning. This is the negative
// case that closes the "AllowUnpinnedPeer is not mechanically pairing-only" hole.
func TestLive_UnpinnedWithoutPSKRefused(t *testing.T) {
	a, _ := GenerateIdentity()
	_, err := NewNoise(NoiseConfig{
		Initiator: true, Static: a.NoiseStatic(), AllowUnpinnedPeer: true,
		Prologue: LivePrologue([]byte("m"), []byte("d")),
	})
	if !errors.Is(err, ErrUnpinnedRequiresPSK) {
		t.Fatalf("unpinned live session err = %v, want ErrUnpinnedRequiresPSK", err)
	}
}

// PSK seam — an XXpsk0-configured handshake completes with a matching PSK and
// fails with a mismatched one (provisional plain-XX binding until pairing).
func TestPairing_PSKHandshake(t *testing.T) {
	a, _ := GenerateIdentity()
	b, _ := GenerateIdentity()
	mk := func(psk [32]byte, ini bool, self, peer *Identity) *NoiseSession {
		s, err := NewNoise(NoiseConfig{
			Initiator: ini, Static: self.NoiseStatic(), PeerStatic: peer.NoiseStaticPublic(),
			Prologue: PairPrologue([]byte("rv")), PSK: psk[:],
		})
		if err != nil {
			t.Fatalf("NewNoise(psk): %v", err)
		}
		return s
	}
	if err := driveXX(mk(fill(0x77), true, a, b), mk(fill(0x77), false, b, a)); err != nil {
		t.Fatalf("XXpsk0 handshake with matching PSK failed: %v", err)
	}
	if err := driveXX(mk(fill(0x77), true, a, b), mk(fill(0x88), false, b, a)); err == nil {
		t.Fatal("XXpsk0 completed with a mismatched PSK")
	}
}

// F3 — a grant whose outer routing epoch/seq are relabelled is rejected (the
// coordinates are authenticated inside the sealed plaintext).
func TestEpochGrant_RelabeledCoordinatesRejected(t *testing.T) {
	priv, pub := machineSigner(0x31)
	ks := devKeyStore(t, stdMaterial())

	g, err := SealEpochGrant(priv, ks.RecipientPublic(), 5, 1, testEpochKeys())
	if err != nil {
		t.Fatalf("SealEpochGrant: %v", err)
	}
	relabelledEpoch := *g
	relabelledEpoch.EpochID = 6
	if _, _, _, err := OpenEpochGrant(ks, pub, &relabelledEpoch); err == nil {
		t.Error("relabelled epoch_id accepted; inner coordinates not verified")
	}
	relabelledSeq := *g
	relabelledSeq.GrantSeq = 2
	if _, _, _, err := OpenEpochGrant(ks, pub, &relabelledSeq); err == nil {
		t.Error("relabelled grant_seq accepted")
	}
}

// F3 — a grant not signed by the pinned machine key is rejected (SealAnonymous
// alone gives no sender authentication).
func TestEpochGrant_UnsignedOrWrongSignerRejected(t *testing.T) {
	priv, pub := machineSigner(0x31)
	_, otherPub := machineSigner(0x41)
	ks := devKeyStore(t, stdMaterial())

	g, err := SealEpochGrant(priv, ks.RecipientPublic(), 5, 1, testEpochKeys())
	if err != nil {
		t.Fatalf("SealEpochGrant: %v", err)
	}
	if _, _, _, err := OpenEpochGrant(ks, otherPub, g); err == nil {
		t.Error("grant verified against a non-pinned machine key")
	}
	stripped := *g
	stripped.Sig = nil
	if _, _, _, err := OpenEpochGrant(ks, pub, &stripped); err == nil {
		t.Error("grant with a stripped signature accepted")
	}
	// Sanity: the untouched grant opens.
	if _, _, _, err := OpenEpochGrant(ks, pub, g); err != nil {
		t.Errorf("valid grant rejected: %v", err)
	}
}

// F3 — a replayed grant (same or lower epoch/grant_seq) is rejected by the
// per-device high-water tracker.
func TestEpochGrant_ReplayRejected(t *testing.T) {
	priv, pub := machineSigner(0x31)
	ks := devKeyStore(t, stdMaterial())
	rcv := NewGrantReceiver()

	g1, _ := SealEpochGrant(priv, ks.RecipientPublic(), 5, 1, testEpochKeys())
	if _, _, _, err := rcv.Accept(ks, pub, g1); err != nil {
		t.Fatalf("first accept: %v", err)
	}
	if _, _, _, err := rcv.Accept(ks, pub, g1); err == nil {
		t.Error("replayed grant accepted")
	}
	g0, _ := SealEpochGrant(priv, ks.RecipientPublic(), 5, 0, testEpochKeys())
	if _, _, _, err := rcv.Accept(ks, pub, g0); err == nil {
		t.Error("older grant_seq accepted after a newer one")
	}
	g2, _ := SealEpochGrant(priv, ks.RecipientPublic(), 6, 2, testEpochKeys())
	if _, _, _, err := rcv.Accept(ks, pub, g2); err != nil {
		t.Errorf("a strictly newer grant was rejected: %v", err)
	}
}

// F3 — grant anti-replay survives restart: a receiver reseeded from durable
// (epoch_id, grant_seq) rejects a replayed old grant that a relay resends after
// a phone/app restart, instead of accepting it as the first grant.
func TestEpochGrant_ReplaySurvivesRestart(t *testing.T) {
	priv, pub := machineSigner(0x31)
	ks := devKeyStore(t, stdMaterial())

	// Device previously accepted (epoch 5, seq 3); this is persisted.
	old, _ := SealEpochGrant(priv, ks.RecipientPublic(), 5, 3, testEpochKeys())
	// After restart, reseed the receiver from durable state.
	rcv := NewGrantReceiverAt(5, 3)
	if _, _, _, err := rcv.Accept(ks, pub, old); err == nil {
		t.Error("replayed old grant accepted by a restart-reseeded receiver")
	}
	// A strictly newer grant still works.
	newer, _ := SealEpochGrant(priv, ks.RecipientPublic(), 6, 4, testEpochKeys())
	if _, _, _, err := rcv.Accept(ks, pub, newer); err != nil {
		t.Errorf("a strictly newer grant was rejected after restart: %v", err)
	}
}

// F4 — a seeded receiver surfaces a first-event gap relative to the snapshot
// cursor, and rejects an at-or-below-cursor first event as stale.
func TestMailbox_SeededFirstEventGap(t *testing.T) {
	key := fill(0x9c)
	h := testHeader()
	r := NewMailboxReceiver()
	r.SeedHighWater(h.SenderKeyID, h.EpochID, 99)

	res, err := r.Accept(key, sealSeq(t, key, 101)) // 100 missing
	if err != nil {
		t.Fatalf("Accept(101 after seed 99): %v", err)
	}
	if !res.Gap {
		t.Error("seeded receiver did not surface a first-event gap")
	}
}

func TestMailbox_SeededStaleRejected(t *testing.T) {
	key := fill(0x9c)
	h := testHeader()
	r := NewMailboxReceiver()
	r.SeedHighWater(h.SenderKeyID, h.EpochID, 100)
	if _, err := r.Accept(key, sealSeq(t, key, 100)); !errors.Is(err, ErrStaleSeq) {
		t.Fatalf("seeded stale first event err = %v, want ErrStaleSeq", err)
	}
}

func TestMailbox_UnseededFirstEventNoGap(t *testing.T) {
	// Documents the blind spot the seed API closes: an un-seeded receiver
	// cannot detect a gap on its very first event.
	key := fill(0x9c)
	r := NewMailboxReceiver()
	res, err := r.Accept(key, sealSeq(t, key, 100))
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if res.Gap {
		t.Error("unseeded first event unexpectedly reported a gap")
	}
}

// F7 — the byte budget is ENFORCED, not advisory: once a direction crosses it,
// Encrypt refuses further traffic until a coordinated Rekey() resets the budget.
func TestTransport_RekeyDueByBytes(t *testing.T) {
	ini, resp := completedPair(t)
	ini.rekeyBytes = 32
	msg := bytes.Repeat([]byte{0x41}, 16)
	// Two 16-byte messages exactly reach the 32-byte budget.
	for i := 0; i < 2; i++ {
		ct, err := ini.Encrypt(msg)
		if err != nil {
			t.Fatalf("Encrypt %d before budget: %v", i, err)
		}
		if _, err := resp.Decrypt(ct); err != nil {
			t.Fatalf("Decrypt %d: %v", i, err)
		}
	}
	// Enforcement: no more traffic flows on the old key.
	if _, err := ini.Encrypt(msg); !errors.Is(err, ErrRekeyRequired) {
		t.Fatalf("Encrypt past budget err = %v, want ErrRekeyRequired", err)
	}
	// A coordinated Rekey() resets both directions and traffic resumes.
	ini.Rekey()
	resp.Rekey()
	ct, err := ini.Encrypt(msg)
	if err != nil {
		t.Fatalf("Encrypt after Rekey: %v", err)
	}
	if _, err := resp.Decrypt(ct); err != nil {
		t.Fatalf("Decrypt after Rekey: %v", err)
	}
}

// F7 — rekey is due once the elapsed-time budget is crossed (injected clock).
func TestTransport_RekeyDueByTime(t *testing.T) {
	ini, _ := completedPair(t)
	base := time.Now()
	ini.now = func() time.Time { return base }
	ini.established = base
	ini.rekeyDur = time.Minute
	if ini.RekeyDue() {
		t.Fatal("rekey due at t=0")
	}
	base = base.Add(2 * time.Minute)
	if !ini.RekeyDue() {
		t.Fatal("rekey not due after the duration budget elapsed")
	}
}

// F9 — issued_at is AAD-covered (tampering it fails Open) and the receiver can
// reject a mailbox event by age.
func TestEnvelope_IssuedAtInAAD(t *testing.T) {
	key := fill(0x9c)
	h := testHeader()
	h.IssuedAt = time.Now().UnixMilli()
	env, err := seal(key, h, []byte("payload"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	raw := env.Marshal()
	raw[30] ^= 0xff // first byte of issued_at
	bad, err := ParseEnvelope(raw)
	if err != nil {
		return // parse rejection acceptable
	}
	if _, err := bad.open(key); err == nil {
		t.Error("tampering issued_at (AAD-covered) was accepted by Open")
	}
}

func TestMailbox_StaleByAgeRejected(t *testing.T) {
	key := fill(0x9c)
	base := time.Now()
	r := NewMailboxReceiver()
	r.maxAge = 5 * time.Minute
	r.now = func() time.Time { return base }

	stale := testHeader()
	stale.Seq = 1
	stale.IssuedAt = base.Add(-10 * time.Minute).UnixMilli()
	staleEnv, err := seal(key, stale, []byte("stale"))
	if err != nil {
		t.Fatalf("seal(stale): %v", err)
	}
	if _, err := r.Accept(key, staleEnv); !errors.Is(err, ErrStaleAge) {
		t.Fatalf("stale-by-age err = %v, want ErrStaleAge", err)
	}

	fresh := testHeader()
	fresh.Seq = 2
	fresh.IssuedAt = base.UnixMilli()
	freshEnv, err := seal(key, fresh, []byte("fresh"))
	if err != nil {
		t.Fatalf("seal(fresh): %v", err)
	}
	if _, err := r.Accept(key, freshEnv); err != nil {
		t.Fatalf("fresh event rejected by age: %v", err)
	}
}

// F10 — content and wake keys are distinct types; content sealed under the
// content key cannot be opened with the wake key via the typed API, and the
// type byte is forced by the sealing helper.
func TestTypedKeys_ContentNotOpenableWithWakeKey(t *testing.T) {
	keys, err := NewEpochKeys()
	if err != nil {
		t.Fatalf("NewEpochKeys: %v", err)
	}
	if [32]byte(keys.WakeKey) == [32]byte(keys.ContentKey) {
		t.Fatal("wake and content keys must be independently generated")
	}

	cEnv, err := SealMailbox(keys.ContentKey, testHeader(), []byte("session transcript"))
	if err != nil {
		t.Fatalf("SealMailbox: %v", err)
	}
	if cEnv.Header.Type != TypeMailbox {
		t.Errorf("SealMailbox set type %#x, want 0x01", cEnv.Header.Type)
	}
	if _, err := OpenWake(keys.WakeKey, cEnv); err == nil {
		t.Error("OpenWake opened content sealed under the content key")
	}
	pt, err := OpenMailbox(keys.ContentKey, cEnv)
	if err != nil {
		t.Fatalf("OpenMailbox: %v", err)
	}
	if string(pt) != "session transcript" {
		t.Errorf("content round-trip = %q", pt)
	}

	wEnv, err := SealWake(keys.WakeKey, testHeader(), []byte("activity"))
	if err != nil {
		t.Fatalf("SealWake: %v", err)
	}
	if wEnv.Header.Type != TypePushWake {
		t.Errorf("SealWake set type %#x, want 0x02", wEnv.Header.Type)
	}
	if _, err := OpenMailbox(keys.ContentKey, wEnv); err == nil {
		t.Error("OpenMailbox opened a wake payload")
	}
}

// F12 — an unknown envelope type (not 0x01/0x02) is rejected at parse.
func TestEnvelope_UnknownTypeRejected(t *testing.T) {
	key := fill(0x9c)
	env, err := seal(key, testHeader(), []byte("x"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	raw := env.Marshal()
	raw[1] = 0x7f // neither TypeMailbox nor TypePushWake
	if _, err := ParseEnvelope(raw); !errors.Is(err, ErrUnknownType) {
		t.Fatalf("ParseEnvelope(unknown type) err = %v, want ErrUnknownType", err)
	}
}

// F13 — Canonical validates mandatory ids, content-hash length, and treats nil
// and empty content hashes identically.
func TestCommand_CanonicalValidates(t *testing.T) {
	full := func() Command {
		return Command{Action: "a", Machine: "m", Session: "s", OperationID: "o", ExpiresAt: 1}
	}
	for name, mut := range map[string]func(*Command){
		"action":       func(c *Command) { c.Action = "" },
		"machine":      func(c *Command) { c.Machine = "" },
		"session":      func(c *Command) { c.Session = "" },
		"operation_id": func(c *Command) { c.OperationID = "" },
	} {
		c := full()
		mut(&c)
		if _, err := c.Canonical(); err == nil {
			t.Errorf("Canonical accepted a command missing %s", name)
		}
	}

	bad := full()
	bad.ContentHash = []byte("short")
	if _, err := bad.Canonical(); err == nil {
		t.Error("Canonical accepted a non-32-byte content hash")
	}

	ok := full()
	if _, err := ok.Canonical(); err != nil {
		t.Errorf("Canonical rejected a valid command: %v", err)
	}

	nilHash, empty := full(), full()
	empty.ContentHash = []byte{}
	nb, err1 := nilHash.Canonical()
	eb, err2 := empty.Canonical()
	if err1 != nil || err2 != nil {
		t.Fatalf("canonical errs: %v %v", err1, err2)
	}
	if !bytes.Equal(nb, eb) {
		t.Error("nil and empty content hash must canonicalize identically")
	}
}
