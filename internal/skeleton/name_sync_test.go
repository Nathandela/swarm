package skeleton

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/codex"
)

func awaitNameSetCall(t *testing.T, backend *r7FakeBackend, wantName string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, call := range backend.recorded() {
			if call.Method != "thread/name/set" {
				continue
			}
			var params map[string]any
			if json.Unmarshal(call.Params, &params) == nil && params["name"] == wantName {
				return params
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("backend never received thread/name/set for %q; calls = %+v", wantName, backend.recorded())
	return nil
}

func nameSyncRig(t *testing.T) (*Daemon, string, *r7FakeBackend) {
	t.Helper()
	sk := assemble(t)
	sk.setAdapterForTest(func(string) (adapter.Adapter, bool) { return codex.New(), true })
	sk.api.syncName = func(id, name string) { go sk.syncSessionNameToProvider(id, name) }
	m := launchFake(t, sk, r7StdinScript)
	backend := newR7FakeBackend()
	sk.registerBackend(m.ID, r7NativeThreadID, backend)
	return sk, m.ID, backend
}

func TestCodexBackendJoinReceivesExistingSwarmSessionName(t *testing.T) {
	sk := assemble(t)
	sk.setAdapterForTest(func(string) (adapter.Adapter, bool) { return codex.New(), true })
	m := launchFake(t, sk, r7StdinScript)
	if err := sk.core.Rename(m.ID, "launch name"); err != nil {
		t.Fatalf("seed session name: %v", err)
	}
	backend := newR7FakeBackend()
	sk.registerBackend(m.ID, r7NativeThreadID, backend)

	params := awaitNameSetCall(t, backend, "launch name")
	if params["threadId"] != r7NativeThreadID {
		t.Fatalf("threadId = %v, want %q", params["threadId"], r7NativeThreadID)
	}
}

func TestSwarmRenameIsSentToLiveCodexThread(t *testing.T) {
	sk, local, backend := nameSyncRig(t)
	if err := sk.api.Rename(local, "swarm rename"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	awaitNameSetCall(t, backend, "swarm rename")
}

func TestCodexRenameNotificationUpdatesSwarmWithoutEcho(t *testing.T) {
	sk, local, backend := nameSyncRig(t)
	before := len(backend.recorded())
	frame := []byte(`{"method":"thread/name/updated","params":{"threadId":"` + r7NativeThreadID + `","threadName":"native\nrename"}}`)
	sk.ingestBackendFrame(local, frame, time.Now().UnixMilli())

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m, ok := sk.core.Get(local); ok && m.Name == "nativerename" {
			if got := len(backend.recorded()); got != before {
				t.Fatalf("provider-originated rename echoed back to Codex: calls before=%d after=%d", before, got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	m, _ := sk.core.Get(local)
	t.Fatalf("Swarm name = %q, want sanitized provider rename", m.Name)
}
