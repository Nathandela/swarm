package shim

import (
	"encoding/json"
	"github.com/Nathandela/swarm/internal/shimwire"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagerRequiredBackendNeverStartsStandaloneAgent(t *testing.T) {
	for _, mode := range []string{r7BackendNeverBinds, r7BackendBind} {
		t.Run(mode, func(t *testing.T) {
			cfg := r7BackendCfg(t, mode, nil)
			cfg.Backend.ReadyTimeout = 100 * time.Millisecond
			cfg.Backend.GoAheadTimeout = 100 * time.Millisecond
			// An older decoder ignores this field; its standalone fallback must fail this test.
			if err := json.Unmarshal([]byte(`{"Required":true}`), cfg.Backend); err != nil {
				t.Fatal(err)
			}
			result := waitRun(t, runShimAsync(cfg), 15*time.Second)
			if result.err == nil {
				t.Fatal("required manager backend fell back to a standalone native agent")
			}
			body, _ := os.ReadFile(filepath.Join(cfg.SessionDir, TranscriptFile))
			if strings.Contains(string(body), "INFO_DONE") {
				t.Fatal("standalone native agent executed")
			}
			if info, known := ReadBackendInfo(cfg.SessionDir); known && r7Alive(info.PID) {
				t.Fatal("manager backend leaked after refusal")
			}
		})
	}
}

func TestManagerRequiredBackendStartsOnlyWithItsExactAttachment(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(map[bool]string{true: "owned", false: "different"}[valid], func(t *testing.T) {
			cfg := r7BackendCfg(t, r7BackendBind, nil)
			cfg.Backend.Required = true
			done := runShimAsync(cfg)
			r7WaitBackendInfo(t, cfg.SessionDir, 10*time.Second)
			client := dialShim(t, cfg.SocketPath)
			client.startReader()
			client.hello(shimwire.Version)
			endpoint := "unix://" + cfg.Backend.SocketPath
			if !valid {
				endpoint += "-different"
			}
			client.writeControl(shimwire.Control{Type: shimwire.TypeBackendAttach, AgentArgs: []string{"--remote", endpoint}})
			result := waitRun(t, done, 15*time.Second)
			_ = client.conn.Close()
			if valid && result.err != nil {
				t.Fatal(result.err)
			}
			if !valid && result.err == nil {
				t.Fatal("manager accepted another native backend")
			}
		})
	}
}
