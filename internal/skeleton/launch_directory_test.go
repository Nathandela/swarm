//go:build linux

package skeleton

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/tui"
)

type directoryLaunchFixture struct {
	api     *coreAPI
	manager *accountManager
	env     []string
	record  string
}

func newDirectoryLaunchFixture(t *testing.T, provider string) directoryLaunchFixture {
	t.Helper()
	buildBinaries(t)
	m := accountTestManager(t, accountTestState(t))
	accountTestAdmit(t, m, accountTestCandidate(t, m, provider, "directory-account"))
	if _, err := m.store.SetEnabled(accountTestRegistry(t, m).Revision, provider, true); err != nil {
		t.Fatal(err)
	}
	_, _, env := accountTestLaunchEnvironment(t, m.stateRoot)
	bin, record := filepath.Join(m.stateRoot, provider), filepath.Join(m.stateRoot, "native-cwd")
	version := "codex-cli 0.160.0"
	if provider == "claude" {
		version = "2.1.289 (Claude Code)"
	}
	// The executable only reports a pinned version and records its launch cwd.
	// It never authenticates or contacts a provider.
	accountTestPut(t, bin, []byte(fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = --version ]; then printf '%%s\\n' %q; exit; fi\nprintf '%%s\\n' \"$PWD\" > %q\nexec /bin/sleep 60\n", version, record)))
	if err := os.Chmod(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	env[1] = "PATH=" + m.stateRoot + ":/usr/bin:/bin"
	assembly := &Daemon{accounts: m}
	core := accountTestCore(t, m, func(cfg *daemon.Config) {
		cfg.ShimBinary = swarmBin
		cfg.PreLaunch = preLaunchWorktree
		cfg.PreDelete = preDeleteWorktree
		cfg.FinalizeLaunch = assembly.prepareAccountLaunch
	})
	api := newCoreAPI(core, "", testEndpoint)
	api.accounts = m
	t.Cleanup(api.close)
	return directoryLaunchFixture{api: api, manager: m, env: env, record: record}
}

func (f directoryLaunchFixture) check(t *testing.T, meta persist.Meta, requested string, worktree bool) {
	t.Helper()
	t.Cleanup(func() { _ = f.api.core.Kill(meta.ID) })
	if meta.Cwd != requested || meta.AccountBinding == nil || meta.AccountProjectionRef == "" {
		t.Fatalf("launch metadata cwd=%q, want %q with managed projection", meta.Cwd, requested)
	}
	actual := requested
	if worktree {
		actual = meta.AgentCwd
		if actual == "" || actual == requested || filepath.Clean(actual) != actual {
			t.Fatalf("worktree cwd=%q, want a separate canonical checkout", actual)
		}
	}
	store, err := persist.NewStore(f.manager.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.Load(meta.ID)
	if err != nil || stored.Cwd != requested || stored.AgentCwd != meta.AgentCwd {
		t.Fatalf("persisted cwd differs from launch: %+v, %v", stored, err)
	}
	for _, filename := range []string{
		filepath.Join(f.manager.stateRoot, meta.ID, "shim-launch.json"),
		filepath.Join(f.manager.stateRoot, "accounts", "configurations", meta.AccountProjectionRef, "projection.json"),
	} {
		raw, err := os.ReadFile(filename)
		var got struct{ Cwd string }
		if err != nil || json.Unmarshal(raw, &got) != nil || got.Cwd != actual {
			t.Fatalf("%s cwd=%q, want %q (read error: %v)", filepath.Base(filename), got.Cwd, actual, err)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(f.record); err == nil {
			if got := strings.TrimSpace(string(raw)); got != actual {
				t.Fatalf("spawned provider cwd=%q, want projection cwd %q", got, actual)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("synthetic provider did not record its cwd")
}

type directoryLaunchClient struct {
	*protocol.Client
	env []string
	req protocol.LaunchReq
	err error
	id  string
}

func (c *directoryLaunchClient) Launch(req protocol.LaunchReq) (string, string, error) {
	c.req = req
	// Preserve the real TUI request, replacing only the ambient machine environment
	// with the isolated synthetic provider environment before sending the owner RPC.
	req.Env = c.env
	id, name, err := c.Client.Launch(req)
	c.id, c.err = id, err
	return id, name, err
}

func TestLaunchDirectoryAutocompleteManagedProviders(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, worktree := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/worktree=%t", provider, worktree), func(t *testing.T) {
				f := newDirectoryLaunchFixture(t, provider)
				repo := newAgentCwdGitRepo(t)
				server := protocol.NewServer(f.api, testEndpoint)
				t.Cleanup(func() { _ = server.Close() })
				socket := filepath.Join(f.manager.stateRoot, "owner.sock")
				listener, err := net.Listen("unix", socket)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = listener.Close() })
				go func() {
					if conn, err := listener.Accept(); err == nil {
						server.ServeConn(conn)
					}
				}()
				owner, err := protocol.Dial(socket, nil)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = owner.Close() })
				client := &directoryLaunchClient{Client: owner, env: f.env}
				model := tui.New(client, func() []tui.AgentInfo {
					return []tui.AgentInfo{{Name: provider, Installed: true, InRange: true}}
				})
				model, _ = model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
				model, detect := model.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
				model, _ = model.Update(detect())
				model, _ = model.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
				// Paste all but the last character, then accept the unique completion.
				model, _ = model.Update(tea.PasteMsg{Content: repo[:len(repo)-1]})
				model, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyRight})
				if worktree {
					// No option schema: directory, name, tag, agent, prompt, worktree.
					for i := 0; i < 5; i++ {
						model, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
					}
					model, _ = model.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
				}
				_, submit := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				if submit == nil {
					t.Fatal("autocomplete launch did not submit")
				}
				if batch, ok := submit().(tea.BatchMsg); ok {
					for _, cmd := range batch {
						if cmd != nil {
							cmd()
						}
					}
				}
				if client.req.Cwd != repo+"/" || client.req.Worktree != worktree {
					t.Fatalf("autocomplete request cwd=%q worktree=%t, want %q worktree=%t", client.req.Cwd, client.req.Worktree, repo+"/", worktree)
				}
				if client.err != nil {
					t.Fatalf("assembled autocomplete launch: %v", client.err)
				}
				_, local, valid := protocol.ParseID(client.id)
				if !valid {
					t.Fatalf("invalid launched session id %q", client.id)
				}
				meta, ok := f.api.core.Get(local)
				if !ok {
					t.Fatal("owner launch did not persist a session")
				}
				f.check(t, meta, repo, worktree)
			})
		}
	}
}

func TestLaunchDirectoryNormalizationPreservesSafety(t *testing.T) {
	for _, path := range []string{"canonical", "trailing-separators", "root", "root-separators", "final-symlink", "ancestor-symlink", "parent-traversal", "symlink-parent-traversal"} {
		t.Run(path, func(t *testing.T) {
			f := newDirectoryLaunchFixture(t, "codex")
			cwd := filepath.Join(f.manager.stateRoot, "project")
			link := filepath.Join(f.manager.stateRoot, "linked")
			if err := os.Mkdir(filepath.Join(cwd, "inner"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(cwd, link); err != nil {
				t.Fatal(err)
			}
			want, safe := cwd, true
			switch path {
			case "trailing-separators":
				cwd += "///"
			case "root", "root-separators":
				cwd, want = "/", "/"
				if path == "root-separators" {
					cwd = "///"
				}
			case "final-symlink":
				cwd, safe = link+"/", false
			case "ancestor-symlink":
				cwd, safe = link+"/inner/", false
			case "parent-traversal":
				cwd, safe = cwd+"/../project/", false
			case "symlink-parent-traversal":
				cwd, safe = link+"/../project/", false
			}
			meta, err := f.api.Launch(daemon.LaunchSpec{AgentType: "codex", Cwd: cwd, ClientEnv: f.env, Cols: 80, Rows: 24})
			if safe {
				if err != nil {
					t.Fatalf("assembled directory launch: %v", err)
				}
				f.check(t, meta, want, false)
			} else {
				if err == nil || !strings.Contains(err.Error(), "unsafe-project-directory") || len(f.api.core.List()) != 0 {
					t.Fatalf("unsafe directory reached persistence/spawn: meta=%+v err=%v", meta, err)
				}
				if _, err := os.Stat(f.record); !os.IsNotExist(err) {
					t.Fatal("unsafe directory spawned the synthetic provider")
				}
			}
		})
	}
}
