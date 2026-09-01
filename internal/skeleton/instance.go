package skeleton

// THE SESSION INSTANCE, AND THE ONE AUTHORING ENTRY POINT (ADR-017 T8-a / T2-a, Wave R8).
//
// WHY AN INSTANCE AT ALL. ADR-017 binds the capability record, the control generation and
// every TerminalView snapshot to "the session instance", and makes session replacement a
// synchronous severance trigger. What the repository had was a session ID that survives a
// shim restart, a resume AND a daemon restart -- so "a generation dies with its instance"
// was a sentence with no referent, and a generation minted against one PTY would have
// authorised raw bytes into its replacement.
//
// The distinction the whole rule turns on, and the one an implementation is most likely
// to collapse:
//
//   - a DAEMON RESTART re-adopts the SAME incarnation. The shim is still running, the PTY
//     is the same PTY, and the phone's view must not reset. The instance is read back from
//     the session's own directory.
//   - a SESSION REPLACEMENT is a new incarnation: new shim, new PTY. The instance changes,
//     and T4-a requires the phone to meet that as an EPOCH RESET WITH A CHANGED INSTANCE
//     rather than as a seamless continuation -- which is what makes the gateway watcher's
//     silent supervised reconnect safe to keep.
//
// WHERE IT LIVES IS ITS LIFETIME. The instance is a 0600 side-file in the session's own
// 0700 dir, beside meta.json, shim-launch.json and capabilities.json, for the same reason
// capability.go gives for the record: "the side-file has the session's own lifetime by
// construction". A per-daemon or per-process store would hand a restarted daemon no way
// to tell adoption from replacement.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/detect"
	"github.com/Nathandela/swarm/internal/adapter/registry"
	"github.com/Nathandela/swarm/internal/protocol"
)

// sessionInstanceFile is the per-incarnation identifier's on-disk name.
const sessionInstanceFile = "session-instance"

type sessionIncarnation struct {
	ShimPID       int
	ShimStartTime int64
}

func incarnationOf(shimPID int, shimStartTime []int64) sessionIncarnation {
	var start int64
	if len(shimStartTime) > 0 {
		start = shimStartTime[0]
	}
	return sessionIncarnation{ShimPID: shimPID, ShimStartTime: start}
}

// mintSessionInstance returns a fresh, unguessable per-incarnation identifier.
//
// IT IS NOT A FUNCTION OF THE SESSION ID, and that is the whole point (D-INSTANCE's
// mutation fence): if the instance were a hash, a prefix or the id itself, a replacement
// would produce the same value and every binding built on it -- the generation, the
// snapshot's epoch reset, the stale-instance refusal -- would be decorative.
func mintSessionInstance() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on any supported platform; if it ever does, a
		// PREDICTABLE instance is worse than none, so panic rather than degrade to a
		// counter an attacker can guess.
		panic("skeleton: mint session instance: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// recordSessionInstance persists instance as sessionID's current incarnation, together with
// the INCARNATION IT WAS MINTED FOR (the shim's pid), replacing whatever a previous
// incarnation left.
//
// It is reached from exactly one production caller, adoptOrMintSessionInstance, which is
// where the adopt-or-replace decision is made; that is deliberate, so "when does a session
// get a new instance" is one rule in one place rather than a rule per call site. Round 2's
// comment here claimed two call sites -- "at shim spawn (a fresh launch) and at replacement"
// -- and NEITHER existed: the repository has one shim-spawn path (daemon.Launch), it always
// mints a fresh session id, and nothing re-minted on replacement at all.
func (d *Daemon) recordSessionInstance(sessionID, instance string, shimPID int, shimStartTime ...int64) error {
	if instance == "" {
		return fmt.Errorf("skeleton: refusing to record an empty session instance for %q", sessionID)
	}
	d.capStore.authorMu.Lock()
	defer d.capStore.authorMu.Unlock()
	d.capStore.transitionMu.Lock()
	defer d.capStore.transitionMu.Unlock()
	d.capStore.mu.Lock()
	defer d.capStore.mu.Unlock()
	if prior, _, ok := d.sessionInstanceLocked(sessionID); ok && prior != instance {
		delete(d.capStore.liveProof, sessionID)
		_ = os.Remove(d.sessionStatePathLocked(sessionID, sessionSinkProofFile))
	}
	if d.capStore.instances == nil {
		d.capStore.instances = map[string]string{}
	}
	d.capStore.instances[sessionID] = instance
	incarnation := incarnationOf(shimPID, shimStartTime)
	if d.capStore.incarnations == nil {
		d.capStore.incarnations = map[string]sessionIncarnation{}
	}
	d.capStore.incarnations[sessionID] = incarnation
	path := d.sessionStatePathLocked(sessionID, sessionInstanceFile)
	if path == "" {
		return nil // no durable home (a bare literal in a test): memory is the whole store
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeSessionStateFile(path, []byte(encodeSessionInstance(instance, incarnation)))
}

// encodeSessionInstance is the side-file's one-line format: `<instance> <pid> <start-time>`.
//
// THE FORMAT IS APPEND-ONLY BY CONSTRUCTION. A file written by an earlier build carries the
// bare instance with no space, which decodeSessionInstance reads as "incarnation unknown" --
// and an unknown incarnation ADOPTS. Without that, the upgrade that lands this change would
// re-mint for every session on the machine at once and show the phone an epoch reset with no
// shim restart behind any of them.
func encodeSessionInstance(instance string, incarnation sessionIncarnation) string {
	return instance + " " + strconv.Itoa(incarnation.ShimPID) + " " + strconv.FormatInt(incarnation.ShimStartTime, 10)
}

// decodeSessionInstance splits the side-file. An absent or unparsable tuple component is zero,
// which is legacy/unknown and is migrated on the next complete current observation.
func decodeSessionInstance(raw string) (instance string, incarnation sessionIncarnation) {
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) == 0 {
		return "", sessionIncarnation{}
	}
	if len(fields) > 1 {
		if n, err := strconv.Atoi(fields[1]); err == nil {
			incarnation.ShimPID = n
		}
	}
	if len(fields) > 2 {
		if n, err := strconv.ParseInt(fields[2], 10, 64); err == nil {
			incarnation.ShimStartTime = n
		}
	}
	return fields[0], incarnation
}

// sessionInstance resolves sessionID's current incarnation -- from memory, else from the
// durable side-file a PRIOR daemon incarnation wrote, which is what makes a daemon
// restart an adoption rather than a replacement.
func (d *Daemon) sessionInstance(sessionID string) (string, bool) {
	d.capStore.mu.Lock()
	defer d.capStore.mu.Unlock()
	inst, _, ok := d.sessionInstanceLocked(sessionID)
	return inst, ok
}

// sessionInstanceLocked is sessionInstance's body, and it also returns the incarnation the
// instance was minted for so adoptOrMintSessionInstance can tell adoption from replacement.
// Caller holds capStore.mu.
func (d *Daemon) sessionInstanceLocked(sessionID string) (string, sessionIncarnation, bool) {
	if inst, ok := d.capStore.instances[sessionID]; ok && inst != "" {
		return inst, d.capStore.incarnations[sessionID], true
	}
	path := d.sessionStatePathLocked(sessionID, sessionInstanceFile)
	if path == "" {
		return "", sessionIncarnation{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", sessionIncarnation{}, false
	}
	inst, incarnation := decodeSessionInstance(string(data))
	if inst == "" {
		return "", sessionIncarnation{}, false
	}
	if d.capStore.instances == nil {
		d.capStore.instances = map[string]string{}
	}
	if d.capStore.incarnations == nil {
		d.capStore.incarnations = map[string]sessionIncarnation{}
	}
	d.capStore.instances[sessionID] = inst
	d.capStore.incarnations[sessionID] = incarnation
	return inst, incarnation, true
}

// adoptOrMintSessionInstance returns sessionID's instance for THIS incarnation, minting and
// persisting a fresh one when the session has none or when the incarnation on file is not
// the one being asked about.
//
// THE INCARNATION IS THE SHIM'S PID, and it is what makes the distinction the whole rule
// turns on observable rather than aspirational (round-3 blocker 2b):
//
//   - a DAEMON RESTART re-adopts the SAME shim, so the pid matches and the instance is read
//     back off disk. The phone's view must not reset for a daemon upgrade;
//   - a SESSION REPLACEMENT is a NEW shim and a new PTY, so the pid differs and a fresh
//     instance is minted. T4-a requires the phone to meet that as an epoch reset with a
//     changed instance, and T8 makes it a synchronous severance trigger -- neither of which
//     has a referent if the identifier is really the session id wearing another name;
//   - a ZERO incarnation is UNKNOWN, not different, and adopts. Two states produce it -- a
//     side-file written before this format, and a caller with no meta -- and re-minting on
//     either resets every existing session's view exactly once for no shim restart at all.
func (d *Daemon) adoptOrMintSessionInstance(sessionID string, shimPID int, shimStartTime ...int64) (string, error) {
	incarnation := incarnationOf(shimPID, shimStartTime)
	d.capStore.mu.Lock()
	inst, onFile, ok := d.sessionInstanceLocked(sessionID)
	d.capStore.mu.Unlock()
	if ok {
		if incarnation.ShimPID == 0 {
			return inst, nil
		}
		if onFile.ShimPID != 0 && onFile.ShimPID != incarnation.ShimPID {
			// Different known PIDs are different processes even when the caller is too old to
			// provide start time.
		} else if incarnation.ShimStartTime == 0 || onFile == incarnation {
			return inst, nil
		} else if onFile.ShimPID == 0 || onFile.ShimStartTime == 0 {
			// Upgrade a bare or pid-only side-file in place. The same running process keeps its
			// opaque instance; only its durable reuse fence becomes complete.
			if err := d.recordSessionInstance(sessionID, inst, incarnation.ShimPID, incarnation.ShimStartTime); err != nil {
				return "", err
			}
			return inst, nil
		}
	}
	fresh := mintSessionInstance()
	if err := d.recordSessionInstance(sessionID, fresh, incarnation.ShimPID, incarnation.ShimStartTime); err != nil {
		return "", err
	}
	return fresh, nil
}

// writeSessionStateFile writes data atomically at 0600 (temp+fsync+rename), the pattern
// persistSessionCapabilitiesLocked already uses, so a torn write can never be read back
// as a valid-but-wrong instance.
func writeSessionStateFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*") // os.CreateTemp creates 0600
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

// authorSessionCapabilities is THE ONE authoring entry point (ADR-017 T2-a / D-NIL):
// derive, bind the instance, VALIDATE, register, persist. Every session-creation path
// calls it, so "every session-creation path authors a record" is a statement about one
// function with several call sites rather than several independent implementations of
// the same rule.
//
// IT VALIDATES BEFORE IT STORES, and refuses without a trace. Validation at the author is
// what makes the gateway-decode and phone-decode seams a defence in depth rather than the
// only defence: a record that never existed in the inconsistent state cannot be decoded
// out of a stale capabilities.json a year later. A refused authoring leaves NOTHING
// behind, because a partially-authored record is exactly the inconsistent state T2-b
// makes unrepresentable.
func (d *Daemon) authorSessionCapabilities(sessionID, instance, provider string, a adapter.Adapter, providerVersion, adapterRevision string, liveBackend bool) (protocol.SessionCapabilities, error) {
	d.capStore.authorMu.Lock()
	defer d.capStore.authorMu.Unlock()
	rec := deriveSessionCapabilities(provider, a, providerVersion, adapterRevision, liveBackend)
	rec.SessionInstance = instance
	if err := rec.Validate(); err != nil {
		return protocol.SessionCapabilities{}, fmt.Errorf("skeleton: refusing to author a capability record for session %q: %w", sessionID, err)
	}
	d.capStore.mu.Lock()
	currentInstance, currentIncarnation, hasCurrent := d.sessionInstanceLocked(sessionID)
	d.capStore.mu.Unlock()
	if hasCurrent && currentInstance != instance {
		return protocol.SessionCapabilities{}, fmt.Errorf("skeleton: stale capability author for session %q instance %q (current %q)", sessionID, instance, currentInstance)
	}

	publisher := d.sessionStatePublisher
	expectedIncarnation := currentIncarnation
	if d.core != nil {
		if m, present := d.core.Get(sessionID); present {
			currentMeta := sessionIncarnation{ShimPID: m.ShimPID, ShimStartTime: m.ShimStartTime}
			knownMismatch := currentIncarnation.ShimPID != 0 && currentIncarnation.ShimPID != currentMeta.ShimPID ||
				currentIncarnation.ShimStartTime != 0 && currentIncarnation.ShimStartTime != currentMeta.ShimStartTime
			if !hasCurrent || knownMismatch {
				return protocol.SessionCapabilities{}, fmt.Errorf("skeleton: stale capability author for session %q shim %d/%d (current %d/%d)",
					sessionID, currentIncarnation.ShimPID, currentIncarnation.ShimStartTime, m.ShimPID, m.ShimStartTime)
			}
			expectedIncarnation = currentMeta
			if publisher == nil {
				publisher = d.core.RecordSessionStateForIncarnation
			}
		}
	}
	if publisher == nil {
		d.registerSessionCapabilities(sessionID, rec)
		stored, ok := d.sessionCapabilities(sessionID)
		if !ok {
			return rec, nil
		}
		return stored, nil
	}

	d.capStore.transitionMu.Lock()
	defer d.capStore.transitionMu.Unlock()
	var stored protocol.SessionCapabilities
	var publishAttempted bool
	matched, err := publisher(sessionID, expectedIncarnation.ShimPID, expectedIncarnation.ShimStartTime, func() (json.RawMessage, error) {
		d.registerSessionCapabilitiesTransitionLocked(sessionID, rec)
		var ok bool
		stored, ok = d.sessionCapabilities(sessionID)
		if !ok {
			stored = rec
		}
		if d.capStore.publishedInstances != nil && d.capStore.publishedInstances[sessionID] == stored.SessionInstance {
			return nil, nil
		}
		publishAttempted = true
		return json.Marshal(stored)
	})
	if err != nil {
		return protocol.SessionCapabilities{}, err
	}
	if !matched {
		return protocol.SessionCapabilities{}, fmt.Errorf("skeleton: session %q incarnation %d/%d was replaced before capability publication",
			sessionID, expectedIncarnation.ShimPID, expectedIncarnation.ShimStartTime)
	}
	if publishAttempted {
		if d.capStore.publishedInstances == nil {
			d.capStore.publishedInstances = map[string]string{}
		}
		d.capStore.publishedInstances[sessionID] = stored.SessionInstance
	}
	return stored, nil
}

// sessionCapabilityInputs resolves everything authorSessionCapabilities needs from the
// daemon's own state: the session's instance (adopted, or minted if this is the first
// time anyone asked), its adapter, the detected CLI version, and whether it has a live
// backend right now.
//
// ok=false means NO RECORD IS AUTHORED, which by T2-a leaves the session on the honest
// status card. That is the answer for two cases, both deliberate: a session whose
// instance could not be persisted binds nothing, and the reserved dev "fake" agent has no
// adapter and no PTY worth showing.
func (d *Daemon) sessionCapabilityInputs(sessionID, agentType string, shimPID int, shimStartTime ...int64) (instance string, ad adapter.Adapter, version string, ok bool) {
	if sessionID == "" || agentType == "" {
		return "", nil, "", false
	}
	// A session whose agent has NO adapter still gets a record, and a nil adapter is the
	// right input rather than a reason to skip: deriveSessionCapabilities reads the
	// adapter through AsInteractionSource / AsBackendSource / AsTurnInterrupter, all of
	// which answer "absent" for nil, and absence is the signal (ADR-010 §5). The reserved
	// dev "fake" agent therefore authors {structured:false, fallback:true} -- which is
	// true of it: it has a real PTY and no structured plane.
	ad, _ = d.resolveAdapter(agentType)
	inst, err := d.adoptOrMintSessionInstance(sessionID, shimPID, shimStartTime...)
	if err != nil {
		log.Printf("skeleton: session %s has no instance, so it keeps the status card: %v", sessionID, err)
		return "", nil, "", false
	}
	return inst, ad, d.providerVersion(agentType), true
}

// backendPlaneDecided answers whether this session's STRUCTURED PLANE FACT is knowable
// yet, and what it is.
//
// IT EXISTS BECAUSE A RECORD MAY NOT BE AUTHORED EARLY AND WRONG. R7 made structured_chat
// "the seam AND a live backend, per session instance", and T2 rule 2 makes a
// structured_chat degrade ONE-WAY -- so authoring at launch, in the window before a
// side-process backend has dialled, would pin such a session at structured_chat=false
// FOREVER and show the phone a status card for a session that is about to become a
// perfectly good chat. The record is authored once the fact is known, and not before:
//
//   - an adapter that proves no BackendSource needs no backend at all, so the fact is
//     decided at launch and the backend is irrelevant to its derivation;
//   - a live registered backend decides it true;
//   - a PROVEN unavailable backend (the durable degrade marker noteBackendUnavailable
//     writes) decides it false;
//   - otherwise it is still dialling, and nothing is authored yet. backend.go authors it
//     from whichever of the two outcomes arrives.
func (d *Daemon) backendPlaneDecided(sessionID string, ad adapter.Adapter) (live, decided bool) {
	if _, needsBackend := adapter.AsBackendSource(ad); !needsBackend {
		return false, true
	}
	if _, ok := d.sessionBackendFor(sessionID); ok {
		return true, true
	}
	if d.sessionDegraded(sessionID) {
		return false, true
	}
	return false, false
}

// authorSessionCapabilitiesWhenDecided is the shape every non-backend call site uses: it
// authors only once backendPlaneDecided says the structured-plane fact is knowable, and
// stays silent until then rather than authoring a record it would have to un-say.
func (d *Daemon) authorSessionCapabilitiesWhenDecided(sessionID, agentType string, shimPID int, shimStartTime ...int64) {
	inst, ad, version, ok := d.sessionCapabilityInputs(sessionID, agentType, shimPID, shimStartTime...)
	if !ok {
		return // no bindable instance: T2-a's honest status card
	}
	live, decided := d.backendPlaneDecided(sessionID, ad)
	if !decided {
		return // still dialling; backend.go authors it from whichever outcome arrives
	}
	if _, err := d.authorSessionCapabilities(sessionID, inst, agentType, ad, version, adapterRevision, live); err != nil {
		log.Printf("skeleton: author capability record for session %s: %v", sessionID, err)
	}
}

// detectProviderVersion is the production probe behind the seam: the CORE adapter.Detect
// over the real host prober, which is descriptor-based (LookPath + one --version exec) and
// returns "" for a CLI that is not installed. A version that cannot be detected is
// reported as UNKNOWN rather than guessed, because playbook:280's honest header names the
// detected version and "" is a fact the phone can render as one.
func detectProviderVersion(agentType string) string {
	a, ok := registry.New(agentType)
	if !ok {
		return ""
	}
	det := adapter.Detect(a, detect.Host{})
	if !det.Found {
		return ""
	}
	return det.Version
}

// adapterRevision is the revision of the Swarm adapter set that produced a record. It is
// carried verbatim onto the record so a phone can tell "this machine's adapters are older
// than the record format I know" apart from "this provider is old".
const adapterRevision = "r8"

// providerVersion is the DETECTED version of an installed CLI, behind a daemon seam so
// the record carries a fact about the host rather than a guess. It is cached per agent
// type for the daemon's life: the answer cannot change without the binary changing, and
// probing on every session launch would exec a subprocess on a hot path.
func (d *Daemon) providerVersion(agentType string) string {
	d.capStore.mu.Lock()
	if v, ok := d.capStore.versions[agentType]; ok {
		d.capStore.mu.Unlock()
		return v
	}
	probe := d.detectProviderVersion
	d.capStore.mu.Unlock()
	var v string
	if probe != nil {
		v = probe(agentType)
	}
	d.capStore.mu.Lock()
	if d.capStore.versions == nil {
		d.capStore.versions = map[string]string{}
	}
	d.capStore.versions[agentType] = v
	d.capStore.mu.Unlock()
	return v
}
