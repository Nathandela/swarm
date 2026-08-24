package skeleton

// THE SESSION BACKEND, from the ASSEMBLY's side (Wave R7, Mirror M4.1-M4.5; ADR-013 §R7.3-§R7.7):
// the per-session app-server connection registry, the PRODUCER-EDGE PUMP that turns its
// JSON-RPC frames into adapter.HookPayloads, the pending server-request table that native
// approvals are answered through, and the three lifecycle cases that decide what the phone is
// TOLD about a session whose backend is missing.
//
// WHY THE BATCHER IS HERE AND NOT IN THE ADAPTER, and why that is still M4.2's sentence.
// M4.2 says the agentMessage deltas are "BATCHED 200 ms AT THE ADAPTER FROM DAY ONE".
// InteractionSource.Interactions is required to be PURE and TOTAL and internal/adapter is
// grep-fenced against every fd/socket/exec token, so a connection, a correlation table and a
// 200 ms timer are three kinds of state a stateless strategy object may not hold. Read
// literally the sentence is not implementable. It sits at the PRODUCER EDGE instead -- between
// the client's frame stream and captureInteractions -- which is where the intent lands: the
// coalescing happens BEFORE AN ITEM EXISTS. Not the gateway (too late, the record already
// exists), not the shim (the PTY plane).
//
// THE ARITHMETIC IT EXISTS FOR IS MEASURED, NOT ASSUMED. R1 leg 4 recorded 586
// item/agentMessage/delta frames in ONE turn over roughly 14 s -- about 42 frames/s. The
// gateway's combined machine->phone ceiling is 8 appends/s across journal AND terminal
// (remotegw.DefaultAppendWindow), so one unbatched Codex session exceeds the whole machine's
// budget fivefold and starves the terminal plane it shares a target with (PB-GW-7).
//
// AND IT IS MEASURED AGAINST THE RECORDED MIX, not against a delta stream nobody emits (review
// round 4, MEDIUM 3). The same census recorded ~1 mid-stream lifecycle frame per 5 deltas, and
// while EVERY non-delta frame flushed the fold that mix produced 9 offered records/s -- above
// the ceiling, from one session. backendFoldPassthrough is the rule that closes it.
//
// LOCK ORDER, stated once because two of these paths run on different goroutines:
// pumpMu -> itemMu, and backendMu is a LEAF taken alone. Nothing ever holds itemMu while
// taking pumpMu, and nothing holds either while taking backendMu.

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
)

// backendBatchWindow is M4.2's fold window at the producer edge: consecutive agentMessage
// deltas for one item are merged for this long before one synthesized frame is offered.
//
// 200 ms turns the RECORDED 42 frames/s into at most 5 offered records/s for that session,
// which is what makes a streaming Codex session fit inside a machine-wide budget it shares
// with the terminal plane. It is a SPACING FLOOR on the fold only: an ordering boundary or a
// session end flushes immediately.
const backendBatchWindow = 200 * time.Millisecond

// backendCallTimeout bounds ONE app-server request. It exists because a phone op that never
// resolves is worse than one that fails: the owner is left looking at a spinner with no way to
// tell a slow agent from a dead channel.
const backendCallTimeout = 30 * time.Second

// composerClientRefPrefix namespaces the ids this daemon mints for `clientUserMessageId`, so a
// value echoed back on an item is unambiguously ours and not the TUI's own.
const composerClientRefPrefix = "swarm:"

// newComposerClientRef mints the EXACT composer-echo correlation key. It is a fresh ULID with
// this daemon's namespace, sent as `clientUserMessageId` and read straight back off the
// userMessage item's `clientId`.
func newComposerClientRef() string { return composerClientRefPrefix + newItemID() }

// The JSON-RPC methods the pump treats specially. Everything else is passed straight through.
const (
	backendDeltaMethod    = "item/agentMessage/delta"
	backendResolvedMethod = "serverRequest/resolved"
	backendStartedMethod  = "thread/started"
)

// backendConn is the daemon's view of one app-server connection: the two verbs the chat ops
// need and nothing else. *appserver.Client satisfies it; tests substitute a recorder.
//
// It is an INTERFACE here rather than the concrete client for the reason ADR-013 §R7.2d gives:
// internal/appserver holds the socket and knows nothing about sessions, and internal/skeleton
// holds the sessions and should not have to open one to be tested.
type backendConn interface {
	// Call sends a request and waits for its correlated reply.
	Call(ctx context.Context, method string, params, out any) error
	// Respond answers a server-initiated request on THIS connection with THAT id. JSON-RPC
	// ids are per-connection, so a reply carrying any other id answers nothing.
	Respond(ctx context.Context, id json.RawMessage, result any) error
	// Close releases the connection. It is part of the interface rather than a type
	// assertion at the one call site because a registration this daemon can drop but not
	// close is a read loop and a socket NOBODY OWNS (review round 3 MEDIUM 4) -- the same
	// shape as the SIGKILL residual, one layer up.
	Close() error
}

// sessionBackend is one session's live backend.
//
// THE CONNECTION AND THE SUBSCRIPTION ARE TWO FACTS, and separating them is Wave R7 round 4's
// Ruling 2 (ADR-013 §R7.2e revision 3). `conn` is the MESSAGE SINK: initialized, handshaken and
// usable for turn/start the moment the daemon holds it. `subscribed` is whether this daemon is
// also receiving the thread's ITEM STREAM, which `thread/resume` cannot grant until the thread
// has a rollout file -- and that file is created when its FIRST TURN STARTS (RECORDED,
// r1-codex-gate.md:112-115). Round 3 conflated them, so the ability to SEND depended on having
// read history: the phone could not start the very turn that would create the rollout.
type sessionBackend struct {
	threadID string
	conn     backendConn
	// subscribed is set once thread/resume has succeeded on this connection. It is read for
	// evidence and by the retry loop; NO OPERATION IS GATED ON IT, which is the whole point.
	subscribed bool
}

// backendState is the assembly's per-session backend bookkeeping.
type backendState struct {
	mu sync.Mutex
	// live maps a local session id to its connection. ABSENCE IS THE REFUSAL: a session
	// with no entry has no message sink and no approval channel, and the ops refuse rather
	// than falling back to a keystroke.
	live map[string]*sessionBackend
	// requests maps a local session id to the OUTSTANDING server-request ids, keyed by the
	// item ref the request named (params.itemId). The daemon answers an approval by finding
	// the id THIS connection received for that ref.
	requests map[string]map[string]json.RawMessage
	// byID is the reverse lookup requests needs when the SERVER tells us a request was
	// resolved: it names only the request id.
	byID map[string]map[string]string
	// adopted maps a local session id to the CLI'S OWN thread id, learned from the
	// `thread/started` the app-server broadcasts when the AGENT creates its thread.
	//
	// IT IS NOT THE DAEMON'S THREAD, because the daemon no longer has one (review
	// BLOCKING 3). Round 1 called `thread/start` and launched the agent with
	// `--remote unix://SOCK` and nothing else, so the agent created a SECOND thread: every
	// turn/start, turn/steer, turn/interrupt and every approval the daemon answered named a
	// thread the owner was not looking at, and the phone's transcript would have shown an
	// empty conversation. Two conversations, one socket.
	adopted map[string]string
}

// backendPump is the producer-edge batcher's per-session state.
type backendPump struct {
	// emitMu SERIALIZES EMISSION, and it is a second mutex rather than a wider hold of mu
	// because emitting calls all the way down into captureInteractions (which takes itemMu)
	// and must not do that under the lock the fold is manipulated with.
	//
	// It exists because there are TWO emitters -- the connection's read loop and the fold's
	// own 200 ms timer -- and the adapter's Interactions is not required to be safe for
	// concurrent calls (ADR-010 asks for pure, total and deterministic, which is a different
	// promise). It also makes the flush-then-emit pair of an ordering boundary ATOMIC
	// against the timer, so held prose can never be overtaken by the frame that flushed it.
	emitMu sync.Mutex
	mu     sync.Mutex
	// open holds each session's in-flight delta fold. At most one per session, because the
	// fold is flushed on any ordering boundary -- including a delta for a DIFFERENT item.
	open map[string]*deltaBatch
}

// deltaBatch is one session's accumulating agentMessage fold.
type deltaBatch struct {
	itemID string
	// last is the most recent frame's bytes; the synthesized frame keeps its threadId,
	// turnId and itemId, so the adapter sees a frame the server could genuinely have
	// emitted and no shape is invented.
	last []byte
	// text is the CONCATENATION of the folded `delta` strings, in arrival order.
	text strings.Builder
	// firstAtMs is the FIRST folded frame's capture instant. A batched delta's honest
	// capture instant is its earliest content's -- shapeItem's own rule -- and using the
	// flush instant instead is the PB-APP-11 clock mistake.
	firstAtMs int64
	timer     *time.Timer
}

// ---- the registry ----------------------------------------------------------

// registerBackend records a session's live app-server connection. threadID is the thread the
// daemon joined, and it is what every subsequent turn/* request names.
func (d *Daemon) registerBackend(local, threadID string, conn backendConn) {
	d.backend.mu.Lock()
	defer d.backend.mu.Unlock()
	if d.backend.live == nil {
		d.backend.live = map[string]*sessionBackend{}
		d.backend.requests = map[string]map[string]json.RawMessage{}
		d.backend.byID = map[string]map[string]string{}
	}
	if d.backend.adopted == nil {
		d.backend.adopted = map[string]string{}
	}
	d.backend.live[local] = &sessionBackend{threadID: threadID, conn: conn}
	// ADR-017 T2-a / D-NIL: THE BACKEND-CONNECT SEAM AUTHORS THE RECORD IT DECIDES.
	// R7 made structured_chat "the seam AND a live backend, per session instance", so for
	// a provider whose structured plane lives in a side process this moment -- and not
	// launch -- is when the fact becomes knowable. The launch, reconcile and attach paths
	// deliberately author NOTHING until one of this file's two outcomes arrives, because
	// T2 rule 2 makes a structured_chat degrade one-way and an early wrong record could
	// never be corrected.
	//
	// Off the caller's goroutine: registerBackend holds d.backend.mu and the authoring
	// path takes the capability store's lock and writes the session dir; holding a backend
	// lock across a disk write would put the pump behind the filesystem.
	go d.authorCapabilitiesOnBackendJoin(local)
}

// authorCapabilitiesOnBackendJoin authors local's record with the structured plane PROVEN
// LIVE. It is the true half of the pair below.
func (d *Daemon) authorCapabilitiesOnBackendJoin(local string) {
	d.authorCapabilitiesForBackend(local, true)
}

// degradeCapabilitiesOnBackendLoss authors local's record with NO backend plane, which is
// a DEGRADE and only a degrade (ADR-017 T2 rule 2 / D-DEGRADE-ORIGIN). The re-authoring
// runs through the same merge as every other path, so it can only ever withdraw:
// registerSessionCapabilities refuses the upgrade direction outright, and a
// terminal_control it once withdrew stays withdrawn.
//
// A DEGRADE IS MACHINE-LOCAL IN ORIGIN. This caller and the proven hook-spool gap are the
// only ones; no remote-reachable path induces one, because a phone-inducible degrade would
// be a privilege escalation whose payoff is a live peek onto a structured session's
// terminal.
func (d *Daemon) degradeCapabilitiesOnBackendLoss(local string) {
	d.authorCapabilitiesForBackend(local, false)
}

// authorCapabilitiesForBackend is the shared body of the two above.
func (d *Daemon) authorCapabilitiesForBackend(local string, live bool) {
	if d.core == nil {
		return
	}
	m, ok := d.core.Get(local)
	if !ok {
		return
	}
	inst, ad, version, ok := d.sessionCapabilityInputs(local, m.AgentType, m.ShimPID)
	if !ok {
		return
	}
	if _, err := d.authorSessionCapabilities(local, inst, m.AgentType, ad, version, adapterRevision, live); err != nil {
		log.Printf("skeleton: author capability record for backend session %s: %v", local, err)
	}
}

// adoptBackendThread records the thread id the agent created, once.
//
// FIRST WINS, and the rule matters: §R7.10 pins ONE app-server per session, so a session has
// exactly one agent and exactly one thread. Overwriting on a later `thread/started` would
// point a live session's composer at a thread nobody is reading.
func (d *Daemon) adoptBackendThread(local, threadID string) {
	if !adapter.IsCanonicalConversationID(threadID) {
		return
	}
	d.backend.mu.Lock()
	if d.backend.adopted == nil {
		d.backend.adopted = map[string]string{}
	}
	accepted, adopted := d.backend.adopted[local]
	if !adopted {
		d.backend.adopted[local] = threadID
		accepted = threadID
	}
	d.backend.mu.Unlock()
	if accepted != threadID {
		return
	}
	// Persistence is outside backend.mu: metadata I/O may fsync, while the backend
	// lock is the leaf protecting live routing. Repeated notification of the same
	// accepted id deliberately retries a transient write failure.
	if d.core != nil {
		if err := d.core.SetConversationID(local, threadID); err != nil {
			log.Printf("skeleton: could not persist backend conversation identity for session %s", local)
		}
	}
}

// adoptedThread returns the thread id the agent created for this session, if it has been
// announced yet.
func (d *Daemon) adoptedThread(local string) (string, bool) {
	d.backend.mu.Lock()
	defer d.backend.mu.Unlock()
	id, ok := d.backend.adopted[local]
	return id, ok
}

// forgetBackend drops a session's connection. Every op that needed it now refuses -- which is
// the branch order that makes playbook §8.2 structural: if the daemon fell back to a keystroke
// seam when the RPC channel is gone, a Codex approval would be typed into the TUI on exactly
// the day the backend crashed.
// IT ALSO CLOSES THE CONNECTION, which is the whole of review round 3 MEDIUM 4. Dropping the
// registration alone left the *appserver.Client's read loop and its UDS alive until the
// app-server itself died, and watchSessionBackend then returned at its sessionBackendFor guard
// rather than reaping anything. Closing here covers BOTH callers -- endSession and
// noteBackendLost -- and is harmless on a connection that has already ended.
func (d *Daemon) forgetBackend(local string) {
	d.backend.mu.Lock()
	defer d.backend.mu.Unlock()
	if b := d.backend.live[local]; b != nil && b.conn != nil {
		_ = b.conn.Close()
	}
	delete(d.backend.live, local)
	delete(d.backend.requests, local)
	delete(d.backend.byID, local)
	delete(d.backend.adopted, local)
}

// markBackendSubscribed records that thread/resume has succeeded on this session's connection,
// so the daemon is now receiving the thread's item stream.
func (d *Daemon) markBackendSubscribed(local string) {
	d.backend.mu.Lock()
	defer d.backend.mu.Unlock()
	if b := d.backend.live[local]; b != nil {
		b.subscribed = true
	}
}

// backendSubscribed reports whether this session's thread subscription is live.
func (d *Daemon) backendSubscribed(local string) bool {
	d.backend.mu.Lock()
	defer d.backend.mu.Unlock()
	b := d.backend.live[local]
	return b != nil && b.subscribed
}

// sessionBackendFor returns a session's live backend, if it has one.
func (d *Daemon) sessionBackendFor(local string) (*sessionBackend, bool) {
	d.backend.mu.Lock()
	defer d.backend.mu.Unlock()
	b, ok := d.backend.live[local]
	return b, ok
}

// noteServerRequest records the JSON-RPC id an incoming server-request carried, keyed by the
// item ref it named (params.itemId). It is the tie between an approval card and the exact
// frame that must be answered.
func (d *Daemon) noteServerRequest(local, itemRef string, id json.RawMessage) {
	if itemRef == "" || len(id) == 0 {
		return
	}
	d.backend.mu.Lock()
	defer d.backend.mu.Unlock()
	if d.backend.requests == nil {
		d.backend.requests = map[string]map[string]json.RawMessage{}
		d.backend.byID = map[string]map[string]string{}
	}
	if d.backend.requests[local] == nil {
		d.backend.requests[local] = map[string]json.RawMessage{}
		d.backend.byID[local] = map[string]string{}
	}
	d.backend.requests[local][itemRef] = append(json.RawMessage(nil), id...)
	d.backend.byID[local][string(id)] = itemRef
}

// takeServerRequest consumes the ANSWERABILITY of one item ref: the id is returned once and
// the second attempt finds nothing, so a re-delivered approve can never write a second reply
// the server would apply to whatever replaced this request.
//
// IT DELIBERATELY LEAVES byID INTACT, and that asymmetry is review BLOCKING 2's fix.
// The two maps answer two different questions and are consumed by two different events:
//
//	requests[ref] -> id   "may this approval still be answered?"   consumed by ANSWERING
//	byID[id] -> ref       "which card does this resolution retire?" consumed by the SERVER's
//	                       own serverRequest/resolved broadcast
//
// Round 1 consumed both here, so the phone's own answer destroyed the mapping its retirement
// needed: retireResolvedRequest then looked the broadcast up, found nothing, and returned
// having resolved nothing. PROBED: phone approve -> RPC reply -> the RECORDED
// serverRequest/resolved ingested -> the card still pending 5 s later, live until the
// IS-LIFE-2 expiry sweep. The terminal-answered ordering worked, which is why it was missed:
// the phone-answered ordering is the one M4.3 exists for.
//
// Retaining the entry is what keeps "resolution arrives only BY OBSERVATION" true rather than
// making it a claim the daemon has to fake -- the server DOES broadcast to the answering
// client, which the R1 gate recorded (frame-samples.json: the observer replied
// {"decision":"accept"} and then received serverRequest/resolved on its own connection).
//
// ponytail: byID's residue is one small entry per approval for the session's life, dropped by
// forgetBackend. Bounding it would need a drop policy, and a mapping dropped early is exactly
// the card that never clears.
func (d *Daemon) takeServerRequest(local, itemRef string) (json.RawMessage, bool) {
	d.backend.mu.Lock()
	defer d.backend.mu.Unlock()
	id, ok := d.backend.requests[local][itemRef]
	if !ok {
		return nil, false
	}
	delete(d.backend.requests[local], itemRef)
	return id, true
}

// serverRequestRef resolves a request id back to the item ref it named, consuming it.
func (d *Daemon) serverRequestRef(local string, id json.RawMessage) (string, bool) {
	d.backend.mu.Lock()
	defer d.backend.mu.Unlock()
	ref, ok := d.backend.byID[local][string(id)]
	if !ok {
		return "", false
	}
	delete(d.backend.byID[local], string(id))
	delete(d.backend.requests[local], ref)
	return ref, true
}

// ---- the producer-edge pump ------------------------------------------------

// backendFrame is the minimum the pump itself parses. Everything else stays opaque bytes: the
// adapter is handed the WHOLE FRAME verbatim, which is what makes
// r1-codex-fixtures/frame-samples.json literally the golden vector set (ADR-013 §R7.3).
type backendFrame struct {
	Method string          `json:"method"`
	ID     json.RawMessage `json:"id"`
	Params struct {
		ItemID    string          `json:"itemId"`
		Delta     string          `json:"delta"`
		RequestID json.RawMessage `json:"requestId"`
		// Thread is `thread/started`'s payload. Only its id is read here: the thread the
		// AGENT created is the one the daemon must drive, and learning it from the
		// server's own broadcast is what puts both surfaces on ONE conversation.
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	} `json:"params"`
}

// ingestBackendFrame is the pump's entry point: ONE app-server frame, as it arrived.
//
// Consecutive agentMessage deltas for one (session, itemId) fold into ONE synthesized frame
// carrying the CONCATENATION of their `delta` strings and the last frame's other fields -- it
// is exactly the frame the server would have emitted had it chunked more coarsely. ANY other
// frame flushes the open fold FIRST, so ordering is never disturbed: a pump that held prose
// while emitting a turn/completed would show the turn closing before the words it closed on.
//
// APPROVALS BYPASS THE BATCHER ENTIRELY by virtue of that same rule, which is IS-DELTA-3's
// head-of-queue posture one layer up: an approval sitting behind a 200 ms prose batch is
// 200 ms of a session blocked waiting for a human who has not been told yet.
func (d *Daemon) ingestBackendFrame(local string, frame []byte, receivedAtMs int64) {
	var fr backendFrame
	if json.Unmarshal(frame, &fr) != nil {
		// A frame this daemon cannot parse is dropped, never fatal: a CLI upgrade that adds a
		// shape degrades to the grid heuristic rather than killing the pump.
		return
	}
	// The provider-owned thread id is committed before pump.emitMu is acquired.
	// A metadata fsync must never hold the producer ordering lock and delay every
	// other frame. Strict path decoding rejects duplicate identity keys that the
	// ordinary projection above would silently collapse.
	if fr.Method == backendStartedMethod {
		if threadID, ok := backendStartedThreadID(frame); ok {
			d.adoptBackendThread(local, threadID)
		}
	}
	if fr.Method == backendDeltaMethod && fr.Params.ItemID != "" {
		d.foldDelta(local, fr.Params.ItemID, fr.Params.Delta, frame, receivedAtMs)
		return
	}
	d.pump.emitMu.Lock()
	defer d.pump.emitMu.Unlock()
	if !backendFoldPassthrough[fr.Method] {
		// The ordering boundary: the held fold goes out FIRST, and the two emissions are one
		// atomic step against the timer.
		d.releaseFoldLocked(local)
	}
	d.emitBackendFrame(local, fr, frame, receivedAtMs)
}

func backendStartedThreadID(frame []byte) (string, bool) {
	envelope, ok := decodeStrictObject(frame)
	if !ok {
		return "", false
	}
	method, ok := strictJSONString(envelope["method"])
	if !ok || method != backendStartedMethod {
		return "", false
	}
	params, ok := decodeStrictObject(envelope["params"])
	if !ok {
		return "", false
	}
	thread, ok := decodeStrictObject(params["thread"])
	if !ok {
		return "", false
	}
	id, ok := strictJSONString(thread["id"])
	return id, ok && adapter.IsCanonicalConversationID(id)
}

// backendFoldPassthrough names the frames that CANNOT REORDER an open agentMessage fold, and so
// must not flush it. It is review round 4's MEDIUM 3.
//
// THE DEFECT IT FIXES was a correctness-of-claim one. M4.2's headline -- "42 frames/s becomes at
// most 5 offered records/s" -- was measured against a PURE delta stream, and no such stream
// occurs: the R1 census (r1-codex-gate.md:101-104) recorded 49 item/agentMessage/delta frames in
// one turn ALONGSIDE 3 thread/tokenUsage/updated, 3 thread/status/changed and 4 turn/diff/updated,
// about one per five. Treating every non-delta frame as a boundary flushed the fold at that
// ratio, and PROBED at the recorded mix the pump offered 9 records in a second -- ABOVE the
// 8 appends/s the batcher exists to respect, from ONE session.
//
// WHY THESE THREE AND NOTHING ELSE. A boundary flush exists so a frame cannot be shown ahead of
// prose it should follow. These three shape NO INTERACTION AT ALL: the codex adapter dispatches
// on item/started, item/completed, item/agentMessage/delta, turn/completed and the two
// */requestApproval methods, and returns nil for everything else (adapter/codex/interaction.go).
// A frame that can never appear in a transcript cannot be reordered within one, so holding the
// fold across it changes no observable order. Everything that CAN shape an item -- including a
// delta for a different item id -- still flushes, and still does so before it is emitted.
//
// They are still EMITTED, immediately and in order: M4.5's typed status is driven by
// thread/status/changed, and not flushing a fold is not the same as swallowing a frame.
var backendFoldPassthrough = map[string]bool{
	"thread/tokenUsage/updated": true,
	"thread/status/changed":     true,
	"turn/diff/updated":         true,
}

// foldDelta merges one delta into the session's open fold, opening one if needed.
func (d *Daemon) foldDelta(local, itemID, delta string, frame []byte, receivedAtMs int64) {
	d.pump.mu.Lock()
	if d.pump.open == nil {
		d.pump.open = map[string]*deltaBatch{}
	}
	b := d.pump.open[local]
	if b != nil && b.itemID != itemID {
		// A delta for a DIFFERENT item is an ordering boundary like any other frame. Folding
		// across item ids would merge two concurrent messages into one bubble.
		d.pump.mu.Unlock()
		d.flushBackendFrames(local)
		d.pump.mu.Lock()
		if d.pump.open == nil {
			d.pump.open = map[string]*deltaBatch{}
		}
		b = d.pump.open[local]
	}
	if b == nil {
		b = &deltaBatch{itemID: itemID, firstAtMs: receivedAtMs}
		d.pump.open[local] = b
		// A batcher with no timer is a transcript that stops one message short of every turn:
		// a turn whose last delta is followed by nothing must not sit in the pump forever.
		b.timer = time.AfterFunc(backendBatchWindow, func() { d.flushBackendFrames(local) })
	}
	b.last = frame
	b.text.WriteString(delta)
	d.pump.mu.Unlock()
}

// flushBackendFrames releases a session's open fold, if any. It is called on every ordering
// boundary, by the fold's own timer, and at session end.
func (d *Daemon) flushBackendFrames(local string) {
	d.pump.emitMu.Lock()
	defer d.pump.emitMu.Unlock()
	d.releaseFoldLocked(local)
}

// releaseFoldLocked is flushBackendFrames' body. Caller holds pump.emitMu.
func (d *Daemon) releaseFoldLocked(local string) {
	d.pump.mu.Lock()
	b := d.pump.open[local]
	if b == nil {
		d.pump.mu.Unlock()
		return
	}
	delete(d.pump.open, local)
	if b.timer != nil {
		b.timer.Stop()
	}
	d.pump.mu.Unlock()

	synth, ok := synthesizeDelta(b)
	if !ok {
		return
	}
	var fr backendFrame
	if json.Unmarshal(synth, &fr) != nil {
		return
	}
	d.emitBackendFrame(local, fr, synth, b.firstAtMs)
}

// synthesizeDelta rewrites the last folded frame's `params.delta` to the concatenation. The
// METHOD NAME and every other field are the server's own, so the adapter dispatches on exactly
// what it would have seen; a pump that reshaped frames would make the recorded corpus stop
// being a golden vector set.
func synthesizeDelta(b *deltaBatch) ([]byte, bool) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(b.last, &envelope) != nil {
		return nil, false
	}
	var params map[string]json.RawMessage
	if json.Unmarshal(envelope["params"], &params) != nil {
		return nil, false
	}
	text, err := json.Marshal(b.text.String())
	if err != nil {
		return nil, false
	}
	params["delta"] = text
	rebuilt, err := json.Marshal(params)
	if err != nil {
		return nil, false
	}
	envelope["params"] = rebuilt
	out, err := json.Marshal(envelope)
	if err != nil {
		return nil, false
	}
	return out, true
}

// emitBackendFrame is the ONE place a frame leaves the pump. It drives, in order:
//
//	M4.5  the session's TYPED STATUS, through the engine's in-process seam -- which is what
//	      finally makes the mapping the codex adapter has declared since Epic 11 fire, paying
//	      the D1 debt by building the PRODUCER rather than rewriting the mapping;
//	      a `serverRequest/resolved` also RETIRES the pending card, daemon-side;
//	M4.2  the ITEM path, through the REAL capture seam (captureInteractions), so a backend
//	      frame and a hook body take exactly the same route to the journal.
func (d *Daemon) emitBackendFrame(local string, fr backendFrame, raw []byte, receivedAtMs int64) {
	if fr.Method == "" {
		return
	}
	if d.eng != nil {
		if err := d.eng.ApplyTypedEvent(local, fr.Method, nil); err != nil {
			// A session the engine does not run is the ordinary case for a test rig and for
			// a frame arriving after the agent exited; it is not worth a log line per frame.
			_ = err
		}
	}
	if fr.Method == backendResolvedMethod {
		// LIFECYCLE, NOT CONTENT. It names a JSON-RPC request id -- transport state only the
		// daemon holds, which the adapter has never seen -- so it is handled here and is NOT
		// offered to the item path. Shaping it would be the daemon asking the adapter about
		// a fact the adapter cannot know.
		d.retireResolvedRequest(local, fr.Params.RequestID)
		return
	}
	// A server-initiated request carries BOTH a method and an id, and the id is what an
	// approval is later answered with. Recorded BEFORE the item is shaped, so the card can
	// never reach the phone ahead of the means to answer it.
	if len(fr.ID) > 0 && fr.Params.ItemID != "" {
		d.noteServerRequest(local, fr.Params.ItemID, fr.ID)
	}
	agentType := ""
	if m, ok := d.core.Get(local); ok {
		agentType = m.AgentType
	}
	ad, ok := d.resolveAdapter(agentType)
	if !ok {
		return
	}
	d.captureInteractions(local, ad, adapter.HookPayload{
		Event:        fr.Method,
		Raw:          raw,
		ReceivedAtMs: receivedAtMs,
	})
}

// retireResolvedRequest applies the server's own FIRST-ANSWER-WINS broadcast.
//
// The daemon does NOT arbitrate. When the owner answers at the terminal, the server tells every
// attached client that the request is over (RECORDED: frame-samples.json's
// serverRequest/resolved), and that broadcast -- not a grid observation, not a timer -- is what
// retires the phone's card. It is strictly better evidence than the Claude path has.
func (d *Daemon) retireResolvedRequest(local string, requestID json.RawMessage) {
	if len(requestID) == 0 {
		return
	}
	ref, ok := d.serverRequestRef(local, requestID)
	if !ok {
		return
	}
	d.itemMu.Lock()
	d.initInteractionsLocked()
	var out []json.RawMessage
	ap := d.approvals[local]
	if ap != nil && ap.itemID == d.itemIDs[local+"\x00"+ref] {
		// Attribution mirrors noteInteractionStatus's: when the DAEMON typed -- here, sent --
		// the phone's answer itself, the resolution carries what it sent, `by: phone`, and
		// the phone's own operation_id. Otherwise the owner answered at the terminal.
		decision, by, operation := resolveAnsweredLocally, byOwner, ""
		if ap.applied != "" {
			decision, by, operation = ap.applied, byPhone, ap.appliedOp
		}
		out = d.resolveApprovalLocked(local, decision, by, operation)
	}
	d.itemMu.Unlock()
	d.offerAll(local, out)
}

// forgetBackendPump drops a session's open fold without emitting it. Called only from the
// session-end path, AFTER flushBackendFrames has had its chance.
func (d *Daemon) forgetBackendPump(local string) {
	d.pump.mu.Lock()
	if b := d.pump.open[local]; b != nil && b.timer != nil {
		b.timer.Stop()
	}
	delete(d.pump.open, local)
	d.pump.mu.Unlock()
}

// ---- §R7.7: the three lifecycle cases --------------------------------------

// backendGapReason values, spelled so a reader of the journal can tell the cases apart. A
// reason nobody can distinguish is a gap nobody can explain.
const (
	gapBackendUnavailable = "backend_unavailable"
	gapBackendLost        = "backend_lost"
	// gapBackendPriorHistory says the daemon joined a thread that had ALREADY RUN TURNS it
	// never read. It is the ONLY join-time gap, and it REPLACES round 3's
	// `backend_joined_late` (review round 4, RULING 1), whose inference ran backwards.
	//
	// THE RECORDED FACT (r1-codex-gate.md:112-115): `thread/resume` fails with
	// `no rollout found for thread id` until the thread's rollout file exists, and that file
	// is created when its FIRST TURN STARTS. Round 3 read a resume that needed RETRIES as
	// proof that a turn had already begun. The opposite is true: the retries happened
	// BECAUSE no turn had begun, so that join missed nothing -- and emitting a gap there was
	// not merely inconvenient but FACTUALLY FALSE. It also removed the composer from every
	// ordinary healthy session, because the phone derives the composer from the transcript
	// (`structuredChat = !transcript.structureTorn`) and reads ANY gap as "no message sink".
	//
	// A resume that succeeds ON THE FIRST ATTEMPT is the honest case: the rollout already
	// existed, so the thread has already run at least one turn, and a client receives a
	// thread's items only AFTER it resumes -- so those turns are history this daemon could
	// not read, and the transcript really does begin mid-conversation. That is a
	// `codex resume`-shaped session, and it gets a gap and NO durable degrade: the tear is in
	// the HISTORY, while the channel is healthy.
	gapBackendPriorHistory = "backend_prior_history"
)

// noteBackendUnavailable is CASE 1: the launch declared a backend and it NEVER CONNECTED --
// its socket never became servable, or the daemon could not dial it at launch-confirm.
//
// This session will never have a structured plane, which is exactly the case the durable
// one-way degrade marker is for. The gap is emitted AT LAUNCH rather than at the first tap
// because of a PHONE fact: the composer's availability reads `structured_gap` off the
// TRANSCRIPT (`structuredChat = !transcript.structureTorn`), not a capability record -- so
// without this the session still SHOWS a composer, the owner types, and the refusal arrives
// after the tap. ADR-017 T2 wants that surfaced before it.
func (d *Daemon) noteBackendUnavailable(local string) {
	d.emitBackendGap(local, gapBackendUnavailable)
	d.markSessionDegraded(local)
	// ADR-017 T2-a: the marker alone leaves this session with NO capability record, and
	// by T2-a no record is the honest status card -- which is the wrong destination for a
	// session that has a live TUI worth watching. markSessionDegraded degrades "the
	// record, if one exists"; this authors the one it degrades, with the backend plane
	// proven absent, so the session lands on the read-only terminal fallback instead.
	d.degradeCapabilitiesOnBackendLoss(local)
}

// noteBackendRejoined is CASE 2: the daemon went away and came back, the shim and its
// app-server survived (ADR-001), the dial succeeded and the thread was rejoined.
//
// IT EMITS NOTHING AND DEGRADES NOTHING, and that is the whole point. markSessionDegraded is
// ONE-WAY and DURABLE, so a rule that degraded on every daemon restart would PERMANENTLY
// remove the composer from every live Codex session on the first `swarm daemon restart` -- the
// operation ADR-001 exists to make ordinary. A successful rejoin is not a proven gap, and
// ADR-017's whole posture is that a gap is proven, never assumed.
func (d *Daemon) noteBackendRejoined(local, threadID string, conn backendConn) {
	d.registerBackend(local, threadID, conn)
}

// noteBackendLost is CASE 3: the backend died mid-session. The session itself ends (that half
// is the shim's, which owns the escalation); history must be honest about what was not
// captured, so a structured_gap covers the tail.
func (d *Daemon) noteBackendLost(local, reason string) {
	d.forgetBackend(local)
	d.flushBackendFrames(local)
	if reason == "" {
		reason = gapBackendLost
	}
	d.emitBackendGap(local, gapBackendLost+": "+reason)
	d.markSessionDegraded(local)
}

// emitBackendGap appends one honest structured_gap boundary for the session.
func (d *Daemon) emitBackendGap(local, reason string) {
	if d.core == nil {
		return
	}
	if err := d.core.EmitStructuredGap(local, reason); err != nil {
		log.Printf("skeleton: EmitStructuredGap for session %s: %v", local, err)
	}
}
