package skeleton

// Wave R6's daemon-side complete chat (bead agents-tracker-hggx.7): the application halves
// of the four ops internal/protocol/remote_chat.go serves.
//
//   - composerSend (Mirror M2.4, IS-LIFE-5, playbook §8.1 step 3): refuse a session whose
//     structured chat has degraded (ADR-017 T2 rule 2, fix-pack B3), verify expected_turn
//     against the daemon's OWN turn state (turnIDLocked's, IS-ENV-1), write the text plus
//     submit framing into the session's PTY through the shared tap, and remember the
//     injection -- for a BOUNDED window (fix-pack B9) -- so the echoed UserPromptSubmit
//     journals source "phone" with the op's id.
//   - interruptTurn (Mirror M2.4): verify expected_turn exactly as the composer does
//     (fix-pack B7 -- the op was turn-blind and typed the cancel sequence into whichever
//     turn was current on arrival), then type the adapter-declared cancel sequence, or
//     refuse interrupt_unsupported having typed NOTHING (ADR-017's honest degrade; a
//     guessed keystroke is IS-TOOL-2's forbidden move one layer down).
//   - interactionHistory (Mirror M3.1, ADR-014): the page of this session's journalled
//     interaction records strictly older than before_item, ascending, limit-bound, with
//     the honest retention floor.
//   - interactionDetail (Mirror M3.3, IS-CAP-2/-3): the full pre-truncation body retained
//     at capture time, or `unavailable` -- never a partial body presented as whole.
//
// All four live on the OUTER Daemon because they read state that lives here (turnIDs, the
// detail store, the tap) and are handed to the coreAPI as func fields, the approve seam's
// exact shape (api.go).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/appserver"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/submitframe"
)

// maxPendingSends bounds one session's accepted-but-not-yet-echoed composer injections. A
// send whose echo never arrives (the CLI dropped it, the hook never fired) must not leak
// state forever; the FIFO drops its OLDEST entry past the bound, which is the entry whose
// echo is least likely still coming.
const maxPendingSends = 8

// maxDetailBytes bounds the daemon-wide retained pre-truncation bodies (M3.3's "64 MiB LRU
// side store at capture time"): eviction is oldest-first across the whole store, and an
// evicted id answers `unavailable` honestly rather than a partial body.
const maxDetailBytes = 64 << 20

// pendingSendTTL bounds how long an accepted composer injection stays eligible to claim an
// echoed prompt (Wave R6 review finding B9).
//
// TEXT CORRELATION CAN LIE, AND THE SPEC ASSERTED IT COULD NOT. stampComposerEchoLocked
// consumes the first pending entry whose text EQUALS the echoed prompt; probed both
// directions, an OWNER-typed "yes" at the terminal was stamped source=phone carrying the
// phone's operation_id, because a phone send of "yes" was still pending. Short strings are
// exactly the ones two parties type identically ("yes", "y", "continue"), so the collision
// is not exotic. Matching on text is inherent to the mechanism -- the CLI's own
// UserPromptSubmit hook is the only echo, and it carries no injection id -- so the fix is to
// BOUND it rather than to claim it cannot happen: an injection whose echo has not arrived
// within this window is stale, and a prompt that arrives after it keeps the adapter's honest
// owner attribution.
//
// The window is the round trip from "the daemon wrote the bytes into the PTY" to "the CLI's
// prompt hook fired", which is one local process's own event loop -- milliseconds in
// practice. Ten seconds is roughly three orders of magnitude of headroom, chosen so a
// machine under heavy load never mis-attributes a phone send to the owner (the FALSE
// direction of this trade, which loses a true fact), while a human who walks away and types
// the same word at the terminal a minute later is never mis-attributed to the phone (the
// direction that INVENTS a fact, which is the one ADR-009 cares about).
const pendingSendTTL = 10 * time.Second

// pendingSend is one accepted composer injection awaiting its UserPromptSubmit echo.
type pendingSend struct {
	text        string
	operationID string
	// clientRef is the id THE DAEMON MINTED and handed to the CLI with the message
	// (Codex's `clientUserMessageId`), echoed back on the item as `clientId`. When it is
	// set the correlation is EXACT and text is never consulted -- which is the one place
	// R7 does better than the mechanism it inherits, and the reason it must: the text
	// correlation below has a PROBED mis-attribution on record. Empty for a provider whose
	// echo carries no such id (Claude), where text matching is still the only mechanism.
	clientRef string
	// at is when the injection was accepted: pendingSendTTL's start. See its doc.
	at time.Time
}

// chatNow reads the daemon's clock seam (Config.ItemClock), defaulting to time.Now exactly
// as the interaction append floor does. It exists so the composer-echo TTL is testable
// without a wall-clock sleep -- the house rule that put ItemClock there in the first place.
func (d *Daemon) chatNow() time.Time {
	if d.itemClock != nil {
		return d.itemClock()
	}
	return time.Now()
}

// errIsLife5 formats one IS-LIFE-5 refusal.
func errIsLife5(format string, args ...any) error {
	return fmt.Errorf("IS-LIFE-5: "+format, args...)
}

// resolveChatSession parses a namespaced session id and pins it to this machine
// (approveInteraction's rule: an op routed to the wrong daemon must not resolve a
// same-named session here).
func (d *Daemon) resolveChatSession(machine, session string) (string, error) {
	endpoint, local, ok := protocol.ParseID(session)
	if !ok {
		return "", fmt.Errorf("%q is not a namespaced session id", session)
	}
	if me := d.machineID(); me != "" && (endpoint != me || (machine != "" && machine != me)) {
		return "", fmt.Errorf("session %q binds machine %q; this machine is %q", session, endpoint, me)
	}
	return local, nil
}

// composerSend applies one accepted composer_send (Mirror M2.4). The precondition and the
// injection record are taken under ONE itemMu hold, so the turn the send was checked
// against and the correlation the echo will consume cannot be split by a concurrent
// capture.
func (d *Daemon) composerSend(machine, operationID string, req protocol.ComposerSendReq) (protocol.ErrorCode, error) {
	local, err := d.resolveChatSession(machine, req.Session)
	if err != nil {
		return protocol.CodeInvalidField, errIsLife5("composer send: %v", err)
	}
	if _, ok := d.core.Get(local); !ok {
		return protocol.CodeInvalidField, errIsLife5(
			"composer send names session %q, which is not one this daemon runs; OK here would be a sent message no agent received", req.Session)
	}
	if code, err := d.requireStructuredComposer(local, req.Session); err != nil {
		return code, err
	}

	// THE SINK IS RESOLVED BEFORE ANYTHING IS WRITTEN, AND BEFORE itemMu (Wave R7, ADR-013
	// §R7.5). Until R7 this function called injectComposerText -- text plus a CR into the PTY
	// -- for EVERY PROVIDER, with no seam and no provider check anywhere on the path, so a
	// phone send to a Codex session typed into the Codex TUI. That is what playbook §8.2
	// forbids in as many words, and R7 closes it STRUCTURALLY rather than by naming a
	// provider here.
	//
	// It sits OUTSIDE the hold because resolveAdapter takes itemMu itself and a Go mutex is
	// not reentrant. Nothing is lost by that: the resolution reads the session's adapter and
	// its backend registration, neither of which itemMu guards, while the two facts that MUST
	// not be split -- the turn the send was checked against and the correlation its echo will
	// consume -- are both inside the hold below.
	sink, code, serr := d.resolveMessageSink(local, req.Session)
	if serr != nil {
		return code, serr
	}

	d.itemMu.Lock()
	d.initInteractionsLocked()
	current := d.turnIDs[local]
	// The CLI's OWN id for that same turn, read under the SAME hold: a steer names it as a
	// precondition, and reading it in a second hold would let a turn close in between and
	// send the daemon's answer against a turn the CLI has already replaced.
	native := d.nativeTurns[local]
	if req.ExpectedTurn != current {
		// The render-vs-tap race (IS-LIFE-5): a newer user_message opened a new turn, or
		// the turn closed on a terminal agent_message. Refused, never misapplied -- and it
		// is a precondition, not a lockout: the same send against the CURRENT turn goes in.
		d.itemMu.Unlock()
		return protocol.CodeStaleTurn, errIsLife5(
			"expected_turn %q is not the session's current turn; the conversation moved on -- re-read it and send again", req.ExpectedTurn)
	}
	if d.pendingSends == nil {
		d.pendingSends = map[string][]pendingSend{}
	}
	pending := pendingSend{text: req.Text, operationID: operationID, at: d.chatNow()}
	if sink.backend != nil {
		// The echo key is minted HERE, sent with the message, and read straight back off the
		// item's `clientId`. Minting it under the same itemMu hold as the precondition means
		// the turn the send was checked against and the correlation its echo will consume
		// cannot be split by a concurrent capture.
		pending.clientRef = newComposerClientRef()
	}
	q := append(d.pendingSends[local], pending)
	if len(q) > maxPendingSends {
		q = q[len(q)-maxPendingSends:]
	}
	d.pendingSends[local] = q
	d.itemMu.Unlock()

	if err := d.deliverComposerText(local, sink, current, native, req.Text, pending.clientRef); err != nil {
		// The message never reached the agent, so the correlation recorded above will never
		// match an echo; withdraw it rather than letting a later identical owner prompt
		// inherit a phone attribution.
		d.dropPendingSend(local, operationID)
		return "", errIsLife5("composer send into session %q: %v", req.Session, err)
	}
	return "", nil
}

// messageSink is how ONE session's remote messages reach its agent. Exactly one of the two
// members is set; there is no third state, because "neither" is a refusal that never gets this
// far (resolveMessageSink).
type messageSink struct {
	backend  *sessionBackend
	keystrok adapter.KeystrokeComposer
}

// resolveMessageSink is §R7.5's three-branch resolution, in this order:
//
//	live backend  -> the app-server RPC (turn/start when idle, turn/steer mid-turn)
//	no backend    -> the adapter's KEYSTROKE seam, IF IT PROVES ONE
//	neither       -> refuse structured_unsupported, HAVING TYPED NOTHING
//
// The order is what makes §8.2 structural rather than accidental. The Codex adapter implements
// no keystroke seam and never will, so the second branch is STRUCTURALLY UNREACHABLE for it --
// ADR-010 §5's "absence is a signal" doing the work a provider name would otherwise have to do
// -- and the backend branch is checked FIRST so a Codex approval is never typed on the one day
// the backend crashed.
//
// It takes NO lock of its own beyond the ones its callees take, and must NOT be called with
// itemMu held: resolveAdapter acquires it.
func (d *Daemon) resolveMessageSink(local, session string) (messageSink, protocol.ErrorCode, error) {
	if b, ok := d.sessionBackendFor(local); ok {
		return messageSink{backend: b}, "", nil
	}
	m, ok := d.core.Get(local)
	if !ok {
		return messageSink{}, protocol.CodeInvalidField, errIsLife5(
			"composer send names session %q, which is not one this daemon runs", session)
	}
	ad, ok := d.resolveAdapter(m.AgentType)
	if !ok {
		return messageSink{}, protocol.CodeStructuredUnsupported, errIsLife5(
			"agent %q has no adapter, so this session has no message sink; nothing was typed", m.AgentType)
	}
	kc, ok := adapter.AsKeystrokeComposer(ad)
	if !ok {
		return messageSink{}, protocol.CodeStructuredUnsupported, errIsLife5(
			"session %q has no live backend and agent %q proves no keystroke composer seam, so there "+
				"is no way to deliver this message; nothing was typed", session, m.AgentType)
	}
	return messageSink{keystrok: kc}, "", nil
}

// deliverComposerText writes one message through the resolved sink.
//
// ON THE BACKEND BRANCH: turn/start when the daemon's turn is EMPTY, turn/steer when it is not.
// The steer carries the CLI'S OWN optimistic-concurrency guard, `expectedTurnId`, which the
// generated binding documents as "Required active turn id precondition. The request fails when
// it does not match the currently active turn." R1 note 4 says to PROPAGATE it rather than
// invent a Swarm-side one. Dispatching turn/start mid-turn instead would QUEUE A SECOND TURN,
// so the owner's question and the phone's would arrive as two separate conversations.
func (d *Daemon) deliverComposerText(local string, sink messageSink, expectedTurn, nativeTurn, text, clientRef string) error {
	if sink.backend == nil {
		return d.injectComposerText(local, sink.keystrok.ComposerKeys(text))
	}
	ctx, cancel := context.WithTimeout(context.Background(), backendCallTimeout)
	defer cancel()
	input := []map[string]any{{"type": "text", "text": text}}
	if expectedTurn != "" && nativeTurn == "" {
		// REFUSED, HAVING SENT NOTHING. A steer must name the CLI's own turn, and this
		// daemon holds none for the turn it just checked -- so there is no id to send.
		// Sending the daemon's ULID instead is exactly review BLOCKING 1: the server's
		// precondition rejects every one of them, and the phone is told a send succeeded.
		return errIsLife5(
			"session %q has an open turn the CLI never named, so no steer can name it either; "+
				"nothing was sent", local)
	}
	if expectedTurn == "" {
		return sink.backend.conn.Call(ctx, "turn/start", map[string]any{
			"threadId":            sink.backend.threadID,
			"clientUserMessageId": clientRef,
			// `input` is an ARRAY of UserInput. Passing an object yields
			// {"code":-32600,"message":"Invalid request: invalid type: map, expected a
			// sequence"} (RECORDED: errors-observed.json).
			"input": input,
		}, nil)
	}
	return sink.backend.conn.Call(ctx, "turn/steer", map[string]any{
		"threadId":            sink.backend.threadID,
		"clientUserMessageId": clientRef,
		// THE CLI'S OWN TURN ID, never the daemon's. `expectedTurnId` is the app-server's
		// optimistic-concurrency precondition against ITS turn table, and the daemon's
		// `turn_id` is a ULID it minted for the phone's benefit (interaction.go's
		// newTurnID). The two are different namespaces, and R7 round 1 sent the wrong one.
		"expectedTurnId": nativeTurn,
		"input":          input,
	}, nil)
}

// requireStructuredComposer is ADR-017 T2 rule 2 / Mirror M5.5 applied to the composer
// (Wave R6 review finding B3): a session whose structured_chat capability is absent or has
// been DEGRADED by a proven structured_gap has NO STRUCTURED COMPOSER, because it has no
// message sink -- the transcript that would show the user's message is the very thing the
// gap ended.
//
// PROBED, BEFORE THIS EXISTED: a session registered {StructuredChat:true, Interrupt:true},
// then markSessionDegraded'd to {StructuredChat:false, TerminalFallback:true}, accepted a
// composer_send, replied OK with no code, and the fake CLI received the text on stdin. The
// user's words went into the agent and the transcript could never show them: the gap
// silently bridged, which is the one move ADR-017 forbids. The only enforcement anywhere was
// ComposerModel.availabilityFor in Kotlin, which no screen calls -- a client-side gate on a
// server-side fact, which is no gate at all.
//
// turn_interrupt has answered this shape correctly since it landed (interrupt_unsupported,
// having typed nothing). This makes the composer symmetric with it.
//
// AN ABSENT RECORD IS NOT A REFUSAL, AND THAT IS A DISCLOSED COMPROMISE WHOSE REASON CHANGED
// IN WAVE R8. It used to read "registerSessionCapabilities has NO PRODUCTION CALLER, so today
// every live session has no record" -- accurate when written, and false since R8 wired the
// authoring path from five call sites (round-3 minor 8). The compromise itself stands, on the
// reason that survived the wiring: authoring is deliberately SILENT while a provider whose
// structured plane is a side process has not dialled yet (backendPlaneDecided), because a
// record authored in that window would pin a perfectly good chat session at
// structured_chat=false FOREVER -- T2 rule 2 makes that degrade one-way. Refusing on absence
// would therefore refuse the composer for every session in its startup window, which reads to
// a user exactly like the defect this gate exists to fix.
//
// So the gate keys on the two POSITIVE facts: the durable degrade marker, and a record that
// exists and says structured_chat is false. The phone's own router is where absence IS a
// refusal (ADR-017 T2-a: no record is the honest status card and no composer), and it can be,
// because by the time a roster reaches a phone the authoring window has closed.
func (d *Daemon) requireStructuredComposer(local, session string) (protocol.ErrorCode, error) {
	if d.sessionDegraded(local) {
		return protocol.CodeStructuredUnsupported, errIsLife5(
			"session %q proved a structured gap and is in terminal fallback; a message sent now could never appear in its transcript, so nothing was typed", session)
	}
	if c, ok := d.sessionCapabilities(local); ok && !c.StructuredChat {
		return protocol.CodeStructuredUnsupported, errIsLife5(
			"session %q has no structured chat capability; nothing was typed", session)
	}
	return "", nil
}

// injectComposerText writes one message into the session's PTY through the shared tap,
// under the r3p submit-boundary discipline: the text frame and the CR that runs it never
// share a write, and submitframe.Gap elapses between them so no downstream batching
// recompresses the two into one PTY read tick (sendMessage's own rule, restated on the
// tap because this path is the daemon's, not a Server lease's).
//
// DISCLOSED GAP -- THE SEND CAN BE MERGED WITH THE OWNER'S DRAFT (Wave R6 review finding
// B13, playbook §8.1 step 3). This writes text + CR into the PTY with NO check that the
// terminal's input region is empty and NO input transaction. If the owner has a half-typed
// line in the CLI's composer when the phone's send lands, the phone's text is APPENDED to it
// and the CR submits the CONCATENATION: a message nobody wrote, which is precisely the harm
// the "refused, never truncated" rule twenty lines up exists to prevent for the other half
// of the same message.
//
// IT IS NOT CLOSED HERE, AND THE REASON IS STRUCTURAL RATHER THAN AN OVERSIGHT.
// ADR-017:175 records the obligation as "IS-LIFE-5 must be amended, IN THE COMMIT THAT
// IMPLEMENTS composer_send" to carry expected_input_revision, whose enforcement is a
// SHIM-WIDE INPUT TRANSACTION: only the shim owns the PTY writer, so only the shim can make
// "read the input revision, refuse if it moved, write" atomic against the owner's own
// keystrokes. internal/shim is out of this wave's scope. The half that could in principle be
// discharged here -- gate the send on the input region being empty -- was measured and is
// NOT reachable either: no adapter interface characterizes the input region (the seams are
// ApprovalApplier, TurnInterrupter, InteractionSource, HostProber), and deriving "the
// composer is empty" from the raw grid would be exactly the never-guess move IS-TOOL-2
// forbids one layer down and that interruptTurn refuses to make. Inventing a heuristic here
// would trade a disclosed gap for an undisclosed wrong answer.
//
// The amendment obligation is therefore discharged IN WRITING (ADR-017's "Deferred,
// disclosed" section, this wave), and docs/verification/r6-chat.md's CANNOT YET states the
// user-visible consequence in as many words.
func (d *Daemon) injectComposerText(local string, keys []byte) error {
	if d.api == nil {
		return errors.New("this daemon has no session tap wired")
	}
	sub, err := d.api.tap.subscribe(local, readWrite)
	if err != nil {
		return fmt.Errorf("tap session %q: %w", local, err)
	}
	defer func() { _ = sub.Close() }()
	if err := sub.Input(keys); err != nil {
		return fmt.Errorf("writing the message into session %q: %w", local, err)
	}
	time.Sleep(submitframe.Gap)
	if err := sub.Input([]byte{'\r'}); err != nil {
		return fmt.Errorf("text delivered, submit not sent into session %q: %w", local, err)
	}
	return nil
}

// dropPendingSend withdraws one recorded injection by operation id (the failed-write path).
func (d *Daemon) dropPendingSend(local, operationID string) {
	d.itemMu.Lock()
	defer d.itemMu.Unlock()
	q := d.pendingSends[local]
	for i, p := range q {
		if p.operationID == operationID {
			d.pendingSends[local] = append(q[:i:i], q[i+1:]...)
			return
		}
	}
}

// stampComposerEchoLocked is the injection-time correlation (Mirror M2.4, 8.1 step 3): the
// CLI echoes an injected prompt back through its own UserPromptSubmit hook, the ADAPTER
// honestly reports owner (it cannot know), and the daemon -- the only party that watched
// the injection -- stamps the item source "phone" with the phone op's id. A prompt that
// matches NO pending injection keeps the adapter's attribution and consumes nothing:
// attribution is a fact the daemon observed, never a guess that the next prompt must be
// the phone's. Caller holds itemMu.
//
// THE CORRELATION IS TIME-BOUNDED (fix-pack B9): an entry older than pendingSendTTL is
// EXPIRED before any match is attempted, and expiry drops it for good. Without the bound the
// only exits were an exact text match, a failed write and the 8-deep FIFO, so a pending
// "yes" waited indefinitely for someone -- anyone -- to type "yes", and the probe that
// stamped an OWNER-typed prompt with the phone's operation_id is what this bound refuses.
// The sweep is over one session's short queue and runs on the echo path only.
func (d *Daemon) stampComposerEchoLocked(sessionID, text, clientRef string, fields map[string]any) {
	q := d.pendingSends[sessionID]
	if len(q) == 0 {
		// Nothing is pending, so nothing can be claimed and nothing needs sweeping. The
		// early return also keeps this off the nil-map write path: pendingSends is created
		// lazily by composerSend, and the overwhelmingly common case is an owner prompt on
		// a session the phone has never sent to.
		return
	}
	now := d.chatNow()
	live := q[:0:0]
	for _, p := range q {
		if now.Sub(p.at) <= pendingSendTTL {
			live = append(live, p)
		}
	}
	defer func() { d.pendingSends[sessionID] = live }()
	// THE EXACT KEY FIRST (Wave R7, ADR-013 §R7.5). When the CLI echoed back the id the
	// daemon minted and sent with the message, the correlation is a FACT and text is never
	// consulted -- so an OWNER-typed "yes" arriving with clientId null keeps the adapter's
	// honest owner attribution even while a phone send of "yes" is pending. That is exactly
	// the probed mis-attribution above, and R7 does not carry it onto a new provider.
	//
	// AN ECHO CARRYING AN ID WE DID NOT MINT MATCHES NOTHING and falls through to no stamp at
	// all -- deliberately, and NOT down to text matching: an id we do not recognize is
	// positive evidence that some other client authored the message.
	if clientRef != "" {
		for i, p := range live {
			if p.clientRef != clientRef {
				continue
			}
			fields["source"] = adapter.SourcePhone
			fields["operation_id"] = p.operationID
			live = append(live[:i:i], live[i+1:]...)
			return
		}
		return
	}
	for i, p := range live {
		// A pending send with its OWN exact key is never claimed by text: it is waiting for
		// an id, and letting a bare echo consume it would put the phone's operation_id on a
		// message the phone did not author.
		if p.clientRef != "" || p.text != text {
			continue
		}
		fields["source"] = adapter.SourcePhone
		fields["operation_id"] = p.operationID
		live = append(live[:i:i], live[i+1:]...)
		return
	}
}

// interruptTurn applies one turn_interrupt (Mirror M2.4): the adapter-declared cancel
// sequence typed into the NAMED TURN through the daemon's own input path -- or a coded
// refusal, having typed NOTHING (stale_turn when the named turn is over,
// interrupt_unsupported when the adapter proves no seam, ADR-017's honest degrade).
func (d *Daemon) interruptTurn(machine, operationID string, req protocol.TurnInterruptReq) (protocol.ErrorCode, error) {
	_ = operationID // an interrupt correlates with no journalled echo; the op resolves on its reply
	session := req.Session
	local, err := d.resolveChatSession(machine, session)
	if err != nil {
		return protocol.CodeInvalidField, fmt.Errorf("turn interrupt: %v", err)
	}
	m, ok := d.core.Get(local)
	if !ok {
		return protocol.CodeInvalidField, fmt.Errorf(
			"turn interrupt names session %q, which is not one this daemon runs", session)
	}
	// THE TURN PRECONDITION (fix-pack B7), composerSend's own check verbatim and for its own
	// stated reason: a tap lands later than it was rendered. Checked BEFORE the adapter is
	// resolved and long before anything is typed, so a stale Stop types NOTHING -- the same
	// posture the interrupt_unsupported refusal below already takes.
	//
	// The empty check is repeated here rather than left to handleTurnInterrupt because a
	// SEAM must carry its own contract: this function is reachable from anywhere the coreAPI
	// is, and "interrupt whatever is running" must have no spelling at any of them.
	if req.ExpectedTurn == "" {
		return protocol.CodeInvalidField, fmt.Errorf(
			"turn interrupt: missing expected_turn; an interrupt names the turn it stops or it is not an interrupt")
	}
	d.itemMu.Lock()
	d.initInteractionsLocked()
	current := d.turnIDs[local]
	native := d.nativeTurns[local]
	d.itemMu.Unlock()
	if req.ExpectedTurn != current {
		return protocol.CodeStaleTurn, fmt.Errorf(
			"turn interrupt: expected_turn %q is not session %q's current turn; the turn it was rendered against is over, and interrupting whatever replaced it -- including a turn the owner just started at the terminal -- is what this refusal exists to prevent",
			req.ExpectedTurn, session)
	}
	// THE BACKEND BRANCH FIRST (Wave R7, M4.4), for the same reason the composer's is first:
	// a session with a live app-server has a NATIVE interrupt, and falling through to a
	// keystroke on the day the backend died is the one thing playbook §8.2 forbids.
	//
	// RECORDED: turn/interrupt returns {} (turn-interrupt.json) and the server immediately
	// emits turn/completed with "status":"interrupted" for that exact turn id
	// (turn-completed-interrupted.json). The TUI displayed the interruption with no keystroke.
	if b, ok := d.sessionBackendFor(local); ok {
		if native == "" {
			// REFUSED, HAVING TYPED AND SENT NOTHING, and this refusal is the one that
			// matters most (review BLOCKING 1). turn/interrupt against an id the server
			// never minted answers `no active turn to interrupt`, which benignInterruptError
			// -- correctly, for its own case -- reports to the phone as SUCCESS. A Stop that
			// stopped nothing and said it worked is worse than a Stop that refused.
			return protocol.CodeInterruptUnsupported, fmt.Errorf(
				"turn interrupt: session %q has an open turn the CLI never named, so there is "+
					"no turn id to interrupt; nothing was typed and nothing was sent", session)
		}
		ctx, cancel := context.WithTimeout(context.Background(), backendCallTimeout)
		defer cancel()
		err := b.conn.Call(ctx, "turn/interrupt", map[string]any{
			"threadId": b.threadID,
			// THE CLI'S OWN TURN ID. req.ExpectedTurn is the daemon's ULID, which is what
			// the phone named and what the precondition above checked; the app-server has
			// never seen it.
			"turnId": native,
		}, nil)
		if err != nil && !benignInterruptError(err) {
			return "", fmt.Errorf("turn interrupt: %v", err)
		}
		return "", nil
	}
	ad, ok := d.resolveAdapter(m.AgentType)
	if !ok {
		return protocol.CodeInterruptUnsupported, fmt.Errorf(
			"agent %q has no adapter; no safe remote interrupt is proven for it", m.AgentType)
	}
	ti, ok := adapter.AsTurnInterrupter(ad)
	if !ok {
		// ADR-017's honest degrade: this provider version proves no semantic interrupt
		// seam, and a guessed keystroke into a CLI whose cancel key nobody recorded is
		// exactly what IS-TOOL-2's never-guess posture forbids one layer down.
		return protocol.CodeInterruptUnsupported, fmt.Errorf(
			"agent %q proves no semantic interrupt seam; nothing was typed", m.AgentType)
	}
	keys := ti.InterruptKeys()
	if len(keys) == 0 {
		return protocol.CodeInterruptUnsupported, fmt.Errorf(
			"agent %q declares an empty interrupt sequence; a Stop that types nothing is refused, not faked", m.AgentType)
	}
	if d.api == nil {
		return "", errors.New("turn interrupt: this daemon has no session tap wired")
	}
	sub, err := d.api.tap.subscribe(local, readWrite)
	if err != nil {
		return "", fmt.Errorf("turn interrupt: tap session %q: %w", session, err)
	}
	defer func() { _ = sub.Close() }()
	if err := sub.Input(keys); err != nil {
		return "", fmt.Errorf("turn interrupt: writing the interrupt into session %q: %w", session, err)
	}
	return "", nil
}

// benignInterruptError reports whether an app-server error means "the turn the owner wanted
// stopped is already stopped".
//
// `turn/interrupt` on an already-finished turn returns
// {"code":-32600,"message":"no active turn to interrupt"} (RECORDED: errors-observed.json).
// The daemon's own stale_turn precondition already refuses that case BEFORE the RPC is sent;
// when the race is lost anyway the outcome the owner asked for HAS HAPPENED, and reporting a
// failure only teaches them to press Stop again. Every other error -- a transport fault, a
// closed connection -- still surfaces, which is exactly why the client returns a TYPED
// *appserver.RPCError rather than a flattened string.
func benignInterruptError(err error) bool {
	var rpcErr *appserver.RPCError
	if !errors.As(err, &rpcErr) {
		return false
	}
	return strings.Contains(rpcErr.Message, "no active turn to interrupt")
}

// interactionHistory serves ADR-014's paged read (Mirror M3.1): the window of this
// session's journalled interaction records immediately preceding before_item -- ascending
// by cursor, bounded by limit -- plus the honest floor ("nothing older is retained").
func (d *Daemon) interactionHistory(session, beforeItem string, limit int) ([]protocol.JournalRecord, bool, protocol.ErrorCode, error) {
	local, err := d.resolveChatSession("", session)
	if err != nil {
		return nil, false, protocol.CodeInvalidField, fmt.Errorf("interaction history: %v", err)
	}
	if limit <= 0 {
		// There is no limit 0: a caller that sends one has a bug this layer surfaces
		// rather than papering over with a default.
		return nil, false, protocol.CodeInvalidField, fmt.Errorf("interaction history: non-positive limit %d", limit)
	}
	if d.api == nil {
		return nil, false, "", errors.New("interaction history: this daemon has no journal wired")
	}
	res, err := d.api.JournalReadFrom(0)
	if err != nil {
		return nil, false, "", fmt.Errorf("interaction history: %v", err)
	}
	var recs []protocol.JournalRecord
	for _, rec := range res.Events {
		if rec.SessionID != local {
			continue
		}
		// `structured_gap` is kept BESIDE `interaction` (Wave R6 review finding B4a).
		// Keeping only interactions dropped every proven capability-degrade boundary, so a
		// history page spanned a tear CONTIGUOUSLY with nothing marking it: the phone
		// rendered the items either side of a discontinuity the daemon had proved, as one
		// conversation. ADR-017's whole posture is that a gap renders honestly and is never
		// silently bridged, and a page that omits the boundary bridges it.
		if rec.Type == "interaction" || rec.Type == structuredGapRecordType {
			recs = append(recs, rec)
		}
	}
	boundary, found := uint64(0), false
	for _, rec := range recs {
		if historyItemID(rec) == beforeItem && beforeItem != "" {
			// The FIRST record of the item: folds append later records under the same id,
			// and "strictly older than before_item" means older than the item began.
			boundary, found = rec.Cursor, true
			break
		}
	}
	if !found {
		return nil, false, protocol.CodeInvalidField, fmt.Errorf(
			"interaction history: before_item %q is not an item of session %q", beforeItem, session)
	}
	var older []protocol.JournalRecord
	for _, rec := range recs {
		if rec.Cursor < boundary {
			older = append(older, rec)
		}
	}
	start := historyPageStart(older, limit)
	return older[start:], start == 0, "", nil
}

// structuredGapRecordType is journal.TypeStructuredGap's wire spelling. internal/skeleton
// may import internal/journal, but this file reads records that have ALREADY crossed to
// protocol.JournalRecord, whose Type is a plain string; comparing against the wire spelling
// keeps the comparison in the same vocabulary as the value.
const structuredGapRecordType = "structured_gap"

// historyItemID is the item id a history record belongs to, or "" for a record that belongs
// to no item (a structured_gap, which is its own atomic boundary and never folds).
func historyItemID(rec protocol.JournalRecord) string {
	if rec.Type != "interaction" || len(rec.Item) == 0 {
		return ""
	}
	var it struct {
		ItemID string `json:"item_id"`
	}
	if json.Unmarshal(rec.Item, &it) != nil {
		return ""
	}
	return it.ItemID
}

// historyPageStart picks the index older[] must be sliced at so the page NEVER BEGINS IN THE
// MIDDLE OF AN ITEM (Wave R6 review finding B5).
//
// THE BUG IT REPLACES was `older[len(older)-limit:]`: a trim by RAW JOURNAL RECORD, over a
// channel the phone pages by ITEM ID. An agent_message grown through IS-DELTA-1 increments
// occupies several records under one item_id, so that slice could deliver an item's TAIL with
// its head missing -- and the phone, which folds by item_id and has no way to know a head is
// absent, renders the fragment as a whole message. Worse, the fragment was then PERMANENTLY
// unreachable: the next page asks for what is older than that item's FIRST RECORD, which is
// below the records just delivered, so nothing ever returns them.
//
// The rule is therefore: take the largest suffix of WHOLE items whose record count fits
// limit. `limit` keeps bounding records (it is a frame-size bound, and items have no uniform
// size), it simply now rounds DOWN to an item boundary instead of cutting through one.
//
// THE ONE ITEM THAT DOES NOT FIT still ships, alone and over limit. Refusing it would return
// an empty page with floor=false -- "there is more, and you may not have it" -- and the phone
// would ask forever for a page it can never receive. An over-limit page is a bounded, honest
// answer; a livelock is not.
func historyPageStart(older []protocol.JournalRecord, limit int) int {
	// Boundaries: the index of each item's FIRST record within older, plus each gap (which
	// is atomic and therefore always its own boundary).
	var bounds []int
	seen := map[string]bool{}
	for i, rec := range older {
		id := historyItemID(rec)
		if id == "" {
			bounds = append(bounds, i)
			continue
		}
		if !seen[id] {
			seen[id] = true
			bounds = append(bounds, i)
		}
	}
	if len(bounds) == 0 {
		return 0
	}
	for _, b := range bounds {
		if len(older)-b <= limit {
			return b
		}
	}
	// Not even the newest item fits: ship it whole anyway. See the doc.
	return bounds[len(bounds)-1]
}

// interactionDetail serves IS-CAP-2's detail read (Mirror M3.3): the full pre-truncation
// body retained at capture time, or IS-CAP-3's `unavailable` -- for an id never captured,
// evicted from the bounded window, or belonging to another session alike.
func (d *Daemon) interactionDetail(session, itemID string) (json.RawMessage, protocol.ErrorCode, error) {
	local, err := d.resolveChatSession("", session)
	if err != nil {
		return nil, protocol.CodeInvalidField, fmt.Errorf("interaction detail: %v", err)
	}
	d.itemMu.Lock()
	defer d.itemMu.Unlock()
	if body, ok := d.details[local][itemID]; ok {
		return append(json.RawMessage(nil), body...), "", nil
	}
	return nil, protocol.CodeUnavailable, fmt.Errorf(
		"interaction detail: no full body for item %q of session %q is retained (IS-CAP-3)", itemID, session)
}

// detailKey names one retained body in insertion order, across every session: the byte
// bound is daemon-wide (M3.3's one 64 MiB store), so the order must be too.
type detailKey struct{ session, item string }

// retainDetailLocked stores one item's full pre-truncation body for the detail read,
// evicting oldest-first once the store's TOTAL bytes pass maxDetailBytes. Caller holds
// itemMu.
func (d *Daemon) retainDetailLocked(sessionID, itemID string, full json.RawMessage) {
	if d.details == nil {
		d.details = map[string]map[string][]byte{}
	}
	if d.details[sessionID] == nil {
		d.details[sessionID] = map[string][]byte{}
	}
	if prev, exists := d.details[sessionID][itemID]; exists {
		d.detailBytes -= len(prev)
	} else {
		d.detailOrder = append(d.detailOrder, detailKey{session: sessionID, item: itemID})
	}
	d.details[sessionID][itemID] = append([]byte(nil), full...)
	d.detailBytes += len(full)
	for d.detailBytes > maxDetailBytes && len(d.detailOrder) > 0 {
		k := d.detailOrder[0]
		d.detailOrder = d.detailOrder[1:]
		if body, ok := d.details[k.session][k.item]; ok {
			d.detailBytes -= len(body)
			delete(d.details[k.session], k.item)
		}
	}
}
