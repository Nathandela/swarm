// Package remotegw is the supervised gateway sidecar (R-GW): a standalone process
// that dials the daemon's dedicated remote-tier socket (R-GW.8) and, later, the
// untrusted relay, bridging the daemon's journal/events and the phone's commands.
// It is never spawned by the daemon and shares no address space with it (ADR-007 D5);
// a crash leaves the daemon and its sessions untouched (S1) and it resumes from its
// last durable journal cursor.
//
// This slice implements the DAEMON-FACING JOURNAL READ PATH (R-GW.3/.5): the atomic
// roster+cursor snapshot (journal_read) followed by the live event stream
// (journal_subscribe), delivered to a sink and cursor-tracked so a reconnect resumes
// without loss. Relay forwarding and phone-command forwarding are later slices.
package remotegw

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/wire"
)

// JournalSink receives the journal the gateway bridges toward the phone. Snapshot is called
// once per (re)connection with an atomic roster and the PRIOR delivered cursor. Event then
// carries every backlog record after that cursor, followed by live records, in cursor order.
// Keeping the snapshot boundary behind the backlog prevents the phone from stale-dropping
// those records. Implementations must not block the gateway's read loop (R-GW.4/.5:
// bounded/coalescing on the relay side).
type JournalSink interface {
	Snapshot(roster []protocol.JournalRecord, cursor uint64) error
	Event(rec protocol.JournalRecord) error
}

// machineNamer is a sink that stamps this machine's endpoint id on the frames carrying it
// (today only the reconcile record). It is OPTIONAL: a sink that publishes no attributable
// frame does not implement it, and RunJournal skips the stamp.
type machineNamer interface {
	SetMachine(machine string)
}

// TerminalSink receives the server-rendered terminal snapshots the gateway bridges toward
// the phone (A7 slice D). RelaySink implements it alongside JournalSink; RunTerminal
// requires the gateway's sink to accept snapshots.
type TerminalSink interface {
	Terminal(view protocol.TerminalViewV1) error
}

// terminalViewOf reads ONE frame's terminal payload as a versioned view, namespacing the
// session id the way every other id the phone sees is namespaced.
//
// IT PREFERS THE VERSIONED BODY AND FALLS BACK TO THE LEGACY ONE, because both are on the
// wire (ADR-017 T4 keeps `terminal` unchanged) and a machine that predates the closing round
// sends only the legacy half. The fallback yields view_epoch 0, which is the honest answer --
// "this machine does not version its views" -- and never a fabricated epoch, because an
// invented one would let the phone believe it can detect a replacement it cannot.
func terminalViewOf(endpointID string, ctrl protocol.Control) protocol.TerminalViewV1 {
	if v := ctrl.TerminalView; v != nil {
		out := *v
		out.Session = namespaceSessionID(endpointID, v.Session)
		return out
	}
	t := ctrl.Terminal
	return protocol.TerminalViewV1{
		Session: namespaceSessionID(endpointID, t.Session),
		Lines:   t.Lines,
		Cols:    t.Cols,
		Rows:    t.Rows,
	}
}

// ReseedSink receives PB-SYNC-2's journal repair frame. RelaySink implements it alongside
// JournalSink, and both wrappers in the chain (CoalescingSink, PushNotifier) forward it --
// a wrapper that swallowed it would leave every resync answered and delivering nothing.
type ReseedSink interface {
	Reseed(rs protocol.JournalReseed) error
}

// RecoverySnapshotSink is the explicit mailbox-discard recovery publisher. It preserves
// Snapshot's reconcile-then-reseed ordering while echoing the phone's durable recovery token
// on the bounded roster frame. Keeping it separate leaves ordinary reconnect snapshots and
// their long-standing JournalSink interface byte- and source-compatible.
type RecoverySnapshotSink interface {
	RecoverySnapshot(roster []protocol.JournalRecord, cursor uint64, recoveryToken string) error
}

// errNoReseedSink refuses a resync whose sink cannot publish the repair, rather than
// reporting a repair that never left the machine. The phone's stale flag clears only on the
// frame's arrival, so a silent nil here would leave it stale with nothing to say why.
var errNoReseedSink = errors.New("remotegw: this sink cannot publish a journal reseed")

var errInvalidDiscardRecovery = errors.New("remotegw: invalid discard recovery command")

func validDiscardRecoveryToken(token string) bool {
	if len(token) != 32 {
		return false
	}
	for _, c := range token {
		if !('0' <= c && c <= '9') && !('a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// Resync serves the phone's sealed journal read. With rosterOnly false it is PB-SYNC-2's
// journal repair: one atomic roster+events reseed at the daemon's final boundary. With
// rosterOnly true it is the inbox refresh: one authoritative roster-only reseed. A healthy
// refresh stays at the phone's PRIOR cursor so separately forwarded backlog remains
// admissible. Only a sealed command reporting a completed explicit mailbox discard advances
// the bounded reseed to the daemon snapshot's final cursor; the discarded events themselves
// are never copied into the replacement payload.
//
// It opens its OWN daemon connection rather than borrowing RunJournal's. RunJournal's conn
// is inside a blocking read loop that owns its control stream, so interleaving a second
// journal_read on it would race the subscription's events for the reply; and a resync must
// work while the journal loop is between reconnects, which is exactly when the phone is
// most likely to have a hole.
//
// from is the phone's durable cursor. It maps directly onto a full repair's read boundary.
// A healthy roster-only refresh also keeps it as the reseed cursor; completed-discard
// recovery instead uses the final boundary returned by that same atomic daemon read.
func (g *Gateway) Resync(ctx context.Context, from uint64, rosterOnly, discardedBacklog bool, recoveryToken string) error {
	if discardedBacklog != (recoveryToken != "") {
		return fmt.Errorf("%w: discarded_backlog and discard_recovery_token must be present together", errInvalidDiscardRecovery)
	}
	if discardedBacklog && (!rosterOnly || !validDiscardRecoveryToken(recoveryToken)) {
		return fmt.Errorf("%w: recovery requires roster_only and a 32-character lowercase hexadecimal token", errInvalidDiscardRecovery)
	}
	sink, ok := g.sink.(ReseedSink)
	if !ok {
		return errNoReseedSink
	}
	dc, err := dialDaemon(g.socketPath, protocol.CapRemoteGateway, protocol.CapJournal)
	if err != nil {
		return err
	}
	defer func() { _ = dc.Close() }()
	// Fence the atomic read through its replacement frame against RunJournal delivery.
	// Without this span a live record newer than the read boundary can reach the phone
	// first, then be overwritten by this older roster and never be delivered again.
	g.deliveryMu.Lock()
	defer g.deliveryMu.Unlock()
	res, err := dc.readJournal(from)
	if err != nil {
		return err
	}
	roster := append([]protocol.JournalRecord{}, namespaceRoster(dc.endpointID, res.Roster)...)
	if rosterOnly {
		snapshotCursor := from
		if discardedBacklog && recoveryToken != "" {
			snapshotCursor = res.Cursor
		}
		// A destructive phone-side mailbox recovery can leave its authenticated receive
		// high-water behind every frame it explicitly discarded. Snapshot is the existing
		// two-frame recovery anchor: a fresh reconcile whose JournalCeiling is its own seq,
		// immediately followed by the roster-only reseed. The phone adopts the reconcile
		// synchronously, making the reseed contiguous and therefore durable rather than a
		// gapped live-cache update lost on process death.
		//
		// Stamp the identity learned on THIS daemon connection before publishing. Command-IN
		// can serve this request before RunJournal has reached its own SetMachine call, and an
		// empty/old machine on the reconcile is refused by the phone.
		if named, ok := g.sink.(machineNamer); ok {
			named.SetMachine(dc.endpointID)
		}
		// Snapshot is a reconcile followed by the roster reseed. A definitive/transient
		// refusal of the second append burns that frame's shared seq, and the command bridge
		// cannot safely count on redispatching the same command within this process. Retry the
		// complete pair once: the replacement reconcile raises the phone to its own new ceiling
		// and the following reseed is contiguous. Repeating authoritative current state is
		// idempotent; returning after two failures keeps the error honest.
		publish := func() error { return g.sink.Snapshot(roster, snapshotCursor) }
		if discardedBacklog && recoveryToken != "" {
			recoverySink, ok := g.sink.(RecoverySnapshotSink)
			if !ok {
				return errors.New("remotegw: this sink cannot publish a tokened recovery snapshot")
			}
			publish = func() error { return recoverySink.RecoverySnapshot(roster, snapshotCursor, recoveryToken) }
		}
		var snapshotErr error
		for attempt := 0; attempt < 2; attempt++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			if snapshotErr = publish(); snapshotErr == nil {
				return nil
			}
		}
		return snapshotErr
	}
	return sink.Reseed(protocol.JournalReseed{
		Roster: roster,
		Events: namespaceRoster(dc.endpointID, res.Journal),
		Cursor: res.Cursor,
	})
}

// Gateway bridges one daemon's remote socket toward the phone. It holds the last
// journal cursor it delivered so a reconnect resumes from there (R-GW.5: journal
// events are never dropped; the cursor only advances as records are delivered).
type Gateway struct {
	socketPath string
	sink       JournalSink

	// deliveryMu orders every daemon snapshot read with the sink writes derived from it.
	// The order is always deliveryMu -> mu; no path acquires them in reverse.
	deliveryMu sync.Mutex

	// watchLive is the WATCH-LIVENESS clause of ADR-017 T4-b, consulted before EVERY
	// snapshot is handed to the sink. Without it the horizon was a field nothing read: an
	// unrenewed watch kept the daemon rendering, the sink sealing and the relay appending
	// full screens against the shared 8-appends/s budget for a phone that had gone away,
	// which is the precise defect T4-b was written to close. Nil (unit tests that build a
	// bare Gateway) means "no watcher owns this peek", and a peek nobody owns is allowed --
	// the watcher is what installs the horizon, so its absence cannot be its violation.
	watchLive func(session string) bool

	mu     sync.Mutex
	cursor uint64
}

// bindWatchLiveness installs the watch-liveness predicate the snapshot path consults. The
// TerminalWatcher owns the horizons and the Gateway owns the stream, so the predicate is
// injected rather than either type reaching into the other.
func (g *Gateway) bindWatchLiveness(live func(session string) bool) {
	g.watchLive = live
}

// watchStillLive answers the predicate for one namespaced session, defaulting to true when
// no watcher is bound.
func (g *Gateway) watchStillLive(session string) bool {
	if g.watchLive == nil {
		return true
	}
	return g.watchLive(session)
}

// BlankTerminal publishes an EMPTY snapshot for one session, which is how the machine says
// "I am no longer rendering this" to a phone that never asked it to stop (ADR-017 T4-b,
// round-3 moderate 5).
//
// THE HARM IT CLOSES, stated as what the user sees. The watch horizon is sixty seconds and
// the renewal rides the phone's redraw, so an IDLE fallback screen on an IDLE session can
// have its watch reaped while the user is looking at it. `Reap` -> `Unwatch` -> ctx cancel
// ends the peek FROM THE GATEWAY SIDE: the daemon sends no OpError, nothing reaches the sink,
// and the phone keeps the last grid. It is not even labelled stale -- the stream-stale flag
// is set by explicit desync events, not by a clock, and the machine heartbeat keeps arriving.
// That is "the machine went quiet rendered as the terminal is idle", which is precisely what
// T4-b's staleness rule exists to forbid, introduced by this wave's own horizon.
//
// A sink that does not take terminal snapshots has nothing to blank and is not an error: the
// same tolerance RunTerminal's own discovery has.
func (g *Gateway) BlankTerminal(session string) error {
	sink, ok := g.sink.(TerminalSink)
	if !ok {
		return nil
	}
	return sink.Terminal(protocol.TerminalViewV1{Session: session})
}

// CursorSource is a sink that knows its own DURABLE delivered cursor. New seeds the
// gateway's resume point from it -- mirroring how RunTerminal discovers a TerminalSink, so
// New's signature is unchanged and a sink with no durable custody simply starts at 0.
type CursorSource interface {
	DeliveredCursor() uint64
}

// New returns a gateway that dials socketPath (the daemon remote.sock) and delivers
// the journal to sink, resuming from the cursor sink durably delivered (PB-GW-8). Without
// that seeding every restart re-reads from 0 and re-appends the WHOLE journal at fresh seqs
// into the same 600-per-tumbling-minute mailbox, which is also how a restart loop blows the
// §6.0 append budget.
func New(socketPath string, sink JournalSink) *Gateway {
	g := &Gateway{socketPath: socketPath, sink: sink}
	if cs, ok := sink.(CursorSource); ok {
		g.cursor = cs.DeliveredCursor()
	}
	return g
}

// Cursor is the highest journal cursor the gateway has delivered (its durable resume
// point).
func (g *Gateway) Cursor() uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cursor
}

// setCursor advances the delivered-cursor high-water mark (monotonic).
func (g *Gateway) setCursor(c uint64) {
	g.mu.Lock()
	if c > g.cursor {
		g.cursor = c
	}
	g.mu.Unlock()
}

// RunJournal connects to the daemon remote socket, delivers the roster snapshot as-of
// the current cursor, then streams live journal events to the sink until ctx is
// cancelled or the connection fails. It returns the reason it stopped; the caller may
// reconnect, and RunJournal resumes from the last delivered cursor (Cursor()). NOTE:
// the strict no-loss guarantee across the read->subscribe boundary also depends on the
// daemon's atomic read+subscribe (DME-2, agents-tracker-7ra); until that lands the window is
// held to the single control write below (never a relay round-trip), and a reconnect
// re-reads from the last cursor to recover any gap.
func (g *Gateway) RunJournal(ctx context.Context) error {
	dc, err := dialDaemon(g.socketPath, protocol.CapRemoteGateway, protocol.CapJournal)
	if err != nil {
		return err
	}
	defer func() { _ = dc.Close() }()

	// The endpoint id the daemon just assigned is the machine id the phone pairs against --
	// the same id every record below is namespaced with. Stamp the sink with it BEFORE the
	// Snapshot that publishes the reconcile record, or the record names no machine and the
	// phone refuses it, leaving mutating ops fail-closed forever (PB-SYNC-7).
	if ms, ok := g.sink.(machineNamer); ok {
		ms.SetMachine(dc.endpointID)
	}

	if err := func() error {
		// Hold one delivery fence from the cursor boundary through the atomic read,
		// subscription, roster replacement, backlog, and cursor advance. A concurrent
		// Resync must not publish a newer roster and then have this older bootstrap
		// overwrite it; a concurrent live delivery must not cross the same boundary.
		g.deliveryMu.Lock()
		defer g.deliveryMu.Unlock()

		// Snapshot: the atomic roster + events after our cursor (R-JRN.4).
		from := g.Cursor()
		res, err := dc.readJournal(from)
		if err != nil {
			return err
		}
		// Subscribe BEFORE the snapshot is forwarded, not after: everything between the
		// journal_read reply and this write is a window in which a live event reaches neither
		// the read nor the stream, and forwarding the snapshot means relay round-trips (the
		// roster records, and the reconcile record the sink leads each run with) that widen that
		// window from microseconds to tens of milliseconds -- long enough to lose the event of a
		// session launched moments after the gateway started. Events that arrive during the
		// forwarding below are buffered on the daemon conn and read by the loop; deliver dedups
		// them against the roster read by cursor, exactly as it already did for the overlap.
		if err := dc.writeControl(protocol.Control{Op: protocol.OpJournalSubscribe, EndpointID: dc.endpointID}); err != nil {
			return err
		}
		// Snapshot carries the boundary before this read's incremental events. Advancing it
		// to res.Cursor here would make deliver stale-drop the backlog forwarded below.
		if err := g.sink.Snapshot(namespaceRoster(dc.endpointID, res.Roster), from); err != nil {
			return err
		}
		for _, rec := range res.Journal {
			if err := g.deliverLocked(namespaceRecord(dc.endpointID, rec)); err != nil {
				return err
			}
		}
		if res.Cursor > from {
			g.setCursor(res.Cursor)
		}
		return nil
	}(); err != nil {
		return err
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ctrl, err := dc.readControl(time.Second)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue // idle; re-check ctx and keep waiting for events
			}
			return err
		}
		switch ctrl.Op {
		case protocol.OpJournalEvent:
			for _, rec := range ctrl.Journal {
				if err := g.deliver(namespaceRecord(dc.endpointID, rec)); err != nil {
					return err
				}
			}
		case protocol.OpError:
			return fmt.Errorf("daemon refused a journal op: %s (%s)", ctrl.Error, ctrl.ErrorCode)
		default:
			// The journal_subscribe ack (OpOK) and any other control are ignored.
		}
	}
}

// RunTerminal connects to the daemon remote socket, subscribes to the server-rendered
// terminal-snapshot stream FOR ONE SESSION, and forwards every decoded snapshot to the sink
// until ctx is cancelled or the connection fails. It mirrors RunJournal but is latest-wins
// per session (no roster read, no cursor: the phone's SnapshotCache keeps only the newest
// snapshot behind the shared relay seq gate). session is the namespaced id the phone asked
// to peek; it is carried in the terminal_subscribe Control so the daemon's resolveSession
// accepts it (handleTerminalSubscribe is session-scoped -- without the id it refuses "invalid
// session id"). The snapshot's session id is re-namespaced to the endpoint at egress, exactly
// like RunJournal, so the phone correlates a snapshot to the roster/command id it signs against.
// errPeekCapabilityRefused marks a terminal peek the daemon refused on the SESSION's own
// capability record (ADR-017 T2-c). It is terminal rather than transient: unlike a kill
// switch, which flips back, a session's record is authored once per incarnation and
// immutable except in the degrading direction, so reconnecting can only produce the same
// refusal until the session is replaced -- at which point the phone watches the new
// incarnation explicitly.
var errPeekCapabilityRefused = errors.New("remotegw: the session's capability record does not permit a terminal view")

// errWatchHorizonPassed ends a peek whose watch was not renewed within its horizon
// (ADR-017 T4-b). Like a capability refusal it is TERMINAL rather than transient: the phone
// stopped saying it was looking, so reconnecting would resume rendering for nobody. A
// phone that comes back asks for a new watch, which is the verb the capability gate is
// written over.
var errWatchHorizonPassed = errors.New("remotegw: the terminal watch horizon passed without a renewal")

func (g *Gateway) RunTerminal(ctx context.Context, session string) error {
	sink, ok := g.sink.(TerminalSink)
	if !ok {
		return fmt.Errorf("gateway: sink %T does not accept terminal snapshots", g.sink)
	}
	dc, err := dialDaemon(g.socketPath, protocol.CapRemoteGateway, protocol.CapJournal)
	if err != nil {
		return err
	}
	defer func() { _ = dc.Close() }()

	if err := dc.writeControl(protocol.Control{Op: protocol.OpTerminalSubscribe, EndpointID: dc.endpointID, SessionID: session}); err != nil {
		return err
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ctrl, err := dc.readControl(terminalIdleWake)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				// Idle wake: nothing is in flight, so push out whatever the coalescer is
				// still holding. Without it a burst's LAST frame never ships and the peek
				// sits on a stale grid until the session emits again -- for an idle
				// terminal, never (PB-GW-7).
				if err := flushTerminal(sink); err != nil {
					return err
				}
				continue // re-check ctx and keep waiting for snapshots
			}
			return err
		}
		switch ctrl.Op {
		case protocol.OpTerminalSnapshot:
			if ctrl.Terminal == nil {
				continue
			}
			// Don't forward a snapshot decoded just before a cancel (Unwatch/Close): a
			// post-cancel frame would race a rewatch's fresh stream onto the phone.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// ADR-017 T4-b, PER EMISSION. The watch's horizon is asked here and not only
			// on the reap tick, so the last screen rendered for a phone that stopped
			// renewing is not sealed and appended on its way past an expiry that has
			// already happened.
			if !g.watchStillLive(session) {
				return errWatchHorizonPassed
			}
			if err := sink.Terminal(terminalViewOf(dc.endpointID, ctrl)); err != nil {
				return err
			}
		case protocol.OpError:
			// The daemon ENDED the peek (idle kill-switch termination, session end, or a
			// subscribe-time refusal while off, Blocker 1b). BLANK the phone's latest-wins
			// cache so it stops showing the pre-teardown screen (Blocker 1d), then return so
			// the watcher backs off and reconnects (resuming when the switch flips back ON).
			_ = sink.Terminal(protocol.TerminalViewV1{Session: namespaceSessionID(dc.endpointID, session)})
			// Nothing follows the blank on this connection, so it must not sit in the
			// coalescer: force it out before returning.
			_ = flushTerminal(sink)
			if ctrl.ErrorCode == protocol.CodeCapabilityRefused {
				// ADR-017 T2-c: this session's capability record does not permit a
				// terminal view, and that answer does not change by asking again. It is
				// wrapped so the supervised loop ENDS the watch instead of reconnecting:
				// a refused peek must cost ONE dial, not a permanent backoff loop against
				// a daemon that will refuse every attempt for the life of the session.
				return fmt.Errorf("%w: %s", errPeekCapabilityRefused, ctrl.Error)
			}
			return fmt.Errorf("daemon ended the terminal peek: %s (%s)", ctrl.Error, ctrl.ErrorCode)
		default:
			// The terminal_subscribe ack (OpOK) and any other control are ignored.
		}
	}
}

// terminalIdleWake bounds one terminal read so RunTerminal wakes at least once per
// coalescing window and can flush a stashed snapshot. It must stay <= DefaultAppendWindow:
// a longer wake is a longer stale grid.
const terminalIdleWake = DefaultAppendWindow

// terminalFlusher is the coalescing wrapper's trailing-flush seam (CoalescingSink.Flush). A
// sink that does not coalesce holds nothing back and needs no flush.
type terminalFlusher interface {
	Flush() error
}

// flushTerminal forces out any snapshot the sink is holding back, if it holds any.
func flushTerminal(sink TerminalSink) error {
	f, ok := sink.(terminalFlusher)
	if !ok {
		return nil
	}
	return f.Flush()
}

// ForwardCommand sends a phone-authored, device-signed mutating op to the daemon's
// remote socket and returns the daemon's reply. It is the command-IN counterpart to
// the journal-OUT bridge: the gateway is a blind conduit -- it forwards the phone's
// signature untouched, and the daemon verifies it independently (R-POL.9). The gateway
// holds no device key and cannot forge or escalate a command. The in-envelope bodies ride
// across on the frame itself: `rc.Launch` for an OpLaunch, `rc.Approve` for an OpApprove
// (IS-LIFE-4), nil for everything else. A fresh connection is used per command (pooling is a
// later refinement).
func (g *Gateway) ForwardCommand(op string, rc protocol.RemoteCommand) (protocol.Control, error) {
	dc, err := dialDaemon(g.socketPath, protocol.CapRemoteGateway)
	if err != nil {
		return protocol.Control{}, err
	}
	defer func() { _ = dc.Close() }()

	if err := dc.writeControl(forwardControl(dc.endpointID, op, rc)); err != nil {
		return protocol.Control{}, err
	}
	// The daemon replies OpOK / OpLaunch on success or OpError on refusal.
	return dc.readControl(10 * time.Second)
}

// forwardControl is ForwardCommand's ASSEMBLY, split out so it can be tested without a
// daemon socket.
//
// IT IS SPLIT OUT BECAUSE THE ASSEMBLY IS WHERE THE BUGS ARE, TWICE NOW. Wave R5 shipped a
// blocker whose whole shape was "the reference path was never exercised" (the PolicyEnv
// field), and Wave R6's review found the identical defect one field over: rc.ComposerSend
// was NEVER copied onto the Control, so every real phone send arrived at the daemon with a
// nil body while every test that "covered" composer_send hand-built its own Control and
// bypassed this function entirely. r6fix_forwardassembly_test.go now walks
// protocol.RemoteCommand BY REFLECTION and fails on any body field this function forgets --
// a fence that fails on the day a new body is added, not on the day a user taps Send.
func forwardControl(endpointID, op string, rc protocol.RemoteCommand) protocol.Control {
	cmd := rc.DeviceCommandAuth
	sessionID := rc.Session
	exp := cmd.ExpiresAt
	ctrl := protocol.Control{
		Op:          op,
		EndpointID:  endpointID,
		SessionID:   sessionID,
		OperationID: cmd.OperationID,
		DeviceID:    cmd.DeviceID,
		DeviceSig:   cmd.Sig,
		ExpiresAt:   &exp,
		Launch:      rc.Launch,
		Approve:     rc.Approve,
		BodyVersion: rc.BodyVersion,
		// Wave R5: the session_launch preset body rides across unchanged (its content is
		// bound by the phone's signature via SessionLaunchContentHash), and
		// operation_status's query subject is copied onto its Control coordinate.
		SessionLaunch:      rc.SessionLaunch,
		SubjectOperationID: rc.SubjectOperationID,
		// Wave R6: the composer_send body (bound by ComposerSendContentHash), the
		// turn_interrupt body (bound by TurnInterruptContentHash, fix-pack B7) and the two
		// UNSIGNED M3 read bodies. Every one of them is content the gateway carries and
		// never reads.
		ComposerSend:  rc.ComposerSend,
		TurnInterrupt: rc.TurnInterrupt,
		History:       rc.History,
		Detail:        rc.Detail,
		// Wave R8 (ADR-017 T6): the terminal_control_begin body (bound by
		// TerminalControlBeginContentHash), the unsigned terminal_input body, and the
		// generation a keepalive names. All three are content the gateway carries and
		// never reads -- it is a blind conduit here exactly as it is for the bodies above.
		TerminalControlBegin: rc.TerminalControlBegin,
		TerminalInput:        rc.TerminalInput,
		ControlGeneration:    rc.ControlGeneration,
	}
	// device_revoke names a DEVICE, not a session, and handleDeviceRevoke reads
	// Control.TargetDeviceID -- both to authorize (requireRemoteAuthz's subject) and to
	// remove. The phone signs the target device id in the SESSION position of the command
	// tuple, because that tuple has no separate device field, so it arrives here as
	// sessionID and is copied across. The gateway cannot escalate by doing so: the subject
	// is bound under the phone's signature, and any other value simply fails the daemon's
	// authorization. Without this the arm in opForAction would forward a revoke with an
	// empty target, which the daemon refuses.
	if op == protocol.OpDeviceRevoke {
		ctrl.TargetDeviceID = sessionID
	}
	// The two M3 reads name their session INSIDE their own body (they are unsigned, so
	// the signed tuple's Session slot is not their subject). Copy it onto the Control
	// coordinate so the daemon's session-scoped logging and reply addressing agree with
	// the body -- the handlers themselves read the body, never this field.
	if ctrl.SessionID == "" {
		switch {
		case rc.History != nil:
			ctrl.SessionID = rc.History.Session
		case rc.Detail != nil:
			ctrl.SessionID = rc.Detail.Session
		}
	}
	return ctrl
}

// namespaceRecord rewrites a journal record's SessionID to the endpoint-scoped id
// (<endpoint>/<local>) the phone commands against (agents-tracker-p1b). The daemon
// stores and journals raw local ids, but its SessionViews and remote command targets
// are namespaced; namespacing at the gateway's remote egress makes the id the phone
// sees in the journal identical to the id it must sign a command over, so a phone can
// correlate a roster/event entry to a command with no side channel. A record with no
// SessionID (session-neutral, e.g. gateway presence) or an already-namespaced id is
// left untouched.
// It is also amendment T2-b's SECOND SEAM -- "where it is decoded off the wire in the
// gateway" -- which round 2's evidence claimed existed and which did not: `Validate()` had no
// gateway caller and the gateway never touched the record at all (round-3 blocker 2a).
//
// WHY THE MIDDLE SEAM IS NOT REDUNDANT WITH THE AUTHOR'S. The daemon's author-side validation
// protects records THIS daemon writes. It says nothing about a capabilities.json left by an
// older or rolled-back build, a partially-written file, or a daemon an attacker has replaced
// -- and this is the last machine-side thing between any of those and a phone that will route
// a session on what it receives. An inconsistent record is DROPPED here rather than merely
// refused a route downstream, so what crosses the boundary is what a phone with any router,
// present or future, can safely act on.
//
// THE RECORD IS DROPPED WHOLE, and the session keeps T2-a's honest status card -- the same
// destination the phone's own decode seam reaches. A partially-trusted record is exactly the
// inconsistent state T2-b makes unrepresentable.
func namespaceRecord(endpointID string, rec protocol.JournalRecord) protocol.JournalRecord {
	rec.SessionID = namespaceSessionID(endpointID, rec.SessionID)
	if rec.Capabilities != nil && rec.Capabilities.Validate() != nil {
		// Nil the COPY's pointer, never the record behind it: the daemon's own registry
		// hands this pointer out, so writing through it would blank the machine's copy.
		rec.Capabilities = nil
	}
	return rec
}

// namespaceSessionID rewrites a raw local session id to its endpoint-scoped form
// (<endpoint>/<local>). A session-neutral id ("") or an already-namespaced id (contains
// "/") is returned unchanged, so it is safe at every remote egress (journal records and
// terminal snapshots) regardless of whether the daemon emitted a raw or already-namespaced
// id.
func namespaceSessionID(endpointID, session string) string {
	if endpointID == "" || session == "" || strings.Contains(session, "/") {
		return session
	}
	return protocol.NamespacedID(endpointID, session)
}

// namespaceRoster applies namespaceRecord to each roster record, returning a new slice
// so the caller's snapshot is not mutated.
func namespaceRoster(endpointID string, roster []protocol.JournalRecord) []protocol.JournalRecord {
	if len(roster) == 0 {
		return roster
	}
	out := make([]protocol.JournalRecord, len(roster))
	for i, rec := range roster {
		out[i] = namespaceRecord(endpointID, rec)
	}
	return out
}

// deliver forwards a record to the sink only if it advances the delivered cursor,
// deduplicating the small read/subscribe overlap so no event is delivered twice.
func (g *Gateway) deliver(rec protocol.JournalRecord) error {
	g.deliveryMu.Lock()
	defer g.deliveryMu.Unlock()
	return g.deliverLocked(rec)
}

// deliverLocked is deliver inside the snapshot/delivery ordering fence. The caller must
// hold deliveryMu. Cursor state remains separately guarded by mu.
func (g *Gateway) deliverLocked(rec protocol.JournalRecord) error {
	g.mu.Lock()
	if rec.Cursor != 0 && rec.Cursor <= g.cursor {
		g.mu.Unlock()
		return nil
	}
	g.mu.Unlock()

	// R-GW.5/GW-H1: forward first, advance the cursor only after the sink acks. A failed
	// record must NOT record its cursor, or the reconnect re-read would skip it as
	// already-delivered instead of redelivering it.
	if err := g.sink.Event(rec); err != nil {
		return err
	}

	g.mu.Lock()
	if rec.Cursor > g.cursor {
		g.cursor = rec.Cursor
	}
	g.mu.Unlock()
	return nil
}

// dialDaemon is the gateway's minimal remote-tier client: it speaks the frozen wire +
// Control protocol directly because protocol.Client exposes no journal ops.
type daemonConn struct {
	conn       net.Conn
	endpointID string
}

func dialDaemon(socketPath string, caps ...string) (*daemonConn, error) {
	conn, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		return nil, err
	}
	d := &daemonConn{conn: conn}
	if err := d.writeControl(protocol.Control{Op: protocol.OpHello, ProtocolVersion: protocol.Version, Capabilities: caps}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	rep, err := d.readControl(5 * time.Second)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if rep.Op != protocol.OpHello {
		_ = conn.Close()
		return nil, fmt.Errorf("gateway: hello reply op %q, want %q", rep.Op, protocol.OpHello)
	}
	d.endpointID = rep.EndpointID
	return d, nil
}

func (d *daemonConn) writeControl(c protocol.Control) error {
	body, err := protocol.EncodeControl(c)
	if err != nil {
		return err
	}
	return wire.WriteFrame(d.conn, wire.TControl, body)
}

func (d *daemonConn) readControl(within time.Duration) (protocol.Control, error) {
	_ = d.conn.SetReadDeadline(time.Now().Add(within))
	typ, payload, err := wire.ReadFrame(d.conn)
	if err != nil {
		return protocol.Control{}, err
	}
	if typ != wire.TControl {
		return protocol.Control{}, fmt.Errorf("gateway: frame type %d, want a control frame", typ)
	}
	return protocol.DecodeControl(payload)
}

// awaitOp reads control frames until one with the wanted op arrives (or the overall
// deadline elapses), returning an error on a refusal.
func (d *daemonConn) awaitOp(op string, within time.Duration) (protocol.Control, error) {
	deadline := time.Now().Add(within)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return protocol.Control{}, fmt.Errorf("gateway: timed out awaiting %q", op)
		}
		ctrl, err := d.readControl(remaining)
		if err != nil {
			return protocol.Control{}, err
		}
		switch ctrl.Op {
		case op:
			return ctrl, nil
		case protocol.OpError:
			return protocol.Control{}, fmt.Errorf("gateway: daemon refused %q: %s (%s)", op, ctrl.Error, ctrl.ErrorCode)
		default:
			// skip unrelated frames
		}
	}
}

// readJournal consumes every bounded page produced from one atomic daemon read before
// exposing any part to the sink. A failed or malformed page sequence is discarded whole.
func (d *daemonConn) readJournal(from uint64) (protocol.Control, error) {
	if err := d.writeControl(protocol.Control{
		Op: protocol.OpJournalRead, EndpointID: d.endpointID, Cursor: from,
		JournalMaxBytes: wire.MaxFrame - 1,
	}); err != nil {
		return protocol.Control{}, err
	}
	var out protocol.Control
	last := from
	for first := true; ; first = false {
		page, err := d.awaitOp(protocol.OpJournalRead, 10*time.Second)
		if err != nil {
			return protocol.Control{}, err
		}
		if first {
			out = page
			out.Journal = nil
			out.Roster = nil
		} else if page.Cursor != out.Cursor {
			return protocol.Control{}, fmt.Errorf(
				"gateway: journal boundary changed across pages: %d then %d", out.Cursor, page.Cursor,
			)
		}
		if page.JournalMore && len(page.Journal) == 0 && len(page.Roster) == 0 {
			return protocol.Control{}, fmt.Errorf("gateway: empty non-final journal page at cursor %d", page.Cursor)
		}
		for _, rec := range page.Journal {
			if rec.Cursor <= last {
				return protocol.Control{}, fmt.Errorf(
					"gateway: journal page did not advance after cursor %d (got %d)", last, rec.Cursor,
				)
			}
			last = rec.Cursor
			out.Journal = append(out.Journal, rec)
		}
		out.Roster = append(out.Roster, page.Roster...)
		out.FullResync = out.FullResync || page.FullResync
		if !page.JournalMore {
			if out.Cursor < last {
				return protocol.Control{}, fmt.Errorf(
					"gateway: journal boundary %d precedes event cursor %d", out.Cursor, last,
				)
			}
			out.JournalMore = false
			return out, nil
		}
	}
}

// Close closes the connection.
func (d *daemonConn) Close() error { return d.conn.Close() }
