//go:build linux

package skeleton

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/accountconfig"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/codex"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/procstart"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/wire"
)

type accountManualResumeFixture struct {
	manager        *accountManager
	source         persist.Meta
	native         shim.NativeProcessInfo
	profile, probe string
	finalized      bool
	accepted       daemon.LaunchSpec
	stop           error
}

func manualResumeFixture(t *testing.T) *accountManualResumeFixture {
	t.Helper()
	store, root, bindings := accountTestStore(t, 1)
	source := accountTestSource(root, bindings[0])
	source.Status.Process = status.ProcessLost
	source.ShimPID, source.ShimStartTime = 1<<30, 1
	source.LaunchOptions = map[string]string{"model": "gpt-native-fixture"}
	home, bin := filepath.Join(root, "isolated-home"), filepath.Join(root, "synthetic-bin")
	for _, dir := range []string{filepath.Join(home, ".codex"), bin} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	probe, native := filepath.Join(root, "probe-observed"), filepath.Join(bin, "codex")
	// This local synthetic executable only prints a pinned version; reaching it
	// is observable. No provider authentication or model request occurs.
	raw := fmt.Sprintf("#!/bin/sh\nprintf 'probe\\n' >> %q\nprintf 'codex-cli 0.160.0\\n'\n", probe)
	if err := os.WriteFile(native, []byte(raw), 0o700); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := persist.CLIFingerprint(native)
	if err != nil {
		t.Fatal(err)
	}
	source.CLIIdentity = &persist.CLIIdentity{Path: native, Version: "0.160.0", Fingerprint: fingerprint}
	source.Env = []string{"HOME=" + home, "PATH=" + bin}
	profile, err := store.ProfilePath(bindings[0])
	if err != nil {
		t.Fatal(err)
	}
	argv, err := codex.New().Command(adapter.LaunchSpec{Cwd: source.Cwd, Options: source.LaunchOptions})
	if err != nil {
		t.Fatal(err)
	}
	projection, err := accountconfig.Prepare(root, accounts.ProviderCodex, profile, source.Cwd, source.Env, argv, "")
	if err != nil {
		t.Fatal(err)
	}
	source.AccountProjectionRef = projection.Ref
	source.AccountBinding.ConfigurationGeneration = projection.Generation
	accountTestRollout(t, store, *source.AccountBinding, source.Cwd, "synthetic retained history\n")
	f := &accountManualResumeFixture{manager: &accountManager{store: store, stateRoot: root, native: map[string]*persist.CLIIdentity{accounts.ProviderCodex: source.CLIIdentity}}, source: source, profile: profile, probe: probe, stop: errors.New("synthetic stop before child spawn")}
	f.native = shim.NativeProcessInfo{SchemaVersion: shim.ManagedWriterSchemaVersion, Generation: strings.Repeat("a", 32), PID: 1<<30 + 1, PGID: 1<<30 + 1, StartTime: 1, ShimPID: source.ShimPID, ShimStartTime: source.ShimStartTime, Binding: *source.AccountBinding}
	return f
}

func (f *accountManualResumeFixture) api(t *testing.T) *coreAPI {
	t.Helper()
	persistence, err := persist.NewStore(f.manager.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.Save(f.source); err != nil {
		t.Fatal(err)
	}
	assembly := &Daemon{accounts: f.manager}
	core := accountTestCore(t, f.manager, func(cfg *daemon.Config) {
		cfg.FinalizeLaunch = func(id string, spec daemon.LaunchSpec) (daemon.LaunchSpec, error) {
			prepared, err := assembly.prepareAccountLaunch(id, spec)
			if err != nil {
				return prepared, err
			}
			f.finalized, f.accepted = true, prepared
			return prepared, f.stop
		}
	})
	return &coreAPI{core: core, accounts: f.manager, endpointID: testEndpoint}
}

func (f *accountManualResumeFixture) request() daemon.LaunchSpec {
	return daemon.LaunchSpec{AgentType: accounts.ProviderCodex, Options: map[string]string{protocol.OptionResumeFrom: protocol.NamespacedID(testEndpoint, f.source.ID)}}
}

func manualResumeProof(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAccountManualResumeCustody(t *testing.T) {
	for _, kind := range []string{"lost-live", "older-live", "dead-missing", "clean", "clean-zero", "missing-manager", "unavailable-manager", "unmanaged"} {
		t.Run(kind, func(t *testing.T) {
			f := manualResumeFixture(t)
			if kind == "unmanaged" {
				f.source.AccountBinding = nil
				f.source.AccountProjectionRef = ""
			}

			older := f.source
			if kind == "older-live" {
				older.ID, older.ShimPID = "lost-sibling", 1<<30+3
				f.source.ResumedFrom = older.ID
				persistence, err := persist.NewStore(f.manager.stateRoot)
				if err != nil {
					t.Fatal(err)
				}
				if err := persistence.Save(older); err != nil {
					t.Fatal(err)
				}
			}
			api := f.api(t)
			dir := filepath.Join(f.manager.stateRoot, f.source.ID)
			if kind == "lost-live" || kind == "older-live" {
				python, err := exec.LookPath("python3")
				if err != nil {
					t.Skip("synthetic writer requires Python")
				}
				activity := filepath.Join(f.profile, "fixture-writer-activity")
				writer := exec.Command(python, "-c", "import sys,time\nwhile True:\n with open(sys.argv[1],'w') as f:f.write(str(time.monotonic_ns()))\n time.sleep(.01)\n", activity)
				writer.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
				if err := writer.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = writer.Process.Kill(); _ = writer.Wait() })
				start, err := procstart.StartTime(writer.Process.Pid)
				if err != nil {
					t.Fatal(err)
				}

				if kind == "older-live" {
					oldNative := f.native
					oldNative.PID, oldNative.PGID, oldNative.StartTime = writer.Process.Pid, writer.Process.Pid, start
					oldNative.ShimPID, oldNative.ShimStartTime = older.ShimPID, older.ShimStartTime
					oldNative.Generation = strings.Repeat("b", 32)
					manualResumeProof(t, filepath.Join(f.manager.stateRoot, older.ID, shim.NativeProcessFile), oldNative)
				} else {
					f.native.PID, f.native.PGID, f.native.StartTime = writer.Process.Pid, writer.Process.Pid, start
				}

				deadline := time.Now().Add(2 * time.Second)
				for {
					if _, err := os.Stat(activity); err == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("synthetic profile writer did not start")
					}
					time.Sleep(time.Millisecond)
				}
			}
			if kind == "clean-zero" {
				f.native.PID, f.native.PGID, f.native.StartTime = 0, 0, 0
			}
			if kind != "unmanaged" {
				manualResumeProof(t, filepath.Join(dir, shim.NativeProcessFile), f.native)
				if kind == "clean" || kind == "clean-zero" || kind == "older-live" {
					manualResumeProof(t, filepath.Join(dir, shim.NativeStoppedFile), shim.NativeStoppedInfo{SchemaVersion: shim.ManagedWriterSchemaVersion, Native: f.native, WritersStopped: true})
				}
			}
			if kind == "missing-manager" {
				api.accounts = nil
			}
			if kind == "unavailable-manager" {
				api.accounts = &accountManager{store: f.manager.store, stateRoot: f.manager.stateRoot, unavailable: protocol.ErrAccountsUnavailable}
			}
			_, err := api.Launch(f.request())
			allowed := kind == "clean" || kind == "clean-zero" || kind == "unmanaged"
			if allowed {
				if !f.finalized || !errors.Is(err, f.stop) {
					t.Fatalf("clean/unmanaged resume did not reach real launch preparation: %v", err)
				}
				if kind != "unmanaged" && (f.accepted.AccountBinding == nil || *f.accepted.AccountBinding != *f.source.AccountBinding || f.accepted.AccountProjectionRef != f.source.AccountProjectionRef) {
					t.Fatal("clean resume lost its exact frozen account/projection")
				}
			} else {
				if err == nil || f.finalized {
					t.Fatal("unknown writer custody reached new child preparation")
				}
				if kind == "lost-live" || kind == "older-live" || kind == "dead-missing" {
					if !errors.Is(err, accounts.ErrInUse) {
						t.Fatalf("writer custody refusal was not preserved: %v", err)
					}
				}
				if _, err := os.Stat(f.probe); !os.IsNotExist(err) {
					t.Fatal("unknown writer custody reached native version probe")
				}
				wantRows := 1
				if kind == "older-live" {
					wantRows = 2
				}
				if len(api.core.List()) != wantRows {
					t.Fatal("refused owner resume reserved a new session")
				}

			}
		})
	}
}

func TestAccountManualResumeKeepsRunningDedup(t *testing.T) {
	f := manualResumeFixture(t)
	persistence, err := persist.NewStore(f.manager.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	child := f.source
	child.ID, child.ResumedFrom = "healthy-child", f.source.ID
	child.Status.Process = status.ProcessRunning
	child.CreatedAt = time.Now()
	syntheticShim := exec.Command("/bin/sleep", "60")
	if err := syntheticShim.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syntheticShim.Process.Kill(); _ = syntheticShim.Wait() })
	child.ShimPID = syntheticShim.Process.Pid
	child.ShimStartTime, err = procstart.StartTime(child.ShimPID)
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.Save(child); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(f.manager.stateRoot, child.ID, "shim.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	// Reconcile performs the real hello adoption handshake against this local
	// synthetic shim. Its matched PID stays alive through the owner lookup.
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				typ, raw, err := wire.ReadFrame(conn)
				if err != nil || typ != wire.TControl {
					return
				}
				ctrl, err := shimwire.Decode(raw)
				if err != nil || ctrl.Type != shimwire.TypeHello {
					return
				}
				reply, _ := shimwire.Encode(shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version})
				_ = wire.WriteFrame(conn, wire.TControl, reply)
			}()
		}
	}()
	api := f.api(t)
	// Reuse requires no stopped proof or replacement account manager because it
	// creates no writer. A moved guard must not turn this into a failed new spawn.
	api.accounts = nil
	got, err := api.Launch(f.request())
	if err != nil || got.ID != child.ID || got.Status.Process != status.ProcessRunning || f.finalized {
		t.Fatalf("healthy existing resume was not reused: id=%s err=%v", got.ID, err)
	}
	if _, err := os.Stat(f.probe); !os.IsNotExist(err) {
		t.Fatal("deduplicated running resume started a native probe")
	}
}
