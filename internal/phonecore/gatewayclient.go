package phonecore

// Wave R3 scope 1: the phone-side PUSH GATEWAY CLIENT (ADR-015 P5,
// docs/specifications/push-gateway-api.md sections 2 and 3.1-3.4).
//
// It speaks the four installation-side operations -- register, rotate token, allocate
// address, revoke address -- against internal/pushgw's server, which is the contract it is
// tested against in-process (r3a_installation_test.go, r3a_revoke_test.go). It is
// deliberately STDLIB-ONLY (net/http and friends): phonecore's deps allowlist gains no
// line for it.
//
// The two injected seams are the two things Android owns and Go cannot:
//
//   - InstallationSigner: the installation private key. Production is an Android Keystore
//     P-256 key (PG-AUTH-2); the wire contract is IEEE P1363 64-byte r||s with s low,
//     over SHA-256 of the canonical string, and the gateway refuses anything else.
//   - AttestFunc: Play Integrity. The client computes PG-AUTH-11's requestHash and hands
//     it to the attestor, which returns the verdict token the registration body carries.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// InstallationSigner signs installation-control requests with the installation private
// key (PG-AUTH-1/2). PublicKey returns the SEC1 uncompressed P-256 point (65 bytes);
// Sign returns the IEEE P1363 fixed-width 64-byte r||s signature with s normalized low,
// over SHA-256 of the canonical string.
type InstallationSigner interface {
	PublicKey() []byte
	Sign(canonical []byte) ([]byte, error)
}

// AttestFunc produces the Play Integrity verdict token for one registration attempt
// (PG-AUTH-11): requestHash is SHA-256 of the JCS canonicalization of the registration
// body with attestation.token replaced by the empty string, and the returned token must
// be bound to exactly that hash or the gateway's own recomputation refuses it.
type AttestFunc func(requestHash [32]byte) (string, error)

// PushAddress is the 16 opaque, gateway-minted bytes that route a WakeV1 (PG-ALLOC-1) --
// the phone-side twin of remotegw.PushAddress, convertible to it.
type PushAddress [16]byte

// EncodePushAddress renders an address in its wire form: base64url unpadded, 22
// characters (spec section 3.6's PushAddress schema).
func EncodePushAddress(addr PushAddress) string {
	return base64.RawURLEncoding.EncodeToString(addr[:])
}

// PushRegistration is a successful registration's durable result (spec section 3.1).
type PushRegistration struct {
	InstallationID string
	// RefreshBefore is the 180-day inactivity floor; any successfully authenticated
	// installation request moves it (PG-AUTH-5).
	RefreshBefore time.Time
}

// PushAllocation is one allocate-address response (spec section 3.3): the triple is
// returned exactly once and never again, so the caller must convey it into the pairing
// or revoke it (PG-ALLOC-3) -- there is no second read.
type PushAllocation struct {
	Address                 PushAddress
	SubmitCapability        string
	MachineRevokeCapability string
	UnboundExpiresAt        time.Time
}

// ErrAttestationRefused is PG-AUTH-13's typed refusal: the device is not recognised as
// the licensed Play-signed build, registration is refused, and the honest app state is
// "foreground updates only" -- never a half-enrolled durable identity.
var ErrAttestationRefused = errors.New("phonecore: push gateway refused the app attestation; no installation enrolled")

// errGatewayUnauthorized is the gateway's UNDISCRIMINATED 401 (spec section 4, code
// `unauthorized`): an unknown installation, a signature that did not verify, a capability
// the gateway does not hold -- refusals the gateway deliberately does not tell apart,
// because telling them apart is an oracle. It is unexported: the one caller that acts on it
// is EnsurePushRegistration's re-registration fallback.
//
// IT IS NOT "EVERY 401", and the round-4 review proved why the difference is not academic.
// pushgw returns a SECOND 401 -- code `request_expired`, carrying `server_time` -- for a
// signed request whose expiry is outside PG-AUTH-3's 120-second horizon, and it returns the
// server time precisely so the client can correct its clock and retry. Read as this
// sentinel, a phone whose clock is ~60 seconds off falls through to a FRESH registration:
// the register POST carries no signature, so it succeeds while the clock is still wrong, and
// the phone silently swaps its durable identity. The first installation is then orphaned for
// 180 days holding a live FCM token -- the exact orphan PG-REG-2 exists to prevent, reached
// by a path that never touches it -- and every address under it dies at the inactivity
// floor, so live pairings stop waking with EnsurePushRegistration still returning nil.
var errGatewayUnauthorized = errors.New("phonecore: push gateway refused the credential (401 unauthorized)")

// errRequestExpired is the OTHER 401: this phone's clock is outside the gateway's horizon
// (PG-AUTH-3). It is a DISTINCT sentinel and deliberately does not wrap
// errGatewayUnauthorized, so no errors.Is on that value can reach the re-registration
// fallback from here. doSigned has already consumed the body's server_time as a clock offset
// and retried once by the time a caller sees this, so reaching it means the gateway called
// the corrected request expired too -- a refusal to report, never an identity to re-mint.
var errRequestExpired = errors.New("phonecore: push gateway refused the signed request as expired (401 request_expired); this phone's clock is outside the gateway's horizon")

// codeRequestExpired is the wire code above, spelled once.
const codeRequestExpired = "request_expired"

// registerAttempts bounds the lost-response retry loop (PG-REG-2). The retry re-sends
// the byte-identical body under the same Idempotency-Key, so the bound is about giving
// up on a dead network, not about correctness.
const registerAttempts = 3

// registerBackoff spaces the bounded attempts: 250ms then 500ms between the three. A
// loop with no backoff lands every attempt in milliseconds, which on a dead radio is
// spending the whole bound before the network can possibly come back. The waits select
// on ctx.Done so a cancelled caller is not held hostage.
const registerBackoff = 250 * time.Millisecond

// errRegisterOutcomeUnknown marks a register whose every attempt lost the RESPONSE: the
// gateway may or may not have minted the installation. The caller
// (EnsurePushRegistration) keeps the prepared (Idempotency-Key, body) pair durable so
// the NEXT call replays it -- inside pushgw's retention window that replay is answered
// with the installation already minted, which is what stops a lost response from
// orphaning an installation that holds a live FCM token for 180 days (PG-REG-2). A
// DEFINITIVE gateway refusal is never wrapped in this: the outcome is known, the
// prepared pair is dead.
var errRegisterOutcomeUnknown = errors.New("phonecore: register outcome unknown (no response received); the prepared registration must be replayed")

// requestExpiryWindow is how far ahead a signed request's expiry is set: comfortably
// inside PG-AUTH-3's 120-second horizon.
const requestExpiryWindow = 60 * time.Second

// GatewayClient is the phone's client for one push gateway. Safe for concurrent use.
type GatewayClient struct {
	baseURL string
	signer  InstallationSigner
	attest  AttestFunc
	hc      *http.Client

	mu             sync.Mutex
	installationID string
	// clockOffset is what this client adds to time.Now() when it stamps a signed request's
	// expiry: the correction PG-AUTH-3's server_time last told it to apply. It is a CLIENT
	// correction and nothing else -- no durable state, no other path, and never applied to
	// anything the phone decides for itself (the WakeV1 freshness bound reads the real
	// clock). A handset whose clock is minutes off can therefore still authenticate its
	// installation-control requests, which is the difference between one installation and a
	// new one every launch.
	clockOffset time.Duration
}

// NewGatewayClient builds a client over baseURL (scheme://host, no path). hc nil falls
// back to http.DefaultClient.
func NewGatewayClient(baseURL string, signer InstallationSigner, attest AttestFunc, hc *http.Client) *GatewayClient {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &GatewayClient{baseURL: baseURL, signer: signer, attest: attest, hc: hc}
}

// InstallationID returns the id this client registered under, or "" before a successful
// Register.
func (g *GatewayClient) InstallationID() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.installationID
}

// registerBody is spec section 3.1's closed field set (additionalProperties: false).
type registerBody struct {
	InstallationPublicKey string              `json:"installation_public_key"`
	FCMToken              string              `json:"fcm_token"`
	Attestation           registerAttestation `json:"attestation"`
}

type registerAttestation struct {
	Kind  string `json:"kind"`
	Token string `json:"token"`
}

// registrationRequestHash is PG-AUTH-11's carve-out, computed the way the gateway
// recomputes it (pushgw's requestHash): the ACTUAL registration body with
// attestation.token blanked, re-read as a map (so key ordering matches the gateway's
// map-canonicalization), encoded with HTML escaping off, trailing newline trimmed,
// SHA-256 of the result. It deliberately derives from the same registerBody value
// Register marshals onto the wire -- never a hand-maintained duplicate of the field set
// -- so a field added to registerBody flows into both the wire body and the hash, and
// the two derivations cannot drift into a runtime 403 only the real gateway would catch.
func registrationRequestHash(body registerBody) ([32]byte, error) {
	body.Attestation.Token = ""
	raw, err := json.Marshal(body)
	if err != nil {
		return [32]byte{}, fmt.Errorf("phonecore: canonicalize registration body: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return [32]byte{}, fmt.Errorf("phonecore: canonicalize registration body: %w", err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return [32]byte{}, fmt.Errorf("phonecore: canonicalize registration body: %w", err)
	}
	return sha256.Sum256(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

// preparedRegister is one registration attempt's durable identity: the Idempotency-Key
// and the exact bytes it keys. pushgw's idempotency cache is keyed on BOTH (see
// register.go's recorded deviation), so a replay -- within one call or from the NEXT
// call after a lost response -- must present this pair verbatim or it is a cache miss
// that mints a second installation.
type preparedRegister struct {
	IdemKey  string
	Body     []byte
	FCMToken string
}

// prepareRegister builds one registration's (key, body) pair: the attested body and a
// fresh Idempotency-Key. Attestation runs here, once per prepared pair -- the verdict
// token is bound to the body's requestHash, so re-attesting a replay would CHANGE the
// body and defeat the idempotency it exists for.
func (g *GatewayClient) prepareRegister(fcmToken string) (preparedRegister, error) {
	rb := registerBody{
		InstallationPublicKey: base64.RawURLEncoding.EncodeToString(g.signer.PublicKey()),
		FCMToken:              fcmToken,
		Attestation:           registerAttestation{Kind: "play_integrity"},
	}
	hash, err := registrationRequestHash(rb)
	if err != nil {
		return preparedRegister{}, err
	}
	verdictToken, err := g.attest(hash)
	if err != nil {
		return preparedRegister{}, fmt.Errorf("phonecore: app attestation: %w", err)
	}
	rb.Attestation.Token = verdictToken
	body, err := json.Marshal(rb)
	if err != nil {
		return preparedRegister{}, err
	}
	idemKey, err := randomToken22()
	if err != nil {
		return preparedRegister{}, err
	}
	return preparedRegister{IdemKey: idemKey, Body: body, FCMToken: fcmToken}, nil
}

// registerPrepared POSTs one prepared registration, retrying a lost response with the
// SAME key and byte-identical body (PG-REG-2), spaced by registerBackoff. When every
// attempt loses the response the error is errRegisterOutcomeUnknown: the pair may have
// been processed and must be kept for replay, never re-minted.
func (g *GatewayClient) registerPrepared(ctx context.Context, prep preparedRegister) (PushRegistration, error) {
	var lastErr error
	for attempt := 0; attempt < registerAttempts; attempt++ {
		if attempt > 0 {
			// Backoff BETWEEN attempts, cancellable: 250ms then 500ms.
			select {
			case <-time.After(registerBackoff << (attempt - 1)):
			case <-ctx.Done():
				return PushRegistration{}, fmt.Errorf("%w: %w", errRegisterOutcomeUnknown, ctx.Err())
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+"/v1/installations", bytes.NewReader(prep.Body))
		if err != nil {
			return PushRegistration{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", prep.IdemKey)
		resp, err := g.hc.Do(req)
		if err != nil {
			// The request may well have been PROCESSED and only the response lost; the
			// retry is byte-identical under the same key, which is what makes it safe.
			lastErr = err
			continue
		}
		reg, err := g.decodeRegisterResponse(resp)
		if err != nil {
			// A response ARRIVED: the outcome is definitive, the prepared pair is dead.
			return PushRegistration{}, err
		}
		g.mu.Lock()
		g.installationID = reg.InstallationID
		g.mu.Unlock()
		return reg, nil
	}
	return PushRegistration{}, fmt.Errorf("%w: %w", errRegisterOutcomeUnknown, lastErr)
}

// Register mints one installation (POST /v1/installations, spec section 3.1): attested
// admission, no address allocated. A response lost on the way back is retried with the
// SAME Idempotency-Key and the byte-identical body (PG-REG-2), so a flaky handset
// network cannot mint a second durable installation. Callers with durable state use
// EnsurePushRegistration, which additionally keeps the prepared pair for replay ACROSS
// calls; this one-shot form prepares and posts in one motion.
func (g *GatewayClient) Register(ctx context.Context, fcmToken string) (PushRegistration, error) {
	prep, err := g.prepareRegister(fcmToken)
	if err != nil {
		return PushRegistration{}, err
	}
	return g.registerPrepared(ctx, prep)
}

func (g *GatewayClient) decodeRegisterResponse(resp *http.Response) (PushRegistration, error) {
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return PushRegistration{}, err
	}
	if resp.StatusCode != http.StatusCreated {
		return PushRegistration{}, gatewayError(resp.StatusCode, raw)
	}
	var body struct {
		InstallationID string `json:"installation_id"`
		RefreshBefore  string `json:"refresh_before"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return PushRegistration{}, fmt.Errorf("phonecore: register response: %w", err)
	}
	refresh, err := time.Parse(time.RFC3339, body.RefreshBefore)
	if err != nil {
		return PushRegistration{}, fmt.Errorf("phonecore: register refresh_before: %w", err)
	}
	return PushRegistration{InstallationID: body.InstallationID, RefreshBefore: refresh}, nil
}

// RotateToken replaces the FCM token (PUT /v1/installations/{id}/token, spec section
// 3.2). It touches no address, capability, wake key or pairing (PG-ROT-1), and
// re-presenting the current token is the PG-AUTH-5 inactivity refresh. An UNDISCRIMINATED
// 401 surfaces as errGatewayUnauthorized so EnsurePushRegistration can fall back to a fresh
// registration; a 401 request_expired has already been clock-corrected and retried by
// doSigned and surfaces as errRequestExpired, which that fallback must never act on.
func (g *GatewayClient) RotateToken(ctx context.Context, installationID, fcmToken string) error {
	body, err := json.Marshal(struct {
		FCMToken string `json:"fcm_token"`
	}{FCMToken: fcmToken})
	if err != nil {
		return err
	}
	path := "/v1/installations/" + installationID + "/token"
	resp, err := g.doSigned(ctx, http.MethodPut, path, body, "application/json")
	if err != nil {
		return err
	}
	return expectStatus(resp, http.StatusNoContent)
}

// AllocateAddress allocates one unbound per-machine address and its two distinct
// capabilities (POST /v1/installations/{id}/addresses, spec section 3.3). The triple is
// shown once; the caller owns conveying or revoking it.
func (g *GatewayClient) AllocateAddress(ctx context.Context, installationID string) (PushAllocation, error) {
	path := "/v1/installations/" + installationID + "/addresses"
	// The request schema is deliberately empty: the gateway learns nothing about the
	// machine being paired.
	resp, err := g.doSigned(ctx, http.MethodPost, path, []byte("{}"), "application/json")
	if err != nil {
		return PushAllocation{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return PushAllocation{}, err
	}
	if resp.StatusCode != http.StatusCreated {
		return PushAllocation{}, gatewayError(resp.StatusCode, raw)
	}
	var body struct {
		PushAddress             string `json:"push_address"`
		SubmitCapability        string `json:"submit_capability"`
		MachineRevokeCapability string `json:"machine_revoke_capability"`
		UnboundExpiresAt        string `json:"unbound_expires_at"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return PushAllocation{}, fmt.Errorf("phonecore: allocate response: %w", err)
	}
	addrBytes, err := base64.RawURLEncoding.DecodeString(body.PushAddress)
	if err != nil || len(addrBytes) != 16 {
		return PushAllocation{}, errors.New("phonecore: allocate response push_address is not 16 base64url bytes")
	}
	var alloc PushAllocation
	copy(alloc.Address[:], addrBytes)
	alloc.SubmitCapability = body.SubmitCapability
	alloc.MachineRevokeCapability = body.MachineRevokeCapability
	if body.UnboundExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, body.UnboundExpiresAt); err == nil {
			alloc.UnboundExpiresAt = t
		}
	}
	return alloc, nil
}

// RevokeAddress deletes one address with the INSTALLATION key (DELETE
// /v1/addresses/{addr}, spec section 3.4): the phone-initiated "forget this computer"
// arm, and PG-ALLOC-3's immediate cleanup of a failed pairing's allocation. The machine's
// arm presents its own machine-revoke capability and never comes through here
// (PG-AUTH-10).
func (g *GatewayClient) RevokeAddress(ctx context.Context, addr PushAddress) error {
	path := "/v1/addresses/" + EncodePushAddress(addr)
	resp, err := g.doSigned(ctx, http.MethodDelete, path, nil, "")
	if err != nil {
		return err
	}
	return expectStatus(resp, http.StatusNoContent)
}

// doSigned issues one installation-key-signed request (PG-AUTH-1) and, on the one refusal
// that says so, CORRECTS THIS CLIENT'S CLOCK AND RETRIES EXACTLY ONCE.
//
// PG-AUTH-3 returns `server_time` on a 401 `request_expired` for no other purpose: the
// request was well-formed and correctly signed, and the only thing wrong with it was the
// expiry this phone stamped. Consuming it here -- at the one place that stamps expiries --
// keeps the correction out of every caller and, more importantly, keeps a clock skew from
// ever reaching EnsurePushRegistration's re-registration fallback as an "unauthorized".
//
// EXACTLY ONCE, and the bound is the point: a gateway that calls the corrected request
// expired too is not a clock this client can chase, it is a refusal to report. A fresh nonce
// is minted for the retry (the first one is spent whether or not it verified), so the retry
// is a new request and never a replay.
func (g *GatewayClient) doSigned(ctx context.Context, method, path string, body []byte, contentType string) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := g.signedAttempt(ctx, method, path, body, contentType)
		if err != nil || resp.StatusCode != http.StatusUnauthorized || attempt > 0 {
			return resp, err
		}
		raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		_ = resp.Body.Close()
		if rerr != nil {
			return nil, rerr
		}
		code, serverTime := parseGatewayRefusal(raw)
		if code != codeRequestExpired || serverTime.IsZero() {
			// Any other 401 is the caller's to classify, on the body it would have read
			// itself. Handing the buffered bytes back keeps this peek invisible.
			resp.Body = io.NopCloser(bytes.NewReader(raw))
			return resp, nil
		}
		g.mu.Lock()
		g.clockOffset = time.Until(serverTime)
		g.mu.Unlock()
	}
}

// signedAttempt is one signed request: the canonical string
// swarm-pg-v1|METHOD|path|body_sha256|nonce|expiry, path INCLUDING the /v1 prefix and
// excluding any query, the empty body hashed as SHA-256 of zero bytes.
func (g *GatewayClient) signedAttempt(ctx context.Context, method, path string, body []byte, contentType string) (*http.Response, error) {
	nonce, err := randomToken22()
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	offset := g.clockOffset
	g.mu.Unlock()
	expiry := strconv.FormatInt(time.Now().Add(offset).Add(requestExpiryWindow).Unix(), 10)
	bodyHash := sha256.Sum256(body)
	canonical := "swarm-pg-v1|" + method + "|" + path + "|" +
		base64.RawURLEncoding.EncodeToString(bodyHash[:]) + "|" + nonce + "|" + expiry
	sig, err := g.signer.Sign([]byte(canonical))
	if err != nil {
		return nil, fmt.Errorf("phonecore: sign %s %s: %w", method, path, err)
	}

	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Swarm-Nonce", nonce)
	req.Header.Set("Swarm-Expiry", expiry)
	req.Header.Set("Swarm-Signature", "p256-sha256 "+base64.RawURLEncoding.EncodeToString(sig))
	return g.hc.Do(req)
}

// expectStatus drains and closes resp, mapping any status but want onto the gateway
// error taxonomy.
func expectStatus(resp *http.Response, want int) error {
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return err
	}
	if resp.StatusCode != want {
		return gatewayError(resp.StatusCode, raw)
	}
	return nil
}

// gatewayError maps one gateway refusal onto the client's typed errors: 403
// attestation_invalid is ErrAttestationRefused (PG-AUTH-13), a 401 is DISCRIMINATED BY ITS
// CODE -- request_expired is errRequestExpired (a clock, not a credential) and everything
// else is errGatewayUnauthorized -- and any other status carries the code verbatim. Error
// bodies are secret-free by PG-ERR-2, so quoting the code is safe.
//
// THE 401 SPLIT IS LOAD-BEARING. errGatewayUnauthorized is the only value
// EnsurePushRegistration's re-registration fallback acts on, and a request_expired that
// reached it would mint a duplicate installation and orphan the live one for 180 days (see
// the sentinels' own comments). Collapsing the two again is that defect.
func gatewayError(status int, raw []byte) error {
	code, _ := parseGatewayRefusal(raw)
	switch {
	case status == http.StatusForbidden && code == "attestation_invalid":
		return ErrAttestationRefused
	case status == http.StatusUnauthorized && code == codeRequestExpired:
		return errRequestExpired
	case status == http.StatusUnauthorized:
		return errGatewayUnauthorized
	default:
		return fmt.Errorf("phonecore: push gateway refused: status %d code %q", status, code)
	}
}

// parseGatewayRefusal reads the two fields of an error body this client acts on (spec
// section 3.6's Error schema): the code, and the RFC 3339 server_time PG-AUTH-3 attaches to
// request_expired. An unparseable body yields the zero values, which classify as the
// undiscriminated refusal -- the fail-closed direction.
func parseGatewayRefusal(raw []byte) (code string, serverTime time.Time) {
	var body struct {
		Code       string `json:"code"`
		ServerTime string `json:"server_time"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", time.Time{}
	}
	if body.ServerTime != "" {
		if t, err := time.Parse(time.RFC3339, body.ServerTime); err == nil {
			serverTime = t
		}
	}
	return body.Code, serverTime
}

// randomToken22 mints 16 CSPRNG bytes as unpadded base64url -- the shape both
// Idempotency-Key and Swarm-Nonce require (spec sections 2.1 and 3.6).
func randomToken22() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
