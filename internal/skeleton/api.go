package skeleton

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/registry"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/journal"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"

	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/device"
	"github.com/Nathandela/swarm/internal/remote/grant"
	"github.com/Nathandela/swarm/internal/remote/machineid"
	"github.com/Nathandela/swarm/internal/status"
)

const (
	// eventPoll is how often the roster is sampled for status changes (well within
	// the L1 <=1 s bound). It mirrors protocol.FromDaemon's cadence.
	eventPoll = 200 * time.Millisecond
	// eventsBuffer sizes the roster event channel the Server fans out from.
	eventsBuffer = 64
)

// coreAPI adapts the core *daemon.Daemon to the protocol.DaemonAPI the Server
// wraps. It is a leak-free, self-contained equivalent of protocol.FromDaemon: the
// same list/kill/delete/attach forwarding and roster-poll event source, plus the
// walking-skeleton's reserved-agent "fake" argv resolution on Launch. It is owned
// here (not FromDaemon) so its poller is stopped deterministically on Close — the
// daemon owns the socket, so the Server never runs FromDaemon's own stop path.
type coreAPI struct {
	core         *daemon.Daemon
	fakeAgentBin string
	endpointID   string // this daemon's stable federation id (resume source validation)
	// externalResumeMu makes provider-native adoption idempotent across concurrent
	// owner-tier requests: lookup and launch are one critical section.
	externalResumeMu sync.Mutex

	// historyResolver performs the lazy, read-only migration for an ended/lost
	// source whose native id predates durable capture. recoveryMu protects only
	// the bounded in-flight map; scans and metadata I/O run outside it.
	historyResolver       resumeHistoryResolver
	recoveryMu            sync.Mutex
	recoveries            map[string]*resumeRecoveryCall
	beforeRecoveryPersist func() // private deterministic race seam; nil in production

	// devices is the pinned-device registry backing R-POL.9 remote-command
	// authorization. It is nil until wired at assembly; a nil registry authorizes
	// nothing (authorizeCommand fails closed), so a remote-tier Server built on a
	// coreAPI without a registry refuses every mutating op. clock is the expiry clock
	// (nil => time.Now).
	devices *device.Registry
	clock   func() time.Time

	// launchPolicy is the machine-configured remote launch policy (allowed cwd roots,
	// R-POL.3/.7), loaded at assembly. nil until wired; the assembly ALWAYS wires a
	// non-nil deny-all-by-default policy, so the remote tier is fail-closed even with no
	// config file. A nil policy denies (fail-closed) via RemoteLaunchAllowed.
	launchPolicy protocol.LaunchPolicy

	// approve is the approval lifecycle's validator (IS-LIFE-4), handed in at assembly
	// (Daemon.approveInteraction). It is a func field rather than a back-pointer to the outer
	// Daemon for the reason sampleFn and captureFn are: one seam, not the whole assembly.
	// nil => the daemon cannot answer an approve, and says so.
	approve func(machine, operationID string, req protocol.ApproveReq) (protocol.ErrorCode, error)

	// The Wave R6 complete-chat seams (chat.go), handed across at assembly exactly as
	// approve is: composer applies one composer_send (M2.4), interrupt one turn_interrupt,
	// history serves ADR-014's paged read (M3.1) and detail IS-CAP-2's full-body read
	// (M3.3). Each is nil in a bare test literal; the coreAPI methods below then refuse
	// loudly rather than pretending (ApproveInteraction's rule).
	composer              func(machine, operationID string, req protocol.ComposerSendReq) (protocol.ErrorCode, error)
	composerTransactional func(machine, operationID string, req protocol.ComposerSendReq, begin func() error) (protocol.ErrorCode, error)
	interrupt             func(machine, operationID string, req protocol.TurnInterruptReq) (protocol.ErrorCode, error)
	history               func(session, beforeItem string, limit int) ([]protocol.JournalRecord, bool, protocol.ErrorCode, error)
	detail                func(session, itemID string) (json.RawMessage, protocol.ErrorCode, error)

	// pairing carries the machine-side pairing identity + enrollment material and the
	// rendezvous seam BeginPairing hosts a real pairing on (slice A3.3-d). It is nil
	// until provisioned (a LATER slice: `swarm remote init`); a nil config makes
	// BeginPairing fail closed, so pairing is simply unsupported until keys exist.
	pairing *pairingConfig
	// lifecycleMu is the OUTERMOST coreAPI lifecycle-transaction lock (round-4 re-audit,
	// ADR-007). It serializes the ATOMIC CORE of the RevokeDevice transaction (presence check +
	// rotateEpoch + Remove + the Count()==0 sever DECISION) against the BeginPairing COMMIT section
	// (epoch re-check + enroll + AddSole + grant.Save), and two concurrent revokes against each
	// other. This closes the residual finding-1 epoch TOCTOU (a rotate+remove could interleave
	// between the commit's re-check and AddSole, enrolling under a stale epoch) and finding-4 (two
	// revokes both rotating the epoch key -- a lost update). Round-5 finding 2 (codex#5+sonnet#1):
	// the SLOW follow-up work -- the sever's deadline-bounded socket writes and grant.Delete's fsync
	// -- runs OUTSIDE this lock (the per-keystroke DeviceRegistered check is the independent
	// backstop), so a concurrent revoke/pair never stalls behind blocking network writes. Lock
	// ORDER: lifecycleMu is taken FIRST; rotateEpoch's pairingMu is taken INSIDE it, NEVER the
	// reverse, so no cycle can form. The sever's severMu is now taken only AFTER lifecycleMu is
	// released, so it never nests under lifecycleMu at all. BeginPairing takes it ONLY for the brief
	// commit -- never across the long handshake, and it is released BEFORE the result() notification.
	lifecycleMu sync.Mutex
	// pairingMu guards a.pairing. BeginPairing read it lock-free until revoke gained the
	// power to MUTATE it: RevokeDevice rotates the machine epoch key and reassigns the
	// snapshot (codex#1, ADR-007 2026-07-24). Held only for the pointer read/reassign,
	// never across the long pairing handshake, and always INSIDE lifecycleMu when taken during
	// a revoke/commit transaction (never the reverse), so no lock-ordering cycle can form.
	pairingMu sync.Mutex
	// lifecycleGate is a TEST-ONLY seam (nil in production, a no-op) invoked at the two
	// lifecycle-transaction points the round-4 serialization fix makes atomic: the pairing
	// commit's post-epoch-recheck window ("pair-commit") and the revoke's post-rotate window
	// ("revoke-rotated"). It lets a concurrency test deterministically interleave the operation
	// lifecycleMu must exclude (the residual finding-1 TOCTOU and the finding-4 double rotation).
	lifecycleGate func(point string)

	// stateDir is the daemon's persistent home; the durable remote-control kill-switch
	// state file (remote-state.json) is mirrored here (R-KS.1). Set at assembly.
	stateDir string
	// contextGuardSettings is the narrow owner-settings backend. It remains nil in
	// bare coreAPI tests, where the optional protocol seam honestly answers unavailable.
	contextGuardSettings *contextGuardSettingsStore
	contextGuards        *contextGuardManager
	// ksMu guards the read-time diff-write of the durable kill-switch state:
	// RemoteControlEnabled runs on every remote op and concurrently. ksPersisted is the
	// last enabled value written to remote-state.json this process (nil => never written),
	// so the mirror only writes on a transition, not on every call.
	ksMu        sync.Mutex
	ksPersisted *bool
	// manualOff is the durable OWNER override behind `swarm remote off`/`on` (A4): when
	// set, RemoteControlEnabled reports false regardless of paired devices (manual off WINS
	// over device presence). It is atomic so the hot RemoteControlEnabled read (every remote
	// op) is lock-free, and it is loaded from remote-state.json at assembly so an owner who
	// severs remote control stays severed across a restart.
	manualOff atomic.Bool
	// onRemoteControlDisabled, when set, is invoked whenever remote control transitions to
	// DISABLED — `swarm remote off` (SetRemoteControl(false)) or the last paired device removed
	// (RevokeDevice dropping Count to 0). The assembly wires it to the remote Server's
	// SeverAllRemoteControl so `off` PROACTIVELY tears down every live remote control lease +
	// terminal peek (C2a), not merely pausing per-keystroke input. Guarded by severMu; nil until
	// wired (no remote Server) and a no-op then. Mirrors the daemon's cross-package hook callbacks.
	severMu                 sync.Mutex
	onRemoteControlDisabled func()

	// controlledFn is the roster poller's source for a session's REMOTE controller
	// lease (the remote Server's IsControlled). It feeds the poller's diff key, not the
	// wire: the protocol Server stamps the SessionView from its own registered source.
	// Unset (no remote listener) reports false for every session.
	//
	// An atomic pointer, not a plain field: the poller goroutine starts in newCoreAPI,
	// BEFORE the assembly can build the remote Server and wire this.
	controlledFn atomic.Pointer[controlledFunc]
	// supervisionPendingFn is the roster poller's source for a session's pending
	// supervision event (ADR-010 Amendment 3 C5): live supervisor state, in the diff key
	// for the same reason controlledFn is, and an atomic pointer for the same reason too
	// (the supervisor is built after the poller starts).
	supervisionPendingFn atomic.Pointer[controlledFunc]

	// tap is the shared per-session output multiplexer (A7 F1). Attach routes through
	// it so the owner controller and the future remote peek can both observe one
	// single-consumer shim session over a SINGLE upstream. Wired at construction.
	tap *tapManager

	// onLaunched and sessionCaps are the assembly's two capability-record hooks
	// (ADR-017 T2-a): the first authors a record the instant a launch succeeds, the
	// second is the PURE READ the remote-tier gate consults. Both nil until Serve wires
	// them, and nil is fail-closed -- no record authored, no record found.
	onLaunched  func(persist.Meta)
	sessionCaps func(local string) (protocol.SessionCapabilities, bool)
	// syncName is the optional provider-name egress. The local rename commits first;
	// provider sync is best-effort and must never make the durable Swarm rename fail.
	syncName func(local, name string)

	events   chan persist.Meta
	nudge    chan struct{} // wakes the poller to sample NOW (it is the sole snapshot producer)
	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// AuthorizeCommand makes coreAPI a protocol.DeviceAuthenticator (R-POL.9): it verifies
// a remote command's Ed25519 signature against the pinned registry key and enforces the
// device's capability and the command's expiry. Fail-closed on every error.
func (a *coreAPI) AuthorizeCommand(cmd protocol.DeviceCommandAuth) error {
	return authorizeCommand(a.devices, a.now(), cmd)
}

// now returns the authorization clock (injectable for tests; defaults to time.Now).
func (a *coreAPI) now() time.Time {
	if a.clock != nil {
		return a.clock()
	}
	return time.Now()
}

// coreAPI ALSO satisfies protocol.DeviceAuthenticator so an assembled remote-tier
// Server authorizes remote mutating ops against the pinned device registry (R-POL.9).
var _ protocol.DeviceAuthenticator = (*coreAPI)(nil)

// RemoteLaunchAllowed makes coreAPI a protocol.LaunchPolicy (R-POL.3): it delegates to the
// loaded policy so an assembled remote-tier Server confines remote launches to the
// machine-configured cwd roots. A nil policy (never wired) denies every launch — fail-closed.
func (a *coreAPI) RemoteLaunchAllowed(resolvedCwd string) error {
	if a.launchPolicy == nil {
		return fmt.Errorf("remote launch policy unavailable")
	}
	return a.launchPolicy.RemoteLaunchAllowed(resolvedCwd)
}

// coreAPI ALSO satisfies protocol.LaunchPolicy so the assembled remote-tier Server confines
// remote launches to the configured cwd roots (R-POL.3).
var _ protocol.LaunchPolicy = (*coreAPI)(nil)

// ListDevices makes coreAPI a protocol.DeviceLister (slice A3.1): it converts the
// pinned device registry's roster to the wire-facing protocol.DeviceView, carrying
// the capability tier as its stable text form (device.Capability.MarshalText). A
// nil registry (never wired) reports no devices rather than panicking.
func (a *coreAPI) ListDevices() []protocol.DeviceView {
	if a.devices == nil {
		return nil
	}
	recs := a.devices.List()
	out := make([]protocol.DeviceView, 0, len(recs))
	for _, r := range recs {
		capText, err := r.Capability.MarshalText()
		if err != nil {
			continue // corrupted capability: fail closed by omitting the record
		}
		out = append(out, protocol.DeviceView{
			DeviceID:   r.DeviceID,
			Name:       r.Name,
			Capability: string(capText),
			PairedAt:   r.PairedAt,
		})
	}
	return out
}

// coreAPI ALSO satisfies protocol.DeviceLister so the assembled remote-tier Server
// can serve device_list (R-DEV.1).
var _ protocol.DeviceLister = (*coreAPI)(nil)

// RevokeDevice makes coreAPI a protocol.DeviceRevoker (slice A3.2): it removes
// deviceID from the pinned device registry. A nil registry (never wired) reports no
// device removed rather than panicking (nil-registry-safe like ListDevices).
func (a *coreAPI) RevokeDevice(deviceID string) (bool, error) {
	if a.devices == nil {
		return false, nil
	}
	// The ATOMIC CORE of the transaction runs under the OUTERMOST lifecycle lock (ADR-007):
	// presence check + rotateEpoch + Remove + grant.Delete + the Count()==0 sever DECISION. These are
	// fast LOCAL ops that all need the transaction's atomicity. In particular grant.Delete must stay
	// INSIDE the lock (round-6 finding 1, codex#1): a concurrent BeginPairing COMMIT of the SAME
	// device id (a re-pair, also serialized on lifecycleMu) would otherwise slip its AddSole +
	// grant.Save into the window between this revoke's Unlock and an outside-the-lock grant.Delete,
	// and the delete would then wipe the freshly-sealed sidecar -- bricking the re-paired phone (a
	// registered device with no deliverable grant). The closure uses defer Unlock so a panic in any
	// step still releases the lock (panic-safe, opus#1, mirroring BeginPairing's commit). lifecycleMu
	// is the OUTERMOST lock; rotateEpoch's pairingMu is taken inside it.
	//
	// Round-5 finding 2 (codex#5 + sonnet#1): ONLY the slow network sever (severRemoteControl ->
	// sendDetach's deadline-bounded socket writes) runs OUTSIDE this lock -- the per-keystroke
	// DeviceRegistered check is its independent backstop -- so a concurrent revoke/pair never stalls
	// behind blocking network writes. severMu is thus taken only AFTER lifecycleMu is released.
	removed, shouldSever, err := func() (bool, bool, error) {
		a.lifecycleMu.Lock()
		defer a.lifecycleMu.Unlock()
		// Only rotate/remove a device that is actually present: a revoke of an absent id is a no-op
		// (mirrors Registry.Remove's absent=false) and must NOT rotate the epoch. Under lifecycleMu
		// this check is atomic with the rotation, so a second concurrent revoke of the same device
		// finds it already gone here and does not rotate a second time (finding 4).
		if _, ok := a.devices.Get(deviceID); !ok {
			return false, false, nil
		}
		// Finding 3 (re-audit, crash-atomicity): ROTATE THE EPOCH BEFORE REMOVING the device so the
		// invariant "device removed => epoch rotated" holds across a crash between the two. codex#1:
		// the rotation kills the revoked device's retained content key for all future traffic. A
		// rotation/persist fault ABORTS the revoke (return the error; the device stays registered and
		// still severable) rather than removing under a stale, still-live key. Done BEFORE the sever
		// so pairingMu (taken in rotateEpoch) never nests inside severMu.
		if rerr := a.rotateEpoch(); rerr != nil {
			return false, false, rerr
		}
		removed, err := a.devices.Remove(deviceID)
		// A genuine PRE-rename Remove failure, or a raced-away device (removed==false), leaves nothing
		// removed: abort without severing or deleting a grant. The device is still registered (and
		// still severable) or was already gone -- either way there is no committed removal to follow.
		if !removed {
			return false, false, err
		}
		// Round-5 finding 1 (codex#2 + opus#1, CRITICAL REGRESSION): the device WAS durably removed --
		// even if Registry.Remove ALSO returned a trailing post-rename dir-fsync durability error. The
		// sever + grant.Delete MUST still run: skipping the last-device sever on a committed removal
		// leaves a still-running gateway's stale-epoch journal subscription alive, so after a re-pair
		// it re-seals the NEW session under the OLD key to the revoked device's mailbox. Decide the
		// last-device sever atomically under the lock (the Count read must be serialized with Remove).
		shouldSever := a.devices.Count() == 0
		// C4 + Finding 4b (re-audit): clean the device's sealed grant sidecar, INSIDE the lock (round-6
		// finding 1). Delete is idempotent (an absent sidecar -- e.g. a pre-grant pairing -- is not an
		// error). Surface the durability error first (the device IS removed; the dir-fsync just wasn't
		// confirmed), else the grant-cleanup error -- a stranded sidecar is a leak the operator must
		// learn about.
		derr := grant.Delete(a.registryDir(), deviceID)
		if err != nil {
			return true, shouldSever, err
		}
		return true, shouldSever, derr
	}()

	if shouldSever {
		// C2a: this removal took the LAST device (Count was 0) -> remote control transitions to
		// disabled; proactively sever every live remote control lease + terminal peek OUTSIDE the lock
		// (the per-device C1 sever in handleDeviceRevoke covers the revoked device's own lease; this
		// covers any lingering lease once the switch goes off).
		a.severRemoteControl()
	}
	return removed, err
}

// rotateEpoch rotates the machine epoch key after a device is revoked (codex#1): it
// loads the persisted machine identity, mints a fresh epoch (RotateEpoch), re-persists
// it atomically (temp+fsync+rename, 0600), and reloads the in-memory pairing snapshot so
// the NEXT BeginPairing seals the new device's grant under the new epoch. A MISSING
// machine identity is a no-op (pairing unprovisioned, nothing to rotate -- mirrors
// loadPairingConfig's tri-state); a present-but-broken identity surfaces the error.
func (a *coreAPI) rotateEpoch() error {
	path := filepath.Join(a.stateDir, "remote", remoteIdentityFile)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil // pairing unprovisioned: no epoch to rotate
		}
		return err
	}
	id, err := machineid.Load(path)
	if err != nil {
		return err
	}
	if err := id.RotateEpoch(); err != nil {
		return err
	}
	if err := id.Save(path); err != nil {
		return err
	}
	pc, err := loadPairingConfig(a.stateDir)
	if err != nil {
		return err
	}
	a.pairingMu.Lock()
	a.pairing = pc
	a.pairingMu.Unlock()
	if a.lifecycleGate != nil {
		a.lifecycleGate("revoke-rotated") // TEST-ONLY seam (nil in production): finding-4 window
	}
	return nil
}

// errNoRegistry refuses a re-grant on a daemon assembled without a device registry: there
// is no record to converge and no device to seal to.
var errNoRegistry = errors.New("skeleton: no device registry; nothing to re-grant")

// RegrantDevice is PB-KEY-3's machine-side unblock and PB-KEY-4's convergence, which are
// the same act: mint a FRESH sealed EpochGrant for a still-registered device under the
// CURRENT machine epoch, persist it as that device's sidecar, and update the device
// record's GrantedEpoch to match.
//
// It is the exit from PB-KEY-3's terminal state, and today there is no other. A grant can
// be lost with no recovery: the relay refuses appends past the mailbox depth cap and
// SweepRetention purges items older than RetentionCap (7 days) even when never acked, and
// re-pairing is refused outright because BeginPairing fail-fasts while a device is
// registered. Without this verb a phone that never received its grant -- or slept through a
// rotation -- is recoverable only by physical access to the machine.
//
// THE GrantedEpoch UPDATE IS NOT BOOKKEEPING. reconcilePairedDevices removes any device
// whose GrantedEpoch != the current machine epoch on EVERY daemon start (serve.go), so a
// re-grant that leaves the record on the old epoch does not merely fail to converge -- it
// SILENTLY UNPAIRS the only device on the next restart, and the owner discovers it when
// their phone stops working for no visible reason.
//
// ORDER, and it is fail-closed at each step. The seq allocation is made DURABLE (id.Save)
// before the grant is sealed, so a crash can only ever SKIP a coordinate, never re-issue
// one -- a re-issued (epoch, seq) is refused by the phone as a replay, which is the one
// outcome that leaves the device exactly as broken as it was. The sidecar lands before the
// record moves, because the gateway delivers by loading that file: a record on the new
// epoch with no sidecar for it is the "not fully paired" state reconcilePairedDevices
// clears by REMOVING the device.
//
// It runs under lifecycleMu, the outermost lock, so it is atomic against a concurrent
// revoke (which rotates the epoch and removes the device) and against a BeginPairing commit.
func (a *coreAPI) RegrantDevice(deviceID string) error {
	if a.devices == nil {
		return errNoRegistry
	}
	a.lifecycleMu.Lock()
	defer a.lifecycleMu.Unlock()

	// Fail closed on an unknown id. Minting a sidecar for a device the registry does not
	// hold would write a deliverable key nothing ever cleans up: the startup reconcile walks
	// the REGISTRY, not the sidecar directory.
	rec, ok := a.devices.Get(deviceID)
	if !ok {
		return fmt.Errorf("skeleton: no such device %q; nothing to re-grant", deviceID)
	}
	path := filepath.Join(a.stateDir, "remote", remoteIdentityFile)
	id, err := machineid.Load(path)
	if err != nil {
		return fmt.Errorf("load machine identity: %w", err)
	}
	// The floor is the coordinate this device has already been handed. It matters when the
	// epoch has NOT moved: a re-grant of a lost bootstrap reuses the live epoch, so only the
	// seq can carry the strict increase the phone's grant receiver demands.
	floor := uint64(0)
	if prev, lerr := grant.Load(a.registryDir(), deviceID); lerr == nil && prev != nil && prev.EpochID == id.EpochID() {
		floor = prev.GrantSeq
	}
	seq := id.NextGrantSeq(floor)
	if err := id.Save(path); err != nil {
		return fmt.Errorf("persist grant seq: %w", err)
	}
	g, err := crypto.SealEpochGrant(id.GrantSignPrivate(), rec.RecipientPub, id.EpochID(), seq, id.EpochKeys())
	if err != nil {
		return fmt.Errorf("seal epoch grant: %w", err)
	}
	if err := grant.Save(a.registryDir(), deviceID, g); err != nil {
		return fmt.Errorf("persist epoch grant: %w", err)
	}
	rec.GrantedEpoch = id.EpochID()
	if err := a.devices.Add(rec); err != nil {
		return fmt.Errorf("converge device record onto epoch %d: %w", id.EpochID(), err)
	}
	// The in-memory pairing snapshot carries the grant seq the NEXT BeginPairing would seal
	// under, so it must not keep naming one this re-grant has just consumed (the same reload
	// rotateEpoch does for the same reason).
	pc, err := loadPairingConfig(a.stateDir)
	if err != nil {
		return err
	}
	a.pairingMu.Lock()
	a.pairing = pc
	a.pairingMu.Unlock()
	return nil
}

// coreAPI ALSO satisfies protocol.DeviceRevoker so the assembled remote-tier Server
// can serve device_revoke (slice A3.2), and protocol.DeviceRegranter so the OWNER-tier
// Server can serve device_regrant (PB-KEY-3's unblock).
var (
	_ protocol.DeviceRevoker   = (*coreAPI)(nil)
	_ protocol.DeviceRegranter = (*coreAPI)(nil)
)

// DeviceRegistered makes coreAPI a protocol.DeviceRegistrar (C1): it reports whether deviceID
// is still present in the pinned device registry, so the daemon's controlGateOpen severs a
// revoked device's live control lease on the very next keystroke — independent of which Server
// handled the revoke. A nil registry (never wired) reports not-registered (fail-closed, like
// ListDevices/RevokeDevice).
func (a *coreAPI) DeviceRegistered(deviceID string) bool {
	if a.devices == nil {
		return false
	}
	_, ok := a.devices.Get(deviceID)
	return ok
}

// coreAPI ALSO satisfies protocol.DeviceRegistrar so the assembled remote-tier Server severs a
// revoked device's live control lease per keystroke (C1).
var _ protocol.DeviceRegistrar = (*coreAPI)(nil)

// DescribePolicy makes coreAPI a protocol.PolicyDescriber (slice A3.1): it reports
// the configured remote launch policy's allowed cwd roots. protocol.LaunchPolicy
// itself only carries RemoteLaunchAllowed, so the roots are obtained by type-asserting
// the loaded policy's own AllowedRoots() (remoteLaunchPolicy implements it); a nil or
// non-conforming policy reports an empty root set rather than panicking.
func (a *coreAPI) DescribePolicy() protocol.PolicyView {
	rp, ok := a.launchPolicy.(interface{ AllowedRoots() []string })
	if !ok {
		return protocol.PolicyView{}
	}
	return protocol.PolicyView{AllowedCwdRoots: rp.AllowedRoots()}
}

// coreAPI ALSO satisfies protocol.PolicyDescriber so the assembled remote-tier
// Server can serve policy_query (R-POL.3).
var _ protocol.PolicyDescriber = (*coreAPI)(nil)

// ApproveInteraction makes coreAPI a protocol.InteractionApprover (IS-LIFE-4): it validates one
// arriving approve against the stored ADR-007 D7 binding tuple and resolves the approval. It
// delegates to the outer Daemon's approveInteraction, which owns the pending-approval state.
//
// An UNWIRED approver refuses, and refuses LOUDLY. A bare test Daemon literal and a
// misassembled one both reach here with a nil func, and the two wrong answers are worse than
// the error: replying OK dismisses the card on every surface (IS-LIFE-2) while the CLI stays
// blocked on a permission nobody applied, and staying silent leaves the phone's op in flight
// forever. This is ListDevices' nil-registry rule with the direction that fits a mutation.
//
// It carries NO ErrorCode, and that is refusePushPrefs's rule (remotegw/command_loop.go): none
// of D10's six describes a machine-side assembly failure, and inventing a mapping tells the
// phone's retry policy something untrue -- not_authorized above all, which would send a
// correctly-paired owner off to re-pair a device that is fine.
func (a *coreAPI) ApproveInteraction(machine, operationID string, req protocol.ApproveReq) (protocol.ErrorCode, error) {
	if a.approve == nil {
		return "", errors.New("skeleton: this daemon has no interaction approver wired; nothing applied the decision")
	}
	return a.approve(machine, operationID, req)
}

// coreAPI ALSO satisfies protocol.InteractionApprover so the assembled remote-tier Server can
// serve approve (IS-LIFE-4).
var _ protocol.InteractionApprover = (*coreAPI)(nil)

// ComposerSend makes coreAPI a protocol.ComposerSender (Wave R6, Mirror M2.4): it delegates
// to the outer Daemon's composerSend (chat.go), which owns the expected_turn precondition,
// the PTY write and the injection-time attribution. An unwired seam refuses LOUDLY with no
// invented code, for ApproveInteraction's stated reasons: OK here is a sent message no
// agent received.
func (a *coreAPI) ComposerSend(machine, operationID string, req protocol.ComposerSendReq) (protocol.ErrorCode, error) {
	if a.composer == nil {
		return "", errors.New("skeleton: this daemon has no composer seam wired; nothing was typed")
	}
	return a.composer(machine, operationID, req)
}

// ComposerSendTransactional is the remote-tier durable variant. It keeps the operation in
// prepared while the request waits in the per-session FIFO and while chat.go validates the
// current incarnation/capability/sink, then invokes begin at the final provider-I/O boundary.
func (a *coreAPI) ComposerSendTransactional(machine, operationID string, req protocol.ComposerSendReq, begin func() error) (protocol.ErrorCode, error) {
	if a.composerTransactional == nil {
		return "", errors.New("skeleton: this daemon has no composer seam wired; nothing was typed")
	}
	return a.composerTransactional(machine, operationID, req, begin)
}

var _ protocol.TransactionalComposerSender = (*coreAPI)(nil)

// InterruptTurn makes coreAPI a protocol.TurnInterrupter (Wave R6): the semantic Stop.
func (a *coreAPI) InterruptTurn(machine, operationID string, req protocol.TurnInterruptReq) (protocol.ErrorCode, error) {
	if a.interrupt == nil {
		return "", errors.New("skeleton: this daemon has no interrupt seam wired; nothing was interrupted")
	}
	return a.interrupt(machine, operationID, req)
}

// InteractionHistory makes coreAPI a protocol.InteractionHistorian (Wave R6, M3.1).
func (a *coreAPI) InteractionHistory(session, beforeItem string, limit int) ([]protocol.JournalRecord, bool, protocol.ErrorCode, error) {
	if a.history == nil {
		return nil, false, "", errors.New("skeleton: this daemon has no interaction-history seam wired")
	}
	return a.history(session, beforeItem, limit)
}

// InteractionDetail makes coreAPI a protocol.InteractionDetailer (Wave R6, M3.3).
func (a *coreAPI) InteractionDetail(session, itemID string) (json.RawMessage, protocol.ErrorCode, error) {
	if a.detail == nil {
		return nil, "", errors.New("skeleton: this daemon has no interaction-detail seam wired")
	}
	return a.detail(session, itemID)
}

// coreAPI ALSO satisfies the four Wave R6 chat seams so the assembled remote-tier Server
// can serve composer_send / turn_interrupt / interaction_history / interaction_detail.
var (
	_ protocol.ComposerSender       = (*coreAPI)(nil)
	_ protocol.TurnInterrupter      = (*coreAPI)(nil)
	_ protocol.InteractionHistorian = (*coreAPI)(nil)
	_ protocol.InteractionDetailer  = (*coreAPI)(nil)
)

func newCoreAPI(core *daemon.Daemon, fakeAgentBin, endpointID string) *coreAPI {
	a := &coreAPI{
		core:         core,
		fakeAgentBin: fakeAgentBin,
		endpointID:   endpointID,
		events:       make(chan persist.Meta, eventsBuffer),
		nudge:        make(chan struct{}, 1),
		stop:         make(chan struct{}),
	}
	// The tap's dial seam is exactly today's Attach path (DialSession + the shared
	// shim-wire stream); the tap tees that one upstream to N subscribers. DialSession
	// returns the shim's negotiated caps since v0.6 (C3), and NewShimStream wants them.
	a.tap = newTapManager(func(id string) (protocol.SessionStream, error) {
		conn, caps, err := a.core.DialSession(id)
		if err != nil {
			return nil, err
		}
		return protocol.NewShimStream(conn, caps)
	})
	a.wg.Add(1)
	go a.watch()
	return a
}

func (a *coreAPI) List() []persist.Meta   { return a.core.List() }
func (a *coreAPI) Kill(id string) error   { return a.core.Kill(id) }
func (a *coreAPI) Delete(id string) error { return a.core.Delete(id) }
func (a *coreAPI) Rename(id, name string) error {
	err := a.core.Rename(id, name)
	if err == nil {
		a.pokeWatch() // fan the new name out now, not at the next poll tick
		if a.syncName != nil {
			a.syncName(id, name)
		}
	}
	return err
}
func (a *coreAPI) SetTag(id, tag string) error {
	err := a.core.SetTag(id, tag)
	if err == nil {
		a.pokeWatch() // fan the new grouping metadata out now, not at the next poll tick
	}
	return err
}
func (a *coreAPI) Events() <-chan persist.Meta { return a.events }

func (a *coreAPI) ContextGuardSettings() (protocol.ContextGuardSettings, error) {
	if a.contextGuardSettings == nil {
		return protocol.ContextGuardSettings{}, protocol.ErrContextGuardSettingsUnavailable
	}
	return a.contextGuardSettings.ContextGuardSettings()
}

func (a *coreAPI) SetContextGuardSettings(expectedRevision uint64, autoCompact protocol.ContextGuardAutoCompact) (protocol.ContextGuardSettings, error) {
	if a.contextGuardSettings == nil {
		return protocol.ContextGuardSettings{}, protocol.ErrContextGuardSettingsUnavailable
	}
	settings, err := a.contextGuardSettings.SetContextGuardSettings(expectedRevision, autoCompact)
	if err == nil && a.contextGuards != nil {
		a.contextGuards.updateSettings(settings)
	}
	return settings, err
}

var _ protocol.ContextGuardSettingsBackend = (*coreAPI)(nil)

func (a *coreAPI) ContextGuardView(sessionID string) (protocol.ContextGuardView, bool) {
	if a.contextGuards == nil {
		return protocol.ContextGuardView{}, false
	}
	return a.contextGuards.view(sessionID)
}

var _ protocol.ContextGuardViewBackend = (*coreAPI)(nil)

// ClaimOperation makes coreAPI a protocol.OperationClaimer (slice A5-c): it claims a
// remote op's operation_id single-use through the daemon's durable idempotency store so
// a take_control operation_id cannot be replayed to open a second lease. It delegates to
// the daemon's ClaimOperation wrapper (Prepare + existed).
func (a *coreAPI) ClaimOperation(operationID, action, session string) (bool, error) {
	if action == protocol.ActionComposerSend {
		if _, local, ok := protocol.ParseID(session); ok {
			session = local
		}
	}
	return a.core.ClaimOperation(operationID, action, session)
}

// coreAPI ALSO satisfies protocol.OperationClaimer so the assembled remote-tier Server
// enforces take_control operation_id single-use (slice A5-c).
var _ protocol.OperationClaimer = (*coreAPI)(nil)

// ClaimIdempotentOp / CommitIdempotentOp make coreAPI a protocol.IdempotentExecutor
// (slice DHI-3): they forward to the daemon's durable two-phase idempotency store so a
// replayed remote kill/delete returns the original attempt's cached outcome and executes
// the side effect exactly once.
func (a *coreAPI) ClaimIdempotentOp(operationID, action, session string) (existed, priorOK bool, err error) {
	return a.core.ClaimIdempotentOp(operationID, action, session)
}

func (a *coreAPI) CommitIdempotentOp(operationID string, ok bool) error {
	return a.core.CommitIdempotentOp(operationID, ok)
}

// coreAPI ALSO satisfies protocol.IdempotentExecutor so the assembled remote-tier Server
// makes remote kill/delete replay-safe (slice DHI-3).
var _ protocol.IdempotentExecutor = (*coreAPI)(nil)

// ComposerOperationExecutor keeps message delivery at-most-once across process death while
// retaining exact coded outcomes. The session is already local at this seam; the wire handler
// strips its endpoint exactly once before calling it.
func (a *coreAPI) ClaimComposerOperation(operationID, action, localSession, sessionInstance, requestHash string) (string, []byte, error) {
	return a.core.ClaimComposerOperation(operationID, action, localSession, sessionInstance, requestHash)
}

func (a *coreAPI) BeginComposerOperation(operationID string) error {
	return a.core.BeginComposerOperation(operationID)
}

func (a *coreAPI) CommitComposerOperation(operationID string, outcome []byte, success bool) error {
	return a.core.CommitComposerOperation(operationID, outcome, success)
}

var _ protocol.ComposerOperationExecutor = (*coreAPI)(nil)

// coreAPI ALSO satisfies protocol.JournalBackend so the assembled remote-tier
// Server can serve journal_read / journal_subscribe (DHI-1). The daemon and
// internal/journal stay free of a protocol import; the wire-type conversion lives
// here, where both packages are already in scope.
var _ protocol.JournalBackend = (*coreAPI)(nil)

// toWireJournalRecord converts a daemon-internal journal.Record to the wire-facing
// protocol.JournalRecord (only the fields the phone needs; the opaque payload and
// schema/ts are not carried on the wire). Agent IS one of those fields: the session
// row renders it, and this conversion is the only place it can cross.
//
// An `interaction` record is the ONE payload exception (interaction-schema.md §1): its
// payload IS the transcript item (IS-LAYER-1), so it crosses verbatim as Item. Every other
// type's payload -- `presence`'s online flag today -- stays daemon-internal, which is why
// the copy is gated on the type rather than done unconditionally.
// Name is the
// second: the session's user-given label heads the row the agent identity annotates,
// and it crosses here for the same reason and by the same rule.
func toWireJournalRecord(r journal.Record) protocol.JournalRecord {
	return toWireJournalRecordWith(r, nil)
}

// toWireJournalRecordWith is toWireJournalRecord plus the ADR-017 T2 capability lookup
// the ROSTER carries. caps is nil where no lookup is available, and a nil lookup means the
// record ships with no capability record -- which by T2-a is the honest status card, not a
// hole the phone improvises around.
func toWireJournalRecordWith(r journal.Record, caps func(string) (protocol.SessionCapabilities, bool)) protocol.JournalRecord {
	out := protocol.JournalRecord{
		Cursor:    r.Cursor,
		SessionID: r.SessionID,
		Type:      string(r.Type),
		Group:     r.Group,
		Agent:     r.Agent,
		Name:      r.Name,
		// Both stamps verbatim, by Name's rule: the phone has no other clock for either.
		TS:         r.TS,
		StateSince: r.StateSince,
	}
	if r.Type == journal.TypeCapabilityTransition {
		// A transition is an ordered fact about the state AT THIS CURSOR. Decode only
		// its own validated payload: consulting the current roster here would rewrite
		// a historical false transition as true after recovery (or the reverse).
		var rec protocol.SessionCapabilities
		if json.Unmarshal(r.Payload, &rec) == nil && rec.Validate() == nil {
			out.Capabilities = &rec
		}
	} else if caps != nil {
		if rec, ok := caps(r.SessionID); ok {
			out.Capabilities = &rec
		}
	}
	if r.Type == journal.TypeInteraction || r.Type == journal.TypeStructuredGap {
		// `structured_gap` joins `interaction` as a payload that IS transcript content
		// (Wave R6 review finding B4, ADR-017 T2 rule 2). Its payload carries the proven
		// boundary's instant and reason, and WITHOUT crossing here the phone received a
		// typed record with an empty body: it could see that a tear existed only if it
		// inspected the type, and it had nothing to render. A gap that cannot be rendered
		// is a gap silently bridged -- the transcript shows the items either side of it
		// as contiguous -- which is the one thing ADR-017 forbids.
		out.Item = r.Payload
	}
	return out
}

// JournalReadFrom forwards journal_read to the core and converts the daemon
// journal.Resume to the wire protocol.JournalResume (Events + full-resync + cursor).
func (a *coreAPI) JournalReadFrom(from uint64) (protocol.JournalResume, error) {
	res, err := a.core.JournalReadFrom(from)
	if err != nil {
		return protocol.JournalResume{}, err
	}
	out := protocol.JournalResume{Cursor: res.Cursor, FullResync: res.FullResync}
	for _, r := range res.Roster {
		// The ROSTER is where the capability record rides (ADR-017 T2 rule 3: "the phone
		// renders from that record"). Events do not carry it: the record is authored once
		// per session instance and immutable except in the degrading direction, so
		// stamping it onto every event would spend the append budget restating a fact the
		// roster already carries -- and a degrade reaches the phone as the next roster.
		out.Roster = append(out.Roster, toWireJournalRecordWith(r, a.sessionCaps))
	}
	for _, e := range res.Events {
		out.Events = append(out.Events, toWireJournalRecord(e))
	}
	return out, nil
}

// JournalSubscribe forwards to the daemon journal fan-out, converting each
// journal.Record to the wire protocol.JournalRecord on a dedicated relay goroutine
// (the daemon cannot import protocol, so the conversion happens here). The returned
// cancel stops the relay AND cancels the daemon subscription; it is idempotent and
// race-free. The relay's send onto the wire feed is guarded by the done channel, so
// cancel/shutdown never blocks it and no goroutine leaks.
func (a *coreAPI) JournalSubscribe() (<-chan protocol.JournalRecord, func()) {
	src, cancelSrc := a.core.JournalSubscribe()
	out := make(chan protocol.JournalRecord, eventsBuffer)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			case rec, ok := <-src:
				if !ok {
					return // daemon journal closed the source
				}
				select {
				case out <- toWireJournalRecord(rec):
				case <-done:
					return
				}
			}
		}
	}()
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			close(done)
			cancelSrc()
		})
	}
	return out, cancel
}

// Launch resolves a client launch/resume request into a concrete daemon spec
// (real agent argv composed through the registry adapter, resume validated and
// composed from the source's conversation id) and forwards it to the core.
func (a *coreAPI) Launch(spec daemon.LaunchSpec) (persist.Meta, error) {
	// The launch ENVIRONMENT is resolved before argv, because argv depends on it: the
	// adapter's argv[0] is a bare binary name resolved against the AGENT's own PATH
	// (lookPathIn). A remote/preset launch carries no client env by design (ADR-007 D8
	// forbids a phone-supplied one), so without daemon policy filling it there is no
	// PATH to search and no production provider can ever resolve -- round-4 review
	// BLOCKER 1. The core's LaunchPolicyEnv is that policy -- for a nil client env it
	// reads the environment the daemon SAVED at Open (daemon.env, the file converge
	// spawns replacements from) rather than the live environ, and it is the SAME env
	// the core then hands the shim, so the binary this resolves is the binary the
	// agent runs. THIS is the one point every launch entry passes through; resolving
	// here is what makes the daemon-side seam real (R1 audit H1).
	spec.ClientEnv = a.core.LaunchPolicyEnv(spec.ClientEnv)
	// PRESENCE, not emptiness -- and this layer is the one that must get it right,
	// because it is the ONLY point every launch entry passes through. handleLaunch has
	// the same presence check, but `session_launch` (the signed remote-preset path) does
	// not go through handleLaunch at all: it copies a preset's options wholesale and
	// calls Launch directly. A preset carrying `handoff_from=` would therefore arrive
	// here with the key PRESENT and EMPTY, and an emptiness test would read it as absent
	// and compose an ordinary launch -- a context-free agent in the owner's checkout,
	// which is the one outcome ADR-010 Amendment 4 E7 says must never happen. A key that
	// was never set is still an ordinary launch; a key set to "" is a caller bug and is
	// refused by name.
	// PRESENCE, not emptiness -- and this layer is the one that must get it right,
	// because it is the ONLY point every launch entry passes through. handleLaunch has
	// the same presence check, but `session_launch` (the signed remote-preset path) does
	// not go through handleLaunch at all: it copies a preset's options wholesale and
	// calls Launch directly. A preset carrying `handoff_from=` would therefore arrive
	// here with the key PRESENT and EMPTY, and an emptiness test would read it as absent
	// and compose an ordinary launch -- a context-free agent in the owner's checkout,
	// which is the one outcome ADR-010 Amendment 4 E7 says must never happen. A key that
	// was never set is still an ordinary launch; a key set to "" is a caller bug and is
	// refused by name.
	if handoffFrom, present := spec.Options[protocol.OptionHandoffFrom]; present {
		if handoffFrom == "" {
			return persist.Meta{}, fmt.Errorf("handoff: %s is empty; it must name the source session", protocol.OptionHandoffFrom)
		}
		composed, err := composeHandsOffLaunch(spec, a.endpointID, a.core.Get, a.historyResolver)
		if err != nil {
			return persist.Meta{}, err
		}
		spec = composed
	}
	if conversationID := spec.Options[protocol.OptionResumeConversationID]; conversationID != "" {
		if spec.Options[protocol.OptionResumeFrom] != "" {
			return persist.Meta{}, fmt.Errorf("resume: external conversation id cannot be combined with resume_from")
		}
		if !adapter.IsCanonicalConversationID(conversationID) {
			return persist.Meta{}, fmt.Errorf("resume: external conversation identity is invalid")
		}
		a.externalResumeMu.Lock()
		defer a.externalResumeMu.Unlock()
		for _, existing := range a.core.List() {
			if existing.AgentType == spec.AgentType && existing.ConversationID == conversationID {
				return existing, nil
			}
		}
	}
	if src := spec.Options[protocol.OptionResumeFrom]; src != "" {
		local, source, err := validateResumeSource(src, spec.AgentType, a.endpointID, a.core.Get)
		if err != nil {
			return persist.Meta{}, err
		}
		if err := a.ensureResumeConversationID(local, source); err != nil {
			return persist.Meta{}, err
		}
		// ONE live resume per source (audit M1/codex-5). Kill on an ended
		// session is a silent no-op, so the TUI's r, `swarm relogin` and the
		// auth watcher can each pass their kill step and race Launch on the
		// SAME ended source -- two live sessions driving one provider thread.
		// Under the resume mutex (the external-adoption precedent), a source
		// that already has a RUNNING resumed child yields that child instead of
		// a duplicate; the same scan is what makes the watcher's crash-replay
		// of an owed resume idempotent. The mutex is held through the launch
		// below so two concurrent resumes cannot interleave scan and spawn.
		a.externalResumeMu.Lock()
		defer a.externalResumeMu.Unlock()
		if existing, ok := runningResumeOf(a.core.List(), spec.AgentType, local); ok {
			return existing, nil
		}
	}
	// ADR-024: stamp the account identity of the credentials this agent will load,
	// resolved at the same moment as the argv and the env above -- this is the one
	// point every launch entry passes through, so every session carries the stamp
	// the auth watcher later compares. The home is the one the AGENT will inherit
	// (a per-session HOME reads that home's credentials, not the daemon's -- audit
	// M6). "" (no probe, no readable credentials) gates conservatively: such a
	// session is never auto-recycled.
	if spec.AuthIdentity == "" {
		spec.AuthIdentity = launchAuthIdentity(spec.AgentType, spec.ClientEnv)
	}
	resolved, err := composeLaunchSpec(spec, a.endpointID, a.fakeAgentBin, a.core.Get, lookPathIn)
	if err != nil {
		return persist.Meta{}, err
	}
	m, err := a.core.Launch(resolved)
	if err != nil {
		return m, err
	}
	// ADR-017 T2-a / D-NIL, PATHS 1 AND 2: the owner-tier TUI launch and the R5 remote
	// session_launch both arrive here -- the remote-tier Server drives the same DaemonAPI
	// -- so this is the one place a session's capability record is authored at the moment
	// the session begins to exist. onLaunched is the assembly's hook (Serve wires it to
	// Daemon.authorLaunchedSessionCapabilities); an unwired coreAPI, which is every unit
	// test that builds one directly, simply authors nothing and the session keeps T2-a's
	// honest status card.
	if fn := a.onLaunched; fn != nil {
		fn(m)
	}
	return m, nil
}

// authorLaunchedSessionCapabilities is the CLIENT-FACING launch/resume seam's own
// guarantee (ADR-017 T2-a): the record exists before Launch returns a Meta, so a session
// can never reach a client roster ahead of the record that routes it. A phone that saw the
// session first would render the status card and then silently re-route, which is the
// flicker T1's "one surface, chosen by the daemon" rules out.
//
// The core's OnSessionStart hook (serve.go registerSession) normally authors first, and
// this call is then an idempotent no-op through registerSessionCapabilities' merge. It is
// still stated here because the ORDERING guarantee belongs to this seam: the hook is the
// core's, its firing order relative to Launch's return is the core's to change, and the
// two are one authoring function with two call sites rather than two rules.
func (d *Daemon) authorLaunchedSessionCapabilities(m persist.Meta) {
	inst, ad, version, ok := d.sessionCapabilityInputs(m.ID, m.AgentType, m.ShimPID)
	if !ok {
		return // no bindable instance: T2-a's honest status card
	}
	live, decided := d.backendPlaneDecided(m.ID, ad)
	if !decided {
		// A provider whose structured plane is a side process has not dialled yet, and a
		// record authored now would say structured_chat=false about a session that is
		// about to become a perfectly good chat -- irreversibly, because T2 rule 2 makes
		// that degrade one-way. backend.go authors it from whichever outcome arrives.
		return
	}
	if _, err := d.authorSessionCapabilities(m.ID, inst, m.AgentType, ad, version, adapterRevision, live); err != nil {
		log.Printf("skeleton: author capability record for launched session %s: %v", m.ID, err)
	}
}

// SessionCapabilities makes coreAPI a protocol.SessionCapabilityLookup, which is the seam
// the remote-tier capability gate reads before it opens a terminal tap (ADR-017 T2-c).
//
// IT IS A PURE READ AND FAILS CLOSED. It authors nothing: a gate that authored the record
// it is about to consult would be a gate whose answer depends on the asking. ok=false
// means "no record", which by T2-a is the status card and a refusal of both verbs.
func (a *coreAPI) SessionCapabilities(local string) (protocol.SessionCapabilities, bool) {
	if fn := a.sessionCaps; fn != nil {
		return fn(local)
	}
	return protocol.SessionCapabilities{}, false
}

// coreAPI ALSO satisfies protocol.SessionCapabilityLookup so the assembled remote-tier
// Server can gate terminal_subscribe on the SESSION rather than only on the kill switch
// and the negotiated gateway capability (ADR-017 T2-c).
var _ protocol.SessionCapabilityLookup = (*coreAPI)(nil)

// composeLaunchSpec resolves a launch/resume request's concrete argv (Epic 11
// seam: adapters into launch). It is a pure function of its inputs — getSource
// abstracts the roster lookup — so resume validation and adapter argv composition
// are unit-testable without a live daemon.
//
//   - Resume-as-new-session (R-2): a launch carrying the reserved OptionResumeFrom
//     option resumes a prior session. The source is VALIDATED (belongs to this
//     endpoint, exists, is ended/lost, agent type matches); an invalid source is
//     rejected with a clear error. A resolvable adapter composes the resume argv
//     from the source's conversation id, so the new process CONTINUES the
//     conversation, and the new session's ResumedFrom links back to the source. A
//     resume is rejected (never silently downgraded to a fresh launch) when the
//     agent has no resuming adapter — e.g. the reserved "fake" agent — or no
//     conversation id was captured, so ResumedFrom is stamped only on a real resume.
//   - Fresh launch: a registry-resolvable agent's argv is composed via
//     adapter.Command (the real argv, including any inline hook injection); the
//     reserved dev/test "fake" agent resolves to the swarm-fake-agent binary. The
//     core rejects an unresolved (empty-argv) launch.
//
// An adapter's argv[0] is the bare binary name (e.g. "claude"); the shim execs it
// verbatim, so it is RESOLVED to an absolute path against the agent's own PATH via
// lookPath (a stub in tests, the real PATH search in production). A missing binary
// is a clear launch error.
func composeLaunchSpec(spec daemon.LaunchSpec, endpointID, fakeAgentBin string, getSource func(local string) (persist.Meta, bool), lookPath func(name string, env []string) (string, error)) (daemon.LaunchSpec, error) {
	// GG-6 scope: refuse a registered-but-non-production adapter at the launch
	// boundary so a crafted launch RPC cannot spawn it in a real install. The only
	// such adapter is the fixture-only "reference" (kept registered for the E9.5
	// characterization harness and the launch-picker probe). The gate is lifted ONLY
	// in dev/test mode — signalled, as for the reserved "fake" agent, by fakeAgentBin
	// being configured (SWARM_FAKE_AGENT_BIN, unset in a real install) — under which
	// the reference adapter is the non-billable e2e vehicle for the conversation-
	// capture/resume flows (C1/R2). Riding on that already-dev/test-only signal adds
	// no new production launch surface. ("fake" is not registered here, so it is
	// unaffected; an unknown agent falls through to its existing empty-argv rejection.)
	if fakeAgentBin == "" {
		if _, registered := registry.New(spec.AgentType); registered && !registry.IsProduction(spec.AgentType) {
			return daemon.LaunchSpec{}, fmt.Errorf("launch: agent %q is not a production provider and cannot be launched", spec.AgentType)
		}
	}

	// The adapter's capture=raw rows (ADR-010 §6), resolved HERE because this is the
	// only layer holding both the registry and the launch spec: internal/daemon
	// resolves no adapter, and the `swarm hook` process it injects them into
	// knows its event name but not its adapter. Resolved before the resume/fresh
	// branches below so both carry them — a resumed session's hooks are the same hooks.
	if ad, ok := registry.New(spec.AgentType); ok {
		spec.CaptureEvents = adapter.CaptureEvents(ad)
	}

	if conversationID := spec.Options[protocol.OptionResumeConversationID]; conversationID != "" {
		if spec.Options[protocol.OptionResumeFrom] != "" {
			return daemon.LaunchSpec{}, fmt.Errorf("resume: external conversation id cannot be combined with resume_from")
		}
		if !adapter.IsCanonicalConversationID(conversationID) {
			return daemon.LaunchSpec{}, fmt.Errorf("resume: external conversation identity is invalid")
		}
		ad, ok := registry.New(spec.AgentType)
		if !ok {
			return daemon.LaunchSpec{}, fmt.Errorf("resume: agent %q has no adapter that can resume", spec.AgentType)
		}
		if !adapter.AcceptsConversationID(ad, conversationID) {
			return daemon.LaunchSpec{}, fmt.Errorf("resume: external conversation identity is invalid")
		}
		argv, err := ad.Resume(adapter.ResumeSpec{
			Cwd:            spec.Cwd,
			ConversationID: conversationID,
			Options:        spec.Options,
		})
		if err != nil {
			return daemon.LaunchSpec{}, fmt.Errorf("resume: compose argv: %w", err)
		}
		if len(argv) == 0 {
			return daemon.LaunchSpec{}, fmt.Errorf("resume: agent %q did not compose a resume command", spec.AgentType)
		}
		resolved, err := resolveArgv0(argv, spec.ClientEnv, lookPath)
		if err != nil {
			return daemon.LaunchSpec{}, fmt.Errorf("resume: %w", err)
		}
		spec.Argv = resolved
		spec.ConversationID = conversationID
	} else if src := spec.Options[protocol.OptionResumeFrom]; src != "" {
		local, srcMeta, err := validateResumeSource(src, spec.AgentType, endpointID, getSource)
		if err != nil {
			return daemon.LaunchSpec{}, err
		}
		// The source's persisted launch options ride along beneath the request's
		// own (request keys win), so a resumed session keeps its --model and
		// --sandbox flags: the TUI's resume request carries ONLY resume_from, and
		// without this merge the composed argv silently dropped them (observed
		// live 2026-09-01: a resumed codex fell from its Workspace sandbox to the
		// thread default). Reserved orchestration keys never chain -- a source
		// that was itself resumed persisted its own resume_from, and inheriting
		// it would point the new session at the wrong generation.
		spec.Options = mergeResumeOptions(spec.Options, srcMeta.LaunchOptions)
		ad, ok := registry.New(spec.AgentType)
		if !ok {
			return daemon.LaunchSpec{}, fmt.Errorf("resume: agent %q has no adapter that can resume", spec.AgentType)
		}
		if srcMeta.ConversationID != "" && !adapter.AcceptsConversationID(ad, srcMeta.ConversationID) {
			return daemon.LaunchSpec{}, fmt.Errorf("resume: saved conversation identity is invalid")
		}
		argv, rerr := ad.Resume(adapter.ResumeSpec{
			Cwd:            spec.Cwd,
			ConversationID: srcMeta.ConversationID,
			Name:           spec.Name,
			Options:        spec.Options,
		})
		if rerr != nil {
			return daemon.LaunchSpec{}, fmt.Errorf("resume: compose argv: %w", rerr)
		}
		// An empty resume argv means the adapter had no conversation id to replay
		// (never captured). REFUSE rather than fall through to a fresh launch falsely
		// stamped ResumedFrom (B1): a resume must resume, or fail with a clear reason.
		if len(argv) == 0 {
			return daemon.LaunchSpec{}, fmt.Errorf("resume: cannot resume %q: no captured conversation id", local)
		}
		resolved, lerr := resolveArgv0(argv, spec.ClientEnv, lookPath)
		if lerr != nil {
			return daemon.LaunchSpec{}, fmt.Errorf("resume: %w", lerr)
		}
		spec.Argv = resolved     // the resume argv carries the source's conversation id
		spec.ResumedFrom = local // stamp ONLY now that a real resume argv is composed
		// The resumed process CONTINUES the source's thread, so the new session's
		// conversation identity is known before launch -- seed it (the external-
		// resume precedent) instead of waiting for transcript recapture, during
		// which window a second re-login would find no id to resume (audit L4).
		if spec.ConversationID == "" {
			spec.ConversationID = srcMeta.ConversationID
		}
	}

	if len(spec.Argv) == 0 {
		switch spec.AgentType {
		case "fake":
			if fakeAgentBin != "" {
				spec.Argv = []string{fakeAgentBin, spec.Options["script"]}
			}
		default:
			if ad, ok := registry.New(spec.AgentType); ok {
				argv, err := ad.Command(adapter.LaunchSpec{
					Cwd:           spec.Cwd,
					Name:          spec.Name,
					Options:       spec.Options,
					InitialPrompt: spec.InitialPrompt,
				})
				if err != nil {
					return daemon.LaunchSpec{}, fmt.Errorf("launch: compose %s argv: %w", spec.AgentType, err)
				}
				resolved, lerr := resolveArgv0(argv, spec.ClientEnv, lookPath)
				if lerr != nil {
					return daemon.LaunchSpec{}, fmt.Errorf("launch: resolve %s binary: %w", spec.AgentType, lerr)
				}
				spec.Argv = resolved
			}
		}
	}
	return spec, nil
}

// resolveArgv0 rewrites argv[0] (the bare agent binary name) to an absolute path
// via lookPath, leaving the rest of argv untouched. It copies argv so the caller's
// slice is not mutated.
func resolveArgv0(argv, env []string, lookPath func(name string, env []string) (string, error)) ([]string, error) {
	if len(argv) == 0 {
		return argv, nil
	}
	resolved, err := lookPath(argv[0], env)
	if err != nil {
		return nil, err
	}
	out := append([]string(nil), argv...)
	out[0] = resolved
	return out, nil
}

// lookPathIn resolves a bare program name to an absolute path by searching the PATH
// carried in env — the AGENT's own PATH, not the daemon's — so the resolved binary
// is what the agent would itself run. A name that already contains a path separator
// is returned as-is if it is an executable file. It mirrors exec.LookPath but binds
// to a supplied PATH rather than the daemon process environment.
func lookPathIn(name string, env []string) (string, error) {
	if strings.ContainsRune(name, os.PathSeparator) {
		if isExecutableFile(name) {
			return name, nil
		}
		return "", fmt.Errorf("agent binary %q is not an executable file", name)
	}
	var pathEnv string
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			pathEnv = v
		}
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			continue
		}
		cand := filepath.Join(dir, name)
		if isExecutableFile(cand) {
			return cand, nil
		}
	}
	return "", fmt.Errorf("agent binary %q not found on the agent PATH", name)
}

// isExecutableFile reports whether path is a regular, executable file.
func isExecutableFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0
}

// composeHandsOffLaunch composes a hands-off handoff (ADR-010 Amendment 4) entirely
// inside the daemon: it resolves the source session, establishes a trustworthy
// conversation id, opens the transcript under the anchor, renders the pointers-only
// prompt from the embedded template, and stamps the lineage. It is a pure function of
// its inputs -- getSource abstracts the roster and resolver the provider history -- so
// every refusal below is unit-testable without a live daemon.
//
// EVERY FAILURE IS A NAMED REFUSAL AND NONE MAY FALL THROUGH. Amendment 4 E7 names a
// context-free agent loose in the owner's checkout as the worst outcome available here,
// worse than no handoff at all, because the owner would believe the work was carried
// over. So this function either returns a spec carrying the full prompt or it returns
// an error; there is no degraded middle.
func composeHandsOffLaunch(spec daemon.LaunchSpec, endpointID string, getSource func(local string) (persist.Meta, bool), resolver resumeHistoryResolver) (daemon.LaunchSpec, error) {
	// The protocol layer already refuses these pairings, and they are refused again here
	// because this is where the damage would be: a spec that composed a hands-off prompt
	// and then took composeLaunchSpec's resume branch would have that prompt silently
	// dropped in favour of a resume argv, which is precisely the bare launch E7 forbids.
	// Cheap guard, catastrophic omission.
	if spec.Options[protocol.OptionResumeFrom] != "" {
		return daemon.LaunchSpec{}, fmt.Errorf("handoff: %s cannot be combined with %s", protocol.OptionHandoffFrom, protocol.OptionResumeFrom)
	}
	if spec.Options[protocol.OptionResumeConversationID] != "" {
		return daemon.LaunchSpec{}, fmt.Errorf("handoff: %s cannot be combined with %s", protocol.OptionHandoffFrom, protocol.OptionResumeConversationID)
	}

	// STEP 1. The source, WITHOUT resume's "must not be running" clause. That clause is
	// right for a resume -- two processes replaying one conversation is a real hazard --
	// and inverted here: a rate-limited session is byte-identical on the wire to a healthy
	// idle one (E2), so a RUNNING source is the primary case this feature exists for.
	// Nothing is written to it and its lifecycle is untouched (E3); only its transcript
	// is read.
	local, source, err := resolveSourceSession("handoff", spec.Options[protocol.OptionHandoffFrom], endpointID, getSource)
	if err != nil {
		return daemon.LaunchSpec{}, err
	}

	// STEP 2. E7's scope gate, and it is the ADAPTER's knowledge that draws the line:
	// an adapter without a characterized transcript layout is not asserted into the
	// interface, so codex, agy and opencode are refused BY NAME rather than handed a
	// path this daemon cannot compute.
	ad, _ := registry.New(source.AgentType)
	if _, ok := adapter.AsTranscriptLayout(ad); !ok {
		return daemon.LaunchSpec{}, fmt.Errorf("handoff: source agent %q has no characterized transcript layout; hands-off supports claude sources only in this sweep", source.AgentType)
	}
	if resolver == nil {
		return daemon.LaunchSpec{}, fmt.Errorf("handoff: no provider history resolver is configured")
	}

	// STEP 3. A trustworthy conversation id.
	convID, err := handsOffConversationID(local, source, resolver)
	if err != nil {
		return daemon.LaunchSpec{}, err
	}

	// STEP 4/5. HOME is the DAEMON's, not the source's. Meta.Env would have been the
	// obvious source and is the wrong one: on the local tier it is populated from the
	// CONNECTING CLIENT's request, and persist.FilterEnv allowlists env NAMES without
	// validating VALUES, so "HOME=/tmp/attacker" passes through verbatim. Anchoring the
	// walk there would relocate the trust anchor to a root the client chose and void the
	// resolver's documented trusted-daemon-user-home premise. The resolver is therefore
	// used exactly as serve.go constructed it, once per daemon with an immutable home. A
	// source whose transcript is not under that home simply does not resolve and is
	// refused by name. The divergent-HOME case is a KNOWN, PRE-EXISTING limitation that
	// affects resume recovery today in the same way, and is deliberately not fixed here.
	transcript, outcome := resolver.LocateTranscript(source, convID)
	if outcome != resumeHistoryFound {
		return daemon.LaunchSpec{}, handsOffTranscriptError(source.AgentType, convID, outcome)
	}

	// STEP 5b. THE SUCCESSOR STARTS WHERE THE SOURCE WAS WORKING, not where the client
	// guessed. The TUI holds only a protocol.SessionView, which carries no AgentCwd -- that
	// frozen wire type was deliberately not widened -- so the client can send nothing but
	// SessionView.Cwd, the LAUNCH cwd. For a worktree-isolated source that is the REPO
	// ROOT while the agent ran under <repo>/.swarm/worktrees/<slug>, and a git worktree is a
	// SEPARATE CHECKOUT: different files, possibly a different branch. Inheriting the
	// client's value would start the successor in one tree while the prompt named another
	// and the transcript described the second -- "continue this work" beginning in the
	// wrong place. The daemon holds the value the client is missing, which is the reason
	// composition lives here at all, so it corrects rather than inherits. The hands-off
	// form offers no cwd field, so this overrides no human choice, and it makes worktree
	// and ordinary sources behave alike.
	//
	// The stat is owed HERE because the protocol layer stats the CLIENT's cwd and never
	// this override: a worktree torn down since the source ended would otherwise surface
	// as an obscure spawn failure instead of a named refusal (E7).
	providerCwd := source.ProviderCwd()
	if fi, err := os.Stat(providerCwd); err != nil || !fi.IsDir() {
		return daemon.LaunchSpec{}, fmt.Errorf("handoff: the source's working directory %q is no longer a directory, so the successor has nowhere to continue", providerCwd)
	}
	spec.Cwd = providerCwd

	// STEP 6. The prompt, from the daemon's embedded template, so no client can put
	// words into the successor's opening instruction. Any client-supplied InitialPrompt
	// is REPLACED rather than merged: the hands-off form offers no prompt field, and a
	// launch that arrived with one is not a hands-off handoff with an extra note, it is a
	// caller trying to author half of the instruction.
	prompt, err := renderHandsOffPrompt(handsOffPromptData{
		ConversationID:  convID,
		TranscriptPath:  transcript,
		AgentCwd:        providerCwd,
		SourceAgent:     source.AgentType,
		SourceSessionID: local,
	})
	if err != nil {
		return daemon.LaunchSpec{}, fmt.Errorf("handoff: %w", err)
	}
	spec.InitialPrompt = prompt

	// STEP 7. Lineage. SpawnedFrom carries the LOCAL id, never the namespaced one:
	// schema.go documents spawned_from as the parent's local id and the TUI's lineage
	// matcher compares it against the local half of a roster id, so a namespaced value
	// silently breaks the parent badge and the child count.
	spec.SpawnedFrom = local
	spec.SpawnIntent = protocol.SpawnIntentHandoff
	// EMPTY, not "none". Amendment 3 gave "none" the meaning "a supervisor existed and
	// declined to watch"; in a hands-off handoff no supervisor exists by construction,
	// and collapsing the two would eventually put an orphaned-supervisor marker on a
	// child that never had a supervisor to orphan (E3).
	spec.Supervision = ""
	// spec.Options is carried through untouched, so the form's chosen model survives into
	// composeLaunchSpec and reaches the target adapter's ordinary Command() rather than
	// being silently discarded. The launch itself is that ordinary path: hands-off adds a
	// prompt and a lineage, not a second way to spawn a process.
	return spec, nil
}

// handsOffConversationID establishes the id the successor will be pointed at: the
// source's own if it is canonical, otherwise one re-derived from provider history.
//
// A NON-CANONICAL STORED ID IS RE-DERIVED, NOT REFUSED, which is where this diverges
// from the resume path's validateStoredResumeConversationID. A resume must replay THAT
// conversation, so a corrupt stored id is a hard error there. Here the id is a pointer
// swarm is free to compute again -- and it must be, because two of the owner's seven
// real sessions had latched the literal "./cmd/swarm/" off the rendered grid, and
// refusing them outright would refuse the exact sessions this feature exists for. The
// junk value is never joined into a path, never passed to the locator and never
// reaches the prompt; it is simply discarded.
//
// THE STALE-FORK GUARD IS NOT IMPLEMENTED, DELIBERATELY. Claude mints a new sessionId
// on /clear and on in-PTY /resume, so a latched id can name a real but ABANDONED
// conversation with every check here passing. Preferring a newer id was investigated
// and refused on measured evidence:
//
//   - The recovery scan cannot find the newer id. Its match predicate is
//     withinResumeWindow -- the transcript's first cwd-bearing record must fall within
//     -2s..+30s of the swarm session's CreatedAt -- which structurally excludes any
//     conversation minted later. The fork we would be looking for is exactly the one
//     the existing extractor is built to skip.
//   - Relaxing that to "the newest transcript in the project directory" is unsafe. The
//     directory is keyed by CWD, not by session: on the owner's machine one project
//     directory holds 13 transcripts for a single checkout, and this tool exists to run
//     many sessions in one checkout at once. Preferring the newest would hand the
//     successor a CONCURRENT STRANGER's conversation as often as a fork of its own.
//   - There is no evidence in the file that would tell the two apart. Codex records
//     parent_thread_id; claude records nothing equivalent. Measured across all 13 real
//     transcripts: 13 files, 13 distinct sessionIds, every file naming only its own --
//     no in-file link to a conversation it forked from or into.
//
// So the failure mode left standing is "the successor reads an abandoned but genuine
// conversation of the correct lineage", where the prompt's own ordering rule (the
// repository records fact, the conversation records intent, the repository wins) bounds
// the damage. The failure mode a guess would have introduced is "the successor reads an
// unrelated session's conversation", which is both a wrong-context handoff and a
// cross-session content disclosure. Guessing here is strictly worse than not guessing.
func handsOffConversationID(local string, source persist.Meta, resolver resumeHistoryResolver) (string, error) {
	if adapter.IsCanonicalConversationID(source.ConversationID) {
		return source.ConversationID, nil
	}
	// Unlike the resume path's recovery this neither refuses a running source nor
	// persists what it finds: capture is hook-driven and a wedged agent fires no hooks,
	// so a running source with no captured id is the COMMON case here, and the id is
	// wanted for this one launch rather than as a correction to the source's meta.
	result := resolver.Resolve(source)
	provider := source.AgentType
	switch result.Outcome {
	case resumeHistoryFound:
		if !adapter.IsCanonicalConversationID(result.ConversationID) {
			return "", fmt.Errorf("handoff: %s conversation history returned an unsafe identity", provider)
		}
		return result.ConversationID, nil
	case resumeHistoryUnsupported, resumeHistoryNoMatch:
		return "", fmt.Errorf("handoff: source %q has no usable %s conversation id: none was captured and no matching conversation history was found", local, provider)
	case resumeHistoryAmbiguous:
		return "", fmt.Errorf("handoff: multiple matching %s conversation histories were found for source %q; refusing to guess", provider, local)
	case resumeHistoryUnsafe:
		return "", fmt.Errorf("handoff: %s conversation history is unsafe to inspect", provider)
	case resumeHistoryUnreadable:
		return "", fmt.Errorf("handoff: could not read %s conversation history safely", provider)
	default:
		return "", fmt.Errorf("handoff: %s conversation history returned an unsafe result", provider)
	}
}

// handsOffTranscriptError names why the transcript could not be opened. The resume
// path's equivalent switch is deliberately not shared: these two callers differ in
// policy (that one refuses a running source, this one is built for one) and in effect
// (that one persists what it recovers, this one does not), so a common helper would
// have to be parameterized on both and would say less than either.
func handsOffTranscriptError(provider, convID string, outcome resumeHistoryOutcome) error {
	switch outcome {
	case resumeHistoryNoMatch:
		return fmt.Errorf("handoff: the %s transcript for conversation %s was not found", provider, convID)
	case resumeHistoryUnsupported:
		return fmt.Errorf("handoff: %s has no characterized transcript layout", provider)
	case resumeHistoryUnsafe:
		return fmt.Errorf("handoff: the %s transcript for conversation %s is unsafe to open", provider, convID)
	default:
		return fmt.Errorf("handoff: the %s transcript for conversation %s could not be opened", provider, convID)
	}
}

// resolveSourceSession is the half that a resume and a hands-off handoff genuinely
// share: a source id must be a namespaced id of THIS endpoint and must name a session
// that exists. It is parameterized on the flow's name only so each refusal reads as the
// flow the caller asked for. What it deliberately does NOT include is the eligibility
// each flow decides for itself -- resume adds "not running" and "agent type matches",
// hands-off wants neither (see composeHandsOffLaunch).
func resolveSourceSession(kind, src, endpointID string, getSource func(local string) (persist.Meta, bool)) (string, persist.Meta, error) {
	ep, local, ok := protocol.ParseID(src)
	if !ok {
		return "", persist.Meta{}, fmt.Errorf("%s: source id %q is not a valid namespaced session id", kind, src)
	}
	if endpointID != "" && ep != endpointID {
		return "", persist.Meta{}, fmt.Errorf("%s: source %q belongs to another daemon endpoint", kind, src)
	}
	m, ok := getSource(local)
	if !ok {
		return "", persist.Meta{}, fmt.Errorf("%s: source session %q not found", kind, local)
	}
	return local, m, nil
}

// validateResumeSource resolves and validates a resume source id: it must be a
// namespaced id of THIS endpoint, name a session that exists, that has ENDED (not
// running), and whose agent type matches the requested one. It returns the source
// local id and meta, or a clear error naming the reason the resume was rejected.
// mergeResumeOptions folds a resume source's persisted launch options beneath
// the request's own (request keys win) into a FRESH map -- the caller's map is
// never mutated. Reserved orchestration keys are never inherited: chaining a
// source's own resume_from/handoff_from would re-orchestrate a past generation,
// "script" is the dev-only fake-agent input, and OptionWorktree would make the
// resume mint a BRAND-NEW worktree from HEAD while the conversation believes
// its files are where it left them -- and, worse, arm preDeleteWorktree so a
// later delete of either row `git worktree remove --force`s a checkout with
// uncommitted agent work (audit C1). A resume continues a conversation; it
// never re-isolates.
func mergeResumeOptions(req, src map[string]string) map[string]string {
	merged := make(map[string]string, len(src)+len(req))
	for k, v := range src {
		switch k {
		case protocol.OptionResumeFrom, protocol.OptionHandoffFrom, protocol.OptionResumeConversationID,
			protocol.OptionWorktree, "script":
			continue
		}
		merged[k] = v
	}
	for k, v := range req {
		merged[k] = v
	}
	return merged
}

// runningResumeOf finds the RUNNING session that already resumed local, if one
// exists -- the pure half of the one-live-resume-per-source rule above.
func runningResumeOf(roster []persist.Meta, agentType, local string) (persist.Meta, bool) {
	for _, m := range roster {
		if m.AgentType == agentType && m.ResumedFrom == local && m.Status.Process == status.ProcessRunning {
			return m, true
		}
	}
	return persist.Meta{}, false
}

func validateResumeSource(src, agentType, endpointID string, getSource func(local string) (persist.Meta, bool)) (string, persist.Meta, error) {
	local, m, err := resolveSourceSession("resume", src, endpointID, getSource)
	if err != nil {
		return "", persist.Meta{}, err
	}
	if m.Status.Process == status.ProcessRunning {
		return "", persist.Meta{}, fmt.Errorf("resume: source session %q is still running; resume an ended or lost session", local)
	}
	if m.AgentType != agentType {
		return "", persist.Meta{}, fmt.Errorf("resume: source agent %q does not match requested agent %q", m.AgentType, agentType)
	}
	return local, m, nil
}

// Attach opens a SessionStream for id, multiplexed through the shared per-session
// tap (A7 F1). The first attach opens the one upstream shim connection; concurrent
// attaches (the owner controller and the future remote peek) SHARE it. The owner
// tier attaches readWrite so its Input/Resize drive the session exactly as before.
func (a *coreAPI) Attach(id string) (protocol.SessionStream, error) {
	return a.tap.subscribe(id, readWrite)
}

// SampleSnapshot fetches one session's current grid snapshot for the tap
// (serve.go sampleGrid). Against a shim advertising SnapshotOnly it uses the
// non-subscribing snapshot_req — which CANNOT supersede a controller's stream,
// closing the C3 tap-steal TOCTOU race by construction. Against an old shim
// (capability absent) it falls back to the pre-C3 attach-based sample, whose
// exposure is limited to the tapOnce controlled-skip exactly as before (G-D:
// old-shim degradation no worse than today).
func (a *coreAPI) SampleSnapshot(id string) ([]byte, error) {
	conn, caps, err := a.core.DialSession(id)
	if err != nil {
		return nil, err
	}
	if caps.SnapshotOnly {
		defer func() { _ = conn.Close() }()
		return protocol.SnapshotOnly(conn, caps)
	}
	stream, err := protocol.NewShimStream(conn, caps) // owns conn from here
	if err != nil {
		return nil, err
	}
	snap := stream.Snapshot()
	_ = stream.Close()
	return snap, nil
}

// TerminalTap makes coreAPI a protocol.TerminalTapper (A7 F2): it opens a READ-ONLY tap on
// the shared per-session multiplexer, so a remote peek observes the session's output over the
// SAME single upstream the owner controller uses, WITHOUT injecting input — readOnly makes the
// returned tapSub's Input/Resize no-ops, so the peek can never drive the session.
func (a *coreAPI) TerminalTap(local string) (protocol.SessionStream, error) {
	return a.tap.subscribe(local, readOnly)
}

// coreAPI ALSO satisfies protocol.TerminalTapper so the assembled remote-tier Server can serve
// terminal_subscribe (A7 F2).
var _ protocol.TerminalTapper = (*coreAPI)(nil)

// emitStatus routes an engine-derived status change through both halves of Epic
// 10's status wiring (the Epic 11 carry-forward, now wired):
//
//   - PERSIST (G6): SetStatus writes the change back through the daemon's sole meta
//     writer, so it is durable and a reconnecting client's List reflects it.
//   - FAN OUT (Epic 6): the updated meta is pushed to the roster event channel the
//     protocol Server fans out, so Subscribe delivers it immediately (L1) rather
//     than waiting for the next roster poll.
//
// SetStatus is the choke point that also guards the process dimension (the daemon
// stays its sole authority), so an unknown/ended session persists nothing and is
// dropped here.
func (a *coreAPI) emitStatus(id string, s status.Status) {
	if err := a.core.SetStatus(id, s); err != nil {
		return // unknown/ended session: nothing to persist or fan out
	}
	// FAN OUT happens via the poller, which is the SOLE snapshot producer: a
	// direct Get-then-send here could capture meta, lose the CPU to a concurrent
	// Rename, and queue its stale snapshot AFTER the poller queued the newer one
	// - the client's row would revert and the seen-map would never repair it
	// (the codex v0.5 audit interleaving). The nudge keeps L1 immediacy: the
	// poller samples now, not at the next tick, and emits the CURRENT meta under
	// its own seen discipline.
	a.pokeWatch()
}

// pokeWatch wakes the roster poller for an immediate sample. Non-blocking and
// coalescing: a nudge while one is already pending is a no-op (the pending sample
// will observe both changes).
func (a *coreAPI) pokeWatch() {
	select {
	case a.nudge <- struct{}{}:
	default:
	}
}

// close stops the roster poller and waits for it to exit, so the assembly leaves
// no goroutine behind.
func (a *coreAPI) close() {
	a.stopOnce.Do(func() { close(a.stop) })
	a.wg.Wait()
}

// controlledFunc reports whether a session (by LOCAL id) currently has a REMOTE
// controller lease. Named so the setter can hand it to an atomic.Pointer.
type controlledFunc func(local string) bool

// SetRemoteControlledFunc registers the roster poller's remote-control source. The
// assembly wires it to the remote Server's IsControlled, beside the kill-switch
// observer. nil clears it; unset, no session is ever reported controlled.
func (a *coreAPI) SetRemoteControlledFunc(fn func(local string) bool) {
	if fn == nil {
		a.controlledFn.Store(nil)
		return
	}
	f := controlledFunc(fn)
	a.controlledFn.Store(&f)
}

// isControlled answers the registered source, false when none is registered.
func (a *coreAPI) isControlled(local string) bool {
	if p := a.controlledFn.Load(); p != nil {
		return (*p)(local)
	}
	return false
}

// SetSupervisionPendingFunc registers the roster poller's supervision-pending source
// (ADR-010 Amendment 3 C5). The assembly wires it to its supervisor's pending query,
// beside the protocol Server's own registration. nil clears it; unset, no session is
// ever reported pending.
func (a *coreAPI) SetSupervisionPendingFunc(fn func(local string) bool) {
	if fn == nil {
		a.supervisionPendingFn.Store(nil)
		return
	}
	f := controlledFunc(fn)
	a.supervisionPendingFn.Store(&f)
}

// isSupervisionPending answers the registered source, false when none is registered.
func (a *coreAPI) isSupervisionPending(local string) bool {
	if p := a.supervisionPendingFn.Load(); p != nil {
		return (*p)(local)
	}
	return false
}

// rosterSnap is the per-session change key the poller diffs on: the status the
// board groups by, the display label (so a rename, which changes only the name,
// fans out live just like a status change), whether a paired device holds the
// session's controller lease, and whether a supervision event awaits the session's
// source. All four fields are comparable, so the whole key compares with ==.
//
// controlled and supervisionPending are the key members that are NOT derived from
// persist.Meta: a remote take_control or a supervisor's pending event changes nothing
// the core persists, so without them in the key a flip alone would fan out no event
// at all and the roster marker would appear only when some unrelated status change
// happened to follow (ADR-008's 1s roster bound would silently fail for that field).
type rosterSnap struct {
	status             status.Status
	name               string
	tag                string
	controlled         bool
	supervisionPending bool
	// backendPlanError joins the key because launch persists it in phase TWO,
	// after the phase-one reservation a poll may already have emitted: without
	// it, an open board would show the degraded session healthy until some
	// unrelated change happened to follow (R1 audit: codex finding 2).
	backendPlanError string
	contextGuard     protocol.ContextGuardView
	hasContextGuard  bool
}

// watch samples the roster and emits a meta whenever a session's status, display
// label, tag, remote-control OR supervision-pending state changes (the core exposes no push
// source, so changes are observed by polling). It mirrors protocol.FromDaemon's
// watcher: dedup, retry a momentarily-full queue on the next poll (never drop a
// change), and prune vanished sessions so the seen map stays bounded.
func (a *coreAPI) watch() {
	defer a.wg.Done()
	seen := map[string]rosterSnap{}
	// sample diffs the roster against seen and queues every change; it reports
	// false when the assembly is stopping. It is the ONLY writer to a.events, so
	// no stale snapshot from a second producer can ever trail a newer one.
	sample := func() bool {
		present := map[string]struct{}{}
		for _, m := range a.core.List() {
			present[m.ID] = struct{}{}
			guard, hasGuard := a.ContextGuardView(m.ID)
			cur := rosterSnap{status: m.Status, name: m.Name, tag: m.Tag, controlled: a.isControlled(m.ID), supervisionPending: a.isSupervisionPending(m.ID), backendPlanError: m.BackendPlanError, contextGuard: guard, hasContextGuard: hasGuard}
			if prev, ok := seen[m.ID]; ok && prev == cur {
				continue
			}
			select {
			case a.events <- m:
				seen[m.ID] = cur // mark seen ONLY once the change is queued
			case <-a.stop:
				return false
			default:
				// Queue momentarily full: leave seen unadvanced so this change is
				// retried on the next poll rather than lost.
			}
		}
		for id := range seen {
			if _, ok := present[id]; !ok {
				delete(seen, id)
			}
		}
		return true
	}
	t := time.NewTicker(eventPoll)
	defer t.Stop()
	for {
		select {
		case <-a.stop:
			return
		case <-t.C:
		case <-a.nudge: // an emitStatus/Rename wants immediate fan-out (L1)
		}
		if !sample() {
			return
		}
	}
}
