//go:build linux

package loginworker

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
)

// TestClaudeAuthInstalledNative is opt-in and fake-account-only. It exercises
// production Start/Run/Recover with test-only loopback TLS transport; TestMain
// supplies internal dispatch. It does NOT qualify actual cmd/swarm construction,
// live provider accounts, a native healthy discussion, or managed policy hooks.
func TestClaudeAuthInstalledNative(t *testing.T) {
	if os.Getenv("SWARM_CLAUDE_AUTH_NATIVE_SMOKE") != "1" {
		t.Skip("opt-in native helper qualification")
	}
	if runtime.GOARCH != "amd64" {
		t.Fatal("only Linux amd64 is qualified")
	}
	binary, err := filepath.EvalSymlinks(os.Getenv("SWARM_CLAUDE_AUTH_NATIVE_BINARY"))
	if err != nil {
		t.Fatal(err)
	}
	expected := os.Getenv("SWARM_CLAUDE_AUTH_NATIVE_SHA256")
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	if expected != "b73676430af12f97316a798b8371f81d640f90a58bdcb8f3ce56a835bb47df56" || hex.EncodeToString(digest[:]) != expected {
		t.Fatal("exact qualified native SHA required")
	}
	scenario := os.Getenv("SWARM_CLAUDE_AUTH_NATIVE_CASE")
	if scenario == "" {
		scenario = "future"
	}
	if scenario != "future" && scenario != "expired" && scenario != "401" && scenario != "wrong-account" && scenario != "worker-kill" {
		t.Fatal("unsupported synthetic case")
	}
	output := os.Getenv("SWARM_CLAUDE_AUTH_NATIVE_OUTPUT")
	if !filepath.IsAbs(output) || filepath.Clean(output) != output {
		t.Fatal("new absolute private output directory required")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(output, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	proxy, network := newClaudeAuthNativeProxy(t, state, scenario)
	defer proxy.drain(t)
	transport, _ := json.Marshal(network)
	if err := os.WriteFile(filepath.Join(state, "claude-auth-fixture-network.json"), transport, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := logins.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().Add(time.Hour)
	if scenario == "expired" {
		expiry = time.Now().Add(-time.Minute)
	}
	auth, _ := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{"accessToken": "synthetic-access-B1", "refreshToken": "synthetic-refresh-B1", "expiresAt": expiry.UnixMilli(), "scopes": []string{"user:inference", "user:profile"}, "subscriptionType": "max", "rateLimitTier": "default_claude_max_5x"}})
	snapshot, login, err := store.Add(snapshot.Revision, logins.ProviderClaude, "synthetic", logins.Credential{Auth: auth, Identity: json.RawMessage(`{"oauthAccount":{"accountUuid":"synthetic-B","organizationUuid":"synthetic-org-B"},"userID":"synthetic-B"}`)})
	if err != nil {
		t.Fatal(err)
	}
	ref, created, err := store.BeginClaudeAuth(snapshot.Revision, login.ID)
	if err != nil || !created {
		t.Fatalf("begin synthetic helper: %v %v", created, err)
	}
	fingerprint, err := persist.CLIFingerprint(binary)
	if err != nil {
		t.Fatal(err)
	}
	config := ClaudeAuthConfig{StateRoot: state, Ref: ref, NativePath: binary, NativeVersion: "2.1.310", NativeFingerprint: fingerprint}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Mechanical same-authority witness, deliberately labelled as such. A real
	// healthy native discussion remains a separate canary, not inferred here.
	authority := filepath.Join(state, "logins", "saved", login.ID)
	beforeCanonical, readErr := os.ReadFile(filepath.Join(authority, ".credentials.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	witness := exec.Command("/bin/sleep", "70")
	witness.Env = []string{"PATH=/usr/bin:/bin", "CLAUDE_SECURESTORAGE_CONFIG_DIR=" + authority}
	if err := witness.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = witness.Process.Kill(); _ = witness.Wait() }()
	started, err := procstart.StartTime(witness.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	healthy := processcontain.Identity{PID: witness.Process.Pid, StartTime: started}
	ctx, cancel := context.WithTimeout(t.Context(), 65*time.Second)
	defer cancel()
	cleaned := false
	t.Cleanup(func() {
		if cleaned {
			return
		}
		current, readErr := ReadClaudeAuth(config)
		if readErr == nil && current.Scope != (logins.ClaudeAuthScope{}) {
			_ = processcontain.SignalIdentity(current.Scope.Worker, syscall.SIGKILL)
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cleanupCancel()
		if _, err := RecoverClaudeAuth(cleanupCtx, config); err != nil {
			t.Log("claude_auth_fixture_cleanup_pending: retain exact Store/unit proof")
		}
	})
	launched, err := StartClaudeAuth(ctx, executable, config)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var result logins.ClaudeAuthResult
	if scenario == "worker-kill" {
		select {
		case <-proxy.profileEntered:
		case <-ctx.Done():
			t.Fatal("native profile gate not reached")
		}
		result, err = ReadClaudeAuth(config)
		if err != nil || result.Phase != "running" || result.Scope.Worker != launched.Scope.Worker || !result.Scope.Worker.Alive() {
			t.Fatal("expected exact running attempt before worker kill")
		}
		if err := processcontain.SignalIdentity(result.Scope.Worker, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		until := time.Now().Add(3 * time.Second)
		for result.Scope.Worker.Alive() && time.Now().Before(until) {
			time.Sleep(10 * time.Millisecond)
		}
		result, err = ReadClaudeAuth(config)
		if err != nil || result.Phase != "running" || result.Scope.Worker.Alive() {
			t.Fatal("killed worker must leave unresolved running attempt, never ready")
		}
	}
	for scenario != "worker-kill" {
		result, err = ReadClaudeAuth(config)
		if err != nil {
			t.Fatal(err)
		}
		if result.Phase == "ready" || result.Phase == "failed" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("bounded native helper deadline")
		case <-time.After(25 * time.Millisecond):
		}
	}
	expectedReady := scenario == "future" || scenario == "expired"
	if (result.Phase == "ready") != expectedReady {
		t.Fatalf("unexpected native result phase %s", result.Phase)
	}
	if !healthy.Alive() {
		t.Fatal("helper touched independent same-authority witness")
	}
	if proxy.invalid.Load() || proxy.models.Load() != 0 {
		t.Fatal("unexpected native request or inference")
	}
	refreshes := proxy.refresh.Load()
	expectedRefresh := int64(0)
	expectedAttempt := 1
	if scenario == "expired" {
		expectedRefresh = 1
		expectedAttempt = 2
	}
	if refreshes != expectedRefresh || result.Attempt != expectedAttempt || proxy.profiles.Load() < 1 {
		t.Fatalf("unexpected refresh/profile confirmation: refresh=%d attempt=%d profile=%d", refreshes, result.Attempt, proxy.profiles.Load())
	}
	after, err := os.ReadFile(filepath.Join(authority, ".credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if scenario == "expired" {
		if !strings.Contains(string(after), "synthetic-access-B2") || !strings.Contains(string(after), "synthetic-refresh-B2") {
			t.Fatal("native renewal not preserved")
		}
	} else if !bytes.Equal(beforeCanonical, after) {
		t.Fatal("nonrenewal changed canonical synthetic authority")
	}
	// A fresh verifier process receives only immutable config; it has no observer
	// FDs or parent in-memory scope. The production verifier reloads durable Store.
	configPath := filepath.Join(state, "logins", "claude-auth", ref.LoginID, ref.Nonce, "worker.json")
	var receipt *boundedBuffer
	recoveryCtx, recoveryCancel := context.WithTimeout(ctx, 15*time.Second)
	defer recoveryCancel()
	for {
		recovery := exec.CommandContext(recoveryCtx, executable, "internal", "claude-auth-fixture-recover", configPath)
		receipt = &boundedBuffer{limit: 4096}
		recovery.Stdout = receipt
		recovery.Stderr = io.Discard
		if err := recovery.Run(); err == nil {
			break
		}
		if scenario != "worker-kill" || recoveryCtx.Err() != nil {
			t.Fatal("fresh-process production recovery failed; retain exact Store/unit proof")
		}
		time.Sleep(50 * time.Millisecond)
	}
	recoveredRaw, overflow := receipt.Bytes()
	var recovered ClaudeAuthRecovery
	if overflow || json.Unmarshal(recoveredRaw, &recovered) != nil || !recovered.Retired || recovered.MetadataCleanupUnknown {
		t.Fatal("incomplete bound-unit recovery")
	}
	cleaned = true
	if launched.Scope.Worker.Alive() || !healthy.Alive() {
		t.Fatal("wrong process custody after recovery")
	}
	unit, err := claudeAuthUnit(ctx, launched.Scope.Unit)
	if err != nil || unit["LoadState"] != "not-found" {
		t.Fatal("owned unit metadata not collected")
	}
	proxy.drain(t)
	// Descendants may still run before recovery; the final traffic and authority
	// assertions must follow positively verified unit retirement and collection.
	if proxy.invalid.Load() || proxy.models.Load() != 0 || proxy.refresh.Load() != expectedRefresh {
		t.Fatal("unexpected native traffic before verified retirement")
	}
	finalCanonical, err := os.ReadFile(filepath.Join(authority, ".credentials.json"))
	if err != nil || !bytes.Equal(after, finalCanonical) {
		t.Fatal("canonical authority changed during recovery")
	}
	phase := result.Phase
	if scenario == "worker-kill" {
		phase = "unready_recovered"
	}
	summary := map[string]any{"passed": true, "case": scenario, "native_sha256": expected, "phase": phase, "attempts": result.Attempt, "refreshes": refreshes, "profiles": proxy.profiles.Load(), "inference_requests": proxy.models.Load(), "fresh_verifier": true, "same_authority_witness": "synthetic-sleep-not-native-discussion", "healthy_pid_unchanged": healthy.Alive(), "unit_collected": true, "scope": launched.Scope, "cmd_swarm_constructor_qualified": false}
	data, _ := json.MarshalIndent(summary, "", "  ")
	if err := os.WriteFile(filepath.Join(output, "receipt.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

type claudeAuthNativeProxy struct {
	server                              *httptest.Server
	profiles, refresh, models, requests atomic.Int64
	invalid                             atomic.Bool
	profileEntered                      chan struct{}
	mu                                  sync.Mutex
	stopped                             bool
	stop                                chan struct{}
	handlers                            sync.WaitGroup
}

func (p *claudeAuthNativeProxy) drain(t *testing.T) {
	t.Helper()
	p.mu.Lock()
	if !p.stopped {
		p.stopped = true
		close(p.stop)
	}
	p.mu.Unlock()
	p.server.Close()
	done := make(chan struct{})
	go func() { p.handlers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("synthetic proxy handlers failed to drain")
	}
}

type claudeAuthBufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c claudeAuthBufferedConn) Read(b []byte) (int, error) { return c.reader.Read(b) }
func newClaudeAuthNativeProxy(t *testing.T, state, scenario string) (*claudeAuthNativeProxy, claudeAuthFixtureNetwork) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "swarm-synthetic.invalid"}, DNSNames: []string{"api.anthropic.com", "platform.claude.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	ca := filepath.Join(state, "fixture-ca.pem")
	if err := os.WriteFile(ca, certificate, 0600); err != nil {
		t.Fatal(err)
	}
	proxy := &claudeAuthNativeProxy{profileEntered: make(chan struct{}, 1), stop: make(chan struct{})}
	proxy.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.mu.Lock()
		if proxy.stopped {
			proxy.mu.Unlock()
			return
		}
		proxy.handlers.Add(1)
		proxy.mu.Unlock()
		defer proxy.handlers.Done()
		if proxy.requests.Add(1) > 96 || r.Method != "CONNECT" || (r.Host != "api.anthropic.com:443" && r.Host != "platform.claude.com:443") {
			proxy.invalid.Store(true)
			http.Error(w, "synthetic refusal", http.StatusForbidden)
			return
		}
		connection, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			proxy.invalid.Store(true)
			return
		}
		defer func() { _ = connection.Close() }()
		_ = connection.SetDeadline(time.Now().Add(4 * time.Second))
		_, _ = buffer.WriteString("HTTP/1.1 200 Connection established\r\n\r\n")
		if buffer.Flush() != nil {
			return
		}
		secure := tls.Server(claudeAuthBufferedConn{connection, buffer.Reader}, &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12})
		if secure.Handshake() != nil {
			return
		}
		request, err := http.ReadRequest(bufio.NewReader(secure))
		if err != nil {
			return
		}
		defer func() { _ = request.Body.Close() }()
		status := 200
		var response any = map[string]any{}
		switch {
		case request.Method == "GET" && request.URL.Path == "/api/oauth/profile":
			proxy.profiles.Add(1)
			bearer := request.Header.Get("Authorization")
			if bearer != "Bearer synthetic-access-B1" && bearer != "Bearer synthetic-access-B2" {
				proxy.invalid.Store(true)
				return
			}
			if scenario == "worker-kill" {
				select {
				case proxy.profileEntered <- struct{}{}:
				default:
				}
				// Never release a successful profile: even the timeout closes the
				// connection, so killing the worker cannot race readiness.
				select {
				case <-request.Context().Done():
				case <-proxy.stop:
				case <-time.After(4 * time.Second):
				}
				return
			}
			if scenario == "401" {
				status = 401
				response = map[string]any{"error": map[string]any{"type": "authentication_error", "message": "synthetic refusal"}}
				break
			}
			account := "synthetic-B"
			if scenario == "wrong-account" {
				account = "synthetic-WRONG"
			}
			response = map[string]any{"account": map[string]any{"uuid": account, "email": "B@example.invalid", "created_at": "2024-01-01T00:00:00.000Z"}, "organization": map[string]any{"uuid": "synthetic-org-B", "organization_type": "claude_max", "rate_limit_tier": "default_claude_max_5x", "billing_type": "stripe_subscription", "subscription_created_at": "2024-01-01T00:00:00.000Z", "cc_onboarding_flags": map[string]any{}}}
		case request.Method == "POST" && request.URL.Path == "/v1/oauth/token":
			raw, err := io.ReadAll(io.LimitReader(request.Body, 64001))
			var body map[string]any
			if err != nil || len(raw) > 64000 || json.Unmarshal(raw, &body) != nil || body["grant_type"] != "refresh_token" || body["refresh_token"] != "synthetic-refresh-B1" || proxy.refresh.Add(1) != 1 {
				proxy.invalid.Store(true)
				status = 400
				break
			}
			response = map[string]any{"access_token": "synthetic-access-B2", "refresh_token": "synthetic-refresh-B2", "expires_in": 3600, "token_type": "Bearer", "scope": "user:inference user:profile"}
		default:
			if request.Method == "POST" && strings.Contains(request.URL.Path, "/messages") {
				proxy.models.Add(1)
			}
			status = 404
		}
		raw, _ := json.Marshal(response)
		_, _ = fmt.Fprintf(secure, "HTTP/1.1 %d Synthetic\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", status, len(raw))
		_, _ = secure.Write(raw)
	}))
	digest := sha256.Sum256(certificate)
	return proxy, claudeAuthFixtureNetwork{Proxy: proxy.server.URL, CA: ca, SHA256: hex.EncodeToString(digest[:])}
}
