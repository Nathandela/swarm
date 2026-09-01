package phonecore

// Wave R3 scopes 2-4, phone side: the per-pairing PUSH BINDING state and the WakeV1
// receiver (ADR-015 P6/P7/P8, push-gateway-api.md section 5.5).
//
// WHY A SEPARATE DURABLE FILE. State (state.go) is the pinned, versioned schema of the
// pre-gateway coordinates, and its field set is fenced in both directions
// (TestStateSchemaVersion_IsPinnedToTheDurableFieldSet). The push-binding table is a NEW
// per-address record set -- wake key, high-water, revocation verdict -- that the
// migration (P12) runs BESIDE the legacy scalar WakeReplay, not instead of it, so it
// lives in its own sealed container: <dir>/push-state.sealed, under the WAKE tier KEK,
// because the wake path is the one path that must read it with no user present and the
// content tier locked (PB-KEY-2). The legacy AcceptWake/WakeMaxAge path (wake.go) is not
// weakened, renamed or retargeted by anything here.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"golang.org/x/crypto/chacha20poly1305"
)

// WakeV1MaxAge is PG-WAKE-7's bound for the v1 receiver: five minutes, matching the FCM
// TTL -- an expiry longer than the TTL is a replay window with no delivery behind it. It
// is a NEW constant beside the legacy type-0x02 path's WakeMaxAge (10m), which P12 keeps
// until the migration retires it.
const WakeV1MaxAge = 5 * time.Minute

// The WakeV1 wire shape (spec section 5.1): one pinned size, a type byte distinct from
// the mailbox (0x01) and legacy wake (0x02) shapes, and the canonical AAD's domain
// string. These mirror internal/remotegw's producer constants; the cross-check that the
// two sides agree is r3a_wakev1_test.go opening the real producer's seal.
//
// TWO COPIES, PINNED BY TEST. ADR-015 P8 says "exactly one constant with schema tests
// that fail when it moves"; the producer/receiver custody split means each side carries
// its own copy -- remotegw's WakeV1Size/WakeV1Type/wakeV1Domain/WakeV1Expiry over there,
// these (and WakeV1MaxAge) here -- and the AAD only opens because WakeV1MaxAge equals
// WakeV1Expiry millisecond for millisecond. The compiler enforces nothing across the two
// blocks: what holds them together is r3a_wakev1_test.go opening the REAL producer's
// seal, so moving either side alone fails loudly there. Never "fix" one side alone; the
// twin comment obligation on remotegw's block is recorded in the round-2 evidence.
// WakeV1Size is exported for the ONE routing decision the facade owns: the FCM receipt
// (mobile.HandlePushWake) routes a payload of exactly this size to AcceptWakeV1 and
// everything else to the legacy receiver. It is the same pinned copy, not a third.
const (
	WakeV1Size         = 74
	wakeV1Type   uint8 = 0x03
	wakeV1Domain       = "swarm-wake-v1"
)

// wakeV1MaxFutureSkew is the LOWER bound's allowance on issued_at: a wake stamped further
// than this in the future is refused. Without it, a forward-skewed machine clock (or one
// bug in the obligation's issued_at stamp) hands a network capture a validity window as
// long as the skew plus WakeV1MaxAge -- the five-minute bound exists precisely so a
// captured envelope stops being valid on the wire (PG-WAKE-7). Two minutes matches the
// repo's existing clock-skew allowance (PG-AUTH-3's 120-second request horizon).
const wakeV1MaxFutureSkew = 2 * time.Minute

var (
	// ErrPushAddressRevoked refuses re-adopting an address a machine-side revoke severed
	// (ADR-015 P6, PG-WAKE-14): the successor of a revoked address is a DIFFERENT address
	// with its own high-water, so re-adoption is the pin-the-window lever handed back to
	// whoever captured old wakes.
	ErrPushAddressRevoked = errors.New("phonecore: push address was revoked; its successor is a different address")

	// errWakeV1Malformed refuses an envelope on shape before the AEAD is touched
	// (PG-WAKE-3): wrong length, wrong version, or a type byte belonging to another
	// generation.
	errWakeV1Malformed = errors.New("phonecore: not a 74-byte WakeV1 envelope")

	// errWakeV1Forged refuses an envelope that did not open under this address's wake
	// key: a forgery, or any single flipped bit of the AAD-covered bytes.
	errWakeV1Forged = errors.New("phonecore: WakeV1 envelope failed authentication")

	// ErrWakePeerClockAhead refuses a GENUINE envelope -- it opened, so every field is
	// authenticated -- stamped further than wakeV1MaxFutureSkew in the future. The guard
	// itself does not move (see the constant): without it a forward-skewed producer hands a
	// network capture a validity window as long as the skew plus WakeV1MaxAge.
	//
	// IT IS A SEPARATE SENTINEL FROM ErrWakeExpired BECAUSE THE TWO NAME DIFFERENT WORLDS.
	// remotegw stamps issued_at from the MACHINE clock and replays the SAME sealed bytes on
	// every retry (PG-WAKE-12), so a machine three minutes ahead has 100% of its wakes
	// refused forever -- a permanently dead wake path whose only trace used to be the opaque
	// WakeDrops total, indistinguishable from someone forging wakes. One is a clock an
	// operator can fix on a machine they own; the other is an attack. The counter they land
	// in tells them apart (WakeDropCounts).
	//
	// IT WRAPS ErrWakeExpired RATHER THAN REPLACING IT. Every caller that already routes on
	// "this wake is outside the freshness bound" keeps working unchanged -- the facade's
	// class mapping, the shipped two-sided-bound assertion -- and the new sentinel is a
	// REFINEMENT a caller may ask for, never a case an existing one silently stopped
	// matching. Nothing about the refusal itself moved.
	ErrWakePeerClockAhead = fmt.Errorf(
		"phonecore: WakeV1 envelope is stamped too far in the future; the sending machine's "+
			"clock is ahead of this phone's: %w", ErrWakeExpired)
)

// wakeDropPersistEvery bounds how far the durable refusal counter may lag the in-memory one
// in a LONG-LIVED process. See countWakeDropLocked for the two topologies this number
// reconciles.
const wakeDropPersistEvery = 64

// NewPairingWakeKey mints one phone-generated per-pairing wake key (ADR-015 P7): 32
// CSPRNG bytes, fresh per pairing, conveyed to the machine inside the authenticated
// pairing transcript and never given to the gateway.
func NewPairingWakeKey() (crypto.WakeKey, error) {
	var k crypto.WakeKey
	if _, err := rand.Read(k[:]); err != nil {
		return crypto.WakeKey{}, err
	}
	return k, nil
}

// pushStateFileName is the sealed push-binding container inside the state directory.
const pushStateFileName = "push-state.sealed"

// pushData is the container's plaintext: the durable installation identity (scope 1)
// with the last token it registered (staleness must be detectable at the seam) and the
// prepared registration awaiting replay (PG-REG-2 across calls), the per-address binding
// table with its high-waters (PG-WAKE-15), the machine-revoked address set (PG-WAKE-14),
// and the refused-wake counter ("dropped and counted, never acted on").
type pushData struct {
	InstallationID string `json:"installation_id,omitempty"`
	// InstallationPublicKey is the canonical SEC1 uncompressed P-256 public key of
	// Android's non-exportable Keystore signer. It is persisted before registration so a
	// restarted process can reattach only the byte-identical platform authority; the
	// private key never enters Go or this sealed container.
	InstallationPublicKey []byte `json:"installation_public_key,omitempty"`
	// LastFCMToken is the token the gateway last accepted (register or rotate). It is
	// what makes a stale snapshot DETECTABLE at this seam at all -- without it, a queued
	// caller's older token and a genuine rotation are the same PUT.
	LastFCMToken string `json:"last_fcm_token,omitempty"`
	// PendingRegister is a registration whose outcome is UNKNOWN (every attempt lost the
	// response). It is persisted BEFORE the first POST and replayed verbatim by the next
	// call: pushgw's idempotency cache is keyed on (Idempotency-Key, body), so only this
	// exact pair maps a maybe-processed POST back onto the installation it minted.
	PendingRegister *pendingRegisterRec `json:"pending_register,omitempty"`
	// InstallationKey is the durable P-256 installation private key in SEC1 DER
	// (installationkey.go), sealed here under the WAKE tier like everything else in this
	// container -- the tier that opens with no user present, which registration and rotation
	// both need. See that file for the PG-AUTH-2 deviation this custody represents.
	InstallationKey []byte           `json:"installation_key,omitempty"`
	Bindings        []pushBindingRec `json:"bindings,omitempty"`
	Revoked         []string         `json:"revoked,omitempty"`
	// PendingPairingRevokes is the durable compensation journal for an address
	// allocated while pairing has not committed. StagePushBinding adds the address
	// in the same sealed write that adopts its wake key. Pairing commit removes the
	// marker; every other exit drops the key and leaves the marker until the gateway
	// confirms revocation. Process death can therefore leak neither a live local key
	// nor an untracked public allocation.
	PendingPairingRevokes []string `json:"pending_pairing_revokes,omitempty"`
	// Drops is the refused-wake counter, and it is ONE record rather than a total beside a
	// breakdown: two fields that must sum to each other are two fields that drift. WakeDrops
	// reads its Total; WakeDropCounts hands back the whole record.
	Drops WakeDropCounts `json:"drops,omitzero"`
}

// WakeDropCounts is the refused-wake counter with enough structure for an operator to act
// on. It is the answer to "the wake path is dead and I cannot tell whether that is my
// machine's clock or an attacker": PeerClockAhead and Unauthenticated are different
// diagnoses with different remedies, and a single total distinguished neither.
//
// The field set is CLOSED and every field is one refusal arm of AcceptWakeV1, so the sum of
// the arms is Total by construction. It carries no address, no key, no sequence number and
// no timestamp: it is read on a screen and written to a durable file, and a per-address
// breakdown would put the pairing topology in both.
type WakeDropCounts struct {
	// Total is every refusal, the counter WakeDrops has always reported.
	Total uint64 `json:"total,omitempty"`
	// Malformed is a payload refused on SHAPE, before the AEAD was touched (PG-WAKE-3):
	// wrong length, wrong version, or a type byte from another wake generation.
	Malformed uint64 `json:"malformed,omitempty"`
	// NoKey is the WAITING verdict: this phone holds no wake key for the address, either
	// because the pairing has not landed yet or because a forget erased the key.
	NoKey uint64 `json:"no_key,omitempty"`
	// Revoked is a wake for an address a machine-side revoke severed forever (PG-WAKE-14).
	Revoked uint64 `json:"revoked,omitempty"`
	// Unauthenticated is a BAD MAC: the envelope did not open under this address's key. A
	// forgery, a corrupted delivery, or a wake sealed under a key this phone has replaced.
	Unauthenticated uint64 `json:"unauthenticated,omitempty"`
	// Replay is an authenticated envelope at or below the per-address high-water.
	Replay uint64 `json:"replay,omitempty"`
	// Expired is an authenticated envelope older than WakeV1MaxAge: a stale capture, or a
	// delivery the provider sat on past the TTL. A future-dated one is NOT counted here
	// even though its error wraps ErrWakeExpired -- it has its own bucket below, which is
	// the whole point of the split.
	Expired uint64 `json:"expired,omitempty"`
	// PeerClockAhead is an authenticated envelope stamped further than wakeV1MaxFutureSkew
	// in the future. It is the diagnosis the finding asked for: a machine whose clock runs
	// ahead has EVERY wake refused, permanently, and this is the only number that says so.
	PeerClockAhead uint64 `json:"peer_clock_ahead,omitempty"`
}

// pendingRegisterRec is the durable half of one prepared registration (see
// gatewayclient.go's preparedRegister). Body holds the attested wire bytes -- the
// verdict token inside is short-lived and single-purpose, and the container is sealed
// under the wake KEK like everything else here.
type pendingRegisterRec struct {
	IdemKey  string `json:"idem_key"`
	Body     []byte `json:"body"`
	FCMToken string `json:"fcm_token"`
}

// pushBindingRec is one pairing's (address, wake key, high-water) row. Address is the
// 22-character wire encoding so the JSON is inspectable once unsealed. WakeKeyHash
// (SHA-256 of the key) survives DropPushBinding's key erasure so a later re-adoption can
// tell "same pairing re-paired" (keep the high-water) from "new key on a reused address"
// (reset it) -- the hash opens nothing and authenticates nothing.
type pushBindingRec struct {
	Address     string `json:"address"`
	WakeKey     []byte `json:"wake_key"`
	WakeKeyHash []byte `json:"wake_key_hash,omitempty"`
	HighWater   uint64 `json:"high_water"`
}

// pushStore is the durable custody of pushData. It has no lock of its own: every access
// runs under Core.mu, exactly like Core.st.
type pushStore struct {
	path   string
	sealer Sealer
	data   pushData
}

// openPushStore loads (or initialises) the sealed push-binding container. An empty dir
// persists nothing, mirroring OpenStore. A container that exists but cannot be opened or
// parsed fails CLOSED: starting from an empty table would reset every per-address
// high-water to zero, which is exactly the replay hole the table exists to refuse.
func openPushStore(dir string, sealer Sealer) (*pushStore, error) {
	if dir == "" {
		return &pushStore{}, nil
	}
	s := &pushStore{path: filepath.Join(dir, pushStateFileName), sealer: sealer}
	blob, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open push state: %w", err)
	}
	plain, err := sealer.Open(blob)
	if err != nil {
		return nil, fmt.Errorf("unseal push state: %w", err)
	}
	if err := json.Unmarshal(plain, &s.data); err != nil {
		return nil, fmt.Errorf("decode push state: %w", err)
	}
	for _, enc := range s.data.PendingPairingRevokes {
		if _, err := decodePushAddress(enc); err != nil {
			return nil, fmt.Errorf("decode pending pairing revoke: %w", err)
		}
	}
	return s, nil
}

// persist seals and atomically rewrites the container. Memory-only stores (empty path)
// persist trivially.
func (s *pushStore) persist() error {
	if s.path == "" {
		return nil
	}
	plain, err := json.Marshal(s.data)
	if err != nil {
		return err
	}
	blob, err := s.sealer.Seal(plain)
	if err != nil {
		return fmt.Errorf("seal push state: %w", err)
	}
	return writeFileAtomic(s.path, ".push-state-*", blob)
}

func (s *pushStore) binding(addr string) *pushBindingRec {
	for i := range s.data.Bindings {
		if s.data.Bindings[i].Address == addr {
			return &s.data.Bindings[i]
		}
	}
	return nil
}

func (s *pushStore) revoked(addr string) bool {
	for _, r := range s.data.Revoked {
		if r == addr {
			return true
		}
	}
	return false
}

func (s *pushStore) removeBinding(addr string) {
	kept := s.data.Bindings[:0]
	for _, b := range s.data.Bindings {
		if b.Address != addr {
			kept = append(kept, b)
		}
	}
	s.data.Bindings = kept
}

// wakeKeyHash is the retained fingerprint of one wake key: SHA-256, so a tombstone can
// recognise its own key without holding it. Preimage-resistant, opens nothing.
func wakeKeyHash(key []byte) []byte {
	h := sha256.Sum256(key)
	return h[:]
}

func decodePushAddress(enc string) (PushAddress, error) {
	var addr PushAddress
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil || len(raw) != len(addr) {
		return addr, fmt.Errorf("invalid push address %q", enc)
	}
	copy(addr[:], raw)
	return addr, nil
}

func clonePushBindings(in []pushBindingRec) []pushBindingRec {
	out := make([]pushBindingRec, len(in))
	for i := range in {
		out[i] = in[i]
		out[i].WakeKey = append([]byte(nil), in[i].WakeKey...)
		out[i].WakeKeyHash = append([]byte(nil), in[i].WakeKeyHash...)
	}
	return out
}

func (c *Core) adoptPushBindingLocked(addr PushAddress, key crypto.WakeKey) error {
	enc := EncodePushAddress(addr)
	if c.push.revoked(enc) {
		return ErrPushAddressRevoked
	}
	if b := c.push.binding(enc); b != nil {
		// Re-adoption. The high-water guards envelopes sealed under ONE key, so its fate
		// follows the key: the SAME key (live, or a DropPushBinding tombstone recognised
		// by its retained hash) KEEPS the coordinate -- a forget/re-pair of the same
		// pairing never moves it backwards -- while a DIFFERENT key RESETS it, because
		// captures under the old key cannot open under the new one and a retained
		// coordinate would silently refuse the new pairing's first wakes up to the old
		// high-water, with no diagnostic.
		sameKey := false
		if len(b.WakeKey) != 0 {
			sameKey = bytes.Equal(b.WakeKey, key[:])
		} else if len(b.WakeKeyHash) != 0 {
			sameKey = bytes.Equal(b.WakeKeyHash, wakeKeyHash(key[:]))
		}
		b.WakeKey = append([]byte(nil), key[:]...)
		b.WakeKeyHash = wakeKeyHash(key[:])
		if !sameKey {
			b.HighWater = 0
		}
	} else {
		c.push.data.Bindings = append(c.push.data.Bindings, pushBindingRec{
			Address: enc, WakeKey: append([]byte(nil), key[:]...), WakeKeyHash: wakeKeyHash(key[:]),
		})
	}
	return nil
}

// AdoptPushBinding installs one pairing's (address, wake key) as the pairing commit
// will: the coordinate starts at zero for a fresh address (a wake refused before
// adoption leaves no residue, PG-WAKE-13 step 2), and a machine-revoked address is
// refused forever (ErrPushAddressRevoked).
func (c *Core) AdoptPushBinding(addr PushAddress, key crypto.WakeKey) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.adoptPushBindingLocked(addr, key); err != nil {
		return err
	}
	return c.push.persist()
}

// StagePushBinding durably adopts a newly allocated address immediately before the
// pairing protocol sends it in msg4. The same sealed write records the allocation as a
// pending compensation: until pairing commits, every exit owes the gateway a revoke.
func (c *Core) StagePushBinding(addr PushAddress, key crypto.WakeKey) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	bindingsBefore := clonePushBindings(c.push.data.Bindings)
	pendingBefore := append([]string(nil), c.push.data.PendingPairingRevokes...)
	if err := c.adoptPushBindingLocked(addr, key); err != nil {
		return err
	}
	enc := EncodePushAddress(addr)
	if !containsString(c.push.data.PendingPairingRevokes, enc) {
		c.push.data.PendingPairingRevokes = append(c.push.data.PendingPairingRevokes, enc)
	}
	if err := c.push.persist(); err != nil {
		c.push.data.Bindings = bindingsBefore
		c.push.data.PendingPairingRevokes = pendingBefore
		return err
	}
	return nil
}

// CommitStagedPushBinding marks a pairing accepted while retaining its live wake key.
// It is idempotent so a crash-replayed commit converges on the same sealed state.
func (c *Core) CommitStagedPushBinding(addr PushAddress) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.removePendingPairingRevokeLocked(EncodePushAddress(addr))
}

// AbandonStagedPushBinding drops the local wake key but retains the durable revoke
// obligation. The network revoke is deliberately performed by the caller after this
// short state transition, never while Core.mu is held.
func (c *Core) AbandonStagedPushBinding(addr PushAddress) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	enc := EncodePushAddress(addr)
	bindingsBefore := clonePushBindings(c.push.data.Bindings)
	pendingBefore := append([]string(nil), c.push.data.PendingPairingRevokes...)
	if b := c.push.binding(enc); b != nil {
		if len(b.WakeKey) != 0 && len(b.WakeKeyHash) == 0 {
			b.WakeKeyHash = wakeKeyHash(b.WakeKey)
		}
		b.WakeKey = nil
	}
	if !containsString(c.push.data.PendingPairingRevokes, enc) {
		c.push.data.PendingPairingRevokes = append(c.push.data.PendingPairingRevokes, enc)
	}
	if err := c.push.persist(); err != nil {
		c.push.data.Bindings = bindingsBefore
		c.push.data.PendingPairingRevokes = pendingBefore
		return err
	}
	return nil
}

// CompleteStagedPushRevoke clears the durable compensation only after the gateway has
// confirmed revocation. The key is defensively erased again, making retries safe.
func (c *Core) CompleteStagedPushRevoke(addr PushAddress) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	enc := EncodePushAddress(addr)
	bindingsBefore := clonePushBindings(c.push.data.Bindings)
	if b := c.push.binding(enc); b != nil {
		if len(b.WakeKey) != 0 && len(b.WakeKeyHash) == 0 {
			b.WakeKeyHash = wakeKeyHash(b.WakeKey)
		}
		b.WakeKey = nil
	}
	if err := c.removePendingPairingRevokeLocked(enc); err != nil {
		c.push.data.Bindings = bindingsBefore
		return err
	}
	return nil
}

// PendingPushBindingRevocations returns a stable snapshot for the network cleanup
// worker. Startup validates the sealed encodings, so decoding here cannot fail.
func (c *Core) PendingPushBindingRevocations() []PushAddress {
	c.mu.Lock()
	defer c.mu.Unlock()
	encoded := append([]string(nil), c.push.data.PendingPairingRevokes...)
	sort.Strings(encoded)
	out := make([]PushAddress, 0, len(encoded))
	for _, enc := range encoded {
		addr, err := decodePushAddress(enc)
		if err == nil {
			out = append(out, addr)
		}
	}
	return out
}

// StagedPushBindingPending reports whether addr still carries the durable pre-commit
// compensation obligation. Pairing rollback uses this after its callback returns: a binding
// whose pin-owned transaction already completed has no pending marker and must not be revoked
// merely because the final acknowledgement was lost.
func (c *Core) StagedPushBindingPending(addr PushAddress) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return containsString(c.push.data.PendingPairingRevokes, EncodePushAddress(addr))
}

func (c *Core) removePendingPairingRevokeLocked(enc string) error {
	pendingBefore := append([]string(nil), c.push.data.PendingPairingRevokes...)
	kept := c.push.data.PendingPairingRevokes[:0]
	for _, candidate := range c.push.data.PendingPairingRevokes {
		if candidate != enc {
			kept = append(kept, candidate)
		}
	}
	c.push.data.PendingPairingRevokes = kept
	if err := c.push.persist(); err != nil {
		c.push.data.PendingPairingRevokes = pendingBefore
		return err
	}
	return nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// DropPushBinding is the local half of the phone-initiated "forget this computer": the
// KEY is gone, durably, and every wake under it is refused (and counted) from that
// moment. The HIGH-WATER stays behind as a keyless tombstone, because deleting it is the
// replay hole the table exists to refuse: a later re-adoption of the same address (a
// re-pair) starting from zero would accept every wake captured before the forget. The
// key's HASH stays with it so that re-adoption can tell the same key from a fresh one
// (see AdoptPushBinding). The gateway half is GatewayClient.RevokeAddress.
func (c *Core) DropPushBinding(addr PushAddress) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if b := c.push.binding(EncodePushAddress(addr)); b != nil {
		if len(b.WakeKey) != 0 && len(b.WakeKeyHash) == 0 {
			b.WakeKeyHash = wakeKeyHash(b.WakeKey)
		}
		b.WakeKey = nil
	}
	return c.push.persist()
}

// HonorMachineRevoke severs a binding the MACHINE revoked, forever: wakes under the dead
// key are dropped and counted, the severance survives process death, and the address can
// never be re-adopted -- its successor is a different address (ADR-015 P6, PG-WAKE-14).
func (c *Core) HonorMachineRevoke(addr PushAddress) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	enc := EncodePushAddress(addr)
	c.push.removeBinding(enc)
	if !c.push.revoked(enc) {
		c.push.data.Revoked = append(c.push.data.Revoked, enc)
	}
	return c.push.persist()
}

// WakeDrops is the monotonic count of refused wakes: the scope's "an unverifiable wake
// is dropped and counted, never acted on", and the trace the degraded-push surface
// reads.
func (c *Core) WakeDrops() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.push.data.Drops.Total
}

// WakeDropCounts is the same counter with the reason attached: it is what separates "the
// machine I own has its clock three minutes ahead, so every wake it sends is correctly
// refused forever" (PeerClockAhead) from "somebody is sending this address forgeries"
// (Unauthenticated) -- two states that were indistinguishable while the only trace was a
// total. Durable, like the total, so the surface that reads it need not be the process that
// refused anything.
func (c *Core) WakeDropCounts() WakeDropCounts {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.push.data.Drops
}

// PushInstallationID is the durable installation identity, or "" while this phone has
// never successfully registered (including after a refused attestation, PG-AUTH-13).
func (c *Core) PushInstallationID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.push.data.InstallationID
}

// TokenSource answers with the provider's CURRENT registration token at call time.
//
// THE SHAPE IS THE FIX for the round-3 stale-rotation finding: two opaque token strings
// cannot be ordered by any phone-side rule, so a token taken as a snapshot BEFORE the
// registration lock can be installed durably by a caller that merely acted later than it
// read ("a phone silently never receives a wake"). EnsurePushRegistration therefore
// reads the token INSIDE the lock, at act time, from this source -- which is exactly
// what the Android callers hold: FirebaseMessaging.getToken answers with the current
// token, and onNewToken's argument is only ever the hint that it changed.
type TokenSource func() (string, error)

// EnsurePushRegistration is the durable orchestration the Kotlin token entry points call
// (scope 1): register when this installation has never registered, rotate or refresh
// (PG-AUTH-5 -- the same token re-presented still PUTs, never a dropped no-op) when it
// has, and fall back to a FRESH registration when the gateway no longer knows the
// installation -- the alternative is a phone that retries an unauthorized PUT forever
// and never receives another wake. A registration whose outcome was UNKNOWN (every
// attempt lost its response) is kept durable and REPLAYED here first (PG-REG-2 across
// calls), so a maybe-processed POST resolves to its installation instead of minting an
// orphan.
func (c *Core) EnsurePushRegistration(ctx context.Context, client *GatewayClient, token TokenSource) (PushRegistration, error) {
	// The WHOLE read-decide-network-write sequence runs under regMu (see the field's
	// comment in core.go): a caller that raced in behind a first registration must find
	// the durable id already written and ROTATE, never mint a second installation. The
	// token is read INSIDE the lock so every wire write carries the token that was
	// current when it was made -- the wire and the durable record can only END on the
	// newest one.
	c.regMu.Lock()
	defer c.regMu.Unlock()

	tok, err := token()
	if err != nil {
		return PushRegistration{}, fmt.Errorf("phonecore: read current push token: %w", err)
	}

	c.mu.Lock()
	id := c.push.data.InstallationID
	pending := c.push.data.PendingRegister
	c.mu.Unlock()

	if id != "" {
		err := client.RotateToken(ctx, id, tok)
		if err == nil {
			return PushRegistration{InstallationID: id}, c.persistPushIdentity(id, tok)
		}
		if !errors.Is(err, errGatewayUnauthorized) {
			// Everything else is REPORTED, never re-registered -- errRequestExpired above
			// all. That one means this phone's clock is outside PG-AUTH-3's horizon after
			// doSigned already corrected it and retried; the installation is fine, and
			// re-registering would mint a duplicate (the register POST carries no
			// signature, so it succeeds while the clock is still wrong) and orphan the live
			// one for 180 days holding this phone's FCM token.
			return PushRegistration{}, err
		}
		// The gateway forgot the installation (180-day expiry, or a rebuilt store):
		// fall through to a fresh registration. Only the UNDISCRIMINATED 401 reaches here.
		// A pending pair can coexist with a forgotten id (a previous fallback's lost
		// response) and is still replayed below -- it may name an installation the gateway
		// DID mint.
	}

	// A prior call's registration with an UNKNOWN outcome is replayed verbatim first:
	// same Idempotency-Key, byte-identical body. Inside pushgw's retention window a
	// processed POST answers with the installation it already minted.
	if pending != nil {
		reg, err := client.registerPrepared(ctx, preparedRegister(*pending))
		switch {
		case err == nil:
			if perr := c.persistPushIdentity(reg.InstallationID, pending.FCMToken); perr != nil {
				return PushRegistration{}, perr
			}
			if pending.FCMToken != tok {
				// The token moved while the outcome was unresolved: one ordinary rotate
				// brings the resolved installation to the current token.
				if rerr := client.RotateToken(ctx, reg.InstallationID, tok); rerr != nil {
					return PushRegistration{}, rerr
				}
				if perr := c.persistPushIdentity(reg.InstallationID, tok); perr != nil {
					return PushRegistration{}, perr
				}
			}
			return reg, nil
		case errors.Is(err, errRegisterOutcomeUnknown):
			// Still unresolved: the pair stays durable for the next call.
			return PushRegistration{}, err
		default:
			// A DEFINITIVE refusal: the pair will never mint (its verdict token may
			// simply have aged out). Clear it and register fresh below.
			if cerr := c.clearPendingRegister(); cerr != nil {
				return PushRegistration{}, cerr
			}
		}
	}

	prep, err := client.prepareRegister(tok)
	if err != nil {
		// Attestation refused client-side: nothing durable (PG-AUTH-13).
		return PushRegistration{}, err
	}
	// The pair is durable BEFORE the first POST: from here on, a lost response is
	// recoverable by replay instead of being a 180-day orphan.
	if err := c.storePendingRegister(prep); err != nil {
		return PushRegistration{}, err
	}
	reg, err := client.registerPrepared(ctx, prep)
	if err != nil {
		if errors.Is(err, errRegisterOutcomeUnknown) {
			return PushRegistration{}, err
		}
		// Definitive refusal (e.g. PG-AUTH-13): nothing durable remains -- a restart
		// comes back unregistered, honestly foreground-only.
		if cerr := c.clearPendingRegister(); cerr != nil {
			return PushRegistration{}, cerr
		}
		return PushRegistration{}, err
	}
	if perr := c.persistPushIdentity(reg.InstallationID, tok); perr != nil {
		return PushRegistration{}, perr
	}
	return reg, nil
}

// PushFCMToken is the token the gateway last accepted for this installation, or "" while
// none was ever registered. It exists so a stale snapshot is detectable at the seam.
func (c *Core) PushFCMToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.push.data.LastFCMToken
}

// persistPushIdentity records the accepted (installation, token) pair and clears any
// pending registration: an accepted wire write is the outcome the pending pair awaited.
func (c *Core) persistPushIdentity(id, tok string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.push.data.InstallationID = id
	c.push.data.LastFCMToken = tok
	c.push.data.PendingRegister = nil
	return c.push.persist()
}

func (c *Core) storePendingRegister(prep preparedRegister) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	rec := pendingRegisterRec(prep)
	c.push.data.PendingRegister = &rec
	return c.push.persist()
}

func (c *Core) clearPendingRegister() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.push.data.PendingRegister = nil
	return c.push.persist()
}

// AcceptWakeV1 authenticates one 74-byte WakeV1 envelope and, only if it is genuine,
// fresh and beyond the persisted per-address coordinate, advances that coordinate
// DURABLY. PG-WAKE-13's order, step by step:
//
//  1. parse; require version 0x01 and type 0x03 before touching the AEAD;
//  2. select the per-pairing wake key by push_address; none held is the WAITING verdict
//     (ErrNoWakeKey), never "invalid request";
//  3. OPEN under that key, which authenticates every field;
//  4. only then compare wake_seq against the per-address high-water and issued_at
//     against WakeV1MaxAge;
//  5. only then atomically persist the advanced high-water -- before anything routes.
//
// Step 3 before step 4 is the whole contract: an unopenable envelope carrying seq 2^63
// must refuse WITHOUT advancing the coordinate, or any party on the path owns a
// one-packet permanent denial of service against the phone's only background wake path.
// Every refusal, on every step, is counted (WakeDrops).
func (c *Core) AcceptWakeV1(raw []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(raw) != WakeV1Size || raw[0] != crypto.VersionV1 || raw[1] != wakeV1Type {
		return c.countWakeDropLocked(errWakeV1Malformed)
	}
	var addr PushAddress
	copy(addr[:], raw[2:18])
	enc := EncodePushAddress(addr)
	if c.push.revoked(enc) {
		return c.countWakeDropLocked(ErrPushAddressRevoked)
	}
	b := c.push.binding(enc)
	if b == nil || len(b.WakeKey) == 0 {
		// No row, or a DropPushBinding tombstone (high-water retained, key gone): either
		// way this phone holds no key for the address -- the waiting verdict.
		return c.countWakeDropLocked(ErrNoWakeKey)
	}
	var key crypto.WakeKey
	copy(key[:], b.WakeKey)

	seq, issuedMs, err := openWakeV1(key, raw)
	if err != nil {
		return c.countWakeDropLocked(err)
	}
	// AUTHENTICATED from here down.
	if seq <= b.HighWater {
		return c.countWakeDropLocked(ErrWakeReplay)
	}
	// The freshness bound is TWO-SIDED, and the two sides are REPORTED SEPARATELY: older
	// than WakeV1MaxAge is a stale capture (ErrWakeExpired), and further than
	// wakeV1MaxFutureSkew in the future is a forward-skewed producer whose envelope would
	// otherwise stay valid for the whole skew plus the five minutes
	// (ErrWakePeerClockAhead). Both refuse -- the guard is unchanged -- but only the second
	// one is a machine clock an operator can go and fix, and it is the one that kills the
	// wake path permanently and silently.
	age := time.Since(time.UnixMilli(issuedMs))
	if age < -wakeV1MaxFutureSkew {
		return c.countWakeDropLocked(ErrWakePeerClockAhead)
	}
	if age > WakeV1MaxAge {
		return c.countWakeDropLocked(ErrWakeExpired)
	}
	prev := b.HighWater
	b.HighWater = seq
	if err := c.push.persist(); err != nil {
		b.HighWater = prev
		return err
	}
	return nil
}

// countWakeDropLocked advances the drop counter -- the total and the cause's own bucket --
// for one refusal, and returns the cause.
//
// DURABILITY IS BOUNDED, NOT DROPPED, AND IT CONVERGES IN BOTH TOPOLOGIES. On Android the
// FCM receipt is a process that wakes, refuses and dies: a refused wake by definition
// triggers no adopt, accept or revoke that would persist the container, so a counter that
// waits for "the next real state change" is durably zero in the exact topology it exists to
// measure. The FIRST refusal of a process therefore persists the container once.
//
// The round-3 fix stopped there, with a per-process latch, and that was DISHONEST IN A
// FOREGROUND PROCESS -- one that lives for hours can accumulate an unbounded number of
// refusals behind a single persisted "1", so the number an operator reads after a crash
// understates by however long the app happened to stay up. So the latch is a BUDGET
// instead: after the first write, one more write per wakeDropPersistEvery unpersisted
// refusals. The durable counter therefore lags the truth by at most that many in any
// process, forever, while the attacker-driveable cost stays bounded at one container re-seal
// + fsync per wakeDropPersistEvery messages -- and a party able to deliver unverifiable data
// messages already pays a process spawn for each one.
//
// A refusal stands whether or not its persist does, so the persist error is not the verdict:
// the unpersisted count is only cleared by a write that succeeded, and the next refusal
// retries.
func (c *Core) countWakeDropLocked(cause error) error {
	c.push.data.Drops.Total++
	if bucket := c.wakeDropBucketLocked(cause); bucket != nil {
		*bucket++
	}
	c.wakeDropsUnpersisted++
	if !c.wakeDropPersisted || c.wakeDropsUnpersisted >= wakeDropPersistEvery {
		if c.push.persist() == nil {
			c.wakeDropPersisted = true
			c.wakeDropsUnpersisted = 0
		}
	}
	return cause
}

// wakeDropBucketLocked selects the per-cause counter one refusal belongs in. It returns nil
// for a cause with no bucket -- today only a Sealer/disk fault raised inside the opener --
// which still counts in the Total: an unclassified refusal must not vanish, and inventing a
// bucket for it would put a fault in a security diagnosis.
func (c *Core) wakeDropBucketLocked(cause error) *uint64 {
	d := &c.push.data.Drops
	switch {
	case errors.Is(cause, errWakeV1Malformed):
		return &d.Malformed
	case errors.Is(cause, ErrNoWakeKey):
		return &d.NoKey
	case errors.Is(cause, ErrPushAddressRevoked):
		return &d.Revoked
	case errors.Is(cause, errWakeV1Forged):
		return &d.Unauthenticated
	case errors.Is(cause, ErrWakeReplay):
		return &d.Replay
	case errors.Is(cause, ErrWakePeerClockAhead):
		return &d.PeerClockAhead
	case errors.Is(cause, ErrWakeExpired):
		return &d.Expired
	}
	return nil
}

// openWakeV1 opens one shape-checked envelope under key: the canonical AAD tuple of spec
// section 5.3 -- "swarm-wake-v1" || version || push_address || wake_seq || issued_at ||
// expires_at (derived, PG-WAKE-6) || nonce -- over a zero-length plaintext, exactly what
// internal/remotegw.SealWakeV1 produces.
func openWakeV1(key crypto.WakeKey, env []byte) (seq uint64, issuedMs int64, err error) {
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return 0, 0, err
	}
	seq = binary.BigEndian.Uint64(env[18:26])
	issuedMs = int64(binary.BigEndian.Uint64(env[26:34]))
	expiresMs := issuedMs + WakeV1MaxAge.Milliseconds()

	aad := make([]byte, 0, 78)
	aad = append(aad, wakeV1Domain...)
	aad = append(aad, env[0])
	aad = append(aad, env[2:18]...)
	aad = binary.BigEndian.AppendUint64(aad, seq)
	aad = binary.BigEndian.AppendUint64(aad, uint64(issuedMs))
	aad = binary.BigEndian.AppendUint64(aad, uint64(expiresMs))
	aad = append(aad, env[34:58]...)

	if _, err := aead.Open(nil, env[34:58], env[58:74], aad); err != nil {
		return 0, 0, errWakeV1Forged
	}
	return seq, issuedMs, nil
}
