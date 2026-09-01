package pushgw

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type opsSender struct{}

func (opsSender) Send(context.Context, string, []byte) error { return nil }

type opsAttestor struct{}

func (opsAttestor) Verify(context.Context, string) (VerdictBinding, error) {
	return VerdictBinding{}, errors.New("not used")
}

func newOpsServer(t *testing.T, readiness DeploymentReadiness, logger *slog.Logger) *Server {
	t.Helper()
	srv, err := NewServer(Config{
		DBPath:    filepath.Join(t.TempDir(), "pushgw.db"),
		Sender:    opsSender{},
		Attest:    opsAttestor{},
		Readiness: readiness,
		Logger:    logger,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func adminRequest(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	srv.AdminHandler().ServeHTTP(w, req)
	return w
}

func TestAdminReadinessRequiresStaticProductionDependenciesAndWorkers(t *testing.T) {
	srv := newOpsServer(t, DeploymentReadiness{}, nil)
	if got := adminRequest(t, srv, "/healthz").Code; got != http.StatusOK {
		t.Fatalf("healthz = %d, want 200 even when dependencies are not configured", got)
	}

	srv.SetServing(true)
	srv.SetRetentionWorkerRunning(true)
	w := adminRequest(t, srv, "/readyz")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz without production dependencies = %d, want 503", w.Code)
	}
	for _, missing := range []string{"sender", "attestor", "configuration"} {
		if !strings.Contains(w.Body.String(), missing) {
			t.Errorf("readyz body %q does not name missing %s", w.Body.String(), missing)
		}
	}
}

func TestAdminReadinessDoesNotFlapOnTransientProviderOutcome(t *testing.T) {
	srv := newOpsServer(t, DeploymentReadiness{
		ProductionSender:   true,
		ProductionAttestor: true,
		RequiredConfig:     true,
	}, nil)
	srv.SetServing(true)
	srv.SetRetentionWorkerRunning(true)
	srv.recordProviderOutcome(ErrUnavailable)

	w := adminRequest(t, srv, "/readyz")
	if w.Code != http.StatusOK {
		t.Fatalf("readyz after transient upstream failure = %d body=%q, want 200", w.Code, w.Body.String())
	}
}

func TestAdminReadinessChecksThePersistedAEADKey(t *testing.T) {
	srv := newOpsServer(t, DeploymentReadiness{
		ProductionSender:   true,
		ProductionAttestor: true,
		RequiredConfig:     true,
	}, nil)
	srv.SetServing(true)
	srv.SetRetentionWorkerRunning(true)
	if err := os.Remove(srv.store.keyPath); err != nil {
		t.Fatalf("remove temporary key: %v", err)
	}
	w := adminRequest(t, srv, "/readyz")
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "key") {
		t.Fatalf("readyz with missing key = %d body=%q, want 503 naming key", w.Code, w.Body.String())
	}
}

func TestAdminReadinessRejectsSameSizeReplacementAEADKey(t *testing.T) {
	srv := newOpsServer(t, DeploymentReadiness{
		ProductionSender:   true,
		ProductionAttestor: true,
		RequiredConfig:     true,
	}, nil)
	srv.SetServing(true)
	srv.SetRetentionWorkerRunning(true)
	replacement := make([]byte, 32)
	for i := range replacement {
		replacement[i] = byte(i + 1)
	}
	if err := os.WriteFile(srv.store.keyPath, replacement, 0o600); err != nil {
		t.Fatalf("replace temporary key: %v", err)
	}
	w := adminRequest(t, srv, "/readyz")
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "does not match") {
		t.Fatalf("readyz with same-size replacement key = %d body=%q, want 503 mismatch", w.Code, w.Body.String())
	}
}

func TestMetricsAndLogsUseRouteTemplatesNeverCallerIdentifiers(t *testing.T) {
	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	srv := newOpsServer(t, DeploymentReadiness{}, logger)

	const privateAddress = "caller-controlled-private-address"
	req := httptest.NewRequest(http.MethodDelete, "/v1/addresses/"+privateAddress, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if strings.Contains(logs.String(), privateAddress) {
		t.Fatalf("request log leaked path identifier: %q", logs.String())
	}
	if !strings.Contains(logs.String(), "operation=address_revoke") {
		t.Fatalf("request log lacks fixed operation template: %q", logs.String())
	}

	metrics := adminRequest(t, srv, "/metrics")
	if metrics.Code != http.StatusOK {
		t.Fatalf("metrics status = %d", metrics.Code)
	}
	if strings.Contains(metrics.Body.String(), privateAddress) {
		t.Fatalf("metrics leaked path identifier: %q", metrics.Body.String())
	}
	if !strings.Contains(metrics.Body.String(), `pushgw_requests_total{operation="address_revoke",status="401"} 1`) {
		t.Fatalf("metrics missing aggregate request outcome: %q", metrics.Body.String())
	}
	for _, name := range []string{"pushgw_database_bytes", "pushgw_installations", "pushgw_addresses", "pushgw_tombstones"} {
		if !strings.Contains(metrics.Body.String(), name) {
			t.Errorf("metrics missing %s: %q", name, metrics.Body.String())
		}
	}
}

func TestAdminHandlerRejectsNonGETAndUnknownPaths(t *testing.T) {
	srv := newOpsServer(t, DeploymentReadiness{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/healthz"},
		{http.MethodGet, "/private"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		w := httptest.NewRecorder()
		srv.AdminHandler().ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404", tc.method, tc.path, w.Code)
		}
	}
}
