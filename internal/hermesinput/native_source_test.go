package hermesinput

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
)

// This opt-in test runs the real native CLI and owner against a loopback model.
// It requires no production Hermes home, provider key, Discord token, or paid call.
func TestNativeSourcePermissionAndOwnedLifecycle(t *testing.T) {
	program := os.Getenv("SWARM_HERMES_NATIVE_BINARY")
	if program == "" {
		t.Skip("set SWARM_HERMES_NATIVE_BINARY to an isolated Hermes >=0.22.5 executable")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "approval-target")
	if err := os.WriteFile(target, []byte("must survive"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"data":[{"id":"swarm-native-fixture","object":"model"}]}`))
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		message := map[string]any{"role": "assistant", "content": "NATIVE_FIXTURE_ACK"}
		finish := "stop"
		tools, hasTools := body["tools"].([]any)
		if hasTools && len(tools) > 0 && calls.Add(1) == 1 {
			args, _ := json.Marshal(map[string]string{"command": "rm -f " + target})
			message = map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": "native-tool-1", "type": "function", "function": map[string]string{"name": "terminal", "arguments": string(args)}}}}
			finish = "tool_calls"
		}
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			if tools, ok := message["tool_calls"].([]any); ok {
				tools[0].(map[string]any)["index"] = 0
			}
			for _, pair := range []struct {
				delta  any
				finish any
			}{{message, nil}, {map[string]any{}, finish}} {
				frame, _ := json.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "created": 1, "model": "swarm-native-fixture", "choices": []any{map[string]any{"index": 0, "delta": pair.delta, "finish_reason": pair.finish}}})
				_, _ = fmt.Fprintf(w, "data: %s\n\n", frame)
			}
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "object": "chat.completion", "created": 1, "model": "swarm-native-fixture", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}, "usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}})
	}))
	defer model.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	start := func(name string) *Owner {
		t.Helper()
		home := filepath.Join(dir, name)
		if err := os.Mkdir(home, 0700); err != nil {
			t.Fatal(err)
		}
		config := map[string]any{"model": map[string]any{"provider": "custom", "default": "swarm-native-fixture", "base_url": model.URL + "/v1"}, "approvals": map[string]any{"mode": "manual", "timeout": 60}, "auxiliary": map[string]any{"title_generation": map[string]any{"enabled": false}}, "telemetry": map[string]any{"shared_metrics": map[string]any{"enabled": false, "send": false, "offer_version": 2}}}
		raw, _ := json.Marshal(config)
		if err := os.WriteFile(filepath.Join(home, "config.yaml"), raw, 0600); err != nil {
			t.Fatal(err)
		}
		log, err := os.Create(filepath.Join(dir, name+".log"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = log.Close() })
		env := []string{"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8", "OPENAI_API_KEY=loopback-only", "OPENAI_BASE_URL=" + model.URL + "/v1", "XDG_STATE_HOME=" + filepath.Join(home, "xdg-state")}
		if source := os.Getenv("SWARM_HERMES_NATIVE_SOURCE"); source != "" {
			env = append(env, "PYTHONPATH="+source)
		}
		owner, err := Start(ctx, OwnerConfig{Argv: GatewayArgv(program), Home: home, Cwd: dir, Env: env, Log: log, RequestID: name, Launch: map[string]any{"toolsets": []string{"terminal"}}, Grace: 5 * time.Second})
		if err != nil {
			raw, _ := os.ReadFile(filepath.Join(dir, name+".log"))
			t.Fatalf("native start: %v\n%s", err, raw)
		}
		t.Cleanup(owner.Stop)
		return owner
	}
	owner := start("owned")
	other := start("unrelated")
	stopViewer := func() {}
	var viewerText func() string
	if tuiDir := os.Getenv("SWARM_HERMES_NATIVE_TUI_DIR"); tuiDir != "" {
		argv, err := ViewerArgv(program, owner.Binding.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		viewer := exec.Command(argv[0], argv[1:]...)
		viewer.Dir = dir
		viewer.Env = append(append([]string(nil), owner.cmd.Env...), "TERM=xterm-256color", "HERMES_TUI_DIR="+tuiDir, "HERMES_NODE="+os.Getenv("SWARM_HERMES_NATIVE_NODE"))
		terminal, err := pty.StartWithSize(viewer, &pty.Winsize{Rows: 45, Cols: 160})
		if err != nil {
			t.Fatal(err)
		}
		var mu sync.Mutex
		var output []byte
		go func() {
			buffer := make([]byte, 8192)
			for {
				n, err := terminal.Read(buffer)
				if n > 0 {
					mu.Lock()
					output = append(output, buffer[:n]...)
					if len(output) > 1024*1024 {
						output = output[len(output)-1024*1024:]
					}
					mu.Unlock()
				}
				if err != nil {
					return
				}
			}
		}()
		viewerText = func() string { mu.Lock(); defer mu.Unlock(); return ansi.Strip(string(output)) }
		var once sync.Once
		stopViewer = func() {
			once.Do(func() {
				_ = syscall.Kill(-viewer.Process.Pid, syscall.SIGTERM)
				done := make(chan struct{})
				go func() { _ = viewer.Wait(); close(done) }()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
				}
				_ = syscall.Kill(-viewer.Process.Pid, syscall.SIGKILL)
				<-done
				_ = terminal.Close()
			})
		}
		t.Cleanup(stopViewer)
	}
	c, err := connect(ctx, owner.Binding)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.CloseNow() }()
	var snap struct {
		Generation int64 `json:"execution_generation"`
	}
	if err := rpc(ctx, c, 1, "session.resume", map[string]string{"session_id": owner.Binding.SessionID}, &snap); err != nil {
		t.Fatal(err)
	}
	first, err := Submit(ctx, owner.Binding, "first-native-prompt", "Run the fixture terminal tool")
	if err != nil {
		t.Fatal(err)
	}
	var pending struct {
		Approvals []struct {
			ID string `json:"request_id"`
		} `json:"approvals"`
	}
	for {
		if err := rpc(ctx, c, 2, "approval.pending", map[string]string{"session_id": owner.Binding.SessionID}, &pending); err != nil {
			t.Fatal(err)
		}
		if len(pending.Approvals) == 1 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("no native approval", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	permission := pending.Approvals[0].ID
	if viewerText != nil {
		deadline := time.Now().Add(15 * time.Second)
		for !strings.Contains(viewerText(), "Run the fixture terminal tool") || !strings.Contains(viewerText(), "rm -f") {
			if time.Now().After(deadline) {
				t.Fatalf("native TUI did not show external prompt and tool approval:\n%s", viewerText())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	second, err := Submit(ctx, owner.Binding, "during-native-permission", "/approve all")
	if err != nil {
		t.Fatal(err)
	}
	if err := rpc(ctx, c, 3, "approval.pending", map[string]string{"session_id": owner.Binding.SessionID}, &pending); err != nil {
		t.Fatal(err)
	}
	if len(pending.Approvals) != 1 || pending.Approvals[0].ID != permission {
		t.Fatalf("external message changed native permission: %#v", pending)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("message accepted native tool permission: %v", err)
	}
	if viewerText != nil && !strings.Contains(viewerText(), "rm -f") {
		t.Fatal("native TUI approval disappeared after prompt ingress")
	}
	if err := rpc(ctx, c, 4, "session.resume", map[string]string{"session_id": owner.Binding.SessionID}, &snap); err != nil {
		t.Fatal(err)
	}
	var decision map[string]any
	if err := rpc(ctx, c, 5, "approval.respond", map[string]any{"session_id": owner.Binding.SessionID, "execution_generation": snap.Generation, "prompt_id": permission, "choice": "deny"}, &decision); err != nil {
		t.Fatal(err)
	}
	stopViewer()
	owner.Stop()
	if current, err := procstart.StartTime(owner.Binding.OwnerPID); err == nil && current == owner.Binding.OwnerStartTime {
		t.Fatal("owned native gateway survived stop")
	}
	if current, err := procstart.StartTime(other.Binding.OwnerPID); err != nil || current != other.Binding.OwnerStartTime {
		t.Fatalf("unrelated native gateway stopped: %v", err)
	}
	if _, err := Submit(ctx, other.Binding, "other-still-runs", "Independent owner remains usable"); err != nil {
		t.Fatal(err)
	}
	t.Logf("native proof: owner_pid=%d other_pid=%d instance=%s session=%s permission=%s first=%+v second=%+v permission_not_answered=true native_tui_external_prompt_and_permission=%t owned_stop=true unrelated_owner_usable=true", owner.Binding.OwnerPID, other.Binding.OwnerPID, owner.Binding.InstanceID, owner.Binding.SessionID, permission, first, second, viewerText != nil)
}
