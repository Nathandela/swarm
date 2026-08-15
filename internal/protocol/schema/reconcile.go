package schema

// ReconcileRecord is the machine->phone RECONCILE RECORD (PB-SYNC-7): the wire carrier
// of the three per-coordinate rollback authorities PB-STATE-4 names. The phone's inbound
// plaintext set is otherwise journal record / terminal snapshot / command reply / epoch
// grant, none of which carries an authority -- so without this record PB-STATE-4's "fail
// closed until reconciled" is PERMANENT.
//
// It rides the EXISTING machine->phone sealed mailbox stream as an ordinary plaintext
// with a kind tag (internal/remotegw seals it, internal/phonecore demuxes it), so
// internal/remote/crypto stays frozen and no new signed device action is introduced --
// a phone-INITIATED reconcile would need one, and PB-SYNC-5's actionClass switch is
// closed and fail-closed.
//
// The three authorities, one field group each:
//
//	(a) phone send-seq            -> InboundHighWater
//	(b) phone receive high-waters -> JournalCeiling (the shared journal/terminal bucket)
//	                                and ReplyCeiling (the separate sender-zero reply bucket)
//	(c) grant watermark           -> GrantEpoch + GrantSeq
//
// NO FIELD MAY CARRY omitempty. A legitimately-zero authority (a gateway that has accepted
// nothing inbound yet, a machine that has issued no grant beyond the first) must stay
// distinguishable on the wire from a producer that never published the field at all: with
// omitempty they are the SAME BYTES, and a phone that reads "absent" as "zero" raises no
// high-water and silently leaves every retained pre-rollback frame acceptable.
type ReconcileRecord struct {
	Machine          string `json:"machine"`            // endpoint id the authorities belong to
	EpochID          uint32 `json:"epoch_id"`           // epoch the content key (and both seq buckets) belong to
	InboundHighWater uint64 `json:"inbound_high_water"` // (a) the gateway's durable inbound accepted high-water (PB-GW-1)
	JournalCeiling   uint64 `json:"journal_ceiling"`    // (b) highest seq ISSUED on the shared journal/terminal bucket
	ReplyCeiling     uint64 `json:"reply_ceiling"`      // (b) highest seq ISSUED on the command-reply bucket
	GrantEpoch       uint32 `json:"grant_epoch"`        // (c) the daemon's grant-issuance epoch
	GrantSeq         uint64 `json:"grant_seq"`          // (c) the daemon's grant-issuance seq
	IssuedAt         int64  `json:"issued_at"`          // unix millis, the SAME value the envelope header carries

	// Profile is the machine's sealed RemoteProfileV1 (ADR-017 T5), riding this EXISTING
	// record rather than a new mailbox frame kind. It carries no omitempty for the same
	// reason as every authority above: a struct-typed field is never "empty" to
	// encoding/json anyway (omitempty would be a silent no-op here), but the tag is kept
	// off deliberately so a reader never mistakes it for a field that may legitimately be
	// absent -- enforced as a source-level convention by
	// TestReconcileRecord_ProfileFieldTag_NoOmitempty.
	Profile RemoteProfileV1 `json:"profile"`
}
