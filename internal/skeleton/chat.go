package skeleton

// Wave R6's daemon-side complete chat (bead agents-tracker-hggx.7): the application halves
// of the four ops internal/protocol/remote_chat.go serves.
//
//   - composerSend (Mirror M2.4, IS-LIFE-5, playbook §8.1 step 3): refuse a session whose
//     structured chat has degraded (ADR-017 T2 rule 2, fix-pack B3), serialize messages per
//     session, select the CURRENT provider turn for each queue head, deliver through the
//     proven backend/keystroke sink, and remember the injection for a BOUNDED window so the
//     echoed UserPromptSubmit journals source "phone" with the op's id. expected_turn remains
//     signed render context; unlike Stop it is advisory for conversational messages.
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
	"sync"
	"sync/atomic"
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

// testHookComposerCheckedNotYetDelivered, when set, is invoked inside composerSend after
// the request enters its per-session lane and immediately before current turn selection.
// It exists so tests can drive a turn transition at that boundary without timing races.
// Always unset in production (mirroring internal/shim's testHookAfterPTYResize).
//
// IT IS AN ATOMIC AND NOT A BARE VAR because the two accesses genuinely race. Tests install
// and clear it from the test goroutine while composerSend reads it on whichever goroutine is
// serving the send, and a t.Cleanup that clears it runs BEFORE the rig teardown registered
// ahead of it -- cleanups are LIFO -- so the daemon is still running sends at the moment the
// hook is nilled. -race reports it, and a scaffolding race in a package whose own subject is
// concurrency teaches the next reader to skim -race output.
var testHookComposerCheckedNotYetDelivered atomic.Pointer[func(local string)]

// testHookInterruptValidatedNotYetBarrier is a deterministic concurrency seam for the
// strict Stop precondition. Production leaves it nil. Tests may pause after expected_turn
// has been validated while lane.mu and itemMu are still held, proving no composer retry can
// slip between that validation and barrier publication.
var testHookInterruptValidatedNotYetBarrier atomic.Pointer[func(local string)]

// testHookComposerBegunNotYetSubmitted pauses the shim-backed path after the durable Begin
// callback and immediately before Submit. It drives the otherwise instruction-sized Stop race:
// production leaves it nil.
var testHookComposerBegunNotYetSubmitted atomic.Pointer[func(local string)]

// testHookComposerBackendBegunNotYetCalled drives the backend counterpart of the shim race:
// after durable Begin but before the JSON-RPC request crosses the connection write boundary.
var testHookComposerBackendBegunNotYetCalled atomic.Pointer[func(local string)]

// composerLane is the daemon's per-session semantic send coordinator. Relay delivery is
// ordered for one phone, but the daemon is the first layer that knows which native provider
// turn is current. The reservation bridges the normal app-server race in which turn/start has
// returned its turn id but turn/started has not reached the event pump yet.
type composerLane struct {
	mu                  sync.Mutex
	ready               *sync.Cond
	nextTicket          uint64
	servingTicket       uint64
	barrier             uint64
	stopsInFlight       uint64
	reservedNativeTurn  string
	closedAtReservation string
	// supersededTurn is the daemon turn a typed provider steer refusal proved has ended.
	// It is published at the retry turn/start write boundary, before that call's reply can
	// reveal the replacement's native id. Until the event pump folds current state, a Stop
	// rendered against supersededTurn is stale; reservedNativeTurn, once known, routes later
	// queued messages to the replacement.
	supersededTurn    string
	uncertain         bool
	uncertainTurn     string
	uncertainClosed   string
	uncertainProgress uint64
	progress          atomic.Uint64
}

// withComposerInteractionState is the one nesting boundary for state shared by the
// per-session composer lane and the folded interaction model. Stop and composer retry both
// need supersededTurn, while current/native/closed turn coordinates live under itemMu. Taking
// the locks here keeps the declared lane.mu -> itemMu order explicit and gives every
// supersededTurn access one synchronization domain. The callback must stay memory-only: no
// provider, shim, journal or filesystem I/O belongs under either lock.
func (d *Daemon) withComposerInteractionState(lane *composerLane, fn func()) {
	lane.mu.Lock()
	d.itemMu.Lock()
	fn()
	d.itemMu.Unlock()
	lane.mu.Unlock()
}

func (d *Daemon) composerLaneFor(local string) *composerLane {
	candidate := &composerLane{}
	candidate.ready = sync.NewCond(&candidate.mu)
	lane, _ := d.composerLanes.LoadOrStore(local, candidate)
	return lane.(*composerLane)
}

// enter assigns an explicit FIFO ticket and returns when that ticket is at the head. The
// bookkeeping lock is released during provider I/O so later arrivals can take their place in
// the queue; only the serving ticket executes. A plain mutex is not sufficient as a product
// guarantee because Go does not promise waiter acquisition order.
func (l *composerLane) enter() uint64 {
	l.mu.Lock()
	ticket := l.nextTicket
	l.nextTicket++
	admittedBarrier := l.barrier
	for ticket != l.servingTicket || l.stopsInFlight != 0 {
		l.ready.Wait()
	}
	l.mu.Unlock()
	return admittedBarrier
}

func (l *composerLane) endStop() {
	l.mu.Lock()
	if l.stopsInFlight > 0 {
		l.stopsInFlight--
	}
	l.ready.Broadcast()
	l.mu.Unlock()
}

// uncertainNow reports whether the lane holds an unresolved composer outcome.
// The context guard's dispatch revalidation reads it at its queue head
// (ADR-023 D6): an automatic compaction never rides over an operation whose
// effect on the provider is still undecided.
func (l *composerLane) uncertainNow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.uncertain
}

func (l *composerLane) barrierChanged(admitted uint64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.barrier != admitted
}

func (l *composerLane) leave() {
	l.mu.Lock()
	l.servingTicket++
	l.ready.Broadcast()
	l.mu.Unlock()
}

// composerSend applies one accepted composer_send (Mirror M2.4). expected_turn is signed
// render context, not a destructive-target precondition: messages are serialized per session
// and each queue head is dispatched against the daemon's current provider turn. Stop retains
// its strict expected_turn precondition in interruptTurn.
func (d *Daemon) composerSend(machine, operationID string, req protocol.ComposerSendReq) (protocol.ErrorCode, error) {
	return d.composerSendTransactional(machine, operationID, req, nil)
}

func (d *Daemon) composerSendTransactional(machine, operationID string, req protocol.ComposerSendReq, begin func() error) (protocol.ErrorCode, error) {
	local, err := d.resolveChatSession(machine, req.Session)
	if err != nil {
		return protocol.CodeInvalidField, errIsLife5("composer send: %v", err)
	}
	lane := d.composerLaneFor(local)
	admittedBarrier := lane.enter()
	defer lane.leave()
	if lane.barrierChanged(admittedBarrier) {
		return protocol.CodeStaleTurn, errIsLife5("composer send was queued before Stop completed; nothing was sent")
	}
	if _, ok := d.core.Get(local); !ok {
		return protocol.CodeInvalidField, errIsLife5(
			"composer send names session %q, which is not one this daemon runs; OK here would be a sent message no agent received", req.Session)
	}
	if req.SessionInstance != "" {
		currentInstance, ok := d.sessionInstance(local)
		if !ok || currentInstance != req.SessionInstance {
			return protocol.CodeStaleInstance, errIsLife5(
				"composer send names session incarnation %q, but %q is current; refresh before sending", req.SessionInstance, currentInstance)
		}
	}
	if code, err := d.requireStructuredComposer(local, req.Session); err != nil {
		return code, err
	}
	// Resolve the sink only after this request reaches the queue head. A backend may be
	// replaced or disappear while earlier messages are in provider I/O.
	sink, code, serr := d.resolveMessageSink(local, req.Session)
	if serr != nil {
		return code, serr
	}

	// This test seam now drives a turn transition immediately before the coordinator
	// selects current state. It proves a queued message follows the live conversation
	// instead of being rejected because the phone rendered an older turn.
	if hook := testHookComposerCheckedNotYetDelivered.Load(); hook != nil {
		(*hook)(local)
	}

	var current, native, closed string
	var active bool
	var pending pendingSend
	var priorUncertain bool
	d.withComposerInteractionState(lane, func() {
		d.initInteractionsLocked()
		current = d.turnIDs[local]
		// The CLI's OWN id for that same turn, read under the SAME hold: a steer names it as a
		// precondition, and reading it in a second hold would let a turn close in between and
		// send the daemon's answer against a turn the CLI has already replaced.
		native = d.nativeTurns[local]
		closed = d.closedTurns[local]
		progress := lane.progress.Load()
		if lane.uncertain {
			if current == lane.uncertainTurn && closed == lane.uncertainClosed && progress == lane.uncertainProgress {
				priorUncertain = true
				return
			}
			lane.uncertain = false
		}
		active = current != ""
		if active && lane.reservedNativeTurn != "" && lane.supersededTurn == current {
			// The provider already rejected a steer for current and accepted a retry start,
			// but its completion/start events have not reached the fold yet. The returned
			// native id is the actual active destination for the next FIFO head.
			native = lane.reservedNativeTurn
		} else if active {
			// The event pump has authoritative state now; any start reservation has served its
			// purpose and must not survive a later close/reopen.
			lane.reservedNativeTurn = ""
			lane.closedAtReservation = ""
			lane.supersededTurn = ""
		} else if lane.reservedNativeTurn != "" && (lane.supersededTurn != "" || lane.closedAtReservation == closed) {
			// turn/start returned before turn/started was folded. Use the returned native id
			// so a second queued message steers rather than starts a competing turn.
			active = true
			native = lane.reservedNativeTurn
		} else {
			// A terminal event advanced the close marker, so an older reservation is dead.
			lane.reservedNativeTurn = ""
			lane.closedAtReservation = ""
			lane.supersededTurn = ""
		}
		if d.pendingSends == nil {
			d.pendingSends = map[string][]pendingSend{}
		}
		pending = pendingSend{text: req.Text, operationID: operationID, at: d.chatNow()}
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
	})
	if priorUncertain {
		return protocol.CodeOutcomeUnknown, fmt.Errorf("%w: a prior message has no provider outcome yet; no later message was allowed to overtake it", protocol.ErrComposerOutcomeUnknown)
	}
	if lane.barrierChanged(admittedBarrier) {
		d.dropPendingSend(local, operationID)
		return protocol.CodeStaleTurn, errIsLife5("composer send was superseded by Stop before delivery; nothing was sent")
	}
	startedNative, err := d.deliverComposerText(local, lane, admittedBarrier, sink, active, native, req.Text, pending.clientRef, begin)
	if err != nil {
		// The message never reached the agent, so the correlation recorded above will never
		// match an echo; withdraw it rather than letting a later identical owner prompt
		// inherit a phone attribution.
		if errors.Is(err, protocol.ErrComposerOutcomeUnknown) {
			stillPending := false
			d.withComposerInteractionState(lane, func() {
				for _, candidate := range d.pendingSends[local] {
					if candidate.operationID == operationID {
						stillPending = true
						break
					}
				}
				if !stillPending {
					return
				}
				lane.uncertain = true
				lane.uncertainTurn = d.turnIDs[local]
				lane.uncertainClosed = d.closedTurns[local]
				lane.uncertainProgress = lane.progress.Load()
			})
			if !stillPending {
				// The provider's matching echo is stronger evidence than a lost RPC reply:
				// this exact operation was folded and consumed its pending correlation.
				// Treat it as delivered and do not poison later FIFO heads with an
				// uncertainty snapshot taken after the proof already advanced.
				d.notePhoneActivity(local)
				return "", nil
			}
			return protocol.CodeOutcomeUnknown, err
		}
		d.dropPendingSend(local, operationID)
		if errors.Is(err, errComposerStopped) {
			return protocol.CodeStaleTurn, errIsLife5("composer send was superseded by Stop; nothing was sent")
		}
		if errors.Is(err, protocol.ErrInputBusy) {
			// ITS OWN CODE, because it has its own remedy: nothing is wrong with the
			// caller, the session or the message -- the line simply was not clean, and
			// the same words sent a moment later will land. NOTHING WAS WRITTEN.
			return protocol.CodeInputBusy, errIsLife5(
				"session %q had input on its line, so this message was not written; nothing was typed", req.Session)
		}
		return "", errIsLife5("composer send into session %q: %w", req.Session, err)
	}
	if startedNative != "" {
		// Only retain the returned id while no folded event has opened or closed a turn
		// since selection. If the event pump won the race, its state is authoritative.
		d.withComposerInteractionState(lane, func() {
			if d.turnIDs[local] == "" && d.closedTurns[local] == closed {
				lane.reservedNativeTurn = startedNative
				lane.closedAtReservation = closed
				lane.supersededTurn = ""
			}
		})
	}
	// DELIVERED, so somebody is driving this session from a phone (ADR-010 Amendment 3 C3).
	// Recorded only on the success path: a message the machine turned away is not somebody
	// driving anything.
	d.notePhoneActivity(local)
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

// deliverComposerText writes one queue head through the resolved sink. The caller has selected
// current state while holding itemMu and owns the session lane for the entire provider call.
// Backend idle uses turn/start; active uses turn/steer with the CLI's native id. A typed
// no-active-turn response means the provider completed the selected turn before the event pump
// folded it, so this retries once against freshly resolved state using the same client id.
// Keystroke delivery retains the shim's atomic whole-submit/input-busy protection.
var errComposerStopped = errors.New("composer send superseded by Stop")

func (d *Daemon) deliverComposerText(local string, lane *composerLane, admittedBarrier uint64, sink messageSink, active bool, nativeTurn, text, clientRef string, begin func() error) (string, error) {
	if lane.barrierChanged(admittedBarrier) {
		return "", errComposerStopped
	}
	if sink.backend == nil {
		return "", d.injectComposerTextTransactional(
			local, sink.keystrok.ComposerKeys(text), lane, admittedBarrier, begin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), backendCallTimeout)
	defer cancel()
	input := []map[string]any{{"type": "text", "text": text}}
	if active && nativeTurn == "" {
		// REFUSED, HAVING SENT NOTHING. A steer must name the CLI's own turn, and this
		// daemon holds none for the turn it just checked -- so there is no id to send.
		// Sending the daemon's ULID instead is exactly review BLOCKING 1: the server's
		// precondition rejects every one of them, and the phone is told a send succeeded.
		return "", errIsLife5(
			"session %q has an open turn the CLI never named, so no steer can name it either; "+
				"nothing was sent", local)
	}
	if !active {
		var result struct {
			Turn struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if err := callComposerBackendAtWriteBoundary(ctx, local, lane, admittedBarrier,
			sink.backend.conn, "turn/start", map[string]any{
				"threadId":            sink.backend.threadID,
				"clientUserMessageId": clientRef,
				// `input` is an ARRAY of UserInput. Passing an object yields
				// {"code":-32600,"message":"Invalid request: invalid type: map, expected a
				// sequence"} (RECORDED: errors-observed.json).
				"input": input,
			}, &result, begin); err != nil {
			return "", classifyComposerBackendError(err)
		}
		if result.Turn.ID == "" {
			return "", fmt.Errorf("%w: session %q turn/start returned no native turn id; a queued follow-up cannot be steered safely", protocol.ErrComposerOutcomeUnknown, local)
		}
		return result.Turn.ID, nil
	}
	err := callComposerBackendAtWriteBoundary(ctx, local, lane, admittedBarrier,
		sink.backend.conn, "turn/steer", map[string]any{
			"threadId":            sink.backend.threadID,
			"clientUserMessageId": clientRef,
			// THE CLI'S OWN TURN ID, never the daemon's. `expectedTurnId` is the app-server's
			// optimistic-concurrency precondition against ITS turn table, and the daemon's
			// `turn_id` is a ULID it minted for the phone's benefit (interaction.go's
			// newTurnID). The two are different namespaces, and R7 round 1 sent the wrong one.
			"expectedTurnId": nativeTurn,
			"input":          input,
		}, nil, begin)
	if err == nil {
		return "", nil
	}
	if !noActiveTurnToSteer(err) {
		return "", classifyComposerBackendError(err)
	}
	// The native turn completed after this queue head selected it. Re-resolve against
	// current daemon state and retry once with the SAME client id: if a newer turn has
	// already been folded, steer it; otherwise the provider's typed answer is the freshest
	// authority and this message starts the next turn. The refused steer performed no send.
	// Select retry state under lane.mu, then use the same narrow write boundary as the initial
	// call. Stop either publishes first (and the boundary sends nothing), follows a replacement
	// start write and sees supersededTurn, or follows a steer write against the freshly folded
	// current turn. None of those requires waiting up to 30 seconds for the provider reply.
	barrierChanged := false
	var current, latestNative, closed string
	d.withComposerInteractionState(lane, func() {
		if lane.barrier != admittedBarrier {
			barrierChanged = true
			return
		}
		current = d.turnIDs[local]
		latestNative = d.nativeTurns[local]
		closed = d.closedTurns[local]
	})
	if barrierChanged {
		return "", errComposerStopped
	}
	if current != "" && latestNative != "" && latestNative != nativeTurn {
		err := callComposerBackendAtWriteBoundary(ctx, local, lane, admittedBarrier,
			sink.backend.conn, "turn/steer", map[string]any{
				"threadId": sink.backend.threadID, "clientUserMessageId": clientRef,
				"expectedTurnId": latestNative, "input": input,
			}, nil, nil)
		return "", classifyComposerBackendError(err)
	}
	var result struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := callComposerBackendAtWriteBoundaryPrepared(ctx, local, lane, admittedBarrier,
		sink.backend.conn, "turn/start", map[string]any{
			"threadId": sink.backend.threadID, "clientUserMessageId": clientRef, "input": input,
		}, &result, nil, func() {
			// The typed no-active-turn steer already proved this daemon turn is dead. Publish
			// that fact atomically with the replacement request write so Stop can refuse the
			// obsolete target without waiting for the reply that carries the new native id.
			if current != "" {
				lane.supersededTurn = current
			}
		}); err != nil {
		return "", classifyComposerBackendError(err)
	}
	if result.Turn.ID == "" {
		return "", fmt.Errorf("%w: session %q retry turn/start returned no native turn id", protocol.ErrComposerOutcomeUnknown, local)
	}
	d.withComposerInteractionState(lane, func() {
		lane.reservedNativeTurn = result.Turn.ID
		lane.closedAtReservation = closed
		lane.supersededTurn = current
	})
	return "", nil
}

type backendWriteBoundary interface {
	CallAtWriteBoundary(ctx context.Context, method string, params, out any, beforeWrite func() error, afterWrite func()) error
}

func callComposerBackendAtWriteBoundary(ctx context.Context, local string, lane *composerLane, admittedBarrier uint64, conn backendConn, method string, params, out any, begin func() error) error {
	return callComposerBackendAtWriteBoundaryPrepared(ctx, local, lane, admittedBarrier,
		conn, method, params, out, begin, nil)
}

func callComposerBackendAtWriteBoundaryPrepared(ctx context.Context, local string, lane *composerLane, admittedBarrier uint64, conn backendConn, method string, params, out any, begin func() error, prepare func()) error {
	beforeWrite := func() error {
		lane.mu.Lock()
		if lane.barrier != admittedBarrier {
			lane.mu.Unlock()
			return errComposerStopped
		}
		if err := beginComposerDelivery(begin); err != nil {
			lane.mu.Unlock()
			return err
		}
		if prepare != nil {
			prepare()
		}
		if begin != nil {
			if hook := testHookComposerBackendBegunNotYetCalled.Load(); hook != nil {
				(*hook)(local)
			}
		}
		return nil // lane remains locked through the request write
	}
	if bounded, ok := conn.(backendWriteBoundary); ok {
		return bounded.CallAtWriteBoundary(ctx, method, params, out, beforeWrite, func() {
			lane.mu.Unlock()
		})
	}
	// Compatibility for small/older backend implementations without a write-boundary seam.
	// Production *appserver.Client always takes the atomic branch above. Keep Stop responsive
	// here rather than holding the lane for an opaque implementation's whole reply wait.
	if err := beforeWrite(); err != nil {
		return err
	}
	lane.mu.Unlock()
	return conn.Call(ctx, method, params, out)
}

func beginComposerDelivery(begin func() error) error {
	if begin == nil {
		return nil
	}
	if err := begin(); err != nil {
		return fmt.Errorf("%w: durable composer execution boundary: %v", protocol.ErrComposerOutcomeUnknown, err)
	}
	return nil
}

func classifyComposerBackendError(err error) error {
	if err == nil {
		return nil
	}
	// Errors raised by our own write-boundary callbacks already carry exact semantics:
	// Stop won before the request write (definite non-delivery), or durable Begin failed
	// after the operation crossed into an indeterminate execution phase. Reclassifying
	// either as an opaque transport failure would turn stale_turn into outcome_unknown or
	// erase the durable sentinel respectively.
	if errors.Is(err, errComposerStopped) || errors.Is(err, protocol.ErrComposerOutcomeUnknown) {
		return err
	}
	var rpcErr *appserver.RPCError
	if errors.As(err, &rpcErr) {
		return err // typed provider refusal proves the request was rejected
	}
	return fmt.Errorf("%w: %v", protocol.ErrComposerOutcomeUnknown, err)
}

func noActiveTurnToSteer(err error) bool {
	var rpcErr *appserver.RPCError
	return errors.As(err, &rpcErr) && rpcErr.Code == -32600 &&
		strings.Contains(rpcErr.Message, "no active turn to steer")
}

// requireStructuredComposer is ADR-017 T2 rule 2 / Mirror M5.5 applied to the composer
// (Wave R6 review finding B3): a session whose structured_chat capability is absent or has
// been DEGRADED by a proven structured_gap has NO STRUCTURED COMPOSER until the exact
// current instance freshly proves its message sink. The durable gap marker remains visible
// forever; only current/future delivery authority can recover.
//
// PROBED, BEFORE THIS EXISTED: a session registered {StructuredChat:true, Interrupt:true},
// then markSessionDegraded'd to {StructuredChat:false, TerminalFallback:true}, accepted a
// composer_send, replied OK with no code, and the fake CLI received the text on stdin. The
// user's words went into the agent and the transcript could never show them: the gap
// silently bridged, which is the one move ADR-017 forbids. The only enforcement anywhere was
// ComposerModel.availabilityFor in Kotlin -- a client-side check that cannot enforce a
// server-side fact. SessionDetailPanel calls it today; the server gate is still required.
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
// structured_chat=false until an exact live backend proof exists. Refusing on absence
// would therefore refuse the composer for every session in its startup window, which reads to
// a user exactly like the defect this gate exists to fix.
//
// So the gate keys on the proof-aware record when one exists: false refuses, while true may
// coexist with the durable history marker only after an ordered exact-instance recovery.
// A marker with no record also refuses. Absence without a marker retains the startup-window
// compatibility compromise; the phone's router remains fail-closed on absence.
func (d *Daemon) requireStructuredComposer(local, session string) (protocol.ErrorCode, error) {
	if c, ok := d.sessionCapabilities(local); ok {
		if !c.StructuredChat {
			return protocol.CodeStructuredUnsupported, errIsLife5(
				"session %q has no currently proven structured chat sink; nothing was typed", session)
		}
		return "", nil
	}
	if d.sessionDegraded(local) {
		return protocol.CodeStructuredUnsupported, errIsLife5(
			"session %q proved a structured gap before a capability record existed; nothing was typed", session)
	}
	return "", nil
}

// injectComposerText writes one message into the session's PTY through the shared tap,
// under the r3p submit-boundary discipline: the text frame and the CR that runs it never
// share a write, and submitframe.Gap elapses between them so no downstream batching
// recompresses the two into one PTY read tick (sendMessage's own rule, restated on the
// tap because this path is the daemon's, not a Server lease's).
//
// B13 IS CLOSED HERE (Slice 0, agents-tracker-bzfe), and what closed it is deliberately
// weaker than what was once thought necessary.
//
// WHAT THE GAP WAS. This wrote text + CR into the PTY with NO check that the terminal's
// input region was empty and NO transaction. If the owner had a half-typed line in the
// CLI's composer when the phone's send landed, the phone's text was APPENDED to it and the
// CR submitted the CONCATENATION: a message nobody wrote. The same hole admitted a SECOND
// PHONE SEND -- both pass expected_turn, because the turn id only advances when the CLI
// echoes -- producing one concatenation and one empty submit, which is the shape a chat
// surface produces the moment a person fires three short messages in a row.
//
// WHY IT LOOKED STRUCTURAL. ADR-017:175 framed the obligation as expected_input_revision,
// and enforcing THAT does require characterizing the CLI's input region -- which no adapter
// seam exposes (they are ApprovalApplier, TurnInterrupter, InteractionSource, HostProber)
// and which deriving from the raw grid would be exactly the never-guess move IS-TOOL-2
// forbids one layer down. That reasoning was right, and it is why the answer is a different
// question.
//
// THE QUESTION THAT IS ANSWERABLE. The shim owns the PTY's only serialized writer, so under
// that lock it can ask whether the owner's logical input line is PROVABLY CLEAN from the
// input stream it serialized, without reading the agent's grid. It tracks only operations
// with characterized effects: character insertion/deletion, complete horizontal/home/end
// navigation, line kill and submit. Provider-dependent word/Meta keys and lone/incomplete
// escape sequences remain unknown and busy. shimwire.TypeSubmit checks that predicate,
// writes the text, waits submitframe.Gap and writes the CR under ONE hold of the writer's
// lock, or refuses having written nothing. A known draft deleted back to empty becomes
// clean; uncertainty stays safe. protocol.ErrInputBusy becomes the wire's CodeInputBusy,
// so the phone can retain and retry rather than silently join somebody else's input.
// shimwire.TypeControlInput frames bypass owner-input tracking by provenance: the daemon's
// own interrupt and dialog-answer keys do not poison the next send; the residual is an
// approval key landing on an already-closed dialog, one untracked character.
//
// WHAT REMAINS. A shim that predates the transaction answers ErrSubmitUnsupported and this
// falls through to the two unlocked writes below -- reachable only mid-upgrade, and
// resolved by the daemon restart that replaces the shim. The merge is also exclusively a
// property of THIS keystroke branch: resolveMessageSink's backend arm never touches the
// PTY, and the only ComposerKeys implementor in the tree is Claude, so the class has a
// known exit the day Claude gains a structured sink.
func (d *Daemon) injectComposerTextTransactional(local string, keys []byte, lane *composerLane, admittedBarrier uint64, begin func() error) error {
	if d.api == nil {
		return errors.New("this daemon has no session tap wired")
	}
	sub, err := d.api.tap.subscribe(local, readWrite)
	if err != nil {
		return fmt.Errorf("tap session %q: %w", local, err)
	}
	defer func() { _ = sub.Close() }()
	// Tap acquisition may dial and block, but it cannot deliver message bytes. Serialize the
	// final Stop check, durable Begin and whole shim Submit under lane.mu so their order is
	// indivisible: either Stop publishes first and this sends nothing, or Submit starts/finishes
	// before Stop can report success. Stop uses the same lane.mu -> itemMu lock order.
	lane.mu.Lock()
	defer lane.mu.Unlock()
	if lane.barrier != admittedBarrier {
		return errComposerStopped
	}
	if err := beginComposerDelivery(begin); err != nil {
		return err
	}
	if hook := testHookComposerBegunNotYetSubmitted.Load(); hook != nil {
		(*hook)(local)
	}

	// THE TRANSACTION, WHERE THE SHIM PROVES ONE (Slice 0). Text and CR cross the PTY's
	// single serialized writer under one hold of its lock, refusing rather than writing
	// into a line somebody else is holding. The disclosed gap this file has carried since
	// Wave R6 closes here for every shim that advertises it.
	serr := sub.Submit(string(keys))
	if !errors.Is(serr, protocol.ErrSubmitUnsupported) {
		if serr != nil && !errors.Is(serr, protocol.ErrInputBusy) {
			return fmt.Errorf("%w: %v", protocol.ErrComposerOutcomeUnknown, serr)
		}
		return serr
	}
	// AN OLD SHIM, MID-UPGRADE, and the only path left is the one that can merge. It is
	// reached exactly when the running shim predates the transaction, which a daemon
	// restart resolves.
	if err := sub.Input(keys); err != nil {
		return fmt.Errorf("%w: writing the message into session %q: %v", protocol.ErrComposerOutcomeUnknown, local, err)
	}
	time.Sleep(submitframe.Gap)
	if err := sub.Input([]byte{'\r'}); err != nil {
		return fmt.Errorf("%w: text delivered, submit not sent into session %q: %v", protocol.ErrComposerOutcomeUnknown, local, err)
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
			d.noteComposerProgress(sessionID)
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
		d.noteComposerProgress(sessionID)
		return
	}
}

func (d *Daemon) noteComposerProgress(sessionID string) {
	if lane, ok := d.composerLanes.Load(sessionID); ok {
		lane.(*composerLane).progress.Add(1)
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
	// Validate the named turn and publish the Stop barrier as one transaction with
	// composer retry. Lock order is lane.mu -> itemMu everywhere these domains nest.
	// A stale Stop does not increment the barrier and therefore does not discard valid
	// queued messages.
	lane := d.composerLaneFor(local)
	var current, native string
	var stale bool
	d.withComposerInteractionState(lane, func() {
		d.initInteractionsLocked()
		current = d.turnIDs[local]
		native = d.nativeTurns[local]
		if hook := testHookInterruptValidatedNotYetBarrier.Load(); hook != nil {
			(*hook)(local)
		}
		stale = req.ExpectedTurn != current ||
			(lane.supersededTurn != "" && lane.supersededTurn == current)
		if !stale {
			lane.barrier++
			lane.stopsInFlight++
		}
	})
	if stale {
		return protocol.CodeStaleTurn, fmt.Errorf(
			"turn interrupt: expected_turn %q is not session %q's current turn; the turn it was rendered against is over, and interrupting whatever replaced it -- including a turn the owner just started at the terminal -- is what this refusal exists to prevent",
			req.ExpectedTurn, session)
	}
	// Stop is a priority barrier on the composer lane, not a FIFO message behind provider
	// I/O it is meant to stop. It invalidates every message admitted before this point and
	// prevents later queue heads from dispatching until the interrupt resolves. An in-flight
	// steer may finish, but if Stop made its native-turn precondition fail it is forbidden
	// from retrying as a fresh turn.
	defer lane.endStop()
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
		d.notePhoneActivity(local)
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
	if err := sub.ControlKeys(keys); err != nil {
		return "", fmt.Errorf("turn interrupt: writing the interrupt into session %q: %w", session, err)
	}
	// Stopping a turn is driving the session as much as sending into it is.
	d.notePhoneActivity(local)
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
// by cursor, bounded by limit -- plus the honest floor ("nothing older is retained"). An
// empty before_item places the boundary after the retained transcript and therefore returns
// the newest page; this is how a phone holding no item obtains its first stable anchor.
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
	var older []protocol.JournalRecord
	if beforeItem == "" {
		// The sentinel is a boundary after the newest retained record. Copy the slice so
		// the paging code below has exactly the same ownership in both modes.
		older = append(older, recs...)
	} else {
		boundary, found := uint64(0), false
		for _, rec := range recs {
			if historyItemID(rec) == beforeItem {
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
		for _, rec := range recs {
			if rec.Cursor < boundary {
				older = append(older, rec)
			}
		}
	}
	start, fits := historyPageStartBounded(older, limit, maxHistoryRecordsJSONBytes)
	if !fits {
		// Never cut an interaction item: agent_message records are increments and a tail
		// presented without its head is corrupted prose, not a partial success. One item
		// larger than the reply budget is therefore an explicit refusal rather than an
		// oversized relay append or an empty page the phone retries forever.
		return nil, false, protocol.CodeUnavailable, fmt.Errorf(
			"interaction history: newest whole item before %q exceeds the bounded reply", beforeItem)
	}
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
// The rule is therefore: take the largest suffix closed over WHOLE item identities whose
// record count fits limit. `limit` normally bounds records (items have no uniform size), but
// item closure wins when no such suffix fits: a streamed A may surround B as A(head), B,
// A(delta), making all three records the smallest suffix that contains no headless tail.
//
// THE MINIMAL WHOLE-IDENTITY CLOSURE THAT DOES NOT FIT still ships over limit. Usually that is
// one multi-record item; an interleaving can make it several items. Refusing it would return an
// empty page with floor=false -- "there is more, and you may not have it" -- and the phone
// would ask forever for a page it can never receive. The independent byte ceiling remains
// hard, so this escape is bounded; a headless tail or livelock is not an acceptable substitute.
func historyPageStart(older []protocol.JournalRecord, limit int) int {
	bounds := historyItemBoundaries(older)
	if len(bounds) == 0 {
		return 0
	}
	for _, b := range bounds {
		if len(older)-b <= limit {
			return b
		}
	}
	// Not even the newest whole-identity closure fits: ship it anyway. See the doc.
	return bounds[len(bounds)-1]
}

// maxHistoryRecordsJSONBytes bounds the serialized Journal slice before the protocol reply
// is sealed and appended to the relay. The relay's 1 MiB append frame base64-encodes the
// encrypted envelope, so a page near 768 KiB of plaintext is already too close to the hard
// ceiling once Control/reply framing, the 78-byte envelope overhead and JSON are included.
// 512 KiB leaves more than 250 KiB of plaintext headroom; the worst-case sealing test pins
// the complete replyFrame -> encrypted envelope -> base64 relay-append composition.
const maxHistoryRecordsJSONBytes = 512 << 10

// historyPageStartBounded applies the record-count boundary first, then advances across
// whole-item boundaries until the serialized Journal slice fits byteBudget. It never returns
// a partial item. fits=false means even the newest whole item is too large and the caller must
// refuse explicitly rather than emit an oversized frame or an empty-with-more page.
func historyPageStartBounded(older []protocol.JournalRecord, limit, byteBudget int) (start int, fits bool) {
	start = historyPageStart(older, limit)
	bounds := historyItemBoundaries(older)
	for {
		raw, err := json.Marshal(older[start:])
		if err == nil && len(raw) <= byteBudget {
			return start, true
		}
		next := len(older)
		for _, boundary := range bounds {
			if boundary > start {
				next = boundary
				break
			}
		}
		if next == len(older) {
			return len(older), false
		}
		start = next
	}
}

// historyItemBoundaries returns every index at which the WHOLE suffix may begin without
// cutting an interaction item. Checking only the record at the candidate is insufficient:
// streamed item A may be interleaved A(head), B, A(delta), in which case B is its own first
// record but the suffix beginning there still contains A's tail without A's head. A structured
// gap carries no item_id and is atomic, but is a valid start only when every later item is whole.
func historyItemBoundaries(older []protocol.JournalRecord) []int {
	first := make(map[string]int)
	for i, rec := range older {
		id := historyItemID(rec)
		if _, ok := first[id]; id != "" && !ok {
			first[id] = i
		}
	}
	// Scan from the new edge, retaining the earliest first-record required by any item in
	// the suffix. A candidate is closed over item identity exactly when that requirement
	// does not precede it. bounds are reversed into ascending order for the paging callers.
	required := len(older)
	var reverse []int
	for i := len(older) - 1; i >= 0; i-- {
		id := historyItemID(older[i])
		if id == "" {
			if required >= i {
				reverse = append(reverse, i)
			}
			continue
		}
		if first[id] < required {
			required = first[id]
		}
		if required == i {
			reverse = append(reverse, i)
		}
	}
	bounds := make([]int, len(reverse))
	for i := range reverse {
		bounds[len(reverse)-1-i] = reverse[i]
	}
	return bounds
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
