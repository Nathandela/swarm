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

// TerminalSink receives the server-rendered terminal snapshots the gateway bridges toward
// the phone (A7 slice D). RelaySink implements it alongside JournalSink; RunTerminal
// requires the gateway's sink to accept snapshots.
type TerminalSink interface {
	Terminal(session string, lines []string, cols, rows int) error
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

// New returns a gateway that dials socketPath (the daemon remote.sock) and delivers
// the journal to sink.
func New(socketPath string, sink JournalSink) *Gateway {
	return &Gateway{socketPath: socketPath, sink: sink}
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
// daemon's atomic read+subscribe (DME-2, agents-tracker-7ra); until that lands a
// reconnect re-reads from the last cursor to recover any gap.
func (g *Gateway) RunJournal(ctx context.Context) error {
	dc, err := dialDaemon(g.socketPath, protocol.CapRemoteGateway, protocol.CapJournal)
	if err != nil {
		return err
	}
	defer dc.Close()

	// Snapshot: the atomic roster + events after our cursor (R-JRN.4).
	from := g.Cursor()
	if err := dc.writeControl(protocol.Control{Op: protocol.OpJournalRead, EndpointID: dc.endpointID, Cursor: from}); err != nil {
		return err
	}
	res, err := dc.awaitOp(protocol.OpJournalRead, 10*time.Second)
	if err != nil {
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

	// Live stream: subscribe, then relay every journal_event whose cursor advances past
	// what we have delivered (dedup guards the read->subscribe overlap).
	if err := dc.writeControl(protocol.Control{Op: protocol.OpJournalSubscribe, EndpointID: dc.endpointID}); err != nil {
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
// terminal-snapshot stream, and forwards every decoded snapshot to the sink until ctx is
// cancelled or the connection fails. It mirrors RunJournal but is latest-wins per session
// (no roster read, no cursor: the phone's SnapshotCache keeps only the newest snapshot
// behind the shared relay seq gate). The snapshot's session id is namespaced to the
// endpoint at egress, exactly like RunJournal, so the phone correlates a snapshot to the
// roster/command id it signs against. NOTE: the live daemon terminal_subscribe handler is
// Slice E/F; RunTerminal's Slice-D contract is unit-level (it constructs the subscribe
// frame and forwards decoded snapshots), its live E2E deferred to Slice 7.
func (g *Gateway) RunTerminal(ctx context.Context) error {
	sink, ok := g.sink.(TerminalSink)
	if !ok {
		return fmt.Errorf("gateway: sink %T does not accept terminal snapshots", g.sink)
	}
	dc, err := dialDaemon(g.socketPath, protocol.CapRemoteGateway, protocol.CapJournal)
	if err != nil {
		return err
	}
	defer dc.Close()

	if err := dc.writeControl(protocol.Control{Op: protocol.OpTerminalSubscribe, EndpointID: dc.endpointID}); err != nil {
		return err
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ctrl, err := dc.readControl(time.Second)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue // idle; re-check ctx and keep waiting for snapshots
			}
			return err
		}
		switch ctrl.Op {
		case protocol.OpTerminalSnapshot:
			if ctrl.Terminal == nil {
				continue
			}
			session := namespaceSessionID(dc.endpointID, ctrl.Terminal.Session)
			if err := sink.Terminal(session, ctrl.Terminal.Lines, ctrl.Terminal.Cols, ctrl.Terminal.Rows); err != nil {
				return err
			}
		case protocol.OpError:
			return fmt.Errorf("daemon refused a terminal op: %s (%s)", ctrl.Error, ctrl.ErrorCode)
		default:
			// The terminal_subscribe ack (OpOK) and any other control are ignored.
		}
	}
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
