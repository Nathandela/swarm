package relay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// Clock is the single authoritative time source the relay reads for every TTL,
// rate window, presence timeout, and retention cap (ADR-007). Tests inject a
// fake clock so no assertion depends on a real sleep.
type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Option configures a Server at construction.
type Option func(*Server)

// WithClock injects the authoritative clock.
func WithClock(c Clock) Option { return func(s *Server) { s.clk = c } }

// WithAPNsSink injects the push transport (nil = pushes are dropped).
func WithAPNsSink(a APNsSink) Option { return func(s *Server) { s.apns = a } }

// WithLogWriter directs the relay's (body-free) log output.
func WithLogWriter(w io.Writer) Option {
	return func(s *Server) { s.logger = log.New(w, "relay ", log.LstdFlags) }
}

// WithSourceKeyFunc installs the pre-authentication source-key deriver. The relay
// evaluates it ONCE per accepted connection (passing that connection's transport
// RemoteAddr) and uses the result to key every PRE-SIGNATURE rate window —
// auth_init and the unauthenticated rendezvous ops — instead of any client-
// presented (and still unproven) relay-auth pubkey (ADR-007 amendment 2026-07-20,
// remediating R1-H1/H2). A nil fn keeps the default (the IP host of RemoteAddr).
func WithSourceKeyFunc(fn func(remoteAddr string) string) Option {
	return func(s *Server) {
		if fn != nil {
			s.sourceKeyFn = fn
		}
	}
}

// defaultSourceKey derives a connection's pre-auth rate key from its RemoteAddr by
// stripping the port, so every connection from one IP host shares a single source
// window. On loopback this collapses all connections to one source.
func defaultSourceKey(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

// serverCaps is the relay's capability set; r_hello negotiates the intersection.
var serverCaps = map[string]bool{
	"mailbox": true, "push": true, "presence": true, "rendezvous": true,
}

// rateWindow is a fixed one-minute window evaluated on the injected clock.
type rateWindow struct {
	start time.Time
	count int
}

func (w *rateWindow) allow(now time.Time, limit int) bool {
	if now.Sub(w.start) >= time.Minute {
		w.start = now
		w.count = 0
	}
	if w.count >= limit {
		return false
	}
	w.count++
	return true
}

// presenceEntry is a routing id's ephemeral presence (never persisted).
type presenceEntry struct {
	connected      bool
	disconnectedAt time.Time
	notified       bool
	state          PresenceState
}

// rdvSlot is a live pairing rendezvous: at most two participants keyed by id.
type rdvSlot struct {
	createdAt time.Time
	creator   *serverConn
	claimer   *serverConn
}

func (sl *rdvSlot) other(sc *serverConn) *serverConn {
	if sl.creator == sc {
		return sl.claimer
	}
	if sl.claimer == sc {
		return sl.creator
	}
	return nil
}

func (sl *rdvSlot) detach(sc *serverConn) {
	if sl.creator == sc {
		sl.creator = nil
	}
	if sl.claimer == sc {
		sl.claimer = nil
	}
}

// Server is the untrusted relay: a websocket listener over a bbolt store. It
// forwards ciphertext and routing metadata and holds no plaintext or identity
// keys.
type Server struct {
	cfg    Config
	clk    Clock
	apns   APNsSink
	logger *log.Logger
	st     *store

	ln      net.Listener
	httpSrv *http.Server
	url     string

	baseCtx    context.Context
	baseCancel context.CancelFunc
	closeOnce  sync.Once

	mu         sync.Mutex
	sessions   map[string]*serverConn // rid -> active authenticated conn
	presence   map[string]*presenceEntry
	tokens     map[string]string // rid -> APNs token (ephemeral)
	rendezvous map[string]*rdvSlot
	burned     map[string]bool // completed (single-use) rendezvous ids
	conns      map[*serverConn]struct{}
	authRate   map[string]*rateWindow // pre-signature auth_init attempts, keyed by TRANSPORT SOURCE (ConnPerMin)
	opsRate    map[string]*rateWindow // state-touching ops: pre-signature keyed by source, post-signature keyed by "rid:"+rid (OpsPerMin)
	appendRate map[string]*rateWindow
	pushRate   map[string]*rateWindow

	// sourceKeyFn derives a connection's pre-authentication rate key from its
	// transport RemoteAddr. It is evaluated ONCE per accepted connection. The
	// default strips the port so all connections from one IP host collapse to a
	// single source (ADR-007 amendment 2026-07-20). A pubkey the client presents
	// in auth_init is NEVER a rate key: it is unproven until a signature verifies.
	sourceKeyFn func(remoteAddr string) string
}

// New constructs a relay over cfg.DBPath. It opens the persistence store; call
// Start to bind the listener.
func New(cfg Config, opts ...Option) (*Server, error) {
	s := &Server{
		cfg:         cfg,
		clk:         realClock{},
		logger:      log.New(io.Discard, "", 0),
		sessions:    make(map[string]*serverConn),
		presence:    make(map[string]*presenceEntry),
		tokens:      make(map[string]string),
		rendezvous:  make(map[string]*rdvSlot),
		burned:      make(map[string]bool),
		conns:       make(map[*serverConn]struct{}),
		authRate:    make(map[string]*rateWindow),
		opsRate:     make(map[string]*rateWindow),
		appendRate:  make(map[string]*rateWindow),
		pushRate:    make(map[string]*rateWindow),
		sourceKeyFn: defaultSourceKey,
	}
	for _, o := range opts {
		o(s)
	}
	st, err := openStore(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	s.st = st
	return s, nil
}

// Start binds the listener and begins serving. The relay lives until Close (or
// ctx cancellation).
func (s *Server) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	s.ln = ln
	s.url = "ws://" + ln.Addr().String()
	s.baseCtx, s.baseCancel = context.WithCancel(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleHTTP)
	s.httpSrv = &http.Server{Handler: mux}
	go func() { _ = s.httpSrv.Serve(ln) }()
	return nil
}

// URL is the relay's ws:// endpoint (plain ws is intentional — E2EE does not
// depend on TLS).
func (s *Server) URL() string { return s.url }

// Close severs every connection, stops the listener, and closes the store. It is
// idempotent.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		if s.baseCancel != nil {
			s.baseCancel()
		}
		s.mu.Lock()
		conns := make([]*serverConn, 0, len(s.conns))
		for sc := range s.conns {
			conns = append(conns, sc)
		}
		s.mu.Unlock()
		for _, sc := range conns {
			sc.cancel()
			_ = sc.ws.CloseNow()
		}
		if s.httpSrv != nil {
			_ = s.httpSrv.Close()
		}
		if s.ln != nil {
			_ = s.ln.Close()
		}
		if s.st != nil {
			_ = s.st.close()
		}
	})
	return nil
}

func (s *Server) handleHTTP(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	ws.SetReadLimit(MaxFrame + 64)
	s.serveConn(ws, r.RemoteAddr)
}

// serverConn is one live connection's server-side state.
type serverConn struct {
	s      *Server
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	wmu    sync.Mutex

	// sourceKey is this connection's pre-authentication rate key, derived ONCE at
	// accept time from its transport RemoteAddr (never from a presented pubkey).
	sourceKey string

	authed     bool
	rid        string
	authNonce  []byte
	pendingPub ed25519.PublicKey
	pendingRID string
	superseded atomic.Bool

	rdvID    string
	rdvInbox chan []byte
}

func (s *Server) serveConn(ws *websocket.Conn, remoteAddr string) {
	s.mu.Lock()
	if capN := s.cfg.Quotas.MaxConcurrentConnections; capN > 0 && len(s.conns) >= capN {
		// CR-1 admission control: over the global live-connection cap, refuse the
		// (cap+1)th socket cleanly rather than admit it into an unbounded pool.
		s.mu.Unlock()
		_ = ws.CloseNow()
		return
	}
	ctx, cancel := context.WithCancel(s.baseCtx)
	sc := &serverConn{s: s, ws: ws, ctx: ctx, cancel: cancel, sourceKey: s.sourceKeyFn(remoteAddr)}
	s.conns[sc] = struct{}{}
	s.mu.Unlock()
	defer func() {
		cancel()
		s.removeConn(sc)
		_ = ws.CloseNow()
	}()
	for {
		tag, payload, err := sc.readFrame()
		if err != nil {
			return
		}
		if err := sc.dispatch(tag, payload); err != nil {
			return
		}
	}
}

func (s *Server) removeConn(sc *serverConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, sc)
	// H3: keep the pre-auth (source) and post-auth (rid) rate maps bounded — every
	// window is tied to at least one live connection. When the last connection that
	// shares a source key (or routing id) disconnects, its windows are reaped, so an
	// attacker cannot mint unbounded rate-limit state.
	sourceLive, ridLive := false, false
	for other := range s.conns {
		if other.sourceKey == sc.sourceKey {
			sourceLive = true
		}
		if sc.rid != "" && other.rid == sc.rid {
			ridLive = true
		}
	}
	if !sourceLive {
		delete(s.authRate, sc.sourceKey)
		delete(s.opsRate, sc.sourceKey)
	}
	if sc.rid != "" && !ridLive {
		delete(s.opsRate, "rid:"+sc.rid)
	}
	if sc.rdvID != "" {
		if slot, ok := s.rendezvous[sc.rdvID]; ok {
			slot.detach(sc)
		}
	}
	if sc.authed {
		if cur, ok := s.sessions[sc.rid]; ok && cur == sc {
			delete(s.sessions, sc.rid)
			if p := s.presence[sc.rid]; p != nil {
				p.connected = false
				p.disconnectedAt = s.clk.Now()
				p.notified = false
			}
		}
	}
}

func (sc *serverConn) readFrame() (MsgType, []byte, error) {
	// CR-1: bound reads on a connection that has neither authenticated nor joined
	// a rendezvous. A socket that completes the ws handshake but sends no frame is
	// closed within HandshakeTimeout (slowloris / fd-exhaustion defense), while an
	// established (authenticated or rendezvous) connection may idle indefinitely.
	// These fields are only ever mutated in this connection's own dispatch
	// goroutine, so reading them here without the lock is race-free.
	ctx := sc.ctx
	if to := sc.s.cfg.HandshakeTimeout; to > 0 && !sc.authed && sc.rdvID == "" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(sc.ctx, to)
		defer cancel()
	}
	mt, data, err := sc.ws.Read(ctx)
	if err != nil {
		return 0, nil, err
	}
	if mt != websocket.MessageBinary {
		return 0, nil, errors.New("relay: non-binary frame")
	}
	return ReadFrame(bytes.NewReader(data))
}

func (sc *serverConn) writeFrame(tag MsgType, payload []byte) error {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, tag, payload); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(sc.ctx, 10*time.Second)
	defer cancel()
	sc.wmu.Lock()
	defer sc.wmu.Unlock()
	return sc.ws.Write(ctx, websocket.MessageBinary, buf.Bytes())
}

func (sc *serverConn) reply(tag MsgType, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return sc.writeFrame(tag, b)
}

func (sc *serverConn) replyOK(v any) error { return sc.reply(MsgOK, v) }
func (sc *serverConn) replyErr(code string) error {
	return sc.reply(MsgError, errorBody{Code: code})
}

// requireAuth gates authenticated ops: an unauthenticated conn is refused, and a
// superseded conn (a newer connection took over its routing id) is told so.
func (sc *serverConn) requireAuth() (string, bool) {
	if !sc.authed {
		return codeNotAuthorized, false
	}
	if sc.superseded.Load() {
		return codeDuplicateConn, false
	}
	return "", true
}

// opSource identifies the source a state-touching op is metered against. AFTER a
// signature verifies, the op is keyed by the PROVEN routing id ("rid:"+rid), so
// each authenticated identity gets its own per-key window (ADR-007 amendment point
// 4). BEFORE any signature verifies (mid-handshake auth_resp, the unauthenticated
// rendezvous ops), the op is keyed by TRANSPORT SOURCE — never by the unproven
// presented pubkey — so no per-unproven-key state is ever retained (R1-H2/H3).
func (sc *serverConn) opSource() string {
	if sc.rid != "" {
		return "rid:" + sc.rid
	}
	return sc.sourceKey
}

// meterOp charges one unit against the per-source OpsPerMin window (CR-2 /
// R-REL.8). It is called at the TOP of every state-touching op — before the op's
// own auth/validation — so abuse is metered even when the op would otherwise
// short-circuit (e.g. a revoke on an already-unpaired target). A limit <= 0 is
// unlimited.
func (sc *serverConn) meterOp() bool {
	limit := sc.s.cfg.Quotas.OpsPerMin
	if limit <= 0 {
		return true
	}
	key := sc.opSource()
	sc.s.mu.Lock()
	w := sc.s.opsRate[key]
	if w == nil {
		w = &rateWindow{}
		sc.s.opsRate[key] = w
	}
	ok := w.allow(sc.s.clk.Now(), limit)
	sc.s.mu.Unlock()
	return ok
}

func (sc *serverConn) dispatch(tag MsgType, payload []byte) error {
	switch tag {
	case MsgMailboxAppend:
		return sc.handleMailboxAppend(payload)
	case MsgRelay:
		var env struct {
			Op string `json:"op"`
		}
		if err := json.Unmarshal(payload, &env); err != nil {
			return sc.replyErr(codeBadRequest)
		}
		switch env.Op {
		case "hello":
			return sc.handleHello(payload)
		case "auth_init":
			return sc.handleAuthInit(payload)
		case "auth_resp":
			return sc.handleAuthResp(payload)
		case "authorize_device":
			return sc.handleAuthorizeDevice(payload)
		case "mailbox_read":
			return sc.handleMailboxRead(payload)
		case "mailbox_ack":
			return sc.handleMailboxAck(payload)
		case "token_register":
			return sc.handleTokenRegister(payload)
		case "token_delete":
			return sc.handleTokenDelete(payload)
		case "presence":
			return sc.handlePresence(payload)
		case "push_trigger":
			return sc.handlePushTrigger(payload)
		case "device_revoke":
			return sc.handleDeviceRevoke(payload)
		case "rendezvous_create":
			return sc.handleRendezvousCreate(payload)
		case "rendezvous_claim":
			return sc.handleRendezvousClaim(payload)
		case "rendezvous_send":
			return sc.handleRendezvousSend(payload)
		case "rendezvous_recv":
			return sc.handleRendezvousRecv(payload)
		case "rendezvous_complete":
			return sc.handleRendezvousComplete(payload)
		default:
			return sc.replyErr(codeBadRequest)
		}
	default:
		return sc.replyErr(codeBadRequest)
	}
}

// --- handshake -------------------------------------------------------------

func (sc *serverConn) handleHello(payload []byte) error {
	var req struct {
		Version int      `json:"version"`
		Caps    []string `json:"caps"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return sc.replyErr(codeBadRequest)
	}
	if req.Version != ProtocolVersion {
		return sc.replyErr(codeUnsupported)
	}
	agreed := make([]string, 0, len(req.Caps))
	for _, c := range req.Caps {
		if serverCaps[c] {
			agreed = append(agreed, c)
		}
	}
	return sc.replyOK(map[string]any{"version": ProtocolVersion, "caps": agreed})
}

func (sc *serverConn) handleAuthInit(payload []byte) error {
	var req struct {
		RelayAuthPub []byte `json:"relay_auth_pub"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return sc.replyErr(codeBadRequest)
	}
	if len(req.RelayAuthPub) != ed25519.PublicKeySize {
		return sc.replyErr(codeBadRequest)
	}
	pub := ed25519.PublicKey(append([]byte(nil), req.RelayAuthPub...))
	rid := RoutingID(pub)
	if sc.s.st.isRevoked(rid) {
		return sc.replyErr(codeRevoked)
	}
	// Pre-signature rate limiting is keyed by the TRANSPORT SOURCE, never by the
	// presented relay-auth pubkey (which is unproven until a signature verifies).
	// A per-source auth_init window (ConnPerMin) bounds one network source without
	// letting an attacker exhaust a victim identity's budget by presenting the
	// victim's pubkey, and without minting unbounded per-key state (ADR-007
	// amendment 2026-07-20, remediating R1-H1/H2). There is no global auth counter
	// a single source could monopolize (R1-H3).
	sc.s.mu.Lock()
	w := sc.s.authRate[sc.sourceKey]
	if w == nil {
		w = &rateWindow{}
		sc.s.authRate[sc.sourceKey] = w
	}
	ok := w.allow(sc.s.clk.Now(), sc.s.cfg.Quotas.ConnPerMin)
	sc.s.mu.Unlock()
	if !ok {
		return sc.replyErr(codeQuotaExceeded)
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return sc.replyErr(codeBadRequest)
	}
	sc.authNonce = nonce
	sc.pendingPub = pub
	sc.pendingRID = rid
	return sc.replyOK(map[string]any{"nonce": nonce})
}

func (sc *serverConn) handleAuthResp(payload []byte) error {
	if !sc.meterOp() {
		return sc.replyErr(codeQuotaExceeded)
	}
	if sc.authNonce == nil {
		return sc.replyErr(codeBadRequest)
	}
	var req struct {
		Signature []byte `json:"signature"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return sc.replyErr(codeBadRequest)
	}
	msg := AuthChallengeMessage(sc.authNonce, sc.pendingRID)
	if len(req.Signature) != ed25519.SignatureSize || !ed25519.Verify(sc.pendingPub, msg, req.Signature) {
		return sc.replyErr(codeAuthFailed)
	}
	// No global auth counter is charged here: admission is bounded by the per-source
	// auth_init window (above) plus MaxConcurrentConnections/HandshakeTimeout, none
	// of which a single source can monopolize to lock out other sources (R1-H3).
	sc.authed = true
	sc.rid = sc.pendingRID
	sc.authNonce = nil
	sc.s.registerSession(sc)
	return sc.replyOK(map[string]any{"routing_id": sc.rid})
}

// registerSession binds sc as the live session for its routing id, superseding
// any older connection (newest-wins takeover) and marking presence online.
func (s *Server) registerSession(sc *serverConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.sessions[sc.rid]; ok && old != sc {
		old.superseded.Store(true)
	}
	s.sessions[sc.rid] = sc
	p := s.presence[sc.rid]
	if p == nil {
		p = &presenceEntry{}
		s.presence[sc.rid] = p
	}
	p.connected = true
	p.notified = false
	p.state = PresenceOnline
}

// --- pairing / mailbox / push ----------------------------------------------

func (sc *serverConn) handleAuthorizeDevice(payload []byte) error {
	if !sc.meterOp() {
		return sc.replyErr(codeQuotaExceeded)
	}
	if code, ok := sc.requireAuth(); !ok {
		return sc.replyErr(code)
	}
	var req struct {
		DevicePub []byte `json:"device_pub"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return sc.replyErr(codeBadRequest)
	}
	if len(req.DevicePub) != ed25519.PublicKeySize {
		return sc.replyErr(codeBadRequest)
	}
	deviceRID := RoutingID(ed25519.PublicKey(req.DevicePub))
	if err := sc.s.st.addPair(sc.rid, deviceRID); err != nil {
		return sc.replyErr(codeBadRequest)
	}
	return sc.replyOK(map[string]any{})
}

func (sc *serverConn) handleMailboxAppend(payload []byte) error {
	if code, ok := sc.requireAuth(); !ok {
		return sc.replyErr(code)
	}
	var req struct {
		Target   string `json:"target"`
		Envelope []byte `json:"envelope"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return sc.replyErr(codeBadRequest)
	}
	if !sc.s.st.isPaired(sc.rid, req.Target) {
		return sc.replyErr(codeNotAuthorized)
	}
	sc.s.mu.Lock()
	w := sc.s.appendRate[req.Target]
	if w == nil {
		w = &rateWindow{}
		sc.s.appendRate[req.Target] = w
	}
	allowed := w.allow(sc.s.clk.Now(), sc.s.cfg.Quotas.MailboxAppendPerMin)
	sc.s.mu.Unlock()
	if !allowed {
		return sc.replyErr(codeQuotaExceeded)
	}
	cur, err := sc.s.st.appendItem(req.Target, req.Envelope, sc.s.clk.Now().UnixMilli())
	if err != nil {
		return sc.replyErr(codeBadRequest)
	}
	return sc.replyOK(map[string]any{"cursor": cur})
}

func (sc *serverConn) handleMailboxRead(payload []byte) error {
	if !sc.meterOp() {
		return sc.replyErr(codeQuotaExceeded)
	}
	if code, ok := sc.requireAuth(); !ok {
		return sc.replyErr(code)
	}
	var req struct {
		Cursor uint64 `json:"cursor"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return sc.replyErr(codeBadRequest)
	}
	items, err := sc.s.st.readItems(sc.rid, req.Cursor)
	if err != nil {
		return sc.replyErr(codeBadRequest)
	}
	if items == nil {
		items = []Item{}
	}
	return sc.replyOK(map[string]any{"items": items})
}

func (sc *serverConn) handleMailboxAck(payload []byte) error {
	if !sc.meterOp() {
		return sc.replyErr(codeQuotaExceeded)
	}
	if code, ok := sc.requireAuth(); !ok {
		return sc.replyErr(code)
	}
	var req struct {
		Cursor uint64 `json:"cursor"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return sc.replyErr(codeBadRequest)
	}
	if err := sc.s.st.ackItems(sc.rid, req.Cursor); err != nil {
		return sc.replyErr(codeBadRequest)
	}
	return sc.replyOK(map[string]any{})
}

func (sc *serverConn) handleTokenRegister(payload []byte) error {
	if !sc.meterOp() {
		return sc.replyErr(codeQuotaExceeded)
	}
	if code, ok := sc.requireAuth(); !ok {
		return sc.replyErr(code)
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return sc.replyErr(codeBadRequest)
	}
	sc.s.mu.Lock()
	sc.s.tokens[sc.rid] = req.Token
	sc.s.mu.Unlock()
	return sc.replyOK(map[string]any{})
}

func (sc *serverConn) handleTokenDelete(_ []byte) error {
	if !sc.meterOp() {
		return sc.replyErr(codeQuotaExceeded)
	}
	if code, ok := sc.requireAuth(); !ok {
		return sc.replyErr(code)
	}
	sc.s.mu.Lock()
	delete(sc.s.tokens, sc.rid)
	sc.s.mu.Unlock()
	return sc.replyOK(map[string]any{})
}

func (sc *serverConn) handlePresence(payload []byte) error {
	if !sc.meterOp() {
		return sc.replyErr(codeQuotaExceeded)
	}
	if code, ok := sc.requireAuth(); !ok {
		return sc.replyErr(code)
	}
	var req struct {
		Target string `json:"target"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return sc.replyErr(codeBadRequest)
	}
	return sc.replyOK(PresenceInfo{State: sc.s.presenceState(req.Target)})
}

func (s *Server) presenceState(rid string) PresenceState {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.presence[rid]
	if p == nil {
		return PresenceUnknown
	}
	if p.connected {
		return PresenceOnline
	}
	return p.state
}

func (sc *serverConn) handlePushTrigger(payload []byte) error {
	if code, ok := sc.requireAuth(); !ok {
		return sc.replyErr(code)
	}
	var req struct {
		Target   string `json:"target"`
		Envelope []byte `json:"envelope"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return sc.replyErr(codeBadRequest)
	}
	if !sc.s.st.isPaired(sc.rid, req.Target) {
		return sc.replyErr(codeNotAuthorized)
	}
	sc.s.mu.Lock()
	w := sc.s.pushRate[req.Target]
	if w == nil {
		w = &rateWindow{}
		sc.s.pushRate[req.Target] = w
	}
	allowed := w.allow(sc.s.clk.Now(), sc.s.cfg.Quotas.PushPerMin)
	tok := sc.s.tokens[req.Target]
	sc.s.mu.Unlock()
	if !allowed {
		return sc.replyErr(codeQuotaExceeded)
	}
	if tok != "" {
		sc.s.deliverPush(sc.ctx, tok, APNsPayload{Alert: GenericPushAlert, Ciphertext: req.Envelope})
	}
	return sc.replyOK(map[string]any{})
}

func (sc *serverConn) handleDeviceRevoke(payload []byte) error {
	if !sc.meterOp() {
		return sc.replyErr(codeQuotaExceeded)
	}
	if code, ok := sc.requireAuth(); !ok {
		return sc.replyErr(code)
	}
	var req struct {
		Target string `json:"target"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return sc.replyErr(codeBadRequest)
	}
	if !sc.s.st.isPaired(sc.rid, req.Target) {
		return sc.replyErr(codeNotAuthorized)
	}
	_ = sc.s.st.removePair(sc.rid, req.Target)
	_ = sc.s.st.revoke(req.Target)
	_ = sc.s.st.purgeMailbox(req.Target)
	sc.s.mu.Lock()
	if old, ok := sc.s.sessions[req.Target]; ok {
		old.superseded.Store(true)
		delete(sc.s.sessions, req.Target)
	}
	delete(sc.s.tokens, req.Target)
	delete(sc.s.presence, req.Target)
	sc.s.mu.Unlock()
	return sc.replyOK(map[string]any{})
}

func (s *Server) deliverPush(ctx context.Context, token string, p APNsPayload) {
	if s.apns == nil || token == "" {
		return
	}
	_ = s.apns.Push(ctx, token, p)
}

// --- rendezvous ------------------------------------------------------------

func (sc *serverConn) handleRendezvousCreate(payload []byte) error {
	if !sc.meterOp() {
		return sc.replyErr(codeQuotaExceeded)
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return sc.replyErr(codeBadRequest)
	}
	now := sc.s.clk.Now()
	sc.s.mu.Lock()
	sc.s.purgeExpiredRendezvous(now)
	// HI-1: never blindly overwrite. A burned (completed, single-use) id or a live
	// slot is refused so the original creator's in-flight pairing is never
	// orphaned or hijacked.
	if sc.s.burned[req.ID] {
		sc.s.mu.Unlock()
		return sc.replyErr(codeRendezvousUsed)
	}
	if _, exists := sc.s.rendezvous[req.ID]; exists {
		sc.s.mu.Unlock()
		return sc.replyErr(codeRendezvousExists)
	}
	if len(sc.s.rendezvous) >= sc.s.cfg.Quotas.MaxConcurrentRendezvous {
		sc.s.mu.Unlock()
		return sc.replyErr(codeQuotaExceeded)
	}
	sc.s.rendezvous[req.ID] = &rdvSlot{createdAt: now, creator: sc}
	sc.rdvID = req.ID
	sc.rdvInbox = make(chan []byte, 16)
	sc.s.mu.Unlock()
	return sc.replyOK(map[string]any{})
}

func (sc *serverConn) handleRendezvousClaim(payload []byte) error {
	if !sc.meterOp() {
		return sc.replyErr(codeQuotaExceeded)
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return sc.replyErr(codeBadRequest)
	}
	now := sc.s.clk.Now()
	sc.s.mu.Lock()
	defer sc.s.mu.Unlock()
	if sc.s.burned[req.ID] {
		return sc.replyErr(codeRendezvousUsed)
	}
	slot, ok := sc.s.rendezvous[req.ID]
	if !ok {
		return sc.replyErr(codeRendezvousTTL)
	}
	if now.Sub(slot.createdAt) >= sc.s.cfg.RendezvousTTL {
		delete(sc.s.rendezvous, req.ID)
		return sc.replyErr(codeRendezvousTTL)
	}
	if slot.creator != nil && slot.claimer != nil {
		return sc.replyErr(codeRendezvousFull)
	}
	slot.claimer = sc
	sc.rdvID = req.ID
	sc.rdvInbox = make(chan []byte, 16)
	return sc.replyOK(map[string]any{})
}

func (sc *serverConn) handleRendezvousSend(payload []byte) error {
	if !sc.meterOp() {
		return sc.replyErr(codeQuotaExceeded)
	}
	var req struct {
		ID   string `json:"id"`
		Data []byte `json:"data"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return sc.replyErr(codeBadRequest)
	}
	sc.s.mu.Lock()
	slot, ok := sc.s.rendezvous[req.ID]
	// HI-1: only a participant (creator/claimer) may inject into a rendezvous; a
	// non-participant is cleanly refused rather than silently told success.
	if !ok || (slot.creator != sc && slot.claimer != sc) {
		sc.s.mu.Unlock()
		return sc.replyErr(codeNotAuthorized)
	}
	var inbox chan []byte
	if target := slot.other(sc); target != nil {
		inbox = target.rdvInbox
	}
	sc.s.mu.Unlock()
	if inbox != nil {
		select {
		case inbox <- append([]byte(nil), req.Data...):
		default:
		}
	}
	return sc.replyOK(map[string]any{})
}

func (sc *serverConn) handleRendezvousRecv(_ []byte) error {
	sc.s.mu.Lock()
	inbox := sc.rdvInbox
	sc.s.mu.Unlock()
	if inbox == nil {
		return sc.replyErr(codeBadRequest)
	}
	select {
	case data := <-inbox:
		return sc.replyOK(map[string]any{"data": data})
	case <-sc.ctx.Done():
		return sc.ctx.Err()
	}
}

func (sc *serverConn) handleRendezvousComplete(payload []byte) error {
	if !sc.meterOp() {
		return sc.replyErr(codeQuotaExceeded)
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return sc.replyErr(codeBadRequest)
	}
	sc.s.mu.Lock()
	slot, ok := sc.s.rendezvous[req.ID]
	// HI-1: only a participant may burn the id, so a third party cannot burn a
	// victim's in-flight pairing.
	if !ok || (slot.creator != sc && slot.claimer != sc) {
		sc.s.mu.Unlock()
		return sc.replyErr(codeNotAuthorized)
	}
	delete(sc.s.rendezvous, req.ID)
	sc.s.burned[req.ID] = true
	sc.s.mu.Unlock()
	return sc.replyOK(map[string]any{})
}

func (s *Server) purgeExpiredRendezvous(now time.Time) {
	for id, slot := range s.rendezvous {
		if now.Sub(slot.createdAt) >= s.cfg.RendezvousTTL {
			delete(s.rendezvous, id)
		}
	}
}

// --- clock-driven sweeps ---------------------------------------------------

// SweepPresence transitions machines that dropped past PresenceTimeout to
// offline and fires exactly one silent push per transition toward each paired
// device that has a registered token (R-REL.3).
func (s *Server) SweepPresence(ctx context.Context) {
	now := s.clk.Now()
	var tokens []string
	s.mu.Lock()
	for rid, p := range s.presence {
		if p.connected || p.notified {
			continue
		}
		if now.Sub(p.disconnectedAt) < s.cfg.PresenceTimeout {
			continue
		}
		p.state = PresenceOffline
		p.notified = true
		for _, peer := range s.st.pairedPeers(rid) {
			if tok := s.tokens[peer]; tok != "" {
				tokens = append(tokens, tok)
			}
		}
	}
	s.mu.Unlock()
	for _, tok := range tokens {
		s.deliverPush(ctx, tok, APNsPayload{Alert: GenericPushAlert})
	}
}

// SweepRetention purges mailbox items older than the retention cap even if never
// acked (R-REL.10).
func (s *Server) SweepRetention(_ context.Context) {
	cutoff := s.clk.Now().Add(-s.cfg.RetentionCap).UnixMilli()
	_ = s.st.purgeOlderThan(cutoff)
}

// MailboxDepth reports how many items a routing id's mailbox holds (test/ops
// visibility; revocation asserts it drops to zero).
func (s *Server) MailboxDepth(rid string) int { return s.st.mailboxDepth(rid) }
