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
// interrupt is left at its zero value (false) for every adapter: nothing in
// internal/adapter exposes a LifecycleSink-style interrupt seam to consult, so there is
// nothing to derive it from. T6's own closing paragraph gives the honest default for an
// unset record: "This provider version has no safe remote interrupt." Guessing interrupt
// from structured_chat would assert a capability no seam proves, which is exactly what T2
// rule 3 ("the phone renders from the record and infers nothing") forbids one layer up.
// Tracked in bd agents-tracker-hggx.2.1 alongside the LifecycleSink seam itself.

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"

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
// under sessionID before the incoming one is stored, so a later re-registration --
// exactly what a daemon restart's reconcile performs for every reconnected session --
// can never resurrect a session a prior incarnation degraded (ADR-017 T2 rule 2's
// "one-way", carried across the daemon's own restart).
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
	mu   sync.Mutex
	dir  string // durable home (the daemon's state dir); "" = memory only
	byID map[string]protocol.SessionCapabilities
}

// sessionCapabilityFile is the per-session capability record's on-disk name, sibling of
// meta.json and shim-launch.json inside a session's own 0700 dir.
const sessionCapabilityFile = "capabilities.json"

// sessionDegradedFile is the durable "this session instance proved a structured gap"
// marker, sibling of the record above.
//
// WHY IT EXISTS SEPARATELY (R6 review fix-pack round 1, BLOCKER 1). A degrade can be
// proven BEFORE any capability record has been authored -- today that is in fact the
// normal case, because nothing in production calls deriveSessionCapabilities yet
// (capability.go's own header defers the T3 gate and the remaining adapter seams to bd
// agents-tracker-hggx.2.1). Degrading "the record, if one exists" would therefore be a
// guaranteed no-op in production, and the first record authored afterwards would claim
// structured_chat=true over a session with a proven, unrecoverable hole in its event
// stream. The marker records the FACT of the gap without inventing a record to hold it:
// ADR-017 T2 says capability records are authored at launch and this seam may only
// degrade them, never fabricate one, so the degrade is stored as what it is.
const sessionDegradedFile = "structured-degraded"

// registerSessionCapabilities stores c for sessionID, merging against any existing
// record (in memory, else on disk) via SetStructuredChat's one-way degrade rule.
func (d *Daemon) registerSessionCapabilities(sessionID string, c protocol.SessionCapabilities) {
	d.capStore.mu.Lock()
	defer d.capStore.mu.Unlock()
	if existing, ok := d.lookupCapabilitiesLocked(sessionID); ok {
		// SetStructuredChat refuses an upgrade (false->true) and leaves existing
		// unchanged; it honors a degrade (true->false), forcing TerminalFallback true.
		// Either way, existing now holds the merged structured_chat/terminal_fallback
		// pair, which is what actually governs the session -- an already-degraded
		// session's incoming record is stored with that pair overridden back in, so
		// its OTHER fields (provider_version, adapter_revision, ...) can still refresh.
		_ = existing.SetStructuredChat(c.StructuredChat)
		c.StructuredChat = existing.StructuredChat
		c.TerminalFallback = existing.TerminalFallback
	}
	if d.sessionDegradedLocked(sessionID) {
		// A gap was proven for this session instance -- possibly by an incarnation
		// that is gone, possibly before any record existed to degrade. Either way the
		// answer to "may this session claim structured chat" is already no, and this
		// incoming record does not get to reopen it (ADR-017 T2 rule 2).
		_ = c.SetStructuredChat(false)
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

// SessionCapabilities returns sessionID's stored capability record, if any -- from
// memory, or from the durable side-file a PRIOR daemon incarnation wrote.
func (d *Daemon) SessionCapabilities(sessionID string) (protocol.SessionCapabilities, bool) {
	d.capStore.mu.Lock()
	defer d.capStore.mu.Unlock()
	return d.lookupCapabilitiesLocked(sessionID)
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
	if c.StructuredChat && d.sessionDegradedLocked(sessionID) {
		_ = c.SetStructuredChat(false) // forces TerminalFallback true (protocol/schema)
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

// markSessionDegraded durably records that sessionID proved a structured gap, then
// degrades its stored record if one exists. One-way and idempotent: calling it on an
// already-degraded session changes nothing.
func (d *Daemon) markSessionDegraded(sessionID string) {
	d.capStore.mu.Lock()
	defer d.capStore.mu.Unlock()
	if path := d.sessionStatePathLocked(sessionID, sessionDegradedFile); path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			log.Printf("skeleton: mark session %s structurally degraded: %v", sessionID, err)
		} else if err := writeDegradedMarker(path); err != nil {
			log.Printf("skeleton: mark session %s structurally degraded: %v", sessionID, err)
		}
	}
	// The RAW record on purpose: lookupCapabilitiesLocked now applies the marker written
	// just above, so it would report an already-degraded record and this would never
	// rewrite a stale structured_chat=true one back to disk.
	c, ok := d.rawCapabilitiesLocked(sessionID)
	if !ok {
		return // no record authored yet: the marker above is the whole degrade
	}
	if !c.StructuredChat {
		return // already degraded; nothing to write
	}
	_ = c.SetStructuredChat(false)
	d.capStore.byID[sessionID] = c
	if err := d.persistSessionCapabilitiesLocked(sessionID, c); err != nil {
		log.Printf("skeleton: persist degraded capability record for session %s: %v", sessionID, err)
	}
}

// writeDegradedMarker creates the marker file (0600) and fsyncs its directory, so the
// degrade survives the power loss as well as the process exit. Re-marking is a no-op.
func writeDegradedMarker(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
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
	return os.Rename(tmpName, path)
}

// deriveSessionCapabilities builds the capability record for a newly launched session
// instance. providerVersion is the detected CLI version and adapterRevision is the Swarm
// adapter revision that produced the record; both are carried through verbatim.
func deriveSessionCapabilities(provider string, a adapter.Adapter, providerVersion, adapterRevision string) protocol.SessionCapabilities {
	_, structured := adapter.AsInteractionSource(a)
	return protocol.SessionCapabilities{
		Provider:         provider,
		ProviderVersion:  providerVersion,
		AdapterRevision:  adapterRevision,
		StructuredChat:   structured,
		TerminalFallback: !structured,
		// Interrupt stays false (zero value): no LifecycleSink seam exists to derive it
		// from. See the package doc above and bd agents-tracker-hggx.2.1.
	}
}
