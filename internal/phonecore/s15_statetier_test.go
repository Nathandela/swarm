// FAILING-FIRST (TDD RED, GG-5) tests for slice S15 / PB-STATE-9: WHICH TIER SEALS WHICH
// STATE.
//
// S14a sealed the two EPOCH KEYS, each under its own tier KEK. It sealed nothing else.
// persistState writes every other field of State as plain JSON -- and three of those fields
// are DECRYPTED SESSION CONTENT: Sessions (the journal-derived session model), Snapshots
// (server-rendered terminal grids) and OpOutcomes (durable command outcomes). So a locked
// handset holds the user's journal, their terminal contents and their command results in
// plaintext at rest. PB-KEY-7's lock purge clears them from MEMORY; nothing seals or purges
// them at REST.
//
// The existing acceptance does not see it. android/gate's PB-SEC-1 pair searches the state
// blob for the two epoch keys and the four device private scalars -- key material -- and
// finds them properly sealed. Session content was never in its needle set. That is this
// project's standing "requirement satisfiable while the defect ships" class, and PB-STATE-9
// is the requirement written to close it.
//
// PB-STATE-9 splits the state by tier and says why in its own words: "One undifferentiated
// 'sealed' would let the implementer pick whichever tier passes."
//
//   - WAKE tier: what the wake path must read WHILE LOCKED -- the push token and the dedup
//     coordinate (State.WakeReplay, PB-PUSH-3's persisted replay coordinate). The wake path
//     runs with no user present, so everything under this tier is reachable without the
//     biometric. That is why the tier is narrow.
//   - CONTENT tier: send-seq, receive high-waters, and the decrypted caches.
//
// Its acceptance is "a locked-device process can read only the wake-tier state".
//
// HOW THESE TESTS READ. Every tier assertion is measured from the BYTES the core writes into
// the state directory, never from a Go accessor. A Go accessor returning empty is satisfiable
// by a load path that drops a field it nonetheless wrote in the clear; an at-rest inventory
// that reports "sealed" beside a plaintext file is the same defect one level up, and is
// exactly why android/gate/keycustody_test.go exists in Go rather than in Kotlin. Absence of
// bytes is never the whole assertion either -- any re-encoding hides bytes -- so every sealed
// row is paired with the POSITIVE half S14a established: the material was handed to THAT
// TIER'S injected sealer, and never to the other one.
//
// THE FIXTURES ARE S14a's, deliberately reused rather than copied: s14aSealer is a real AEAD
// under a KEK this package never holds that also RECORDS every plaintext it seals, which is
// the positive half above, and s14aStateDirBytes/s14aFindMaterial already carry the recorded
// reasoning about what a byte search can and cannot see. A second copy is a second place for
// that reasoning to drift.

package phonecore

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
)

// ---------------------------------------------------------------------------
// Sentinels. Every value below is chosen so that finding it in a file is proof it came from
// this fixture: the strings carry an s15- prefix and a random-looking tail, and the numbers
// are 19-digit so their decimal rendering cannot collide with an unrelated counter or appear
// by chance inside ciphertext.
// ---------------------------------------------------------------------------

const (
	s15Machine              = "s15-machine-8f3a1c"
	s15MachineName          = "s15-hostname-1e6c93"
	s15RoutingID            = "s15-routing-4d7e2b"
	s15PushToken            = "s15-push-token-c19a6f"
	s15SessionID            = "s15-session-2b8d4e"
	s15SnapLine             = "s15-terminal-line-7a3f9c"
	s15OpID                 = "s15-op-id-5e1b8d"
	s15OpOutcome            = "s15-outcome-9c4a2f"
	s15QueuedOp             = "s15-queued-op-3d9f7b"
	s15PublicationID        = "s15-publication-91b3e7"
	s15PublicationText      = "s15-publication-text-f4c82a"
	s15PublicationEnvelope  = "s15-publication-envelope-6a2d1f"
	s15StaleStream          = "s15-stream-6f2e1a"
	s15RelayIncarnation     = "abcdef0123456789abcdef0123456789"
	s15DiscardRecoveryToken = "1234567890abcdef1234567890abcdef"
	// s15ItemText is the TRANSCRIPT's sentinel: the reconstructed body of one interaction item
	// (ADR-009). It is what the user and the agent actually said to each other, which makes it
	// the most revealing thing this file measures.
	s15ItemText = "s15-item-text-1e7c5a"

	s15EpochID         uint32 = 1000000007
	s15GrantEpoch      uint32 = 1000000009
	s15ReconciledEpoch uint32 = 1000000021

	s15SendCeiling               uint64 = 8100000000000000017
	s15ReceiveSeq                uint64 = 8200000000000000029
	s15GrantSeq                  uint64 = 8300000000000000037
	s15WakeReplay                uint64 = 8400000000000000041
	s15RelayCursor               uint64 = 8500000000000000053
	s15RosterRevision            uint64 = 8500000000000000059
	s15DiscardRecoveryGeneration uint64 = 8700000000000000067
	s15DiscardRecoveryCompleted  uint64 = 8700000000000000063
	s15LastHeardAt               int64  = 8600000000000000061
)

// s15Bucket is the receive bucket the high-water sentinel is keyed by. Its sender id is what
// identifies the record on disk (a uint64 seq alone would be ambiguous), so it is as
// recognisable as the strings above.
var s15Bucket = Bucket{Sender: [8]byte{0xB1, 0xB2, 0xB3, 0xB4, 0xB5, 0xB6, 0xB7, 0xB8}, Epoch: s15EpochID}

// s15StaleBucket is a SECOND bucket, so the Stale set's sentinel cannot be satisfied by the
// receive high-water's record.
var s15StaleBucket = Bucket{Sender: [8]byte{0xD1, 0xD2, 0xD3, 0xD4, 0xD5, 0xD6, 0xD7, 0xD8}, Epoch: s15EpochID}

func s15MachineStatic() []byte   { return s15Filled(0xC1) }
func s15MachineSignPub() []byte  { return s15Filled(0xC2) }
func s15MachineRelayPub() []byte { return s15Filled(0xC3) }

func s15RelaySPKIPin() []byte { return s15Filled(0xD4) }

const s15RelayTLSPolicy = "pinned_spki"
const s15OperatorNamespace = "owner"

func s15Filled(first byte) []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = first + byte(i)
	}
	return b
}

// s15EpochKeys are the two tier keys the fixture installs. They are the same recognisable
// pattern S14a uses; PB-KEY-2 already assigns their tiers, so they appear in the inventory as
// rows that pass today and prove its "sealed" arm can measure anything at all.
func s15EpochKeys() crypto.EpochKeys {
	var keys crypto.EpochKeys
	for i := range keys.WakeKey {
		keys.WakeKey[i] = byte(0x11 + i)
	}
	for i := range keys.ContentKey {
		keys.ContentKey[i] = byte(0x55 + i)
	}
	return keys
}

// s15State is a State with EVERY field carrying a sentinel, so one Save exercises the whole
// inventory at once.
func s15State() State {
	st := State{
		Keys:                      s15EpochKeys(),
		Machine:                   s15Machine,
		MachineName:               s15MachineName,
		MachineStatic:             s15MachineStatic(),
		MachineSignPub:            s15MachineSignPub(),
		MachineRelayAuthPub:       s15MachineRelayPub(),
		OperatorNamespace:         s15OperatorNamespace,
		RelaySPKIPin:              s15RelaySPKIPin(),
		RelayTLSPolicy:            s15RelayTLSPolicy,
		RoutingID:                 s15RoutingID,
		EpochID:                   s15EpochID,
		SendSeq:                   map[uint32]uint64{s15EpochID: s15SendCeiling},
		Receive:                   map[Bucket]uint64{s15Bucket: s15ReceiveSeq},
		GrantEpoch:                s15GrantEpoch,
		GrantSeq:                  s15GrantSeq,
		WakeReplay:                s15WakeReplay,
		RelayCursor:               s15RelayCursor,
		RelayIncarnation:          s15RelayIncarnation,
		DiscardRecoveryGeneration: s15DiscardRecoveryGeneration,
		DiscardRecoveryCompleted:  s15DiscardRecoveryCompleted,
		DiscardRecoveryToken:      s15DiscardRecoveryToken,
		RosterRevision:            s15RosterRevision,
		Sessions:                  []CachedSession{{SessionID: s15SessionID, Present: true}},
		Snapshots:                 []Snapshot{{Session: s15SessionID, Lines: []string{s15SnapLine}, Cols: 80, Rows: 24}},
		PendingOps:                []QueuedOp{{Op: s15QueuedOp, SessionID: s15SessionID}},
		PendingPublications: []PendingPublication{{
			LogicalID: s15PublicationID + "-logical", OperationID: s15PublicationID,
			Kind: PublicationComposer, SessionID: s15SessionID, SessionInstance: "s15-instance",
			Text: s15PublicationText, Machine: s15Machine, EpochID: s15EpochID,
			Target: s15RoutingID, AuthorityPub: s15MachineRelayPub(), Phase: PublicationSealed, Sequence: 71,
			Envelope: []byte(s15PublicationEnvelope), CreatedAt: time.Unix(1_700_000_000, 0),
			Command: schema.DeviceCommandAuth{
				Action: schema.ActionComposerSend, Machine: s15Machine, Session: s15SessionID,
				OperationID: s15PublicationID, ExpiresAt: time.Unix(1_700_000_060, 0),
			},
			Composer: &schema.ComposerSendReq{
				Session: s15SessionID, SessionInstance: "s15-instance", Text: s15PublicationText,
			},
		}},
		OpOutcomes:      map[string]schema.Control{s15OpID: {Op: "kill", Error: s15OpOutcome}},
		Stale:           map[Bucket]bool{s15StaleBucket: true},
		StaleStreams:    map[string]bool{s15StaleStream: true},
		PushToken:       s15PushToken,
		PushPreference:  PushPreference{Alerts: true},
		ReconciledEpoch: s15ReconciledEpoch,
		LastHeardAt:     s15LastHeardAt,
		Disowned:        true,
		Items: []Item{{
			SessionID: s15SessionID, ItemID: "s15-item-id", Kind: KindAgentMessage,
			Status: StatusCompleted, Text: s15ItemText,
		}},
		HistoryFloor:  map[string]bool{"s15-history-floor-session": true},
		HistoryCapped: map[string]bool{"s15-history-capped-session": true},
	}
	return st
}

// s15Provision brings up a phone with both tier KEKs, saves the whole sentinel state through
// the durable path, and returns the state directory and the two recording sealers.
func s15Provision(t *testing.T) (dir string, wake, content *s14aSealer) {
	t.Helper()
	dir = t.TempDir()
	wake, content = s14aNewSealer(t), s14aNewSealer(t)
	core, err := Resume(Config{Dir: dir, Machine: s15Machine, WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume with both tier KEKs: %v", err)
	}
	if err := core.Save(s15State()); err != nil {
		t.Fatalf("saving the sentinel state: %v", err)
	}
	return dir, wake, content
}

// ---------------------------------------------------------------------------
// Needle forms.
// ---------------------------------------------------------------------------

// s15Str is the needle set for a string field: JSON writes a string verbatim.
func s15Str(s string) [][]byte { return [][]byte{[]byte(s)} }

// s15Num is the needle set for an integer field. It carries the DECIMAL rendering (what JSON
// writes) and both fixed-width binary renderings, so an implementation that seals a binary
// encoding of the state rather than a JSON one is measured just as exactly. Any one form
// matching is a hit.
func s15Num(n uint64, width int) [][]byte {
	needles := [][]byte{[]byte(strconv.FormatUint(n, 10))}
	switch width {
	case 8:
		be, le := make([]byte, 8), make([]byte, 8)
		binary.BigEndian.PutUint64(be, n)
		binary.LittleEndian.PutUint64(le, n)
		needles = append(needles, be, le)
	case 4:
		be, le := make([]byte, 4), make([]byte, 4)
		binary.BigEndian.PutUint32(be, uint32(n))
		binary.LittleEndian.PutUint32(le, uint32(n))
		needles = append(needles, be, le)
	}
	return needles
}

// s15Raw is the needle set for a []byte field: the raw bytes and the base64 encoding/json
// writes them as.
func s15Raw(b []byte) [][]byte {
	return [][]byte{b, []byte(base64.StdEncoding.EncodeToString(b))}
}

// s15SenderID is the needle set for a bucket's sender key id: the raw 8 bytes and the hex
// string state.go encodes them as (a [8]byte is not a JSON map key).
func s15SenderID(b [8]byte) [][]byte {
	return [][]byte{b[:], []byte(hex.EncodeToString(b[:]))}
}

// s15Hits reports every file in which ANY of the needle forms appears.
func s15Hits(files map[string][]byte, needles [][]byte) []string {
	seen := map[string]bool{}
	for _, needle := range needles {
		for _, name := range s14aFindMaterial(files, needle) {
			seen[name] = true
		}
	}
	var out []string
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// s15Sealed reports whether sl was ever asked to seal a plaintext holding any needle form --
// i.e. whether that tier's KEK is what covers the field.
func s15Sealed(sl *s14aSealer, needles [][]byte) bool {
	for _, needle := range needles {
		if sl.s14aSealedMaterial(needle) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// The at-rest inventory, MEASURED.
// ---------------------------------------------------------------------------

// s15Tier is one State field's row in the inventory.
type s15Tier struct {
	// field is the State field this row measures. The part before any "." is matched against
	// the struct's real field set, so a field added to State with no row here fails.
	field string
	// tier is "wake", "content", or "" for a field PB-STATE-9 assigns to neither.
	tier string
	// needles are the byte forms whose presence in the state directory means this field is
	// readable at rest without any KEK.
	needles [][]byte
	// why records the reasoning for an UNASSIGNED row -- what is being measured and why the
	// measurement is what it is. Sealed rows take their reasoning from PB-STATE-9 itself.
	why string
}

// s15Inventory is the whole of State, one row per field, with the tier PB-STATE-9 assigns and
// the byte form each field is measured by.
//
// THE UNASSIGNED ROWS ARE A MEASUREMENT, NOT AN ENDORSEMENT. PB-STATE-9 names five content
// fields and two wake fields and stops; every other field of State is left without a tier.
// Rows with an empty tier therefore record what the core ACTUALLY writes in the clear today,
// so that the inventory is complete and any change to it has to be made deliberately rather
// than noticed later. Two of them are flagged in their own why: they are content by every
// argument PB-STATE-9 makes and the requirement simply does not name them.
func s15Inventory() []s15Tier {
	keys := s15EpochKeys()
	return []s15Tier{
		{field: "Machine", needles: s15Str(s15Machine),
			why: "the load path filters on it BEFORE any unseal (a blob belonging to another machine " +
				"is discarded wholesale), so it cannot itself be behind a seal"},
		{field: "MachineName", needles: s15Str(s15MachineName),
			why: "the hostname the machine published at pairing (agents-tracker-ksvb.1). It is a " +
				"DISPLAY LABEL for the coordinate above it, not a fact about a session, so it is the " +
				"same class as Machine itself -- and it must be readable with the content tier " +
				"LOCKED, because a push-woken process renders the machine's name on the notification " +
				"it woke for. A name behind the content seal would leave that surface naming the " +
				"machine `ep-` plus a hash on exactly the screens a person reads without unlocking"},
		{field: "MachineStatic", needles: s15Raw(s15MachineStatic()),
			why: "a public key pinned at pairing; public by definition, as device.key's cleartext " +
				"public half already is"},
		{field: "MachineSignPub", needles: s15Raw(s15MachineSignPub()),
			why: "a public key pinned at pairing"},
		{field: "MachineRelayAuthPub", needles: s15Raw(s15MachineRelayPub()),
			why: "a public key, and the destination the wake path needs to reach the machine at all"},
		{field: "OperatorNamespace", needles: s15Str(s15OperatorNamespace),
			why: "the authenticated public home coordinate paired with MachineRelayAuthPub; a background-resumed relay reconnect needs both without user presence"},
		{field: "RelaySPKIPin", needles: s15Raw(s15RelaySPKIPin()),
			why: "a hash of a public key from a public certificate; it is a REFUSAL criterion, not a " +
				"secret, and the wake path must apply it with no user present -- a pin behind the " +
				"content seal would leave a locked handset dialling unpinned or not at all"},
		{field: "RelayTLSPolicy", needles: s15Str(s15RelayTLSPolicy),
			why: "ADR-016 W1's named relay TLS policy, beside the pin it scopes: the wake path applies " +
				"it (W3, effectiveRelayPin/handsetSecurity) with no user present, so it must be readable " +
				"exactly like RelaySPKIPin above -- a policy behind the content seal would leave a locked " +
				"handset unable to tell which of its two verification modes to run"},
		{field: "RoutingID", needles: s15Str(s15RoutingID),
			why: "this phone's own relay routing id, derived from the relay-auth public key; the wake " +
				"path must state it with no user present"},
		{field: "EpochID", needles: s15Num(uint64(s15EpochID), 4),
			why: "the tier records are keyed by it -- resealTier carries a blob verbatim only into the " +
				"epoch it was sealed for, which it cannot do if the epoch is behind the same seal"},

		{field: "Keys.WakeKey", tier: "wake", needles: s15Raw(keys.WakeKey[:])},
		{field: "Keys.ContentKey", tier: "content", needles: s15Raw(keys.ContentKey[:])},

		{field: "SendSeq", tier: "content", needles: s15Num(s15SendCeiling, 8)},
		{field: "Receive", tier: "content", needles: append(s15Num(s15ReceiveSeq, 8), s15SenderID(s15Bucket.Sender)...)},

		{field: "GrantEpoch", needles: s15Num(uint64(s15GrantEpoch), 4),
			why: "the grant watermark. PB-STATE-9 does not name it; installing a grant needs the " +
				"device recipient key, which is content tier, so nothing reads it while locked either way"},
		{field: "GrantSeq", needles: s15Num(s15GrantSeq, 8),
			why: "the other half of the grant watermark; same reasoning"},

		{field: "WakeReplay", tier: "wake", needles: s15Num(s15WakeReplay, 8)},

		{field: "RelayCursor", needles: s15Num(s15RelayCursor, 8),
			why: "the relay mailbox read cursor. A replay-guard coordinate PB-STATE-9 does not name; " +
				"it discloses how far the phone has read, not what it read"},
		{field: "RelayIncarnation", needles: s15Str(s15RelayIncarnation),
			why: "an opaque mailbox identity; it reveals no content and must be available before " +
				"the content tier opens so a reset mailbox cannot inherit an old cursor"},
		{field: "DiscardRecoveryGeneration", needles: s15Num(s15DiscardRecoveryGeneration, 8),
			why: "a monotonic synchronization generation proving an explicit mailbox recovery began"},
		{field: "DiscardRecoveryCompleted", needles: s15Num(s15DiscardRecoveryCompleted, 8),
			why: "the monotonic completion coordinate paired with the recovery generation"},
		{field: "DiscardRecoveryToken", needles: s15Str(s15DiscardRecoveryToken),
			why: "an opaque random correlation token carrying no mailbox or session content; the restart path " +
				"must read it before issuing the idempotent transport recovery"},
		{field: "RosterRevision", needles: s15Num(s15RosterRevision, 8),
			why: "the generation proving an authoritative roster committed; it reveals neither " +
				"the rows nor their content and must be readable before the content tier opens"},

		{field: "Sessions", tier: "content", needles: s15Str(s15SessionID)},
		{field: "Snapshots", tier: "content", needles: s15Str(s15SnapLine)},
		{field: "OpOutcomes", tier: "content", needles: append(s15Str(s15OpID), s15Str(s15OpOutcome)...)},
		// The TRANSCRIPT (ADR-009). Content tier and PURGEABLE, by both of PB-STATE-9's tests:
		// it is a decrypted machine-sealed cache like Sessions and Snapshots above it, and it
		// is the conversation itself -- prose, file paths, diffs and the summaries of pending
		// permissions -- so a locked handset reading it in the clear would disclose more than
		// the other three combined.
		{field: "Items", tier: "content", needles: s15Str(s15ItemText)},
		{field: "HistoryFloor", tier: "content", needles: s15Str("s15-history-floor-session")},
		{field: "HistoryCapped", tier: "content", needles: s15Str("s15-history-capped-session")},

		// REASSIGNED, on this row's own instruction. It was written UNASSIGNED and FLAGGED --
		// "the offline mutating-op queue holds session ids and, for a launch, the command line
		// the user typed, user content by any reading", measured in the clear and reported
		// rather than silently reassigned. PB-STATE-9 was AMENDED on that evidence
		// (2026-07-25, amendment 3): PendingOps is content tier and NON-purgeable. Content
		// because it is user content; non-purgeable because it is not a DECRYPTED cache --
		// nothing machine-sealed produced it -- so PB-KEY-7's lock purge must leave it, which
		// is why it sits in the content tier's KEPT container beside the replay-guard
		// coordinates rather than in the purgeable one beside the caches.
		{field: "PendingOps", tier: "content", needles: s15Str(s15QueuedOp)},
		{field: "PendingPublications", tier: "content", needles: append(
			append(s15Str(s15PublicationID), s15Str(s15PublicationText)...),
			s15Str(s15PublicationEnvelope)...),
		},

		{field: "Stale", needles: s15SenderID(s15StaleBucket.Sender),
			why: "which buckets have a hole in them. It is the record that content is untrustworthy, " +
				"not the content"},
		{field: "StaleStreams", needles: s15Str(s15StaleStream),
			why: "same, per repair channel -- and it deliberately SURVIVES the lock purge (state.go), " +
				"so a tier that withholds it while locked would present a known-holed stream as live, " +
				"which PB-APP-8 forbids"},

		{field: "PushToken", tier: "wake", needles: s15Str(s15PushToken)},

		{field: "PushPreference", needles: nil,
			why: "NOT MEASURABLE BY SENTINEL. Two booleans have four states between them and every " +
				"byte form of each appears throughout any file, so no needle distinguishes 'written in " +
				"the clear' from 'absent'. Its tier is asserted by round trip instead, in " +
				"TestS15_ALockedProcessReadsOnlyTheWakeTierState"},

		{field: "LastHeardAt", needles: s15Num(uint64(s15LastHeardAt), 8),
			why: "PB-APP-11's freshness coordinate: the newest authenticated machine timestamp this " +
				"phone has accepted. It is the record of how OLD the content is, not the content -- the " +
				"same class as Stale and StaleStreams, and unassigned for the same reason -- and it must " +
				"be readable while the content tier is LOCKED, because a phone that comes back from a " +
				"lock without it would present its restored caches as live"},

		{field: "ReconciledEpoch", needles: s15Num(uint64(s15ReconciledEpoch), 4),
			why: "records that a fold of the machine's rollback authorities happened; adopting one " +
				"needs the content key, so nothing reads it while locked"},

		{field: "Disowned", needles: s15Str(`"disowned":true`),
			why: "the revoke's own verdict: this registration is over (PB-KEY-7, agents-tracker-d0b8). " +
				"It is the record that the pairing ENDED rather than anything the pairing protected -- " +
				"the same class as Stale and LastHeardAt -- and it must be readable with the content " +
				"tier locked, which after a revoke is where the phone permanently is: the purge " +
				"destroyed the tier, so a verdict sealed under it could never be read again and every " +
				"launch would come up believing the phone is paired. THE NEEDLE IS THE KEY AND THE " +
				"VALUE TOGETHER, which no other row needs: a Boolean has no distinctive byte form of " +
				"its own, so `true` alone would match any other flag on disk"},
	}
}

// TestS15_TheAtRestInventoryIsCompleteAndMeasuredFromTheBytes is PB-STATE-9's inventory, one
// row per State field, every row measured from what the core wrote into the state directory.
//
// It fails in three independent ways, and each is a different defect:
//
//   - a field PB-STATE-9 seals that is readable at rest without any KEK. That is the headline:
//     Sessions, Snapshots and OpOutcomes are decrypted session content and they go to disk as
//     plain JSON today.
//   - a field PB-STATE-9 leaves UNASSIGNED whose at-rest visibility changed. The row records a
//     measurement; if a later slice seals one, that may well be right, but it is a decision
//     and it updates the row.
//   - a field of State with no row at all. An inventory that silently misses a field is the
//     defect class this whole test exists for.
//
// The unassigned rows are also this test's own positive control. They assert their sentinel IS
// present, so a run against an empty or truncated state directory -- under which every
// "absent" assertion passes vacuously -- fails here instead.
func TestS15_TheAtRestInventoryIsCompleteAndMeasuredFromTheBytes(t *testing.T) {
	dir, wake, content := s15Provision(t)
	files := s14aStateDirBytes(t, dir)
	if _, ok := files[StateFileName]; !ok {
		t.Fatalf("PB-STATE-9: %s is not in the state directory after a Save; every assertion below "+
			"would measure nothing", StateFileName)
	}

	inventory := s15Inventory()
	for _, row := range inventory {
		if row.needles == nil {
			continue
		}
		hits := s15Hits(files, row.needles)

		switch row.tier {
		case "":
			if len(hits) == 0 {
				t.Errorf("PB-STATE-9: State.%s is NOT readable at rest, but this inventory measured it "+
					"in the clear when it was written (%s). Either the sentinel never reached disk -- in "+
					"which case every 'absent' assertion in this file is measuring nothing -- or a tier "+
					"was assigned to this field. If it was assigned, that is a decision: update this row "+
					"and record it", row.field, row.why)
			}
		case "wake", "content":
			if len(hits) > 0 {
				t.Errorf("PB-STATE-9: State.%s is sealed under the %s tier, and it sits in the clear in "+
					"%v. A locked-device process may read only the WAKE tier; anything readable with no "+
					"KEK at all is readable by a locked device, by an ADB backup and by a restored image",
					row.field, row.tier, hits)
			}
			// Absence alone is satisfiable by any re-encoding, and by a field that was simply
			// dropped. The seam must be what removed it, and it must be THIS tier's seam: a
			// content field that went through the wake KEK is reachable with no user present,
			// which is the collapse the split exists to prevent, and a wake field that went
			// through the content KEK cannot be read on a push at all.
			mine, other, otherName := content, wake, "wake"
			if row.tier == "wake" {
				mine, other, otherName = wake, content, "content"
			}
			if !s15Sealed(mine, row.needles) {
				t.Errorf("PB-STATE-9: State.%s was never handed to the %s-tier sealer, so whatever removed "+
					"it from the state file was not this seam and no external KEK protects it",
					row.field, row.tier)
			}
			if s15Sealed(other, row.needles) {
				t.Errorf("PB-STATE-9: State.%s is %s tier but was sealed under the %s KEK. The tier split "+
					"is the requirement; sealing under whichever tier passes is what PB-STATE-9 exists to "+
					"make impossible", row.field, row.tier, otherName)
			}
		default:
			t.Fatalf("the inventory row for State.%s names tier %q, which is not a PB-KEY-2 tier",
				row.field, row.tier)
		}
	}

	// Completeness. A field added to State with no row here is an inventory that reports on
	// something smaller than the state it describes.
	covered := map[string]bool{}
	for _, row := range inventory {
		covered[strings.SplitN(row.field, ".", 2)[0]] = true
	}
	typ := reflect.TypeOf(State{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			// purgeGen is custody's own bookkeeping and is never persisted; state.go says so and
			// persistState has no field for it.
			continue
		}
		if !covered[f.Name] {
			t.Errorf("PB-STATE-9: State.%s has no row in the at-rest inventory. Every field must be "+
				"assigned a tier or recorded as deliberately unassigned; a field nobody classified is "+
				"how decrypted content reached disk in the clear in the first place", f.Name)
		}
	}
	for name := range covered {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("the at-rest inventory has a row for State.%s, which does not exist", name)
		}
	}
}

// ---------------------------------------------------------------------------
// The acceptance criterion itself: a locked device reads the wake tier and nothing else.
// ---------------------------------------------------------------------------

// TestS15_ALockedProcessReadsOnlyTheWakeTierState is PB-STATE-9's stated acceptance, driven
// through the real Resume with the content tier refusing exactly as an auth-gated Keystore key
// does when the user has not authenticated.
//
// It is the COMPLEMENT of the byte inventory above, not a substitute for it: the inventory
// says the content tier is unreadable without the content KEK, and this says the wake tier
// stays readable without it. Neither alone is the requirement -- sealing everything under the
// content KEK passes the first and bricks the phone, and sealing everything under the wake KEK
// passes the second and collapses the split.
//
// Resume itself MUST succeed here. A phone that cannot start without the biometric cannot
// receive the push that asks for it.
func TestS15_ALockedProcessReadsOnlyTheWakeTierState(t *testing.T) {
	dir, wake, content := s15Provision(t)

	content.openErr = crypto.ErrKeyAuthRequired
	locked, err := Resume(Config{Dir: dir, Machine: s15Machine, WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("PB-STATE-9: Resume with the content tier locked returned %v. The wake path comes up "+
			"with no user present, so this must succeed", err)
	}
	st := locked.State()

	// The wake tier: readable with no user present, because the wake path has to read it.
	if st.PushToken != s15PushToken {
		t.Errorf("PB-STATE-9: a locked process reads PushToken %q, want %q. The push token is WAKE tier: "+
			"a token the app cannot read while locked cannot be re-registered on the path that runs "+
			"while locked (PB-PUSH-9)", st.PushToken, s15PushToken)
	}
	if st.WakeReplay != s15WakeReplay {
		t.Errorf("PB-STATE-9: a locked process reads WakeReplay %d, want %d. The dedup coordinate is "+
			"WAKE tier: a push replay window that cannot be consulted while locked is not a replay "+
			"window at all (PB-PUSH-3)", st.WakeReplay, s15WakeReplay)
	}
	if st.Keys.WakeKey != s15State().Keys.WakeKey {
		t.Error("PB-KEY-2: a locked process cannot read the epoch wake key, so it cannot open the push " +
			"envelope the wake tier exists for")
	}

	// The content tier: NOT readable. Each of these is a separate consequence, so they are
	// reported separately rather than as one "state is not empty".
	if len(st.SendSeq) != 0 {
		t.Errorf("PB-STATE-9: a locked process read the send-seq ceilings %v. Send-seq is CONTENT tier",
			st.SendSeq)
	}
	if len(st.Receive) != 0 {
		t.Errorf("PB-STATE-9: a locked process read the receive high-waters %v. They are CONTENT tier",
			st.Receive)
	}
	if len(st.Sessions) != 0 {
		t.Errorf("PB-STATE-9: a locked process read %d cached session(s) -- the decrypted journal model. "+
			"PB-KEY-7 purges this from memory on lock; a copy readable at rest by a locked process is "+
			"the same content by another route", len(st.Sessions))
	}
	if len(st.Snapshots) != 0 {
		t.Errorf("PB-STATE-9: a locked process read %d terminal snapshot(s) -- server-rendered grids of "+
			"the user's terminal", len(st.Snapshots))
	}
	if len(st.OpOutcomes) != 0 {
		t.Errorf("PB-STATE-9: a locked process read %d durable operation outcome(s) -- the decrypted "+
			"reply cache PB-KEY-7 names beside sessions and snapshots", len(st.OpOutcomes))
	}
	if st.Keys.ContentKey != (crypto.ContentKey{}) {
		t.Error("PB-KEY-2: a locked process read the epoch content key")
	}

	// PushPreference has no byte-distinguishable sentinel (see its inventory row), so its tier
	// is asserted here instead. It is UNASSIGNED by PB-STATE-9 and measured to be readable
	// while locked; the settings screen renders it, and rendering a toggle is not reading
	// session content.
	if !st.PushPreference.Alerts {
		t.Error("PB-STATE-9: a locked process cannot read PushPreference. It is unassigned by the " +
			"requirement and measured readable; if it moved to the content tier that is a decision, and " +
			"this assertion and its inventory row are where it gets recorded")
	}
}

// ---------------------------------------------------------------------------
// The brick the tier split can cause, and the one it must not.
// ---------------------------------------------------------------------------

// TestS15_ASaveWhileLockedNeitherExposesNorDestroysTheContentTier is the class-(ii) fence:
// a plausible-looking implementation that hides a brick.
//
// The wake path SAVES while the content tier is locked. state.go says so in resealTier's own
// comment -- "this is the wake path's NORMAL condition" -- and a push token rotation
// (PB-PUSH-9's onNewToken) is exactly such a write: it arrives with no user present and must
// reach disk. A locked process cannot READ the content tier, so it holds zero for every
// content-tier coordinate, and the two obvious implementations both fail:
//
//   - rewrite the content tier from what the process holds. The send-seq ceiling goes to zero,
//     and after the unlock the phone renumbers from 1 under an epoch the gateway already holds
//     a high-water for -- so every keystroke, take_control, launch and kill is stale-dropped
//     for the life of the epoch, silently. The receive high-waters go with it and the replay
//     guard is blind.
//   - write the content tier in the clear because it is "only counters". That is the defect
//     this slice exists to close, and it is invisible to a test that only checks the state
//     survives.
//
// So both halves are asserted against ONE locked Save: nothing content-tier appears in the
// clear, and everything content-tier is still there after the unlock. The already-solved
// mechanism is resealTier's: carry a tier this process could not open VERBATIM.
func TestS15_ASaveWhileLockedNeitherExposesNorDestroysTheContentTier(t *testing.T) {
	dir, wake, content := s15Provision(t)
	before := s15State()

	content.openErr = crypto.ErrKeyAuthRequired
	locked, err := Resume(Config{Dir: dir, Machine: s15Machine, WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("PB-STATE-9: Resume with the content tier locked: %v", err)
	}

	// The wake-path write: a rotated push token and an advanced dedup coordinate, saved from a
	// process holding nothing of the content tier -- which is what the locked state genuinely
	// looks like once the split is real.
	const rotated = "s15-rotated-token-0d6b3e"
	st := locked.State()
	st.PushToken = rotated
	st.WakeReplay = s15WakeReplay + 1
	if err := locked.Save(st); err != nil {
		t.Fatalf("PB-PUSH-9: a wake-tier Save with the content tier locked failed: %v. The token "+
			"rotation arrives with no user present and must reach disk", err)
	}

	// Half one: the locked Save did not put content-tier state on disk in the clear.
	files := s14aStateDirBytes(t, dir)
	for _, row := range s15Inventory() {
		if row.tier != "content" || row.needles == nil {
			continue
		}
		if hits := s15Hits(files, row.needles); len(hits) > 0 {
			t.Errorf("PB-STATE-9: after a Save taken with the content tier LOCKED, State.%s is in the "+
				"clear in %v", row.field, hits)
		}
	}

	// Half two: and it did not destroy it either. A fresh Resume with the real content KEK is
	// the strongest reader there is.
	content.openErr = nil
	unlocked, err := Resume(Config{Dir: dir, Machine: s15Machine, WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("PB-STATE-9: Resume after the unlock: %v", err)
	}
	after := unlocked.State()

	if got := after.SendSeq[s15EpochID]; got != s15SendCeiling {
		t.Errorf("PB-STATE-3: the durable send-seq ceiling is %d after a Save taken while locked, want "+
			"%d. A ceiling that moved BACKWARDS renumbers the phone under an epoch the gateway already "+
			"holds a high-water for, and every frame it sends is stale-dropped for the life of that "+
			"epoch", got, s15SendCeiling)
	}
	if got := after.Receive[s15Bucket]; got != s15ReceiveSeq {
		t.Errorf("PB-STATE-1: the receive high-water for the sentinel bucket is %d, want %d. A reset "+
			"high-water leaves the replay guard blind and re-opens every frame the relay still retains",
			got, s15ReceiveSeq)
	}
	if len(after.Sessions) != len(before.Sessions) {
		t.Errorf("PB-STATE-9: the cached session model has %d entries after the unlock, want %d",
			len(after.Sessions), len(before.Sessions))
	}
	if len(after.Snapshots) != len(before.Snapshots) {
		t.Errorf("PB-STATE-9: the cached snapshots have %d entries after the unlock, want %d",
			len(after.Snapshots), len(before.Snapshots))
	}
	if len(after.OpOutcomes) != len(before.OpOutcomes) {
		t.Errorf("PB-SYNC-2: the durable operation outcomes have %d entries after the unlock, want %d. "+
			"An op whose outcome was lost never settles", len(after.OpOutcomes), len(before.OpOutcomes))
	}
	if after.Keys.ContentKey != before.Keys.ContentKey {
		t.Error("PB-KEY-2: the sealed epoch content key did not survive a Save taken while the tier was " +
			"locked. resealTier already solves exactly this for the key; the content-tier STATE needs " +
			"the same treatment")
	}

	// And the wake-tier write itself must have landed, or the locked Save was a no-op and the
	// two halves above are measuring nothing.
	if after.PushToken != rotated {
		t.Errorf("PB-PUSH-9: the rotated push token is %q after the unlock, want %q. The locked Save "+
			"did not reach disk, so this test measured nothing", after.PushToken, rotated)
	}
}

// TestS15_ThePurgeStillReachesTheSealedContentTier. PB-KEY-7's purge destroys what is behind
// the content KEK, and it must do so from a process that never opened that tier -- destroying
// a blob has never required being able to read it (PurgeKeys' own comment). The failure this
// fences is a purge that, unable to open the content tier, carries it verbatim by the very
// mechanism the test above requires: the journal, the snapshots and the outcomes then stay at
// rest, which is the state PB-KEY-7 exists to prevent.
//
// The purge is taken from a process that has not opened the tier, because a revoke can arrive
// at one -- the push path never opens it (PB-KEY-2 is enforced by code discipline since
// ADR-007 B133), and the revoke is issued from the computer.
//
// LEGITIMATE PASSER TODAY. Nothing is sealed yet, so the purge reaches the caches trivially
// and this is green on the RED run. It is the fence on the GREEN: the failure it names only
// becomes reachable once the content tier exists and carrying-verbatim becomes an option.
func TestS15_ThePurgeStillReachesTheSealedContentTier(t *testing.T) {
	dir, wake, content := s15Provision(t)

	content.openErr = crypto.ErrKeyAuthRequired
	locked, err := Resume(Config{Dir: dir, Machine: s15Machine, WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume with the content tier locked: %v", err)
	}
	if err := locked.PurgeKeys(); err != nil {
		t.Fatalf("PB-KEY-7: the purge failed: %v. It must run from a process that never opened the "+
			"content tier, so it may not need the KEK", err)
	}

	// The strongest reader there is: a fresh Resume holding BOTH real KEKs.
	content.openErr = nil
	reopened, err := Resume(Config{Dir: dir, Machine: s15Machine, WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume after the purge: %v", err)
	}
	st := reopened.State()

	if len(st.Sessions) != 0 || len(st.Snapshots) != 0 || len(st.OpOutcomes) != 0 {
		t.Errorf("PB-KEY-7: after a purge taken with the content tier unopened, the decrypted caches "+
			"are still recoverable through the seal: %d session(s), %d snapshot(s), %d outcome(s). A "+
			"purge that carried the tier verbatim because it could not open it destroyed nothing",
			len(st.Sessions), len(st.Snapshots), len(st.OpOutcomes))
	}
	// AND THE SEALED CONTENT KEY GOES WITH THEM. This test asserted the opposite through the
	// ADR-007 B35/B36 round, and correctly: while the trigger was a SCREEN LOCK, destroying the
	// blob was a permanent brick, because PB-KEY-10 delivers the epoch key as a machine-sealed
	// grant inside Go and the grant watermark refuses a re-delivery as a replay. B133 moves the
	// trigger to revoke/unpair, where "no way back without the machine" is the outcome rather
	// than the cost: re-pairing mints a fresh epoch and fresh keys.
	if st.Keys.ContentKey != (crypto.ContentKey{}) {
		t.Errorf("PB-KEY-7: the revoke purge left the SEALED content key at rest, so a restart "+
			"recovers it with no re-pairing at all: %x", st.Keys.ContentKey)
	}

	// AND THE CONTENT TIER IS STILL NOT ONE THING, for a reason the purge no longer carries.
	// dropContentMaterial -- the drop a Save takes when the tier is unopened -- destroys the
	// content key and the three decrypted caches while the replay-guard coordinates are carried
	// verbatim by a writer that cannot read them. That two-container split is a correctness
	// argument about renumbering, independent of any purge, and it is what the locked-Save case
	// one test over depends on. A REVOKE takes the whole tier, counters included: the epoch is
	// dead, so there is no stream left for a watermark to guard.
	if got := st.SendSeq[s15EpochID]; got != 0 {
		t.Errorf("PB-KEY-7: the revoke purge left the send-seq ceiling at rest (%d). The epoch it "+
			"guards died with the pairing", got)
	}
	if got := st.Receive[s15Bucket]; got != 0 {
		t.Errorf("PB-KEY-7: the revoke purge left the receive high-water at rest (%d)", got)
	}

	// The wake tier goes too. ADR-007 B9/B16 spared it from a LOCK -- a push arrives with nobody
	// there, and a handset that stops being wakeable in a pocket is broken -- and a revoked
	// handset must not be wakeable at all. The push token is the same door from the provider's
	// side, which is what PB-PUSH-9 requires deleted on revoke.
	if st.PushToken != "" {
		t.Errorf("PB-KEY-7/PB-PUSH-9: the revoke purge left the push token at rest (%q). It is a "+
			"provider-visible identifier for a device its owner disowned", st.PushToken)
	}
	if st.Keys.WakeKey != (crypto.WakeKey{}) {
		t.Errorf("PB-KEY-7: the revoke purge left the wake key at rest: %x", st.Keys.WakeKey)
	}
}

// ---------------------------------------------------------------------------
// The trap named in the brief: send-seq is content tier, and sends reserve seqs.
// ---------------------------------------------------------------------------

// TestS15_NoSendSeqIsIssuedFromBelowACeilingTheProcessCannotRead is the send-seq trap, stated
// as the property rather than as a mechanism.
//
// PB-STATE-9 puts the send-seq ceiling in the CONTENT tier. PB-STATE-3 makes every issued seq
// come from a DURABLE reservation, and reserveSendSeq's own comment records why: "handing one
// out that was never durably reserved is precisely how a seq gets reused across a restart". A
// process that cannot READ the ceiling cannot raise it, so with the content tier locked there
// are exactly two honest outcomes -- refuse to issue, or issue from above the ceiling it
// cannot see, which is impossible -- and one dishonest one: renumber from 1 and have the
// gateway stale-drop everything the phone sends for the life of the epoch.
//
// The assertion is therefore the property and not the choice: whatever the Sequencer does with
// the content tier locked, it must not hand out a seq at or below the ceiling already durably
// reserved.
//
// LEGITIMATE PASSER TODAY, and recorded as one. It passes on today's code for the wrong
// reason -- the ceiling is in the clear, so a locked process reads it and numbers from above
// it. It is here as the fence on the GREEN, where sealing send-seq under the content tier is
// what makes the failure reachable.
//
// WHETHER THE TRAP IS REAL is a separate question and the answer is in mobile/commands.go, not
// here: resolveSend refuses with errNoContentKey before either NextCommand or NextInput is
// reached, because a frame cannot be sealed without the epoch content key -- which is itself
// content tier. So no production send path reserves a seq while the content tier is locked,
// and content-sealing the ceiling does not brick the wake path. phonecore.Sequencer has no
// such guard of its own, which is why this fence is on the Sequencer rather than on the facade.
func TestS15_NoSendSeqIsIssuedFromBelowACeilingTheProcessCannotRead(t *testing.T) {
	dir, wake, content := s15Provision(t)

	content.openErr = crypto.ErrKeyAuthRequired
	locked, err := Resume(Config{Dir: dir, Machine: s15Machine, WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume with the content tier locked: %v", err)
	}

	seq, err := locked.Seq().NextCommand()
	if err != nil {
		// Refusing is the honest answer: no seq was issued, so none was reused.
		return
	}
	if seq <= s15SendCeiling {
		t.Errorf("PB-STATE-3: with the content tier locked the Sequencer issued seq %d, which is at or "+
			"below the durably reserved ceiling %d. The gateway holds a per-(sender,epoch) high-water "+
			"at that ceiling and stale-drops everything numbered under it -- every keystroke, "+
			"take_control, launch and kill, for the life of the epoch, with MailboxAppend returning nil "+
			"each time", seq, s15SendCeiling)
	}
}

// TestS15_TheDurableCeilingSurvivesASealedRoundTrip is the positive control the test above
// needs. Without it, "never issues from below the ceiling" is satisfiable by a Sequencer that
// refuses forever, and "the content tier is unreadable" is satisfiable by a store that writes
// nothing at all.
//
// LEGITIMATE PASSER TODAY: it asserts the behaviour that must SURVIVE the seal, and nothing is
// sealed yet. It fails the moment a GREEN implements the content tier by dropping it.
func TestS15_TheDurableCeilingSurvivesASealedRoundTrip(t *testing.T) {
	dir, wake, content := s15Provision(t)

	unlocked, err := Resume(Config{Dir: dir, Machine: s15Machine, WakeSealer: wake, ContentSealer: content})
	if err != nil {
		t.Fatalf("Resume with both tier KEKs: %v", err)
	}
	if got := unlocked.State().SendSeq[s15EpochID]; got != s15SendCeiling {
		t.Fatalf("PB-STATE-3: the durable send-seq ceiling is %d after a restart, want %d", got, s15SendCeiling)
	}
	seq, err := unlocked.Seq().NextCommand()
	if err != nil {
		t.Fatalf("PB-STATE-3: an UNLOCKED process could not issue a send-seq: %v", err)
	}
	if seq <= s15SendCeiling {
		t.Errorf("PB-STATE-3: an unlocked process issued seq %d at or below the durable ceiling %d",
			seq, s15SendCeiling)
	}
	if got := unlocked.State().Receive[s15Bucket]; got != s15ReceiveSeq {
		t.Errorf("PB-STATE-1: the receive high-water is %d after a sealed round trip, want %d",
			got, s15ReceiveSeq)
	}
	if len(unlocked.State().Sessions) == 0 || len(unlocked.State().Snapshots) == 0 ||
		len(unlocked.State().OpOutcomes) == 0 {
		t.Error("PB-STATE-9: the decrypted caches did not survive a round trip through the content " +
			"tier. Sealing them by dropping them satisfies every absence assertion in this file and " +
			"leaves the phone re-fetching its whole session model on each start")
	}
}

// s15Candidates returns every byte string in the state file that could be a sealed blob: the
// file itself, and every base64 value anywhere in its JSON tree. It is how the two tests below
// attack the file WITHOUT knowing its layout -- the point of PB-STATE-9 is which KEK opens
// what, not where the implementer puts the field.
func s15Candidates(body []byte) [][]byte {
	out := [][]byte{body}
	var tree any
	if json.Unmarshal(body, &tree) != nil {
		return out
	}
	var walk func(any)
	walk = func(n any) {
		switch t := n.(type) {
		case map[string]any:
			for _, e := range t {
				walk(e)
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		case string:
			if raw, err := base64.StdEncoding.DecodeString(t); err == nil && len(raw) > 0 {
				out = append(out, raw)
			}
		}
	}
	walk(tree)
	return out
}

// s15OpenAll returns every candidate sl could actually open. A KEK that opens nothing yields
// nothing, which is the whole question.
func s15OpenAll(sl *s14aSealer, candidates [][]byte) [][]byte {
	var out [][]byte
	for _, c := range candidates {
		if plain, err := sl.Open(c); err == nil && len(plain) > 0 {
			out = append(out, plain)
		}
	}
	return out
}

// s15Found reports whether any needle form appears in any of the plaintexts.
func s15Found(plaintexts [][]byte, needles [][]byte) bool {
	for _, plain := range plaintexts {
		for _, needle := range needles {
			if bytes.Contains(plain, needle) {
				return true
			}
		}
	}
	return false
}

// TestS15_TheWakeKEKAloneDoesNotYieldTheContentTier is "a locked-device process can read only
// the wake-tier state" attacked from the READING side, with no Go accessor in the path at all.
//
// A locked device holds the wake KEK -- it opens with no user present, that is its purpose --
// and nothing else. So this takes the bytes the core wrote, hands the WAKE sealer every blob
// they could contain, opens everything it can, and requires none of it to be content-tier
// state. It is the assertion an "at-rest inventory" cannot fake and a Go accessor returning
// empty cannot substitute for: a load path that drops a field it nonetheless sealed under the
// wake KEK passes the accessor test and fails this one.
//
// Its POSITIVE CONTROL is the same walk with the CONTENT sealer, which must recover the
// content-tier sentinels. Without it, every assertion here is satisfiable by a state file that
// holds no content at all -- and that is precisely how "sealed" gets implemented as "dropped".
// The control is also what makes this test RED today: today the content KEK covers the epoch
// content key and nothing else, so it recovers no send-seq, no high-water and no cache.
func TestS15_TheWakeKEKAloneDoesNotYieldTheContentTier(t *testing.T) {
	dir, wake, content := s15Provision(t)
	files := s14aStateDirBytes(t, dir)
	body, ok := files[StateFileName]
	if !ok {
		t.Fatalf("PB-STATE-9: %s is not in the state directory; this test would measure nothing",
			StateFileName)
	}
	candidates := s15Candidates(body)

	byWake := s15OpenAll(wake, candidates)
	byContent := s15OpenAll(content, candidates)

	for _, row := range s15Inventory() {
		if row.tier != "content" || row.needles == nil {
			continue
		}
		if s15Found(byWake, row.needles) {
			t.Errorf("PB-STATE-9: State.%s came back from %s under the WAKE KEK alone. The wake KEK "+
				"opens with no user present, so anything it reaches is reachable by a locked device -- "+
				"which is the collapse the two-tier split exists to prevent", row.field, StateFileName)
		}
		if !s15Found(byContent, row.needles) {
			t.Errorf("PB-STATE-9: State.%s cannot be recovered from %s under the CONTENT KEK either. "+
				"Either it is not sealed under that tier, or it is not on disk at all -- and 'sealed' "+
				"implemented as 'dropped' satisfies every absence assertion in this file while the phone "+
				"loses the coordinate", row.field, StateFileName)
		}
	}

	// The mirror, without which sealing the whole file under the content KEK passes everything
	// above and leaves the phone unable to come up on a push.
	for _, row := range s15Inventory() {
		if row.tier != "wake" || row.needles == nil {
			continue
		}
		if !s15Found(byWake, row.needles) {
			t.Errorf("PB-STATE-9: State.%s cannot be recovered from %s under the WAKE KEK. The wake "+
				"tier is defined as what the wake path must read WHILE LOCKED; a wake field the wake "+
				"KEK cannot open is not wake tier", row.field, StateFileName)
		}
	}
}

// TestS15_TheStateSealUsesTheSameSeamAndAddsNoCrossing. ADR-007 B8 pins the JNI key crossing
// to ONE inbound artifact, and B17 declined to widen it; S14a delivered exactly that seam and
// TestS14A_TheSealerSeamIsInboundOnly holds its shape. PB-STATE-9 is a consumer of that seam,
// so the failure mode it introduces is a SECOND way in -- a per-tier state sealer beside the
// per-tier key sealer, or a Config field that takes a KEK rather than a Sealer.
//
// It is asserted on Config because that is the only place a new crossing can enter phonecore.
//
// LEGITIMATE PASSER TODAY: Config already carries exactly the two sealers S14 delivered. It is
// green on the RED run because the crossing it forbids has not been added yet, which is the
// only state in which a fence like this is ever green.
func TestS15_TheStateSealUsesTheSameSeamAndAddsNoCrossing(t *testing.T) {
	sealer := reflect.TypeOf((*Sealer)(nil)).Elem()
	typ := reflect.TypeOf(Config{})

	var crossings []string
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Type == sealer {
			crossings = append(crossings, f.Name)
		}
	}
	sort.Strings(crossings)
	want := []string{"ContentSealer", "WakeSealer"}
	if !reflect.DeepEqual(crossings, want) {
		t.Errorf("ADR-007 B8: phonecore.Config carries Sealer fields %v, want exactly %v. PB-STATE-9 is "+
			"a CONSUMER of the S14 custody seam -- one sealer per PB-KEY-2 tier -- and a third is a "+
			"second key crossing, which B8 permits only to narrow", crossings, want)
	}
	// And no field may hand raw key material in instead of a sealer. []byte is what a KEK
	// looks like when someone gives up on the interface.
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		name := strings.ToLower(f.Name)
		if f.Type.Kind() == reflect.Slice && f.Type.Elem().Kind() == reflect.Uint8 &&
			(strings.Contains(name, "key") || strings.Contains(name, "kek")) {
			t.Errorf("ADR-007 B8: phonecore.Config.%s takes raw bytes that look like key material. The "+
				"KEK comes in behind a Sealer and never as a value the Go core holds", f.Name)
		}
	}
}
