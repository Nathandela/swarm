package play

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/remote/push"
)

// SCOPE HONESTY, stated once for the whole file. Every request here goes to a loopback
// httptest.Server and every key is generated in-process. This package has never been run
// against Google: these tests model the PROTOCOL the Publishing API documents -- the call
// sequence, the assertion, the release payload -- and model NOTHING about whether Google
// accepts it. A green run is not evidence that a bundle would publish.

// fakePlay is a stand-in for both Google endpoints the flow touches: the OAuth token
// endpoint and the Publishing API. It records every request so a test can assert on the
// sequence rather than on one call in isolation -- the ordering IS the protocol here, and
// a flow that commits before it sets the track publishes an empty release.
type fakePlay struct {
	srv *httptest.Server

	mu   sync.Mutex
	reqs []recordedRequest

	// Responses, all defaulted by newFakePlay to the happy path.
	accessToken string
	versionCode int64
	editID      string

	tokenStatus  int
	tokenBody    string
	editsStatus  int
	editsBody    string
	uploadStatus int
	uploadBody   string
}

type recordedRequest struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   []byte
}

func newFakePlay(t *testing.T) *fakePlay {
	t.Helper()
	f := &fakePlay{
		accessToken: "ya29.FAKE-ACCESS-TOKEN-do-not-log",
		versionCode: 4711,
		editID:      "edit-9001",
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePlay) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.reqs = append(f.reqs, recordedRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.Query(),
		Header: r.Header.Clone(),
		Body:   body,
	})
	f.mu.Unlock()

	switch {
	case r.URL.Path == "/token":
		if f.tokenStatus != 0 {
			w.WriteHeader(f.tokenStatus)
			_, _ = io.WriteString(w, f.tokenBody)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"access_token": f.accessToken,
			"expires_in":   3600,
			"token_type":   "Bearer",
		})
	case strings.HasSuffix(r.URL.Path, ":commit"):
		writeJSON(w, http.StatusOK, map[string]any{"id": f.editID})
	case strings.Contains(r.URL.Path, "/tracks/"):
		writeJSON(w, http.StatusOK, map[string]any{"track": "internal"})
	case strings.HasSuffix(r.URL.Path, "/bundles"):
		if f.uploadStatus != 0 {
			w.WriteHeader(f.uploadStatus)
			_, _ = io.WriteString(w, f.uploadBody)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"versionCode": f.versionCode})
	case strings.HasSuffix(r.URL.Path, "/edits"):
		if f.editsStatus != 0 {
			w.WriteHeader(f.editsStatus)
			_, _ = io.WriteString(w, f.editsBody)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": f.editID})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakePlay) requests() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.reqs...)
}

// find returns the first recorded request whose path satisfies match.
func (f *fakePlay) find(t *testing.T, what string, match func(recordedRequest) bool) recordedRequest {
	t.Helper()
	for _, r := range f.requests() {
		if match(r) {
			return r
		}
	}
	t.Fatalf("no %s request was made; got %s", what, pathsOf(f.requests()))
	return recordedRequest{}
}

func pathsOf(reqs []recordedRequest) string {
	var b strings.Builder
	for i, r := range reqs {
		if i > 0 {
			b.WriteString(" -> ")
		}
		b.WriteString(r.Method + " " + r.Path)
	}
	if b.Len() == 0 {
		return "(no requests)"
	}
	return b.String()
}

// testAccount generates a THROWAWAY RSA key and wraps it in the service-account JSON
// document Google issues, pointed at the fake token endpoint. No real credential is read
// by this package's tests, and none may ever be.
func testAccount(t *testing.T, tokenURI string) (*push.ServiceAccount, *rsa.PublicKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate test key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal test key: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	doc, err := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     "swarm-test",
		"private_key_id": "test-key-id",
		"private_key":    string(pemBytes),
		"client_email":   "publisher@swarm-test.iam.gserviceaccount.com",
		"token_uri":      tokenURI,
	})
	if err != nil {
		t.Fatalf("marshal service account: %v", err)
	}
	acct, err := push.LoadServiceAccount(doc)
	if err != nil {
		t.Fatalf("load test service account: %v", err)
	}
	return acct, &key.PublicKey
}

// writeAAB drops a stand-in bundle on disk. Its CONTENT is arbitrary; what the tests care
// about is that the exact bytes reach the upload endpoint unaltered.
func writeAAB(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app-release.aab")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write test aab: %v", err)
	}
	return path
}

func testConfig(t *testing.T, f *fakePlay, aab string) Config {
	t.Helper()
	acct, _ := testAccount(t, f.srv.URL+"/token")
	return Config{
		Account: acct,
		Package: "dev.swarm.phone",
		Track:   "internal",
		AAB:     aab,
		BaseURL: f.srv.URL,
	}
}

// TestPublishRunsTheFullEditFlowInOrder pins the call sequence, because the sequence is the
// protocol: an edit must exist before a bundle can go into it, the track must name a
// versionCode the upload returned, and the commit must come last. A flow that reorders any
// of that still returns 200s from a permissive fake and publishes nothing real.
func TestPublishRunsTheFullEditFlowInOrder(t *testing.T) {
	f := newFakePlay(t)
	aab := writeAAB(t, "PK\x03\x04 pretend bundle")

	res, err := Publish(context.Background(), testConfig(t, f, aab))
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}

	want := []struct{ method, path string }{
		{http.MethodPost, "/token"},
		{http.MethodPost, "/androidpublisher/v3/applications/dev.swarm.phone/edits"},
		{http.MethodPost, "/upload/androidpublisher/v3/applications/dev.swarm.phone/edits/edit-9001/bundles"},
		{http.MethodPut, "/androidpublisher/v3/applications/dev.swarm.phone/edits/edit-9001/tracks/internal"},
		{http.MethodPost, "/androidpublisher/v3/applications/dev.swarm.phone/edits/edit-9001:commit"},
	}
	got := f.requests()
	if len(got) != len(want) {
		t.Fatalf("made %d requests, want %d: %s", len(got), len(want), pathsOf(got))
	}
	for i, w := range want {
		if got[i].Method != w.method || got[i].Path != w.path {
			t.Errorf("request %d = %s %s, want %s %s", i, got[i].Method, got[i].Path, w.method, w.path)
		}
	}
	if res.VersionCode != 4711 {
		t.Errorf("Result.VersionCode = %d, want 4711", res.VersionCode)
	}
	if res.EditID != "edit-9001" {
		t.Errorf("Result.EditID = %q, want %q", res.EditID, "edit-9001")
	}
	if !res.Committed {
		t.Error("Result.Committed = false after a full publish")
	}
}

// TestPublishUploadsTheBundleBytesAsAnOctetStream pins the upload request itself: the exact
// file bytes, the media uploadType, and the content type. A bundle uploaded as anything
// else is rejected by Google with an error that does not name the cause.
func TestPublishUploadsTheBundleBytesAsAnOctetStream(t *testing.T) {
	f := newFakePlay(t)
	const content = "PK\x03\x04 the bytes that must arrive unaltered"
	aab := writeAAB(t, content)

	if _, err := Publish(context.Background(), testConfig(t, f, aab)); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	up := f.find(t, "upload", func(r recordedRequest) bool {
		return strings.HasSuffix(r.Path, "/bundles")
	})
	if string(up.Body) != content {
		t.Errorf("uploaded body = %q, want the file's bytes %q", up.Body, content)
	}
	if got := up.Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("upload Content-Type = %q, want application/octet-stream", got)
	}
	if got := up.Query.Get("uploadType"); got != "media" {
		t.Errorf("upload uploadType = %q, want media", got)
	}
}

// TestPublishAuthorizesEveryAPICallWithTheMintedToken pins that the bearer from the token
// exchange is actually attached. An unauthenticated call fails with a 401 whose message
// says nothing about a missing header.
func TestPublishAuthorizesEveryAPICallWithTheMintedToken(t *testing.T) {
	f := newFakePlay(t)
	aab := writeAAB(t, "bundle")

	if _, err := Publish(context.Background(), testConfig(t, f, aab)); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	for _, r := range f.requests() {
		if r.Path == "/token" {
			continue
		}
		if got, want := r.Header.Get("Authorization"), "Bearer "+f.accessToken; got != want {
			t.Errorf("%s %s Authorization = %q, want the minted bearer", r.Method, r.Path, got)
		}
	}
}

// TestPublishSignsAnAssertionThatVerifiesAgainstTheAccountKey is the one test that checks
// the CRYPTO rather than the plumbing: it verifies the RS256 signature with the matching
// public key. Every other assertion about the JWT is worthless if the signature is not one
// Google could verify -- a malformed assertion is rejected as "invalid_grant", which reads
// like a permissions problem and sends the operator to the Console.
func TestPublishSignsAnAssertionThatVerifiesAgainstTheAccountKey(t *testing.T) {
	f := newFakePlay(t)
	aab := writeAAB(t, "bundle")
	acct, pub := testAccount(t, f.srv.URL+"/token")
	cfg := Config{
		Account: acct,
		Package: "dev.swarm.phone",
		Track:   "internal",
		AAB:     aab,
		BaseURL: f.srv.URL,
	}

	before := time.Now()
	if _, err := Publish(context.Background(), cfg); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	tok := f.find(t, "token", func(r recordedRequest) bool { return r.Path == "/token" })
	form, err := url.ParseQuery(string(tok.Body))
	if err != nil {
		t.Fatalf("parse token form: %v", err)
	}
	if got, want := form.Get("grant_type"), "urn:ietf:params:oauth:grant-type:jwt-bearer"; got != want {
		t.Errorf("grant_type = %q, want %q", got, want)
	}

	parts := strings.Split(form.Get("assertion"), ".")
	if len(parts) != 3 {
		t.Fatalf("assertion is not a three-part JWT: %q", form.Get("assertion"))
	}
	signingInput := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	digest := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		t.Fatalf("assertion signature does not verify against the account key: %v", err)
	}

	var header map[string]string
	decodeSegment(t, parts[0], &header)
	if header["alg"] != "RS256" || header["typ"] != "JWT" {
		t.Errorf("JWT header = %v, want alg RS256 and typ JWT", header)
	}

	var claims map[string]any
	decodeSegment(t, parts[1], &claims)
	if got, want := claims["iss"], "publisher@swarm-test.iam.gserviceaccount.com"; got != want {
		t.Errorf("iss = %v, want %v", got, want)
	}
	if got, want := claims["scope"], "https://www.googleapis.com/auth/androidpublisher"; got != want {
		t.Errorf("scope = %v, want %v", got, want)
	}
	if got, want := claims["aud"], f.srv.URL+"/token"; got != want {
		t.Errorf("aud = %v, want the account's token_uri %v", got, want)
	}
	iat, exp := int64(claims["iat"].(float64)), int64(claims["exp"].(float64))
	if iat < before.Add(-time.Minute).Unix() || iat > time.Now().Add(time.Minute).Unix() {
		t.Errorf("iat = %d, want approximately now (%d)", iat, before.Unix())
	}
	// Google rejects an assertion claiming more than an hour.
	if d := time.Duration(exp-iat) * time.Second; d <= 0 || d > time.Hour {
		t.Errorf("exp-iat = %v, want a positive lifetime of at most 1h", d)
	}
}

func decodeSegment(t *testing.T, seg string, into any) {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		t.Fatalf("decode JWT segment: %v", err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("unmarshal JWT segment: %v", err)
	}
}

// TestPublishReleasesWithCompletedStatus is load-bearing and is the reason this command
// exists. A release written with any draft status uploads fine, reports success, and then
// sits in the Play Console waiting for a human to click a button -- which is precisely the
// manual step this command was written to remove. A silent regression to "draft" would
// leave every test above green.
func TestPublishReleasesWithCompletedStatus(t *testing.T) {
	f := newFakePlay(t)
	aab := writeAAB(t, "bundle")

	if _, err := Publish(context.Background(), testConfig(t, f, aab)); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	tr := f.find(t, "track update", func(r recordedRequest) bool {
		return r.Method == http.MethodPut && strings.Contains(r.Path, "/tracks/")
	})
	var payload struct {
		Releases []struct {
			VersionCodes []string `json:"versionCodes"`
			Status       string   `json:"status"`
		} `json:"releases"`
	}
	if err := json.Unmarshal(tr.Body, &payload); err != nil {
		t.Fatalf("track payload is not JSON: %v (%s)", err, tr.Body)
	}
	if len(payload.Releases) != 1 {
		t.Fatalf("track payload carries %d releases, want exactly 1: %s", len(payload.Releases), tr.Body)
	}
	if got := payload.Releases[0].Status; got != "completed" {
		t.Errorf("release status = %q, want %q -- anything else waits for a human click", got, "completed")
	}
	if got := payload.Releases[0].VersionCodes; len(got) != 1 || got[0] != "4711" {
		t.Errorf("versionCodes = %v, want [4711] from the upload response", got)
	}
	if !strings.HasSuffix(tr.Path, "/tracks/internal") {
		t.Errorf("track path = %q, want it to end in /tracks/internal", tr.Path)
	}
}

// TestDryRunDoesEverythingExceptCommit pins the one guarantee --dry-run makes. An
// uncommitted edit expires on Google's side and changes nothing, so uploading under a dry
// run is safe; committing is not, and is irreversible from this side.
func TestDryRunDoesEverythingExceptCommit(t *testing.T) {
	f := newFakePlay(t)
	aab := writeAAB(t, "bundle")
	cfg := testConfig(t, f, aab)
	cfg.DryRun = true

	res, err := Publish(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Publish(dry-run): %v", err)
	}

	for _, r := range f.requests() {
		if strings.HasSuffix(r.Path, ":commit") {
			t.Fatalf("dry run issued a commit: %s %s", r.Method, r.Path)
		}
	}
	if res.Committed {
		t.Error("Result.Committed = true after a dry run")
	}
	// The dry run must still be a real rehearsal: it proves the credential works and the
	// bundle is acceptable. A dry run that skips the upload proves nothing.
	f.find(t, "upload", func(r recordedRequest) bool { return strings.HasSuffix(r.Path, "/bundles") })
	if res.VersionCode != 4711 {
		t.Errorf("Result.VersionCode = %d, want the uploaded 4711 even in a dry run", res.VersionCode)
	}
}

// TestDuplicateVersionCodeIsReportedAsAnUploadThatAlreadyLanded encodes something learned
// the hard way on the owner's other app: Google rejects a versionCode that is already in
// use, and that rejection means the PREVIOUS upload SUCCEEDED. Reported as a generic
// failure it reads as "publishing is broken" and invites a retry loop that cannot ever
// work; the fix is to bump the versionCode, which the message has to say.
func TestDuplicateVersionCodeIsReportedAsAnUploadThatAlreadyLanded(t *testing.T) {
	f := newFakePlay(t)
	f.uploadStatus = http.StatusForbidden
	f.uploadBody = `{"error":{"code":403,"message":"APK specifies a version code that has already been used.","status":"PERMISSION_DENIED"}}`
	aab := writeAAB(t, "bundle")

	_, err := Publish(context.Background(), testConfig(t, f, aab))
	if err == nil {
		t.Fatal("Publish returned nil for a duplicate versionCode")
	}
	if !errors.Is(err, ErrDuplicateVersionCode) {
		t.Fatalf("error does not match ErrDuplicateVersionCode: %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "already") {
		t.Errorf("message does not say the upload already landed: %q", msg)
	}
	if !strings.Contains(strings.ToLower(msg), "version code") && !strings.Contains(strings.ToLower(msg), "versioncode") {
		t.Errorf("message does not name the versionCode as the cause: %q", msg)
	}
}

// TestAPIErrorsSurfaceGooglesMessage pins that the JSON error body is read and reported. A
// bare "400 Bad Request" is the unhelpful failure this repository argues against
// everywhere: Google's message names the actual problem, and dropping it turns a
// two-second fix into a debugging session.
func TestAPIErrorsSurfaceGooglesMessage(t *testing.T) {
	f := newFakePlay(t)
	f.editsStatus = http.StatusNotFound
	f.editsBody = `{"error":{"code":404,"message":"Package not found: dev.swarm.phone.","status":"NOT_FOUND"}}`
	aab := writeAAB(t, "bundle")

	_, err := Publish(context.Background(), testConfig(t, f, aab))
	if err == nil {
		t.Fatal("Publish returned nil for a 404 from the edits endpoint")
	}
	if !strings.Contains(err.Error(), "Package not found: dev.swarm.phone.") {
		t.Errorf("error drops Google's message: %v", err)
	}
}

// TestTokenExchangeFailureNeverEchoesTheAssertion is the credential-leak fence. The token
// endpoint's error body can quote the assertion back, and the assertion is signed with the
// private key: echoing it into a terminal transcript or a CI log is the failure mode that
// matters most here, because it is silent and permanent.
func TestTokenExchangeFailureNeverEchoesTheAssertion(t *testing.T) {
	f := newFakePlay(t)
	f.tokenStatus = http.StatusBadRequest
	aab := writeAAB(t, "bundle")
	cfg := testConfig(t, f, aab)
	// The fake echoes a plausible assertion-bearing error body.
	f.tokenBody = `{"error":"invalid_grant","error_description":"Invalid JWT: eyJhbGciOiJSUzI1NiJ9.eyJpc3MiOiJwdWJsaXNoZXIifQ.SIGNATURE-MATERIAL"}`

	_, err := Publish(context.Background(), cfg)
	if err == nil {
		t.Fatal("Publish returned nil for a refused token exchange")
	}
	msg := err.Error()
	for _, leak := range []string{"SIGNATURE-MATERIAL", "eyJhbGciOiJSUzI1NiJ9", "PRIVATE KEY", "BEGIN"} {
		if strings.Contains(msg, leak) {
			t.Errorf("error echoes credential material %q: %v", leak, err)
		}
	}
}

// TestErrorsNeverCarryTheAccessToken is the second half of the leak fence: once minted, the
// bearer is attached to every request, and an error path that reports the failing request
// verbatim would print it.
func TestErrorsNeverCarryTheAccessToken(t *testing.T) {
	f := newFakePlay(t)
	f.editsStatus = http.StatusInternalServerError
	f.editsBody = `{"error":{"code":500,"message":"backend error"}}`
	aab := writeAAB(t, "bundle")

	_, err := Publish(context.Background(), testConfig(t, f, aab))
	if err == nil {
		t.Fatal("Publish returned nil for a 500")
	}
	if strings.Contains(err.Error(), f.accessToken) {
		t.Errorf("error carries the access token: %v", err)
	}
}

// TestMissingBundleFailsBeforeAnyNetworkCall pins that a typo in --aab costs nothing. The
// alternative -- discovering it after minting a credential and opening an edit -- leaves a
// dangling edit and reads as an API failure.
func TestMissingBundleFailsBeforeAnyNetworkCall(t *testing.T) {
	f := newFakePlay(t)
	cfg := testConfig(t, f, filepath.Join(t.TempDir(), "does-not-exist.aab"))

	if _, err := Publish(context.Background(), cfg); err == nil {
		t.Fatal("Publish returned nil for a missing bundle")
	}
	if got := f.requests(); len(got) != 0 {
		t.Errorf("a missing bundle still made %d requests: %s", len(got), pathsOf(got))
	}
}
