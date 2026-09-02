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
	"strings"
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
// evaluates it ONCE per accepted connection and uses the result to key every
// PRE-SIGNATURE rate window — auth_init and the unauthenticated rendezvous ops —
// instead of any client-presented (and still unproven) relay-auth pubkey (ADR-007
// amendment 2026-07-20, remediating R1-H1/H2). A nil fn keeps the default (the IP
// host of RemoteAddr).
//
// The value passed in is that connection's transport RemoteAddr UNLESS
// trusted_proxies is configured and the peer matches it (playbook 6.5, R2
// "proxy-quota"): then it is the validated client address recovered from
// X-Forwarded-For instead, which typically carries NO port. A custom fn doing
// SplitHostPort should not assume a port is always present; see
// resolveSourceAddr.
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
//
// "wait" is the bounded server-side mailbox_wait (ADR-007 B7, PB-NET-5). It is advertised
// HERE, in the handshake the protocol has had all along, because the alternative was
// measured and rejected by the final-audit committee (finding H1): a client that has to
// PROBE the op learns "unsupported" only from a timeout, and a pre-wait relay's dispatch
// answers the unknown op with an ordinary in-order MsgError the client's parked waiter
// cannot correlate -- a stray reply that shifts the connection's request/reply pairing.
// With the capability advertised, a client never sends the op to a relay that did not
// claim it, and a reconnect to an upgraded relay re-evaluates for free.
// CapabilityMailboxRecovery is advertised by a gateway connection only when that
// gateway understands the destructive-mailbox-recovery command and will replace the
// compacted backlog with a roster reseed. The relay uses the negotiated capability on
// the CURRENT paired gateway session as a deletion fence; an old or disconnected
// gateway therefore cannot authorize a new phone to discard data it would not replace.
const CapabilityMailboxRecovery = "mailbox_recovery"

var serverCaps = map[string]bool{
	"mailbox": true, "push": true, "presence": true, "rendezvous": true, "wait": true,
	CapabilityMailboxRecovery: true,
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
	// maxRequestHeaderBytes bounds http.Server.MaxHeaderBytes, replacing
	// net/http's DefaultMaxHeaderBytes (1 MiB). Reviewer finding, R2
	// "proxy-quota" (BLOCKING): net/http retains a header-parsing buffer up to
	// that bound on EVERY connection, BEFORE any relay code runs — before auth,
	// before serveConn's MaxConcurrentConnections admission check. Behind the
	// shipped trusted-proxy topology (relay.config.example ships trusted_proxies
	// on by default), the request headers include an X-Forwarded-For a real
	// internet client controls end-to-end, so the 1 MiB default is an
	// unauthenticated, per-connection allocation amplifier. 32 KiB is generous
	// for any legitimate websocket handshake (Sec-WebSocket-*, cookies, and a
	// realistic X-Forwarded-For chain of a handful of hops) while cutting the
	// worst case by 32x.
	maxRequestHeaderBytes = 32 << 10
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
	// listening reports whether ln is currently accepting (set true once Start's
	// net.Listen succeeds, false from Close onward). /readyz reads it directly
	// (CR-style admission-control pattern already used elsewhere in this file).
	listening atomic.Bool

	// The admin surface (playbook 6.5): /healthz + /readyz on a SEPARATE
	// loopback-only listener, never the public one.
	adminLn    net.Listener
	adminSrv   *http.Server
	adminURL   string
	diskFreeFn func() (uint64, error)
	// diskLowWarned is guarded by mu (below) and makes the low-disk log warning
	// edge-triggered rather than once-per-/readyz-poll.
	diskLowWarned bool

	// operatorSecret is the R2 diagnostic/admin-authority secret (playbook 6.5),
	// installed via WithOperatorSecret. nil/empty means diagnostics are
	// disabled. NEVER logged.
	operatorSecret []byte
	// diagUsedNonces is a minted diagnostic capability's nonce -> the instant its
	// TTL window closes: single-use enforcement (playbook 6.5, "capability TTL
	// <= 5 min, single-use"). A window, not a permanent record, exactly like
	// `burned` below and for the same reason -- purged lazily in spendDiagNonce
	// (diag.go). Guarded by mu.
	//
	// IN-MEMORY ONLY, so a relay restart forgets every spent nonce (see
	// DiagnosticCapabilityTTL's comment in diag.go for the honest blast-radius
	// accounting -- it is small, not zero).
	diagUsedNonces map[string]time.Time

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
	// burned is rid -> the instant the burn may be forgotten: a rendezvous id that is
	// spent, whether it COMPLETED or merely aged out (ADR-007 B47b). It is a window and
	// not a tombstone because rendezvous_create carries no requireAuth, so a permanent
	// entry is one an unauthenticated stranger can mint at will — a fix that traded the
	// hijack below for memory exhaustion would not be a fix. See burnRendezvous.
	burned     map[string]time.Time
	conns      map[*serverConn]struct{}
	authRate   map[string]*rateWindow // pre-signature auth_init attempts, keyed by TRANSPORT SOURCE (ConnPerMin)
	opsRate    map[string]*rateWindow // state-touching ops: pre-signature keyed by source, post-signature keyed by "rid:"+rid (OpsPerMin)
	appendRate map[string]*rateWindow
	pushRate   map[string]*rateWindow

	// sourceKeyFn derives a connection's pre-authentication rate key. It is
	// evaluated ONCE per accepted connection, from its transport RemoteAddr
	// UNCHANGED, or — with trusted_proxies configured and the peer matching —
	// from the validated X-Forwarded-For-derived client address instead (see
	// trustedProxies below and resolveSourceAddr). The default strips the port
	// so all connections from one IP host collapse to a single source (ADR-007
	// amendment 2026-07-20); a forwarded address typically has no port to strip.
	// A pubkey the client presents in auth_init is NEVER a rate key: it is
	// unproven until a signature verifies.
	sourceKeyFn func(remoteAddr string) string

	// trustedProxies is cfg.TrustedProxies, parsed once at New (playbook 6.5,
	// R2 "proxy-quota"). Empty (the default) keeps sourceKeyFn's input exactly
	// r.RemoteAddr, today's behavior; see resolveSourceAddr.
	trustedProxies []*net.IPNet
}

// New constructs a relay over cfg.DBPath. It opens the persistence store; call
// Start to bind the listener.
func New(cfg Config, opts ...Option) (*Server, error) {
	s := &Server{
		cfg:            cfg,
		clk:            realClock{},
		logger:         log.New(io.Discard, "", 0),
		sessions:       make(map[string]*serverConn),
		waits:          make(map[string]*pendingWait),
		presence:       make(map[string]*presenceEntry),
		tokens:         make(map[string]string),
		rendezvous:     make(map[string]*rdvSlot),
		burned:         make(map[string]time.Time),
		conns:          make(map[*serverConn]struct{}),
		authRate:       make(map[string]*rateWindow),
		opsRate:        make(map[string]*rateWindow),
		appendRate:     make(map[string]*rateWindow),
		pushRate:       make(map[string]*rateWindow),
		sourceKeyFn:    defaultSourceKey,
		diagUsedNonces: make(map[string]time.Time),
	}
	s.diskFreeFn = defaultDiskFreeFn(cfg.DBPath)
	trustedProxies, err := parseTrustedProxies(cfg.TrustedProxies)
	if err != nil {
		return nil, err
	}
	s.trustedProxies = trustedProxies
	for _, o := range opts {
		o(s)
	}
	st, err := openStore(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	s.st = st
	st.configureAdmission(cfg.Quotas, s.clk, s.diskFreeFn)
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
	// Bind the admin surface FIRST: a rejected/failed admin bind must never
	// leave the public listener open with nothing left to close it.
	if err := s.startAdmin(); err != nil {
		return err
	}

	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		s.closeAdmin()
		return err
	}
	s.ln = ln
	s.url = "ws://" + ln.Addr().String()
	s.listening.Store(true)
	s.baseCtx, s.baseCancel = context.WithCancel(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleHTTP)
	s.httpSrv = &http.Server{Handler: mux, MaxHeaderBytes: maxRequestHeaderBytes}
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
			s.SweepRendezvous(s.baseCtx)
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
		s.listening.Store(false)
		if s.httpSrv != nil {
			_ = s.httpSrv.Close()
		}
		s.closeAdmin()
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
	// playbook 6.5, R2 "proxy-quota": resolved to the X-Forwarded-For-derived
	// client address ONLY when r.RemoteAddr is a configured trusted proxy;
	// otherwise identical to r.RemoteAddr, today's behavior.
	//
	// r.Header.Values, NOT r.Header.Get: Get returns only the FIRST
	// X-Forwarded-For header LINE. An add-header-style proxy (HAProxy's
	// default `option forwardfor`) emits the client's original header as a
	// SEPARATE second line rather than merging into it, so Get alone would
	// read only the client-controlled first line and never see the entry the
	// trusted proxy itself appended -- exactly the bucket a client could then
	// choose per connection. Joining every line with "," before splitting on
	// "," keeps resolveSourceAddr's rightmost-hop rule correct regardless of
	// whether the trusted proxy merges into one line or adds a new one.
	xff := strings.Join(r.Header.Values("X-Forwarded-For"), ",")
	sourceAddr := resolveSourceAddr(r.RemoteAddr, xff, s.trustedProxies)
	s.serveConn(ws, sourceAddr)
}

// serverConn is one live connection's server-side state.
type serverConn struct {
	s      *Server
	ws     *websocket.Conn
	ctx    context.Context
	cancel context.CancelFunc
	wmu    sync.Mutex

	// sourceKey is this connection's pre-authentication rate key, derived ONCE at
	// accept time (never from a presented pubkey) from its transport RemoteAddr,
	// or — behind a configured trusted proxy — from the validated forwarded
	// client address instead; see sourceKeyFn.
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
	// pendingPeer is the counterparty this dial is here about, named by the dialer in
	// auth_init and answered in auth_resp (ADR-007 B49). Empty means "no verdict wanted",
	// which is what every machine-side dial sends.
	pendingPeer string
	// negotiatedCaps is the most recent successful r_hello intersection for this
	// exact connection. It is guarded by Server.mu because a paired phone consults
	// the active gateway's set from another connection's dispatch goroutine.
	negotiatedCaps map[string]bool
	superseded     atomic.Bool

	rdvID    string
	rdvInbox chan []byte
	// rdvDeadline is the instant this connection's rendezvous participation ends: one
	// RendezvousTTL — a whole slot lifetime — from the moment it joined, which is a
	// constant the relay picked either way (round-4 threat review C3). It bounds
	// BOTH the park in rendezvous_recv and — for a connection that never authenticated —
	// the reads in readFrame, so a rendezvous can no longer buy an unbounded socket.
	//
	// It is REAL wall-clock time, not the injected s.clk, for the same reason acceptedAt
	// is: context deadlines and timers resolve against real time, and s.clk is a logical
	// clock tests freeze and jump. The TTL DECISIONS about the slot itself (purge, claim)
	// still read s.clk, so the two never disagree about whether a slot is alive — this one
	// only decides when to stop waiting on a socket.
	rdvDeadline time.Time

	// diagOpen/diagItems/diagCursor/diagExpiresAt are this connection's
	// ephemeral diagnostic route (R2 doctor, playbook 4.1/6.5), unlocked by a
	// valid diag_open capability. It is state private to THIS connection --
	// entirely separate from the real mailbox store and from
	// bucketPairs/bucketConsents -- so the scoped diag_* ops can never read a
	// real mailbox or name another routing id: there is no target/rid
	// parameter anywhere in their wire shape. It dies with the connection;
	// nothing here is durable.
	diagOpen   bool
	diagItems  []Item
	diagCursor uint64
	// diagExpiresAt is the instant THIS route's capability TTL closes --
	// issuedAt + DiagnosticCapabilityTTL, stamped once at diag_open (R2 review
	// LOW-MEDIUM) -- checked by diagRouteLive (diag.go) on every subsequent
	// diag_append/diag_read/diag_status/diag_close, so the TTL bounds the
	// WHOLE route rather than just the moment it opens.
	diagExpiresAt time.Time
	// diagItemsBytes is the running estimated-serialized-size total of diagItems
	// (R2 review MEDIUM), the same per-item cost store.readItemsPage estimates.
	// handleDiagRead returns every item in ONE reply with no pagination to fall
	// back on, so this is checked against mailboxPageByteBudget at append time --
	// the read reply can then never exceed what mailbox_read's own page budget
	// already proves fits under MaxFrame.
	diagItemsBytes int
}

// attachRendezvousLocked binds sc to a rendezvous it has just created or claimed, giving
// it a fresh inbox and the deadline its participation is bounded by. s.mu is held.
func (sc *serverConn) attachRendezvousLocked(id string) {
	sc.rdvID = id
	sc.rdvInbox = make(chan []byte, 16)
	sc.rdvDeadline = time.Time{}
	if ttl := sc.s.cfg.RendezvousTTL; ttl > 0 {
		sc.rdvDeadline = time.Now().Add(ttl)
	}
}

// leaveRendezvousLocked detaches sc from the rendezvous it holds and BURNS that id if no
// participant is left. s.mu is held. It is the one place a connection lets go of a slot,
// and every path that takes a new one goes through it first (round-4 threat review C2).
//
// A CONNECTION PARTICIPATES IN AT MOST ONE RENDEZVOUS, and that is what was missing.
// handleRendezvousCreate overwrote sc.rdvID/sc.rdvInbox in place while removeConn detached
// only s.rendezvous[sc.rdvID] — the LAST id — so every earlier slot stayed occupied by a
// connection that could no longer receive on it, and stayed occupied after that connection
// disconnected. Measured: one unauthenticated connection took 64 of 64 slots, a legitimate
// machine's rendezvous_create was refused quota_exceeded, and all 64 were still held after
// the squatter went away. rendezvous_create needs no authentication.
//
// IT BURNS RATHER THAN MERELY FREEING, which is ADR-007 B47b and not a new rule: an id
// whose live pairing ended is as dead as one that completed, because the QR naming it is
// still on the owner's screen and a stranger re-creating the label receives the next phone
// to scan it. The burn is a WINDOW, so the burn set stays bounded (see burnWindow).
func (s *Server) leaveRendezvousLocked(sc *serverConn, now time.Time) {
	id := sc.rdvID
	if id == "" {
		return
	}
	sc.rdvID = ""
	sc.rdvInbox = nil
	sc.rdvDeadline = time.Time{}
	slot, ok := s.rendezvous[id]
	// The slot may be gone (expired, completed) or may have been re-created by somebody
	// else since; in neither case is this connection's departure an event for it.
	if !ok || (slot.creator != sc && slot.claimer != sc) {
		return
	}
	slot.detach(sc)
	if slot.creator == nil && slot.claimer == nil {
		s.burnRendezvous(id, now)
	}
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
	// The slot this connection held is released, and burned if nothing is left in it, so
	// the rendezvous table does not stay occupied by a party that has gone away (round-4
	// threat review C2).
	s.leaveRendezvousLocked(sc, s.clk.Now())
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

// preAuthDeadline is when an UNAUTHENTICATED connection must be done by, and whether it
// has such a bound at all. The fields it reads are only ever mutated in this connection's
// own dispatch goroutine, so it is race-free without the lock.
//
// CR-1: a connection that has neither authenticated nor joined a rendezvous is bounded by
// the CUMULATIVE time-to-authenticate, anchored at accept time — not by a fresh per-read
// idle window, which a drip of harmless frames under HandshakeTimeout could reset forever
// (CR-1 slice 2).
//
// AND A RENDEZVOUS CONNECTION IS BOUNDED BY ITS RENDEZVOUS (round-4 threat review C3).
// Joining one used to waive the deadline outright, leaving an unauthenticated socket with
// no deadline, no quota and no slot accounting — measured still live at 4x the handshake
// deadline AND after its own slot had aged out. There is nothing an unauthenticated
// connection can legitimately do past its slot's lifetime, so the waiver becomes an
// EXTENSION with an end. An AUTHENTICATED connection may still idle indefinitely: that is
// an established session, and every op it can issue is metered and authorized.
func (sc *serverConn) preAuthDeadline() (time.Time, bool) {
	if sc.authed {
		return time.Time{}, false
	}
	if sc.rdvID != "" {
		return sc.rdvDeadline, !sc.rdvDeadline.IsZero()
	}
	if to := sc.s.cfg.HandshakeTimeout; to > 0 {
		return sc.acceptedAt.Add(to), true
	}
	return time.Time{}, false
}

func (sc *serverConn) readFrame() (MsgType, []byte, error) {
	ctx := sc.ctx
	if deadline, bounded := sc.preAuthDeadline(); bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(sc.ctx, deadline)
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
		case "mailbox_discard":
			return sc.handleMailboxDiscard(payload)
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
		case "diag_open":
			return sc.handleDiagOpen(payload)
		case "diag_status":
			return sc.handleDiagStatus(payload)
		case "diag_append":
			return sc.handleDiagAppend(payload)
		case "diag_read":
			return sc.handleDiagRead(payload)
		case "diag_close":
			return sc.handleDiagClose(payload)
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
	negotiated := make(map[string]bool, len(req.Caps))
	for _, c := range req.Caps {
		if serverCaps[c] {
			agreed = append(agreed, c)
			negotiated[c] = true
		}
	}
	sc.s.mu.Lock()
	sc.negotiatedCaps = negotiated
	sc.s.mu.Unlock()
	return sc.replyOK(map[string]any{"version": ProtocolVersion, "caps": agreed})
}

func (sc *serverConn) handleAuthInit(payload []byte) error {
	var req struct {
		RelayAuthPub []byte `json:"relay_auth_pub"`
		Peer         string `json:"peer"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return sc.replyErr(codeBadRequest)
	}
	if len(req.RelayAuthPub) != ed25519.PublicKeySize {
		return sc.replyErr(codeBadRequest)
	}
	pub := ed25519.PublicKey(append([]byte(nil), req.RelayAuthPub...))
	rid := RoutingID(pub)
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
	sc.pendingPeer = req.Peer
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
	// THE REVOKED VERDICT, AND IT IS THE PEER'S ALONE (ADR-007 B49). A ban used to be a
	// standing refusal of the banned identity: one global row, consulted by every dial it
	// ever made, liftable only by its banner. That made every device_revoke MUTUAL ASSURED
	// DESTRUCTION — whoever fired first removed the other from the relay for good, and no
	// party the owner controlled could undo it, because lifting demands a signature under
	// the victim's own key that the banner has never held.
	//
	// ENFORCEMENT IS THE DELETED EDGE; THIS IS ONLY THE SIGNAL. A ban never enforced
	// anything: registration is open, so a banned party mints a fresh keypair and returns.
	// What actually severs access is the pairs edge revokeAndPurge deletes, which is
	// server-side and unforgeable. The ban's whole job is to TELL a revoked handset, so
	// PB-APP-10 can show a re-pair prompt instead of a reconnect loop — and a signal is
	// answered to whoever asks for it, which is why the dialer names the peer whose verdict
	// it is here for and gets no other party's.
	//
	// The relay cannot supply that asymmetry itself: after a revoke the machine and the
	// handset hold identical state — no edges, one ban — so every rule symmetric in
	// (banner, victim) either refuses both registrations or neither. The handset names its
	// pinned machine (mobile/relay.go); the machine names nobody, because no legitimate
	// flow revokes a machine at the relay — `swarm remote revoke` is the only production
	// caller of the verb, and the phone's own RevokeThisDevice rides the sealed command
	// plane to the gateway instead. So a stolen handset's ban reaches no verdict the
	// machine consults.
	//
	// It is answered HERE rather than at auth_init, where the ban used to be read, because
	// a ban is a fact about a PROVEN identity: pre-signature it was a free oracle for
	// anyone holding a routing id.
	if sc.pendingPeer != "" && sc.s.st.revokedBy(sc.pendingRID, sc.pendingPeer) {
		return sc.replyErr(codeRevoked)
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
		DevicePub  []byte `json:"device_pub"`
		ConsentSig []byte `json:"consent_sig"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return sc.replyErr(codeBadRequest)
	}
	if len(req.DevicePub) != ed25519.PublicKeySize {
		return sc.replyErr(codeBadRequest)
	}
	// THE NAMED DEVICE MUST HAVE CONSENTED, AND THIS IS THE ONLY PLACE THAT CAN BE
	// ESTABLISHED (ADR-007 B27's consent signature, made mandatory by B38).
	//
	// requireAuth above proves an identity and nothing more — relay auth is OPEN
	// REGISTRATION, so "authenticated" never meant "the owner's machine" — and a
	// caller naming a routing id is a statement about the CALLER's intent, not
	// about the named party. The relay cannot witness the QR/SAS ceremony that
	// conveys the real consent, so at this handler "machine authorizes the phone it
	// just paired" and "stranger authorizes the machine whose pubkey it photographed"
	// are the SAME SHAPE, and no predicate over the caller distinguishes them.
	//
	// B27 tried to distinguish them by the TARGET's state instead — you may act on a
	// target that has authorized nobody — and that rested on the premise that a
	// target's relay-auth pubkey is disclosed only at the relay handshake and over the
	// SAS-authenticated channel. B37 and B38 falsified the premise three ways: an
	// unprotected auth_init discloses it to a passive observer, pairing msg2 discloses
	// it to a QR photographer one round-trip BEFORE the mandatory desktop confirm and
	// before the SAS exists at all, and msg3 discloses the device's before the SAS
	// check. Holding the pubkey was the whole attack, and the harm was permanent: this
	// same rule gates device_revoke, revokeAndPurge records the ATTACKER as the banner,
	// and only the banner may lift a ban.
	//
	// So the target's consent is CARRIED here rather than inferred: the named device's
	// own relay-auth key over ConsentMessage(sc.rid). It is verified under the pubkey
	// the caller named, which is the key deviceRID derives from, so a caller cannot
	// name one party and satisfy the check with another's signature. It names the
	// GRANTEE's routing id, so it is not transferable to any other caller.
	// AND IT NAMES THE CEREMONY THAT PRODUCED IT (ADR-007 B47). The id rides in the
	// credential, but it is not TRUSTED from there: the signature is verified OVER it, so a
	// holder cannot relabel a retired consent into a live one. That is what lets the store
	// retire an id and have the retirement mean something.
	//
	// AND THE ID IS BOUNDED HERE, WHICH IS THE ONLY PLACE IT CAN BE (ADR-007 B61). Being
	// signed over makes an id unforgeable, not sane: the DEVICE chooses it, and a hostile
	// device is exactly the threat B25/B38 exist for. Everything between the ceremony and
	// this line carries the credential verbatim by design — internal/remote/device's
	// registry says so outright — so no earlier hop is the authority. An id above bbolt's
	// key limit is stored happily as a consent VALUE and then makes retiredKey unwritable,
	// which aborts the owner's revoke transaction for good; see maxCeremonyIDLen.
	ceremonyID, sig, perr := ParseConsent(req.ConsentSig)
	if perr != nil || ceremonyID == "" || len(ceremonyID) > maxCeremonyIDLen ||
		len(sig) != ed25519.SignatureSize ||
		!ed25519.Verify(ed25519.PublicKey(req.DevicePub), ConsentMessage(ceremonyID, sc.rid), sig) {
		return sc.replyErr(codeNotAuthorized)
	}
	deviceRID := RoutingID(ed25519.PublicKey(req.DevicePub))
	// A PARTY MAY NOT CONSENT TO ITSELF (ADR-007 B61). Every check above passes when the
	// caller names its OWN pubkey — it holds the key, so it can sign anything that key is
	// asked to sign — and what the signature then proves is vacuous: that the party holding
	// a key made a statement about the party holding that key. A pairing has two parties by
	// construction, so no ceremony this credential is supposed to carry the outcome of can
	// produce a self-consent; it is reachable only by asking for it directly.
	//
	// The edge it would write is not inert. pairs[X\x00X] is indistinguishable from a real
	// grant to every later reader, and grantsAnyone — which is how revokeAndPurge decides
	// whether any relationship still remains that could wake a handset — then answers true
	// for X forever. One self-consent by a phone therefore disables the push-token purge
	// PB-PUSH-9 requires, for every subsequent revoke by ANY party, leaving precisely the
	// "unreachable provider-visible identifier for a device its owner disowned" PB-PUSH-6
	// names. B49's `if !grantsAnyone(pb, rid)` is right and is left alone: what was wrong is
	// that a party could manufacture a relationship with itself, and grantsAnyone cannot
	// tell that edge from a real one.
	if deviceRID == sc.rid {
		return sc.replyErr(codeNotAuthorized)
	}
	// ADR-007 B22: this also LIFTS a ban standing against deviceRID — but ONLY one
	// sc.rid itself placed (B24). See store.authorizePair.
	switch err := sc.s.st.authorizePair(sc.rid, deviceRID, ceremonyID); {
	case err == nil:
	case isStorageAdmissionError(err):
		return sc.replyErr(codeQuotaExceeded)
	case errors.Is(err, errRetirementsFull):
		// ADR-007 B61's cap on retained retirements. quota_exceeded rather than
		// consent_retired because the credential is fine and the remedy is NOT "pair the
		// device again" — pairing again is the thing being refused. See
		// store.maxRetiredPerPair for why this refuses instead of evicting.
		return sc.replyErr(codeQuotaExceeded)
	case errors.Is(err, errConsentRetired):
		// Distinct from not_authorized on purpose: the credential is well-formed and
		// genuinely signed by the named device, and the remedy is a new pairing rather
		// than a different caller. A refusal that does not name its remedy is the
		// PB-STATE-10 wall this project has already hit once.
		return sc.replyErr(codeConsentRetired)
	default:
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
	// The target must have authorized this caller, or have authorized nobody at
	// all (ADR-007 B27, store.mayActOn). Pairing alone is NOT the gate: it used to
	// be, and a pairing is created by one side naming the other, so the gate proved
	// only that the caller had spoken (ADR-007 B25).
	if !sc.s.st.mayActOn(sc.rid, req.Target) {
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
	//
	// THE CAP IS CHARGED PER (SENDER, TARGET), NOT PER TARGET. Per target it was a
	// shared budget with no owner: any sender the target would accept could hold
	// the mailbox at its cap and every OTHER sender's append was refused for as
	// long as it kept doing so — the owner's own handset locked out of its own
	// machine by somebody else's backlog. That is a live-depth condition, so it
	// lifted on a drain and returned on the next append: sustainable, not one-shot.
	// Charged per sender, a backlog can only ever refuse the party that built it.
	//
	// What bounds the TOTAL is then the number of senders a target accepts, which
	// is what store.mayActOn bounds: the parties it authorized, plus — until it
	// authorizes its first one — anyone holding its relay-auth pubkey, whose rate
	// of arrival is itself bounded by the per-source connection budget above.
	if capN := sc.s.cfg.Quotas.MailboxMaxItems; capN > 0 && sc.s.st.mailboxDepthFrom(req.Target, sc.rid) >= capN {
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
	cur, err := sc.s.st.appendItem(req.Target, sc.rid, req.Envelope, sc.s.clk.Now().UnixMilli())
	if err != nil {
		if isStorageAdmissionError(err) {
			return sc.replyErr(codeQuotaExceeded)
		}
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
		Cursor      uint64  `json:"cursor"`
		Limit       int     `json:"limit"`
		Incarnation *string `json:"mailbox_incarnation"`
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
	if req.Incarnation != nil && *req.Incarnation == "" && req.Cursor > 0 {
		// One-time migration for a generation-aware consumer whose durable legacy
		// checkpoint predates the field. It must rewind before it can safely adopt.
		return sc.replyErr(codeMailboxCursorReset)
	}
	expected := ""
	if req.Incarnation != nil {
		expected = *req.Incarnation
	}
	items, hasMore, resetRequired, incarnation, err := sc.s.st.readItemsPageForIncarnation(sc.rid, expected, req.Cursor, maxItems, mailboxPageByteBudget)
	if err != nil {
		return sc.replyErr(codeBadRequest)
	}
	if resetRequired {
		return sc.replyErr(codeMailboxCursorReset)
	}
	if items == nil {
		items = []Item{}
	}
	return sc.replyOK(map[string]any{"items": items, "has_more": hasMore, "mailbox_incarnation": incarnation})
}

func (sc *serverConn) handleMailboxAck(payload []byte) error {
	if !sc.meterOp() {
		return sc.replyErr(codeQuotaExceeded)
	}
	if code, ok := sc.requireAuth(); !ok {
		return sc.replyErr(code)
	}
	var req struct {
		Cursor      uint64  `json:"cursor"`
		Incarnation *string `json:"mailbox_incarnation"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return sc.replyErr(codeBadRequest)
	}
	if req.Incarnation != nil && *req.Incarnation == "" && req.Cursor > 0 {
		return sc.replyErr(codeMailboxCursorReset)
	}
	expected := ""
	if req.Incarnation != nil {
		expected = *req.Incarnation
	}
	if err := sc.s.st.ackItemsForIncarnation(sc.rid, expected, req.Cursor); errors.Is(err, ErrMailboxCursorResetRequired) {
		return sc.replyErr(codeMailboxCursorReset)
	} else if err != nil {
		return sc.replyErr(codeBadRequest)
	}
	return sc.replyOK(map[string]any{})
}

// handleMailboxDiscard is the explicitly destructive recovery for an authenticated caller's
// OWN mailbox. Unlike mailbox_ack it accepts no target and no numeric cursor supplied by the
// caller: the transaction compacts exactly the current log, and returns the log coordinate the
// caller must durably adopt before it asks the machine for replacement state.
//
// The incarnation is mandatory. A destructive request from a client resumed against another
// relay store must never erase the replacement store merely because its numeric high-water has
// caught up. Appends serialize with the store transaction, so an append either precedes the
// discard and is included in through_cursor, or follows it with a strictly later cursor.
func (sc *serverConn) handleMailboxDiscard(payload []byte) error {
	if !sc.meterOp() {
		return sc.replyErr(codeQuotaExceeded)
	}
	if code, ok := sc.requireAuth(); !ok {
		return sc.replyErr(code)
	}
	var req struct {
		Incarnation string `json:"mailbox_incarnation"`
		Peer        string `json:"peer"`
	}
	if err := json.Unmarshal(payload, &req); err != nil || !ValidMailboxIncarnation(req.Incarnation) || req.Peer == "" {
		return sc.replyErr(codeBadRequest)
	}
	// Authentication proves only the caller's identity. The caller may name only its
	// paired machine, and may not use its own session as the compatibility witness.
	if req.Peer == sc.rid || !sc.s.st.mayActOn(sc.rid, req.Peer) {
		return sc.replyErr(codeNotAuthorized)
	}
	// Hold Server.mu across the rare store transaction so removeConn/newest-wins cannot
	// make the checked gateway stale between the capability verdict and deletion. The
	// mailbox store does not acquire Server.mu, so this lock order cannot recurse.
	sc.s.mu.Lock()
	peer := sc.s.sessions[req.Peer]
	if peer == nil || peer.superseded.Load() || !peer.negotiatedCaps[CapabilityMailboxRecovery] {
		sc.s.mu.Unlock()
		return sc.replyErr(codePeerCapabilityUnavailable)
	}
	through, incarnation, err := sc.s.st.discardItemsForIncarnation(sc.rid, req.Incarnation)
	sc.s.mu.Unlock()
	if errors.Is(err, ErrMailboxCursorResetRequired) {
		return sc.replyErr(codeMailboxCursorReset)
	}
	if err != nil {
		return sc.replyErr(codeBadRequest)
	}
	return sc.replyOK(map[string]any{
		"through_cursor":      through,
		"mailbox_incarnation": incarnation,
	})
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
	// THE TOKEN IS BOUNDED HERE, WHICH IS THE ONLY PLACE IT CAN BE (round-4 threat review
	// C1). Everything downstream carries the label verbatim by design — the store writes it
	// as a bbolt value, loadTokens reads every one of them back at construction, and the
	// sink hands it to the provider untouched — so no later hop is the authority. See
	// maxPushTokenLen for why the bound is on LENGTH, why it is set where it is, and what
	// it deliberately does not bound.
	if len(req.Token) > maxPushTokenLen {
		return sc.replyErr(codeBadRequest)
	}
	// Durable BEFORE the cache (PB-PUSH-6): a persist failure must leave the relay
	// reporting the failure rather than holding a token that vanishes on restart.
	if err := sc.s.st.putToken(sc.rid, req.Token); err != nil {
		if isStorageAdmissionError(err) {
			return sc.replyErr(codeQuotaExceeded)
		}
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
	// THE CALLER MUST HAVE AUTHORITY OVER THE ROUTE IT IS ASKING ABOUT (round-4 threat
	// review H1, first recorded in docs/verification/remote-phase1-relay-review.md).
	//
	// requireAuth above proves an identity and nothing more — relay auth is OPEN
	// REGISTRATION — so this handler answered for ANY routing id anyone cared to name.
	// Measured: an identity minted seconds earlier, paired with nobody, read "online" for a
	// machine it has no edge to. Every other verb that touches somebody else's route goes
	// through a store predicate — mailbox_append and push_trigger through mayActOn,
	// device_revoke through isPairer — and presence went through nothing.
	//
	// IT IS THE SAME PREDICATE THE APPEND AND THE PUSH TAKE, deliberately, rather than a
	// third rule: "may I act on this route" is the relay's one authority decision
	// (store.mayActOn), and a liveness read of somebody's route is an act on it. It also
	// costs the production caller nothing — mobile/app.go's Presence is the phone asking
	// about its own PINNED machine, and authorizePair wrote that grant in both directions,
	// so the paired phone satisfies mayActOn against its machine (checked, not assumed:
	// it is the only production caller in the tree).
	//
	// A CALLER MAY ALWAYS ASK ABOUT ITSELF. That is nobody else's route, it is a fact the
	// caller already holds by being connected, and B61's refusal of self-consent means a
	// party can no longer manufacture the pairs[X\x00X] edge that would otherwise make
	// mayActOn answer it.
	//
	// THE REFUSAL IS "unknown" RATHER THAN not_authorized because the oracle is TWO
	// questions, not one: an unauthorized caller also learned whether a routing id exists
	// at all (a never-seen id answered "unknown", a live one "online"). Answering unknown
	// makes the two indistinguishable, and it is a state the client already handles.
	if req.Target != sc.rid && !sc.s.st.mayActOn(sc.rid, req.Target) {
		return sc.replyOK(PresenceInfo{State: PresenceUnknown})
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
	// Same authority decision as an append, and for the same reason: a push is an
	// unsolicited wake of somebody else's handset (store.mayActOn).
	if !sc.s.st.mayActOn(sc.rid, req.Target) {
		return sc.replyErr(codeNotAuthorized)
	}
	// PB-PUSH-3's SCHEMA, applied by the only party that can apply it. This handler used to
	// copy the caller's `envelope` field onto the channel unexamined, so the number of bytes
	// the provider counts was chosen by whoever called push_trigger rather than by the wake
	// format — and "size is constant" is a promise about the CHANNEL, which is the relay's to
	// keep. See PushEnvelopeSize for why this is a refusal and not a pad.
	//
	// REFUSED BEFORE THE QUOTA, deliberately: a push that never reaches the channel must not
	// spend the TARGET's wake budget either. The target is the victim of a malformed trigger,
	// not its beneficiary, and charging it would hand a paired-but-hostile machine a way to
	// silence a handset's real wakes with traffic the relay was always going to drop.
	if len(req.Envelope) != PushEnvelopeSize {
		return sc.replyErr(codeBadRequest)
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
	// THE CALLER MUST BE THE PAIRER, not merely a party mayActOn admits, AND THAT IS
	// ADR-007 B60(4). This is the gravest of the verbs behind an authority decision — it
	// bans the target for this pair and destroys the frames the caller queued for it — and
	// it used to take the same rule as an append (store.mayActOn), which reads the TARGET's
	// grant over its own route. authorizePair writes BOTH directed edges from one ceremony,
	// so a legitimately paired phone satisfies that rule against its own machine, and the
	// phone's revoke was admitted.
	//
	// Admitting it was the defect, because the revoke then landed in the wrong orientation
	// and could not be made durable. pairKey does not sort, so the live consent sits at
	// consents[machine|phone] while revokeAndPurge(phone, machine) looks for it at
	// consents[phone|machine]: it retired NOTHING and deleted both pairs edges anyway. The
	// revoke reported success, and deliverEpochGrant's next re-presentation of the same
	// stored bytes (cmd/swarm-remote, on EVERY gateway connect) found nothing retired to
	// refuse and wrote the pairing straight back — repeatably, with the phone never asked.
	// A revoke no number of repetitions makes stick is not a revoke.
	//
	// THE STRICTER RULE IS THE ONE PB-STATE-10 SURVIVES, and that was checked rather than
	// assumed, because the note this replaces ruled out a stricter rule for a reason still
	// true of a DIFFERENT one. Requiring full mutual pairing would indeed refuse the owner's
	// own `swarm remote revoke` against a handset that paired and then died before ever
	// connecting — the stranded device PB-STATE-10 exists to recover from. Requiring the
	// caller to be the PAIRER does not: authorize_device precedes the device's first connect
	// by design (the bootstrap append that delivers the ContentKey depends on it, cf.
	// store.mayActOn), so the machine's consent row is already there when the device
	// strands. Measured by TestPBSTATE10_RevokePurgesTheStrandedDeviceRelayState and
	// mobile/conformance's grace-window test.
	//
	// AND IT COSTS NO WRITE, which is why it is taken over retiring the credential in both
	// orientations inside revokeAndPurge. That remedy adds a second retiredKey Put to the
	// very transaction ADR-007 B61 has just shown can abort on an oversized key, and
	// deliverEpochGrant's failure is fatal and not quiescent (cmd/swarm-remote/main.go
	// exits, ShouldRestart is true), so a contested revoke would become a gateway restart
	// loop. Fenced by b60_revokeorientation_test.go, which pins the PROPERTY — a revoke is
	// refused and severs nothing, or it is accepted and stays made — and admits either
	// remedy.
	if !sc.s.st.isPairer(sc.rid, req.Target) {
		return sc.replyErr(codeNotAuthorized)
	}
	forgotToken, err := sc.s.st.revokeAndPurge(sc.rid, req.Target)
	if err != nil {
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
	// It follows the durable decision rather than repeating it: the token survives a
	// revoke that leaves another relationship standing (ADR-007 B49), and a cache cleared
	// where the row was kept would silence a handset the relay can still legitimately
	// wake, until a restart happened to re-hydrate it.
	if forgotToken {
		delete(sc.s.tokens, req.Target)
	}
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
	// The id is retained — as a table key while the slot lives and as a burn-set key after
	// it dies — and this is the only place a NEW one enters, so this is where its length is
	// bounded (round-4 threat review C2). See maxRendezvousIDLen. Claim, send and complete
	// only look ids UP, so they retain nothing an unbounded id could inflate.
	if len(req.ID) > maxRendezvousIDLen {
		return sc.replyErr(codeBadRequest)
	}
	now := sc.s.clk.Now()
	sc.s.mu.Lock()
	sc.s.purgeExpiredRendezvous(now)
	// HI-1: never blindly overwrite. A burned id — one that COMPLETED, or one whose slot
	// aged out, which the purge above has just burned (ADR-007 B47b) — or a live slot is
	// refused so the original creator's in-flight pairing is never orphaned or hijacked.
	if sc.s.isBurned(req.ID, now) {
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
	// C2: take the new slot only after letting go of the one this connection already held,
	// so a connection can never hold two. It runs AFTER every refusal above, so a create
	// that is going to be refused does not cost this connection the slot it has.
	sc.s.leaveRendezvousLocked(sc, now)
	sc.s.rendezvous[req.ID] = &rdvSlot{createdAt: now, creator: sc}
	sc.attachRendezvousLocked(req.ID)
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
	// THE DECISION IS TAKEN UNDER THE LOCK; THE REPLY IS WRITTEN OUTSIDE IT (ADR-007 B82).
	//
	// This handler used to hold s.mu — with `defer sc.s.mu.Unlock()` — across its own
	// `return sc.replyOK(...)`, so writeFrame's SOCKET WRITE ran under the one lock every op
	// on every connection contends for (meterOp takes it at the top of each one, before the
	// op's own auth). A claimer that stops draining its socket therefore froze the entire
	// relay until writeFrame's 10-second ceiling expired — from an unauthenticated
	// connection, because rendezvous_claim carries no requireAuth. Measured end to end by
	// TestB82_AStalledClaimReplyDoesNotFreezeEveryOtherConnection: one held write, and an
	// unrelated connection's rendezvous_create timed out.
	//
	// Create, send, recv and complete all unlock before they reply; claim was the one that
	// did not, and the shape here — decide inside, answer outside — is what makes that
	// uniform. The deferred unlock is KEPT, scoped to the closure, so no early return can
	// leak the lock; what moves out is only the write. "" means admitted.
	code := func() string {
		sc.s.mu.Lock()
		defer sc.s.mu.Unlock()
		if sc.s.isBurned(req.ID, now) {
			return codeRendezvousUsed
		}
		slot, ok := sc.s.rendezvous[req.ID]
		if !ok {
			return codeRendezvousTTL
		}
		if now.Sub(slot.createdAt) >= sc.s.cfg.RendezvousTTL {
			// The claimer is told the truth it needs — the slot expired — and the id is spent
			// on the way out. This is the SECOND site that drops a slot for age, and a fix
			// applied only to purgeExpiredRendezvous would leave the very path the victim
			// phone walks handing the label back to a stranger (ADR-007 B47b).
			sc.s.burnRendezvous(req.ID, now)
			return codeRendezvousTTL
		}
		if slot.creator != nil && slot.claimer != nil {
			return codeRendezvousFull
		}
		// A participant re-claiming its own rendezvous changes nothing: taking the free seat
		// would put ONE connection in both of them, and re-making the inbox would drop
		// whatever the peer has already sent (round-4 threat review C2). It answers OK, the
		// same bytes a fresh claim answers, which is why the two share the reply below.
		if slot.creator == sc || slot.claimer == sc {
			return ""
		}
		// C2: as in create — one rendezvous per connection, and the previous one is released
		// (and burned if it is left empty) rather than silently orphaned.
		sc.s.leaveRendezvousLocked(sc, now)
		slot.claimer = sc
		sc.attachRendezvousLocked(req.ID)
		return ""
	}()
	if code != "" {
		return sc.replyErr(code)
	}
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

// handleRendezvousRecv parks for the next frame from the other participant, BOUNDED by the
// rendezvous the connection joined (round-4 threat review C3).
//
// The park is what needs the bound, not the socket: it happens inside dispatch, so no
// deadline readFrame applies can ever reach it, and the pre-C3 handler had no meterOp and
// no ceiling either. mailbox_wait is the shape this matches — it is metered exactly once
// per call and answers cleanly at MaxServerWait rather than holding a socket until an
// intermediary kills it (wait.go).
//
// The ceiling is the RENDEZVOUS's rather than one of its own, because there is nothing to
// receive past it: the peer that could send is refused at claim, and the slot itself is
// purged on the maintenance tick. And it is charged against rdvDeadline rather than against
// preAuthDeadline so it binds a connection that AUTHENTICATED and then joined a rendezvous
// too — an identity is free to mint, so a bound that only covers unauthenticated callers
// is a bound an attacker steps around by minting one.
func (sc *serverConn) handleRendezvousRecv(_ []byte) error {
	if !sc.meterOp() {
		return sc.replyErr(codeQuotaExceeded)
	}
	sc.s.mu.Lock()
	inbox := sc.rdvInbox
	sc.s.mu.Unlock()
	if inbox == nil {
		return sc.replyErr(codeBadRequest)
	}
	var expired <-chan time.Time
	if !sc.rdvDeadline.IsZero() {
		t := time.NewTimer(time.Until(sc.rdvDeadline))
		defer t.Stop()
		expired = t.C
	}
	select {
	case data := <-inbox:
		return sc.replyOK(map[string]any{"data": data})
	case <-expired:
		// The same verdict the claimer gets past the TTL, and the truth the caller needs:
		// this rendezvous is over, so the pairing failed rather than silently hanging.
		return sc.replyErr(codeRendezvousTTL)
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
	now := sc.s.clk.Now()
	sc.s.mu.Lock()
	slot, ok := sc.s.rendezvous[req.ID]
	// HI-1: only a participant may burn the id, so a third party cannot burn a
	// victim's in-flight pairing.
	if !ok || (slot.creator != sc && slot.claimer != sc) {
		sc.s.mu.Unlock()
		return sc.replyErr(codeNotAuthorized)
	}
	sc.s.burnRendezvous(req.ID, now)
	sc.s.mu.Unlock()
	return sc.replyOK(map[string]any{})
}

// burnRendezvous spends a rendezvous id for burnWindow(). Caller holds s.mu.
//
// EXPIRY BURNS, IT DOES NOT MERELY FREE (ADR-007 B47b). handleRendezvousCreate's own
// comment claims "a burned or live slot is refused so the original creator's in-flight
// pairing is never orphaned or hijacked" — true of Complete and exactly false past TTL,
// because expiry used to delete the slot and leave the label available. The machine's
// QR is still on the owner's screen at that point (internal/skeleton caps its announced
// window at this same TTL, but a screen does not blank itself), so an unauthenticated
// stranger re-created the label, the next phone to scan claimed the STRANGER's slot, and
// the real machine sat orphaned on Recv with no error at all.
func (s *Server) burnRendezvous(id string, now time.Time) {
	delete(s.rendezvous, id)
	s.burned[id] = now.Add(s.burnWindow())
}

// burnWindow is how long a spent rendezvous id stays refused. One RendezvousTTL past the
// burn: the QR that named the id was already dead when the slot expired, so a whole
// further slot-lifetime of refusal is a generous margin, and it is bounded so the burn set
// cannot grow without limit.
//
// WHAT THE BOUND PRESERVES, stated rather than left to be inferred from the number. The
// property is "no rendezvous id is reusable while a QR naming it could still be scanned",
// and the QR's own window is capped at this same TTL (internal/skeleton's pairWindow), so
// a full further TTL of refusal covers it with a whole slot-lifetime to spare. It is NOT
// "a rendezvous id is single-use forever", and it never was: s.burned is in-memory, so a
// relay restart already forgets every burn, including the ones rendezvous_complete wrote.
// That is survivable for the same reason the window is: the live rendezvous table is lost
// in the same instant, so after a restart there is no in-flight pairing left to hijack —
// unlike a route consent, which outlives every connection and is therefore retired in the
// durable store (see store.authorizePair).
func (s *Server) burnWindow() time.Duration { return s.cfg.RendezvousTTL }

// isBurned reports whether id is inside its burn window. Caller holds s.mu.
func (s *Server) isBurned(id string, now time.Time) bool {
	until, ok := s.burned[id]
	return ok && now.Before(until)
}

func (s *Server) purgeExpiredRendezvous(now time.Time) {
	for id, slot := range s.rendezvous {
		if now.Sub(slot.createdAt) >= s.cfg.RendezvousTTL {
			s.burnRendezvous(id, now)
		}
	}
	// Collect burns whose window has closed, so the set stays bounded by the burn
	// window rather than by the process lifetime.
	for id, until := range s.burned {
		if !now.Before(until) {
			delete(s.burned, id)
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
			tok := s.tokens[peer]
			if tok == "" {
				continue
			}
			// CHARGED AGAINST THE TARGET'S PUSH WINDOW, exactly as push_trigger is
			// (round-4 threat review H2). This path used to call deliverPush directly while
			// pushRate guarded only the handler, so the sweep's wakes were charged against
			// nothing: measured at 12 delivered pushes from 12 presence flaps with
			// PushPerMin=1.
			//
			// That matters because THE RELAY DECIDES when a machine's socket has dropped,
			// and the relay is the declared adversary (ADR-007 D9). An unrated sweep is
			// therefore a lever it can pull at will to drive high-priority wakes at the
			// owner's handset — battery, notification churn, and the owner's own provider
			// quota — while looking like nothing worse than an unreliable network. It is
			// the SAME window as the trigger's on purpose: a device's wake budget is a
			// property of the device, not of which relay code path spent it.
			w := s.pushRate[peer]
			if w == nil {
				w = &rateWindow{}
				s.pushRate[peer] = w
			}
			if !w.allow(now, s.cfg.Quotas.PushPerMin) {
				continue
			}
			targets = append(targets, pushTarget{rid: peer, token: tok})
		}
	}
	s.mu.Unlock()
	for _, t := range targets {
		// SAME SHAPE ON THE WIRE AS A GATEWAY WAKE (PB-PUSH-3). The sweep cannot seal an
		// envelope — see silentWakeCover — so what it equalises is the size, which is the
		// property the provider is conceded.
		s.deliverPush(t.rid, t.token, PushPayload{Alert: GenericPushAlert, Ciphertext: silentWakeCover()})
	}
}

// SweepRendezvous expires aged rendezvous slots and collects burn windows that have
// closed, on the maintenance tick (round-4 threat review C2).
//
// It exists because purgeExpiredRendezvous ran in exactly one place: inside
// handleRendezvousCreate. A table filled by connections that then went quiet was therefore
// reclaimed only when some stranger happened to call create — which is precisely the call
// a full table refuses, so nothing reclaimed it at all and no phone on that relay could
// pair. Every other retention rule the relay has (presence, mailbox) is already on this
// tick; this one was the exception.
func (s *Server) SweepRendezvous(_ context.Context) {
	now := s.clk.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeExpiredRendezvous(now)
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
