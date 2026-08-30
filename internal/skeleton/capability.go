package skeleton

// THE CAPABILITY PRODUCER: derives a session's daemon-authored per-session capability
// record (ADR-017 T2, playbook §6.2) at launch, from the adapter seam.
//
// WHY IT LIVES HERE: internal/skeleton is the only package that imports both the adapter
// contract/registry and the wire schema (protocol) -- exactly why interaction.go's
// producer lives here too. It mirrors api.go's "adapters into launch" seam.
//
// structured_chat is derived from adapter.AsInteractionSource, discovered exactly as
// ADR-010 §5 already discovers it for interaction capture: "ABSENCE IS A SIGNAL", never a
// hardcoded per-provider list. Per ADR-017 T2 rule 4 ("no route to the fallback from a
// healthy structured session"), a structured session gets terminal_fallback=false; every
// other adapter gets the generic fallback pair.
//
// ADR-017 T3 IS NOT FULLY ENFORCED YET. AsInteractionSource is NECESSARY for
// structured_chat=true but not, by itself, SUFFICIENT: T3 requires every mandatory row to
// pass against the recorded provider_version, including the Version-skew row ("unknown
// provider versions fail to the terminal fallback ... not optimistic structured mode"),
// and playbook §6.2 names four seams beyond InteractionSource -- MessageSink, ApprovalSink,
// LifecycleSink, TerminalFallback -- none of which have an adapter interface yet. This
// derivation checks only the one seam that exists today; the version-skew gate and the
// remaining seam checks are deferred, tracked in bd agents-tracker-hggx.2.1, and land as
// their own RED+GREEN slice since they change what the existing capability-record tests
// pin.
//
// interrupt is derived from adapter.AsTurnInterrupter (Wave R6, Mirror M2.4). The note
// that stood here -- "interrupt is left at its zero value (false) for every adapter:
// nothing in internal/adapter exposes a LifecycleSink-style interrupt seam to consult, so
// there is nothing to derive it from" -- was the honest state of a deferral this wave
// discharged: the seam now exists (adapter.TurnInterrupter, proven by claude, absent on
// every other adapter), the daemon's InterruptTurn executes exactly its declared data,
// and the capability record derives Interrupt from the SAME seam -- true where a seam is
// proven, false where none is, never guessed from structured_chat (T2 rule 3's "the phone
// renders from the record and infers nothing" still governs; the record just finally has
// a fact to carry). r6_interruptapply_test.go is the pin.

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/protocol"
)

// THE CAPABILITY STORE: the daemon-authored per-session capability record's home
// (registerSessionCapabilities/SessionCapabilities), sibling of the pure derivation
// above. It is what HookDrainer degrades on a proven structured_gap (hookdrain.go) and
// what a future capability-serving surface would read.
//
// registerSessionCapabilities MERGES rather than overwrites: SetStructuredChat's
// degrade-only rule (protocol/schema/capability.go) is applied to any EXISTING record
// under sessionID before the incoming one is stored, so ordinary re-registration cannot
// resurrect chat. The sole recovery path is commitStructuredSinkProof, which binds an
// exact current sink to the latest durable gap and publishes the ordered transition.
//
// THE RECORD IS DURABLE, NOT MERELY IN MEMORY (R6 review fix-pack round 1, BLOCKER 1).
// The store WAS a plain map, so every degrade died with the process that authored it: a
// probe watched structured_chat come back TRUE on the next daemon incarnation of a
// session whose spool gap was still proven and still present -- T2 rule 2 ("it cannot
// upgrade a fallback session in place") inverted. A capability record is daemon-AUTHORED
// state (playbook §6.2, "session_capabilities(machine, session_instance)"), so it is
// persisted where the rest of a session's daemon-authored state lives: one 0600 JSON
// side-file in the session's own dir, retired with the session dir itself.
//
// WHY A SIDE-FILE AND NOT A REPLAY OF THE JOURNAL'S OWN structured_gap RECORDS. The
// journal is the natural candidate -- the degrade's proof is already there -- but it
// ROTATES under a retention bound (internal/journal enforceRetentionLocked). A session
// long-lived enough to outlive its own structured_gap record would silently re-acquire
// structured_chat, which is the very bug this fixes, merely deferred. The side-file has
// the session's own lifetime by construction.
type sessionCapabilityStore struct {
	mu sync.Mutex
	// transitionMu serializes state commit plus its ordered journal publication.
	// Without it, a proof could commit, a newer gap could publish false, and the
	// older proof could then append true after it while lookup correctly read false.
	transitionMu sync.Mutex
	dir          string // durable home (the daemon's state dir); "" = memory only
	byID         map[string]protocol.SessionCapabilities
	// instances caches each session's per-incarnation identifier (instance.go). It is
	// backed by the same 0700 session dir as the record, so a daemon restart ADOPTS an
	// instance rather than minting one (ADR-017 T8-a).
	instances map[string]string
	// incarnations caches the shim pid each cached instance was minted for. It is what
	// makes a daemon restart (same shim, same pid) an ADOPTION and a session replacement
	// (new shim, new pid) a re-mint -- the distinction ADR-017 T8-a's whole binding turns
	// on, and one the session id cannot make (round-3 blocker 2b). Zero is UNKNOWN, never
	// "different": a side-file written before this format carries no pid, and reading that
	// as a replacement would reset every session's view once on the upgrade.
	incarnations map[string]int
	// versions caches the detected CLI version per agent type for this daemon's life.
	versions map[string]string
	// liveProof is deliberately process-local. A durable proof records that a history
	// tear was legitimately migrated, but it cannot prove that this daemon
	// has reconnected the sink after a restart. Sessions carrying a history marker are
	// therefore chat-capable only while this map also names the current instance and
	// marker generation.
	liveProof map[string]structuredSinkProof
	// publish overrides the final journal append in deterministic tests. Production
	// leaves it nil and publishes through daemon.EmitCapabilityTransition.
	publish func(sessionID string, payload []byte) error
	// retrying is the desired unpublished transition per session. retryMu makes its
	// worker single-flight: repeated observations of one gap update one slot rather
	// than accumulating identical append goroutines.
	retryMu  sync.Mutex
	retrying map[string]protocol.SessionCapabilities
}

// sessionCapabilityFile is the per-session capability record's on-disk name, sibling of
// meta.json and shim-launch.json inside a session's own 0700 dir.
const sessionCapabilityFile = "capabilities.json"

// sessionDegradedFile is the durable "this session instance proved a structured gap"
// marker, sibling of the record above.
//
// WHY IT EXISTS SEPARATELY (R6 review fix-pack round 1, BLOCKER 1). A degrade can be
// proven BEFORE any capability record has been authored. When this was written that was
// the normal case; since Wave R8 wired the authoring path (authorSessionCapabilities is
// reached from five production seams -- see sessionDegraded's routing note below) a live
// session normally HAS a record, but the authoring path stays deliberately silent while
// a side-process backend is still dialling, so a gap proven in that window still finds
// no record. Degrading "the record, if one exists" would therefore no-op in exactly that
// window, and the first record authored afterwards would claim structured_chat=true over
// a session with a proven, unrecoverable hole in its event stream. The marker records the FACT of the gap without inventing a record to hold it:
// ADR-017 T2 says capability records are authored at launch and this seam may only
// degrade them, never fabricate one, so the degrade is stored as what it is.
const sessionDegradedFile = "structured-degraded"

// sessionSinkProofFile is the durable commit record for a proof-authorized recovery.
// It is written only after capabilities.json contains the recovered record. Reads also
// require a matching process-local proof, so this file is necessary but never sufficient.
const sessionSinkProofFile = "structured-sink-proof.json"

type historyTornMarker struct {
	Version         int    `json:"version"`
	SessionInstance string `json:"session_instance"`
	Generation      string `json:"generation"`
}

type structuredSinkProof struct {
	Version         int    `json:"version"`
	SessionInstance string `json:"session_instance"`
	GapGeneration   string `json:"gap_generation"`
	Kind            string `json:"kind"`
}

const (
	structuredSinkProofVersion = 1
	sinkProofBackend           = "initialized_backend"
	sinkProofShimSubmit        = "shim_submit_transaction"
)

var errStructuredSinkProof = errors.New("skeleton: structured sink proof refused")

// registerSessionCapabilities stores c for sessionID, merging against any existing
// record (in memory, else on disk) via the public degrade-only helper. It never performs
// the private exact-sink recovery.
func (d *Daemon) registerSessionCapabilities(sessionID string, c protocol.SessionCapabilities) {
	d.capStore.transitionMu.Lock()
	defer d.capStore.transitionMu.Unlock()
	d.capStore.mu.Lock()
	defer d.capStore.mu.Unlock()
	recovered := false
	if existing, ok := d.lookupCapabilitiesLocked(sessionID); ok {
		// A changed instance is a replacement, not a re-registration. Authority
		// withdrawn from the old PTY must not be merged into the new one, and a live
		// proof for the old PTY must not authorize its replacement.
		if existing.SessionInstance != "" && c.SessionInstance != "" &&
			existing.SessionInstance != c.SessionInstance {
			delete(d.capStore.liveProof, sessionID)
			_ = os.Remove(d.sessionStatePathLocked(sessionID, sessionSinkProofFile))
		} else {
			recovered = d.sessionDegradedLocked(sessionID) && existing.StructuredChat
			if recovered {
				// A generic reconcile/attach refresh is not a sink-loss event. Preserve
				// the exact proof-authorized chat routing while still accepting updated
				// descriptive metadata and independent Interrupt capability.
				c.StructuredChat = true
				c.TerminalFallback = false
				c.TerminalControl = false
			} else {
				// SetStructuredChat refuses an upgrade (false->true) and leaves existing
				// unchanged; it honors a degrade (true->false), forcing TerminalFallback true.
				// Either way, existing now holds the merged structured_chat/terminal_fallback
				// pair, which is what actually governs the session -- an already-degraded
				// session's incoming record is stored with that pair overridden back in, so
				// its OTHER fields (provider_version, adapter_revision, ...) can still refresh.
				_ = existing.SetStructuredChat(c.StructuredChat)
				c.StructuredChat = existing.StructuredChat
				c.TerminalFallback = existing.TerminalFallback
				// D-DEGRADE-ORIGIN FENCE 2 (ADR-017 T6-b). terminal_control is one of the
				// "other fields" the merge above deliberately lets refresh, and a daemon
				// restart's reconcile re-registers every reconnected session -- which is
				// precisely what that merge exists for. Left alone, an unchanged reconcile
				// silently RE-GRANTS control over a session that proved a structured gap.
				// So control is one-way in the withdrawing direction, exactly like
				// structured_chat: a re-registration may drop it and may never restore it.
				c.TerminalControl = c.TerminalControl && existing.TerminalControl
			}
		}
	}
	if d.sessionDegradedLocked(sessionID) {
		// A gap was proven for this session instance -- possibly by an incarnation
		// that is gone, possibly before any record existed to degrade. Either way the
		// answer to "may this session claim structured chat" is already no, and this
		// incoming record does not get to reopen it (ADR-017 T2 rule 2).
		c.StructuredChat = recovered
		c.TerminalFallback = false
		// And a degrade grants a SCREEN, never a keyboard (T6-b): the fallback this
		// session just acquired was DERIVED, not authored at launch, and its live TUI
		// has an uncharacterized input region.
		c.TerminalControl = false
	}
	if d.capStore.byID == nil {
		d.capStore.byID = map[string]protocol.SessionCapabilities{}
	}
	d.capStore.byID[sessionID] = c
	if err := d.persistSessionCapabilitiesLocked(sessionID, c); err != nil {
		// Logged, never fatal: an unwritable record leaves the session governed by the
		// in-memory copy for this incarnation, which is strictly what the old behavior
		// was. Silence would make a degrade that will NOT survive a restart look
		// identical to one that will.
		log.Printf("skeleton: persist capability record for session %s: %v", sessionID, err)
	}
}

// sessionCapabilities returns sessionID's stored capability record, if any -- from
// memory, or from the durable side-file a PRIOR daemon incarnation wrote.
func (d *Daemon) sessionCapabilities(sessionID string) (protocol.SessionCapabilities, bool) {
	d.capStore.mu.Lock()
	defer d.capStore.mu.Unlock()
	return d.lookupCapabilitiesLocked(sessionID)
}

func (d *Daemon) hasRawSessionCapabilities(sessionID string) bool {
	d.capStore.mu.Lock()
	defer d.capStore.mu.Unlock()
	_, ok := d.rawCapabilitiesLocked(sessionID)
	return ok
}

// lookupCapabilitiesLocked resolves sessionID's record, then applies the durable
// structured-degraded marker to whatever it found. Caller holds capStore.mu.
//
// NOTHING READS PAST THE MARKER (R6 review fix-pack round 2, MEDIUM 3). Round 1 bound
// the marker to the WRITE path only (registerSessionCapabilities consulted it;
// SessionCapabilities did not), which left one partial-failure state able to resurrect
// structured chat across a restart: markSessionDegraded writes the marker FIRST and the
// degraded record SECOND, logging rather than failing if the second write fails, so an
// ENOSPC between the two -- precisely the disk-full case playbook 6.1 names -- leaves a
// marker beside a capabilities.json still claiming structured_chat=true. A probe read
// that state back as StructuredChat=true on a fresh Daemon: ADR-017 T2 rule 2 inverted
// on a narrower path than round 1's, but the same rule. The marker is the PROOF of the
// gap; the record is only a cache of what that proof implies, so the proof wins on every
// read, not merely on the reads that happen to be writes.
func (d *Daemon) lookupCapabilitiesLocked(sessionID string) (protocol.SessionCapabilities, bool) {
	c, ok := d.rawCapabilitiesLocked(sessionID)
	if !ok {
		return protocol.SessionCapabilities{}, false
	}
	if d.sessionDegradedLocked(sessionID) {
		// THE DEGRADE IS NOT APPLIED TO AN ALREADY-INCONSISTENT RECORD (ADR-017 T2-b, closing
		// review finding 9).
		//
		// THE LAUNDERING THIS CLOSES. `SetStructuredChat(false)` FORCES TerminalFallback true,
		// and it was applied to whatever the disk held with no validity check at all. So the
		// INVALID {structured_chat:true, terminal_fallback:false, terminal_control:true} --
		// refused by every other T2-b seam -- came back as the VALID {false, true, true}, which
		// grants AllowsTerminalControl(). The mutual-exclusion shapes launder the same way:
		// {structured_chat:true, terminal_fallback:true, ...} is invalid as authored, and the
		// transform erases exactly the boolean that made it invalid, reading it back as a
		// valid grant (closing round 2 finding -- the guard originally covered only the
		// control-without-fallback clause). The transform ran from LESS VALID to MORE
		// AUTHORITY, which is the one direction a read may never take: T2 rule 2 lets a
		// degrade REMOVE structured chat, and nothing in it lets a read manufacture a grant
		// the authoring daemon never made. T2-b's own words are that an inconsistent record
		// is REJECTED rather than resolved, because resolving it means the READER choosing
		// which boolean to believe.
		//
		// IT CHECKS Validate's TWO BOOLEAN CLAUSES AND DELIBERATELY NOT ITS SESSION-INSTANCE
		// CLAUSE, and the distinction is the whole reason this is not a bare `c.Validate()`.
		// The boolean clauses are what the transform can ERASE -- it writes exactly those
		// fields -- so an inconsistency there is one this branch would hide. A missing
		// session_instance is a T8-a fact the transform never touches and cannot launder, and
		// it is already enforced fail-closed on every read by `AllowsTerminalWatch`, which runs
		// the full Validate. Refusing the record outright for it here would also make every
		// record written before instances existed unreadable, which is a behaviour change this
		// finding does not ask for and a standing test pins against.
		if (c.StructuredChat && c.TerminalFallback) || (c.TerminalControl && !c.TerminalFallback) {
			log.Printf("skeleton: capability record for session %s fails a routing-boolean clause "+
				"of Validate; the structured degrade is not applied to it", sessionID)
			return protocol.SessionCapabilities{}, false
		}
		marker, _ := d.historyTornMarkerLocked(sessionID)
		durable, durableOK := d.durableSinkProofLocked(sessionID)
		live, liveOK := d.capStore.liveProof[sessionID]
		current, _, currentOK := d.sessionInstanceLocked(sessionID)
		proofOK := c.StructuredChat && currentOK && current == c.SessionInstance &&
			durableOK && liveOK && sinkProofMatches(durable, marker, current) &&
			sinkProofMatches(live, marker, current) && durable.Kind == live.Kind
		if proofOK {
			// Recovered chat is the only authority this proof grants. In particular,
			// a legacy fallback bit is never carried through the migration.
			c.TerminalFallback = false
			c.TerminalControl = false
			return c, true
		}
		// Marker + no complete current proof is a status/chat-shell state, never an
		// implicit terminal route. This also covers every crash prefix: cap=true
		// persisted before the proof commit, corrupt/stale sidecars, and restarts
		// before this process has freshly proved the sink.
		c.StructuredChat = false
		c.TerminalFallback = false
		c.TerminalControl = false
	}
	return c, true
}

// rawCapabilitiesLocked resolves sessionID's stored record VERBATIM -- from memory,
// falling back to the durable side-file and caching what it finds -- WITHOUT applying the
// degraded marker. Only markSessionDegraded uses it, precisely so it can still see a
// stale structured_chat=true record and rewrite it; every other reader goes through
// lookupCapabilitiesLocked. Caller holds capStore.mu.
func (d *Daemon) rawCapabilitiesLocked(sessionID string) (protocol.SessionCapabilities, bool) {
	if c, ok := d.capStore.byID[sessionID]; ok {
		return c, true
	}
	path := d.capabilityPathLocked(sessionID)
	if path == "" {
		return protocol.SessionCapabilities{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return protocol.SessionCapabilities{}, false
	}
	var c protocol.SessionCapabilities
	if json.Unmarshal(data, &c) != nil {
		return protocol.SessionCapabilities{}, false
	}
	if d.capStore.byID == nil {
		d.capStore.byID = map[string]protocol.SessionCapabilities{}
	}
	d.capStore.byID[sessionID] = c
	return c, true
}

// capabilityPathLocked is sessionID's record path, or "" when this Daemon has no
// durable home (a bare literal in a test). Caller holds capStore.mu.
func (d *Daemon) capabilityPathLocked(sessionID string) string {
	return d.sessionStatePathLocked(sessionID, sessionCapabilityFile)
}

// sessionStatePathLocked joins name under sessionID's own state dir, or "" when this
// Daemon has no durable home. Caller holds capStore.mu.
func (d *Daemon) sessionStatePathLocked(sessionID, name string) string {
	if d.capStore.dir == "" || sessionID == "" {
		return ""
	}
	return filepath.Join(d.capStore.dir, sessionID, name)
}

// sessionDegradedLocked reports whether a structured gap has been proven for
// sessionID, durably. Caller holds capStore.mu.
func (d *Daemon) sessionDegradedLocked(sessionID string) bool {
	path := d.sessionStatePathLocked(sessionID, sessionDegradedFile)
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func (d *Daemon) historyTornMarkerLocked(sessionID string) (historyTornMarker, bool) {
	path := d.sessionStatePathLocked(sessionID, sessionDegradedFile)
	if path == "" {
		return historyTornMarker{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return historyTornMarker{}, false
	}
	var marker historyTornMarker
	if json.Unmarshal(data, &marker) == nil && marker.Generation != "" {
		return marker, true
	}
	// Every pre-recovery marker was an empty/prose presence file. It is one stable
	// legacy generation and intentionally binds no instance until a fresh proof does.
	return historyTornMarker{Version: 0, Generation: "legacy"}, true
}

func (d *Daemon) durableSinkProofLocked(sessionID string) (structuredSinkProof, bool) {
	path := d.sessionStatePathLocked(sessionID, sessionSinkProofFile)
	if path == "" {
		return structuredSinkProof{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return structuredSinkProof{}, false
	}
	var proof structuredSinkProof
	if json.Unmarshal(data, &proof) != nil || proof.Version != structuredSinkProofVersion ||
		proof.SessionInstance == "" || proof.GapGeneration == "" || proof.Kind == "" {
		return structuredSinkProof{}, false
	}
	return proof, true
}

func sinkProofMatches(proof structuredSinkProof, marker historyTornMarker, current string) bool {
	return proof.Version == structuredSinkProofVersion && proof.SessionInstance == current &&
		marker.Generation != "" && proof.GapGeneration == marker.Generation
}

// sessionDegraded reports whether a structured gap has been PROVEN for sessionID, reading
// the durable marker directly rather than through the capability record.
//
// IT READS THE MARKER AND NOT THE RECORD BECAUSE THE MARKER OUTLIVES AND PRECEDES IT.
//
// THIS PARAGRAPH USED TO SAY THE OPPOSITE OF WHAT IS TRUE NOW, and the correction is on the
// record rather than quietly applied (round-3 minor 8). Until Wave R8 it said "there is no
// record in production -- registerSessionCapabilities has no production caller", and that was
// accurate when written. R8 wired the authoring path: authorSessionCapabilities is reached
// from the client-facing launch seam (api.go), the core's session-start hook and the
// reconcile re-adoption (serve.go), the session tap (sessiontap.go) and the backend join
// (backend.go), and every one of them ends in registerSessionCapabilities. A live session
// today normally HAS a record, and a reader auditing the routing story must not come away
// believing otherwise.
//
// The marker is still the right thing to read here, for two reasons that survived the
// wiring. It is written the instant a gap is PROVEN (hookdrain.go), which can be before any
// record exists -- the authoring path deliberately stays silent while a side-process backend
// is still dialling (backendPlaneDecided), so a degrade proven in that window would have no
// record to be applied to. And it is durable in the session's own dir, so it survives the
// daemon restart that would otherwise re-author a record from a live-looking backend.
func (d *Daemon) sessionDegraded(sessionID string) bool {
	d.capStore.mu.Lock()
	defer d.capStore.mu.Unlock()
	return d.sessionDegradedLocked(sessionID)
}

// markSessionDegraded durably records that sessionID proved a structured gap, then
// disables its stored chat authority if one exists. The history marker is one-way and
// permanent; current/future sending may recover only through commitStructuredSinkProof.
func (d *Daemon) markSessionDegraded(sessionID string) {
	d.markSessionDegradedFor(sessionID, "legacy")
}

// markSessionDegradedFor records one exact gap/sink-loss generation. Repeating the
// same proven boundary is idempotent; a different generation invalidates the prior
// proof before any cap-false write or observable read.
func (d *Daemon) markSessionDegradedFor(sessionID, generation string) {
	if generation == "" {
		generation = "legacy"
	}
	d.capStore.transitionMu.Lock()
	defer d.capStore.transitionMu.Unlock()
	d.capStore.mu.Lock()
	var publish *protocol.SessionCapabilities
	defer func() {
		d.capStore.mu.Unlock()
		if publish != nil {
			if err := d.publishCapabilityTransition(sessionID, *publish); err != nil {
				d.scheduleCapabilityTransitionRetry(sessionID, *publish)
			}
		}
	}()
	if path := d.sessionStatePathLocked(sessionID, sessionDegradedFile); path != "" {
		instance, _, _ := d.sessionInstanceLocked(sessionID)
		marker := historyTornMarker{Version: 1, SessionInstance: instance, Generation: generation}
		prior, priorOK := d.historyTornMarkerLocked(sessionID)
		sameBoundary := priorOK && prior.Generation == marker.Generation &&
			(prior.SessionInstance == "" || prior.SessionInstance == marker.SessionInstance)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			log.Printf("skeleton: mark session %s structurally degraded: %v", sessionID, err)
		} else {
			// Invalidate the authority commit first. If the process dies here, lookup
			// sees marker/no proof and refuses both chat and terminal. This also runs
			// when the exact old boundary is observed after a clean recovery: replaying
			// a marker is idempotent, but losing future ingestion again is not.
			delete(d.capStore.liveProof, sessionID)
			_ = os.Remove(d.sessionStatePathLocked(sessionID, sessionSinkProofFile))
			if !sameBoundary {
				if err := writeJSONStateFile(path, marker); err != nil {
					log.Printf("skeleton: mark session %s structurally degraded: %v", sessionID, err)
				}
			}
		}
	}
	// The RAW record on purpose: lookupCapabilitiesLocked now applies the marker written
	// just above, so it would report an already-degraded record and this would never
	// rewrite a stale structured_chat=true one back to disk.
	c, ok := d.rawCapabilitiesLocked(sessionID)
	if !ok {
		return // no record authored yet: the marker above is the whole degrade
	}
	if !c.StructuredChat && !c.TerminalFallback && !c.TerminalControl {
		if d.capabilityTransitionRetryPending(sessionID, c) {
			return // one exact false publication is already single-flight
		}
		publish = &c // a prior append may have failed; replay is ordered and idempotent
		return       // already fail-closed; nothing to write or republish
	}
	c.StructuredChat = false
	c.TerminalFallback = false
	c.TerminalControl = false
	d.capStore.byID[sessionID] = c
	if err := d.persistSessionCapabilitiesLocked(sessionID, c); err != nil {
		log.Printf("skeleton: persist degraded capability record for session %s: %v", sessionID, err)
		return
	}
	publish = &c
}

func writeJSONStateFile(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeSessionStateFile(path, data)
}

// persistSessionCapabilitiesLocked writes sessionID's record atomically at 0600
// (temp+fsync+rename, writeRemoteState's pattern). Caller holds capStore.mu.
func (d *Daemon) persistSessionCapabilitiesLocked(sessionID string, c protocol.SessionCapabilities) error {
	path := d.capabilityPathLocked(sessionID)
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, sessionCapabilityFile+".tmp*") // os.CreateTemp creates 0600
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	dirHandle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = dirHandle.Close() }()
	return dirHandle.Sync()
}

func (d *Daemon) publishCapabilityTransition(sessionID string, c protocol.SessionCapabilities) error {
	if c.Validate() != nil || d.core == nil {
		return errStructuredSinkProof
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if d.capStore.publish != nil {
		return d.capStore.publish(sessionID, payload)
	}
	return d.core.EmitCapabilityTransition(sessionID, payload)
}

func (d *Daemon) scheduleCapabilityTransitionRetry(sessionID string, expected protocol.SessionCapabilities) {
	d.capStore.retryMu.Lock()
	if d.capStore.retrying == nil {
		d.capStore.retrying = map[string]protocol.SessionCapabilities{}
	}
	_, running := d.capStore.retrying[sessionID]
	d.capStore.retrying[sessionID] = expected
	d.capStore.retryMu.Unlock()
	if !running {
		go d.retryCapabilityTransition(sessionID)
	}
}

func (d *Daemon) capabilityTransitionRetryPending(sessionID string, expected protocol.SessionCapabilities) bool {
	d.capStore.retryMu.Lock()
	defer d.capStore.retryMu.Unlock()
	pending, ok := d.capStore.retrying[sessionID]
	return ok && pending == expected
}

func (d *Daemon) retryCapabilityTransition(sessionID string) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	// A retry is a retry, not a second foreground append racing the caller that
	// just scheduled it. Back off before the first attempt as well as later ones.
	select {
	case <-d.closing:
		return
	case <-ticker.C:
	}
	for {
		d.capStore.retryMu.Lock()
		expected, pending := d.capStore.retrying[sessionID]
		d.capStore.retryMu.Unlock()
		if !pending {
			return
		}
		d.capStore.transitionMu.Lock()
		current, ok := d.sessionCapabilities(sessionID)
		if !ok || current != expected {
			d.capStore.transitionMu.Unlock()
			d.capStore.retryMu.Lock()
			if latest, exists := d.capStore.retrying[sessionID]; exists && latest == expected {
				delete(d.capStore.retrying, sessionID)
			}
			d.capStore.retryMu.Unlock()
			continue // a newer transition may have replaced this retry
		}
		err := d.publishCapabilityTransition(sessionID, expected)
		d.capStore.transitionMu.Unlock()
		if err == nil {
			d.capStore.retryMu.Lock()
			if latest, exists := d.capStore.retrying[sessionID]; exists && latest == expected {
				delete(d.capStore.retrying, sessionID)
			}
			more := len(d.capStore.retrying) > 0
			d.capStore.retryMu.Unlock()
			if !more {
				return
			}
			continue
		}
		select {
		case <-d.closing:
			return
		case <-ticker.C:
		}
	}
}

// commitStructuredSinkProof is the only false->true mutation. Its caller has
// already established a live machine-local sink through one of the two proof seams;
// this function binds that proof to the current instance and latest history boundary,
// then commits the recovered record without granting either terminal bit.
func (d *Daemon) commitStructuredSinkProof(sessionID, expectedInstance, kind string) error {
	d.capStore.transitionMu.Lock()
	defer d.capStore.transitionMu.Unlock()
	d.capStore.mu.Lock()
	current, _, currentOK := d.sessionInstanceLocked(sessionID)
	marker, markerOK := d.historyTornMarkerLocked(sessionID)
	raw, recordOK := d.rawCapabilitiesLocked(sessionID)
	if !currentOK || current == "" || current != expectedInstance || !markerOK ||
		!recordOK || raw.SessionInstance != current || raw.Validate() != nil {
		d.capStore.mu.Unlock()
		return errStructuredSinkProof
	}
	// Multiple clean-drain/backend observers may prove the same sink concurrently.
	// transitionMu serializes them; after the first commits, every later observer is
	// an idempotent no-op rather than another true transition and disk rewrite.
	if durable, durableOK := d.durableSinkProofLocked(sessionID); durableOK {
		if live, liveOK := d.capStore.liveProof[sessionID]; liveOK && raw.StructuredChat &&
			sinkProofMatches(durable, marker, current) &&
			sinkProofMatches(live, marker, current) && durable.Kind == live.Kind {
			d.capStore.mu.Unlock()
			return nil
		}
	}
	proof := structuredSinkProof{
		Version: structuredSinkProofVersion, SessionInstance: current,
		GapGeneration: marker.Generation, Kind: kind,
	}
	recovered := raw
	recovered.StructuredChat = true
	recovered.TerminalFallback = false
	recovered.TerminalControl = false
	if recovered.Validate() != nil {
		d.capStore.mu.Unlock()
		return errStructuredSinkProof
	}
	// Crash order: cap first, proof sidecar last. Lookup requires both plus the
	// process-local proof, so every prefix remains disabled.
	if err := d.persistSessionCapabilitiesLocked(sessionID, recovered); err != nil {
		d.capStore.mu.Unlock()
		return err
	}
	proofPath := d.sessionStatePathLocked(sessionID, sessionSinkProofFile)
	if proofPath == "" {
		d.capStore.mu.Unlock()
		return errStructuredSinkProof
	}
	if err := writeJSONStateFile(proofPath, proof); err != nil {
		d.capStore.mu.Unlock()
		return err
	}
	d.capStore.byID[sessionID] = recovered
	d.capStore.mu.Unlock()

	if err := d.publishCapabilityTransition(sessionID, recovered); err != nil {
		// Do not leave daemon-enabled / phone-stale authority. The durable commit is
		// retained for a retry, but no process-local proof becomes authoritative until
		// its ordered transition is durably appended.
		return err
	}
	d.capStore.mu.Lock()
	if d.capStore.liveProof == nil {
		d.capStore.liveProof = map[string]structuredSinkProof{}
	}
	d.capStore.liveProof[sessionID] = proof
	d.capStore.mu.Unlock()
	return nil
}

// deriveSessionCapabilities builds the capability record for a newly launched session
// instance. providerVersion is the detected CLI version and adapterRevision is the Swarm
// adapter revision that produced the record; both are carried through verbatim.
//
// IT IS WIRED, AND UNTIL WAVE R8 IT WAS NOT. This paragraph used to say "THIS FUNCTION IS DEAD
// CODE TODAY, AND SO IS ITS `liveBackend` ARGUMENT ... a reader must not come away believing a
// Codex session's capability record says any of this today -- it has none", which was true when
// R7 wrote it and is now false in the opposite direction (round-3 minor 8). R8's
// authorSessionCapabilities is the one authoring entry point and it calls this function, from
// five production call sites: the client-facing launch/resume seam, the core's session-start
// hook, the reconcile re-adoption of a running session, the session tap and the backend join.
// A live session's record says exactly what this derivation says.
//
// What R7 left, and what R8 reused rather than rewrote, is the per-session-instance
// correction: `liveBackend` is a fact about THIS incarnation, and the caller supplies it from
// backendPlaneDecided rather than from anything ambient. What remains deferred is
// agents-tracker-hggx.2.1's T3 version-skew row, and its absence is stated in the wave's
// evidence rather than here.
func deriveSessionCapabilities(provider string, a adapter.Adapter, providerVersion, adapterRevision string, liveBackend bool) protocol.SessionCapabilities {
	// WAVE R7 CORRECTS A REAL DERIVATION DEFECT (ADR-013 §R7.7). Until now both fields were
	// facts about the ADAPTER TYPE. The moment the Codex adapter implements InteractionSource
	// -- which R7 is the wave that does it -- a PRE-UPGRADE Codex session (argv `codex`, no
	// --remote, no backend child, no backend_socket_path) would claim structured_chat=true and
	// the phone would show a composer whose every send is refused. So the derivation is now
	// SEAM AND LIVE BACKEND, PER SESSION INSTANCE.
	//
	// `liveBackend` is not ANDed into every provider: a provider that needs no backend
	// (Claude, whose structured plane is its HOOK channel) proves no BackendSource and its
	// derivation is untouched. ANDing a backend fact into every provider would have turned
	// this wave into a Claude regression.
	_, needsBackend := adapter.AsBackendSource(a)
	_, sourcesItems := adapter.AsInteractionSource(a)
	structured := sourcesItems && (!needsBackend || liveBackend)
	// Wave R6 (ADR-017 T2 rule 3): Interrupt is derived from the SAME seam InterruptTurn
	// executes, so the Stop affordance the phone renders and the op behind it agree by
	// construction -- true where a TurnInterrupter is proven, false where none is, never
	// inferred from structured_chat.
	//
	// R7 adds the OTHER half of that same rule, in the opposite direction: interruptTurn now
	// dispatches turn/interrupt on a live backend BEFORE it consults AsTurnInterrupter, so a
	// Codex session whose RPC interrupt is RECORDED working would otherwise read
	// interrupt:false and the phone would hide a Stop button that works.
	_, keystrokeInterrupt := adapter.AsTurnInterrupter(a)
	interrupt := keystrokeInterrupt || (needsBackend && liveBackend)
	// WAVE R8 (ADR-017 T6-b): terminal_control is authored TRUE AT LAUNCH for a provider
	// whose fallback is its DESIGNED surface, and never derived on the phone from
	// terminal_fallback. The two look the same here and diverge on exactly the sessions
	// the ruling is about historically: a history/sink failure now authors the stricter
	// {structured_chat:false, terminal_fallback:false, terminal_control:false} shape and
	// never grants a terminal screen or keyboard.
	return protocol.SessionCapabilities{
		Provider:         provider,
		ProviderVersion:  providerVersion,
		AdapterRevision:  adapterRevision,
		StructuredChat:   structured,
		TerminalFallback: !structured,
		TerminalControl:  !structured,
		Interrupt:        interrupt,
	}
}
