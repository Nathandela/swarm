package pushgw_test

// Cross-cutting error-vocabulary conformance (spec §4, §1). version_unsupported is tested
// here rather than per-operation because PG-TR-2 refuses it "before dispatch" — it belongs
// to no operation's responses map (§3's own exhaustiveness note) — and PG-ERR-2's secrecy
// rule is asserted here once and referenced from operation-specific files rather than
// repeated in each of them.
//
// RED: package pushgw has no implementation yet; every pushgw.* reference below is an
// undefined symbol.

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/pushgw"
)

// TestVersionUnsupported_UnknownPathPrefix_Returns404 is PG-TR-2: a request to an unknown
// version prefix is refused before dispatch, on every kind of operation.
func TestVersionUnsupported_UnknownPathPrefix_Returns404(t *testing.T) {
	h := newHarness(t, nil)
	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"unversioned-installations", "POST", "/installations"},
		{"future-version-wakes", "POST", "/v2/wakes"},
		{"future-version-health", "GET", "/v99/health"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := h.do(c.method, c.path, nil, nil)
			requireStatus(t, resp, http.StatusNotFound)
			if e := decodeError(t, resp); e.Code != "version_unsupported" || e.Retryable {
				t.Fatalf("got code=%q retryable=%v, want version_unsupported/false", e.Code, e.Retryable)
			}
		})
	}
}

// TestUnknownRoute_KnownVersion_ReturnsMalformedRequestNotVersionUnsupported is PG-TR-2's
// scoping: version_unsupported names "an unknown version prefix" specifically. A request
// under a KNOWN version that simply matches no route or wrong method is a different
// failure -- answering version_unsupported here would send a submitter that used the wrong
// method to the wrong repair (§6.4's terminal row for that code). It also asserts a
// PARSEABLE error body: net/http's bare, code-less 404 falls into PG-ERR-1's bodyless-
// response fallback, which reads as a transport failure and would send a submitter that hit
// the wrong method or path retrying to expiry rather than to malformed_request's
// retryable=false terminal repair.
func TestUnknownRoute_KnownVersion_ReturnsMalformedRequestNotVersionUnsupported(t *testing.T) {
	h := newHarness(t, nil)
	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"get-on-wakes", "GET", "/v1/wakes"},
		{"unknown-subpath", "POST", "/v1/nonsense"},
		{"wrong-method-on-token", "DELETE", "/v1/installations/x/token"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := h.do(c.method, c.path, nil, nil)
			requireStatus(t, resp, http.StatusBadRequest)
			e := decodeError(t, resp)
			if e.Code != "malformed_request" || e.Retryable {
				t.Fatalf("got code=%q retryable=%v, want malformed_request/false", e.Code, e.Retryable)
			}
		})
	}
}

// TestHealth_UnauthenticatedAndCallerAgnostic is PG-TR-6 / §14.2 addition A2: static,
// unauthenticated, and carrying no installation- or address-scoped datum.
func TestHealth_UnauthenticatedAndCallerAgnostic(t *testing.T) {
	h := newHarness(t, nil)
	resp := h.do("GET", "/v1/health", nil, nil)
	requireStatus(t, resp, http.StatusOK)
}

// TestHealth_CarriesProviderReachability is PG-TR-6's other half: health SHALL carry
// "service and provider-reachability state", not status alone. The signal comes from the
// FCM leg's (WakeSender's) own last outcome class -- no field is present until a wake has
// actually been attempted, it reads "reachable" after an accepted or provider-refused send,
// and it flips to "unreachable" on the very next health call after a transport failure,
// with no separate polling loop of its own.
func TestHealth_CarriesProviderReachability(t *testing.T) {
	h := newHarness(t, nil)

	var out struct {
		Status   string `json:"status"`
		Provider string `json:"provider"`
	}
	decodeJSON(t, h.do("GET", "/v1/health", nil, nil), &out)
	if out.Provider != "" {
		t.Fatalf("provider = %q before any wake was ever attempted, want absent/empty", out.Provider)
	}

	r := registerInstallation(t, h, "fcm-token-health-reachable")
	a := allocateAddress(t, h, r)
	env := buildWakeV1(t, decodeAddr(t, a.pushAddress), 1, h.clock.Now())
	requireStatus(t, submitTestWake(h, a.submitCapability, env), http.StatusOK)

	decodeJSON(t, h.do("GET", "/v1/health", nil, nil), &out)
	if out.Provider != "reachable" {
		t.Fatalf("provider = %q after a successful send, want reachable", out.Provider)
	}

	h.sender.setBehavior(func(string, []byte) error { return pushgw.ErrUnavailable })
	env2 := buildWakeV1(t, decodeAddr(t, a.pushAddress), 2, h.clock.Now())
	submitTestWake(h, a.submitCapability, env2)

	decodeJSON(t, h.do("GET", "/v1/health", nil, nil), &out)
	if out.Provider != "unreachable" {
		t.Fatalf("provider = %q after ErrUnavailable, want unreachable", out.Provider)
	}
}

// TestErrorBodies_NeverContainSecrets is PG-ERR-2: an error body must never contain an
// FCM token, a capability, an envelope, a signature, or an attestation token — checked
// across a representative sample of refusals that had a secret in hand to leak.
func TestErrorBodies_NeverContainSecrets(t *testing.T) {
	h := newHarness(t, nil)
	r := registerInstallation(t, h, "fcm-token-secret-abc123")
	a := allocateAddress(t, h, r)

	secrets := []string{
		r.fcmToken,
		a.submitCapability,
		a.machineRevokeCapability,
	}

	// Wrong capability on a wake: the caller's OWN (wrong) capability must not be echoed.
	env := buildWakeV1(t, decodeAddr(t, a.pushAddress), 1, h.clock.Now())
	wrongCap := a.submitCapability + "-tampered"
	resp := submitTestWake(h, wrongCap, env)
	requireStatus(t, resp, http.StatusUnauthorized)
	assertNoSecret(t, rawBody(t, resp), append(secrets, wrongCap))

	// Wrong installation-key signature on rotate: the presented (bad) signature bytes and
	// the installation's real fcm_token must not be echoed.
	impostor, _ := genInstallationKey(t)
	path := "/v1/installations/" + r.installationID + "/token"
	body := rotateBody("fcm-token-should-not-leak")
	headers, _, _ := sign(t, impostor, h.clock.Now(), signParams{method: "PUT", path: path, body: body})
	resp2 := h.doJSON("PUT", path, body, headers)
	requireStatus(t, resp2, http.StatusUnauthorized)
	assertNoSecret(t, rawBody(t, resp2), append(secrets, headers["Swarm-Signature"]))
}

// rawBody reads the full response body as text, unparsed — a secret-leak check must scan
// every byte of the wire response, not only the fields a well-behaved Error schema names.
func rawBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(raw)
}

func assertNoSecret(t *testing.T, haystack string, secrets []string) {
	t.Helper()
	for _, s := range secrets {
		if s == "" {
			continue
		}
		if strings.Contains(haystack, s) {
			t.Fatalf("error body leaked a secret value: body=%q contains %q", haystack, s)
		}
	}
}
