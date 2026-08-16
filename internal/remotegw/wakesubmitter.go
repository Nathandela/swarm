package remotegw

// HTTPWakeSubmitter is the production WakeSubmitter (spec §3.5, PG-SUB-1/2): it POSTs
// the 74 raw octets, unopened and unmodified, to the gateway's POST /v1/wakes under the
// submit capability.
//
// ON THE BODYLESS-RESPONSE MAPPING (a divergence resolved, not glossed): PG-ERR-1's own
// text describes a status-based fallback for a response with no parseable error body
// ("treat 429 and 5xx as retryable and every other status as not"). But §6.4's transition
// table places that SAME case in its "timeout / transport failure" row -- "handled here,
// NOT as a code" -- and PG-ERR-3 names PG-ERR-1's fallback as the exception §6.4 overrides
// for this machine specifically. Taken together, a bodyless response is unconditionally
// retryable here: SubmitWake returns it PLAIN (never *WakeSubmitError), exactly like a
// response that never arrived at all. The alternative (apply PG-ERR-1's status rule and
// let a bodyless 400/401 reach the machine's terminal `abandoned` state) is the UNSAFE
// reading: a truncating proxy or a misbehaving intermediary, positioned on-path with no
// authentication of its own, could then permanently suppress a pairing's push by
// truncating any response body -- which is a strictly worse failure mode than "retry an
// extra few times before expiry", the cost of the reading this file implements instead.
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// HTTPWakeSubmitter forwards a WakeV1 envelope to the push gateway.
type HTTPWakeSubmitter struct {
	BaseURL          string
	SubmitCapability string
	Client           *http.Client // nil => defaultWakeSubmitClient
}

var _ WakeSubmitter = (*HTTPWakeSubmitter)(nil)

// defaultWakeSubmitClient is used when Client is nil. An explicit Timeout matters beyond
// the ordinary per-call ctx bound: push.go's normal call already wraps the ctx in
// defaultPushTimeout, but a restart re-drive path (PG-OBL-8) may call SubmitWake with a
// context carrying no deadline of its own, and an unbounded HTTP call there would hang a
// re-drive indefinitely against a gateway that accepted the TCP connection and then never
// answered.
var defaultWakeSubmitClient = &http.Client{Timeout: 30 * time.Second, CheckRedirect: refuseWakeSubmitRedirect}

// refuseWakeSubmitRedirect refuses every redirect a submit response tries to issue
// (PG-TR-1). The submit surface is a single POST; a redirect target is never a valid
// answer to it. Left to Go's default policy, http.Client follows up to 10 redirects and
// strips the Authorization header only when the redirect target's HOST differs -- so a
// same-host scheme-downgrading 302 from a compromised or misbehaving gateway (a named
// adversary, spec §7.3) would replay the submit capability and the 74-byte WakeV1
// envelope over cleartext. http.ErrUseLastResponse makes Client.Do return the redirect
// response itself, unfollowed and with no error, which SubmitWake below then treats
// exactly like any other non-2xx response -- no second request is ever issued, to any
// host or scheme, so there is no "final URL" left to separately re-check for HTTPS.
func refuseWakeSubmitRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

// wakeProviderAcceptedStatus is the exact §3.5-pinned `status` value a 200 response body
// must carry for SubmitWake to treat the wake as delivered (PG-SUB-2).
const wakeProviderAcceptedStatus = "provider_accepted"

// maxGatewayMessageLen bounds how much of the gateway's free-text `message` field this
// process will ever hold, log, or surface through WakeSubmitError.Error(). The gateway is
// a named, potentially-compromised party (spec §7.3): nothing stops it returning an
// oversized or control-character-laden string, and unbounded gateway text reaching
// machine logs verbatim is a log-injection surface this local truncation closes cheaply.
const maxGatewayMessageLen = 200

// sanitizeGatewayMessage strips control characters (which would otherwise let a hostile
// or malfunctioning gateway inject newlines/escape sequences into machine logs) and
// truncates to maxGatewayMessageLen.
func sanitizeGatewayMessage(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			b.WriteByte(' ')
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if len(out) > maxGatewayMessageLen {
		out = out[:maxGatewayMessageLen] + "...(truncated)"
	}
	return out
}

// SubmitWake POSTs envelope to {BaseURL}/v1/wakes (spec §3.5). A nil error means the
// gateway answered 200 with the §3.5-pinned body `{"status":"provider_accepted"}` --
// FCM already accepted the byte-identical request (PG-SUB-2). The status field is
// actually parsed and checked (not inferred from the HTTP code alone): the gateway is a
// named, potentially-compromised party (spec §7.3), and a bare 200 whose body does not
// carry that exact constant must not retire the obligation as delivered on FCM's behalf.
// A non-2xx response with a parseable §3.6 Error body returns a *WakeSubmitError carrying
// the gateway's own `retryable` verdict (PG-ERR-3). A response with NO parseable body --
// including a 200 whose body fails to pin provider_accepted -- and any error where no
// response was ever received at all (timeout, refused connection, any transport failure)
// -- are ALL returned PLAIN, never wrapped as *WakeSubmitError: see this file's header for
// why the bodyless case joins the unconditionally-retryable transport-failure case rather
// than PG-ERR-1's status fallback.
func (s *HTTPWakeSubmitter) SubmitWake(ctx context.Context, envelope []byte) error {
	if len(envelope) != WakeV1Size {
		// A local, fail-closed shape check: cheaper than a round trip to earn the
		// gateway's own `wake_malformed` refusal, which this file's own mapping (and
		// §6.4) makes terminal (abandoned) -- so catching a malformed envelope here,
		// before it can ever be submitted, is strictly safer than catching it on the wire.
		return fmt.Errorf("remotegw: refusing to submit a %d-byte envelope, want the pinned WakeV1Size %d", len(envelope), WakeV1Size)
	}
	base, err := url.Parse(s.BaseURL)
	if err != nil {
		return fmt.Errorf("remotegw: invalid gateway BaseURL: %w", err)
	}
	if base.Scheme != "https" {
		// PG-TR-1: every gateway operation is HTTPS only. Refused here, before any
		// request leaves the process, rather than left to whatever TLS enforcement (or
		// its absence) sits between this process and the configured URL -- the submit
		// capability and the WakeV1 authenticator both go out with this request, and
		// neither may ever be put on a cleartext wire.
		return fmt.Errorf("remotegw: refusing a non-https gateway BaseURL %q (PG-TR-1)", s.BaseURL)
	}

	client := s.Client
	if client == nil {
		client = defaultWakeSubmitClient
	}
	if client.CheckRedirect == nil {
		// An injected Client (tests, or a future caller) may not have
		// refuseWakeSubmitRedirect set. Rather than mutate a client this type does not
		// own -- a data race if it is shared with a concurrent caller for any other
		// purpose -- submit through a value COPY carrying the same Transport/Timeout/Jar
		// with only CheckRedirect overridden.
		cp := *client
		cp.CheckRedirect = refuseWakeSubmitRedirect
		client = &cp
	}
	reqURL := strings.TrimRight(s.BaseURL, "/") + "/v1/wakes"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(envelope))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Authorization", "Swarm-Capability "+s.SubmitCapability)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode == http.StatusOK {
		var accepted struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(body, &accepted) == nil && accepted.Status == wakeProviderAcceptedStatus {
			return nil
		}
		// A 200 that does not carry the §3.5-pinned body: NOT treated as delivered.
		// Returned PLAIN, exactly like a bodyless non-2xx response (see this file's
		// header) -- unconditionally retryable, never *WakeSubmitError, since the
		// gateway declared no §3.6 error code to parse either.
		return fmt.Errorf("remotegw: gateway 200 response did not carry the pinned body {\"status\":%q}", wakeProviderAcceptedStatus)
	}

	var parsed struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		Retryable bool   `json:"retryable"`
	}
	if len(body) > 0 && json.Unmarshal(body, &parsed) == nil && parsed.Code != "" {
		return &WakeSubmitError{
			Code: parsed.Code, Retryable: parsed.Retryable,
			Message:    sanitizeGatewayMessage(parsed.Message),
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}
	// No parseable error body: see this file's header. Returned PLAIN, exactly like a
	// transport failure, never as a *WakeSubmitError.
	return fmt.Errorf("remotegw: gateway response (status %d) carried no parseable error body", resp.StatusCode)
}

// parseRetryAfter reads an HTTP Retry-After value in its delta-seconds form (the shape
// spec §3.6's Throttled response uses), so PG-OBL-9's scheduler can honour it. The
// HTTP-date form is deliberately NOT parsed: turning an absolute date into a delay needs
// a wall-clock read this otherwise clock-injected package has no seam for, and the
// gateway under this spec never sends one -- an unrecognised value is simply no floor,
// which the machine's ordinary backoff already covers safely.
func parseRetryAfter(v string) time.Duration {
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}
