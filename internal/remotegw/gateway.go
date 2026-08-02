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

// JournalSink receives the journal the gateway bridges toward the phone. Snapshot is
// called once per (re)connection with the roster as-of the read cursor; Event is then
// called for each live record in cursor order. Implementations must not block the
// gateway's read loop (R-GW.4/.5: bounded/coalescing on the relay side).
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
	Terminal(session string, lines []string, cols, rows int) error
}

// ReseedSink receives PB-SYNC-2's journal repair frame. RelaySink implements it alongside
// JournalSink, and both wrappers in the chain (CoalescingSink, PushNotifier) forward it --
// a wrapper that swallowed it would leave every resync answered and delivering nothing.
type ReseedSink interface {
	Reseed(rs protocol.JournalReseed) error
}

// errNoReseedSink refuses a resync whose sink cannot publish the repair, rather than
// reporting a repair that never left the machine. The phone's stale flag clears only on the
// frame's arrival, so a silent nil here would leave it stale with nothing to say why.
var errNoReseedSink = errors.New("remotegw: this sink cannot publish a journal reseed")

// Resync answers the phone's journal_resync (PB-SYNC-2): read the daemon's atomic roster +
// the events after the phone's own cursor, and publish them as ONE reseed frame.
//
// It opens its OWN daemon connection rather than borrowing RunJournal's. RunJournal's conn
// is inside a blocking read loop that owns its control stream, so interleaving a second
// journal_read on it would race the subscription's events for the reply; and a resync must
// work while the journal loop is between reconnects, which is exactly when the phone is
// most likely to have a hole.
//
// from is the phone's cursor, so res.Roster/res.Events/res.Cursor map onto the reseed's
// three fields directly. The roster is namespaced with the daemon's endpoint id like every
// other record the phone sees, or the repaired session ids do not match the ids it signs
// commands against.
func (g *Gateway) Resync(ctx context.Context, from uint64) error {
	sink, ok := g.sink.(ReseedSink)
	if !ok {
		return errNoReseedSink
	}
	dc, err := dialDaemon(g.socketPath, protocol.CapRemoteGateway, protocol.CapJournal)
	if err != nil {
		return err
	}
	defer dc.Close()
	if err := dc.writeControl(protocol.Control{Op: protocol.OpJournalRead, EndpointID: dc.endpointID, Cursor: from}); err != nil {
		return err
	}
	res, err := dc.awaitOp(protocol.OpJournalRead, 10*time.Second)
	if err != nil {
		return err
	}
	return sink.Reseed(protocol.JournalReseed{
		Roster: namespaceRoster(dc.endpointID, res.Roster),
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

	mu     sync.Mutex
	cursor uint64
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
	defer dc.Close()

	// The endpoint id the daemon just assigned is the machine id the phone pairs against --
	// the same id every record below is namespaced with. Stamp the sink with it BEFORE the
	// Snapshot that publishes the reconcile record, or the record names no machine and the
	// phone refuses it, leaving mutating ops fail-closed forever (PB-SYNC-7).
	if ms, ok := g.sink.(machineNamer); ok {
		ms.SetMachine(dc.endpointID)
	}

	// Snapshot: the atomic roster + events after our cursor (R-JRN.4).
	from := g.Cursor()
	if err := dc.writeControl(protocol.Control{Op: protocol.OpJournalRead, EndpointID: dc.endpointID, Cursor: from}); err != nil {
		return err
	}
	res, err := dc.awaitOp(protocol.OpJournalRead, 10*time.Second)
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
	if err := g.sink.Snapshot(namespaceRoster(dc.endpointID, res.Roster), res.Cursor); err != nil {
		return err
	}
	for _, rec := range res.Journal {
		if err := g.deliver(namespaceRecord(dc.endpointID, rec)); err != nil {
			return err
		}
	}
	if res.Cursor > from {
		g.setCursor(res.Cursor)
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
func (g *Gateway) RunTerminal(ctx context.Context, session string) error {
	sink, ok := g.sink.(TerminalSink)
	if !ok {
		return fmt.Errorf("gateway: sink %T does not accept terminal snapshots", g.sink)
	}
	dc, err := dialDaemon(g.socketPath, protocol.CapRemoteGateway, protocol.CapJournal)
	if err != nil {
		return err
	}
	defer dc.Close()

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
			session := namespaceSessionID(dc.endpointID, ctrl.Terminal.Session)
			if err := sink.Terminal(session, ctrl.Terminal.Lines, ctrl.Terminal.Cols, ctrl.Terminal.Rows); err != nil {
				return err
			}
		case protocol.OpError:
			// The daemon ENDED the peek (idle kill-switch termination, session end, or a
			// subscribe-time refusal while off, Blocker 1b). BLANK the phone's latest-wins
			// cache so it stops showing the pre-teardown screen (Blocker 1d), then return so
			// the watcher backs off and reconnects (resuming when the switch flips back ON).
			_ = sink.Terminal(namespaceSessionID(dc.endpointID, session), nil, 0, 0)
			// Nothing follows the blank on this connection, so it must not sit in the
			// coalescer: force it out before returning.
			_ = flushTerminal(sink)
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
// holds no device key and cannot forge or escalate a command. `launch` is set only for
// an OpLaunch (nil otherwise). A fresh connection is used per command (pooling is a
// later refinement).
func (g *Gateway) ForwardCommand(op, sessionID string, cmd protocol.DeviceCommandAuth, launch *protocol.LaunchReq) (protocol.Control, error) {
	dc, err := dialDaemon(g.socketPath, protocol.CapRemoteGateway)
	if err != nil {
		return protocol.Control{}, err
	}
	defer dc.Close()

	exp := cmd.ExpiresAt
	ctrl := protocol.Control{
		Op:          op,
		EndpointID:  dc.endpointID,
		SessionID:   sessionID,
		OperationID: cmd.OperationID,
		DeviceID:    cmd.DeviceID,
		DeviceSig:   cmd.Sig,
		ExpiresAt:   &exp,
		Launch:      launch,
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
	if err := dc.writeControl(ctrl); err != nil {
		return protocol.Control{}, err
	}
	// The daemon replies OpOK / OpLaunch on success or OpError on refusal.
	return dc.readControl(10 * time.Second)
}

// namespaceRecord rewrites a journal record's SessionID to the endpoint-scoped id
// (<endpoint>/<local>) the phone commands against (agents-tracker-p1b). The daemon
// stores and journals raw local ids, but its SessionViews and remote command targets
// are namespaced; namespacing at the gateway's remote egress makes the id the phone
// sees in the journal identical to the id it must sign a command over, so a phone can
// correlate a roster/event entry to a command with no side channel. A record with no
// SessionID (session-neutral, e.g. gateway presence) or an already-namespaced id is
// left untouched.
func namespaceRecord(endpointID string, rec protocol.JournalRecord) protocol.JournalRecord {
	rec.SessionID = namespaceSessionID(endpointID, rec.SessionID)
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
		conn.Close()
		return nil, err
	}
	rep, err := d.readControl(5 * time.Second)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if rep.Op != protocol.OpHello {
		conn.Close()
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

// Close closes the connection.
func (d *daemonConn) Close() error { return d.conn.Close() }
