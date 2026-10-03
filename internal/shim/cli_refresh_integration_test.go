package shim

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shimwire"
)

// Gate the two real child execs with the actual backend-attach protocol, then
// replace their shared launcher between them. A mixed installation must never
// acquire the observation used to authorize the next automatic refresh.
func TestCLIRefreshDualProcessInstallationSwap(t *testing.T) {
	for _, swap := range []bool{false, true} {
		t.Run(fmt.Sprintf("swap=%v", swap), func(t *testing.T) {
			cfg := r7BackendCfg(t, r7BackendBind, nil)
			cfg.Backend.GoAheadTimeout = 10 * time.Second
			launcher := filepath.Join(cfg.SessionDir, "codex")
			install := func(version string) {
				t.Helper()
				body := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '%s' > '%s/process-'\"${SWARM_SHIM_TEST_BACKEND:-terminal}\"\nexec '%s' \"$@\"\n", version, cfg.SessionDir, selfExe(t))
				if err := os.WriteFile(launcher+".new", []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(launcher+".new", launcher); err != nil {
					t.Fatal(err)
				}
			}
			install("1.0.0")
			fingerprint, err := persist.CLIFingerprint(launcher)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Argv[0] = launcher
			cfg.Backend.Program = launcher
			cfg.CLIIdentity = &persist.CLIIdentity{Path: launcher, Version: "1.0.0", Fingerprint: fingerprint}
			done := runShimAsync(cfg)
			r7WaitBackendInfo(t, cfg.SessionDir, 10*time.Second)
			// Backend readiness proves its exec completed before the atomic replacement.
			if swap {
				install("1.0.1")
			}
			c := dialShim(t, cfg.SocketPath)
			c.startReader()
			c.hello(shimwire.Version)
			c.attach()
			c.writeControl(shimwire.Control{Type: shimwire.TypeBackendAttach})
			if c.waitObserved("INFO_DONE", 15*time.Second) == "" {
				t.Fatal("terminal never started")
			}
			_ = c.conn.Close()
			result := waitRun(t, done, 20*time.Second)
			if result.err != nil {
				t.Fatalf("shim: %v", result.err)
			}
			backend, _ := os.ReadFile(filepath.Join(cfg.SessionDir, "process-bind"))
			terminal, _ := os.ReadFile(filepath.Join(cfg.SessionDir, "process-terminal"))
			wantTerminal := "1.0.0"
			if swap {
				wantTerminal = "1.0.1"
			}
			if strings.TrimSpace(string(backend)) != "1.0.0" || strings.TrimSpace(string(terminal)) != wantTerminal {
				t.Fatalf("actual versions backend=%q terminal=%q", backend, terminal)
			}
			observed := ReadCLIObservation(cfg.SessionDir)
			if swap && observed != nil {
				t.Fatalf("mixed process versions were stamped as homogeneous: %+v", observed)
			}
			if !swap && (observed == nil || *observed != *cfg.CLIIdentity) {
				t.Fatalf("stable processes lost launch observation: %+v", observed)
			}
		})
	}
}
