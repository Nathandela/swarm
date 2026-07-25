package swarmmobile

// The phone -> machine plane: signed mutating commands, unsigned read commands, and the
// live input frames.
//
// Two rules shape every function here and neither is negotiable:
//
//   - A COMMAND draws its seq from Sequencer.NextCommand and an INPUT frame from
//     Sequencer.NextInput. Both are durable reservations (PB-STATE-3); the plain
//     in-memory allocator is never used, because a seq issued without a durable
//     reservation is reused after one process death and the gateway then stale-drops
//     every keystroke, take_control, launch and kill permanently. NextInput additionally
//     REFUSES while a burned reservation block is outstanding: the gateway drops a gapped
//     input frame silently, so the re-lease command must absorb the gap first
//     (PB-STATE-8), and the facade enforces that rather than the caller.
//
//   - A MUTATING op is refused until the machine's rollback authorities have been adopted
//     (PB-SYNC-7). Reads -- roster, journal, peek, terminal_watch -- are never gated: a
//     phone that shows nothing is indistinguishable from a dead one.

import (
	"context"
	"errors"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// TakeControl acquires the live control lease for a session (PB-INPUT-3). It is also the
// COMMAND frame that absorbs a burned seq-reservation block after a restart, which is why
// the app must re-lease before typing.
func (a *App) TakeControl(session string) (op *Op, err error) {
	defer barrier(&err)
	return a.signedCommand(schema.ActionTakeControl, session, nil, nil)
}

// opTakeControlEnd is the lease teardown's wire action. take_control_end has no signed
// Action constant: the gateway routes it on the LEASE plane by the daemon op string
// (protocol.OpTakeControlEnd -> Leases.End) and never forwards it, so there is no action
// for the daemon's capability switch to class. This package cannot name that constant --
// PB-BIND-0 keeps internal/protocol out of the bound closure, because it wraps the daemon
// and would drag the PTY and the VT emulator onto the handset -- so the string is pinned
// against protocol.OpTakeControlEnd by the conformance suite instead.
const opTakeControlEnd = "take_control_end"

// ReleaseControl ends the control lease.
//
// The verb is take_control_end and nothing else. Any action the gateway FORWARDS is a
// daemon mutation, and delete above all: it is control-class (skeleton/deviceauth.go), so
// the very tier that can take control at all is authorized for it, and a release button
// that sealed one would destroy the session the user was merely stepping away from.
//
// The gateway seals no reply for a lease teardown, so the op is NOT tracked in flight: a
// pending count that only rises makes every real pending op invisible.
func (a *App) ReleaseControl(session string) (op *Op, err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return nil, err
	}
	// PB-INPUT-6: a release is a BOUNDARY, so what is still buffered is flushed while the
	// lease is still live. Dropping it would lose keystrokes the user watched appear, and
	// holding it would queue a keystroke for a lease that no longer exists (ADR-007 D7).
	// Live-only, like every other send of these bytes: with no connection the flush resolves
	// them as undelivered rather than waiting for one to come back.
	if sc, serr := a.liveSendContext(); serr == nil {
		_ = a.sendCoalesced(sc, core, a.coalesce.Flush())
	} else {
		for _, u := range a.coalesce.Abandon(serr.Error()) {
			a.emitUndelivered(u)
		}
	}
	op, err = a.sealSignedCommand(opTakeControlEnd, session, nil, nil)
	if err != nil {
		return nil, err
	}
	core.Leases().Sever(session, "control was released")
	return op, nil
}

// Kill terminates a session. PB-APP-3's persistent Stop maps here.
func (a *App) Kill(session string) (op *Op, err error) {
	defer barrier(&err)
	return a.signedCommand(schema.ActionKill, session, nil, nil)
}

// Launch starts a session on the machine (PB-APP-6).
//
// The signed tuple names no session -- a launch has none yet -- so WHAT is launched is
// bound by ContentHash = the CANONICAL launch content hash, computed by the one
// implementation both the signer and the daemon's verifier use. Re-deriving that
// length-prefixed encoding here is forbidden: a one-byte divergence yields signature
// verification failures with no compile error and no daemon error until a real launch is
// refused.
func (a *App) Launch(spec *LaunchSpec) (op *Op, err error) {
	defer barrier(&err)
	if spec == nil {
		return nil, errors.New("swarmmobile: Launch requires a LaunchSpec")
	}
	req := &schema.LaunchReq{
		Agent:         spec.Agent,
		Cwd:           spec.Cwd,
		InitialPrompt: spec.Prompt,
		Options:       parseOptions(spec.Options),
	}
	return a.signedCommand(schema.ActionLaunch, schema.LaunchSessionSentinel,
		schema.LaunchContentHash(req), req)
}

// RevokeThisDevice revokes this phone's own pairing (PB-SEC-7). It is the phone's panic
// action: the kill switch is owner-tier only and this surface can never set it.
//
// GAP, recorded in screen_coverage.tsv: device_revoke IS in the signed action set and the
// daemon serves it, but the gateway's action->op mapping does not carry it, so the
// correctly-signed command is refused "unsupported command action" one hop short of the
// daemon -- and a refused action produces NO reply, so the op would never resolve.
// Sealing it anyway would burn a durable send-seq and hand the panic action back as a
// success that then hangs forever, which for THIS button is the worst possible shape. So
// nothing is sealed and the refusal is recorded durably, exactly as Interrupt does. The
// mapping is owed by the gateway slice.
func (a *App) RevokeThisDevice() (op *Op, err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return nil, err
	}
	// The target device id sits in the session position of the signed tuple, so a
	// self-revoke names this phone; the recorded refusal keeps naming it.
	return a.refuse(schema.ActionDeviceRevoke, a.deviceID(),
		"device_revoke has no gateway action->op mapping; the correctly-signed command is refused "+
			"one hop short of the daemon, and the verb is owed by the gateway slice")
}

// Interrupt asks the machine to interrupt a session's agent (PB-APP-3).
//
// GAP, recorded in screen_coverage.tsv: there is NO interrupt action in the signed set,
// and inventing a wire string here would produce a command every daemon refuses while
// looking, to the app, exactly like one that was delivered. So nothing is sealed: the op
// is created and IMMEDIATELY RESOLVED against a durable, legible local refusal, which is
// what makes the missing verb visible on the screen instead of a Stop button that hangs
// forever. S8 owns this surface; the verb is a cross-slice split.
func (a *App) Interrupt(session string) (op *Op, err error) {
	defer barrier(&err)
	if _, err = a.ready(); err != nil {
		return nil, err
	}
	if err = a.requireReconciled(); err != nil {
		return nil, err
	}
	return a.refuse("interrupt", session,
		"interrupt has no signed wire action; the verb is owed by another slice")
}

// TerminalWatch opens the server-rendered terminal peek for a session (PB-APP-4). It is a
// READ: the phone seals an UNSIGNED command carrying only the action and the target, and
// the gateway routes it to its terminal watcher without forwarding it to the daemon's
// device authenticator. The daemon still gates the peek itself.
func (a *App) TerminalWatch(session string) (err error) {
	defer barrier(&err)
	_, err = a.unsignedCommand(schema.ActionTerminalWatch, session)
	return err
}

// TerminalUnwatch closes the peek. Without it the peek plane leaks per-session server
// render work for every screen the user ever opened.
func (a *App) TerminalUnwatch(session string) (err error) {
	defer barrier(&err)
	_, err = a.unsignedCommand(schema.ActionTerminalUnwatch, session)
	return err
}

// SendInput sends a keystroke burst on the live control lease (PB-APP-4 / PB-NET-4). It
// is LIVE ONLY: never queued, never replayed. The target session is bound INSIDE the
// sealed frame, so the machine routes by it and never by mutable focus state.
//
// It is GATED on a CONFIRMED lease generation (PB-INPUT-2) and PACED through the coalescer
// (PB-INPUT-5). Ungated, the phone types happily at a machine that granted it nothing and
// the gateway drops every frame silently, so the user sees a live keyboard and a dead
// terminal. Unpaced, one relay append per keystroke is 30/s at autorepeat against the
// relay's 600-per-minute window, so the lease dies with codeQuotaExceeded after roughly
// twenty seconds of held-down key -- while every short-burst latency test still passes.
func (a *App) SendInput(session string, data []byte) (err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return err
	}
	// Refused BEFORE the bytes are buffered: accepting them would leave keystrokes held for
	// a lease that does not exist, which is the queue ADR-007 D7 forbids.
	if err = core.Leases().Require(session, time.Now()); err != nil {
		return a.refuseInput(session, data, err)
	}
	sc, err := a.liveSendContext()
	if err != nil {
		return a.refuseInput(session, data, err)
	}
	err = a.sendCoalesced(sc, core, a.coalesce.Type(session, data))
	a.scheduleDrain()
	return err
}

// Paste delivers a clipboard paste or an IME COMMIT as ONE unit (PB-INPUT-6). It is not a
// keystroke stream and is deliberately not subject to the 125 ms coalescing window, which
// exists to pace autorepeat: holding a paste for a window buys nothing (it is already one
// event) and costs a window of visible latency, with the user watching the tail of their
// paste disappear. Buffered keystrokes are flushed FIRST, so a paste can never overtake
// characters typed before it, and an oversize unit is split at the 4 KiB frame cap and
// nowhere else.
//
// The parameter is TEXT, not bytes: a clipboard and an IME both hand Android a String, and
// PB-BIND-4 keeps []byte crossings to the enumerated few. An IME PREEDIT is never sent and
// has no entry point here -- that is the decision, not an omission: a preedit is local until
// the IME commits, at which point it arrives through this method. Sending preedit text would
// type-then-correct against a live shell.
func (a *App) Paste(session string, text string) (err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return err
	}
	data := []byte(text)
	if err = core.Leases().Require(session, time.Now()); err != nil {
		return a.refuseInput(session, data, err)
	}
	sc, err := a.liveSendContext()
	if err != nil {
		return a.refuseInput(session, data, err)
	}
	return a.sendCoalesced(sc, core, a.coalesce.Insert(session, data))
}

// refuseInput resolves input the phone accepted from the user but will not send, as an
// explicit undelivered record rather than a silent drop (PB-INPUT-1). The bytes never enter
// the coalescer's buffer: buffering input for a lease or a link that is gone is the queue
// ADR-007 D7 makes structurally impossible.
func (a *App) refuseInput(session string, data []byte, cause error) error {
	a.coalesce.Fail(phonecore.InputFrame{T: "data", Session: session, Data: data}, cause.Error())
	a.events.emit(&Event{
		Kind: "input", Stream: "input", SessionID: session,
		State: "undelivered", Message: cause.Error(), Cursor: int64(len(data)),
	})
	return cause
}

// Resize sends a terminal resize on the live control lease. It is gated on the same
// confirmed lease as typing, and it FLUSHES the buffered keystrokes first (PB-INPUT-6): a
// resize that overtook them would re-wrap the line and land the bytes against a grid the
// user was not looking at when they typed them.
func (a *App) Resize(session string, cols, rows int) (err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return err
	}
	if err = core.Leases().Require(session, time.Now()); err != nil {
		return err
	}
	sc, err := a.liveSendContext()
	if err != nil {
		return err
	}
	return a.sendCoalesced(sc, core, a.coalesce.Resize(session, cols, rows))
}

// sendCoalesced seals and appends the frames the coalescer released, IN ORDER. A frame whose
// send fails is recorded on the undelivered ledger and never handed back: re-buffering a
// failed live frame turns the live-only path into a queue, one frame at a time.
func (a *App) sendCoalesced(sc sendCtx, core *phonecore.Core, frames []phonecore.InputFrame) error {
	var errs []error
	for _, f := range frames {
		if err := a.sendInputFrame(sc, core, f); err != nil {
			a.coalesce.Fail(f, err.Error())
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// sendInputFrame seals ONE coalesced frame and appends it. The lease is re-checked here
// rather than only at the caller, because a frame the window held for 125 ms can outlive the
// lease that authorized it -- and PB-INPUT-2's rule is about the moment of the SEND.
//
// THE WHOLE allocate -> append RUNS UNDER a.bucketMu, and it must, for the reason
// remotegw.CommandBridge.sealReply states for the gateway's reply bucket.
//
// THE LOCK IS THE BUCKET'S, NOT THIS FUNCTION'S, and getting that wrong is what let the
// inversion survive a remediation round. Every producer that draws from the phone's one
// Sequencer is on this stream: the caller's goroutine typing (SendInput, Paste, Resize;
// PB-BIND-6 makes concurrent facade calls part of the contract), drainHeldInput on
// time.AfterFunc's goroutine, AND every command author -- sealSignedCommand and
// unsignedCommand take the same lock, because phonecore.Sequencer hands commands and input
// frames seq numbers from ONE counter per epoch (they share a single MailboxReceiver key, so
// a private per-kind counter would collide). Releasing the sequencer before entering the
// append lets a LATER seq reach the relay first.
//
// The machine has ONE crypto.MailboxReceiver for this stream, and what an inversion costs
// depends on which kinds collided:
//
//   - two INPUT frames: BOTH die. The high one is marked Gap and routeInput drops it
//     silently; the low one is refused with crypto.ErrStaleSeq.
//   - a COMMAND anywhere in the pair: routeCommand executes a Gap frame, so only the LOW
//     frame dies -- and when that is the command, a take_control never confirms or a kill
//     never runs, with the op pending forever.
//
// MailboxAppend returned nil for each, so the undelivered ledger records nothing and the loss
// is invisible from both ends -- the silent drop PB-INPUT-1 forbids.
//
// relay.Conn.roundtrip ALREADY holds its own c.mu across write-then-read, so appends on one
// connection were serialised before this lock existed; what was missing is that the seq was
// allocated OUTSIDE that critical section. This lock therefore costs one AEAD seal inside an
// interval the path was already spending, not a new queue -- which matters, because unlike
// replies this is the hot path (PB-NET-5's 150 ms budget). Relying on roundtrip's mutex for
// ORDER would be relying on an accident of Go's mutex starvation mode; it is cited here only
// as the reason the cost is nil.
func (a *App) sendInputFrame(sc sendCtx, core *phonecore.Core, f phonecore.InputFrame) error {
	if err := core.Leases().Require(f.Session, time.Now()); err != nil {
		return err
	}
	a.bucketMu.Lock()
	defer a.bucketMu.Unlock()
	seq, err := core.Seq().NextInput()
	if err != nil {
		return err
	}
	var env []byte
	if f.T == "resize" {
		env, err = phonecore.SealInputResize(sc.key, sc.epoch, seq, f.Session, f.Cols, f.Rows)
	} else {
		env, err = phonecore.SealInputData(sc.key, sc.epoch, seq, f.Session, f.Data)
	}
	if err != nil {
		return err
	}
	_, err = sc.cl.MailboxAppend(context.Background(), sc.target, env)
	return err
}

// scheduleDrain arms the one-shot release of whatever the coalescer is still holding.
// Without it the TAIL of a burst -- the keystrokes buffered inside the last 125 ms window --
// would sit there until the user typed again, so a fast "ls\r" would leave the shell waiting
// on a carriage return that was never sent. A user who keeps typing closes the window
// themselves, so this timer only ever fires for the tail.
func (a *App) scheduleDrain() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.sess == nil {
		return
	}
	if a.drainTimer == nil {
		a.drainTimer = time.AfterFunc(phonecore.InputFrameInterval, a.drainHeldInput)
		return
	}
	a.drainTimer.Reset(phonecore.InputFrameInterval)
}

// drainHeldInput sends what the elapsed window released. Held bytes that can no longer be
// sent -- the link went away, the lease died -- resolve as an explicit undelivered entry
// through sendCoalesced, never as a silent drop (PB-INPUT-1).
func (a *App) drainHeldInput() {
	core, err := a.ready()
	if err != nil {
		return
	}
	frames := a.coalesce.Due()
	if len(frames) == 0 {
		return
	}
	sc, err := a.liveSendContext()
	if err != nil {
		for _, f := range frames {
			a.coalesce.Fail(f, err.Error())
		}
		return
	}
	_ = a.sendCoalesced(sc, core, frames)
	a.scheduleDrain() // bytes may have arrived inside the window that just closed
}

// ---- internals -----------------------------------------------------------------

func (a *App) requireReconciled() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.reconciled {
		return nil
	}
	return errUnreconciled
}

// sendContext resolves everything a phone -> machine append needs, failing LOUDLY on each
// missing coordinate rather than sealing into nowhere. The destination check is the one
// requirements 4.3 is about one field lower: a phone with a valid content key, a valid
// send-seq and no machine routing id would otherwise seal every frame and deliver none,
// with nothing failing.
func (a *App) sendContext() (sendCtx, error) {
	return a.resolveSend(a.awaitConn)
}

// liveSendContext is sendContext for the LIVE-ONLY plane (input and resize). It differs in
// exactly one thing and the difference is the requirement: it takes the connection as it
// stands instead of waiting for one.
//
// awaitConn polls for up to five seconds so a command issued right after Start is not
// refused by a race the caller cannot see. That is right for a command -- idempotent, queued
// by design -- and exactly wrong for a keystroke: waiting means the byte is appended to the
// RECONNECTED link and reaches the machine seconds later, against a terminal state the user
// has since changed and long after they gave up on it. ADR-007 D7 makes that structurally
// impossible, so input fails immediately and resolves as an undelivered record instead.
func (a *App) liveSendContext() (sendCtx, error) {
	return a.resolveSend(a.conn)
}

// resolveSend is the shared destination lookup; conn supplies the connection policy.
func (a *App) resolveSend(conn func() (*relay.Client, error)) (sendCtx, error) {
	core, err := a.ready()
	if err != nil {
		return sendCtx{}, err
	}
	target, _ := a.destination()
	if target == "" {
		return sendCtx{}, errNoDestination
	}
	st := core.State()
	if st.Keys.ContentKey == (crypto.ContentKey{}) {
		return sendCtx{}, errNoContentKey
	}
	cl, err := conn()
	if err != nil {
		return sendCtx{}, err
	}
	return sendCtx{cl: cl, target: target, key: st.Keys.ContentKey, epoch: st.EpochID}, nil
}

// sendCtx is everything one phone -> machine append needs, resolved together.
type sendCtx struct {
	cl     *relay.Client
	target string
	key    crypto.ContentKey
	epoch  uint32
}

// refuse records a durable, legible refusal for a surface whose wire verb no hop can
// resolve today, and returns the already-resolved op.
//
// It is the shape every such surface must take. Sealing the command instead would burn a
// durable send-seq on a frame that is dropped one hop later, and issuing the op would
// raise PendingOpCount for the life of the process -- so the screen would show a button
// that hangs forever, and every genuinely in-flight op would be hidden behind the ones
// that can never land.
func (a *App) refuse(action, session, reason string) (*Op, error) {
	core, err := a.ready()
	if err != nil {
		return nil, err
	}
	id, err := newOperationID()
	if err != nil {
		return nil, err
	}
	if err := core.RecordOutcome(schema.Control{
		Op:          "error",
		EndpointID:  core.State().Machine,
		SessionID:   session,
		OperationID: id,
		Error:       reason,
	}); err != nil {
		return nil, err
	}
	return &Op{Action: action, SessionID: session, OperationID: id}, nil
}

// signedCommand seals one mutating command and tracks it IN FLIGHT, because the gateway
// answers it: a forwarded action carries the daemon's reply back, and take_control its
// lease confirmation. A command the gateway answers with nothing must use
// sealSignedCommand directly.
func (a *App) signedCommand(action, session string, contentHash []byte, launch *schema.LaunchReq) (*Op, error) {
	op, err := a.sealSignedCommand(action, session, contentHash, launch)
	if err != nil {
		return nil, err
	}
	a.issue(op)
	return op, nil
}

// sealSignedCommand authors, signs, seals and appends one mutating command.
func (a *App) sealSignedCommand(action, session string, contentHash []byte, launch *schema.LaunchReq) (*Op, error) {
	core, err := a.ready()
	if err != nil {
		return nil, err
	}
	if err := a.requireReconciled(); err != nil {
		return nil, err
	}
	// THE SKEW VERDICT DOES NOT GATE THIS CALL, and that is a decision (PB-TIME-1 with
	// PB-STATE-10). The only authenticated machine time rides a REPLY, and a reply only
	// exists in answer to a command -- so a local refusal stops the very command that would
	// have re-measured the clock, and the refusal outlives the broken clock it was
	// reporting: the user fixes the clock exactly as the error told them to and every op
	// stays refused until the process restarts. Letting one command through per measurement
	// is the same defect rearranged, since the machine answers every command and re-arms the
	// probe. So the phone EXPLAINS -- onReply reports the verdict on the event plane the
	// moment a reply produces it -- and the daemon's own ExpiresAt check ENFORCES, which is
	// the split s11_skew_test.go already states for a phone that has never measured.
	sc, err := a.sendContext()
	if err != nil {
		return nil, err
	}
	id, err := newOperationID()
	if err != nil {
		return nil, err
	}
	// §6.0 sets the signed horizon BY OP CLASS: 1 minute for ordinary commands, 15 for
	// take_control. The daemon's lease deadline is the EARLIEST of this ExpiresAt,
	// now+TTLSeconds and a 30-minute cap, so one flat short TTL makes the SIGNATURE the
	// thing that ends a typing session.
	expiresAt := time.Now().Add(phonecore.CommandTTLFor(action))
	cmd, err := phonecore.SignCommand(core.KeyStore(), phonecore.CommandInput{
		Action:      action,
		Machine:     core.State().Machine,
		Session:     session,
		OperationID: id,
		ExpiresAt:   expiresAt,
		ContentHash: contentHash,
	})
	if err != nil {
		return nil, err
	}
	// A COMMAND IS A PRODUCER ON THE SAME BUCKET AS THE KEYSTROKES, so allocate -> seal ->
	// append is spanned
	// here exactly as it is in sendInputFrame: one Sequencer per epoch numbers both kinds, and
	// a command whose seq was drawn outside this section is overtaken by every keystroke
	// numbered after it. What dies is then the COMMAND -- the machine executes the Gap frame
	// that overtook it and refuses this one as stale -- so the op is never answered and the
	// phone shows it in flight forever. Held from the draw rather than from the top of the
	// function: everything above resolves the destination, and a.sendContext may wait five
	// seconds for a connection.
	a.bucketMu.Lock()
	defer a.bucketMu.Unlock()
	seq, err := core.Seq().NextCommand()
	if err != nil {
		return nil, err
	}
	var env []byte
	if launch != nil {
		env, err = phonecore.SealLaunchEnvelope(sc.key, sc.epoch, seq, cmd, launch)
	} else {
		env, err = phonecore.SealCommandEnvelope(sc.key, sc.epoch, seq, cmd)
	}
	if err != nil {
		return nil, err
	}
	// PB-TIME-3: T1 of the skew bracket. It can only be recorded here, where the operation
	// id is minted, and as close to the append as possible -- without it every machine stamp
	// arrives uncorrelated, the monitor ignores it by design, and the phone can never
	// measure skew at all.
	core.SkewMonitor().Sent(id)
	if action == schema.ActionTakeControl {
		// PB-INPUT-2: authoring a take_control is NOT a lease, and Require still refuses
		// until the machine confirms one. What is recorded here is the horizon the phone
		// SIGNED (the fallback expiry for a grant carrying none of its own) and the id the
		// grant will answer, which is what identifies the lease across a daemon restart.
		//
		// BEFORE the append, not after: the inbound drain runs on its own goroutine, so a
		// grant that arrived while this one was still in MailboxAppend would find no request
		// recorded, fall back to the generation floor, and be discarded -- which is exactly
		// the dead keyboard the floor was making. Recording a request whose append then fails
		// costs nothing: the operation id is freshly minted, so no reply can ever name it.
		core.Leases().Requested(session, id, expiresAt)
	}
	if _, err := sc.cl.MailboxAppend(context.Background(), sc.target, env); err != nil {
		return nil, err
	}
	return &Op{Action: action, SessionID: session, OperationID: id}, nil
}

// unsignedResync seals the journal repair request (PB-SYNC-2). It is a READ, unsigned like
// the two watches -- PB-SYNC-5's decision, and the gateway that performs the journal_read
// holds no device key with which to satisfy a signature gate anyway.
//
// It carries the phone's own cache cursor so the machine's journal_read answers with the
// events after it rather than every event ever journalled: JournalResume's
// {Roster, Events, Cursor} is exactly the reseed's three fields.
func (a *App) unsignedResync(from uint64) (*Op, error) {
	return a.unsignedCommandAt(schema.ActionJournalResync, "", from)
}

// unsignedCommand seals a READ command (terminal_watch / terminal_unwatch): the action and
// its target only, with no device signature, because the gateway serves it itself.
func (a *App) unsignedCommand(action, session string) (*Op, error) {
	return a.unsignedCommandAt(action, session, 0)
}

// unsignedCommandAt is unsignedCommand with journal_resync's from-cursor. It is one function
// because every unsigned read draws from the SAME Sequencer and must take the same bucket
// lock; a second copy of this body is a second place for that rule to be forgotten.
func (a *App) unsignedCommandAt(action, session string, resyncCursor uint64) (*Op, error) {
	core, err := a.ready()
	if err != nil {
		return nil, err
	}
	sc, err := a.sendContext()
	if err != nil {
		return nil, err
	}
	// The same bucket and the same rule as sealSignedCommand: a read command is numbered from
	// the one Sequencer the keystrokes draw from, so its allocate -> seal -> append is spanned
	// too. A terminal_watch lost to an inversion leaves a peek that never opens.
	a.bucketMu.Lock()
	defer a.bucketMu.Unlock()
	seq, err := core.Seq().NextCommand()
	if err != nil {
		return nil, err
	}
	auth := schema.DeviceCommandAuth{Action: action, Machine: core.State().Machine, Session: session}
	var env []byte
	if action == schema.ActionJournalResync {
		env, err = phonecore.SealResyncEnvelope(sc.key, sc.epoch, seq, auth, resyncCursor)
	} else {
		env, err = phonecore.SealCommandEnvelope(sc.key, sc.epoch, seq, auth)
	}
	if err != nil {
		return nil, err
	}
	if _, err := sc.cl.MailboxAppend(context.Background(), sc.target, env); err != nil {
		return nil, err
	}
	return &Op{Action: action, SessionID: session}, nil
}
