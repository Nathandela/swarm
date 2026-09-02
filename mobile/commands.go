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
//     phone that shows nothing is indistinguishable from a dead one. device_revoke is the
//     one mutating EXEMPTION (PB-STATE-4, amended 2026-07-26): it selects no target from
//     synchronized state and only removes capability, and an unreconciled phone is the lost
//     handset the panic button exists for. sealSignedCommand carries the reasoning.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/remote/crypto"
	"github.com/Nathandela/swarm/internal/remote/relay"
)

// TakeControl acquires the live control lease for a session (PB-INPUT-3). It is also the
// COMMAND frame that absorbs a burned seq-reservation block after a restart, which is why
// the app must re-lease before typing.
//
// IT MINTS THE ONE-SHOT GATE TOKEN the daemon requires. handleTakeControl refuses an empty
// one BEFORE authorization, and deliberately does not settle for a hash check, because
// SHA256("") is a valid 32-byte hash -- so a take_control sealed through the ordinary
// command path is refused at the machine and the phone can never take control or type. The
// token is bound into the signature (ContentHash = SHA256(token)) and carried on the wire,
// so the daemon recomputes the hash from what arrived and a relay that swaps the token
// breaks the signature.
//
// THE TOKEN IS RANDOM, NOT ATTESTED, and that boundary is deliberate. §6.0's biometric
// freshness is PB-SEC-2's, and real biometric backing is PB-E2E-5, which is DEFERRED: no
// code here may imply an Android BiometricPrompt gated this. What the token delivers today
// is the property the daemon actually enforces -- one-shot, unforgeable by the relay, bound
// to this exact command -- and the biometric gate attaches to its minting when that slice
// lands.
func (a *App) TakeControl(session string) (op *Op, err error) {
	defer barrier(&err)
	token, err := newGateToken()
	if err != nil {
		return nil, err
	}
	return a.signedCommand(schema.ActionTakeControl, session, nil, commandBody{gate: token})
}

// Approve answers ONE pending approval_request (IS-LIFE-4): the phone's decision travels as
// the existing signed ActionApprove, validated machine-side against ADR-007 D7's binding tuple
// before the adapter applies it.
//
// THE ARGUMENTS ARE THE THREE THINGS A SCREEN KNOWS, and nothing else. session and itemID name
// the card (itemID IS D7's interaction_id, IS-APR-1) and decisionID is the id the tapped
// button carried, in the CLI's OWN vocabulary -- Codex offers accept |
// acceptWithExecpolicyAmendment | cancel (IS-APR-3), and this side never normalizes it,
// because a daemon reading `cancel` as a refusal would be guessing at a vocabulary it does not
// own. Flat strings because gomobile binds no struct argument.
//
// THE BINDING TUPLE IS NOT A PARAMETER, and that is IS-APR-2 expressed as a signature. A phone
// echoes content_hash and expires_at verbatim and computes neither, so they are read off the
// card this phone is holding rather than accepted from a caller -- a screen that could pass
// them is a screen that could compute them, and the failure would be a perfectly valid command
// the daemon refuses as stale.
//
// It answers LOCALLY for a card it cannot answer, and that is not a shortcut around the
// daemon's authority. An unknown item, a resolved one and a malformed one all come back from
// the machine as CodeStaleApproval, which the phone can only render as "your card is out of
// date" -- true for the resolved case, wrong and unactionable for the other two. The daemon
// still re-validates everything it is sent; this only refuses what could never have been sent.
//
// LIVE-ONLY, NEVER QUEUED (B43): sealSignedCommand appends to the mailbox or fails, and an
// approval has a daemon-authoritative window, so a decision stored for a later reconnect would
// be answering a question the agent has stopped asking.
func (a *App) Approve(session, itemID, decisionID string) (op *Op, err error) {
	defer barrier(&err)
	if session == "" || itemID == "" || decisionID == "" {
		return nil, classed(ErrClassInvalidRequest, errors.New(
			"swarmmobile: Approve needs a session, an item id and the decision id the tapped button carried"))
	}
	core, err := a.ready()
	if err != nil {
		return nil, err
	}
	b, ok := core.Router().Items().PendingApproval(session, itemID)
	if !ok {
		return nil, classed(ErrClassNotFound, fmt.Errorf(
			"swarmmobile: this phone holds no unresolved approval %q for session %q that carries an answerable binding tuple",
			itemID, session))
	}
	expires := b.ExpiresAt
	return a.signedCommand(schema.ActionApprove, session, nil, commandBody{approve: &schema.ApproveReq{
		Session:       session,
		AgentInstance: schema.AgentInstanceRef{ShimPID: b.ShimPID, ShimStartTime: b.ShimStartTime},
		InteractionID: itemID,
		ContentHash:   b.ContentHash,
		ExpiresAt:     &expires,
		Decision:      decisionID,
	}})
}

// gateTokenBytes is the entropy behind one gate token. 16 bytes is the same width the phone
// simulator has always minted and the same order as the operation id beside it: the token is
// single-use and short-lived, and its job is to be unguessable by a relay that sees only
// ciphertext.
const gateTokenBytes = 16

// newGateToken mints one one-shot gate token. A custody or entropy failure is RETURNED: a
// take_control carrying an empty token is refused at the machine with a message about the
// gate rather than about the phone, which is a failure reported as somebody else's.
func newGateToken() (string, error) {
	raw := make([]byte, gateTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", classed(ErrClassInternal, err)
	}
	return hex.EncodeToString(raw), nil
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
	op, err = a.sealSignedCommand(opTakeControlEnd, session, nil, commandBody{})
	if err != nil {
		return nil, err
	}
	core.Leases().Sever(session, "control was released")
	return op, nil
}

// Kill terminates a session. PB-APP-3's persistent Stop maps here.
func (a *App) Kill(session string) (op *Op, err error) {
	defer barrier(&err)
	return a.signedCommand(schema.ActionKill, session, nil, commandBody{})
}

// defaultLaunchCols / defaultLaunchRows are the grid a launch carries when the caller named
// none. The daemon REFUSES a launch below one column, so there is no "unset" to forward: the
// choice is a default or a launch that never reaches a PTY. 80x24 is the conventional
// terminal and the size the daemon's own session tap opens its emulator at, so a session
// launched from a sheet that has measured nothing renders the way every other one does.
const (
	defaultLaunchCols = 80
	defaultLaunchRows = 24
)

// Launch starts a session on the machine (PB-APP-6).
//
// The signed tuple names no session -- a launch has none yet -- so WHAT is launched is
// bound by ContentHash = the CANONICAL launch content hash, computed by the one
// implementation both the signer and the daemon's verifier use. Re-deriving that
// length-prefixed encoding here is forbidden: a one-byte divergence yields signature
// verification failures with no compile error and no daemon error until a real launch is
// refused.
//
// THE GEOMETRY IS THE PHONE'S TO SEND, and not because the signature forces it:
// LaunchContentHash excludes Cols/Rows as cosmetic, so the gateway could legally fill them.
// It is the phone's because only the phone knows the grid -- the size of the view the user
// is about to watch this session in -- and a machine-side default would open every remotely
// launched session at a width nobody chose.
func (a *App) Launch(spec *LaunchSpec) (op *Op, err error) {
	defer barrier(&err)
	if spec == nil {
		return nil, classed(ErrClassInvalidRequest, errors.New("swarmmobile: Launch requires a LaunchSpec"))
	}
	cols, rows := spec.Cols, spec.Rows
	if cols < 1 || rows < 1 {
		cols, rows = defaultLaunchCols, defaultLaunchRows
	}
	req := &schema.LaunchReq{
		Agent:         spec.Agent,
		Cwd:           spec.Cwd,
		InitialPrompt: spec.Prompt,
		Options:       parseOptions(spec.Options),
		Cols:          cols,
		Rows:          rows,
	}
	return a.signedCommand(schema.ActionLaunch, schema.LaunchSessionSentinel,
		schema.LaunchContentHash(req), commandBody{launch: req})
}

// RevokeThisDevice revokes this phone's own pairing (PB-SEC-7). It is the phone's panic
// action: the kill switch is owner-tier only and this surface can never set it.
//
// THE GAP THIS VERB WORKED AROUND IS CLOSED (S18). device_revoke was in the signed action set
// and the daemon served it, but remotegw's opForAction had no arm, so a correctly-signed
// revoke was refused "unsupported command action" one hop short of the daemon -- and a refused
// action seals no reply, so the op would never resolve either. Sealing it anyway would have
// burnt a durable send-seq and handed the panic action back as a success that then hangs
// forever, so this verb sealed NOTHING and recorded a durable local refusal instead, exactly
// as Interrupt did before its own resolution. That was right while the mapping was missing and
// became wrong the moment it landed: the button is on the screen and it would do nothing.
//
// So the revoke now rides the ordinary mutating path. opForAction maps it to
// protocol.OpDeviceRevoke and Gateway.ForwardCommand copies the signed subject into
// Control.TargetDeviceID, which is where handleDeviceRevoke reads both its authorization
// subject and the device to remove.
func (a *App) RevokeThisDevice() (op *Op, err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return nil, err
	}
	// PB-PUSH-9's "deletion on revoke/disable", done by the PHONE rather than left to the
	// relay. The relay does drop the token inside revokeAndPurge's transaction (S12), but a
	// phone that relies on that has no deletion on revoke at all when the revoke is issued
	// while the relay is unreachable -- and it goes on holding a token in durable state that
	// the next connection dutifully re-registers. Left in place it is a provider-visible
	// identifier for a device its owner disowned, and a machine that can still wake it.
	//
	// ONLY THE LOCAL HALF RUNS FIRST, AND THAT SPLIT IS THE FIX (agents-tracker-2x4e). The
	// durable clear must precede the command, for the reason a revoke is unlike every other
	// verb: its success DESTROYS the path its own reply would come back on -- the daemon
	// removes the device and rotates the epoch in one transaction, and the gateway severs and
	// exits -- so a revoke that lands while this handset still holds the token leaves it held
	// on a phone that can no longer be told anything. That half writes to this device's own
	// disk and speaks to nobody, and a core that cannot take the write cannot reserve a command
	// seq either, so it is not a gate the command would otherwise have passed.
	//
	// THE RELAY HOP IS NOT THAT, AND IT USED TO RIDE THE SAME LINE. handleTokenDelete refuses
	// over the connection's op quota, over authorization and over its own persistence failure,
	// and every one of those returned from here -- so the owner's panic action was suppressed
	// by the relay's answer to a housekeeping call that the revoke itself makes redundant, the
	// machine-side revokeAndPurge dropping the token in the same transaction that removes the
	// device. It now runs after the command and decides nothing: see
	// issueRevokeThenDropTokenAtRelay.
	if err = a.dropPushTokenLocally(core); err != nil {
		return nil, err
	}
	// The target device id sits in the SESSION position of the signed tuple -- that tuple has
	// no separate device field -- so a self-revoke names this phone, and the gateway moves it
	// to Control.TargetDeviceID on the daemon hop.
	return issueRevokeThenDropTokenAtRelay(
		func() (*Op, error) {
			return a.signedCommand(schema.ActionDeviceRevoke, a.deviceID(), nil, commandBody{})
		},
		a.dropPushTokenAtRelay,
	)
}

// issueRevokeThenDropTokenAtRelay is the ORDER, as a function, because the order IS the decision
// and neither half can be reached from a test: the revoke resolves through sendContext, whose
// awaitConn polls up to five seconds for a live relay connection, and the refusal the cleanup
// can produce comes from a relay client this package holds as a concrete type.
//
// THE CLEANUP ANSWERS FOR NOTHING, and dropping its error is the honest handling rather than a
// shortcut. Nothing in this package logs -- the facade has no sink and the log gates under
// android/gate exist to keep it that way -- and there is no event kind for a housekeeping
// failure; what there is instead is the recovery, which needs no report: durable state already
// holds no token, and onConnected reconciles the relay to whatever durable state holds on the
// next authenticated reconnect.
//
// IT RUNS ON BOTH PATHS. A revoke that never reached the machine still leaves a handset whose
// owner confirmed the destructive dialog -- SettingsSurface purges both key tiers in a `finally`
// for that reason -- so the token goes either way, and what comes back is the REVOKE's own
// answer, because that is the one the screen has to report.
//
// AND IT LEAVES WITH THE CALLER RATHER THAN IN FRONT OF IT (agents-tracker-j4pi). The hop is
// bounded -- relay.DefaultCallTimeout gives every control call ten seconds -- and the bound was
// the defect: this verb's RETURN is what runs the phone's own purge, because the Kotlin side
// destroys both key tiers in the `finally` around the call. So a hop on this goroutine put up to
// ten seconds of network in front of the local destruction of key material, on a handset whose
// owner has just confirmed a destructive dialog and whose process Android may kill at any moment.
// Waiting bought nothing at that price: the error was already going nowhere.
//
// A GOROUTINE NEEDS NO CONTEXT OF ITS OWN HERE. Conn.bounded applies the connection's call
// deadline to the context.Background() the hop passes, so it is bounded before it starts; and a
// hop that outlives the App finds a closed one at a.conn() and returns without dialling anything.
func issueRevokeThenDropTokenAtRelay(revoke func() (*Op, error), atRelay func() error) (*Op, error) {
	op, err := revoke()
	go func() { _ = atRelay() }()
	return op, err
}

// Interrupt is PB-APP-3's persistent Stop: the SIGNED turn_interrupt op (Wave R6, Mirror
// M2.4 "Stop becomes a signed interrupt op"). The daemon resolves the session's adapter and
// types that CLI's OWN recorded cancel sequence, or refuses `interrupt_unsupported` having
// typed nothing -- so Stop has a visible SUCCESS and a visible REFUSAL, per verb, which the
// input-plane ride structurally could not give it (a dropped input frame is silence).
//
// SUPERSESSION, EXECUTED (pre-recorded in docs/verification/r6-red/chat-red.txt and
// mobile/r6_chatverbs_test.go). The 2026-07-25 resolution that stood here -- "an interrupt
// IS a keystroke (Ctrl-C, 0x03, through a PTY in ISIG mode), so Stop holds the lease and
// sends 0x03 on the live input plane" -- rested on a premise its own text stated: "MINTING A
// NEW SIGNED ACTION WAS REJECTED [because] ... until every hop learned it, a command bearing
// the new action would be refused by the daemon's CLOSED, fail-closed capability switch one
// hop short of the daemon". Wave R1 dissolved that premise (ActionTurnInterrupt is mapped at
// every hop: actionClass, opForAction, handleControl) and Wave R6 landed the real handler,
// so the ride is retired with its reason. What the keystroke ride could never deliver, this
// op does: no lease is needed (the tuple's own signature is the authorization), the daemon
// executes the ADAPTER's declared sequence rather than assuming ISIG semantics, and the
// reply resolves the op -- success and refusal both land on the screen.
//
// What is KEPT from the old ride, deliberately:
//
//   - LIVE ONLY (ADR-007 D7's spirit): sealSignedCommand appends to the mailbox or fails.
//     A Stop that arrives ten minutes late interrupts whatever the agent is doing THEN, so
//     an offline press refuses with the offline class -- a LEGIBLE refusal, which preserves
//     the 4lta pin (an offline Stop SAYS SO) rather than weakening it.
//   - Nothing rides the undelivered-INPUT ledger: the signed op is not a keystroke, and its
//     failure surfaces on the op itself (r6_chatverbs_test.go pins the ledger stays empty).
//
// EXPECTED_TURN IS REQUIRED (Wave R6 fix-pack, review finding B7). The verb took only a
// session until a probe showed what that costs: with turnA superseded by turnB, a
// ComposerSend rendered against turnA was refused stale_turn while an Interrupt rendered
// against the SAME turnA succeeded and typed the cancel sequence into turnB. In playbook
// §8.1 turnB is the turn the OWNER just started at the terminal, and the Claude adapter's
// own note records that the cancel key at an idle prompt CLEARS the composer -- so a late
// Stop wipes the terminal user's half-typed line. Stop and Send are tapped under one race
// and now carry one precondition; the screen passes the turn it DREW the button against, and
// a Stop that names no turn is refused before anything is signed.
func (a *App) Interrupt(session, expectedTurn string) (op *Op, err error) {
	defer barrier(&err)
	if session == "" || expectedTurn == "" {
		return nil, classed(ErrClassInvalidRequest, errors.New(
			"swarmmobile: Interrupt needs a session id and the turn the screen drew Stop against"))
	}
	if _, err = a.ready(); err != nil {
		return nil, err
	}
	return a.signedCommand(schema.ActionTurnInterrupt, session, nil, commandBody{
		interrupt: &schema.TurnInterruptReq{Session: session, ExpectedTurn: expectedTurn},
	})
}

// ComposerSend is Mirror M2.4's structured composer: one message into a session's agent,
// as the signed composer_send op. expectedTurn is the turn the SCREEN rendered the draft
// against ("" for an idle session). It remains signed context, but the daemon treats it as
// advisory and dispatches accepted messages through a per-session FIFO against current
// provider state. The signed tuple's content slot binds
// (session, session_instance, expected_turn, text) via schema.ComposerSendContentHash -- derived in
// phonecore.SignComposerSend, never here (the take_control-token rule: a re-derivation is a
// silent signature failure with no compile error).
//
// ONLINE-ONLY: accepted online messages are ordered by the daemon's semantic queue. There is
// still no offline outbox: an offline send refuses with the offline class and stores NOTHING,
// so reconnect cannot unexpectedly deliver old words into a later conversation.
func (a *App) ComposerSend(session, expectedTurn, text string) (op *Op, err error) {
	defer barrier(&err)
	if session == "" || text == "" {
		return nil, classed(ErrClassInvalidRequest, errors.New(
			"swarmmobile: ComposerSend needs a session id and a non-empty message"))
	}
	if _, err = a.ready(); err != nil {
		return nil, err
	}
	return a.signedCommand(schema.ActionComposerSend, session, nil, commandBody{
		composer: &schema.ComposerSendReq{Session: session, ExpectedTurn: expectedTurn, Text: text},
	})
}

// ComposerRetry authors a fresh operation for one exact composer attempt which the machine
// refused with input_busy. The durable core retains its LogicalID and FIFO slot, so Android may
// replace one bubble without treating identical message text as identity. No other refusal,
// accepted result or delivery-unknown state authorizes this verb.
func (a *App) ComposerRetry(previousOperationID, session, expectedTurn, text string) (op *Op, err error) {
	defer barrier(&err)
	if previousOperationID == "" || session == "" || text == "" {
		return nil, classed(ErrClassInvalidRequest, errors.New(
			"swarmmobile: ComposerRetry needs the prior operation, session and non-empty message"))
	}
	if _, err = a.ready(); err != nil {
		return nil, err
	}
	return a.signedCommand(schema.ActionComposerSend, session, nil, commandBody{
		composer: &schema.ComposerSendReq{Session: session, ExpectedTurn: expectedTurn, Text: text},
		retryOf:  previousOperationID,
	})
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

// ---------------------------------------------------------------------------
// WAVE R8 -- the capability-routed terminal fallback (ADR-017 T4 / T6).
// ---------------------------------------------------------------------------

// TerminalViewWatch opens the terminal stream for a terminal_fallback session (ADR-017 T4).
//
// IT IS THE SAME WIRE VERB AS TerminalWatch AND THIS COMMENT SAYS SO, because the previous
// one did not: it claimed to open "the VERSIONED TerminalViewV1 stream" while the body was
// byte-for-byte TerminalWatch and nothing versioned existed on any wire path (closing review,
// finding 5). Versioning is a property of the BODY the machine sends -- since the closing
// round every terminal_snapshot frame carries `terminal_view` beside the legacy `terminal`,
// and a machine that predates it sends only the legacy half -- so there is nothing for a
// second verb to negotiate and inventing one would be a wire change with no reader.
//
// WHY THE TWO NAMES REMAIN. `TerminalWatch` is the pre-R8 verb every existing caller uses;
// this is the name the FALLBACK BINDING calls, and it is what the Kotlin gate's single-file
// allowlist is written over. Two names for one verb is the honest state of it, and it is
// recorded here rather than in a comment that describes a difference the code does not have.
//
// A WATCH IS A READ AND GRANTS NO INPUT AUTHORITY. It acquires no lease, mints no
// generation and never touches the input coalescer -- gating a read on the input plane is
// how a monitoring surface becomes a control surface by accident. The command is UNSIGNED
// and the SESSION gate is the machine's: handleTerminalSubscribe refuses a session whose
// capability record does not permit a terminal view, with the sealed CodeCapabilityRefused,
// before it opens any tap.
func (a *App) TerminalViewWatch(session string) (err error) {
	defer barrier(&err)
	_, err = a.unsignedCommand(schema.ActionTerminalWatch, session)
	return err
}

// TerminalViewUnwatch closes the stream. Without it the machine keeps rendering, sealing
// and appending full screens for a screen the user has left, against an append budget
// shared with every other session's transcript.
func (a *App) TerminalViewUnwatch(session string) (err error) {
	defer barrier(&err)
	_, err = a.unsignedCommand(schema.ActionTerminalUnwatch, session)
	return err
}

// TerminalViewRenew renews the watch's horizon (ADR-017 amendment T4-b): the machine's only
// evidence that anyone is still looking. It never STARTS a watch, so it cannot be a way to
// acquire a peek without the verb the capability gate is written over.
func (a *App) TerminalViewRenew(session string) (err error) {
	defer barrier(&err)
	_, err = a.unsignedCommand(schema.ActionTerminalRenew, session)
	return err
}

// TerminalControlBegin enters control over a terminal_fallback session: the SIGNED op that
// mints one non-transferable generation (ADR-017 T6), on the device.ActionControl tier that
// internal/skeleton/deviceauth.go already maps and T6-a ratifies.
//
// It binds the session INSTANCE and the selected profile as well as the session, because a
// signature that verifies over bytes which do not name what it authorised is a signature
// over the wrong thing: a generation bound to a session id alone authorises raw bytes into
// the PTY that REPLACED the one the user was reading.
func (a *App) TerminalControlBegin(session string, sessionInstance string) (op *Op, err error) {
	defer barrier(&err)
	if session == "" || sessionInstance == "" {
		return nil, classed(ErrClassInvalidRequest, errors.New(
			"swarmmobile: TerminalControlBegin needs a session id and the session instance the screen is showing"))
	}
	if _, err = a.ready(); err != nil {
		return nil, err
	}
	return a.signedCommand(schema.ActionTerminalControlBegin, session, nil, commandBody{
		terminalControl: &schema.TerminalControlBeginReq{
			Session: session, SessionInstance: sessionInstance, Profile: schema.CurrentProfileVersion,
		},
	})
}

// TerminalControlEnd releases the generation. It lands beside Begin rather than after it
// because without it the first generation a user opens could only be closed by a timeout.
func (a *App) TerminalControlEnd(session string) (op *Op, err error) {
	defer barrier(&err)
	if session == "" {
		return nil, classed(ErrClassInvalidRequest, errors.New("swarmmobile: TerminalControlEnd needs a session id"))
	}
	core, err := a.ready()
	if err != nil {
		return nil, err
	}
	op, err = a.signedCommand(schema.ActionTerminalControlEnd, session, nil, commandBody{})
	if err != nil {
		return nil, err
	}
	// ADR-017 T6-f: the release SEVERS LOCALLY AND DROPS THE HELD BYTES, without waiting for
	// the machine's reply. The natural implementation of "release control" flushes the
	// coalescer's window -- which is the one place a queue can form on this path, and a flush
	// there delivers bytes whose authority the user has just withdrawn. Severing here also
	// closes the window in which the screen still believes it may type: the machine refuses
	// those frames anyway, and a phone that authored them would be authoring input it has
	// already told the user it gave up.
	core.TerminalControl().Sever(session, "control was released")
	return op, nil
}

// TerminalInput types raw bytes into a terminal_fallback session under a live generation.
//
// THE PARAMETER IS TEXT, not bytes, for Paste's reason: an IME and a clipboard both hand
// Android a String, and PB-BIND-4 keeps []byte crossings to the enumerated few.
//
// REFUSED INPUT IS UNDELIVERED AND NEVER BUFFERED (ADR-017 T6-f / PB-INPUT-1). The two
// wrong answers are symmetrical and both ship a lie: buffering makes the UI look successful
// and delivers the bytes later (the offline queue B43 proved unbuildable), and dropping
// silently makes the UI look successful and never delivers them. refuseInput is the shape,
// and it keeps the bytes out of the coalescer's buffer on purpose.
//
// IT IS NOT AN APPROVAL VERB AND MUST NEVER BECOME ONE (T6). An approval answered from a
// fallback screen still travels as the signed ActionApprove of IS-LIFE-4, or the button is
// not shown.
func (a *App) TerminalInput(session string, text string) (err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return err
	}
	data := []byte(text)
	// The generation is re-read per frame, and its absence refuses BEFORE the bytes are
	// accepted anywhere. There is no place in this app to hold a byte on the way to
	// terminal_input -- not a refused one, none.
	generation, instance, ok := core.TerminalControl().Generation(session)
	if !ok {
		return a.refuseInput(session, data, classed(ErrClassInvalidRequest, errors.New(
			"swarmmobile: no live terminal control generation for this session; nothing was typed")))
	}
	sc, err := a.liveSendContext()
	if err != nil {
		return a.refuseInput(session, data, err)
	}
	return a.sendTerminalInput(sc, core, session, instance, generation, data)
}

// sendTerminalInput seals and appends ONE unsigned raw-input frame. The bytes are NEVER
// buffered on the way: a failure resolves them as an explicit undelivered record, which is
// refuseInput's rule inherited verbatim.
func (a *App) sendTerminalInput(sc sendCtx, core *phonecore.Core, session, instance, generation string, data []byte) error {
	a.bucketMu.Lock()
	defer a.bucketMu.Unlock()
	if err := a.flushPendingPublicationsLocked(context.Background(), sc); err != nil {
		return a.refuseInput(session, data, err)
	}
	seq, err := core.Seq().NextCommand()
	if err != nil {
		return a.refuseInput(session, data, err)
	}
	env, err := phonecore.SealTerminalInputEnvelope(sc.key, sc.epoch, seq, core.State().Machine,
		&schema.TerminalInputReq{
			Session: session, SessionInstance: instance, ControlGeneration: generation, Bytes: data,
		})
	if err != nil {
		return a.refuseInput(session, data, err)
	}
	if _, err := sc.cl.MailboxAppend(context.Background(), sc.target, env); err != nil {
		return a.refuseInput(session, data, err)
	}
	return nil
}

// unsignedTerminalFrame seals and appends the keepalive: the second and last unsigned frame
// kind, carrying the generation and nothing else.
func (a *App) unsignedTerminalFrame(op, session, generation string, _ []byte) (*Op, error) {
	core, err := a.ready()
	if err != nil {
		return nil, err
	}
	sc, err := a.liveSendContext()
	if err != nil {
		return nil, err
	}
	a.bucketMu.Lock()
	defer a.bucketMu.Unlock()
	if err := a.flushPendingPublicationsLocked(context.Background(), sc); err != nil {
		return nil, err
	}
	seq, err := core.Seq().NextCommand()
	if err != nil {
		return nil, err
	}
	env, err := phonecore.SealTerminalKeepaliveEnvelope(sc.key, sc.epoch, seq, core.State().Machine, session, generation)
	if err != nil {
		return nil, err
	}
	if _, err := sc.cl.MailboxAppend(context.Background(), sc.target, env); err != nil {
		return nil, err
	}
	return nil, nil
}

// TerminalControlKeepalive renews the generation's horizon (ADR-017 T6-c).
//
// IT IS EMITTED ONLY BY THE LIVE FOREGROUND FALLBACK SCREEN, the same composition that owns
// TerminalInput -- same routing rule, same fence. A background coroutine, a scheduled job
// or a service-hosted timer that could emit it would hold a generation open for the full
// horizon with NO screen displaying it, which defeats the persistent banner and the
// leave-screen trigger together. The daemon's idle expiry is what holds when the app does
// not; this is the app's own contract.
func (a *App) TerminalControlKeepalive(session string) (err error) {
	defer barrier(&err)
	core, err := a.ready()
	if err != nil {
		return err
	}
	generation, _, ok := core.TerminalControl().Generation(session)
	if !ok {
		return classed(ErrClassInvalidRequest, errors.New("swarmmobile: no live terminal control generation to keep alive"))
	}
	_, err = a.unsignedTerminalFrame(schema.ActionTerminalControlKeepalive, session, generation, nil)
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
	if err := a.flushPendingPublicationsLocked(context.Background(), sc); err != nil {
		return err
	}
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
		// PB-SEC-2, and it is the whole of ADR-007 B36. The content key used to be unwrapped
		// ONCE, at Resume, and read from this State copy for every send thereafter -- so after a
		// single resume neither a screen lock nor the stated 60-second freshness window stopped
		// any content operation, and the Keystore that enforces both was never asked again.
		//
		// ASKING IT HERE IS THE ENFORCEMENT. The tier sealer holds the FETCHER and never a key
		// (keycustody.go), so this is a real Keystore round trip: it succeeds only while the
		// device has authenticated inside the window the content KEK was provisioned with
		// (setUserAuthenticationParameters(60, AUTH_BIOMETRIC_STRONG)), and otherwise refuses
		// with the reauth verdict the Android side already routes to a prompt. A phone that
		// holds the key answers from memory and never reaches here, which is what a 60-second
		// authorization window means; a phone whose lock purged it must earn it back.
		//
		// It is cheap on the keyless-and-awaiting-grant path this shares with PB-KEY-3: there is
		// no sealed content blob to open, so no KEK is consulted at all.
		if err := core.UnsealContent(); err != nil {
			return sendCtx{}, err
		}
		st = core.State()
	}
	if st.Keys.ContentKey == (crypto.ContentKey{}) {
		// PB-KEY-3's two keyless states are NOT the same failure and must not share a screen.
		// Waiting for a grant resolves itself when the machine's next one lands; grant LOSS
		// never will, and telling that user to pair again is a brick -- BeginPairing fail-fasts
		// while this device is registered (PB-STATE-10), so the only exit left is physical
		// access to the machine. The verdict is read from the CORE's durable stream mark rather
		// than cached here, so the re-grant that clears it clears this too, in the same
		// transaction that installs the key.
		if core.StreamStale(phonecore.StreamGrant) {
			return sendCtx{}, errGrantLost
		}
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
	cl     mailboxAppender
	target string
	key    crypto.ContentKey
	epoch  uint32
}

// (*App).refuse is GONE, and its absence is the record that this facade has no surface left
// whose wire verb no hop can resolve. It recorded a durable, already-resolved refusal for such
// a surface -- the right shape while one existed, because sealing the command instead would
// burn a durable send-seq on a frame dropped one hop later, and issuing the op would raise
// PendingOpCount for the life of the process. Its three callers were interrupt (resolved to a
// keystroke on the live input plane), push_preference (resolved by S12's signed push_prefs)
// and device_revoke (resolved by S18's opForAction arm). Should a future surface need the
// shape again, it is in this comment and in git; keeping the helper alive with no caller would
// only invite a new surface to reach for a local refusal instead of a wire verb.

// commandBody is the optional payload a signed command carries beside its tuple. It is one
// struct rather than a growing tail of nil parameters: each field is a DIFFERENT sealing
// shape (SealLaunchEnvelope / SealPushPrefsEnvelope / SealCommandEnvelope), so a call site
// that passes the wrong one produces a frame the gateway refuses, and a positional nil is
// exactly the kind of argument that gets passed in the wrong slot.
type commandBody struct {
	launch *schema.LaunchReq
	prefs  *schema.PushPrefs
	// gate is take_control's one-shot gate token. It is a string rather than a bool-plus-
	// field because it changes BOTH halves of the frame: the signature covers SHA256(it) and
	// the envelope carries it beside the signed tuple, and the two must be the same value.
	gate string
	// approve is IS-LIFE-4's ApproveReq. Like gate it changes both halves -- the signature
	// covers the body's content_hash and the envelope carries the body -- and for the same
	// reason the derivation is phonecore's (SignApprove), not this package's.
	approve *schema.ApproveReq
	// sessionLaunch is Wave R5's preset confirmation body. Launch's own shape: the body
	// rides beside the signed tuple (SealSessionLaunchEnvelope) and is bound into the
	// signature via ContentHash = schema.SessionLaunchContentHash(it).
	sessionLaunch *schema.SessionLaunchReq
	// composer is Wave R6's composer_send body. Like approve it changes both halves --
	// the signature covers schema.ComposerSendContentHash(it) and the envelope carries the
	// body (SealComposerSendEnvelope) -- and for the same reason the derivation is
	// phonecore's (SignComposerSend), not this package's.
	composer *schema.ComposerSendReq
	// retryOf is the exact terminal input_busy operation ComposerRetry replaces. It never
	// changes the signed/wire body; the durable publication transition uses it only to retain
	// the prior LogicalID and atomically retire that one proven-no-write attempt.
	retryOf string
	// interrupt is Wave R6's turn_interrupt body, composer's exact twin since the fix-pack
	// bound expected_turn into it (finding B7): signature covers
	// schema.TurnInterruptContentHash(it), envelope carries it, derivation is phonecore's
	// (SignTurnInterrupt).
	interrupt *schema.TurnInterruptReq
	// terminalControl is Wave R8's terminal_control_begin body (ADR-017 T6), composer's
	// shape once more: the signature covers schema.TerminalControlBeginContentHash(it) and
	// the envelope carries the body, and phonecore owns the derivation
	// (SignTerminalControlBegin) for SignApprove's stated reason.
	terminalControl *schema.TerminalControlBeginReq
}

// signedCommand seals one mutating command and tracks it IN FLIGHT, because the gateway
// answers it: a forwarded action carries the daemon's reply back, and take_control its
// lease confirmation. A command the gateway answers with nothing must use
// sealSignedCommand directly.
func (a *App) signedCommand(action, session string, contentHash []byte, body commandBody) (*Op, error) {
	op, err := a.sealSignedCommand(action, session, contentHash, body)
	if err != nil {
		return nil, err
	}
	a.issue(op)
	return op, nil
}

// sealSignedCommand authors, signs, seals and appends one mutating command.
func (a *App) sealSignedCommand(action, session string, contentHash []byte, body commandBody) (*Op, error) {
	core, err := a.ready()
	if err != nil {
		return nil, err
	}
	// PB-SYNC-7's fail-closed gate, with PB-STATE-4's amendment of 2026-07-26 applied: the
	// action decides, and device_revoke is EXEMPT. The boundary is not "revoke is special" --
	// this gate protects ops whose TARGET IS SELECTED FROM SYNCHRONIZED STATE (kill, launch,
	// take_control), because a rollback makes them act on the wrong object. A self-revoke
	// selects no target (it names its own signer, which needs no synchronized state to
	// identify) and only REMOVES capability, never grants it, so a rollback attacker who
	// forces one gains a denial of service they already had -- while gating it denies the
	// owner their only remote kill on an unreconciled phone, which is close to the definition
	// of the lost handset the panic button exists for.
	if action != schema.ActionDeviceRevoke {
		if err := a.requireReconciled(); err != nil {
			return nil, err
		}
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
	// take_control signs a DIFFERENT tuple: its content hash is SHA256(gate token), and the
	// rule that it is is phonecore's, not this package's. Re-deriving it here would be the
	// same forbidden duplication as re-deriving LaunchContentHash -- a divergence produces a
	// signature the daemon rejects, with no compile error and no message naming the cause.
	var cmd schema.DeviceCommandAuth
	switch {
	case body.gate != "":
		cmd, err = phonecore.SignTakeControl(core.KeyStore(), phonecore.TakeControlInput{
			Machine:     core.State().Machine,
			Session:     session,
			OperationID: id,
			ExpiresAt:   expiresAt,
			GateToken:   body.gate,
		})
	case body.composer != nil:
		cs, ok := core.Router().Sessions().Get(session)
		if !ok || cs.Capabilities == nil || cs.Capabilities.SessionInstance == "" {
			return nil, classed(ErrClassInvalidRequest, errors.New(
				"swarmmobile: ComposerSend needs a current session incarnation; refresh the machine and upgrade if it remains unavailable"))
		}
		body.composer.SessionInstance = cs.Capabilities.SessionInstance
		// ComposerSend signs a DIFFERENT tuple too: the content slot is
		// schema.ComposerSendContentHash over the exact
		// (session, session_instance, expected_turn, text)
		// body the envelope carries. phonecore owns the derivation, for SignApprove's
		// stated reason.
		cmd, err = phonecore.SignComposerSend(core.KeyStore(), phonecore.ComposerSendInput{
			Machine:         core.State().Machine,
			Session:         session,
			SessionInstance: body.composer.SessionInstance,
			OperationID:     id,
			ExpiresAt:       expiresAt,
			ExpectedTurn:    body.composer.ExpectedTurn,
			Text:            body.composer.Text,
		})
	case body.interrupt != nil:
		// Wave R6 fix-pack B7: turn_interrupt signs the composer's shape -- the content
		// slot is schema.TurnInterruptContentHash over the exact (session, expected_turn)
		// body the envelope carries. phonecore owns the derivation, same rule as above.
		cmd, err = phonecore.SignTurnInterrupt(core.KeyStore(), phonecore.TurnInterruptInput{
			Machine:      core.State().Machine,
			Session:      session,
			OperationID:  id,
			ExpiresAt:    expiresAt,
			ExpectedTurn: body.interrupt.ExpectedTurn,
		})
	case body.terminalControl != nil:
		// Wave R8: terminal_control_begin signs the composer's shape with one field more --
		// the SESSION INSTANCE -- because a generation bound to a session id alone survives
		// the session's replacement and would authorise raw bytes into its successor.
		cmd, err = phonecore.SignTerminalControlBegin(core.KeyStore(), phonecore.TerminalControlBeginInput{
			Machine:         core.State().Machine,
			Session:         session,
			SessionInstance: body.terminalControl.SessionInstance,
			Profile:         body.terminalControl.Profile,
			OperationID:     id,
			ExpiresAt:       expiresAt,
		})
	case body.approve != nil:
		// Approve signs a DIFFERENT tuple too: ADR-007 D7 spends the content slot on the
		// INTERACTION CONTENT, so the hash under the signature is the card's own content_hash,
		// echoed verbatim (IS-APR-2). Decoding it is phonecore's rule for the same reason
		// SHA256(gate token) is -- a divergence here produces a signature the daemon rejects,
		// with no compile error and no message naming the cause.
		cmd, err = phonecore.SignApprove(core.KeyStore(), phonecore.ApproveInput{
			Machine:     core.State().Machine,
			Session:     session,
			OperationID: id,
			ExpiresAt:   expiresAt,
			ContentHash: body.approve.ContentHash,
		})
	default:
		cmd, err = phonecore.SignCommand(core.KeyStore(), phonecore.CommandInput{
			Action:      action,
			Machine:     core.State().Machine,
			Session:     session,
			OperationID: id,
			ExpiresAt:   expiresAt,
			ContentHash: contentHash,
		})
	}
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
	if body.composer != nil {
		if err := core.ExpirePublications(time.Now()); err != nil {
			return nil, err
		}
		pending := phonecore.PendingPublication{
			LogicalID:       id,
			OperationID:     id,
			Kind:            phonecore.PublicationComposer,
			SessionID:       session,
			SessionInstance: body.composer.SessionInstance,
			ExpectedTurn:    body.composer.ExpectedTurn,
			Text:            body.composer.Text,
			Command:         cmd,
			Composer:        body.composer,
			Phase:           phonecore.PublicationPrepared,
			CreatedAt:       time.Now(),
		}
		if err := a.preparePublicationLocked(core, sc, pending, body.retryOf); err != nil {
			return nil, err
		}
		// Once PreparePublication commits, this press is accepted locally and owns an
		// operation id. An append failure is delivery-unknown, not a refusal: the exact
		// envelope remains in the durable FIFO and the connection pump redrives it.
		if err := a.flushPendingPublicationsLocked(context.Background(), sc); err != nil {
			a.wakePublicationPump()
		}
		// Wake even after successful admission: the pump owns the bounded no-reply timer.
		a.wakePublicationPump()
		return &Op{Action: action, SessionID: session, OperationID: id}, nil
	}
	// Non-durable producers may not allocate above an older sealed publication. If its
	// append still fails, this command refuses without consuming another sequence.
	if err := a.flushPendingPublicationsLocked(context.Background(), sc); err != nil {
		return nil, err
	}
	seq, err := core.Seq().NextCommand()
	if err != nil {
		return nil, err
	}
	var env []byte
	switch {
	case body.launch != nil:
		env, err = phonecore.SealLaunchEnvelope(sc.key, sc.epoch, seq, cmd, body.launch)
	case body.sessionLaunch != nil:
		// Wave R5: the preset confirmation rides beside the signed tuple with its
		// body-version binding, exactly as the free-form launch spec does.
		env, err = phonecore.SealSessionLaunchEnvelope(sc.key, sc.epoch, seq, cmd, body.sessionLaunch)
	case action == opLaunchPresets:
		// Wave R5: the signed preset-list read carries no body of its own, but MUST
		// carry the body-version binding a bare SealCommandEnvelope omits -- the daemon
		// refuses an absent body_version identically to a wrong one.
		env, err = phonecore.SealLaunchPresetsEnvelope(sc.key, sc.epoch, seq, cmd)
	case body.composer != nil:
		// Wave R6: the composer body rides beside the signed tuple with its body-version
		// binding, exactly as the preset confirmation does.
		env, err = phonecore.SealComposerSendEnvelope(sc.key, sc.epoch, seq, cmd, body.composer)
	case body.interrupt != nil:
		// Wave R6 fix-pack B7: the interrupt body rides beside the signed tuple with its
		// body-version binding, exactly as the composer body does. (It was bodyless when
		// the op landed; schema.TurnInterruptReq records what that cost.)
		env, err = phonecore.SealTurnInterruptEnvelope(sc.key, sc.epoch, seq, cmd, body.interrupt)
	case body.terminalControl != nil:
		// Wave R8: the begin body rides beside the signed tuple with its body-version
		// binding, exactly as the composer body does.
		env, err = phonecore.SealTerminalControlBeginEnvelope(sc.key, sc.epoch, seq, cmd, body.terminalControl)
	case body.prefs != nil:
		env, err = phonecore.SealPushPrefsEnvelope(sc.key, sc.epoch, seq, cmd, *body.prefs)
	case body.approve != nil:
		// The body rides beside the signed tuple so the gateway can rebuild the approve Control
		// the daemon validates against -- launch's shape, for the other action whose payload the
		// daemon reads.
		env, err = phonecore.SealApproveEnvelope(sc.key, sc.epoch, seq, cmd, *body.approve)
	case body.gate != "":
		// The wire token rides beside the signed tuple so the gateway can rebuild the
		// take_control Control the daemon verifies against. The requested lifetime is the
		// horizon this command was SIGNED for: the daemon takes the earliest of it, its own
		// 30-minute cap and the signed ExpiresAt, so asking for anything else would either be
		// ignored or would end the typing session before the signature did.
		env, err = phonecore.SealTakeControlEnvelope(sc.key, sc.epoch, seq, cmd, body.gate,
			int(phonecore.CommandTTLFor(action).Seconds()))
	default:
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
	return a.unsignedCommandAt(schema.ActionJournalResync, "", from, false, false, "")
}

func (a *App) unsignedRosterRefresh(from uint64, discardedBacklog bool, recoveryToken string) (*Op, error) {
	return a.unsignedCommandAt(schema.ActionJournalResync, "", from, true, discardedBacklog, recoveryToken)
}

// unsignedCommand seals a READ command (terminal_watch / terminal_unwatch): the action and
// its target only, with no device signature, because the gateway serves it itself.
func (a *App) unsignedCommand(action, session string) (*Op, error) {
	return a.unsignedCommandAt(action, session, 0, false, false, "")
}

// unsignedCommandAt is unsignedCommand with journal_resync's from-cursor. It is one function
// because every unsigned read draws from the SAME Sequencer and must take the same bucket
// lock; a second copy of this body is a second place for that rule to be forgotten.
func (a *App) unsignedCommandAt(action, session string, resyncCursor uint64, rosterOnly, discardedBacklog bool, recoveryToken string) (*Op, error) {
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
	if err := a.flushPendingPublicationsLocked(context.Background(), sc); err != nil {
		return nil, err
	}
	seq, err := core.Seq().NextCommand()
	if err != nil {
		return nil, err
	}
	auth := schema.DeviceCommandAuth{Action: action, Machine: core.State().Machine, Session: session}
	var env []byte
	if action == schema.ActionJournalResync {
		if rosterOnly {
			env, err = phonecore.SealRosterRefreshEnvelope(sc.key, sc.epoch, seq, auth, resyncCursor, discardedBacklog, recoveryToken)
		} else {
			env, err = phonecore.SealResyncEnvelope(sc.key, sc.epoch, seq, auth, resyncCursor)
		}
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
