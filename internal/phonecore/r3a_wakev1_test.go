// FAILING-FIRST (TDD RED, GG-5) for Wave R3's Android/phone slice, part 3 of the scope:
// the WakeV1 RECEIVER (ADR-015 P7/P8, push-gateway-api.md section 5.5, PG-WAKE-13..18).
//
// WHAT IS UNDER TEST. The phone-side v1 wake path that does not exist yet:
//
//   - phonecore.NewPairingWakeKey -- the PHONE-generated per-pairing wake key (ADR-015 P7:
//     the custody change; the key is minted here, conveyed in the pairing transcript, and
//     the gateway never receives it).
//   - (*Core).AdoptPushBinding / DropPushBinding -- per-pairing (address, wake key) state
//     with a per-address durable high-water (PG-WAKE-15: State.WakeReplay becomes a table).
//   - (*Core).AcceptWakeV1 -- PG-WAKE-13's five-step order against the 74-byte envelope.
//   - (*Core).WakeDrops -- the scope's "an unverifiable wake is dropped and counted, never
//     acted on": a monotonic count of refused wakes, advanced on every refusal.
//   - phonecore.WakeV1MaxAge = 5 minutes -- PG-WAKE-7's narrowed bound (the v1 receiver's
//     own constant; the legacy 10-minute WakeMaxAge belongs to the type-0x02 path and is
//     not weakened or touched here).
//
// THE PRODUCER IS THE REAL MACHINE-SIDE SEALER. Every genuine envelope in this file is
// sealed by internal/remotegw.SealWakeV1 -- the code swarm-remote ships -- so the two
// sides' AAD tuples, offsets and domain separation are pinned to each other by test
// rather than by prose. A phone-side opener that "almost" matches the producer fails
// here, not on a handset.
//
// NOTHING HERE TOUCHES FCM, GOOGLE, OR A HANDSET. PB-E2E-5 and R3's physical exit are
// not claimed by any test in this file.
package phonecore

import (
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remotegw"
)

// r3aBinding mints a phone-generated wake key and adopts (addr, key) into the core, as
// the pairing commit will once the conveyance lands.
func r3aBinding(t *testing.T, core *Core, addrByte byte) (PushAddress, crypto.WakeKey) {
	t.Helper()
	key, err := NewPairingWakeKey()
	if err != nil {
		t.Fatalf("NewPairingWakeKey: %v", err)
	}
	var addr PushAddress
	for i := range addr {
		addr[i] = addrByte
	}
	if err := core.AdoptPushBinding(addr, key); err != nil {
		t.Fatalf("AdoptPushBinding: %v", err)
	}
	return addr, key
}

// r3aSeal seals one genuine WakeV1 with the machine-side producer.
func r3aSeal(t *testing.T, key crypto.WakeKey, addr PushAddress, seq uint64, issuedAt time.Time) []byte {
	t.Helper()
	env, err := remotegw.SealWakeV1(key, remotegw.PushAddress(addr), seq, issuedAt)
	if err != nil {
		t.Fatalf("remotegw.SealWakeV1: %v", err)
	}
	if len(env) != remotegw.WakeV1Size {
		t.Fatalf("producer sealed %d bytes, want %d", len(env), remotegw.WakeV1Size)
	}
	return env
}

// TestR3A_NewPairingWakeKey_IsFreshPerPairing: ADR-015 P7. The key is phone-minted CSPRNG
// material, never zero, and never repeated across pairings -- one leaked pairing's key
// must say nothing about another's.
func TestR3A_NewPairingWakeKey_IsFreshPerPairing(t *testing.T) {
	a, err := NewPairingWakeKey()
	if err != nil {
		t.Fatalf("NewPairingWakeKey: %v", err)
	}
	b, err := NewPairingWakeKey()
	if err != nil {
		t.Fatalf("NewPairingWakeKey (second): %v", err)
	}
	var zero crypto.WakeKey
	if a == zero || b == zero {
		t.Fatal("a minted wake key is all zeroes")
	}
	if a == b {
		t.Fatal("two pairings received the same wake key")
	}
}

// TestR3A_WakeV1MaxAgeIsFiveMinutes: PG-WAKE-7. The v1 bound is a named constant, five
// minutes, matching the FCM TTL -- an expiry longer than the TTL is a replay window with
// no delivery behind it.
func TestR3A_WakeV1MaxAgeIsFiveMinutes(t *testing.T) {
	if WakeV1MaxAge != 5*time.Minute {
		t.Fatalf("WakeV1MaxAge = %v, want 5m (PG-WAKE-7)", WakeV1MaxAge)
	}
}

// TestR3A_AcceptWakeV1_AcceptsTheMachinesSealAndAdvancesDurably: the happy path plus the
// two properties that make it a wake path at all -- the coordinate advances, and it
// advances DURABLY (atomically persisted before routing, PG-WAKE-13 step 5), so a replay
// after process death is still a replay.
func TestR3A_AcceptWakeV1_AcceptsTheMachinesSealAndAdvancesDurably(t *testing.T) {
	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)
	addr, key := r3aBinding(t, core, 0xA1)

	env := r3aSeal(t, key, addr, 1, time.Now())
	if err := core.AcceptWakeV1(env); err != nil {
		t.Fatalf("AcceptWakeV1 on the machine's own seal: %v", err)
	}

	// The same bytes again, same process: replay.
	if err := core.AcceptWakeV1(env); !errors.Is(err, ErrWakeReplay) {
		t.Fatalf("replay in-process: got %v, want ErrWakeReplay", err)
	}

	// The same bytes after process death: still a replay -- the coordinate was persisted,
	// not held in memory.
	restarted := phone.resume(t)
	if err := restarted.AcceptWakeV1(env); !errors.Is(err, ErrWakeReplay) {
		t.Fatalf("replay across restart: got %v, want ErrWakeReplay (durable high-water)", err)
	}

	// A later seq from the same machine is still fine.
	if err := restarted.AcceptWakeV1(r3aSeal(t, key, addr, 2, time.Now())); err != nil {
		t.Fatalf("seq 2 after restart: %v", err)
	}
}

// TestR3A_AcceptWakeV1_EveryWireByteIsAuthenticated: the scope's "74 bytes, AAD-covered"
// stated as a mutation matrix. Flipping ANY single byte of a genuine envelope must refuse
// the wake, must not advance the coordinate, and must count the drop. (Bytes 0-1 refuse
// on shape before the AEAD -- PG-WAKE-3 -- which is still a refusal; every other byte is
// under the tag by PG-WAKE-8's tuple or IS the tag/nonce.)
func TestR3A_AcceptWakeV1_EveryWireByteIsAuthenticated(t *testing.T) {
	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)
	addr, key := r3aBinding(t, core, 0xA2)

	env := r3aSeal(t, key, addr, 1, time.Now())
	for i := 0; i < len(env); i++ {
		mutated := append([]byte(nil), env...)
		mutated[i] ^= 0x01
		if err := core.AcceptWakeV1(mutated); err == nil {
			t.Fatalf("byte %d: a mutated envelope was accepted", i)
		}
	}
	if drops := core.WakeDrops(); drops != uint64(len(env)) {
		t.Errorf("WakeDrops = %d after %d refused mutations, want %d (every refusal is counted)",
			drops, len(env), len(env))
	}

	// None of the 74 refusals advanced the coordinate: the genuine envelope still lands.
	if err := core.AcceptWakeV1(env); err != nil {
		t.Fatalf("the genuine envelope after the mutation matrix: %v (a refusal advanced the coordinate)", err)
	}
}

// TestR3A_AcceptWakeV1_FiveMinuteBoundIsEnforced: PG-WAKE-7 at the receiver. issued_at is
// AAD-covered, so the bound is checked against an AUTHENTICATED field; a stale capture is
// refused and counted, a fresh one inside the bound is accepted.
func TestR3A_AcceptWakeV1_FiveMinuteBoundIsEnforced(t *testing.T) {
	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)
	addr, key := r3aBinding(t, core, 0xA3)

	stale := r3aSeal(t, key, addr, 1, time.Now().Add(-WakeV1MaxAge-time.Second))
	if err := core.AcceptWakeV1(stale); !errors.Is(err, ErrWakeExpired) {
		t.Fatalf("a wake %v past issue: got %v, want ErrWakeExpired", WakeV1MaxAge+time.Second, err)
	}
	if drops := core.WakeDrops(); drops != 1 {
		t.Errorf("WakeDrops = %d after one expired wake, want 1", drops)
	}

	fresh := r3aSeal(t, key, addr, 2, time.Now().Add(-WakeV1MaxAge+10*time.Second))
	if err := core.AcceptWakeV1(fresh); err != nil {
		t.Fatalf("a wake inside the bound: %v", err)
	}
}

// TestR3A_AcceptWakeV1_CoordinatesArePerAddress: PG-WAKE-15. One machine's wake must
// never advance another machine's coordinate: machine A running at seq 5 must not make
// machine B's seq 1 look like a replay, and A's own replay guard is unaffected by B's
// acceptances.
func TestR3A_AcceptWakeV1_CoordinatesArePerAddress(t *testing.T) {
	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)
	addrA, keyA := r3aBinding(t, core, 0xAA)
	addrB, keyB := r3aBinding(t, core, 0xBB)

	if err := core.AcceptWakeV1(r3aSeal(t, keyA, addrA, 5, time.Now())); err != nil {
		t.Fatalf("machine A seq 5: %v", err)
	}
	if err := core.AcceptWakeV1(r3aSeal(t, keyB, addrB, 1, time.Now())); err != nil {
		t.Fatalf("machine B seq 1 after A reached 5: %v (A's coordinate leaked into B's)", err)
	}
	if err := core.AcceptWakeV1(r3aSeal(t, keyA, addrA, 5, time.Now())); !errors.Is(err, ErrWakeReplay) {
		t.Fatalf("machine A seq 5 replay: got %v, want ErrWakeReplay", err)
	}
	if err := core.AcceptWakeV1(r3aSeal(t, keyB, addrB, 2, time.Now())); err != nil {
		t.Fatalf("machine B seq 2: %v", err)
	}
}

// TestR3A_AcceptWakeV1_AnUnopenableEnvelopeCannotPinTheWindow: PG-WAKE-13's step-3-
// before-step-4, the invariant ADR-015 flags as most at risk from this migration. A
// forged envelope (wrong key) carrying seq 2^63 must be refused WITHOUT advancing the
// coordinate -- otherwise any party on the path owns a one-packet permanent denial of
// service against the phone's only background wake path.
func TestR3A_AcceptWakeV1_AnUnopenableEnvelopeCannotPinTheWindow(t *testing.T) {
	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)
	addr, key := r3aBinding(t, core, 0xA4)

	wrongKey, err := NewPairingWakeKey()
	if err != nil {
		t.Fatalf("NewPairingWakeKey: %v", err)
	}
	forged := r3aSeal(t, wrongKey, addr, 1<<63, time.Now())
	if err := core.AcceptWakeV1(forged); err == nil {
		t.Fatal("a forgery under the wrong key was accepted")
	}
	if drops := core.WakeDrops(); drops != 1 {
		t.Errorf("WakeDrops = %d after the forgery, want 1", drops)
	}

	// The genuine machine's seq 1 must still be accepted: the forgery's 2^63 did not pin
	// the window.
	if err := core.AcceptWakeV1(r3aSeal(t, key, addr, 1, time.Now())); err != nil {
		t.Fatalf("genuine seq 1 after the forgery: %v (the forgery advanced the coordinate)", err)
	}
}

// TestR3A_AcceptWakeV1_ShapesAreSeparatedBeforeTheAEAD: PG-WAKE-3. A type-0x01 (mailbox)
// or type-0x02 (legacy wake) byte at offset 1 is refused on shape, and the legacy
// 78-byte envelope is not accepted by the v1 opener either -- the two wake generations
// are separate paths under P12's compatibility window, never one lenient parser.
func TestR3A_AcceptWakeV1_ShapesAreSeparatedBeforeTheAEAD(t *testing.T) {
	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)
	addr, key := r3aBinding(t, core, 0xA5)

	env := r3aSeal(t, key, addr, 1, time.Now())
	for _, wrongType := range []byte{0x01, 0x02} {
		mutated := append([]byte(nil), env...)
		mutated[1] = wrongType
		if err := core.AcceptWakeV1(mutated); err == nil {
			t.Fatalf("type 0x%02x was accepted by the v1 opener", wrongType)
		}
	}

	// A legacy-sized (78-byte) buffer is refused outright -- over-length is not parsed.
	legacySized := append(append([]byte(nil), env...), 0, 0, 0, 0)
	if err := core.AcceptWakeV1(legacySized); err == nil {
		t.Fatal("a 78-byte buffer was accepted by the 74-byte v1 opener")
	}

	// And an under-length one.
	if err := core.AcceptWakeV1(env[:73]); err == nil {
		t.Fatal("a 73-byte buffer was accepted by the 74-byte v1 opener")
	}
}

// TestR3A_AcceptWakeV1_AnUnknownAddressIsTheWaitingVerdict: PG-WAKE-13 step 2. A wake
// naming an address this phone holds no key for is refused with ErrNoWakeKey -- the
// "waiting" verdict, never "invalid request" -- because a phone paired minutes ago and
// backgrounded before its binding landed is a healthy state that self-heals. The drop is
// still counted, and no coordinate is created for the unknown address.
func TestR3A_AcceptWakeV1_AnUnknownAddressIsTheWaitingVerdict(t *testing.T) {
	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)

	key, err := NewPairingWakeKey()
	if err != nil {
		t.Fatalf("NewPairingWakeKey: %v", err)
	}
	var unknown PushAddress
	for i := range unknown {
		unknown[i] = 0xEE
	}
	env := r3aSeal(t, key, unknown, 7, time.Now())
	if err := core.AcceptWakeV1(env); !errors.Is(err, ErrNoWakeKey) {
		t.Fatalf("wake for an unknown address: got %v, want ErrNoWakeKey (the waiting verdict)", err)
	}
	if drops := core.WakeDrops(); drops != 1 {
		t.Errorf("WakeDrops = %d after the unknown-address wake, want 1", drops)
	}

	// Adopting the binding afterwards starts the coordinate at 0: the refused wake's seq 7
	// left no residue, so the machine's real seq 1..7 all still deliver.
	if err := core.AdoptPushBinding(unknown, key); err != nil {
		t.Fatalf("AdoptPushBinding: %v", err)
	}
	if err := core.AcceptWakeV1(r3aSeal(t, key, unknown, 1, time.Now())); err != nil {
		t.Fatalf("seq 1 after adopting the binding: %v (the refused wake advanced a coordinate it must not have)", err)
	}
}

// TestR3A_AcceptWakeV1_SeqIsReadBigEndianFromOffset18: one deliberate cross-check of the
// wire table (spec section 5.1) against the producer, so the offsets cannot silently be
// re-derived on the phone side: the producer's uint64 BE at bytes 18..26 is the seq the
// receiver's replay window operates on.
func TestR3A_AcceptWakeV1_SeqIsReadBigEndianFromOffset18(t *testing.T) {
	phone := &r3aPhone{dir: t.TempDir(), wake: s14aNewSealer(t), content: s14aNewSealer(t)}
	core := phone.resume(t)
	addr, key := r3aBinding(t, core, 0xA6)

	const seq = uint64(0x0102030405060708)
	env := r3aSeal(t, key, addr, seq, time.Now())
	if got := binary.BigEndian.Uint64(env[18:26]); got != seq {
		t.Fatalf("producer wire check: seq at offset 18 is %#x, want %#x", got, seq)
	}
	if err := core.AcceptWakeV1(env); err != nil {
		t.Fatalf("AcceptWakeV1: %v", err)
	}
	// A lower seq is now a replay: the receiver read the same coordinate the producer wrote.
	if err := core.AcceptWakeV1(r3aSeal(t, key, addr, seq-1, time.Now())); !errors.Is(err, ErrWakeReplay) {
		t.Fatalf("seq-1 after accepting %#x: got %v, want ErrWakeReplay", seq, err)
	}
}
