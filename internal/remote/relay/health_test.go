package relay

// R2 bundle (playbook 6.5) — FAILING-FIRST (TDD RED, GG-5) tests for the health
// and readiness surface a Compose deployment's healthcheck (and any orchestrator)
// polls, plus the low-disk alarm folded into readiness.
//
// Contract these tests pin (none of it exists yet):
//
//   - Config.AdminListen string, json:"admin_listen" — a SEPARATE address from
//     Config.Listen. DefaultConfig sets it to a loopback address (matching the
//     Listen default's ephemeral-port convention) so it is safe out of the box;
//     an operator who leaves it unset in relay.config still gets health/readiness
//     serving, on loopback only. Start REFUSES a non-loopback admin_listen — the
//     public protocol gains no unauthenticated endpoint, and the doctor rule
//     (playbook 6.5) applies to health too: this port is never the public one.
//   - Quotas.DiskFreeMinBytes int64, json:"disk_free_min_bytes" — the low-disk
//     alarm threshold folded into the existing quota knobs. DefaultConfig sets a
//     positive, generous default (CR-4's own pattern: a real cap, on by default).
//   - (*Server).AdminURL() string — the bound admin listener's http:// base, ""
//     if admin_listen is empty (admin serving disabled).
//   - WithDiskFreeFunc(func() (uint64, error)) Option — the free-space seam a
//     test overrides instead of touching the real filesystem, exactly the pattern
//     WithClock/WithSourceKeyFunc already establish.
//   - GET /healthz on the admin listener: process up, always 200.
//   - GET /readyz on the admin listener: 200 while the store is writable, the
//     public listener is accepting, and free disk is above the threshold; 503
//     the moment any one of those is false. Going below the disk threshold logs
//     exactly one bounded warning line per low-disk transition, not once per
//     poll (an orchestrator hits this every few seconds).

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// getAdmin issues a GET against the server's admin listener and returns the
// status code and body.
func getAdmin(t *testing.T, srv *Server, path string) (int, string) {
	t.Helper()
	base := srv.AdminURL()
	if base == "" {
		t.Fatalf("AdminURL() is empty; admin serving is disabled")
	}
	client := &http.Client{Timeout: testDeadline}
	resp, err := client.Get(base + path)
	if err != nil {
		t.Fatalf("GET %s%s: %v", base, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, string(body)
}

// ---------------------------------------------------------------------------
// Defaults.
// ---------------------------------------------------------------------------

func TestDefaultConfig_AdminListenIsLoopback(t *testing.T) {
	got := DefaultConfig().AdminListen
	if got == "" {
		t.Fatalf("DefaultConfig().AdminListen is empty, want a safe loopback default so health/readiness serve out of the box")
	}
	host, _, err := net.SplitHostPort(got)
	if err != nil {
		t.Fatalf("DefaultConfig().AdminListen %q: SplitHostPort: %v", got, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		t.Fatalf("DefaultConfig().AdminListen %q is not loopback-only; the health surface must never be reachable off-host by default", got)
	}
}

func TestDefaultConfig_DiskFreeMinBytesPositive(t *testing.T) {
	got := DefaultConfig().Quotas.DiskFreeMinBytes
	if got <= 0 {
		t.Fatalf("DefaultConfig().Quotas.DiskFreeMinBytes = %d, want > 0: the low-disk alarm must be on by default", got)
	}
}

// ---------------------------------------------------------------------------
// Loopback enforcement.
// ---------------------------------------------------------------------------

func TestStart_RejectsNonLoopbackAdminListen(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Listen = "127.0.0.1:0"
	cfg.DBPath = dbPathForTest(t)
	cfg.AdminListen = "0.0.0.0:9441"
	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	err = srv.Start(testCtx(t))
	if err == nil {
		t.Fatalf("Start with a non-loopback admin_listen (%q) returned nil, want a refusal: the health/readiness surface must never bind off-loopback", cfg.AdminListen)
	}
	if !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("Start error %q does not explain the loopback-only rule", err.Error())
	}
}

func TestAdminListen_EmptyDisablesAdminServer(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, func(c *Config) {
		c.AdminListen = ""
	})
	if got := srv.AdminURL(); got != "" {
		t.Fatalf("AdminURL() = %q with admin_listen empty, want empty (admin serving must be off)", got)
	}
}

// ---------------------------------------------------------------------------
// /healthz and /readyz happy path.
// ---------------------------------------------------------------------------

func TestHealthz_AlwaysReportsProcessUp(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	code, _ := getAdmin(t, srv, "/healthz")
	if code != http.StatusOK {
		t.Fatalf("GET /healthz: status %d, want %d", code, http.StatusOK)
	}
}

func TestReadyz_ReportsReadyOnAHealthyRelay(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	code, _ := getAdmin(t, srv, "/readyz")
	if code != http.StatusOK {
		t.Fatalf("GET /readyz on a healthy relay: status %d, want %d", code, http.StatusOK)
	}
}

// ---------------------------------------------------------------------------
// /readyz reflects the store.
// ---------------------------------------------------------------------------

func TestReadyz_UnreadyWhenStoreUnwritable(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	// White-box: force the underlying store closed without tearing down the
	// whole server, so /readyz's bbolt-writable check has something real to
	// observe fail. Server.Close (t.Cleanup) closes it again afterward, which
	// bbolt tolerates.
	if err := srv.st.close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	code, body := getAdmin(t, srv, "/readyz")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("GET /readyz with the store closed: status %d, want %d; body=%q", code, http.StatusServiceUnavailable, body)
	}
}

// ---------------------------------------------------------------------------
// /readyz reflects the listener.
// ---------------------------------------------------------------------------

func TestReadyz_UnreadyWhenListenerNotAccepting(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, nil)
	srv.listening.Store(false)
	code, body := getAdmin(t, srv, "/readyz")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("GET /readyz with the public listener marked not-accepting: status %d, want %d; body=%q", code, http.StatusServiceUnavailable, body)
	}
	srv.listening.Store(true) // restore, so t.Cleanup's Close sees ordinary state
}

// ---------------------------------------------------------------------------
// /readyz reflects the low-disk alarm, with a BOUNDED warning.
// ---------------------------------------------------------------------------

func TestReadyz_UnreadyWhenDiskBelowThreshold(t *testing.T) {
	srv, _, _, _ := startTestRelay(t, func(c *Config) {
		c.Quotas.DiskFreeMinBytes = 1024 * 1024 * 1024 // 1 GiB
	})
	WithDiskFreeFunc(func() (uint64, error) { return 100, nil })(srv)

	code, body := getAdmin(t, srv, "/readyz")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("GET /readyz with free disk (100 bytes) under the 1 GiB threshold: status %d, want %d; body=%q", code, http.StatusServiceUnavailable, body)
	}
	if !strings.Contains(body, "disk") {
		t.Fatalf("readyz body %q does not name disk space as the reason", body)
	}
}

func TestReadyz_LowDiskWarningIsBoundedNotPerPoll(t *testing.T) {
	var logBuf bytes.Buffer
	srv, _, _, _ := startTestRelay(t, func(c *Config) {
		c.Quotas.DiskFreeMinBytes = 1024 * 1024 * 1024 // 1 GiB
	})
	WithLogWriter(&logBuf)(srv)
	WithDiskFreeFunc(func() (uint64, error) { return 100, nil })(srv)

	// Poll /readyz repeatedly while disk stays low. An orchestrator healthcheck
	// does this every few seconds for the life of the container; the warning
	// must fire once for the transition into "low", not once per poll.
	for i := 0; i < 5; i++ {
		code, _ := getAdmin(t, srv, "/readyz")
		if code != http.StatusServiceUnavailable {
			t.Fatalf("poll %d: status %d, want %d", i, code, http.StatusServiceUnavailable)
		}
	}
	warnings := strings.Count(logBuf.String(), "low disk")
	if warnings != 1 {
		t.Fatalf("low-disk warning logged %d times across 5 polls while continuously low, want exactly 1 (bounded, edge-triggered): log=%q", warnings, logBuf.String())
	}
}

func TestReadyz_LowDiskWarningFiresAgainAfterRecovery(t *testing.T) {
	var logBuf bytes.Buffer
	srv, _, _, _ := startTestRelay(t, func(c *Config) {
		c.Quotas.DiskFreeMinBytes = 1024 * 1024 * 1024 // 1 GiB
	})
	WithLogWriter(&logBuf)(srv)

	low := true
	WithDiskFreeFunc(func() (uint64, error) {
		if low {
			return 100, nil
		}
		return 10 * 1024 * 1024 * 1024, nil // 10 GiB: comfortably above threshold
	})(srv)

	if code, _ := getAdmin(t, srv, "/readyz"); code != http.StatusServiceUnavailable {
		t.Fatalf("first poll (low disk): status %d, want %d", code, http.StatusServiceUnavailable)
	}
	low = false
	if code, _ := getAdmin(t, srv, "/readyz"); code != http.StatusOK {
		t.Fatalf("second poll (recovered): status %d, want %d", code, http.StatusOK)
	}
	low = true
	if code, _ := getAdmin(t, srv, "/readyz"); code != http.StatusServiceUnavailable {
		t.Fatalf("third poll (low again): status %d, want %d", code, http.StatusServiceUnavailable)
	}
	warnings := strings.Count(logBuf.String(), "low disk")
	if warnings != 2 {
		t.Fatalf("low-disk warning logged %d times across low->recovered->low, want exactly 2 (one per transition INTO low): log=%q", warnings, logBuf.String())
	}
}

// ---------------------------------------------------------------------------
// The shipped example config actually turns on what it documents.
// ---------------------------------------------------------------------------

// TestShippedConfigExample_HealthAndSecretFieldsAreSet loads
// deploy/relay/relay.config.example through the real LoadConfig and asserts
// the R2 bundle fields are actually turned on in it, not merely documented in
// a comment: admin_listen is set and loopback, operator_secret_file is set,
// and the low-disk alarm is enabled. repoRoot is shared with
// trustedproxy_test.go (same package).
func TestShippedConfigExample_HealthAndSecretFieldsAreSet(t *testing.T) {
	path := repoRoot(t) + "/deploy/relay/relay.config.example"
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig(%s): %v", path, err)
	}
	if cfg.AdminListen == "" {
		t.Fatalf("shipped relay.config.example sets no admin_listen: /healthz and /readyz would never be reachable")
	}
	if loopback, err := isLoopbackHostPort(cfg.AdminListen); err != nil || !loopback {
		t.Fatalf("shipped relay.config.example admin_listen %q is not loopback-only (err=%v)", cfg.AdminListen, err)
	}
	if cfg.OperatorSecretFile == "" {
		t.Fatalf("shipped relay.config.example sets no operator_secret_file: diagnostics would stay disabled by default")
	}
	if cfg.Quotas.DiskFreeMinBytes <= 0 {
		t.Fatalf("shipped relay.config.example disk_free_min_bytes = %d, want > 0: the low-disk alarm must be on", cfg.Quotas.DiskFreeMinBytes)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

var dbPathCounter int

// dbPathForTest returns a fresh bbolt path under t.TempDir(), for tests (like
// the loopback-refusal one above) that construct a Server directly with New
// rather than going through startTestRelay.
func dbPathForTest(t *testing.T) string {
	t.Helper()
	dbPathCounter++
	return fmt.Sprintf("%s/relay-%d-%d.db", t.TempDir(), time.Now().UnixNano(), dbPathCounter)
}
