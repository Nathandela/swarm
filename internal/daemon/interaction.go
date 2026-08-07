package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Nathandela/swarm/internal/journal"
)

// InteractionSchemaVersion is the item schema version carried as the item's own `v`
// (interaction-schema.md §2). It is DISTINCT from journal.SchemaVersion and from
// protocol.Version, and IS-COMPAT-3 forbids bumping either of those to add a kind or an
// optional field: `v` is the only schema version a transcript consumer needs.
const InteractionSchemaVersion = 1

// MaxItemBytes caps one item's serialized JSON payload (interaction-schema.md §5).
//
// The number is PROPOSED AND UNRATIFIED -- §5 says so in its own preamble: ADR-009 carries
// none of these numbers and hands the question back to the schema, so nothing has ratified
// them. It is a floor chosen well under the relay's per-envelope admission cap (~768 KiB of
// plaintext once base64 expansion is accounted for), not a measured optimum. Ratifying it
// is a measured slice or an owner ruling written into ADR-009.
//
// ponytail: this is the ONLY §5 cap enforced here, because it is the only one this seam can
// see. The others (MaxTextBytes, MaxSummaryBytes, MaxPromptLines, MaxSteps, MaxDecisions)
// all apply to per-kind fields, which reach RecordInteraction as an opaque Fields blob;
// they belong to the kind-shaping producer upstream, with IS-CAP-1's rune-boundary
// truncation and the `truncated`/`full_bytes` pair it sets. An item that arrives over this
// cap therefore had that truncation skipped, which is a producer bug -- see the refusal
// note on RecordInteraction.
const MaxItemBytes = 8 << 10

// InteractionItem is the item envelope of interaction-schema.md §2, daemon-side: the shape
// the daemon marshals into a journal record's payload. ADR-010 §3 makes the daemon the sole
// producer of what goes on the wire -- ids, ordering, caps, hashes and expiry are all
// decided here, never by an adapter.
//
// session_id and cursor are deliberately absent: the enclosing wire record already carries
// both, and §2 forbids duplicating them. `ts` is present for the opposite reason -- the
// wire record carries none, so without it a consumer would have to substitute arrival time,
// which is exactly the PB-APP-11 clock mistake.
type InteractionItem struct {
	V         int       `json:"v"`
	ItemID    string    `json:"item_id"`
	TS        time.Time `json:"ts"`
	TurnID    string    `json:"turn_id,omitempty"`
	Kind      string    `json:"kind"`
	Status    string    `json:"status,omitempty"`
	Truncated bool      `json:"truncated,omitempty"`
	FullBytes int       `json:"full_bytes,omitempty"`
	Detail    bool      `json:"detail,omitempty"`

	// Fields carries the per-kind fields of §3, which are "additional to the envelope":
	// ONE flat JSON object, not a nested body, so MarshalJSON merges them in beside the
	// envelope keys. It is a raw message rather than the eight typed kinds because this is
	// the carriage seam and it has no reason to know them -- the kinds marshal INTO it, and
	// an unknown kind or an unknown field costs this seam nothing (IS-COMPAT-1/-2).
	Fields json.RawMessage `json:"-"`
}

// itemEnvelope keeps MarshalJSON from recursing into itself.
type itemEnvelope InteractionItem

// MarshalJSON emits the envelope with the §3 kind fields flat beside it. A kind field whose
// name collides with an envelope field is an error, not an overwrite: silently re-labelling
// an item's `kind` or `item_id` would break the fold-by-item_id rule (IS-ENV-2) in a way no
// consumer could detect.
func (it InteractionItem) MarshalJSON() ([]byte, error) {
	env, err := json.Marshal(itemEnvelope(it))
	if err != nil {
		return nil, err
	}
	if len(it.Fields) == 0 {
		return env, nil
	}
	var merged, extra map[string]json.RawMessage
	if err := json.Unmarshal(env, &merged); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(it.Fields, &extra); err != nil {
		return nil, fmt.Errorf("interaction: kind fields are not a JSON object: %w", err)
	}
	for k, v := range extra {
		if _, dup := merged[k]; dup {
			return nil, fmt.Errorf("interaction: kind field %q collides with an envelope field", k)
		}
		merged[k] = v
	}
	return json.Marshal(merged)
}

// validate enforces the envelope rules of §2 that a producer can get wrong. IS-ENV-3 is
// all-or-nothing: an item lacking v, item_id or kind is not emitted at all, because a
// consumer's only recourse is to skip it, and a skipped record still burned a cursor.
func (it InteractionItem) validate() error {
	switch {
	case it.V == 0:
		return errors.New(`interaction: item has no "v" (IS-ENV-3: emit nothing rather than a partial item)`)
	case it.ItemID == "":
		return errors.New(`interaction: item has no "item_id" (IS-ENV-3)`)
	case it.Kind == "":
		return errors.New(`interaction: item has no "kind" (IS-ENV-3)`)
	case it.TS.IsZero():
		return errors.New(`interaction: item has no "ts" (§2: required, and the wire record carries none to substitute)`)
	case it.FullBytes != 0 && !it.Truncated:
		return errors.New(`interaction: "full_bytes" is carried only with "truncated" (§2)`)
	}
	// ponytail: the kind is checked for presence, not against the eight-value vocabulary of
	// §3. The vocabulary belongs with the kind types, and IS-COMPAT-1 already makes an
	// unknown kind a consumer-side skip rather than a producer-side error.
	return nil
}

// RecordInteraction appends one interaction item to the daemon-wide journal as a BARE
// record: type `interaction`, the item object in the opaque payload, no mailbox kind and no
// new demux branch (IS-LAYER-1). Ordering is the cursor Append assigns (IS-LAYER-3), and a
// gap repairs through the journal's existing roster+events reseed (IS-LAYER-4).
//
// WRITE PATH -- the choice interaction-schema.md leaves open, recorded here. This appends
// DIRECTLY, as a sibling of RecordGatewayPresence, and NOT through the saveMetaLocked choke
// point, and NOT under writeMu.
//
//   - Not through saveMetaLocked/journalRecordFor, because an item is not a meta
//     transition. It is captured off an adapter event and correlates with no persist.Meta
//     write at all, so journalRecordFor -- which switches on a status transition -- has
//     nothing to derive it from, and routing it there would mean inventing a meta write to
//     hang it on.
//   - Not under writeMu, because writeMu exists to keep JournalReadFrom's roster snapshot
//     consistent with the cursor it is taken at (R-JRN.4; see the comment on
//     JournalReadFrom). An interaction record carries a SessionID but is ROSTER-NEUTRAL: it
//     never writes persist.Meta, and rosterSnapshotLocked reads only persist.Meta and
//     d.sessions, so no interleaving of this append with a roster read can make the roster
//     disagree with the cursor it is stamped at. That is the RecordGatewayPresence argument
//     with a session id attached; j.mu still serializes the append itself. Taking writeMu
//     would instead put a per-tool-call append on the daemon's write-critical path.
//
// ponytail: rate admission is NOT here. ADR-010 §7's one-append-per-window floor and
// IS-DELTA-2a's per-target ceiling are the producer's queue, upstream of this call; this
// function is the entry that queue releases into, and it neither merges nor delays.
func (d *Daemon) RecordInteraction(sessionID string, it InteractionItem) error {
	if it.TS.IsZero() {
		// The machine instant for THIS record. A producer that captured the event earlier
		// owns the instant and passes it in; stamping only when unset keeps this from
		// substituting the append time for a known capture time (PB-APP-11).
		it.TS = time.Now().UTC()
	}
	if err := it.validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(it)
	if err != nil {
		return err
	}
	return d.RecordInteractionRaw(sessionID, payload)
}

// RecordInteractionRaw is RecordInteraction's already-serialized sibling: the entry ADR-010
// §7's append floor releases into.
//
// IT EXISTS BECAUSE THE FLOOR MERGES BYTES, NOT STRUCTS. remotegw.ItemAdmission collapses a
// tool_run's open and close into ONE record by a field-wise union of the two JSON objects
// (IS-DELTA-3), and it forwards an UNMERGED item byte-exact -- which is what keeps an
// approval_request's bytes the bytes the daemon hashed (IS-APR-2). Re-parsing that result into
// an InteractionItem only to marshal it again would re-order keys and re-encode values, and
// would break the byte-exactness the merge deliberately preserves.
//
// It applies the same two refusals as the typed form: IS-ENV-3's required envelope fields, and
// §5's MaxItemBytes. Both are cheap and neither can be assumed of bytes that have been merged.
func (d *Daemon) RecordInteractionRaw(sessionID string, item json.RawMessage) error {
	var env struct {
		V      int    `json:"v"`
		ItemID string `json:"item_id"`
		Kind   string `json:"kind"`
	}
	if err := json.Unmarshal(item, &env); err != nil {
		return fmt.Errorf("interaction: item is not a JSON object: %w", err)
	}
	if env.V == 0 || env.ItemID == "" || env.Kind == "" {
		return errors.New(`interaction: item is missing "v", "item_id" or "kind" (IS-ENV-3: emit nothing rather than a partial item)`)
	}
	if len(item) > MaxItemBytes {
		// Refuse rather than clip: the per-field truncation that keeps an item under this
		// cap is upstream (see MaxItemBytes), and cutting a serialized JSON object here
		// would produce the partial item IS-ENV-3 forbids.
		return fmt.Errorf("interaction: item is %d bytes, over the %d-byte cap (interaction-schema.md §5)", len(item), MaxItemBytes)
	}
	_, err := d.journal.Append(journal.Record{
		SessionID: sessionID,
		Type:      journal.TypeInteraction,
		Payload:   item,
	})
	return err
}
