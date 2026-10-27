package shim

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/claudeinput"
)

func TestClaudeNativeControlBrokerSpool(t *testing.T) {
	dir, err := os.MkdirTemp(os.TempDir(), "nc")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	cfg := Config{SessionDir: dir, SessionID: "session", RequiredRuntime: claudeinput.NativeControlRuntime, HookSocketPath: filepath.Join(dir, "hook.sock"), Env: []string{"SWARM_SESSION_ID=session", "SWARM_HOOK_TOKEN=" + strings.Repeat("a", 64)}}
	hooks, err := newSessionHookServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer hooks.shutdown()
	if _, err := hooks.spool.Append([]byte(`{"ordinary":"hook"}`)); err != nil {
		t.Fatal(err)
	}
	broker, err := claudeinput.StartNativeControl(dir, "session", "2.1.310", 123, 456, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = broker.Close() }()
	if err := attachClaudeControlSpool(cfg, broker, hooks); err != nil {
		t.Fatal(err)
	}
	desc := broker.Descriptor()
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", desc.SocketPath)
	}}}
	post := func(path string, value any) (int, []byte, error) {
		raw, _ := json.Marshal(value)
		req, _ := http.NewRequest("POST", "http://localhost"+path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+desc.Token)
		resp, err := client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		return resp.StatusCode, body, err
	}
	worker := claudeinput.Worker{NativeSessionID: "native", Generation: "guard", Version: "2.1.310"}
	status, _, err := post("/guard", map[string]any{"generation": desc.Generation, "worker": worker, "captured_sequence": 0, "queued": 0, "lost": false})
	if err != nil || status != 200 {
		t.Fatal(status, err)
	}
	old := hookSpoolSync
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	hookSpoolSync = func(f *os.File) error { once.Do(func() { close(entered) }); <-release; return old(f) }
	done := make(chan struct{})
	var statusCode int
	var body []byte
	var postErr error
	released := false
	defer func() {
		if !released {
			close(release)
		}
		<-done
		hookSpoolSync = old
	}()
	go func() {
		defer close(done)
		statusCode, body, postErr = post("/control", map[string]any{"generation": desc.Generation, "worker": worker, "sequence": 1, "invocation": 1, "event": "turn.step", "phase": "entered", "context": map[string]any{"turn_id": "turn", "index": 0, "message_count": 1, "origin": map[string]string{"plugin": "engine", "tier": "core"}}, "data": map[string]any{}})
	}()
	select {
	case <-entered:
	case <-done:
		t.Fatalf("no spool fsync: status=%d body=%s err=%v", statusCode, body, postErr)
	case <-time.After(time.Second):
		t.Fatal("did not enter fsync")
	}
	select {
	case <-done:
		t.Fatal("ACK before fsync")
	default:
	}
	srv := &server{claudeRetire: broker.Revoke}
	stopped := make(chan struct{})
	go func() { srv.onSignal("kill"); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop blocked behind durable append")
	}
	close(release)
	released = true
	<-done
	if postErr != nil || statusCode != 200 {
		t.Fatalf("pre-Stop fact failed retention: %d %s %v", statusCode, body, postErr)
	}
	if status, _, err := post("/guard", map[string]any{"generation": desc.Generation, "worker": worker, "captured_sequence": 1, "queued": 0, "lost": false}); err != nil || status == 200 {
		t.Fatalf("actual Stop path did not retire broker: %d %v", status, err)
	}
	records, _, _, err := hooks.spool.ReadFrom(0)
	if err != nil || len(records) != 2 || records[1].Seq != 2 {
		t.Fatalf("separate spool sequence not retained: %v %v", records, err)
	}
	var envelope claudeinput.ControlEnvelope
	if err := json.Unmarshal(records[1].Body, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Sequence != 1 || envelope.SessionID != "session" || envelope.Token != strings.Repeat("a", 64) || envelope.DescriptorGeneration != desc.Generation || envelope.Worker != worker {
		t.Fatal("wrong durable source/auth identity")
	}
	if strings.Contains(envelope.ReportHash, envelope.Token) || bytes.Contains(records[1].Body, []byte(desc.Token)) {
		t.Fatal("broker credential leaked into durable control envelope")
	}
}
