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
	"github.com/Nathandela/swarm/internal/remote/crypto"
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
		return nil, classed(ErrClassOffline, errors.New("swarmmobile: relay connection not established yet"))
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

// The connection states App.ConnectionState reports, named once so the transport loop and
// the Android side cannot disagree about a literal.
//
// The last two are PB-KEY-6's: a custody refusal is not a transport condition and must not be
// reported as one. connReauthRequired means "prompt for the biometric and it will connect";
// connRepairRequired means "the key is gone" and is TERMINAL -- the loop stops, because
// retrying a destroyed key forever while showing a spinner is the failure this pair exists to
// remove.
const (
	connOffline        = "offline"
	connConnecting     = "connecting"
	connOnline         = "online"
	connReconnecting   = "reconnecting"
	connReauthRequired = "reauth_required"
	connRepairRequired = "repair_required"

	// connRevoked is PB-APP-10's seventh state and it is NOT a custody condition.
	//
	// relay.ErrRevoked comes back from the RELAY HANDSHAKE, so it matches neither crypto
	// sentinel and used to fall through the dial switch's bare `continue`: the phone redialled
	// every reconnectDelay for the life of the process behind a "reconnecting" spinner, which
	// is the failure LOOP the requirement forbids in as many words -- reached by the owner
	// doing exactly what the product tells them to do when a handset is lost.
	//
	// It is TERMINAL for the same reason connRepairRequired is: nothing on this device can
	// un-revoke itself, so every retry is a websocket handshake spent re-proving that, on a
	// battery, against the relay's per-source budget.
	//
	// It is kept apart from connRepairRequired although the two share a remedy, because they
	// do not share a cause: repair_required means this handset's Keystore key is gone, revoked
	// means the OWNER removed it -- and the machine-side registration is what the owner has to
	// clear before a re-pair can succeed.
	connRevoked = "revoked"
)

func (a *App) setConn(state string) {
	a.mu.Lock()
	changed := a.connState != state
	a.connState = state
	a.mu.Unlock()
	if changed {
		a.events.emit(&Event{Kind: "connection", State: state})
	}
}

// currentConn reads the state without the ready()/barrier wrapping ConnectionState carries:
// it is consulted from inside the transport loop, which is not an entry point.
func (a *App) currentConn() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.connState
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
			a.setConn(connConnecting)
			first = false
		} else {
			// A custody refusal is NOT a transport problem, so it must not be overwritten
			// by "reconnecting": the user has to be told that authenticating is what fixes
			// this, and a spinner tells them the opposite. The state therefore persists
			// across the retry, and the next successful dial clears it by setting "online".
			if a.currentConn() != connReauthRequired {
				a.setConn(connReconnecting)
			}
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
			// PB-KEY-6, at the one production call site of relay.ClientAuth.Sign that can
			// refuse. This error used to be discarded with a bare `continue`, which was
			// unreachable while the app ran on the software keystore and went LIVE the
			// moment PB-KEY-9's Keystore-backed KEK landed: a recoverable refusal became an
			// endless "reconnecting" with no prompt, and a permanent one the same loop
			// against a key that no longer exists.
			switch {
			case errors.Is(err, crypto.ErrKeyInvalidated):
				// PERMANENT and therefore TERMINAL. The relay-auth key is destroyed;
				// nothing on-device recovers it and every retry is a round trip spent
				// proving that again. Returning here rather than breaking is deliberate --
				// break would fall through to setConn("offline") and erase the one state
				// that tells the user to pair again.
				a.setConn(connRepairRequired)
				a.setClient(nil)
				return
			case errors.Is(err, crypto.ErrKeyAuthRequired):
				// RECOVERABLE. Keep retrying -- the biometric may be satisfied at any
				// moment, and the retry is what notices -- but say what is actually wrong.
				a.setConn(connReauthRequired)
			case errors.Is(err, relay.ErrRevoked):
				// PB-APP-10. The THIRD identity this switch has to distinguish, and the one
				// the fix for the first two left behind with an identical shape: a bare
				// `continue` here is an unbounded reconnect the user is shown as a spinner.
				// Returning rather than breaking, for the same reason as the arm above --
				// break falls through to setConn("offline") and erases the one state that
				// tells the user what happened.
				a.setConn(connRevoked)
				a.setClient(nil)
				return
			}
			continue
		}
		a.setClient(cl)
		a.setConn(connOnline)
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
	a.setConn(connOffline)
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
//
// IT NO LONGER ATTRIBUTES THE GAP, and that is PB-SYNC-1. This used to mark the stream of
// the frame it happened to be holding -- a hole seen while decoding a terminal snapshot
// staled "terminal" -- but journal and terminal share ONE (sender, epoch) seq space and
// crypto.MailboxResult carries a bare Gap bool with no frame kind, so the skipped seq may
// just as well have been the journal record saying a session exited. The conservative
// per-bucket mark and the per-channel clear both live in the core now, inside the same
// durable transaction that moves the watermark (PB-SYNC-3), and StreamState reads them from
// there.
func (a *App) accept(raw []byte, cursor uint64) {
	key := a.core.State().Keys.ContentKey
	if _, err := a.core.Router().AcceptCommit(raw, cursor); err != nil {
		return
	}
	v, ok := viewFrame(key, raw)
	if !ok {
		return
	}
	switch v.Kind {
	case "terminal_snapshot":
		a.events.emit(&Event{Kind: "terminal", Stream: "terminal", SessionID: v.Terminal.Session})
	case "command_reply":
		a.onReply(v.Reply)
	case "reconcile":
		a.adoptReconcile()
	case "journal_reseed":
		// The repair landed and the core has already replaced the session model with it. The
		// facade's own journal PAGE is deliberately not rewritten: it is a log of events, and
		// a reseed is a set, so folding one in would invent entries the machine never
		// journalled. The event tells a screen to re-read the roster.
		a.events.emit(&Event{Kind: "journal", Stream: "journal", State: "resynced"})
	case "":
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
// It also maintains the PULL surface App.ClockVerdict reads, and it EMITS ON BOTH
// TRANSITIONS. The `msg == ""` early return that used to sit here meant nothing was raised
// when the verdict went back to healthy, so a UI that latched the first event went on telling
// a user with a correct clock to fix their clock -- the same latch S11's round-1 fix removed
// from the command path, re-created one layer up. A screen that is already open never calls a
// pull surface, so the clearing event is what reaches it.
func (a *App) reportSkew() {
	msg := ""
	if err := a.core.SkewMonitor().Check(); err != nil {
		msg = err.Error()
	}
	a.mu.Lock()
	changed := a.skewed != (msg != "")
	a.skewed = msg != ""
	a.clockVerdict = msg
	a.mu.Unlock()
	if !changed {
		return
	}
	if msg == "" {
		a.events.emit(&Event{Kind: "clock", Stream: "clock", State: "healthy"})
		return
	}
	a.events.emit(&Event{Kind: "clock", Stream: "clock", State: "skewed", Message: msg})
}
