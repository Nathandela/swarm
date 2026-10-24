package loginworker

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/coder/websocket"
)

func refreshFixtureCredential(expiry int64) []byte {
	claims, _ := json.Marshal(map[string]any{"exp": expiry, "https://api.openai.com/auth": map[string]string{"chatgpt_user_id": "u", "chatgpt_account_id": "a"}})
	token := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".synthetic"
	raw, _ := json.Marshal(map[string]any{"auth_mode": "chatgpt", "tokens": map[string]string{"access_token": token, "id_token": token, "refresh_token": "synthetic-refresh", "account_id": "a"}})
	return raw
}

// This fresh process implements only initialize and native account/read. No
// model endpoint, provider network, user home, or real grant is involved.
func TestCodexRefreshNativeFixture(t *testing.T) {
	if !strings.Contains(strings.Join(os.Args, " "), "-test.run=^TestCodexRefreshNativeFixture$") {
		return
	}
	if os.Getenv("CODEX_HOME") == "" {
		t.Fatal("missing isolated auth home")
	}
	mode, _ := os.ReadFile("fixture-mode")
	if string(mode) == "startup-overflow" {
		f, e := os.Create(filepath.Join(os.Getenv("TMPDIR"), "overflow"))
		if e != nil {
			t.Fatal(e)
		}
		e = f.Truncate(maxRefreshScratchBytes + 1)
		_ = f.Close()
		if e != nil {
			t.Fatal(e)
		}
		time.Sleep(10 * time.Second)
		return
	}
	previous := syscall.Umask(0177)
	defer syscall.Umask(previous)
	ln, e := net.Listen("unix", "n")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = ln.Close() }()
	_ = os.Chmod("n", 0600)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, e := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if e != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		for {
			_, raw, e := conn.Read(context.Background())
			if e != nil {
				return
			}
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(raw, &req) != nil {
				return
			}
			var result any
			switch req.Method {
			case "initialize":
				result = map[string]string{"userAgent": "native-specific-banner"}
			case "initialized":
				continue
			case "account/read":
				var p map[string]bool
				_ = json.Unmarshal(req.Params, &p)
				if !p["refreshToken"] {
					return
				}
				mode, _ := os.ReadFile("fixture-mode")
				if string(mode) == "hang" {
					_ = os.WriteFile("fixture-account-read-entered", nil, 0600)
					<-r.Context().Done()
					return
				}
				if string(mode) == "reply-overflow" {
					file, e := os.Create(filepath.Join(os.Getenv("HOME"), "overflow"))
					if e != nil {
						return
					}
					_ = file.Truncate(maxRefreshScratchBytes + 1)
					_ = file.Close()
				}
				if string(mode) != "unchanged" {
					if e = os.WriteFile(filepath.Join(os.Getenv("CODEX_HOME"), "auth.json"), refreshFixtureCredential(time.Now().Add(2*time.Hour).Unix()), 0600); e != nil {
						return
					}
				}
				result = map[string]any{"account": map[string]string{"type": "chatgpt"}}
			default:
				return
			}
			reply, _ := json.Marshal(map[string]any{"id": req.ID, "result": result})
			if conn.Write(context.Background(), websocket.MessageText, reply) != nil {
				return
			}
		}
	})
	_ = http.Serve(ln, handler)
}

type fixtureRefreshGrant struct {
	home              string
	owner             string
	native            ProcessIdentity
	prepared, cleaned bool
}

func (g *fixtureRefreshGrant) Home() string { return g.home }
func (g *fixtureRefreshGrant) Generation() ([32]byte, error) {
	raw, e := os.ReadFile(filepath.Join(g.home, "auth.json"))
	return sha256.Sum256(raw), e
}
func (g *fixtureRefreshGrant) RecordNative(p processcontain.Identity) error { g.native = p; return nil }
func (g *fixtureRefreshGrant) PrepareAuxiliary(string) error                { g.prepared = true; return nil }
func (g *fixtureRefreshGrant) CheckAuxiliary() error                        { return nil }
func (g *fixtureRefreshGrant) CleanupAuxiliary() error                      { g.cleaned = true; return nil }
func (g *fixtureRefreshGrant) ProveStopped() error {
	if g.native.Alive() {
		return ErrUnavailable
	}
	return nil
}
func (g *fixtureRefreshGrant) ValidateRenewal(before [32]byte, now time.Time) error {
	after, e := g.Generation()
	if e != nil {
		return e
	}
	if after == before {
		return ErrInvalid
	}
	raw, e := os.ReadFile(filepath.Join(g.home, "auth.json"))
	if e != nil {
		return e
	}
	_, e = logins.CodexAccess(raw, g.owner, now)
	return e
}
func TestCodexRefreshNativeLifecycle(t *testing.T) {
	for _, mode := range []string{"renew", "unchanged", "hang", "startup-overflow", "reply-overflow"} {
		t.Run(mode, func(t *testing.T) {
			tmp, e := filepath.EvalSymlinks("/tmp")
			if e != nil {
				t.Fatal(e)
			}
			root, e := os.MkdirTemp(tmp, "swarm-r-")
			if e == nil {
				t.Cleanup(func() { _ = os.RemoveAll(root) })
			}
			if e != nil {
				t.Fatal(e)
			}
			s, e := logins.Open(root)
			if e != nil {
				t.Fatal(e)
			}
			_ = s.Close()
			id := "00112233445566778899aabbccddeeff"
			f, e := openRefreshJob(root, id, true)
			if e != nil {
				t.Fatal(e)
			}
			defer f.Close()
			home := filepath.Join(f.path, "canonical")
			if e = os.Mkdir(home, 0700); e != nil {
				t.Fatal(e)
			}
			raw := refreshFixtureCredential(time.Now().Add(-time.Hour).Unix())
			if e = os.WriteFile(filepath.Join(home, "auth.json"), raw, 0600); e != nil {
				t.Fatal(e)
			}
			owner, e := logins.CodexGrantOwner(raw)
			if e != nil {
				t.Fatal(e)
			}
			executable, e := os.Executable()
			if e != nil {
				t.Fatal(e)
			}
			native := newRefreshFixtureTransport(t, f.path, refreshFixtureTransport{Target: executable, Version: "0.163.0", Prefix: []string{"-test.run=^TestCodexRefreshNativeFixture$"}})
			if e = os.WriteFile(filepath.Join(f.path, "fixture-mode"), []byte(mode), 0600); e != nil {
				t.Fatal(e)
			}
			fingerprint, e := persist.CLIFingerprint(native)
			if e != nil {
				t.Fatal(e)
			}
			c := RefreshConfig{StateRoot: root, LoginID: id, Generation: 1, LaunchNonce: "00112233445566778899aabbccddeeff", NativePath: native, NativeVersion: "0.163.0", NativeFingerprint: fingerprint, Deadline: time.Now().Add(5 * time.Second)}
			grant := &fixtureRefreshGrant{home: home, owner: owner}
			timeout := 5 * time.Second
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			// This unit seam stubs only global process inventory. The fake native leader
			// is real and must be dead before returning; host containment is separate.
			inventory := func([]string, []string) ([]processcontain.Identity, error) { return nil, nil }
			run := func() error {
				return refreshCodexNative(ctx, c, f, grant, inventory, func(ProcessIdentity) error { return nil })
			}
			if mode == "hang" {
				done := make(chan error, 1)
				go func() { done <- run() }()
				entered := false
				deadline := time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) {
					if _, statErr := f.root.Lstat("fixture-account-read-entered"); statErr == nil {
						entered = true
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				cancel()
				e = <-done
				if !entered {
					t.Fatal("timeout fixture never entered actual native account/read")
				}
			} else {
				e = run()
			}
			if mode == "renew" && e != nil {
				t.Fatal(e)
			}
			if mode != "renew" && e == nil {
				t.Fatal("failed or unchanged refresh reported success")
			}
			if grant.native.Alive() {
				t.Fatal("returned with native child alive")
			}
			if !grant.prepared || !grant.cleaned {
				t.Fatal("native scratch lifetime not closed")
			}
			for _, name := range []string{"home", "tmp", refreshScratchMarker} {
				if _, e := f.root.Lstat(name); !os.IsNotExist(e) {
					t.Fatalf("helper scratch retained %s after stop: %v", name, e)
				}
			}
		})
	}
}

func TestCodexRefreshRejectsUnpublishedOrReboundLaunch(t *testing.T) {
	root, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	s, e := logins.Open(root)
	if e != nil {
		t.Fatal(e)
	}
	_ = s.Close()
	executable, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	executable, e = filepath.EvalSymlinks(executable)
	if e != nil {
		t.Fatal(e)
	}
	fingerprint, e := persist.CLIFingerprint(executable)
	if e != nil {
		t.Fatal(e)
	}
	id := "00112233445566778899aabbccddeeff"
	nonce := "ffeeddccbbaa99887766554433221100"
	f, e := openRefreshJob(root, id, true)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	c := RefreshConfig{StateRoot: root, LoginID: id, Generation: 1, LaunchNonce: nonce, NativePath: executable, NativeVersion: "0.163.0", NativeFingerprint: fingerprint, Deadline: time.Now().Add(5 * time.Second)}
	if e = f.write(refreshConfigFile, c); e != nil {
		t.Fatal(e)
	}
	t.Setenv(refreshLaunchEnv, nonce)
	// A daemon died before publishing this worker. The config alone cannot
	// authorize any native process even with a matching inherited launch nonce.
	if e = RunCodexRefreshConfig(context.Background(), filepath.Join(f.path, refreshConfigFile)); e != ErrStale {
		t.Fatalf("unpublished launch accepted: %v", e)
	}
	c.LaunchNonce = "00110011001100110011001100110011"
	if e = f.write(refreshConfigFile, c); e != nil {
		t.Fatal(e)
	}
	if e = RunCodexRefreshConfig(context.Background(), filepath.Join(f.path, refreshConfigFile)); e != ErrStale {
		t.Fatalf("old worker adopted rebound config: %v", e)
	}
}
