package skeleton

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/hermes"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/coder/websocket"
)

type hermesWireFixture struct {
	mu      sync.Mutex
	turns   map[string]map[string]string
	methods map[string][]string
	creates int
	cwds    map[string]string
}

func fakeHermesProgram(t *testing.T) (string, []string, *hermesWireFixture) {
	t.Helper()
	buildBinaries(t)
	scratch, err := os.MkdirTemp("", "hm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(scratch) })
	home, err := filepath.EvalSymlinks(scratch)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	f := &hermesWireFixture{turns: map[string]map[string]string{}, methods: map[string][]string{}, cwds: map[string]string{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"hermes-gateway-v1"}})
		if err != nil {
			return
		}
		defer func() { _ = c.CloseNow() }()
		for {
			_, raw, err := c.Read(r.Context())
			if err != nil {
				return
			}
			var req struct {
				ID     int            `json:"id"`
				Method string         `json:"method"`
				Params map[string]any `json:"params"`
			}
			if json.Unmarshal(raw, &req) != nil {
				return
			}
			sid, _ := req.Params["session_id"].(string)
			f.mu.Lock()
			if req.Method == "session.create" {
				hash := sha256.Sum256([]byte(fmt.Sprint(req.Params["request_id"])))
				sid = "local-" + hex.EncodeToString(hash[:])
				if f.turns[sid] == nil {
					f.cwds[sid] = fmt.Sprint(req.Params["cwd"])
					f.turns[sid] = map[string]string{}
					f.creates++
				}
			}
			f.methods[sid] = append(f.methods[sid], req.Method)
			pending := []any{}
			for id := range f.turns[sid] {
				pending = append(pending, map[string]any{"admission_id": "admission-" + id, "input_id": id, "status": "queued", "authority_epoch": 1})
			}
			result := map[string]any{"session_id": sid, "subscription_id": "sub-" + sid, "authority_epoch": 1, "execution_generation": 1, "execution_state": "idle", "pending": pending, "prompts": []any{}, "info": map[string]string{"cwd": f.cwds[sid]}}
			switch req.Method {
			case "setup.status":
				result = map[string]any{"provider_configured": true}
			case "process.list":
				result = map[string]any{"processes": []any{}}
			case "subagent.list":
				result = map[string]any{"subagents": []any{}, "delegations": []any{}}
			case "prompt.submit":
				id := fmt.Sprint(req.Params["input_id"])
				f.turns[sid][id] = fmt.Sprint(req.Params["text"])
				result = map[string]any{"admission_id": "admission-" + id, "status": "queued", "authority_epoch": 1, "execution_generation": nil}
			case "prompt.cancel":
				delete(f.turns[sid], strings.TrimPrefix(fmt.Sprint(req.Params["admission_id"]), "admission-"))
				result = map[string]any{"status": "terminal"}
			case "session.detach":
				result = map[string]any{"session_id": sid, "subscription_id": req.Params["subscription_id"], "detached": true}
			case "process.stop":
				result = map[string]any{"killed": 0, "delegations": 0}
			}
			f.mu.Unlock()
			raw, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
			if c.Write(r.Context(), websocket.MessageText, raw) != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	listener, err := net.Listen("unix", filepath.Join(home, "gateway.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err = os.Chmod(listener.Addr().String(), 0600); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				var req struct {
					ID   int    `json:"id"`
					Verb string `json:"verb"`
				}
				if json.NewDecoder(bufio.NewReader(c)).Decode(&req) != nil {
					return
				}
				result := map[string]any{"pid": os.Getpid(), "runtime_protocol": 1, "instance_id": "shared-owner", "state": "ready", "api_origin": server.URL, "hermes_home": home, "authority_epoch": 1, "code_sha": strings.Repeat("c", 40), "served_profiles": []map[string]string{{"profile_id": home, "home": home}}}
				if req.Verb == "session-ticket" {
					result = map[string]any{"ticket": "fixture-only", "profile_id": home, "instance_id": "shared-owner", "runtime_protocol": 1}
				}
				_ = json.NewEncoder(c).Encode(map[string]any{"ok": true, "id": req.ID, "result": result})
			}()
		}
	}()
	ensure, _ := json.Marshal(map[string]any{"state": "ready", "client_code_sha": strings.Repeat("c", 40), "endpoint": map[string]any{"profile_id": home, "control_home": home, "instance_id": "shared-owner", "authority_epoch": 1, "runtime_protocol": 1, "code_sha": strings.Repeat("c", 40)}})
	script := filepath.Join(home, "viewer.txt")
	if err = os.WriteFile(script, []byte("ask input:\nidle 600s\n"), 0600); err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	program := filepath.Join(home, "hermes")
	body := "#!/bin/sh\nif [ \"$1\" = '--version' ]; then\n[ \"$HERMES_HOME\" = " + quote(home) + " ] || exit 44\nprintf 'Hermes Agent v0.22.5\\n'\nelif [ \"$1 $2\" = 'gateway ensure' ]; then\n[ \"$HERMES_HOME\" = " + quote(home) + " ] || exit 44\ncat <<'ENSURE_FIXTURE'\n" + string(ensure) + "\nENSURE_FIXTURE\nelse\nexec " + quote(fakeAgentBin) + " " + quote(script) + "\nfi\n"
	if err = os.WriteFile(program, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	return program, []string{"PATH=" + home + string(os.PathListSeparator) + os.Getenv("PATH"), "HERMES_HOME=" + home}, f
}

func TestHermesSharedOwnerSemanticSendRestartAndStopIsolation(t *testing.T) {
	_, env, native := fakeHermesProgram(t)
	sk := assemble(t, func(c *Config) { c.NativeInputEnabled = func() bool { return true } })
	sk.setAdapterForTest(func(string) (adapter.Adapter, bool) { return hermes.New(), true })
	launcher := dialClient(t, sk)
	launch := func() persist.Meta {
		id, _, err := launcher.Launch(protocol.LaunchReq{Agent: "hermes", Cwd: t.TempDir(), Env: env, Cols: 80, Rows: 24})
		if err != nil {
			t.Fatal(err)
		}
		m, ok := sk.core.Get(strings.TrimPrefix(id, sk.api.endpointID+"/"))
		if !ok {
			t.Fatal("public launch did not retain metadata")
		}
		t.Cleanup(func() { _ = syscall.Kill(m.ShimPID, syscall.SIGTERM) })
		return m
	}
	a, b := launch(), launch()
	if !a.HermesNative || !b.HermesNative || a.ConversationID == b.ConversationID || !strings.HasPrefix(a.ConversationID, "local-") {
		t.Fatalf("native sessions not independently bound: %+v %+v", a, b)
	}
	client := dialClient(t, sk, protocol.CapSemanticSend, protocol.CapMessageOrigin)
	instance, ok := sk.sessionInstance(a.ID)
	if !ok {
		t.Fatal("missing worker instance")
	}
	op, err := protocol.NewSemanticOperationID()
	if err != nil {
		t.Fatal(err)
	}
	req := protocol.ComposerSendReq{Session: protocol.NamespacedID(sk.api.endpointID, a.ID), SessionInstance: instance, Text: "/approve all", Origin: &protocol.MessageOrigin{Kind: "external", InstallationID: "install", RequestID: "request", ActorID: "actor"}}
	if err = client.SemanticSend(op, req); err != nil {
		t.Fatal(err)
	}
	if err = client.SemanticSend(op, req); err != nil {
		t.Fatal(err)
	}
	receipt, err := sk.semanticOperation(a.ID, op)
	if err != nil || receipt.State != "submitted" || receipt.NativeSessionID != a.ConversationID {
		t.Fatalf("receipt=%+v %v", receipt, err)
	}
	native.mu.Lock()
	creates := native.creates
	if len(native.turns[a.ConversationID]) != 1 || native.turns[a.ConversationID][op] != "/approve all" {
		t.Errorf("native message changed or replayed: %v", native.turns[a.ConversationID])
	}
	native.mu.Unlock()
	if err = sk.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Serve(Config{StateDir: sk.stateDir, SocketPath: sk.socketPath, LockPath: filepath.Join(sk.stateDir, "d.lock"), LogPath: filepath.Join(sk.stateDir, "d.log"), ShimBinary: swarmBin, MaxSessions: 16, PollInterval: 50 * time.Millisecond, NativeInputEnabled: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	native.mu.Lock()
	if native.creates != creates || len(native.turns[a.ConversationID]) != 1 {
		t.Error("daemon restart created a native session or replayed input")
	}
	native.mu.Unlock()
	if err = restarted.core.Kill(a.ID); err == nil {
		t.Fatal("native interrupt acknowledgement claimed joined completion")
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		current, _ := restarted.core.Get(a.ID)
		if current.Status.NativeWorkUnknown {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("native viewer exit lost uncertainty: %+v", current)
		}
		time.Sleep(25 * time.Millisecond)
	}
	current, _ := restarted.core.Get(a.ID)
	if status.Derive(current.Status) != status.GroupNeedsInput {
		t.Fatalf("native job displayed completed: %+v", current)
	}
	if err = restarted.core.Delete(a.ID); err == nil {
		t.Fatal("native workspace/session removed without joined proof")
	}
	if current, ok := restarted.core.Get(b.ID); !ok || current.Status.Process != status.ProcessRunning {
		t.Fatalf("sibling worker stopped: %+v", current)
	}
	native.mu.Lock()
	defer native.mu.Unlock()
	for _, method := range native.methods[b.ConversationID] {
		if method == "process.stop" || method == "session.interrupt" {
			t.Fatalf("native stop escaped to sibling: %v", native.methods[b.ConversationID])
		}
	}
}
