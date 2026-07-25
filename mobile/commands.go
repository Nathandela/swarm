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
	return a.sealSignedCommand(opTakeControlEnd, session, nil, nil)
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
func (a *App) SendInput(session string, data []byte) (err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return err
	}
	sc, err := a.sendContext()
	if err != nil {
		return err
	}
	seq, err := core.Seq().NextInput()
	if err != nil {
		return err
	}
	env, err := phonecore.SealInputData(sc.key, sc.epoch, seq, session, data)
	if err != nil {
		return err
	}
	_, err = sc.cl.MailboxAppend(context.Background(), sc.target, env)
	return err
}

// Resize sends a terminal resize on the live control lease.
func (a *App) Resize(session string, cols, rows int) (err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return err
	}
	sc, err := a.sendContext()
	if err != nil {
		return err
	}
	seq, err := core.Seq().NextInput()
	if err != nil {
		return err
	}
	env, err := phonecore.SealInputResize(sc.key, sc.epoch, seq, session, cols, rows)
	if err != nil {
		return err
	}
	_, err = sc.cl.MailboxAppend(context.Background(), sc.target, env)
	return err
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
	cl, err := a.awaitConn()
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
	sc, err := a.sendContext()
	if err != nil {
		return nil, err
	}
	id, err := newOperationID()
	if err != nil {
		return nil, err
	}
	cmd, err := phonecore.SignCommand(core.KeyStore(), phonecore.CommandInput{
		Action:      action,
		Machine:     core.State().Machine,
		Session:     session,
		OperationID: id,
		ExpiresAt:   time.Now().Add(commandTTL),
		ContentHash: contentHash,
	})
	if err != nil {
		return nil, err
	}
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
	if _, err := sc.cl.MailboxAppend(context.Background(), sc.target, env); err != nil {
		return nil, err
	}
	return &Op{Action: action, SessionID: session, OperationID: id}, nil
}

// unsignedCommand seals a READ command (terminal_watch / terminal_unwatch): the action and
// its target only, with no device signature, because the gateway serves it itself.
func (a *App) unsignedCommand(action, session string) (*Op, error) {
	core, err := a.ready()
	if err != nil {
		return nil, err
	}
	sc, err := a.sendContext()
	if err != nil {
		return nil, err
	}
	seq, err := core.Seq().NextCommand()
	if err != nil {
		return nil, err
	}
	env, err := phonecore.SealCommandEnvelope(sc.key, sc.epoch, seq, schema.DeviceCommandAuth{
		Action:  action,
		Machine: core.State().Machine,
		Session: session,
	})
	if err != nil {
		return nil, err
	}
	if _, err := sc.cl.MailboxAppend(context.Background(), sc.target, env); err != nil {
		return nil, err
	}
	return &Op{Action: action, SessionID: session}, nil
}
