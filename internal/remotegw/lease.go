package remotegw

// A7 input Slice 3 — the gateway's PERSISTENT lease-holding connection, the keystroke
// data-plane foundation. Unlike ForwardCommand (a fresh daemon connection per mutating
// op), a live-input session must ride ONE connection: take_control establishes cc.control
// on a connection and BINDS the daemon's input gate to THAT connection's lease
// (cc.attSession/attGen), so every subsequent OpDataIn/OpResize must travel the same conn
// (internal/protocol/server.go handleTakeControl -> controlGateOpen -> handleDataIn).
//
// LeaseConn is that conn primitive: it dials one remote-daemon connection, forwards a
// take_control reconstructed from a phone-authored RemoteCommand (so the daemon's
// requireRemoteAuthz verifies the device signature and the gate binds), then writes
// wire.TDataIn keystrokes and OpResize on that SAME connection. Its readLoop captures the
// OpLease grant and signals lease-death on OpDetach or connection close. With the remote
// pump suppressing output (F3), the reader sees only OpLease + OpDetach — there is no raw
// TDataOut/TSnapshot to drain. The LeaseManager that owns many of these is Slice 4.

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/wire"
)

var (
	// errLeaseDead reports that the lease died (OpDetach or conn close) before it was
	// ever granted — e.g. the daemon refused the take_control (OpError).
	errLeaseDead = errors.New("remotegw: lease died before it was granted")
	// errLeaseTimeout reports that no OpLease grant arrived within the caller's deadline.
	errLeaseTimeout = errors.New("remotegw: timed out awaiting the lease grant")
)

// LeaseConn holds ONE persistent remote-daemon connection carrying a take_control lease.
// It is exported because the only harness that stands up a GENUINELY-authorized
// take_control (a real device signature the daemon's requireRemoteAuthz verifies) lives in
// package skeleton, whose test drives this primitive; the internal machinery (readLoop)
// stays unexported.
type LeaseConn struct {
	dc          *daemonConn
	session     string // namespaced session id the lease targets (for OpResize addressing)
	operationID string // the phone's take_control operation id, so a severance is attributable

	wmu sync.Mutex // serializes writes on the conn (take_control, data_in, resize)

	mu         sync.Mutex
	gen        uint64 // captured OpLease generation (0 until granted)
	refusal    string // the daemon's OpError text, if it refused (see readLoop)
	leased     chan struct{}
	leasedOnce sync.Once
	dead       chan struct{}
	deadOnce   sync.Once
}

// DialLease dials one persistent remote-daemon connection and forwards a take_control
// reconstructed from the phone-authored, opened RemoteCommand — the signed tuple
// (DeviceCommandAuth) plus the wire GateToken and requested TTLSeconds — so the daemon's
// requireRemoteAuthz + gate bind. The lease is granted asynchronously: the caller awaits it
// with AwaitLease. A failure to dial or send returns an error and no LeaseConn.
func DialLease(socketPath string, cmd protocol.RemoteCommand) (*LeaseConn, error) {
	dc, err := dialDaemon(socketPath, protocol.CapRemoteGateway)
	if err != nil {
		return nil, err
	}
	lc := &LeaseConn{
		dc:          dc,
		session:     cmd.Session,
		operationID: cmd.OperationID,
		leased:      make(chan struct{}),
		dead:        make(chan struct{}),
	}
	// Reconstruct the take_control Control from the RemoteCommand, exactly as
	// ForwardCommand reconstructs a mutating op (gateway.go): the gateway forwards the
	// device signature untouched; the daemon verifies it and recomputes SHA256(GateToken).
	exp := cmd.ExpiresAt
	ctrl := protocol.Control{
		Op:          protocol.OpTakeControl,
		EndpointID:  dc.endpointID,
		SessionID:   cmd.Session,
		OperationID: cmd.OperationID,
		DeviceID:    cmd.DeviceID,
		DeviceSig:   cmd.Sig,
		ExpiresAt:   &exp,
		GateToken:   cmd.GateToken,
		TTLSeconds:  cmd.TTLSeconds,
	}
	lc.wmu.Lock()
	werr := dc.writeControl(ctrl)
	lc.wmu.Unlock()
	if werr != nil {
		_ = dc.Close()
		return nil, werr
	}
	go lc.readLoop()
	return lc, nil
}

// readLoop drains the lease connection: it captures the OpLease generation (the lease
// grant), captures the REASON on an OpError refusal, and treats a refusal, an OpDetach or a
// connection close as lease-death. Post-F3 the daemon sends a remote controller OpLease +
// OpDetach only, so there is no raw output frame to drain (any stray non-control frame is
// ignored).
func (lc *LeaseConn) readLoop() {
	defer lc.markDead()
	for {
		// Block until a frame arrives or the connection closes (no read deadline): the
		// lease conn is long-lived and only teardown ends the loop.
		_ = lc.dc.conn.SetReadDeadline(time.Time{})
		typ, payload, err := wire.ReadFrame(lc.dc.conn)
		if err != nil {
			return
		}
		if typ != wire.TControl {
			continue // no raw output to a remote controller (F3); ignore any non-control frame
		}
		ctrl, derr := protocol.DecodeControl(payload)
		if derr != nil {
			continue
		}
		switch ctrl.Op {
		case protocol.OpLease:
			if ctrl.Generation != 0 {
				lc.mu.Lock()
				lc.gen = ctrl.Generation
				lc.mu.Unlock()
				lc.leasedOnce.Do(func() { close(lc.leased) })
			}
		case protocol.OpError:
			// CAPTURE THE REASON before dying. The daemon refuses a take_control for six
			// distinct causes -- an absent gate token, an unknown device, a forged or expired
			// signature, an insufficient capability, the kill switch, a consumed operation id
			// -- and each one says so in this frame. Returning without reading it collapsed
			// all six into "the lease died before it was granted", which names the transport
			// and not one of the causes; the phone then shows a dead keyboard with no way to
			// tell a policy refusal from a broken socket.
			lc.mu.Lock()
			lc.refusal = ctrl.Error
			lc.mu.Unlock()
			return
		case protocol.OpDetach:
			return // lease lost: dead
		default:
			// OpOK (a resize/other ack) or any other control: nothing to do.
		}
	}
}

// markDead closes the dead channel once, signalling the lease is gone.
func (lc *LeaseConn) markDead() {
	lc.deadOnce.Do(func() { close(lc.dead) })
}

// AwaitLease blocks until the readLoop captures the OpLease grant (returning its nonzero
// generation), the lease dies, or the timeout elapses.
//
// A death the DAEMON explained is reported in the daemon's own words, wrapped so
// errors.Is(err, errLeaseDead) still holds for a caller classifying the failure. A death with
// no explanation -- the connection dropped, the session ended -- is the bare sentinel, which
// is then a true statement rather than a catch-all.
func (lc *LeaseConn) AwaitLease(timeout time.Duration) (uint64, error) {
	select {
	case <-lc.leased:
		return lc.Generation(), nil
	case <-lc.dead:
		if reason := lc.refusalReason(); reason != "" {
			return 0, fmt.Errorf("%w: the machine refused it: %s", errLeaseDead, reason)
		}
		return 0, errLeaseDead
	case <-time.After(timeout):
		return 0, errLeaseTimeout
	}
}

// refusalReason is the daemon's OpError text, or "" when the lease died without one.
func (lc *LeaseConn) refusalReason() string {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.refusal
}

// WriteDataIn writes a wire.TDataIn keystroke frame on the lease connection. The daemon
// forwards it to the session's shim only while the control gate is open (the four-clause
// gate bound to this connection's lease); it is fire-and-forget (no reply).
func (lc *LeaseConn) WriteDataIn(b []byte) error {
	lc.wmu.Lock()
	defer lc.wmu.Unlock()
	return wire.WriteFrame(lc.dc.conn, wire.TDataIn, b)
}

// WriteResize writes an OpResize control on the lease connection. On the remote tier the
// daemon forwards the resize on the gate-validated lease identity (it ignores the wire
// session/generation), so this is likewise fire-and-forget.
func (lc *LeaseConn) WriteResize(cols, rows int) error {
	lc.wmu.Lock()
	defer lc.wmu.Unlock()
	return lc.dc.writeControl(protocol.Control{
		Op:         protocol.OpResize,
		EndpointID: lc.dc.endpointID,
		SessionID:  lc.session,
		Generation: lc.Generation(),
		Cols:       cols,
		Rows:       rows,
	})
}

// Generation returns the captured OpLease generation (0 until the lease is granted).
func (lc *LeaseConn) Generation() uint64 {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.gen
}

// Dead is closed when the lease dies (OpDetach from the daemon or the connection closes).
func (lc *LeaseConn) Dead() <-chan struct{} { return lc.dead }

// Close tears down the connection; the client EOF releases the lease server-side, and the
// readLoop then errors out and marks the lease dead.
func (lc *LeaseConn) Close() error { return lc.dc.Close() }
