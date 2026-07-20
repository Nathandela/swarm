package device

// Failing-first (TDD RED, GG-5) tests for the R-DEV.1 durable device registry
// (plan .claude/tmp/remote-control-implementation-plan.md:515-518). The registry is
// the daemon-side source of truth that pins each paired device's identity and its
// R-POL.6 capability tier; it is fed from a pairing MachineOutcome (its DevicePayload
// now carries DeviceCommandSignPub, ADR-007 2026-07-20) and later read by R-POL.9 to
// authorize each remote command.
//
// Every test below fails to COMPILE today because the package `device` and its
// symbols (Record, Registry, Open, Add, Get, List, Remove, Authorized, Count,
// DeviceIDFor, plus the Capability/Action types) do not yet exist. A separate
// implementer makes them pass; no test here is edited to go green.
//
// The durability contract mirrors internal/persist (atomic temp+rename+Sync, 0600,
// versioned envelope): a record written by one Registry must round-trip byte-exact
// through a FRESH Registry opened on the same directory. The security contract is
// FAIL-CLOSED: a malformed command-signing key or an empty device id is rejected and
// never persisted, and an unknown device is never authorized for any action.

import (
	"bytes"
	"crypto/ed25519"
	"testing"
	"time"
)

// key32 returns a deterministic 32-byte key whose bytes are all `fill`, standing in
// for a real ed25519.PublicKeySize (32) public key. Distinct fills give distinct,
// byte-comparable keys so persistence round-trips can be asserted exactly.
func key32(fill byte) []byte {
	b := make([]byte, ed25519.PublicKeySize)
	for i := range b {
		b[i] = fill
	}
	return b
}

// fullRecord builds a Record with every field set to a distinct, non-zero value so a
// persistence round-trip can prove no field is dropped or defaulted. The DeviceID is
// derived from the command-signing key via DeviceIDFor, mirroring how the daemon will
// mint it from a pairing outcome.
func fullRecord(t *testing.T, cmdFill byte, cap Capability, epoch uint32) Record {
	t.Helper()
	cmd := key32(cmdFill)
	return Record{
		DeviceID:       DeviceIDFor(cmd),
		Name:           "phone-" + string(rune('A'+int(cmdFill%26))),
		NoiseStaticPub: key32(cmdFill + 1),
		RelayAuthPub:   key32(cmdFill + 2),
		CommandSignPub: cmd,
		RecipientPub:   key32(cmdFill + 3),
		RoutingID:      []byte{cmdFill, cmdFill ^ 0xFF, 0x07, 0x2A},
		Capability:     cap,
		PairedAt:       time.Date(2026, 7, 20, 8, 30, 45, 123456789, time.UTC),
		GrantedEpoch:   epoch,
	}
}

// recordsEqual asserts every field of two Records is equal, comparing byte slices
// byte-for-byte and PairedAt via time.Equal (so a monotonic-clock/location reload
// difference does not spuriously fail the round-trip).
func recordsEqual(t *testing.T, got, want Record) {
	t.Helper()
	if got.DeviceID != want.DeviceID {
		t.Errorf("DeviceID = %q, want %q", got.DeviceID, want.DeviceID)
	}
	if got.Name != want.Name {
		t.Errorf("Name = %q, want %q", got.Name, want.Name)
	}
	if !bytes.Equal(got.NoiseStaticPub, want.NoiseStaticPub) {
		t.Errorf("NoiseStaticPub = %x, want %x", got.NoiseStaticPub, want.NoiseStaticPub)
	}
	if !bytes.Equal(got.RelayAuthPub, want.RelayAuthPub) {
		t.Errorf("RelayAuthPub = %x, want %x", got.RelayAuthPub, want.RelayAuthPub)
	}
	if !bytes.Equal(got.CommandSignPub, want.CommandSignPub) {
		t.Errorf("CommandSignPub = %x, want %x", got.CommandSignPub, want.CommandSignPub)
	}
	if !bytes.Equal(got.RecipientPub, want.RecipientPub) {
		t.Errorf("RecipientPub = %x, want %x", got.RecipientPub, want.RecipientPub)
	}
	if !bytes.Equal(got.RoutingID, want.RoutingID) {
		t.Errorf("RoutingID = %x, want %x", got.RoutingID, want.RoutingID)
	}
	if got.Capability != want.Capability {
		t.Errorf("Capability = %v, want %v", got.Capability, want.Capability)
	}
	if !got.PairedAt.Equal(want.PairedAt) {
		t.Errorf("PairedAt = %v, want %v", got.PairedAt, want.PairedAt)
	}
	if got.GrantedEpoch != want.GrantedEpoch {
		t.Errorf("GrantedEpoch = %d, want %d", got.GrantedEpoch, want.GrantedEpoch)
	}
}

// TestRegistry_Persist is an R-DEV.1 verify target. It Adds two devices with
// DIFFERENT capability tiers and full field sets to a registry, opens a FRESH
// Registry on the SAME directory, and asserts every field of every record survives
// intact (keys byte-equal, Capability preserved, PairedAt via .Equal, GrantedEpoch,
// Name, RoutingID). It also asserts Count() and List() determinism across the reload.
func TestRegistry_Persist(t *testing.T) {
	dir := t.TempDir()

	reg, err := Open(dir)
	if err != nil {
		t.Fatalf("Open(%q) error: %v", dir, err)
	}

	recA := fullRecord(t, 0x11, CapReadOnly, 3)
	recB := fullRecord(t, 0x22, CapFull, 7)
	if err := reg.Add(recA); err != nil {
		t.Fatalf("Add(recA) error: %v", err)
	}
	if err := reg.Add(recB); err != nil {
		t.Fatalf("Add(recB) error: %v", err)
	}
	if got := reg.Count(); got != 2 {
		t.Fatalf("Count() = %d, want 2", got)
	}

	// A fresh Registry on the same dir must load exactly what was persisted.
	reloaded, err := Open(dir)
	if err != nil {
		t.Fatalf("re-Open(%q) error: %v", dir, err)
	}
	if got := reloaded.Count(); got != 2 {
		t.Fatalf("reloaded Count() = %d, want 2", got)
	}

	for _, want := range []Record{recA, recB} {
		got, ok := reloaded.Get(want.DeviceID)
		if !ok {
			t.Fatalf("reloaded Get(%q) missing", want.DeviceID)
		}
		recordsEqual(t, got, want)
	}

	// List() must be deterministic: the same order on repeated calls and across the
	// reload, so a TUI/app rendering is stable.
	l1 := reloaded.List()
	l2 := reloaded.List()
	if len(l1) != 2 || len(l2) != 2 {
		t.Fatalf("List() len = %d/%d, want 2/2", len(l1), len(l2))
	}
	for i := range l1 {
		if l1[i].DeviceID != l2[i].DeviceID {
			t.Fatalf("List() nondeterministic at %d: %q vs %q", i, l1[i].DeviceID, l2[i].DeviceID)
		}
	}
	pre := reg.List()
	if len(pre) != len(l1) {
		t.Fatalf("pre-reload List() len %d != post-reload %d", len(pre), len(l1))
	}
	for i := range pre {
		if pre[i].DeviceID != l1[i].DeviceID {
			t.Fatalf("List() order changed across reload at %d: %q vs %q", i, pre[i].DeviceID, l1[i].DeviceID)
		}
	}
}

// TestRegistry_CapabilityEnforced is an R-DEV.1 / R-POL.6 verify target. It registers
// a read-only, a read+approve, and a full device and asserts the full 3x3
// Authorized(id, action) matrix, plus the fail-closed unknown-device case.
func TestRegistry_CapabilityEnforced(t *testing.T) {
	reg, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}

	ro := fullRecord(t, 0x01, CapReadOnly, 1)
	ra := fullRecord(t, 0x02, CapReadApprove, 1)
	full := fullRecord(t, 0x03, CapFull, 1)
	for _, r := range []Record{ro, ra, full} {
		if err := reg.Add(r); err != nil {
			t.Fatalf("Add(%q) error: %v", r.DeviceID, err)
		}
	}

	matrix := []struct {
		who     string
		id      string
		read    bool
		approve bool
		control bool
	}{
		{"read_only", ro.DeviceID, true, false, false},
		{"read_approve", ra.DeviceID, true, true, false},
		{"full", full.DeviceID, true, true, true},
	}
	for _, m := range matrix {
		if got := reg.Authorized(m.id, ActionRead); got != m.read {
			t.Errorf("%s Authorized(Read) = %v, want %v", m.who, got, m.read)
		}
		if got := reg.Authorized(m.id, ActionApprove); got != m.approve {
			t.Errorf("%s Authorized(Approve) = %v, want %v", m.who, got, m.approve)
		}
		if got := reg.Authorized(m.id, ActionControl); got != m.control {
			t.Errorf("%s Authorized(Control) = %v, want %v", m.who, got, m.control)
		}
	}

	// Fail-closed: a device that was never registered is authorized for nothing.
	if reg.Authorized("unknown-device", ActionRead) {
		t.Errorf("Authorized(unknown, Read) = true, want false (fail-closed)")
	}
}

// TestRegistry_AddRejectsBadKey is the security guard for R-DEV.1: Add MUST reject a
// CommandSignPub that is not exactly ed25519.PublicKeySize (32) bytes, and MUST reject
// an empty DeviceID. A rejected Add must not persist the record (Count unchanged, Get
// returns false) — the registry is the R-POL.9 authorization authority, so a
// malformed identity must never be admitted (fail-closed).
func TestRegistry_AddRejectsBadKey(t *testing.T) {
	reg, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}

	// A well-formed base record; each subtest corrupts exactly one field.
	base := fullRecord(t, 0x44, CapFull, 2)

	badKeys := []struct {
		name string
		key  []byte
	}{
		{"len_31", make([]byte, 31)},
		{"len_33", make([]byte, 33)},
		{"len_0", []byte{}},
	}
	for _, bk := range badKeys {
		t.Run(bk.name, func(t *testing.T) {
			rec := base
			rec.CommandSignPub = bk.key
			// DeviceID stays a valid, non-empty id; only the key length is wrong.
			if err := reg.Add(rec); err == nil {
				t.Fatalf("Add with CommandSignPub len %d = nil error, want rejection", len(bk.key))
			}
			if got := reg.Count(); got != 0 {
				t.Fatalf("Count() = %d after rejected Add, want 0 (not persisted)", got)
			}
			if _, ok := reg.Get(rec.DeviceID); ok {
				t.Fatalf("Get(%q) found a rejected record, want absent", rec.DeviceID)
			}
		})
	}

	t.Run("empty_device_id", func(t *testing.T) {
		rec := base
		rec.DeviceID = ""
		if err := reg.Add(rec); err == nil {
			t.Fatalf("Add with empty DeviceID = nil error, want rejection")
		}
		if got := reg.Count(); got != 0 {
			t.Fatalf("Count() = %d after rejected Add, want 0", got)
		}
	})
}

// TestRegistry_Remove exercises the delete path used by R-DEV.2 local revocation:
// Remove reports whether it removed something and durably drops the record, while a
// remove of an absent id is a no-op reporting false.
func TestRegistry_Remove(t *testing.T) {
	dir := t.TempDir()
	reg, err := Open(dir)
	if err != nil {
		t.Fatalf("Open error: %v", err)
	}
	rec := fullRecord(t, 0x55, CapReadApprove, 4)
	if err := reg.Add(rec); err != nil {
		t.Fatalf("Add error: %v", err)
	}

	removed, err := reg.Remove(rec.DeviceID)
	if err != nil {
		t.Fatalf("Remove error: %v", err)
	}
	if !removed {
		t.Fatalf("Remove(%q) = false, want true", rec.DeviceID)
	}
	if got := reg.Count(); got != 0 {
		t.Fatalf("Count() = %d after Remove, want 0", got)
	}

	// Idempotent: removing an absent id reports false, not an error.
	again, err := reg.Remove(rec.DeviceID)
	if err != nil {
		t.Fatalf("second Remove error: %v", err)
	}
	if again {
		t.Fatalf("Remove(absent) = true, want false")
	}

	// The removal must be durable: a fresh Registry sees no record.
	reloaded, err := Open(dir)
	if err != nil {
		t.Fatalf("re-Open error: %v", err)
	}
	if got := reloaded.Count(); got != 0 {
		t.Fatalf("reloaded Count() = %d after durable Remove, want 0", got)
	}
}

// TestDeviceIDFor pins the canonical device-id derivation: the id is deterministic
// (same command-signing key -> same id), collision-avoiding (different keys ->
// different ids), and always a non-empty string.
func TestDeviceIDFor(t *testing.T) {
	k1 := key32(0xAB)
	k2 := key32(0xCD)

	id1 := DeviceIDFor(k1)
	id1again := DeviceIDFor(append([]byte(nil), k1...)) // same bytes, different backing array
	id2 := DeviceIDFor(k2)

	if id1 == "" {
		t.Fatalf("DeviceIDFor returned empty string")
	}
	if id1 != id1again {
		t.Fatalf("DeviceIDFor not deterministic: %q vs %q", id1, id1again)
	}
	if id1 == id2 {
		t.Fatalf("DeviceIDFor collided for distinct keys: both %q", id1)
	}
}
