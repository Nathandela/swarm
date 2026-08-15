package pushgw

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// Config configures one gateway instance. DBPath, Sender and Attest are required; every
// other field has a workable default.
type Config struct {
	// DBPath is the single bbolt file's path. The gateway-local at-rest encryption key
	// (PG-RET-5) is generated alongside it at DBPath+".key", 0600, on first boot.
	DBPath string
	// Sender is the FCM leg (spec section 3.5). Wire the real sender with NewFCMSender in
	// production; tests use an in-memory fake.
	Sender WakeSender
	// Attest is Play Integrity's seam (PG-AUTH-11).
	Attest AttestationVerifier
	// Now is the injected clock. Defaults to time.Now.
	Now func() time.Time
	// Quotas is section 9's abuse controls. Zero-valued buckets fall back to the spec's
	// proposed defaults (QuotaConfig.withDefaults).
	Quotas QuotaConfig
	// Logger receives one safe, secret-free line per request (PG-TEST-9 / section 8.2).
	// Defaults to a discarding logger.
	Logger *slog.Logger
	// TrustedProxies is PG-Q-4's rightmost-hop X-Forwarded-For trust list, CIDR strings.
	// Empty (the default) means every request's quota-accounting source is its raw TCP
	// peer address -- the safe default when the gateway is reached directly. Set this only
	// when the deployment sits behind a TLS-terminating reverse proxy that appends the
	// real client to X-Forwarded-For (see cmd/swarm-pushgw's -trusted-proxies flag and its
	// TLS-mode doc comment for the recorded deployment assumption this pairs with).
	TrustedProxies []string
}

// Server is the gateway's HTTP handler. It is safe for concurrent use.
type Server struct {
	store  *store
	sender WakeSender
	attest AttestationVerifier
	now    func() time.Time
	quotas QuotaConfig
	logger *slog.Logger

	limiter        *limiter
	nonces         *nonceCache
	wakeIdem       *wakeIdemCache
	regIdem        *regIdemCache
	trustedProxies []*net.IPNet

	// providerOutcome is PG-TR-6's provider-reachability signal for GET /v1/health: the FCM
	// leg's (WakeSender's) last outcome class, "reachable" or "unreachable" -- unset until a
	// wake has actually been attempted. An atomic.Value rather than a mutex: recorded once
	// per wake, read once per health check, no ordering beyond "most recent write wins".
	providerOutcome atomic.Value // string
}

// NewServer builds a gateway over cfg, opening (or creating) the bbolt file at
// cfg.DBPath and its sibling key file. It fails closed on a missing required dependency
// rather than deferring the failure to the first request.
func NewServer(cfg Config) (*Server, error) {
	if cfg.DBPath == "" {
		return nil, errConfig("DBPath is required")
	}
	if cfg.Sender == nil {
		return nil, errConfig("Sender is required")
	}
	if cfg.Attest == nil {
		return nil, errConfig("Attest is required")
	}
	quotas := cfg.Quotas.withDefaults()
	if err := quotas.validate(); err != nil {
		return nil, err
	}
	trustedProxies, err := parseTrustedProxies(cfg.TrustedProxies)
	if err != nil {
		return nil, err
	}
	st, err := openStore(cfg.DBPath, cfg.DBPath+".key")
	if err != nil {
		return nil, err
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Server{
		store:          st,
		sender:         cfg.Sender,
		attest:         cfg.Attest,
		now:            now,
		quotas:         quotas,
		logger:         logger,
		limiter:        newLimiter(),
		nonces:         newNonceCache(),
		wakeIdem:       newWakeIdemCache(),
		regIdem:        newRegIdemCache(),
		trustedProxies: trustedProxies,
	}, nil
}

// Close releases the underlying bbolt file.
func (s *Server) Close() error { return s.store.close() }

// RunRetention applies section 8.1's three durable retention rows, plus PG-RET-4's
// hardening sweep of the three bounded in-memory caches (quota windows, registration and
// wake idempotency), once, as of the server's current clock reading. Production wiring
// (cmd/swarm-pushgw) calls it on a timer; tests call it directly after advancing a fake
// clock.
func (s *Server) RunRetention(_ context.Context) error {
	now := s.now()
	s.limiter.sweep(now)
	s.regIdem.sweep(now)
	s.wakeIdem.sweep(now)
	return s.store.runRetention(now.UnixMilli())
}

// ServeHTTP dispatches every request. PG-TR-2's version check runs before any operation
// is even identified: a path outside /v1/ is refused version_unsupported regardless of
// method, and GET /v1/health is the one unauthenticated, caller-agnostic exception to
// "every operation lives under /v1/ and is one of the five" (PG-TR-6 / addition A2).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	status := http.StatusNotFound

	switch {
	case r.Method == http.MethodGet && path == "/v1/health":
		status = s.handleHealth(w, r)
	case !strings.HasPrefix(path, "/v1/"):
		s.writeErr(w, errVersionUnsupported)
	case r.Method == http.MethodPost && path == "/v1/installations":
		status = s.handleRegister(w, r)
	case r.Method == http.MethodPost && path == "/v1/wakes":
		status = s.handleWake(w, r)
	case r.Method == http.MethodPut && strings.HasPrefix(path, "/v1/installations/") && strings.HasSuffix(path, "/token"):
		status = s.handleRotate(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/v1/installations/") && strings.HasSuffix(path, "/addresses"):
		status = s.handleAllocate(w, r)
	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/v1/addresses/"):
		status = s.handleRevoke(w, r)
	default:
		// PG-TR-2 scopes version_unsupported to "an unknown version prefix" -- this branch
		// is reached with a KNOWN version (the path passed the /v1/ prefix check above) that
		// simply names no route or method any of the five operations owns. Answering
		// version_unsupported here would send a submitter that used the wrong method or a
		// nonexistent path to the wrong repair (section 6.4's terminal row for that code
		// reads "the repair is a submitter that speaks a served version"). A BARE 404
		// (net/http's http.NotFound) carried NO code from section 4's closed vocabulary at
		// all: PG-ERR-3 treats a bodyless response as a transport failure under PG-ERR-1's
		// status-only fallback, so a submitter that hit the wrong method or path retried
		// this to expiry -- exactly the quota-burning outcome section 6.4's `abandoned`
		// state exists to prevent. malformed_request (400) is the closed-vocabulary code
		// that actually matches what happened: the request named no operation this version
		// serves.
		s.writeErr(w, errMalformedRequest)
		status = errMalformedRequest.status
	}

	// A safe, secret-free line: method, path (installation ids and push addresses are
	// opaque routing handles, not secrets -- section 7.1) and the outcome status. Never a
	// header, a body, or anything section 8.2 forbids (PG-TEST-9).
	s.logger.Info("pushgw request", "method", r.Method, "path", path, "status", status)
}

// healthResponse is GET /v1/health's body (PG-TR-6). Provider is the FCM leg's
// provider-reachability signal ("service and provider-reachability state"): omitted until a
// wake has actually been attempted (nothing is known yet), "reachable" or "unreachable"
// after, from recordProviderOutcome's own classification of the WakeSender seam's last
// outcome -- never a synthetic probe of its own.
type healthResponse struct {
	Status   string `json:"status"`
	Provider string `json:"provider,omitempty"`
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) int {
	body := healthResponse{Status: "ok"}
	if v, ok := s.providerOutcome.Load().(string); ok {
		body.Provider = v
	}
	b, _ := json.Marshal(body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
	return http.StatusOK
}

// recordProviderOutcome updates PG-TR-6's provider-reachability signal from the FCM leg's
// last outcome (fcmsender.go's WakeSender seam classifies a transport failure or provider
// 5xx as ErrUnavailable, distinct from a definitive provider response). Concurrency-safe
// without a lock: an atomic.Value swap, read once per GET /v1/health, always the MOST
// RECENT wake's class -- exactly the signal already available at the point wake.go calls
// the sender, requiring no separate health-probing loop.
func (s *Server) recordProviderOutcome(sendErr error) {
	if errors.Is(sendErr, ErrUnavailable) {
		s.providerOutcome.Store("unreachable")
		return
	}
	s.providerOutcome.Store("reachable")
}

// --- shared request plumbing -----------------------------------------------------------

// sourceIP extracts the quota-accounting identity (PG-Q-4): the validated external source
// address. With no trusted proxies configured (every test in this suite, and the default
// production posture) this is just the raw TCP peer, exactly as before. When
// Config.TrustedProxies names the reverse proxy fronting the gateway, X-Forwarded-For is
// consulted -- but ONLY after confirming the TCP peer itself is that trusted proxy, and
// only its rightmost (self-appended) hop is honoured; see trustedproxy.go.
func (s *Server) sourceIP(r *http.Request) string {
	if len(s.trustedProxies) == 0 {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			return r.RemoteAddr
		}
		return host
	}
	xff := strings.Join(r.Header.Values("X-Forwarded-For"), ",")
	return resolveSourceAddr(r.RemoteAddr, xff, s.trustedProxies)
}

// hasContentEncoding is PG-TR-4: no request may be compressed.
func hasContentEncoding(r *http.Request) bool {
	return r.Header.Get("Content-Encoding") != ""
}

// hasExactContentType is PG-TR-5's content-type gate, checked before authentication.
func hasExactContentType(r *http.Request, want string) bool {
	ct := r.Header.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.TrimSpace(ct) == want
}

// readBounded enforces PG-TR-3's pre-parse body bound for a JSON or empty-bodied
// operation: a declared Content-Length over max is refused without reading the body at
// all, and an undeclared or lying length is still caught by the hard-limited reader.
func readBounded(r *http.Request, maxBytes int64) (body []byte, tooLarge bool, err error) {
	if r.ContentLength > maxBytes {
		return nil, true, nil
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxBytes+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > maxBytes {
		return nil, true, nil
	}
	return data, false, nil
}

// hasAnyBody is PG-TR-3's zero-byte row (DELETE /v1/addresses/{addr}): any body at all,
// regardless of content, is malformed_request.
func hasAnyBody(r *http.Request) (bool, error) {
	if r.ContentLength > 0 {
		return true, nil
	}
	buf := make([]byte, 1)
	n, err := io.ReadFull(r.Body, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return false, err
	}
	return n > 0, nil
}

func errConfig(msg string) error { return &configError{msg: msg} }

type configError struct{ msg string }

func (e *configError) Error() string { return "pushgw: " + e.msg }
