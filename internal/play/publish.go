// Package play uploads an Android App Bundle to Google Play through the Publishing API v3
// (edits -> upload -> track -> commit), using only the standard library.
//
// WHY IT IS HAND-ROLLED. The four calls below are the whole surface this project needs.
// google.golang.org/api/androidpublisher brings a large dependency tree to make them, and
// gradle-play-publisher would churn android/app/gradle.lockfile (278 pinned dependencies)
// and android/gradle/verification-metadata.xml (3391 lines of SHA-256s) -- a deliberate
// human-review gate -- to buy one upload. This module ships CGO_ENABLED=0 pure-Go binaries
// with a small dependency surface, and four HTTP calls do not justify changing that.
//
// SCOPE HONESTY. This package models the PROTOCOL the Publishing API documents. It has
// NEVER been run against Google: every test request goes to a loopback httptest.Server,
// no service account in this project has been granted access, and nothing here may be read
// as evidence that a bundle would actually publish.
//
// CREDENTIAL DISCIPLINE. No error, log line or message this package produces may carry the
// private key or the access token. A credential in a terminal transcript or a CI log is
// silent, permanent, and the failure mode that matters most here -- so error bodies from
// the token endpoint are never echoed (they can quote the assertion back), and API failures
// report only Google's own `message` field, never the raw response.
package play

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/remote/push"
)

// publishScope is the ONLY scope the assertion requests: the credential is asserted for
// publishing and nothing else.
const publishScope = "https://www.googleapis.com/auth/androidpublisher"

// jwtBearerGrant is the RFC 7523 grant type Google's token endpoint expects for a
// service-account assertion.
const jwtBearerGrant = "urn:ietf:params:oauth:grant-type:jwt-bearer"

// defaultBaseURL is the Publishing API host. Config.BaseURL overrides it so the tests can
// point the whole flow at an httptest.Server.
const defaultBaseURL = "https://androidpublisher.googleapis.com"

// maxResponseBytes bounds every response read. These are small JSON documents; an
// unbounded ReadAll against a misbehaving endpoint is a memory hazard for no benefit.
const maxResponseBytes = 1 << 20

// ErrDuplicateVersionCode reports that Google refused the bundle because its versionCode
// is already in use -- which means the PREVIOUS upload of that versionCode LANDED.
//
// It is a distinct error because the correct operator response is the opposite of the one a
// generic failure invites: not "retry", which cannot ever succeed, but "the upload you
// think failed actually worked -- check the Console, and bump versionCode before building
// again". Learned the hard way on another app.
var ErrDuplicateVersionCode = errors.New("play: version code already used")

// Config is one publish run.
type Config struct {
	// Account is the parsed service-account credential. Its TokenURI is both the token
	// endpoint and the assertion's audience.
	Account *push.ServiceAccount
	// Package is the applicationId, e.g. dev.swarm.phone.
	Package string
	// Track is the Play track: internal, alpha, beta or production.
	Track string
	// AAB is the path to the App Bundle to upload.
	AAB string
	// DryRun performs every step EXCEPT the commit. The resulting edit is never applied
	// and expires on Google's side, changing nothing.
	DryRun bool
	// BaseURL overrides the Publishing API host. Empty means the real one.
	BaseURL string
	// Client overrides the HTTP client. Empty means http.DefaultClient; the context
	// governs cancellation, since a bundle upload has no sane fixed timeout.
	Client *http.Client
}

// Result is what a run did, so the caller can report it precisely.
type Result struct {
	// EditID is the edit the bundle went into.
	EditID string
	// VersionCode is the code Google assigned to the uploaded bundle.
	VersionCode int64
	// Committed is false after a dry run.
	Committed bool
}

// Publish runs the full edit flow: mint a token, open an edit, upload the bundle, point the
// track at it with a COMPLETED release, and commit.
//
// The order is the protocol. An edit must exist before a bundle can enter it, the track
// must name a versionCode the upload returned, and the commit must come last -- a flow that
// commits before setting the track publishes an empty release.
func Publish(ctx context.Context, cfg Config) (Result, error) {
	if cfg.Account == nil {
		return Result{}, errors.New("play: no service account")
	}
	// Fail on a bad bundle path BEFORE minting a credential or opening an edit: the
	// alternative leaves a dangling edit behind and reads as an API failure.
	bundle, err := os.Open(cfg.AAB)
	if err != nil {
		return Result{}, fmt.Errorf("play: open bundle: %w", err)
	}
	defer func() { _ = bundle.Close() }()
	info, err := bundle.Stat()
	if err != nil {
		return Result{}, fmt.Errorf("play: stat bundle: %w", err)
	}

	p := &publisher{
		client: cfg.Client,
		base:   strings.TrimSuffix(cfg.BaseURL, "/"),
		pkg:    cfg.Package,
	}
	if p.client == nil {
		p.client = http.DefaultClient
	}
	if p.base == "" {
		p.base = defaultBaseURL
	}
	if p.token, err = fetchAccessToken(ctx, p.client, cfg.Account, time.Now()); err != nil {
		return Result{}, err
	}

	var res Result
	if res.EditID, err = p.createEdit(ctx); err != nil {
		return Result{}, err
	}
	if res.VersionCode, err = p.uploadBundle(ctx, res.EditID, bundle, info.Size()); err != nil {
		return Result{}, err
	}
	if err := p.setTrack(ctx, res.EditID, cfg.Track, res.VersionCode); err != nil {
		return Result{}, err
	}
	if cfg.DryRun {
		return res, nil
	}
	if err := p.commit(ctx, res.EditID); err != nil {
		return Result{}, err
	}
	res.Committed = true
	return res, nil
}

// fetchAccessToken performs one JWT-bearer exchange against the account's token_uri.
func fetchAccessToken(ctx context.Context, client *http.Client, acct *push.ServiceAccount, now time.Time) (string, error) {
	assertion, err := acct.SignJWT(publishScope, now)
	if err != nil {
		return "", fmt.Errorf("play: sign assertion: %w", err)
	}
	form := url.Values{"grant_type": {jwtBearerGrant}, "assertion": {assertion}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, acct.TokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("play: OAuth exchange: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("play: OAuth exchange: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// DELIBERATELY BODY-FREE: a token-endpoint error body can echo the assertion,
		// which is signed with the private key.
		return "", fmt.Errorf("play: OAuth exchange refused with status %d "+
			"(the service account may not have Play access yet)", resp.StatusCode)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", fmt.Errorf("play: OAuth response: %w", err)
	}
	if tok.AccessToken == "" {
		return "", errors.New("play: OAuth response carried no access_token")
	}
	return tok.AccessToken, nil
}

// publisher holds the per-run state the four API calls share.
type publisher struct {
	client *http.Client
	base   string
	pkg    string
	token  string
}

// editsURL builds the edits collection URL for the configured package.
func (p *publisher) editsURL() string {
	return p.base + "/androidpublisher/v3/applications/" + url.PathEscape(p.pkg) + "/edits"
}

func (p *publisher) createEdit(ctx context.Context) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	if err := p.do(ctx, http.MethodPost, p.editsURL(), nil, "", "create edit", &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", errors.New("play: create edit: response carried no edit id")
	}
	return out.ID, nil
}

// uploadBundle sends the raw .aab bytes to the media-upload endpoint, which lives under a
// /upload prefix on the same host.
func (p *publisher) uploadBundle(ctx context.Context, editID string, body io.Reader, size int64) (int64, error) {
	u := p.base + "/upload/androidpublisher/v3/applications/" + url.PathEscape(p.pkg) +
		"/edits/" + url.PathEscape(editID) + "/bundles?uploadType=media"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, body)
	if err != nil {
		return 0, err
	}
	// Set explicitly: an *os.File body is not one of the types net/http can size on its
	// own, and a chunked upload is the less well-trodden path through Google's frontend.
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	var out struct {
		VersionCode int64 `json:"versionCode"`
	}
	if err := p.send(req, "upload bundle", &out); err != nil {
		return 0, err
	}
	if out.VersionCode == 0 {
		return 0, errors.New("play: upload bundle: response carried no versionCode")
	}
	return out.VersionCode, nil
}

// setTrack points the track at the uploaded bundle.
//
// status "completed" is LOAD-BEARING. Any draft status uploads fine, reports success, and
// then waits in the Play Console for a human to click a button -- which is exactly the
// manual step this command exists to remove.
func (p *publisher) setTrack(ctx context.Context, editID, track string, versionCode int64) error {
	payload, err := json.Marshal(map[string]any{
		"releases": []map[string]any{{
			"versionCodes": []string{strconv.FormatInt(versionCode, 10)},
			"status":       "completed",
		}},
	})
	if err != nil {
		return err
	}
	u := p.editsURL() + "/" + url.PathEscape(editID) + "/tracks/" + url.PathEscape(track)
	return p.do(ctx, http.MethodPut, u, payload, "application/json", "set track", nil)
}

func (p *publisher) commit(ctx context.Context, editID string) error {
	return p.do(ctx, http.MethodPost, p.editsURL()+"/"+url.PathEscape(editID)+":commit", nil, "", "commit edit", nil)
}

// do issues one authenticated API call and decodes its JSON response into out, if given.
func (p *publisher) do(ctx context.Context, method, u string, body []byte, contentType, stage string, out any) error {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return p.send(req, stage, out)
}

// send attaches the bearer, performs the request, and turns a non-2xx into a legible error.
// The request already carries its context, set by every caller through
// http.NewRequestWithContext.
func (p *publisher) send(req *http.Request, stage string, out any) error {
	req.Header.Set("Authorization", "Bearer "+p.token)
	resp, err := p.client.Do(req)
	if err != nil {
		// url.Error quotes the request URL, which never carries the credential -- the
		// bearer travels in a header.
		return fmt.Errorf("play: %s: %w", stage, err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("play: %s: %w", stage, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return apiError(stage, resp.StatusCode, respBody)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("play: %s: unreadable response: %w", stage, err)
	}
	return nil
}

// apiError turns Google's JSON error document into an error worth reading.
//
// A bare "400 Bad Request" is the unhelpful failure this repository argues against
// everywhere: Google's own message names the actual cause. Only the parsed `message` is
// surfaced, never the raw body -- an unparseable body is reported by status alone rather
// than pasted into a terminal that may be a CI log.
func apiError(stage string, status int, body []byte) error {
	var doc struct {
		Error struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	msg := ""
	if err := json.Unmarshal(body, &doc); err == nil {
		msg = doc.Error.Message
	}
	if isDuplicateVersionCode(msg) {
		return fmt.Errorf("%w: Google rejected this bundle because its version code is already "+
			"in use, which means the EARLIER upload of it ALREADY LANDED -- check the Play Console "+
			"before retrying, and bump versionCode in android/app/build.gradle.kts to publish a new "+
			"build (Google said: %s)", ErrDuplicateVersionCode, msg)
	}
	if msg == "" {
		return fmt.Errorf("play: %s: refused with status %d", stage, status)
	}
	return fmt.Errorf("play: %s: %s (HTTP %d)", stage, msg, status)
}

// isDuplicateVersionCode recognises the several ways Google words the same rejection. It
// matches on the message because the status code does not distinguish it: the same 403
// covers "no access to this app".
func isDuplicateVersionCode(msg string) bool {
	m := strings.ToLower(msg)
	if !strings.Contains(m, "version code") && !strings.Contains(m, "versioncode") {
		return false
	}
	return strings.Contains(m, "already been used") ||
		strings.Contains(m, "already used") ||
		strings.Contains(m, "already exists")
}
