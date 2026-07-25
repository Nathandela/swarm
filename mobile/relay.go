package swarmmobile

// The transport plane: one authenticated relay connection per Start..Stop generation,
// draining the machine -> phone mailbox into the core's durable receive transaction and
// appending the phone -> machine one.
//
// Nothing here decides anything about a frame. phonecore.MailboxRouter.AcceptCommit owns
// the per-(sender,epoch) replay guard, the one-Save receive transaction and the ack
// ordering; this loop only supplies bytes and a cursor, and turns what the core accepted
// into events for the app.

import (
	"context"
	"crypto/ed25519"
	"errors"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// reconnectDelay is the fixed backoff between relay dial attempts. A phone that
// reconnects hard would exhaust the relay's per-target quota, which is shared with the
// journal it is trying to receive.
const reconnectDelay = 250 * time.Millisecond

// relayAcker releases consumed relay mailbox items. It is injected into the core, which
// must not import the relay client (PB-BIND-0 constrains its closure).
//
// Acks are COALESCED to one per drained page rather than one per frame. The relay ack is
// monotonic and idempotent -- acking cursor N releases everything up to it -- and an ack
// that a process death loses is harmless: the relay redelivers, the phone's DURABLE
// receive high-water refuses the redelivery with crypto.ErrStaleSeq, and the frame is
// acked then. Per-frame acking cost a full websocket round trip and a server-side commit
// on every journal event, which made the phone drain several times slower than the
// machine could publish.
type relayAcker struct{ app *App }

func (r *relayAcker) Ack(cursor uint64) error {
	a := r.app
	a.mu.Lock()
	if cursor > a.ackPending {
		a.ackPending = cursor
	}
	a.mu.Unlock()
	return nil
}

// flushAcks releases everything the core has committed since the last flush.
func (a *App) flushAcks(ctx context.Context, cl *relay.Client) {
	a.mu.Lock()
	cursor := a.ackPending
	sent := a.ackSent
	a.mu.Unlock()
	if cursor <= sent {
		return
	}
	if err := cl.MailboxAck(ctx, cursor); err != nil {
		return
	}
	a.mu.Lock()
	if cursor > a.ackSent {
		a.ackSent = cursor
	}
	a.mu.Unlock()
}

// conn returns the live relay client, or why there is none.
func (a *App) conn() (*relay.Client, error) {
	if a == nil {
		return nil, errNoReceiver
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, errClosed
	}
	if a.sess == nil {
		return nil, errNotRunning
	}
	if a.client == nil {
		return nil, errors.New("swarmmobile: relay connection not established yet")
	}
	return a.client, nil
}

// awaitConn waits briefly for the connection Start is bringing up, so a screen that
// issues a command immediately after Start is not refused by a race it cannot see. A
// stopped or closed App fails immediately -- there is nothing to wait for.
func (a *App) awaitConn() (*relay.Client, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		cl, err := a.conn()
		if err == nil {
			return cl, nil
		}
		if errors.Is(err, errNotRunning) || errors.Is(err, errClosed) || errors.Is(err, errNoReceiver) {
			return nil, err
		}
		if !time.Now().Before(deadline) {
			return nil, err
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (a *App) setConn(state string) {
	a.mu.Lock()
	changed := a.connState != state
	a.connState = state
	a.mu.Unlock()
	if changed {
		a.events.emit(&Event{Kind: "connection", State: state})
	}
}

func (a *App) setClient(cl *relay.Client) {
	a.mu.Lock()
	a.client = cl
	a.mu.Unlock()
}

// run is one Start..Stop generation: dial, drain, reconnect until the context is done.
func (a *App) run(ctx context.Context) {
	first := true
	for ctx.Err() == nil {
		if first {
			a.setConn("connecting")
			first = false
		} else {
			a.setConn("reconnecting")
			select {
			case <-ctx.Done():
			case <-time.After(reconnectDelay):
			}
			if ctx.Err() != nil {
				break
			}
		}
		cl, err := a.dial(ctx)
		if err != nil {
			continue
		}
		a.setClient(cl)
		a.setConn("online")
		a.onConnected(ctx, cl)
		a.drain(ctx, cl)
		a.setClient(nil)
		// PB-INPUT-2's FIRST enumerated severance event. A gateway restart kills the lease
		// while being unable to seal any notice about it -- the gateway is the thing that
		// died -- so the phone's own transport dropping is the ONLY signal that can exist,
		// and a disconnect must therefore SEVER rather than merely pause. Without this the
		// phone keeps reporting the pre-outage generation live and types against a lease the
		// new gateway does not hold. It also empties the coalescer, so bytes buffered when
		// the link went away resolve as undelivered instead of riding the reconnect.
		a.suspendInput("the connection to the machine was lost")
		_ = cl.Close()
	}
	a.setClient(nil)
	a.setConn("offline")
}

func (a *App) dial(ctx context.Context) (*relay.Client, error) {
	ks := a.core.KeyStore()
	return relay.Dial(ctx, a.relayURL, relay.ClientAuth{
		RelayAuthPub: ed25519.PublicKey(ks.RelayAuthPublic()),
		Sign:         ks.SignRelayAuth,
	})
}

// onConnected re-establishes the per-connection state the relay does not persist: the
// machine's authorization to append to this phone's mailbox, and the push token
// (PB-PUSH-9 requires re-registration on every authenticated reconnect).
func (a *App) onConnected(ctx context.Context, cl *relay.Client) {
	if _, pub := a.destination(); len(pub) == ed25519.PublicKeySize {
		_ = cl.AuthorizeDevice(ctx, pub)
	}
	if token := a.core.State().PushToken; token != "" {
		_ = cl.TokenRegister(ctx, token)
	}
}

// drain polls the mailbox and hands every item to the core, in order.
//
// The immediate next read is conditioned on PROGRESS -- the durable cursor moved -- and
// not on the page having been non-empty. The cursor advances only for a frame the core
// OPENED (phonecore commits it inside the receive transaction), so an item that cannot be
// opened is re-served by every subsequent read: one undecodable frame at the mailbox TAIL
// makes every page non-empty forever. Looping on a non-empty page would then spin at full
// speed on a battery-powered device and burn the relay's per-source ops budget until the
// connection dies -- an unbounded-work lever handed to the party the design treats as
// hostile (PB-SYNC-6), and reachable benignly by any frame that arrives before
// InstallContentKey. A real backlog still drains at full speed: it advances the cursor.
func (a *App) drain(ctx context.Context, cl *relay.Client) {
	for ctx.Err() == nil {
		cursor := a.core.State().RelayCursor
		items, err := cl.MailboxRead(ctx, cursor)
		if err != nil {
			return
		}
		for _, it := range items {
			a.accept(it.Envelope, it.Cursor)
		}
		a.flushAcks(ctx, cl)
		if a.core.State().RelayCursor > cursor {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pollInterval):
		}
	}
}

// accept runs the core's durable receive transaction for one envelope, then -- only for a
// frame the core ACCEPTED -- builds the app-facing read models and events from it.
func (a *App) accept(raw []byte, cursor uint64) {
	key := a.core.State().Keys.ContentKey
	receipt, err := a.core.Router().AcceptCommit(raw, cursor)
	if err != nil {
		return
	}
	v, ok := viewFrame(key, raw)
	if !ok {
		return
	}
	switch v.Kind {
	case "terminal_snapshot":
		a.markStream("terminal", receipt.Gap)
		a.events.emit(&Event{Kind: "terminal", Stream: "terminal", SessionID: v.Terminal.Session})
	case "command_reply":
		a.markStream("reply", receipt.Gap)
		a.onReply(v.Reply)
	case "reconcile":
		a.adoptReconcile()
	case "":
		a.markStream("journal", receipt.Gap)
		a.onJournal(v.Record)
	}
}

// adoptReconcile folds the machine's rollback authorities into every durable coordinate
// they cover and RECORDS THE ADOPTION durably. Without the durable record every Android
// process death would re-arm the fail-closed refusal of mutating ops, clearable only by a
// gateway reconnect the phone cannot trigger -- fail-closed turning into PB-STATE-10's
// brick. A record naming another machine or epoch is refused by the core and must be a
// NO-OP here: an adopted foreign authority is unrewindable.
func (a *App) adoptReconcile() {
	if err := a.core.Reconcile(); err != nil {
		return
	}
	st := a.core.State()
	if st.ReconciledEpoch != st.EpochID {
		st.ReconciledEpoch = st.EpochID
		if err := a.core.Save(st); err != nil {
			return
		}
	}
	a.mu.Lock()
	a.reconciled = true
	a.mu.Unlock()
	a.events.emit(&Event{Kind: "connection", Stream: "reconcile", State: "reconciled"})
}

func (a *App) onJournal(rec schema.JournalRecord) {
	entry := JournalEntry{
		Cursor:    int64(rec.Cursor),
		SessionID: rec.SessionID,
		Type:      rec.Type,
		Group:     string(rec.Group),
	}
	a.mu.Lock()
	a.journal = append(a.journal, entry)
	if len(a.journal) > journalLogSize {
		a.journal = a.journal[len(a.journal)-journalLogSize:]
	}
	if rec.SessionID != "" && rec.Type != "" {
		a.needs[rec.SessionID] = rec.Type
	}
	subscribed := a.subscribed
	a.mu.Unlock()
	if !subscribed {
		return
	}
	a.events.emit(&Event{
		Kind:      "journal",
		Stream:    "journal",
		SessionID: rec.SessionID,
		State:     string(rec.Group),
		Message:   rec.Type,
		Cursor:    entry.Cursor,
	})
}

func (a *App) onReply(ctrl schema.Control) {
	if ctrl.OperationID != "" {
		a.resolve(ctrl.OperationID)
	}
	a.mu.Lock()
	a.killSwitch = ctrl.ErrorCode == schema.CodeKillSwitch
	a.mu.Unlock()
	a.reportSkew()
	a.events.emit(&Event{
		Kind:      "outcome",
		Stream:    "reply",
		SessionID: ctrl.SessionID,
		State:     ctrl.Op,
		Message:   ctrl.OperationID,
	})
}

// reportSkew surfaces PB-TIME-1's verdict, which this reply may just have produced: the
// AAD-covered IssuedAt on a machine reply is the only authenticated machine time the phone
// ever sees, so a bracket can close nowhere else.
//
// It is a REPORT, not a gate. A phone two minutes out signs an ExpiresAt the daemon refuses,
// and the daemon's refusal reads "not authorized" -- which sends the user to re-pair when
// the fix is to correct their clock. Refusing the command locally instead would stop the
// command that re-measures, so the verdict could never clear once it went bad; the daemon
// stays the enforcement and this is the explanation. Only a CHANGE is emitted, or a
// two-minute-slow phone would raise an event per reply for the life of the session.
//
// THE CHANGE IS THE VERDICT, NOT ITS WORDING. Every reply closes a fresh bracket out of two
// wall-clock reads around a network round trip, so one CONSTANT skew measures a slightly
// different offset each time and skew.go renders it at full time.Duration precision.
// Comparing the rendered message therefore sees a change on every single reply -- a dedupe
// that can never dedupe, producing exactly the per-reply spam this guard exists to stop. The
// user's fact is binary (the clock is out of budget, or it is not) and so is the key. The
// verdict is not latched: correcting the clock clears it and a later relapse reports again.
func (a *App) reportSkew() {
	msg := ""
	if err := a.core.SkewMonitor().Check(); err != nil {
		msg = err.Error()
	}
	a.mu.Lock()
	changed := a.skewed != (msg != "")
	a.skewed = msg != ""
	a.mu.Unlock()
	if !changed || msg == "" {
		return
	}
	a.events.emit(&Event{Kind: "clock", Stream: "clock", State: "skewed", Message: msg})
}
