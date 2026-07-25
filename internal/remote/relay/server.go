package relay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
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

// WithPushSink injects the push transport (nil = pushes are dropped).
func WithPushSink(p PushSink) Option { return func(s *Server) { s.push = p } }

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

const (
	// defaultMailboxPageItems bounds a mailbox_read page when the client asks for
	// no explicit limit (limit <= 0). The byte budget below independently keeps a
	// page under MaxFrame, so this only caps the count of small items (CR-4).
	defaultMailboxPageItems = 256
	// maxMailboxItemWrapper conservatively over-covers the JSON framing around ONE
	// item in a mailbox_read reply — {"has_more":false,"items":[{"cursor":<=20
	// digits,"envelope":".."}]} is ~74 bytes at the largest uint64 cursor. An
	// append-time envelope-size cap using it guarantees a single stored item always
	// fits one reply page under MaxFrame at any cursor magnitude, so a maximally-sized
	// envelope can never become an un-servable page head and brick the read
	// (CR-4 / R2 review H-1).
	maxMailboxItemWrapper = 96
	// mailboxPageByteBudget is the estimated-serialized-size ceiling for one
	// mailbox_read page. It sits well under MaxFrame so the JSON reply — the items
	// plus the {"items":[...],"has_more":bool} wrapper plus the 5-byte frame header
	// — can never trip WriteFrame's ErrFrameTooLarge and tear the connection. The
	// per-item estimate (base64 envelope length + mailboxItemJSONOverhead) is an
	// over-estimate of the real JSON cost, so the true reply is always smaller than
	// this budget, and the headroom absorbs the wrapper and framing (CR-4).
	mailboxPageByteBudget = MaxFrame - 8192
)

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
	push   PushSink
	logger *log.Logger
	st     *store

	ln      net.Listener
	httpSrv *http.Server
	url     string

	baseCtx    context.Context
	baseCancel context.CancelFunc
	closeOnce  sync.Once
	sweepWG    sync.WaitGroup
	// pushWG tracks in-flight background push deliveries so Close joins them BEFORE the
	// store shuts. Measured, not assumed: a write to a closed bbolt handle returns
	// "database not open" rather than panicking, so the cost of NOT joining is a goroutine
	// outliving Close and, if its verdict was UNREGISTERED, a LOST prune — the dead token
	// stays in the store and comes back on the next restart. Cheap to prevent, so
	// prevented. Adds are guarded by the closing flag below.
	pushWG sync.WaitGroup

	mu sync.Mutex
	// closing is set once Close begins, under mu, so no NEW background push delivery can
	// start after Close snapshots the set it is about to wait for. It is what makes
	// pushWG.Add and pushWG.Wait mutually exclusive rather than merely unlikely to race.
	closing  bool
	sessions map[string]*serverConn  // rid -> active authenticated conn
	waits    map[string]*pendingWait // rid -> its single parked server-side wait (§6.0 caps it at 1)
	presence map[string]*presenceEntry
	// tokens is the in-memory cache of rid -> push token, hydrated from the store's
	// tokens bucket at New and written through on every mutation (PB-PUSH-6). It is a
	// cache, not the record: the store is what survives a restart.
	tokens     map[string]string
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
		waits:       make(map[string]*pendingWait),
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
	// PB-PUSH-6: resume with the push tokens the previous run held. Fail closed on an
	// unreadable tokens bucket rather than booting with an empty map, which would look
	// exactly like a fleet that had never registered — silently push-less, with the loss
	// lasting until each user next opens the app (ADR-007 B16: backgrounding disconnects,
	// so a forgotten token cannot re-register on its own).
	tokens, err := st.loadTokens()
	if err != nil {
		_ = st.close()
		return nil, err
	}
	s.tokens = tokens
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

	// CR-3: when a sweep interval is configured, run the clock-driven maintenance
	// sweeps (presence-went-silent pushes + retention purges) on a timer instead of
	// leaving them to be called by hand. The ticker cadence is wall-clock, but every
	// TTL/retention DECISION each sweep makes still reads the injected clock. The
	// goroutine is guarded by baseCtx and joined in Close, so it neither leaks nor
	// races the store shutdown. SweepInterval <= 0 (the DefaultConfig value) disables
	// the loop, preserving the manual-sweep behavior the existing tests depend on.
	if s.cfg.SweepInterval > 0 {
		s.sweepWG.Add(1)
		go s.runSweeps()
	}
	return nil
}

// runSweeps ticks every SweepInterval and runs both sweeps until baseCtx is
// canceled (Close). It is the CR-3 production wiring of the clock-driven sweeps.
func (s *Server) runSweeps() {
	defer s.sweepWG.Done()
	ticker := time.NewTicker(s.cfg.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.baseCtx.Done():
			return
		case <-ticker.C:
			s.SweepPresence(s.baseCtx)
			s.SweepRetention(s.baseCtx)
		}
	}
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
		// No further background push delivery may start from here on (see closing): every
		// one already started is joined below, before the store closes.
		s.closing = true
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
		// Join the sweep goroutine (baseCancel above signaled it) before closing the
		// store, so an in-flight SweepRetention can never touch a closed bbolt handle
		// (CR-3: no leak, no store-shutdown race).
		s.sweepWG.Wait()
		// Same ordering for background push deliveries: one that ends in an UNREGISTERED
		// verdict prunes the token from the store, and a prune that lands after the store
		// closes is simply lost — the dead token returns on the next start. baseCancel above
		// already cancelled their contexts, so this returns as fast as the sender honours it.
		s.pushWG.Wait()
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
	// acceptedAt is when this connection was accepted, anchoring the CUMULATIVE
	// handshake deadline in readFrame (CR-1 slice 2): unlike a per-read idle
	// window, it cannot be reset by a drip of harmless pre-auth frames.
	acceptedAt time.Time

	authed bool
	rid    string
	// wait is this connection's parked server-side wait, or nil. It is guarded by
	// Server.mu, NOT by the connection's own goroutine, because the wait is
	// released from three places under that lock (its own goroutine's release, a
	// newest-wins takeover, and removeConn). Holding the pointer here rather than
	// re-deriving it from rid is what makes the release exact: rid can be rewritten
	// by a re-authentication, and a lookup by the current value would orphan the
	// slot under the old one.
	wait       *pendingWait
	authNonce  []byte
	pendingPub ed25519.PublicKey
	pendingRID string
	superseded atomic.Bool

	rdvID    string
	rdvInbox chan []byte
}

func (s *Server) serveConn(ws *websocket.Conn, remoteAddr string) {
	sourceKey := s.sourceKeyFn(remoteAddr)
	s.mu.Lock()
	if capN := s.cfg.Quotas.MaxConcurrentConnections; capN > 0 && len(s.conns) >= capN {
		// CR-1 admission control: over the global live-connection cap, refuse the
		// (cap+1)th socket cleanly rather than admit it into an unbounded pool.
		s.mu.Unlock()
		_ = ws.CloseNow()
		return
	}
	if capN := s.cfg.Quotas.MaxConcurrentConnectionsPerSource; capN > 0 {
		// CR-1 slice 2: same admission control as the global cap above, scoped to
		// this connection's source, so one source cannot monopolize the pool.
		// ponytail: a linear scan over s.conns is fine at the documented scale; a
		// per-source counter map is the upgrade path if throughput demands it.
		n := 0
		for c := range s.conns {
			if c.sourceKey == sourceKey {
				n++
			}
		}
		if n >= capN {
			s.mu.Unlock()
			_ = ws.CloseNow()
			return
		}
	}
	ctx, cancel := context.WithCancel(s.baseCtx)
	// acceptedAt anchors the CUMULATIVE handshake deadline in readFrame. It uses
	// the real wall clock, not the injected s.clk: context.WithDeadline always
	// resolves against real time, and s.clk is a logical clock tests can freeze
	// or jump independently of it (matching HandshakeTimeout's existing
	// real-wall-clock behavior via context.WithTimeout).
	sc := &serverConn{s: s, ws: ws, ctx: ctx, cancel: cancel, sourceKey: sourceKey, acceptedAt: time.Now()}
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
	// A dead connection's wait slot is freed here as well as by serveWait's own
	// defer, so the slot is never held by a connection that no longer exists.
	s.severWaitLocked(sc, waitCancelled)
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
	// CR-1: bound the CUMULATIVE time-to-authenticate on a connection that has
	// neither authenticated nor joined a rendezvous, anchored at accept time —
	// not a fresh per-read idle window, which a drip of harmless frames under
	// HandshakeTimeout could otherwise reset forever (CR-1 slice 2). An
	// established (authenticated or rendezvous) connection may idle indefinitely.
	// These fields are only ever mutated in this connection's own dispatch
	// goroutine, so reading them here without the lock is race-free.
	ctx := sc.ctx
	if to := sc.s.cfg.HandshakeTimeout; to > 0 && !sc.authed && sc.rdvID == "" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(sc.ctx, sc.acceptedAt.Add(to))
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
		case "mailbox_wait":
			return sc.handleMailboxWait(payload)
		case "mailbox_wait_cancel":
			return sc.handleMailboxWaitCancel(payload)
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
	sc.authNonce = nil
	sc.s.registerSession(sc) // sets sc.rid under s.mu, where removeConn scans other.rid
	return sc.replyOK(map[string]any{"routing_id": sc.pendingRID})
}

// registerSession binds sc as the live session for its routing id, superseding
// any older connection (newest-wins takeover) and marking presence online.
func (s *Server) registerSession(sc *serverConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc.rid = sc.pendingRID // write rid under the lock: removeConn scans other.rid here (R1b review HIGH-1)
	if old, ok := s.sessions[sc.rid]; ok && old != sc {
		old.superseded.Store(true)
		// A superseded connection issues no further requests, so a wait parked on it
		// would never learn it lost the routing id: it would sit out its whole
		// ceiling holding the single per-client wait slot, and THIS connection could
		// not park its own — live typing dead for up to a ceiling after every
		// reconnect (PB-NET-5(d): newest-wins must not be weakened).
		s.severWaitLocked(old, waitSuperseded)
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
	// ADR-007 B22: this also LIFTS any ban standing against deviceRID. See
	// store.authorizePair for why the owner's machine is the only party that can
	// reach it, and therefore why un-banning here is the owner's own decision.
	if err := sc.s.st.authorizePair(sc.rid, deviceRID); err != nil {
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
	// CR-4: refuse an append that would push the mailbox past its depth cap BEFORE
	// storing it — a clean ErrQuotaExceeded, never unbounded growth. The cap is on
	// LIVE depth, so capacity recovers once the device drains and acks. A value <= 0
	// means no depth cap.
	if capN := sc.s.cfg.Quotas.MailboxMaxItems; capN > 0 && sc.s.st.mailboxDepth(req.Target) >= capN {
		return sc.replyErr(codeQuotaExceeded)
	}
	// CR-4 / R2 review H-1: refuse an envelope so large that a mailbox_read reply
	// carrying it alone would exceed MaxFrame. readItemsPage always emits at least one
	// item (progress guarantee), so an un-servable item would tear the read connection
	// permanently. Cap the envelope at append so every stored item is always readable
	// within one page at any cursor magnitude. base64 is the on-wire size.
	if base64.StdEncoding.EncodedLen(len(req.Envelope))+maxMailboxItemWrapper > MaxFrame-1 {
		return sc.replyErr(codeBadRequest)
	}
	cur, err := sc.s.st.appendItem(req.Target, req.Envelope, sc.s.clk.Now().UnixMilli())
	if err != nil {
		return sc.replyErr(codeBadRequest)
	}
	// Wake the target's parked wait BEFORE replying: the appender's own round-trip
	// is not on the recipient's latency path, and the recipient is the one typing.
	sc.s.notifyMailbox(req.Target)
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
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return sc.replyErr(codeBadRequest)
	}
	// CR-4: a mailbox_read reply is ALWAYS a bounded page. limit caps the item
	// count (a sane server default when limit <= 0), and mailboxPageByteBudget
	// caps the serialized size so an oversized backlog can never overflow one
	// frame and tear the connection (the permanent brick the reviewer flagged).
	maxItems := req.Limit
	if maxItems <= 0 {
		maxItems = defaultMailboxPageItems
	}
	items, hasMore, err := sc.s.st.readItemsPage(sc.rid, req.Cursor, maxItems, mailboxPageByteBudget)
	if err != nil {
		return sc.replyErr(codeBadRequest)
	}
	if items == nil {
		items = []Item{}
	}
	return sc.replyOK(map[string]any{"items": items, "has_more": hasMore})
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
	// Durable BEFORE the cache (PB-PUSH-6): a persist failure must leave the relay
	// reporting the failure rather than holding a token that vanishes on restart.
	if err := sc.s.st.putToken(sc.rid, req.Token); err != nil {
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
	// The cache first, then the store: the reverse order would leave a window in which a
	// restart-free relay still pushes to a token the device just revoked. If the persist
	// then fails the device is told, and the next restart resurrecting the token is the
	// failure it was told about — never a silent one.
	sc.s.mu.Lock()
	delete(sc.s.tokens, sc.rid)
	sc.s.mu.Unlock()
	if err := sc.s.st.deleteToken(sc.rid); err != nil {
		return sc.replyErr(codeBadRequest)
	}
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
		sc.s.deliverPush(req.Target, tok, PushPayload{Alert: GenericPushAlert, Ciphertext: req.Envelope})
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
	if err := sc.s.st.revokeAndPurge(sc.rid, req.Target); err != nil {
		return sc.replyErr(codeBadRequest)
	}
	sc.s.mu.Lock()
	var old *serverConn
	if o, ok := sc.s.sessions[req.Target]; ok {
		old = o
		old.superseded.Store(true)
		delete(sc.s.sessions, req.Target)
	}
	// The durable half rode inside revokeAndPurge's single transaction above; this drops
	// the cache so no push reaches the revoked device before the next restart either.
	delete(sc.s.tokens, req.Target)
	delete(sc.s.presence, req.Target)
	sc.s.mu.Unlock()
	// ME-1: sever the revoked target's live socket OUTSIDE s.mu (mirrors
	// Server.Close()'s collect-then-close pattern) — cancel() unblocks its
	// serveConn's blocking ws.Read(ctx); CloseNow() is belt-and-suspenders.
	if old != nil {
		old.cancel()
		_ = old.ws.CloseNow()
	}
	return sc.replyOK(map[string]any{})
}

// pushVerdictWait is how long deliverPush blocks its CALLER waiting for the provider's
// verdict before letting the delivery finish in the background.
//
// It exists because deliverPush is reached from the connection's REQUEST LOOP, and this
// package's standing invariant is that nothing blocks that loop — handleMailboxWait goes to
// the trouble of parking its wait on a separate goroutine for exactly this reason. The
// stalled loop is the MACHINE's, and the gateway re-registers its mailbox_wait on that same
// connection, so time spent here is time the gateway is not noticing the phone's keystrokes,
// against PB-NET-5's 150 ms p50 budget. The realistic shape is multi-session: agent A goes
// idle and fires a wake while the user is typing into session B.
//
// It is not zero because the UNREGISTERED verdict must have pruned the token before the next
// trigger reads it, and a pure fire-and-forget would decide that after the fact. One second
// covers a normal provider round trip several times over — including the extra OAuth exchange
// a cold sender pays — so in practice the prune still lands before the reply.
//
// The bound was invisible until this slice, because every configured sink was until now a
// test double that answered instantly. A REAL sender retries a 5xx up to
// push.DefaultMaxAttempts times over its own request timeouts, which is why the RETRIES must
// not be on this clock.
const pushVerdictWait = time.Second

// pushDeliveryBudget is the TOTAL lifetime of one delivery, retries included. It runs on a
// background goroutine past pushVerdictWait, so it is generous enough to let the sender's
// retry schedule actually complete (three attempts plus its inter-attempt delays) while still
// capping how long an in-flight push can outlive the request that started it.
const pushDeliveryBudget = 10 * time.Second

// deliverPush hands one push to the transport and ACTS ON ITS VERDICT.
//
// Reading that error is the whole point (PB-PUSH-2): a sender can classify an UNREGISTERED
// response perfectly and change nothing, because the previous `_ = s.push.Push(...)`
// discarded it — a pruning signal nobody reads is a pruning signal that does not exist.
// Exactly one verdict prunes; every other failure leaves the token alone, because pruning on
// a transient provider outage would disable push for every live handset the relay holds and
// nothing would surface until users started missing hand-offs.
//
// THE SPLIT. The delivery runs on its own goroutine and the caller waits only
// pushVerdictWait for it. A sink that answers within that window — every fast verdict,
// including every UNREGISTERED, which is classified non-retryable and returns on the attempt
// that sees it — has therefore pruned before the caller resumes. A sink still grinding
// through retries does not hold the request loop for them. That is the whole difference
// between a 10 s worst-case stall on the machine's connection and a 1 s one.
//
// It also means a LATER-attempt UNREGISTERED still prunes — just in the background. That
// case is real (a 503 then a dead token, or an OAuth blip on the first attempt), so pruning
// only on a first-attempt verdict would have quietly stopped pruning for it.
//
// A push failure is never propagated to the caller: push_trigger answers OK either way, so a
// provider outage cannot make the gateway read the relay itself as failing (PB-PUSH-5).
func (s *Server) deliverPush(rid, token string, p PushPayload) {
	if s.push == nil || token == "" {
		return
	}
	// Registered under s.mu against the closing flag, so Close's Wait can never race an Add:
	// after Close sets closing, no further delivery starts, and every started one is joined
	// before the store closes. Without that, an in-flight prune could reach deleteToken on a
	// shut bbolt handle — the same hazard CR-3 already joins the sweep goroutine for.
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return
	}
	s.pushWG.Add(1)
	s.mu.Unlock()

	// Deliberately NOT the caller's context. The caller is the machine's request loop (or the
	// sweep), and the push targets a DIFFERENT device: neither that request finishing nor that
	// machine disconnecting is a reason to abandon the delivery, least of all the prune that
	// follows an UNREGISTERED verdict.
	ctx, cancel := context.WithTimeout(s.deliveryBase(), pushDeliveryBudget)
	done := make(chan struct{})
	go func() {
		defer s.pushWG.Done()
		defer cancel()
		defer close(done)
		s.pushAndReconcile(ctx, rid, token, p)
	}()

	t := time.NewTimer(pushVerdictWait)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	}
}

// deliveryBase is the parent context every background delivery hangs off: the server's, so
// Close cancels them all promptly and pushWG.Wait returns quickly. Start installs it; a
// Server that was constructed but never started falls back to Background so deliverPush is
// still safe to call.
func (s *Server) deliveryBase() context.Context {
	if s.baseCtx != nil {
		return s.baseCtx
	}
	return context.Background()
}

// pushAndReconcile performs the delivery and applies the provider's verdict.
func (s *Server) pushAndReconcile(ctx context.Context, rid, token string, p PushPayload) {
	err := s.push.Push(ctx, token, p)
	if err == nil {
		return
	}
	if !errors.Is(err, ErrPushUnregistered) {
		// Body-free, like every other relay log line: no token, no routing id.
		s.logger.Printf("push delivery failed (transient; token kept)")
		return
	}
	// Compare-and-delete: between the send and here the device may have re-registered a
	// FRESH token, and pruning that one would silence a handset that just came back.
	s.mu.Lock()
	stale := s.tokens[rid] == token
	if stale {
		delete(s.tokens, rid)
	}
	s.mu.Unlock()
	if !stale {
		return
	}
	if err := s.st.deleteToken(rid); err != nil {
		s.logger.Printf("pruning an unregistered push token failed to persist")
	}
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
	// The routing id rides alongside its token so an UNREGISTERED verdict here prunes the
	// same way a push_trigger's does: a dead handset must not keep a token alive merely
	// because the only thing still pushing to it is the presence sweep.
	type pushTarget struct{ rid, token string }
	var targets []pushTarget
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
				targets = append(targets, pushTarget{rid: peer, token: tok})
			}
		}
	}
	s.mu.Unlock()
	for _, t := range targets {
		s.deliverPush(t.rid, t.token, PushPayload{Alert: GenericPushAlert})
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
