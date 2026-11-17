package skeleton

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

const agyHookConvA = "30000000-0000-4000-8000-000000000033"

func TestAgySupportsStatusLineVersionGate(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{
		{"", false},
		{"bad", false},
		{"1.1.4", false},
		{"1.3.9", false},
		{"1.4.0", true},
		{"1.4.7", true},
		{"1.5.9", true},
		{"2.0.0", true},
	}
	for _, tc := range cases {
		if got := agySupportsStatusLine(tc.version); got != tc.want {
			t.Errorf("agySupportsStatusLine(%q) = %v, want %v", tc.version, got, tc.want)
		}
	}
}

func TestAgyStatusLineArgs_DefaultAndExistingSettings(t *testing.T) {
	t.Run("injects quiet observer preserving argv[0] when settings absent", func(t *testing.T) {
		home := t.TempDir()
		argv := []string{"/usr/local/bin/agy", "--model", "Gemini 4 Carbon (Medium)", "--mode", "auto"}
		got := agyStatusLineArgsWithBinary(argv, []string{"HOME=" + home}, "/opt/bin/swarm")

		if got[0] != "/usr/local/bin/agy" {
			t.Fatalf("argv[0] mutated: %q", got[0])
		}
		if len(got) < 3 || got[1] != "--settings" {
			t.Fatalf("expected --settings at argv[1:3], got %v", got)
		}
		if !reflect.DeepEqual(got[3:], argv[1:]) {
			t.Fatalf("trailing args changed: %v != %v", got[3:], argv[1:])
		}
		var settings map[string]json.RawMessage
		if err := json.Unmarshal([]byte(got[2]), &settings); err != nil {
			t.Fatalf("invalid settings JSON %q: %v", got[2], err)
		}
		var line map[string]any
		if err := json.Unmarshal(settings["statusLine"], &line); err != nil {
			t.Fatalf("invalid statusLine JSON: %v", err)
		}
		if line["type"] != "command" || line["enabled"] != true || line["stack_with_default"] != true {
			t.Fatalf("unexpected default statusLine fields: %#v", line)
		}
		if !strings.Contains(line["command"].(string), `'/opt/bin/swarm' hook AgyStatusLine ''`) {
			t.Fatalf("unexpected statusLine command: %q", line["command"])
		}
		if reflect.DeepEqual(argv, got) {
			t.Fatal("input argv modified in place")
		}
	})

	t.Run("chains user statusLine command and preserves settings without mutating disk", func(t *testing.T) {
		home := t.TempDir()
		settingsPath := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
		if err := os.MkdirAll(filepath.Dir(settingsPath), 0o700); err != nil {
			t.Fatal(err)
		}
		const raw = `{"statusLine":{"type":"command","command":"printf 'custom; $0'","padding":2,"stack_with_default":false},"model":"Gemini 4 Carbon (Medium)"}`
		if err := os.WriteFile(settingsPath, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}

		argv := []string{"/usr/local/bin/agy"}
		got := agyStatusLineArgsWithBinary(argv, []string{"HOME=" + home}, "/opt/bin/swarm")
		var settings map[string]json.RawMessage
		if err := json.Unmarshal([]byte(got[2]), &settings); err != nil {
			t.Fatal(err)
		}
		var line map[string]any
		if err := json.Unmarshal(settings["statusLine"], &line); err != nil {
			t.Fatal(err)
		}
		if line["padding"] != float64(2) || line["stack_with_default"] != false {
			t.Fatalf("user statusLine fields dropped: %#v", line)
		}
		_, quoted, ok := strings.Cut(line["command"].(string), " hook AgyStatusLine ")
		if !ok {
			t.Fatalf("chained command missing AgyStatusLine: %q", line["command"])
		}
		decoded, err := exec.Command("/bin/sh", "-c", "set -- "+quoted+`; printf '%s' "$1"`).Output()
		if err != nil || string(decoded) != "printf 'custom; $0'" {
			t.Fatalf("quoted native command changed: %q, %v", decoded, err)
		}
		after, _ := os.ReadFile(settingsPath)
		if string(after) != raw {
			t.Fatal("user settings.json was mutated")
		}
	})

	t.Run("merges into existing --settings JSON argument", func(t *testing.T) {
		home := t.TempDir()
		argv := []string{"/usr/local/bin/agy", "--settings", `{"colorScheme":"dark"}`, "--mode", "plan"}
		got := agyStatusLineArgsWithBinary(argv, []string{"HOME=" + home}, "/opt/bin/swarm")
		if len(got) != len(argv) || got[1] != "--settings" {
			t.Fatalf("unexpected merged argv: %v", got)
		}
		var settings map[string]json.RawMessage
		if err := json.Unmarshal([]byte(got[2]), &settings); err != nil {
			t.Fatal(err)
		}
		if string(settings["colorScheme"]) != `"dark"` {
			t.Fatalf("existing settings key lost: %s", got[2])
		}
		if _, ok := settings["statusLine"]; !ok {
			t.Fatalf("statusLine not merged: %s", got[2])
		}
	})
}

func TestAgyStatusLineIngest_AuthStatusIdentityAndTitle(t *testing.T) {
	sk := assemble(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "agent.txt")
	if err := os.WriteFile(script, []byte("idle 600s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := sk.core.Launch(daemon.LaunchSpec{
		AgentType: "agy",
		Argv:      []string{fakeAgentBin, script},
		Cwd:       dir,
		Cols:      80,
		Rows:      24,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(m.ShimPID, syscall.SIGTERM) })

	token := hookTokenFor(t, sk.stateDir, m.ID)

	// Foreign token must be rejected without touching status, name, or id.
	bad := marshalHookCallback(t, engine.Callback{
		SessionID: m.ID,
		Token:     "wrong-token",
		Sequence:  1,
		Event:     "AgyStatusLine",
		Payload: map[string]string{
			"agent_state":        "reviewing",
			"conversation_id":    agyHookConvA,
			"conversation_title": "Forged title",
			"observed_at":        time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano),
		},
	})
	if err := sk.ingestHookBytes(bad); err == nil {
		t.Fatal("ingestHookBytes accepted foreign AgyStatusLine token")
	}
	if cur, _ := sk.Core().Get(m.ID); cur.ConversationID != "" || cur.Name == "Forged title" {
		t.Fatalf("foreign token mutated session: %+v", cur)
	}

	// Authenticated working callback latches conversation_id, sets TurnActive, and adopts title.
	t1 := time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano)
	cb1 := marshalHookCallback(t, engine.Callback{
		SessionID: m.ID,
		Token:     token,
		Sequence:  1,
		Event:     "AgyStatusLine",
		Payload: map[string]string{
			"agent_state":        "working",
			"conversation_id":    agyHookConvA,
			"conversation_title": "Refactor auth",
			"observed_at":        t1,
		},
	})
	if err := sk.ingestHookBytes(cb1); err != nil {
		t.Fatalf("ingest cb1: %v", err)
	}
	cur, _ := sk.Core().Get(m.ID)
	if cur.ConversationID != agyHookConvA {
		t.Fatalf("ConversationID = %q, want %q", cur.ConversationID, agyHookConvA)
	}
	if cur.Name != "Refactor auth" {
		t.Fatalf("Name = %q, want %q", cur.Name, "Refactor auth")
	}
	if cur.Status.Turn != status.TurnActive || cur.Status.Interaction != status.InteractionNone {
		t.Fatalf("status after working = %+v, want active/none", cur.Status)
	}

	// User manually renames the session; repeating the SAME agy title on a later tick must not clobber it.
	if err := sk.Core().Rename(m.ID, "My Custom Name"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	t2 := time.Now().Add(2 * time.Second).UTC().Format(time.RFC3339Nano)
	cb2 := marshalHookCallback(t, engine.Callback{
		SessionID: m.ID,
		Token:     token,
		Sequence:  2,
		Event:     "AgyStatusLine",
		Payload: map[string]string{
			"agent_state":        "reviewing",
			"conversation_id":    agyHookConvA,
			"conversation_title": "Refactor auth",
			"observed_at":        t2,
		},
	})
	if err := sk.ingestHookBytes(cb2); err != nil {
		t.Fatalf("ingest cb2: %v", err)
	}
	cur, _ = sk.Core().Get(m.ID)
	if cur.Name != "My Custom Name" {
		t.Fatalf("repeated unchanged agy title clobbered manual rename: %q", cur.Name)
	}
	if cur.Status.Turn != status.TurnIdle || cur.Status.Interaction != status.InteractionPermission {
		t.Fatalf("status after reviewing = %+v, want idle/permission", cur.Status)
	}

	// A genuinely new title from agy with a newer timestamp wins per ADR-022, and idle clears permission.
	t3 := time.Now().Add(3 * time.Second).UTC().Format(time.RFC3339Nano)
	cb3 := marshalHookCallback(t, engine.Callback{
		SessionID: m.ID,
		Token:     token,
		Sequence:  3,
		Event:     "AgyStatusLine",
		Payload: map[string]string{
			"agent_state":        "idle",
			"conversation_title": "New CLI Title",
			"observed_at":        t3,
		},
	})
	if err := sk.ingestHookBytes(cb3); err != nil {
		t.Fatalf("ingest cb3: %v", err)
	}
	cur, _ = sk.Core().Get(m.ID)
	if cur.Name != "New CLI Title" {
		t.Fatalf("new title not adopted: %q", cur.Name)
	}
	if cur.Status.Turn != status.TurnIdle || cur.Status.Interaction != status.InteractionNone {
		t.Fatalf("status after idle = %+v, want idle/none", cur.Status)
	}

	// Verify authenticating and error states map to prompt and error interactions.
	for i, tc := range []struct {
		state string
		want  status.Interaction
	}{
		{"authenticating", status.InteractionPrompt},
		{"error", status.InteractionError},
	} {
		cb := marshalHookCallback(t, engine.Callback{
			SessionID: m.ID,
			Token:     token,
			Sequence:  uint64(4 + i),
			Event:     "AgyStatusLine",
			Payload:   map[string]string{"agent_state": tc.state},
		})
		if err := sk.ingestHookBytes(cb); err != nil {
			t.Fatalf("ingest %s: %v", tc.state, err)
		}
		if cur, _ := sk.Core().Get(m.ID); cur.Status.Turn != status.TurnIdle || cur.Status.Interaction != tc.want {
			t.Fatalf("status after %s = %+v, want idle/%s", tc.state, cur.Status, tc.want)
		}
	}

	if caps, ok := sk.sessionCapabilities(m.ID); ok && caps.StructuredChat {
		t.Fatal("AgyStatusLine must not claim StructuredChat")
	}
}

func TestPublicLaunch_AgyInjectsStatusLineSettingsVersionGated(t *testing.T) {
	for _, tc := range []struct {
		name         string
		version      string
		wantSettings bool
	}{
		{"v1.5.9_injects_settings", "1.5.9", true},
		{"v1.1.4_skips_settings", "1.1.4", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sk := assemble(t)
			dir := t.TempDir()
			argvFile := filepath.Join(dir, "argv.json")
			program := filepath.Join(dir, "agy")
			script := fmt.Sprintf(`#!/usr/bin/env python3
import json, signal, sys
if len(sys.argv) > 1 and sys.argv[1] == '--version':
    print(%q)
    sys.exit(0)
with open(%q, 'w') as f:
    json.dump(sys.argv, f)
signal.pause()
`, tc.version, argvFile)
			if err := os.WriteFile(program, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}

			client := dialClient(t, sk)
			session, _, err := client.Launch(protocol.LaunchReq{
				Agent: "agy",
				Cwd:   dir,
				Env:   []string{"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"), "HOME=" + dir},
				Cols:  80,
				Rows:  24,
			})
			if err != nil {
				t.Fatalf("Launch: %v", err)
			}
			_, local, _ := protocol.ParseID(session)
			if m, ok := sk.core.Get(local); ok {
				t.Cleanup(func() { _ = syscall.Kill(m.ShimPID, syscall.SIGTERM) })
			}

			var gotArgv []string
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				raw, readErr := os.ReadFile(argvFile)
				if readErr == nil && json.Unmarshal(raw, &gotArgv) == nil {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if len(gotArgv) == 0 {
				t.Fatal("fake agy never wrote argv.json")
			}

			hasSettings := false
			for i, a := range gotArgv {
				if a == "--settings" && i+1 < len(gotArgv) && strings.Contains(gotArgv[i+1], "AgyStatusLine") {
					hasSettings = true
				}
			}
			if hasSettings != tc.wantSettings {
				t.Fatalf("hasSettings = %v, want %v (argv=%v)", hasSettings, tc.wantSettings, gotArgv)
			}
			m, _ := sk.core.Get(local)
			if m.CLIIdentity == nil || m.CLIIdentity.Version != tc.version {
				t.Fatalf("CLIIdentity = %+v, want version %q", m.CLIIdentity, tc.version)
			}
		})
	}
}
