package relay

// The R2 `swarm relay doctor` diagnostic capability (playbook 4.1/6.5,
// hggx.3.2). The public protocol gains NO privileged unauthenticated
// endpoint: a capability is a bearer credential HMAC-bound to the relay
// operator secret (operatorsecret.go's WithOperatorSecret) that only whoever
// holds that secret can ever produce, and presenting one is what unlocks a
// new SCOPED op family on the ordinary AUTHENTICATED connection surface
// (auth_init/auth_resp, exactly like every other op) -- never a new dial
// path. That op family can only create/use/delete the caller's OWN ephemeral
// diagnostic route: there is no target/routing-id parameter anywhere in its
// wire shape, and the route lives in per-connection memory (serverConn),
// entirely separate from the real mailbox store and from
// bucketPairs/bucketConsents, so it can never read a real mailbox or
// enumerate a routing id.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"time"
)

// DiagnosticCapabilityTTL is the hard ceiling on a minted diagnostic
// capability's lifetime (playbook 6.5: "capability TTL <= 5 min,
// single-use"). It is enforced server-side against the relay's OWN clock; a
// capability carries no TTL of its own to claim a longer one.
//
// "SINGLE-USE" IS ENFORCED IN PROCESS MEMORY ONLY (spendDiagNonce's
// diagUsedNonces, server.go), not the durable store — the same shape and the
// same reasoning as rendezvous burns (see burnWindow's comment). A relay
// restart forgets every spent nonce, so a capability spent just before a
// restart and still inside its TTL could, in principle, be replayed just
// after one. The blast radius stays tiny even so: replaying it buys nothing
// but a fresh, empty, per-connection diagnostic route bounded by
// maxDiagItems and mailboxPageByteBudget — never real mailbox content, never
// a routing id.
const DiagnosticCapabilityTTL = 5 * time.Minute

// diagClockSkew tolerates a capability whose issued_at reads a few seconds
// ahead of the relay's own clock: MintDiagnosticCapability and the relay are
// two different processes reading two different (NTP-synced, not identical)
// clocks in the ordinary deployment this exists for.
const diagClockSkew = 5 * time.Second

// maxDiagItems bounds one ephemeral diagnostic route's live item count. The
// route exists for a handful of round-trip bytes (playbook 4.1's doctor),
// never as unbounded storage riding an operator capability.
const maxDiagItems = 32

const (
	diagCapVersion  = 2
	diagCapNonceLen = 16
	// diagCapRIDLen is len(RoutingID(...)): a fixed 32 hex characters (16 raw
	// bytes), so -- like nonce -- it needs no length prefix on the wire.
	diagCapRIDLen = 32
	// diagCapLen is version(1) + nonce + issued_at_unix_ms(8) + bound routing
	// id + hmac-sha256(32). R2 review LOW (design): version bumped from 1
	// because the wire shape grew a field (the bound routing id, see
	// MintDiagnosticCapability) v1 tokens never carried.
	diagCapLen = 1 + diagCapNonceLen + 8 + diagCapRIDLen + sha256.Size
)

// diagCapContext domain-separates the diagnostic-capability MAC from every
// other HMAC/signature this package computes, the same discipline
// routing.go's authContext/consentContext follow.
var diagCapContext = []byte("swarm-relay-diagnostic-cap-v1\x00")

// errDiagCapBadRID is returned by MintDiagnosticCapability when rid is not a
// RoutingID-shaped string, so a caller notices a plumbing mistake locally
// rather than minting a capability nothing valid can ever present.
var errDiagCapBadRID = errors.New("relay: MintDiagnosticCapability: rid must be a RoutingID")

// ErrDiagnosticsDisabled is returned when the relay has no operator secret
// configured (or MintDiagnosticCapability is called with none): there is no
// default secret, so diagnostics are refused outright rather than minting a
// capability nothing can ever verify.
var ErrDiagnosticsDisabled = errors.New("relay: diagnostics disabled; no operator secret configured")

// ErrDiagnosticCapabilityInvalid covers every way a presented capability
// fails verification ONCE AN OPERATOR SECRET IS CONFIGURED: wrong shape,
// wrong MAC (wrong secret or tampered bytes), wrong identity binding,
// expired, future-dated past clock skew, or already spent. It is
// DELIBERATELY ONE ERROR: telling a caller who already knows a secret is
// configured WHICH of those checks failed would let it narrow a search for
// one that verifies.
//
// R2 review LOW (info leak): this does NOT cover the ONE case handled before
// any of the above ever runs -- no operator secret configured at all
// (ErrDiagnosticsDisabled, checked first in handleDiagOpen) -- so an
// authenticated stranger CAN still distinguish "diagnostics disabled" from
// "diagnostics configured, capability rejected." That single fact is
// accepted, not closed: it is also the one fact that makes `swarm relay
// doctor`'s own failure message actionable (docs/operations/relay-runbook.md
// section 12) rather than as uninformative as everything else this error
// deliberately hides.
var ErrDiagnosticCapabilityInvalid = errors.New("relay: invalid diagnostic capability")

// ErrDiagRouteNotOpen is returned by diag_append/diag_read/diag_close when
// this connection has no live diagnostic route -- diag_open was never called,
// failed, or a prior diag_close already ended it. It carries no information
// about any OTHER connection's route: there is nothing to enumerate.
var ErrDiagRouteNotOpen = errors.New("relay: no diagnostic route is open on this connection")

func init() {
	codeToErr[codeDiagnosticsDisabled] = ErrDiagnosticsDisabled
	codeToErr[codeDiagInvalid] = ErrDiagnosticCapabilityInvalid
	codeToErr[codeDiagRouteNotOpen] = ErrDiagRouteNotOpen
}

const (
	codeDiagnosticsDisabled = "diagnostics_disabled"
	codeDiagInvalid         = "diag_capability_invalid"
	codeDiagRouteNotOpen    = "diag_route_not_open"
)

// MintDiagnosticCapability produces a fresh, single-use diagnostic capability
// bound to secret (the relay operator secret, e.g. EnsureOperatorSecret's
// file contents), good for DiagnosticCapabilityTTL from now, and presentable
// ONLY by rid -- the RoutingID of the relay-auth keypair the caller is about
// to authenticate with. It runs the SAME computation the relay verifies
// against (parseDiagCap), so `swarm relay doctor` -- given the same
// operator_secret_file the relay reads -- mints one with no network round
// trip and no privileged endpoint: presenting the result is the ONLY thing
// the relay's authenticated surface ever checks.
//
// R2 review LOW (design): rid closes an unaudienced-bearer-token gap. Without
// it, the MAC covered only (context, nonce, issued_at) -- ANY endpoint the
// capability was ever shown to (a typo'd URL, a hijacked DNS record) could
// replay the identical bytes against the REAL relay inside the TTL, under a
// throwaway identity of its own choosing. Binding to a SPECIFIC identity's
// routing id means replaying it still needs that identity's private
// relay-auth key to authenticate as -- exactly what an endpoint that only
// ever OBSERVED the capability never has.
func MintDiagnosticCapability(secret []byte, now time.Time, rid string) ([]byte, error) {
	if len(secret) == 0 {
		return nil, ErrDiagnosticsDisabled
	}
	if len(rid) != diagCapRIDLen {
		return nil, errDiagCapBadRID
	}
	nonce := make([]byte, diagCapNonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return signDiagCap(secret, nonce, now, rid), nil
}

// signDiagCap computes the wire bytes for one (nonce, issuedAt, rid) triple
// under secret. It is the one place both minting and verification build the
// MAC, so the two can never drift apart.
func signDiagCap(secret, nonce []byte, issuedAt time.Time, rid string) []byte {
	ts := uint64(issuedAt.UnixMilli())
	mac := hmac.New(sha256.New, secret)
	mac.Write(diagCapContext)
	mac.Write(nonce)
	var tsBuf [8]byte
	binary.BigEndian.PutUint64(tsBuf[:], ts)
	mac.Write(tsBuf[:])
	mac.Write([]byte(rid))
	sum := mac.Sum(nil)

	out := make([]byte, 0, diagCapLen)
	out = append(out, diagCapVersion)
	out = append(out, nonce...)
	out = append(out, tsBuf[:]...)
	out = append(out, rid...)
	out = append(out, sum...)
	return out
}

// parseDiagCap validates token's shape and MAC against secret and returns its
// nonce, issued-at instant, and the routing id it is bound to. It does NOT
// check expiry, single-use, or that the bound rid matches the presenting
// connection -- those need the relay's clock and mu-guarded state, which
// handleDiagOpen adds.
func parseDiagCap(secret, token []byte) (nonce string, issuedAt time.Time, rid string, err error) {
	if len(secret) == 0 {
		return "", time.Time{}, "", ErrDiagnosticsDisabled
	}
	if len(token) != diagCapLen || token[0] != diagCapVersion {
		return "", time.Time{}, "", ErrDiagnosticCapabilityInvalid
	}
	n := token[1 : 1+diagCapNonceLen]
	tsOff := 1 + diagCapNonceLen
	ts := binary.BigEndian.Uint64(token[tsOff : tsOff+8])
	ridOff := tsOff + 8
	r := string(token[ridOff : ridOff+diagCapRIDLen])
	want := signDiagCap(secret, n, time.UnixMilli(int64(ts)), r)
	if !hmac.Equal(token, want) {
		return "", time.Time{}, "", ErrDiagnosticCapabilityInvalid
	}
	return string(n), time.UnixMilli(int64(ts)), r, nil
}

// diagCapExpired reports whether a capability issued at issuedAt is outside
// its TTL window as measured against now, allowing diagClockSkew of
// future-dating for cross-process clock drift.
func diagCapExpired(issuedAt, now time.Time) bool {
	if issuedAt.After(now.Add(diagClockSkew)) {
		return true
	}
	return now.Sub(issuedAt) > DiagnosticCapabilityTTL
}

// spendDiagNonce atomically checks-and-marks nonce spent, returning false if
// it was already spent (still inside its own recorded window). It also purges
// every entry whose window has closed, so the map stays bounded by
// DiagnosticCapabilityTTL rather than by process lifetime -- the same burn-
// window shape burnRendezvous/isBurned already establish in this package.
func (s *Server) spendDiagNonce(nonce string, expiresAt, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for n, exp := range s.diagUsedNonces {
		if !now.Before(exp) {
			delete(s.diagUsedNonces, n)
		}
	}
	if until, ok := s.diagUsedNonces[nonce]; ok && now.Before(until) {
		return false
	}
	s.diagUsedNonces[nonce] = expiresAt
	return true
}

// --- server-side op handlers -------------------------------------------------

// handleDiagOpen verifies a presented capability and, on success, opens a
// fresh (empty) diagnostic route bound to THIS connection. It requires the
// SAME authentication every other op does (requireAuth) -- diag_open is a new
// SCOPED op on the existing authenticated surface, never a new unauthenticated
// endpoint.
func (sc *serverConn) handleDiagOpen(payload []byte) error {
	if !sc.meterOp() {
		return sc.replyErr(codeQuotaExceeded)
	}
	if code, ok := sc.requireAuth(); !ok {
		return sc.replyErr(code)
	}
	var req struct {
		Capability []byte `json:"capability"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return sc.replyErr(codeBadRequest)
	}
	if len(sc.s.operatorSecret) == 0 {
		return sc.replyErr(codeDiagnosticsDisabled)
	}
	nonce, issuedAt, boundRID, err := parseDiagCap(sc.s.operatorSecret, req.Capability)
	if err != nil {
		return sc.replyErr(codeDiagInvalid)
	}
	now := sc.s.clk.Now()
	if diagCapExpired(issuedAt, now) {
		return sc.replyErr(codeDiagInvalid)
	}
	// R2 review LOW (design): a capability minted for one identity is refused
	// for every other -- see MintDiagnosticCapability's rid parameter. Checked
	// BEFORE spendDiagNonce so a capability replayed by the WRONG identity
	// never burns the rightful holder's single use.
	if boundRID != sc.rid {
		return sc.replyErr(codeDiagInvalid)
	}
	if !sc.s.spendDiagNonce(nonce, issuedAt.Add(DiagnosticCapabilityTTL), now) {
		return sc.replyErr(codeDiagInvalid) // already spent
	}
	sc.diagOpen = true
	sc.diagItems = nil
	sc.diagCursor = 0
	sc.diagItemsBytes = 0
	// R2 review LOW-MEDIUM: bound to issuedAt, not now -- a capability opened
	// near the end of its own TTL window must not get a fresh TTL's worth of
	// route lifetime just because diag_open happened to run late.
	sc.diagExpiresAt = issuedAt.Add(DiagnosticCapabilityTTL)
	return sc.replyOK(map[string]any{})
}

// diagRouteLive reports whether this connection currently holds a diagnostic
// route whose capability has not yet crossed its TTL window. R2 review
// LOW-MEDIUM: DiagnosticCapabilityTTL previously bounded only diag_open --
// once sc.diagOpen was set, the route it unlocked lived for the rest of the
// connection, with no expiry anywhere in diag_append/diag_read/diag_status. A
// route whose TTL has passed is now treated exactly like one that was never
// opened: refused, AND its per-connection state released immediately rather
// than lingering as an unbounded-duration ~1 MiB scratch buffer.
func (sc *serverConn) diagRouteLive() bool {
	if !sc.diagOpen {
		return false
	}
	if sc.s.clk.Now().Before(sc.diagExpiresAt) {
		return true
	}
	sc.diagOpen = false
	sc.diagItems = nil
	sc.diagCursor = 0
	sc.diagItemsBytes = 0
	sc.diagExpiresAt = time.Time{}
	return false
}

// handleDiagAppend stores an opaque envelope in this connection's OWN
// diagnostic route. It takes no target: there is nothing for it to name but
// the route diag_open just unlocked.
func (sc *serverConn) handleDiagAppend(payload []byte) error {
	if !sc.meterOp() {
		return sc.replyErr(codeQuotaExceeded)
	}
	if code, ok := sc.requireAuth(); !ok {
		return sc.replyErr(code)
	}
	if !sc.diagRouteLive() {
		return sc.replyErr(codeDiagRouteNotOpen)
	}
	var req struct {
		Envelope []byte `json:"envelope"`
	}
	if err := json.Unmarshal(payload, &req); err != nil {
		return sc.replyErr(codeBadRequest)
	}
	if len(sc.diagItems) >= maxDiagItems {
		return sc.replyErr(codeQuotaExceeded)
	}
	// R2 review MEDIUM: mirror server.go:1123's mailbox guard. handleDiagRead
	// below has no pagination, so the connection's TOTAL retained bytes -- not
	// just this one envelope -- must stay under mailboxPageByteBudget, or a
	// later diag_read would tear the connection exactly as CR-4 exists to
	// prevent on the mailbox path. The cost estimate is the same one
	// store.readItemsPage uses (base64 envelope length + JSON overhead).
	cost := base64.StdEncoding.EncodedLen(len(req.Envelope)) + mailboxItemJSONOverhead
	if sc.diagItemsBytes+cost > mailboxPageByteBudget {
		return sc.replyErr(codeQuotaExceeded)
	}
	sc.diagCursor++
	sc.diagItems = append(sc.diagItems, Item{
		Cursor:   sc.diagCursor,
		Envelope: append([]byte(nil), req.Envelope...),
	})
	sc.diagItemsBytes += cost
	return sc.replyOK(map[string]any{"cursor": sc.diagCursor})
}

// handleDiagRead returns every item appended to this connection's OWN
// diagnostic route. It reads NOTHING from the real per-routing-id mailbox
// store: diagItems is a field on serverConn, never a store lookup keyed by
// sc.rid.
func (sc *serverConn) handleDiagRead(payload []byte) error {
	if !sc.meterOp() {
		return sc.replyErr(codeQuotaExceeded)
	}
	if code, ok := sc.requireAuth(); !ok {
		return sc.replyErr(code)
	}
	if !sc.diagRouteLive() {
		return sc.replyErr(codeDiagRouteNotOpen)
	}
	items := sc.diagItems
	if items == nil {
		items = []Item{}
	}
	return sc.replyOK(map[string]any{"items": items})
}

// handleDiagStatus reports the relay's OWN storage health -- the same
// store-writable and free-disk checks /readyz reports (checkStorage,
// health.go) -- to a caller that already holds a LIVE (unexpired) diagnostic
// route. It is gated on diagRouteLive exactly like diag_append/diag_read/
// diag_close: a caller that never presented a valid capability, or whose
// capability's TTL has since passed (R2 review LOW-MEDIUM), learns nothing
// (R2 review MEDIUM, playbook 6.5's doctor "storage" result). It never reads
// a real mailbox and returns no routing id -- only relay-wide operational
// health an operator with admin_listen access could already see via
// /readyz.
func (sc *serverConn) handleDiagStatus(payload []byte) error {
	if !sc.meterOp() {
		return sc.replyErr(codeQuotaExceeded)
	}
	if code, ok := sc.requireAuth(); !ok {
		return sc.replyErr(code)
	}
	if !sc.diagRouteLive() {
		return sc.replyErr(codeDiagRouteNotOpen)
	}
	h := sc.s.checkStorage()
	return sc.replyOK(map[string]any{
		"store_ok":            h.storeOK,
		"store_error":         h.storeErr,
		"disk_check_enabled":  h.diskCheckEnabled,
		"disk_ok":             h.diskOK,
		"disk_error":          h.diskErr,
		"disk_free_bytes":     h.diskFreeBytes,
		"disk_free_min_bytes": h.diskFreeMinBytes,
	})
}

// handleDiagClose deletes this connection's diagnostic route. A closed route
// stays closed: diag_append/diag_read/diag_close after this all refuse with
// ErrDiagRouteNotOpen until a fresh diag_open unlocks a new one.
func (sc *serverConn) handleDiagClose(payload []byte) error {
	if !sc.meterOp() {
		return sc.replyErr(codeQuotaExceeded)
	}
	if code, ok := sc.requireAuth(); !ok {
		return sc.replyErr(code)
	}
	if !sc.diagRouteLive() {
		return sc.replyErr(codeDiagRouteNotOpen)
	}
	sc.diagOpen = false
	sc.diagItems = nil
	sc.diagCursor = 0
	sc.diagItemsBytes = 0
	sc.diagExpiresAt = time.Time{}
	return sc.replyOK(map[string]any{})
}

// --- client-side -------------------------------------------------------------

// DiagOpen presents a diagnostic capability (MintDiagnosticCapability) and, if
// it verifies, opens a fresh ephemeral diagnostic route bound to this
// connection. A malformed, expired, wrong-secret, or already-spent capability
// is ErrDiagnosticCapabilityInvalid; no operator secret configured on the
// relay at all is ErrDiagnosticsDisabled.
func (c *Client) DiagOpen(ctx context.Context, capability []byte) error {
	_, err := c.conn.control(ctx, "diag_open", map[string]any{"capability": capability})
	return err
}

// DiagAppend stores one opaque envelope in this connection's diagnostic
// route. ErrDiagRouteNotOpen if DiagOpen has not (yet, or successfully)
// unlocked one.
func (c *Client) DiagAppend(ctx context.Context, envelope []byte) error {
	_, err := c.conn.control(ctx, "diag_append", map[string]any{"envelope": envelope})
	return err
}

// DiagStatus is the storage-health snapshot the diag_status op returns (R2
// review MEDIUM, playbook 6.5's doctor "storage" result): whether the
// relay's persistence store is writable, and whether free disk is above the
// configured disk_free_min_bytes alarm. It is the SAME check /readyz reports
// on the admin listener -- diag_status exists so `swarm relay doctor`, which
// typically has no admin_listen access, can prove it over the ordinary
// public wss:// connection instead.
type DiagStatus struct {
	StoreOK          bool   `json:"store_ok"`
	StoreError       string `json:"store_error,omitempty"`
	DiskCheckEnabled bool   `json:"disk_check_enabled"`
	DiskOK           bool   `json:"disk_ok"`
	DiskError        string `json:"disk_error,omitempty"`
	DiskFreeBytes    uint64 `json:"disk_free_bytes"`
	DiskFreeMinBytes int64  `json:"disk_free_min_bytes"`
}

// DiagStatus reports the relay's storage health. It requires an
// already-open diagnostic route (DiagOpen), exactly like DiagAppend/DiagRead:
// ErrDiagRouteNotOpen otherwise.
func (c *Client) DiagStatus(ctx context.Context) (DiagStatus, error) {
	resp, err := c.conn.control(ctx, "diag_status", nil)
	if err != nil {
		return DiagStatus{}, err
	}
	var status DiagStatus
	if err := json.Unmarshal(resp, &status); err != nil {
		return DiagStatus{}, err
	}
	return status, nil
}

// DiagRead returns every item appended to this connection's diagnostic
// route -- never any other connection's, and never a real mailbox.
func (c *Client) DiagRead(ctx context.Context) ([]Item, error) {
	resp, err := c.conn.control(ctx, "diag_read", nil)
	if err != nil {
		return nil, err
	}
	var r struct {
		Items []Item `json:"items"`
	}
	if err := json.Unmarshal(resp, &r); err != nil {
		return nil, err
	}
	return r.Items, nil
}

// DiagClose deletes this connection's diagnostic route.
func (c *Client) DiagClose(ctx context.Context) error {
	_, err := c.conn.control(ctx, "diag_close", nil)
	return err
}
