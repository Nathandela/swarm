package daemon

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/shimwire"
)

func claudeFixtureStopTarget(t *testing.T, d *Daemon) shimwire.ClaudePrimaryIdentity {
	t.Helper()
	m, err := d.Launch(LaunchSpec{AgentType: "fake", Cwd: t.TempDir(), ClientEnv: []string{"PATH=" + os.Getenv("PATH")}, RequiredRuntime: "claude-chat", ClaudeChatFixture: fakeAgentBin, Argv: []string{fakeAgentBin, "--claude-primary-fixture"}, Options: map[string]string{"claude_interface": "swarm-chat"}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := shim.ReadHookSpoolFileResponse(filepath.Join(d.cfg.StateDir, m.ID, shim.HookSpoolFile), 0)
		if err == nil {
			for _, record := range response.Records {
				var envelope struct {
					Origin  string                         `json:"origin"`
					Primary shimwire.ClaudePrimaryIdentity `json:"primary"`
				}
				if json.Unmarshal(record.Body, &envelope) == nil && envelope.Origin == "claude_primary" {
					p := envelope.Primary
					if p.SessionID != m.ID || p.RequiredRuntime != "claude-chat" || p.NativeSessionID != m.ConversationID || p.ShimPID != m.ShimPID || p.ShimStartTime != m.ShimStartTime || p.PrimaryPID <= 0 || p.PrimaryStartTime <= 0 {
						t.Fatal("actual constructor identity mismatch")
					}
					return p
				}
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("actual fixture init not captured")
	return shimwire.ClaudePrimaryIdentity{}
}

func TestClaudeChatFixtureStopDoesNotWaitForGlobalWriter(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	target := claudeFixtureStopTarget(t, d)
	// This is a held-lock control, NOT called real fsync evidence. The connected
	// skeleton test separately holds actual foreign Request/Evidence Sync.
	d.writeMu.Lock()
	locked := true
	done := make(chan error, 1)
	joined := false
	go func() { done <- d.KillClaudeFixture(target.SessionID, target) }()
	t.Cleanup(func() {
		if locked {
			d.writeMu.Unlock()
		}
		if !joined {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("typed Stop worker did not join")
			}
		}
	})
	select {
	case err := <-done:
		joined = true
		if err != nil {
			t.Fatal("typed Stop refused exact live fixture", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop waited for global persistence writer")
	}
	waitProcessGone(t, target.PrimaryPID, pollTimeout) // before releasing global writer
	d.writeMu.Unlock()
	locked = false
}

func TestClaudeChatFixtureStopBusyOrWrongIdentityHasZeroEffects(t *testing.T) {
	d := openDaemon(t, daemonConfig(t))
	target := claudeFixtureStopTarget(t, d)
	before, err := os.ReadFile(filepath.Join(d.cfg.StateDir, target.SessionID, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	busy := make(chan error, 1)
	go func() { busy <- d.KillClaudeFixture(target.SessionID, target) }()
	select {
	case err := <-busy:
		d.mu.Unlock()
		if err == nil {
			t.Fatal("busy current-row proof fabricated Stop success")
		}
	case <-time.After(time.Second):
		d.mu.Unlock()
		<-busy
		t.Fatal("busy current-row proof blocked")
	}
	for _, mutate := range []func(*shimwire.ClaudePrimaryIdentity){
		func(p *shimwire.ClaudePrimaryIdentity) { p.ShimStartTime++ },
		func(p *shimwire.ClaudePrimaryIdentity) { p.NativeSessionID = "1389ef09-4c19-4d50-8fdd-1fc95bdcfd4a" },
		func(p *shimwire.ClaudePrimaryIdentity) { p.RequiredRuntime = "" },
	} {
		wrong := target
		mutate(&wrong)
		if d.KillClaudeFixture(target.SessionID, wrong) == nil {
			t.Fatal("wrong identity reported Stop success")
		}
	}
	start, err := processStartTime(target.PrimaryPID)
	if err != nil || start != target.PrimaryStartTime {
		t.Fatal("refused Stop terminated or replaced exact child")
	}
	d.mu.Lock()
	d.closed = true
	d.mu.Unlock()
	err = d.KillClaudeFixture(target.SessionID, target)
	d.mu.Lock()
	d.closed = false
	d.mu.Unlock()
	if err == nil {
		t.Fatal("closed daemon retained Stop authority")
	}
	after, err := os.ReadFile(filepath.Join(d.cfg.StateDir, target.SessionID, "meta.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("refused Stop persisted metadata/finalization")
	}
	start, err = processStartTime(target.PrimaryPID)
	if err != nil || start != target.PrimaryStartTime {
		t.Fatal("closed-daemon refusal terminated exact child")
	}
}

func TestClaudeChatFixtureStopDoesNotNeedPrimaryInitialization(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-init", true: "failed-init"}[malformed], func(t *testing.T) {
			d := openDaemon(t, daemonConfig(t))
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			pidFile := filepath.Join(root, "pid")
			fixture := filepath.Join(root, "fake")
			output := ""
			if malformed {
				output = "printf '%s\\n' 'not-a-native-init'\n"
			}
			script := "#!/bin/sh\nprintf '%s' \"$$\" > '" + pidFile + "'\n" + output + "exec sleep 12\n"
			if err := os.WriteFile(fixture, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			m, err := d.Launch(LaunchSpec{AgentType: "fake", Cwd: root, ClientEnv: []string{"PATH=/usr/bin:/bin"}, RequiredRuntime: "claude-chat", ClaudeChatFixture: fixture, Argv: []string{fixture, "--claude-primary-fixture"}, Options: map[string]string{"claude_interface": "swarm-chat"}})
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			var pid int
			for time.Now().Before(deadline) {
				raw, e := os.ReadFile(pidFile)
				if e == nil {
					pid, _ = strconv.Atoi(string(raw))
					if pid > 0 {
						break
					}
				}
				time.Sleep(time.Millisecond)
			}
			if pid <= 0 {
				row, _ := d.Get(m.ID)
				capture, capErr := os.ReadFile(filepath.Join(d.cfg.StateDir, m.ID, "claude-capture.json"))
				exit, exitErr := os.ReadFile(filepath.Join(d.cfg.StateDir, m.ID, "exit.json"))
				// Fixed JSON receipts contain no config/environment/credential payload.
				if len(capture) > 1024 {
					capture = nil
				}
				if len(exit) > 1024 {
					exit = nil
				}
				t.Fatalf("test child never published exact PID: process=%s capture=%s capture_present=%t exit=%s exit_present=%t", row.Status.Process, capture, capErr == nil, exit, exitErr == nil)
			}
			birth, err := processStartTime(pid)
			if err != nil || birth <= 0 {
				t.Fatal("child birth not positively observed")
			}
			target := shimwire.ClaudePrimaryIdentity{SessionID: m.ID, RequiredRuntime: m.RequiredRuntime, NativeSessionID: m.ConversationID, ShimPID: m.ShimPID, ShimStartTime: m.ShimStartTime}
			if err := d.KillClaudeFixture(m.ID, target); err != nil {
				t.Fatal("Stop depended on primary initialization", err)
			}
			waitProcessGone(t, pid, pollTimeout)
		})
	}
}
