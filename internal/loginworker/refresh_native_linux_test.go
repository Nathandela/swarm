//go:build linux

package loginworker

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/processcontain"
)

// Opt-in only: root runs this under a unique bounded systemd cgroup. Every
// native auth value is synthetic; the actual CLI and Swarm executable are SHA pinned.
func TestCodexRefreshInstalledNative(t *testing.T) {
	if os.Getenv("SWARM_TEST_CODEX_BINARY") == "" {
		t.Skip("installed native refresh qualification is opt-in")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 85*time.Second)
	defer cancel()
	codex := nativePinnedBinary(t, "SWARM_TEST_CODEX_BINARY", "SWARM_TEST_CODEX_SHA256")
	version := os.Getenv("SWARM_TEST_CODEX_VERSION")
	if version == "" {
		version = "0.163.0"
	}
	if !logins.SupportsCodexGrantRefresh(version) {
		t.Fatal("exact qualified native version required")
	}
	swarm := nativePinnedBinary(t, "SWARM_TEST_SWARM_BINARY", "SWARM_TEST_SWARM_SHA256")
	group := os.Getenv("SWARM_TEST_NATIVE_CGROUP")
	cgroup, err := os.ReadFile("/proc/self/cgroup")
	if err != nil || !strings.HasPrefix(group, "swarm-native-refresh-") || strings.ContainsAny(group, "/\n\r") || !strings.Contains(string(cgroup), "/"+group+".service") {
		t.Fatal("a unique swarm-native-refresh-* systemd service cgroup is required")
	}
	root := os.Getenv("SWARM_TEST_NATIVE_ROOT")
	if !filepath.IsAbs(root) {
		t.Fatal("private native test root required")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		t.Fatal("native test root must already exist at mode 0700")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		t.Fatal("native test root must belong to the test owner")
	}
	source := os.Getenv("SWARM_TEST_SOURCE_COMMIT")
	if len(source) != 40 {
		t.Fatal("exact source commit required for receipt")
	}
	t.Logf("native=%s native_sha256=%s swarm_sha256=%s source_commit=%s cgroup=%s", version, os.Getenv("SWARM_TEST_CODEX_SHA256"), os.Getenv("SWARM_TEST_SWARM_SHA256"), source, group)
	for _, scenario := range []string{"two_cycles", "expired", "partial_renewal", "revoked", "worker_killed"} {
		t.Run(scenario, func(t *testing.T) {
			dir, err := os.MkdirTemp(root, "worker-")
			if err != nil {
				t.Fatal(err)
			}
			// Only this test-created directory and exact observed socket targets are owned.
			var preserve atomic.Bool
			t.Cleanup(func() {
				if t.Failed() {
					t.Logf("retained_synthetic_case=%s", dir)
				} else if !preserve.Load() {
					_ = os.RemoveAll(dir)
				}
			})
			expiry := time.Now().Add(time.Hour)
			if scenario == "expired" || scenario == "partial_renewal" {
				expiry = time.Now().Add(-time.Hour)
			}
			store, id, home := nativeGrantFixture(t, dir, expiry)
			t.Cleanup(func() { _ = store.Close() })
			before, owner := nativeGrantState(t, store, id, home)
			authBefore := nativeAuthDigest(t, home)
			oauth := newNativeOAuth(t, ctx, scenario)
			t.Cleanup(oauth.server.Close)
			transportEnv := []string{"CODEX_REFRESH_TOKEN_URL_OVERRIDE=" + oauth.server.URL + "/oauth/token"}
			for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
				transportEnv = append(transportEnv, key+"="+oauth.server.URL)
			}
			baseURL, _ := json.Marshal(oauth.server.URL)
			transportEnv = append(transportEnv, "NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost")
			launcher := newRefreshFixtureTransport(t, dir, refreshFixtureTransport{Target: codex, SHA256: os.Getenv("SWARM_TEST_CODEX_SHA256"), Prefix: []string{"-c", "chatgpt_base_url=" + string(baseURL)}, Environment: transportEnv})
			fingerprint, err := persist.CLIFingerprint(launcher)
			if err != nil {
				t.Fatal(err)
			}
			cycles := 1
			if scenario == "two_cycles" {
				cycles = 2
			}
			for cycle := 1; cycle <= cycles; cycle++ {
				oauth.blockNext.Store(true)
				nativePinnedBinary(t, "SWARM_TEST_CODEX_BINARY", "SWARM_TEST_CODEX_SHA256")
				config := RefreshConfig{StateRoot: dir, LoginID: id, Generation: uint64(cycle), NativePath: launcher, NativeVersion: version, NativeFingerprint: fingerprint, Deadline: time.Now().Add(12 * time.Second)}
				ref, err := StartCodexRefresh(ctx, swarm, config)
				if err != nil {
					t.Fatalf("start actual refresh worker: %v", err)
				}
				target := ""
				t.Cleanup(func() {
					if !nativeCleanupWorker(ref, home) {
						preserve.Store(true)
						t.Error("owned worker containment unavailable; retaining private case root for outer cgroup cleanup")
					}
				})

				select {
				case <-oauth.requests:
				case <-ctx.Done():
					t.Fatal("qualification deadline before OAuth")
				case <-time.After(10 * time.Second):
					p, _ := ReadCodexRefresh(ref)
					t.Fatalf("native did not request synthetic OAuth: phase=%s code=%s", p.Phase, p.ErrorCode)
				}
				progress, err := ReadCodexRefresh(ref)
				if err != nil || !progress.Native.Alive() {
					t.Fatal("OAuth request has no owned live native identity")
				}
				socket := filepath.Join(dir, "logins", "refresh", id, "n")
				target, err = codexSocketPath(socket)
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("native socket alias not characterized: %v", err)
				}

				if _, err = StartCodexRefresh(ctx, swarm, config); !errors.Is(err, ErrUnavailable) {
					t.Fatalf("concurrent same-grant worker admitted: %v", err)
				}
				if lease, e := store.AcquireCodexGrant(id); e == nil {
					_ = lease.Close()
					t.Fatal("active native grant custody was not exclusive")
				}
				if scenario == "worker_killed" {
					if err = processcontain.SignalIdentity(ref.Worker, syscall.SIGKILL); err != nil {
						t.Fatal(err)
					}
					nativeWaitDead(t, ctx, ref.Worker)
					automatic := !progress.Native.Alive()
					if !automatic {
						select {
						case <-time.After(500 * time.Millisecond):
						case <-ctx.Done():
						}
						automatic = !progress.Native.Alive()
					}
					retained := false
					if !automatic {
						lease, e := store.AcquireCodexGrant(id)
						if e == nil {
							_ = lease.Close()
							t.Fatal("worker death released a live native writer")
						}
						retained = true
						if err = processcontain.SignalIdentity(progress.Native, syscall.SIGKILL); err != nil {
							t.Fatal(err)
						}
					}
					nativeWaitDead(t, ctx, progress.Native)
					writers, e := processcontain.EnvironmentWriters([]string{home}, nil)
					if e != nil || len(writers) != 0 {
						t.Fatal("owned native writers remain after death qualification")
					}
					lease, e := store.AcquireCodexGrant(id)
					if e != nil {
						t.Fatalf("stopped native writer did not recover availability: %v", e)
					}
					if e = lease.ProveStopped(); e != nil {
						t.Fatal(e)
					}
					if e = lease.Close(); e != nil {
						t.Fatal(e)
					}
					after, afterOwner := nativeGrantState(t, store, id, home)
					if after != before || afterOwner != owner || nativeAuthDigest(t, home) != authBefore {
						t.Fatal("killed blocked refresh mutated canonical grant")
					}
					receipt, e := ReadCodexRefresh(ref)
					if e != nil || receipt.Phase == PhaseReady || receipt.WritersStopped {
						t.Fatal("killed worker fabricated completion proof")
					}
					nativeAssertCgroupDrained(t, ctx)
					t.Logf("scenario=worker_killed native_auto_contained=%t custody_retained_until_stop=%t recovered_after_observed_stop=true", automatic, retained)
					return
				}
				if target == "" {
					// Expiry renewal precedes bind; gate the later explicit refresh to observe the socket.
					oauth.blockNext.Store(true)
					oauth.release <- struct{}{}
					select {
					case <-oauth.requests:
					case <-ctx.Done():
						t.Fatal("qualification deadline after initialization renewal")
					case <-time.After(10 * time.Second):
						t.Fatal("explicit native renewal was not observed")
					}
					target, err = codexSocketPath(socket)
					if err != nil {
						t.Fatalf("native socket absent after initialization: %v", err)
					}
				}
				oauth.release <- struct{}{}
				result := nativeWaitRefresh(t, ctx, ref, socket, &target)
				after, afterOwner := nativeGrantState(t, store, id, home)
				if afterOwner != owner {
					t.Fatal("native refresh changed canonical owner")
				}
				if scenario == "revoked" {
					if result.Phase != PhaseFailed || after != before || nativeAuthDigest(t, home) != authBefore {
						t.Fatal("revoked refresh was ready or changed auth generation")
					}
				} else {
					if result.Phase != PhaseReady || after == before {
						t.Fatalf("native renewal not proven: phase=%s code=%s", result.Phase, result.ErrorCode)
					}
					lease, e := store.AcquireCodexGrant(id)
					if e != nil {
						t.Fatal(e)
					}
					access, e := lease.Access(time.Now())
					closeErr := lease.Close()
					if e != nil || closeErr != nil || access.AccountID != "synthetic-a" || !access.ExpiresAt.After(time.Now()) {
						t.Fatal("fresh access credential unavailable")
					}
				}
				if scenario == "partial_renewal" {
					raw, e := os.ReadFile(filepath.Join(home, "auth.json"))
					var auth struct {
						Tokens struct {
							ID string `json:"id_token"`
						} `json:"tokens"`
					}
					if e != nil || json.Unmarshal(raw, &auth) != nil || auth.Tokens.ID != nativeRefreshJWT(expiry, 0) {
						t.Fatal("native partial renewal did not preserve omitted expired ID token")
					}
				}
				if target == "" {
					t.Fatal("native physical socket was never observed")
				}
				if _, e := os.Lstat(target); !errors.Is(e, os.ErrNotExist) {
					t.Fatalf("native physical socket remains after stop (cycle=%d)", cycle)
				}
				for _, name := range []string{"home", "tmp", "n", refreshScratchMarker} {
					if _, e := os.Lstat(filepath.Join(dir, "logins", "refresh", id, name)); !errors.Is(e, os.ErrNotExist) {
						t.Fatalf("refresh scratch retained: %s", name)
					}
				}
				nativeAssertCgroupDrained(t, ctx)
				before = after
				t.Logf("scenario=%s cycle=%d phase=%s writers_stopped=%t physical_socket_removed=true generation_changed=%t", scenario, cycle, result.Phase, result.WritersStopped, scenario != "revoked")
			}
			// Native initialization may renew an expired grant before explicit account/read.
			if oauth.calls.Load() < int32(cycles) || oauth.calls.Load() > 2*int32(cycles) {
				t.Fatalf("unexpected OAuth request count: %d", oauth.calls.Load())
			}
		})
	}
}

type nativeOAuth struct {
	server    *httptest.Server
	requests  chan struct{}
	release   chan struct{}
	calls     atomic.Int32
	blockNext atomic.Bool
}

func newNativeOAuth(t *testing.T, ctx context.Context, scenario string) *nativeOAuth {
	t.Helper()
	o := &nativeOAuth{requests: make(chan struct{}, 8), release: make(chan struct{}, 8)}
	o.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && !r.URL.IsAbs() && strings.Contains(r.URL.Path, "accounts") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"accounts": []map[string]string{{"id": "synthetic-a", "workspace_backend_origin": strings.Replace(o.server.URL, "http:", "https:", 1), "account_routing_override": "NO_CONSTRAINT"}}})
			return
		}
		if r.Method != "POST" || r.URL.Path != "/oauth/token" {
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		count := o.calls.Add(1)
		if count > 8 {
			http.Error(w, "request limit", http.StatusTooManyRequests)
			return
		}
		raw, e := io.ReadAll(io.LimitReader(r.Body, 4097))
		var payload struct {
			Grant   string `json:"grant_type"`
			Refresh string `json:"refresh_token"`
		}
		if e != nil || len(raw) > 4096 || json.Unmarshal(raw, &payload) != nil || payload.Grant != "refresh_token" || !strings.HasPrefix(payload.Refresh, "synthetic-") {
			http.Error(w, "invalid synthetic request", 400)
			return
		}
		expectedRefresh := "synthetic-original"
		if count > 1 {
			expectedRefresh = fmt.Sprintf("synthetic-renewed-%d", count-1)
		}
		if payload.Refresh != expectedRefresh {
			http.Error(w, "synthetic refresh generation mismatch", 400)
			return
		}
		if o.blockNext.Swap(false) {
			o.requests <- struct{}{}
			select {
			case <-o.release:
			case <-r.Context().Done():
				return
			case <-ctx.Done():
				return
			case <-time.After(14 * time.Second):
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if scenario == "revoked" {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, `{"error":{"code":"refresh_token_invalidated"}}`)
			return
		}
		token := nativeRefreshJWT(time.Now().Add(2*time.Hour), count)
		response := map[string]string{"access_token": token, "refresh_token": fmt.Sprintf("synthetic-renewed-%d", count)}
		if scenario != "partial_renewal" {
			response["id_token"] = token
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	o.server.Config.ReadHeaderTimeout = 3 * time.Second
	o.server.Config.ReadTimeout = 5 * time.Second
	o.server.Config.WriteTimeout = 16 * time.Second
	o.server.Start()
	return o
}
func nativeRefreshJWT(expiry time.Time, generation int32) string {
	raw, _ := json.Marshal(map[string]any{"exp": expiry.Unix(), "generation": generation, "email": "synthetic-a@example.invalid", "https://api.openai.com/auth": map[string]string{"chatgpt_user_id": "synthetic-a", "chatgpt_account_id": "synthetic-a", "chatgpt_plan_type": "pro"}})
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(raw) + ".synthetic"
}
func nativePinnedBinary(t *testing.T, pathKey, shaKey string) string {
	t.Helper()
	path := os.Getenv(pathKey)
	expected := os.Getenv(shaKey)
	if !filepath.IsAbs(path) || len(expected) != 64 {
		t.Fatal("absolute binary and exact SHA256 pin required")
	}
	file, e := os.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer file.Close()
	hash := sha256.New()
	if _, e = io.Copy(hash, file); e != nil {
		t.Fatal(e)
	}
	if hex.EncodeToString(hash.Sum(nil)) != expected {
		t.Fatal("binary SHA256 pin mismatch")
	}
	return path
}
func nativeWaitDead(t *testing.T, ctx context.Context, p ProcessIdentity) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for p.Alive() {
		if time.Now().After(deadline) {
			t.Fatal("owned process did not stop")
		}
		select {
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("qualification deadline while awaiting owned process stop")
		}
	}
}
func nativeWaitRefresh(t *testing.T, ctx context.Context, ref RefreshRef, socket string, target *string) RefreshProgress {
	t.Helper()
	deadline := time.Now().Add(14 * time.Second)
	for {
		if path, e := codexSocketPath(socket); e == nil {
			*target = path
		}
		p, e := ReadCodexRefresh(ref)
		if e != nil {
			t.Fatal(e)
		}
		if !ref.Worker.Alive() {
			if !p.WritersStopped {
				t.Fatalf("worker stopped without writer containment: phase=%s code=%s", p.Phase, p.ErrorCode)
			}
			return p
		}
		if time.Now().After(deadline) {
			t.Fatal("native refresh did not finish")
		}
		select {
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("qualification deadline while awaiting refresh")
		}
	}
}
func nativeGrantState(t *testing.T, s *logins.Store, id, home string) ([32]byte, string) {
	t.Helper()
	lease, e := s.AcquireCodexGrant(id)
	if e != nil {
		t.Fatal(e)
	}
	generation, e := lease.Generation()
	closeErr := lease.Close()
	if e != nil || closeErr != nil {
		t.Fatal("canonical generation unavailable")
	}
	raw, e := os.ReadFile(filepath.Join(home, "auth.json"))
	if e != nil {
		t.Fatal(e)
	}
	owner, e := logins.CodexGrantOwner(raw)
	if e != nil {
		t.Fatal(e)
	}
	return generation, owner
}
func nativeGrantFixture(t *testing.T, state string, expiry time.Time) (*logins.Store, string, string) {
	t.Helper()
	s, e := logins.Open(state)
	if e != nil {
		t.Fatal(e)
	}
	token := nativeRefreshJWT(expiry, 0)
	auth, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"access_token": token, "id_token": token, "account_id": "synthetic-a", "refresh_token": "synthetic-original"}, "last_refresh": time.Now().UTC().Format(time.RFC3339)})
	r, e := s.Snapshot()
	if e != nil {
		t.Fatal(e)
	}
	_, login, e := s.Add(r.Revision, logins.ProviderCodex, "synthetic", logins.Credential{Auth: auth})
	if e != nil {
		t.Fatal(e)
	}
	r, e = s.Snapshot()
	if e != nil {
		t.Fatal(e)
	}
	op, e := s.BeginSwitch(r.Revision, logins.ProviderCodex, login.ID, "native-qualification", nil)
	if e != nil {
		t.Fatal(e)
	}
	finish := func(migration bool) {
		t.Helper()
		for _, phase := range []string{"stopping", "stopped"} {
			op.Phase = phase
			if e = s.SaveSwitch(op); e != nil {
				t.Fatal(e)
			}
		}
		if e = s.CaptureOutgoing(op.ID); e != nil {
			t.Fatal(e)
		}
		if migration {
			e = s.MigrateCodexOwnership(op.ID)
		} else {
			e = s.InstallIncoming(op.ID)
		}
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.CommitSwitch(op.ID); e != nil {
			t.Fatal(e)
		}
		if e = s.CompleteSwitch(op.ID); e != nil {
			t.Fatal(e)
		}
	}
	finish(false)
	r, e = s.Snapshot()
	if e != nil {
		t.Fatal(e)
	}
	op, e = s.BeginCodexOwnershipMigration(r.Revision, nil)
	if e != nil {
		t.Fatal(e)
	}
	finish(true)
	lease, e := s.AcquireCodexGrant(login.ID)
	if e != nil {
		t.Fatal(e)
	}
	home := lease.Home()
	if e = lease.Close(); e != nil {
		t.Fatal(e)
	}
	return s, login.ID, home
}

// Cleanup signals only identity-fenced owned workers and writers advertising this private home.
func nativeCleanupWorker(ref RefreshRef, home string) bool {
	p, _ := ReadCodexRefresh(ref)
	if ref.Worker.Alive() {
		_ = processcontain.SignalIdentity(ref.Worker, syscall.SIGTERM)
	}
	deadline := time.Now().Add(time.Second)
	for ref.Worker.Alive() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if ref.Worker.Alive() {
		_ = processcontain.SignalIdentity(ref.Worker, syscall.SIGKILL)
	}
	if p.Native.Alive() {
		_ = processcontain.SignalIdentity(p.Native, syscall.SIGKILL)
	}
	writers, e := processcontain.EnvironmentWriters([]string{home}, nil)
	if e != nil {
		return false
	}
	for _, writer := range writers {
		_ = processcontain.SignalIdentity(writer, syscall.SIGKILL)
	}
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		writers, e = processcontain.EnvironmentWriters([]string{home}, nil)
		if e == nil && len(writers) == 0 && !ref.Worker.Alive() && !p.Native.Alive() && nativeCgroupDrained() {
			// Uncaptured external sockets remain for inspection; a pathname is not ownership proof.
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func nativeCgroupDrained() bool {
	raw, e := os.ReadFile("/proc/self/cgroup")
	if e != nil {
		return false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "0::/") {
			continue
		}
		path := filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(strings.TrimPrefix(line, "0::"), "/"), "cgroup.procs")
		tasks, e := os.ReadFile(path)
		if e != nil || len(tasks) > 4096 {
			return false
		}
		for _, field := range strings.Fields(string(tasks)) {
			pid, e := strconv.Atoi(field)
			if e != nil || pid != os.Getpid() {
				return false
			}
		}
		return true
	}
	return false
}
func nativeAssertCgroupDrained(t *testing.T, ctx context.Context) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !nativeCgroupDrained() {
		if time.Now().After(deadline) {
			t.Fatal("unique qualification cgroup retains native tasks after worker stop")
		}
		select {
		case <-ctx.Done():
			t.Fatal("qualification deadline while checking cgroup containment")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func nativeAuthDigest(t *testing.T, home string) [32]byte {
	t.Helper()
	raw, e := os.ReadFile(filepath.Join(home, "auth.json"))
	if e != nil {
		t.Fatal("canonical auth unavailable")
	}
	return sha256.Sum256(raw)
}
