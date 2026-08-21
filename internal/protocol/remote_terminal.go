package protocol

// WAVE R8 -- THE CAPABILITY GATE ON THE TERMINAL PATH, AND THE CONTROL PLANE'S SEAMS.
//
// THE HOLE THIS FILE CLOSES, stated as the attack (ADR-017 amendment T2-c). ADR-017 T4
// keeps `TerminalSnapshot` / `terminal_watch` alive "only under the legacy remote
// profile". Two facts made that sentence unenforceable as written:
//
//  1. the production profile ships as a ZERO VALUE, so "the legacy profile" is presently
//     indistinguishable from "any profile"; and
//  2. the legacy path carried NO SESSION-SCOPED GATE AT ALL -- handleTerminalSubscribe
//     gated the kill switch, the negotiated remote-gateway capability and the presence of
//     a tapper, and nothing about the SESSION.
//
// So a downlevel, rolled-back or compromised app that merely ASKED got a live sanitized
// peek onto a healthy Claude session -- exactly the route Wave R8's exit says does not
// exist, and exactly the escape hatch ADR-017's alternatives section rejects by name.
// Closing it in the phone's router alone would leave it open to anything that is not the
// phone's router.
//
// THE GATE IS REMOTE-TIER ONLY, inheriting peekGateOpen's own scoping and its reason: the
// capability record routes a PHONE, and it has no authority over the owner's view of the
// owner's own machine.

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

// The Wave R8 terminal wire types and refusal codes, aliased from the daemon-free schema
// package for PB-BIND-0's reason (see types.go).
type (
	TerminalControlBeginReq = schema.TerminalControlBeginReq
	TerminalInputReq        = schema.TerminalInputReq
	TerminalViewV1          = schema.TerminalViewV1
)

const (
	CodeCapabilityRefused = schema.CodeCapabilityRefused
	CodeStaleGeneration   = schema.CodeStaleGeneration
	CodeStaleInstance     = schema.CodeStaleInstance
)

// SessionCapabilityLookup is the optional DaemonAPI seam the terminal gate reads: the
// daemon-authored, per-session-instance capability record (ADR-017 T2), keyed by LOCAL
// session id because that is what resolveSession hands a handler.
//
// A backend that does not implement it publishes no records, and by T2-a that is the
// honest status card and a refusal of both verbs -- never "unknown, therefore allow".
type SessionCapabilityLookup interface {
	SessionCapabilities(local string) (SessionCapabilities, bool)
}

// sessionCapabilityLookup returns the backend's SessionCapabilityLookup if it implements
// one, mirroring terminalTapper/deviceRegistrar.
func (cc *clientConn) sessionCapabilityLookup() (SessionCapabilityLookup, bool) {
	l, ok := cc.srv.d.(SessionCapabilityLookup)
	return l, ok
}

// terminalWatchAllowed is THE ONE PREDICATE every terminal read gate is written over
// (ADR-017 T2-a / T2-b / T2-c). It answers false for all four fail-closed states, because
// they are one state as far as a router is concerned:
//
//   - the backend publishes no records at all;
//   - this session has none (every pre-R8 session, and every session whose creation path
//     could not bind an instance);
//   - the record is INCONSISTENT -- structured_chat and terminal_fallback both true, or
//     terminal_control without terminal_fallback, or no session instance -- which is the
//     shape an attacker, a downlevel machine, a partially-written capabilities.json or a
//     future derivation bug produces; or
//   - the record's destination is simply not the terminal.
//
// It is written over BOTH booleans, every time, via the record's own nil-safe accessor: a
// gate that tests terminal_fallback alone enforces T2 rule 4 only for as long as the
// daemon's derivation stays right.
//
// ON THE OWNER TIER IT IS ALWAYS TRUE, exactly as peekGateOpen is, and for the same
// reason: this is the TUI looking at its own machine.
func (cc *clientConn) terminalWatchAllowed(local string) bool {
	if !cc.srv.remoteTier {
		return true
	}
	lookup, ok := cc.sessionCapabilityLookup()
	if !ok {
		return false
	}
	rec, ok := lookup.SessionCapabilities(local)
	if !ok {
		return false
	}
	return rec.AllowsTerminalWatch()
}

// terminalWatchInstance is the INCARNATION the render loop stamps on every view it emits
// (ADR-017 T8-a). It reads the same record terminalWatchAllowed does, so the screen and the
// gate name the same incarnation by construction.
//
// An absent record answers "" and that is honest rather than fail-open: the gate above has
// already refused the watch in that case, so no view is ever emitted under an empty instance
// on the remote tier. On the OWNER tier there is no record and no gate -- the TUI looking at
// its own machine -- and an empty instance there means exactly what it says.
func (cc *clientConn) terminalWatchInstance(local string) string {
	lookup, ok := cc.sessionCapabilityLookup()
	if !ok {
		return ""
	}
	rec, ok := lookup.SessionCapabilities(local)
	if !ok {
		return ""
	}
	return rec.SessionInstance
}

// terminalControlAllowed is the one predicate above, one authority level up. It reads
// terminal_control -- a DISTINCT, daemon-authored field -- and never derives control from
// terminal_fallback, because a session degraded INTO the fallback by a proven structured
// gap may watch and may not control (ADR-017 T6-b).
func (cc *clientConn) terminalControlAllowed(local string) (SessionCapabilities, bool) {
	lookup, ok := cc.sessionCapabilityLookup()
	if !ok {
		return SessionCapabilities{}, false
	}
	rec, ok := lookup.SessionCapabilities(local)
	if !ok {
		return SessionCapabilities{}, false
	}
	return rec, rec.AllowsTerminalControl()
}

// TerminalControlTTL is the control generation's SIGNED HORIZON (ADR-017 T7). It is the
// number already implemented for the control lease, adopted rather than invented so the
// system has ONE fifteen-minute wall rather than two nearly-equal ones that drift apart.
const TerminalControlTTL = 15 * time.Minute

// TerminalKeepaliveTTL is T8's MISSING-KEEPALIVE severance clock, and it is a DIFFERENT
// clock from the horizon above (round-2 blocker 1).
//
// The two were one field, renewed together on every keepalive, and that made T7 false in
// the only sense T7 has: "there is no silent renewal, and no keepalive extends the signed
// horizon". Measured on the assembled server, a phone that sent a keepalive every fourteen
// minutes held raw-input authority over a live terminal for four hours and forty minutes
// and counting -- past the fifteen-minute wall, with no fresh signature, which is precisely
// the unrevocable authority T7 exists to bound for "a phone that is off, out of coverage or
// in an attacker's hands after the transport dropped".
//
// So a keepalive renews THIS deadline and only this deadline. The horizon is stamped once
// at begin and is never moved by anything.
const TerminalKeepaliveTTL = 30 * time.Second

// TerminalIdleSweep is how often the server sweeps generations whose keepalive deadline or
// horizon has passed WITH NO INBOUND FRAME AT ALL (ADR-017 T6-c). A deadline checked only
// when a frame arrives is not an expiry, it is a validity test on the frame: the generation
// stays live in the server's own state, so a kill switch flipped OFF and back ON, or a
// device revoked and re-paired, would find it there. The sweep is what makes the expiry a
// property of TIME rather than of the phone's next request.
const TerminalIdleSweep = time.Second

// terminalGeneration is one live, NON-TRANSFERABLE control generation. It is bound to the
// SIGNING DEVICE, to the session, to that session's INSTANCE and to the remote profile the
// phone selected.
//
// IT IS NOT BOUND TO A CONNECTION, and round 3 is where that stopped being an accident that
// read as a binding. Generations lived on `clientConn` until then, and `Gateway.ForwardCommand`
// DIALS A FRESH DAEMON CONNECTION PER COMMAND and closes it on the reply -- so over the real
// composition a begin minted on conn A and every subsequent input arrived on conn B with no
// generation in sight. Measured on the assembled server: `op="error" code="stale_generation"`,
// bytes at the PTY zero. It failed closed, so it was never an exploit; it made the wave's
// exit ("a fallback session can be CONTROLLED") unreachable by the only path the product has.
//
// WHAT BINDS IT NOW IS WHAT ADR-017 T6 ALWAYS SAID BOUND IT: the E2EE seal's own
// authenticated sender, the unguessable 128-bit id the server minted and returned only to an
// authenticated, device-signed begin, and the per-frame re-evaluation of the kill switch, the
// signing device's continued registration, the session's capability record, the session
// instance and both walls (T6-e). The connection identity was never one of them.
//
// IT IS NOT A LEASE, and the two must not share a plane (OPEN-C4). They have different
// lifetimes, different ceremonies and different authority; routing input through
// LeaseManager would make a fallback session compete for the shim's single interactive
// subscriber slot the owner already holds, and would resurrect the visible take-control
// ceremony ADR-013 R3 removed from the chat path.
type terminalGeneration struct {
	id       string
	session  string
	instance string
	// deviceID is the device that SIGNED the begin. It moved onto the generation with the
	// registry: a per-connection field could not survive the connection, and it is what a
	// device_revoke matches and what every frame re-checks for continued registration.
	deviceID string
	profile  int
	// horizon is the SIGNED WALL (T7). Stamped once, at begin, from the instant the
	// signature was accepted. NOTHING moves it -- not a keepalive, not an input frame, not
	// a re-registration. Re-entering control after it passes costs a fresh signed
	// terminal_control_begin, which is the whole of what "no silent renewal" means.
	horizon time.Time
	// keepalive is the missing-keepalive severance clock (T8), and the ONLY thing a
	// keepalive frame renews. It is always <= horizon in effect, because liveness requires
	// both.
	keepalive time.Time
}

// mintTerminalGeneration returns an unguessable generation id.
func mintTerminalGeneration() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("protocol: mint terminal control generation: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// ---------------------------------------------------------------------------
// The registry: server-wide, keyed by generation id.
// ---------------------------------------------------------------------------

// publishTerminalGenerationIfCurrent installs gen unless a sever has happened since the
// caller sampled severAtStart, in which case it publishes NOTHING and reports false.
//
// IT IS severControl's OWN RACE FENCE, applied to the plane that did not have one (round-3
// minor 7). `Server.severControl` bumps its counter BEFORE snapshotting the connections, and
// its comment states why: "a take_control that publishes its lease/cc.control after this
// snapshot re-checks the generation under ctlMu and, seeing it advanced, fails closed rather
// than escaping the sever." `severTerminalControl` had neither the bump nor the re-check, so
// a `terminal_control_begin` landing between the snapshot and the sweep escaped the sever
// outright -- and an escaped generation is LIVE AGAIN the moment the kill switch goes back
// on, which is verbatim the resume defect round 2's blocker 2 exists to close.
func (s *Server) publishTerminalGenerationIfCurrent(severAtStart uint64, gen *terminalGeneration) bool {
	s.termMu.Lock()
	defer s.termMu.Unlock()
	if s.termSeverGen.Load() != severAtStart {
		return false
	}
	if s.termGens == nil {
		s.termGens = map[string]*terminalGeneration{}
	}
	s.termGens[gen.id] = gen
	return true
}

// terminalGenerationByID returns a COPY of the named generation, so a caller can evaluate the
// walls without holding the registry lock across the backend calls a re-check makes.
func (s *Server) terminalGenerationByID(id string) (terminalGeneration, bool) {
	if id == "" {
		return terminalGeneration{}, false
	}
	s.termMu.Lock()
	defer s.termMu.Unlock()
	gen, ok := s.termGens[id]
	if !ok {
		return terminalGeneration{}, false
	}
	return *gen, true
}

// renewTerminalKeepalive moves ONLY the missing-keepalive deadline of the named generation.
// The signed horizon is not a parameter here and never will be (T7).
func (s *Server) renewTerminalKeepalive(id string, until time.Time) {
	s.termMu.Lock()
	defer s.termMu.Unlock()
	if gen, ok := s.termGens[id]; ok {
		gen.keepalive = until
	}
}

// dropTerminalGenerationsFor removes every generation over local signed by deviceID, which is
// what `terminal_control_end` releases: a signed release from a device gives back everything
// that device holds over that session, whichever connection minted it.
func (s *Server) dropTerminalGenerationsFor(local, deviceID string) int {
	s.termMu.Lock()
	defer s.termMu.Unlock()
	n := 0
	for id, gen := range s.termGens {
		if gen.session == local && gen.deviceID == deviceID {
			delete(s.termGens, id)
			n++
		}
	}
	return n
}

// handleTerminalControlBegin mints one control generation over a terminal_fallback
// session (ADR-017 T6). It replaces the Wave R1 op_not_implemented refusal stub.
//
// The tier is device.ActionControl, which internal/skeleton/deviceauth.go already maps and
// amendment T6-a RATIFIES rather than re-derives: entering control over a real terminal is
// at least as consequential as taking the control lease, and a read-only device is refused
// by that mapping without this handler saying anything.
func (cc *clientConn) handleTerminalControlBegin(c Control) {
	body := c.TerminalControlBegin
	if !cc.requireRemoteAuthz(c, ActionTerminalControlBegin, c.SessionID, schema.TerminalControlBeginContentHash(body)) {
		return
	}
	if !cc.requireBodyVersion(c) {
		return
	}
	switch {
	case c.SessionID == "":
		cc.replyErrorCode("terminal_control_begin: missing session_id", CodeInvalidField)
		return
	case body == nil:
		cc.replyErrorCode("terminal_control_begin: missing terminal_control_begin body", CodeInvalidField)
		return
	case body.Session != c.SessionID:
		// handleComposerSend's collision rule: two session coordinates free to differ
		// would let a gateway point a signature authorised for one session's keyboard at
		// another session's PTY.
		cc.replyErrorCode("terminal_control_begin: body names a session the command does not", CodeInvalidField)
		return
	case body.SessionInstance == "":
		cc.replyErrorCode("terminal_control_begin: missing session_instance; a generation binds an incarnation or it binds nothing", CodeInvalidField)
		return
	}
	local, ok := cc.resolveSession(c)
	if !ok {
		return
	}
	if !cc.peekGateOpen() {
		cc.replyErrorCode("remote control is disabled (kill switch off)", CodeKillSwitch)
		return
	}
	rec, ok := cc.terminalControlAllowed(local)
	if !ok {
		cc.replyErrorCode("terminal_control_begin: this session's capability record does not grant terminal control", CodeCapabilityRefused)
		return
	}
	if rec.SessionInstance != body.SessionInstance {
		// The session was REPLACED between the phone rendering the screen and signing the
		// op. A distinct code, because the remedy is distinct: this is not "try again".
		cc.replyErrorCode("terminal_control_begin: the session was replaced; this generation would name a screen that no longer exists", CodeStaleInstance)
		return
	}
	// Sampled BEFORE the mint, so a sever that happens while this handler is working is seen
	// by the publish below and the generation never reaches the registry (round-3 minor 7).
	severAtStart := cc.srv.termSeverGen.Load()
	now := cc.srv.now()
	gen := &terminalGeneration{
		id:        mintTerminalGeneration(),
		session:   local,
		instance:  rec.SessionInstance,
		deviceID:  c.DeviceID,
		profile:   body.Profile,
		horizon:   now.Add(TerminalControlTTL),
		keepalive: now.Add(TerminalKeepaliveTTL),
	}
	if !cc.srv.publishTerminalGenerationIfCurrent(severAtStart, gen) {
		cc.replyErrorCode("terminal_control_begin: remote control was severed while this "+
			"generation was being minted", CodeKillSwitch)
		return
	}
	_ = cc.writeControl(Control{Op: OpOK, EndpointID: cc.endpointID, OperationID: cc.opID, SessionID: c.SessionID, ControlGeneration: gen.id})
}

// handleTerminalControlEnd releases the signing device's generations over this session. It
// lands BEFORE begin does in every sense that matters: without it the first generation a user
// opens could only be closed by a timeout.
//
// IT RELEASES BY (SESSION, SIGNING DEVICE), NOT BY CONNECTION. The release arrives on a fresh
// connection like every other command (round-3 blocker 1), so "this connection's generation"
// named nothing the product could ever produce -- a release always answered
// `stale_generation` and the phone was told its control had not been given back. Matching the
// signing device is what keeps it precise: one device cannot release another's keyboard.
func (cc *clientConn) handleTerminalControlEnd(c Control) {
	if !cc.requireRemoteAuthz(c, ActionTerminalControlEnd, c.SessionID, nil) {
		return
	}
	if !cc.requireBodyVersion(c) {
		return
	}
	local, ok := cc.resolveSession(c)
	if !ok {
		return
	}
	if cc.srv.dropTerminalGenerationsFor(local, c.DeviceID) == 0 {
		// Ending a generation that does not exist is NOT ok. An OK here would tell a
		// phone its control was released when there was nothing to release, which is the
		// one answer that makes the persistent banner's disappearance a lie.
		cc.replyErrorCode("terminal_control_end: this device holds no live control generation over this session", CodeStaleGeneration)
		return
	}
	cc.replyOK(c.SessionID)
}

// liveTerminalGeneration re-evaluates EVERYTHING a raw byte rides on, per frame
// (ADR-017 T6-e), matching the discipline controlGateOpen already applies per keystroke:
// the kill switch, the device's continued registration, the SESSION's capability record,
// the generation's own liveness and the instance it was minted against. A session
// degraded, revoked, killed or replaced mid-stream stops within a frame rather than at
// whichever trigger the phone next happens to send.
func (cc *clientConn) liveTerminalGeneration(local, instance, generation string) (ErrorCode, bool) {
	if !cc.peekGateOpen() {
		return CodeKillSwitch, false
	}
	gen, ok := cc.srv.terminalGenerationByID(generation)
	if !ok || gen.session != local {
		return CodeStaleGeneration, false
	}
	now := cc.srv.now()
	if !now.Before(gen.horizon) || !now.Before(gen.keepalive) {
		// BOTH walls, every frame. The horizon is the signed one and cannot be moved; the
		// keepalive deadline is the one a live screen renews.
		return CodeStaleGeneration, false
	}
	if reg, ok := cc.deviceRegistrar(); ok && gen.deviceID != "" && !reg.DeviceRegistered(gen.deviceID) {
		return CodeStaleGeneration, false
	}
	rec, ok := cc.terminalControlAllowed(local)
	if !ok {
		return CodeCapabilityRefused, false
	}
	if rec.SessionInstance != gen.instance || (instance != "" && instance != gen.instance) {
		return CodeStaleInstance, false
	}
	return "", true
}

// TerminalInputSink is the optional DaemonAPI seam handleTerminalInput delivers to: the
// daemon-side write of raw bytes onto a terminal_fallback session's PTY. A backend that
// does not implement it refuses every input frame, which is R8a's correct state.
type TerminalInputSink interface {
	TerminalInput(local string, p []byte) error
}

// handleTerminalInput serves one UNSIGNED raw-input frame.
//
// IT NEVER TOUCHES THE LEASE PLANE (OPEN-C4). The generation is not the control lease, and
// the absence of any lease call in this function is the ruling, not an omission.
func (cc *clientConn) handleTerminalInput(c Control) {
	body := c.TerminalInput
	if body == nil {
		cc.replyErrorCode("terminal_input: missing terminal_input body", CodeInvalidField)
		return
	}
	local, ok := cc.resolveSession(c)
	if !ok {
		return
	}
	if code, ok := cc.liveTerminalGeneration(local, body.SessionInstance, body.ControlGeneration); !ok {
		// Refused, and NOTHING IS BUFFERED (T6-f / D-NOQUEUE). A refusal that held the
		// bytes for a later retry would convert live-only input into a short offline
		// queue at the one place a queue can form.
		cc.replyErrorCode("terminal_input: refused; nothing was typed", code)
		return
	}
	sink, ok := cc.srv.d.(TerminalInputSink)
	if !ok {
		cc.replyErrorCode("terminal_input: not supported by this daemon; nothing was typed", CodeNotImplemented)
		return
	}
	if err := sink.TerminalInput(local, body.Bytes); err != nil {
		cc.replyError("terminal_input: " + err.Error())
		return
	}
	cc.replyOK(c.SessionID)
}

// handleTerminalControlKeepalive is the second and last unsigned frame kind. It renews the
// MISSING-KEEPALIVE DEADLINE AND NOTHING ELSE, and it re-evaluates the same authority every
// input frame does -- a keepalive that renewed without re-checking would be a way to hold a
// revoked device's generation open.
//
// IT DOES NOT TOUCH THE SIGNED HORIZON (ADR-017 T7, round-2 blocker 1). A keepalive that
// moved the horizon would make the wall unreachable by construction: the phone need only
// keep asking, and "authority which cannot be revoked in real time" becomes unbounded.
func (cc *clientConn) handleTerminalControlKeepalive(c Control) {
	local, ok := cc.resolveSession(c)
	if !ok {
		return
	}
	if code, ok := cc.liveTerminalGeneration(local, "", c.ControlGeneration); !ok {
		cc.replyErrorCode("terminal_control_keepalive: refused", code)
		return
	}
	cc.srv.renewTerminalKeepalive(c.ControlGeneration, cc.srv.now().Add(TerminalKeepaliveTTL))
	cc.replyOK(c.SessionID)
}

// severTerminalGeneration drops this connection's control generation outright (ADR-017 T8's
// "synchronous at the daemon"), reporting whether there was one.
//
// IT IS NOT THE SAME THING as the per-frame re-check in liveTerminalGeneration, and
// round-2's blockers 2 and 8 are what the difference costs. The per-frame check only DROPS
// a frame while the condition holds: the generation itself survives in cc.termGen, so a
// kill switch turned OFF and back ON, or a device revoked and re-paired, RESUMED the very
// same generation typing onto the very same PTY with no fresh signed begin and no new
// device signature. That is verbatim the defect SeverAllRemoteControl's own comment says it
// exists to prevent for the lease. T6-e's per-frame re-check is defence in depth ON TOP of
// severance, never a substitute for it.
func (s *Server) severTerminalGenerations(match func(deviceID string) bool) int {
	// The counter is bumped BEFORE the sweep, exactly as severControl bumps severGen before
	// snapshotting: a begin whose authority was decided before this point re-checks under
	// termMu on publish, sees the counter advanced, and fails closed rather than escaping
	// (round-3 minor 7).
	s.termSeverGen.Add(1)
	s.termMu.Lock()
	defer s.termMu.Unlock()
	n := 0
	for id, gen := range s.termGens {
		if match(gen.deviceID) {
			delete(s.termGens, id)
			n++
		}
	}
	return n
}

// severTerminalGenerationsForSession drops every generation over one LOCAL session id,
// whichever device signed it and whichever connection minted it.
//
// ADR-017 T8 lists SESSION KILL and SESSION REPLACEMENT as severance triggers, and after
// round 3 moved generations to a server-wide registry `severTerminalControl` had exactly two
// callers -- the kill switch and device revocation -- neither of which is either of those.
// Fail-closed in effect is not the same claim as severed: a generation that outlives its
// session is one the NEXT incarnation under the same id inherits, and "refused on the next
// frame" needs a next frame, which is precisely what the phone that has gone away will never
// send.
//
// It bumps the sever counter for publishTerminalGenerationIfCurrent's reason: a begin whose
// authority was decided before this point must fail closed rather than escape the sweep.
func (s *Server) severTerminalGenerationsForSession(local string) int {
	if local == "" {
		return 0
	}
	s.termSeverGen.Add(1)
	s.termMu.Lock()
	defer s.termMu.Unlock()
	n := 0
	for id, gen := range s.termGens {
		if gen.session == local {
			delete(s.termGens, id)
			n++
		}
	}
	return n
}

// anyLiveTerminalGenerationFor reports whether any generation over local is still in the
// registry. Like anyLiveTerminalGeneration it is the observable severance is asserted on:
// "gone from the server's own state" is a different fact from "the next frame would be
// refused", and T8 is about the first.
func (s *Server) anyLiveTerminalGenerationFor(local string) bool {
	s.termMu.Lock()
	defer s.termMu.Unlock()
	for _, gen := range s.termGens {
		if gen.session == local {
			return true
		}
	}
	return false
}

// currentSessionInstance is the incarnation the backend's capability record names for local,
// and false when there is no record to read. It is the sweep's replacement detector.
func (s *Server) currentSessionInstance(local string) (string, bool) {
	lookup, ok := s.d.(SessionCapabilityLookup)
	if !ok {
		return "", false
	}
	rec, ok := lookup.SessionCapabilities(local)
	if !ok {
		return "", false
	}
	return rec.SessionInstance, true
}

// severReplacedTerminalGenerations drops every generation whose bound incarnation is no
// longer the one the session's capability record names (ADR-017 T8 / T8-a).
//
// IT RUNS ON THE SERVER'S OWN CLOCK, in the sweep that already exists for T6-c, and that is
// the honest strength of it: the daemon has no notification seam that tells this server an
// incarnation was re-minted, so severance AT THE INSTANT of replacement is not buildable
// without one. What this does provide is the property T8 actually cares about -- severance
// that never waits for a phone frame. The amendment in ADR-017 records both the strength and
// the missing seam, which is a precondition of the parked control slice.
//
// A session with NO READABLE RECORD is left alone rather than swept. The lookup is absent on
// the owner tier and on a backend that publishes no records, and reading "cannot tell" as
// "replaced" would sweep every generation on every tick for those.
func (s *Server) severReplacedTerminalGenerations() int {
	s.termMu.Lock()
	sessions := make(map[string]struct{}, len(s.termGens))
	for _, gen := range s.termGens {
		sessions[gen.session] = struct{}{}
	}
	s.termMu.Unlock()

	stale := map[string]string{} // session -> the incarnation that replaced the bound one
	for local := range sessions {
		if inst, ok := s.currentSessionInstance(local); ok {
			stale[local] = inst
		}
	}

	s.termMu.Lock()
	defer s.termMu.Unlock()
	n := 0
	for id, gen := range s.termGens {
		want, known := stale[gen.session]
		if !known || want == gen.instance {
			continue
		}
		delete(s.termGens, id)
		n++
	}
	return n
}

// expireTerminalGenerations drops every generation either of whose walls has passed at now.
// It is the sweep's whole body (T6-c): an IDLE generation with no inbound frames at all
// expires on the server's own clock, never on frame arrival.
func (s *Server) expireTerminalGenerations(now time.Time) int {
	s.termMu.Lock()
	defer s.termMu.Unlock()
	n := 0
	for id, gen := range s.termGens {
		if now.Before(gen.horizon) && now.Before(gen.keepalive) {
			continue
		}
		delete(s.termGens, id)
		n++
	}
	return n
}
