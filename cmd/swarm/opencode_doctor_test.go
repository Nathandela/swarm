package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

func TestDoctorOpenCodeChecksExactPrivateBindingWithoutSubmitting(t *testing.T) {
	for _, tc := range []struct{ scenario, version, want string }{
		{"healthy", "1.19.3", "ok"},
		{"newer-patch", "1.19.4", "ok"},
		{"newer-minor", "1.20.0", "ok"},
		{"future-major", "2.0.0", "ok"},
		{"below-floor", "1.19.2", "warn"},
		{"prerelease", "1.20.0-rc.1", "warn"},
		{"build-suffix", "1.20.0+build", "warn"},
		{"leading-zero", "1.020.0", "warn"},
		{"short-version", "1.20", "warn"},
		{"prefixed-version", "v1.20.0", "warn"},
		{"malformed-version", "unknown", "warn"},
		{"symlink-state", "1.19.3", "ok"},
		{"foreign-ready", "1.19.3", "warn"},
		{"missing-socket", "1.19.3", "warn"},
		{"uncertain-initial", "1.19.3", "warn"},
	} {
		scenario := tc.scenario
		t.Run(scenario, func(t *testing.T) {
			root, err := os.MkdirTemp("/tmp", "ocdr-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			root, err = filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(root, "worker")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			physical, err := filepath.EvalSymlinks(dir)
			if err != nil {
				t.Fatal(err)
			}
			generation := strings.Repeat("a", 32)
			b := opencodebridge.Binding{Version: 1, WorkerID: "worker", SessionInstance: generation, BackendGeneration: generation, RootID: "ses_" + strings.Repeat("b", 32), Directory: root, SocketPath: filepath.Join(physical, "oc.sock"), Token: strings.Repeat("secret-token", 6), Mode: "create", NativeCreatedAtMS: 0}
			m := persist.Meta{SchemaVersion: persist.OpenCodeSchemaVersion, ID: "worker", AgentType: "opencode", RequiredRuntime: persist.RequiredRuntimeOpenCode, ConversationID: b.RootID, Cwd: root, Status: status.Status{Process: status.ProcessRunning}, OpenCodeBinding: &persist.OpenCodeBinding{Revision: 3, Mode: "create", Phase: "ready", Generation: generation, NativeCreatedAtMS: 123}}
			if scenario == "uncertain-initial" {
				m.OpenCodeBinding.Initial = &persist.OpenCodeInitial{Phase: persist.OpenCodeInitialUncertain}
			}
			raw, _ := json.Marshal(b)
			if err := os.WriteFile(filepath.Join(physical, shim.OpenCodeBindingFile), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			var reads atomic.Int32
			if scenario != "missing-socket" {
				listener, err := net.Listen("unix", b.SocketPath)
				if err != nil {
					t.Fatal(err)
				}
				server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != "GET" || r.URL.Path != "/ready" || r.Header.Get("Authorization") != "Bearer "+b.Token {
						t.Error("doctor crossed the read-only bridge boundary")
						w.WriteHeader(400)
						return
					}
					reads.Add(1)
					identity := opencodebridge.Identity{Version: 1, WorkerID: b.WorkerID, SessionInstance: generation, RootID: b.RootID, Directory: root, BackendGeneration: generation, NativeCreatedAtMS: 123}
					if scenario == "foreign-ready" {
						identity.RootID = "ses_" + strings.Repeat("c", 32)
					}
					_ = json.NewEncoder(w).Encode(opencodebridge.Ready{Identity: identity, NativeVersion: tc.version, Bound: true, Acknowledged: true, Ready: true})
				})}
				go func() { _ = server.Serve(listener) }()
				t.Cleanup(func() { _ = server.Close() })
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			stateDir := root
			if scenario == "symlink-state" {
				stateDir = filepath.Join(t.TempDir(), "state")
				if err := os.Symlink(root, stateDir); err != nil {
					t.Fatal(err)
				}
			}
			finding := doctorOpenCodeCheck(ctx, stateDir, m)
			if finding.Status != tc.want {
				t.Fatalf("doctor %s: %+v", scenario, finding)
			}
			encoded, _ := json.Marshal(finding)
			if strings.Contains(string(encoded), b.Token) || strings.Contains(string(encoded), "secret-token") {
				t.Fatal("doctor disclosed private bridge credential")
			}
			if tc.want == "ok" && reads.Load() != 1 {
				t.Fatal("healthy native binding was not actually probed")
			}
		})
	}
}

func TestDoctorOpenCodeNamesDefiniteInitialRefusal(t *testing.T) {
	m := persist.Meta{ID: "worker", SchemaVersion: persist.OpenCodeSchemaVersion, OpenCodeBinding: &persist.OpenCodeBinding{Phase: persist.OpenCodePhaseReady, NativeCreatedAtMS: 123}}
	_ = json.Unmarshal([]byte(`{"phase":"rejected","reason":"initial_agent_unavailable"}`), &m.OpenCodeBinding.Initial)
	finding := doctorOpenCodeCheck(context.Background(), t.TempDir(), m)
	if !strings.Contains(finding.Detail, "initial_agent_unavailable") || strings.Contains(finding.Detail, "unconfirmed") || !strings.Contains(finding.Fix, "new launch") {
		t.Fatalf("definite refusal lacks actionable explanation: %+v", finding)
	}
}
