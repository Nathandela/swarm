package shim

// The LIVE hook socket: the second per-session listener playbook §6.1 requires
// ("Claude hooks post to a per-session shim-owned socket"), entirely separate from
// and never touching the PTY/control socket (server.go, listen()) or the emulator/
// transcript pipeline -- ADR-013's sacred rule. It is demuxed on its own leading
// byte, the same style server.go's control socket demuxes shimwire.Control frames
// on, except here there is no wire.ReadFrame envelope: a POST is one self-delimited
// JSON value (the shim never parses it -- HookSpool.Append is body-oblivious), and a
// DRAIN is one self-delimited JSON HookDrainRequest/HookDrainResponse pair.
import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"sync"
	"time"
)

const (
	// HookPostTag leads a POST: the client's JSON body follows, starting with this
	// same byte (the JSON object's opening brace).
	HookPostTag byte = '{'
	// HookDrainTag leads a DRAIN request.
	HookDrainTag byte = 'D'
	// HookAckByte is written back after a POST's Append returns nil -- the ONLY
	// byte ever written on that connection before it closes, so an honest "no ack"
	// is simply an absent or closed read.
	HookAckByte byte = 0x01

	// hookConnTimeout bounds reading a connection's tag and body/request: every
	// real client writes immediately on connect, so this only ever bites a
	// stalled or idle peer.
	hookConnTimeout = 5 * time.Second

	// hookPostMaxBytes bounds a POST's body BEFORE it is even decoded, independent
	// of the spool's own size bound (HookSpool.Append's refusal only runs once the
	// value is already fully buffered in memory). Matches internal/skeleton's own
	// hookBodyLimit (interaction.go) so a body this shim accepts is never one the
	// daemon's live hook path would itself have refused.
	hookPostMaxBytes = 4 << 20

	// hookDrainMaxBytes bounds a DRAIN request BEFORE it is decoded (R6 review
	// fix-pack, MEDIUM SECURITY): unlike POST, servePost's own body has always been
	// wrapped in a LimitReader; serveDrain's had not, so only the 5s connection
	// deadline bounded it -- permitting hundreds of megabytes of allocation inside
	// the shim, the process that owns the PTY (ADR-013's sacred plane). A
	// HookDrainRequest is three small fields; this is generous headroom, not a
	// tight fit.
	hookDrainMaxBytes = 64 << 10

	// hookResidualDiscardMax bounds serveConn's post-handler discard of unread
	// request bytes. Closing a unix socket with unread data in its receive queue
	// resets the peer on Linux, destroying the buffered reply -- or the clean FIN a
	// refusal answers with -- so "refused" would read as ECONNRESET there and as EOF
	// on Darwin. The discard keeps the refusal contract (close with no response
	// bytes = EOF) platform-independent; a writer still flooding past this bound
	// gets the reset it earned.
	hookResidualDiscardMax = 256 << 10
)

// HookDrainRequest is a DRAIN's request body.
type HookDrainRequest struct {
	FromSeq uint64 `json:"from_seq"` // records wanted: Seq>FromSeq
	FoldSeq uint64 `json:"fold_seq"` // 0 = nothing new to fold; else Compact(FoldSeq) first
	// Token, when the shim was configured with one (Config.HookToken), must match it
	// or the drain is refused. Empty on both sides (the compat default -- no token
	// configured) means the check does not run at all, matching HookSocketPath's own
	// "unset means disabled" convention rather than a new failure mode for every
	// caller that predates this field.
	Token string `json:"token,omitempty"`
}

// HookDrainResponse is a DRAIN's reply body.
type HookDrainResponse struct {
	Records     []HookRecord `json:"records"`
	Gap         bool         `json:"gap,omitempty"`
	GapBoundary uint64       `json:"gap_boundary,omitempty"`
}

// hookServer serves cfg.HookSocketPath: a second listener, independent of server's
// PTY/hub, backed by one HookSpool. Connections are served concurrently, mirroring
// server.acceptLoop's tracked-handler shutdown discipline.
type hookServer struct {
	listener net.Listener
	spool    *HookSpool
	// token gates the DRAIN verb, which is both destructive (FoldSeq compacts on the
	// caller's say-so) and read-everything (every spooled body, including the
	// session's own hook POST token).
	//
	// WHERE THIS SITS IN THE THREAT MODEL, honestly (R6 review fix-pack round 1,
	// HIGH 4). The PRIMARY control on this socket is the filesystem: a 0700 session
	// dir and a 0600 socket, i.e. ADR-004's same-user model, exactly as for the
	// control socket beside it. This token is DEFENCE IN DEPTH behind that, and it
	// buys one specific thing the permissions cannot: it keeps the DRAIN verb shut to
	// the AGENT's own process tree, which necessarily holds the POST-side token and
	// could otherwise fold away or read out a spool the daemon has not yet folded --
	// manufacturing a structured_gap degrade on demand. It is not, and is not claimed
	// to be, a defence against a same-user process that can read the 0600 launch
	// config; nothing at this layer is. "" (the compat default: no per-session drain
	// token configured, e.g. any launch config that predates the field) disables the
	// check entirely. POST carries no token of its own by design -- a spooled
	// record's OWN embedded auth is checked downstream at daemon apply time, not at
	// accept time -- so a hook that fires while the daemon is down still lands.
	token string

	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	closing  bool
	handlers sync.WaitGroup
}

func newHookServer(l net.Listener, spool *HookSpool, token string) *hookServer {
	return &hookServer{listener: l, spool: spool, token: token, conns: make(map[net.Conn]struct{})}
}

// newSessionHookServer opens cfg's spool and binds cfg.HookSocketPath, the two
// setup steps Run needs as one failable unit. The caller decides how to react to a
// failure (Run degrades to no hook socket rather than aborting the session).
func newSessionHookServer(cfg Config) (*hookServer, error) {
	spool, err := OpenHookSpool(filepath.Join(cfg.SessionDir, HookSpoolFile), cfg.HookSpoolMaxBytes)
	if err != nil {
		return nil, fmt.Errorf("shim: open hook spool: %w", err)
	}
	l, err := listen(cfg.HookSocketPath)
	if err != nil {
		_ = spool.Close()
		return nil, err
	}
	return newHookServer(l, spool, cfg.HookToken), nil
}

// acceptLoop serves connections until the listener is closed. A connection accepted
// after shutdown began is closed immediately and never served.
func (h *hookServer) acceptLoop() {
	for {
		conn, err := h.listener.Accept()
		if err != nil {
			return
		}
		h.mu.Lock()
		if h.closing {
			h.mu.Unlock()
			_ = conn.Close()
			continue
		}
		h.conns[conn] = struct{}{}
		h.handlers.Add(1)
		h.mu.Unlock()
		go func() {
			defer h.handlers.Done()
			h.serveConn(conn)
		}()
	}
}

func (h *hookServer) serveConn(conn net.Conn) {
	defer func() {
		h.mu.Lock()
		delete(h.conns, conn)
		h.mu.Unlock()
		_ = conn.Close()
	}()
	_ = conn.SetDeadline(time.Now().Add(hookConnTimeout))
	var tag [1]byte
	if _, err := io.ReadFull(conn, tag[:]); err != nil {
		return
	}
	switch tag[0] {
	case HookPostTag:
		h.servePost(conn, tag[0])
	case HookDrainTag:
		h.serveDrain(conn)
	}
	// FIN first so the peer's read sees EOF (or the already-written reply) the
	// moment the handler is done, then a bounded discard of whatever request bytes
	// the handler declined to read, so the deferred Close cannot reset the peer on
	// Linux (see hookResidualDiscardMax). Still under the connection deadline.
	if uc, ok := conn.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(conn, hookResidualDiscardMax))
}

// servePost Appends the posted body to the spool and, ONLY when that succeeds,
// writes back HookAckByte. A refusal (a full spool, a disk error) closes without
// writing it: the client's own bounded read then sees EOF, the single honest
// "not durably accepted" signal for every failure mode.
func (h *hookServer) servePost(conn net.Conn, brace byte) {
	r := io.MultiReader(bytes.NewReader([]byte{brace}), io.LimitReader(conn, hookPostMaxBytes))
	var body json.RawMessage
	if err := json.NewDecoder(r).Decode(&body); err != nil {
		return
	}
	if _, err := h.spool.Append(body); err != nil {
		return
	}
	_, _ = conn.Write([]byte{HookAckByte})
}

// serveDrain answers one drain: compact up to FoldSeq (when >0), then read every
// record past FromSeq up to the first gap and reply. Stateless per connection -- no
// live-follow; a poller reconnects for its next batch.
func (h *hookServer) serveDrain(conn net.Conn) {
	var req HookDrainRequest
	if err := json.NewDecoder(io.LimitReader(conn, hookDrainMaxBytes)).Decode(&req); err != nil {
		return
	}
	if h.token != "" && subtle.ConstantTimeCompare([]byte(req.Token), []byte(h.token)) != 1 {
		return // wrong or missing token: refuse silently, exactly like an unaccepted POST
	}
	if req.FoldSeq > 0 {
		_ = h.spool.Compact(req.FoldSeq) // best-effort: a refused compact leaves the spool untouched
	}
	recs, gapAt, hasGap, err := h.spool.ReadFrom(req.FromSeq)
	if err != nil {
		return
	}
	resp, err := json.Marshal(HookDrainResponse{Records: recs, Gap: hasGap, GapBoundary: gapAt})
	if err != nil {
		return
	}
	_, _ = conn.Write(resp)
}

// shutdown stops accepting, closes every tracked connection, joins every handler,
// then closes the spool. It is safe to call even if acceptLoop was never started.
func (h *hookServer) shutdown() {
	h.mu.Lock()
	h.closing = true
	conns := make([]net.Conn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()

	_ = h.listener.Close()
	for _, c := range conns {
		_ = c.Close()
	}
	h.handlers.Wait()
	_ = h.spool.Close()
}
