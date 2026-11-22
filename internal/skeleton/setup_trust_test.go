package skeleton

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/setuptrust"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
)

type hookSetupProbe struct {
	backendConn
	hook      launchHook
	workspace string
	reloaded  bool
	writes    []string
	changed   bool
}

func (p *hookSetupProbe) Call(_ context.Context, method string, params, out any) error {
	var result any
	switch method {
	case "config/read":
		result = map[string]any{"config": map[string]any{"projects": map[string]any{p.workspace: map[string]string{"trust_level": "trusted"}}}}
	case "hooks/list":
		h := p.hook
		if p.reloaded {
			h.TrustStatus = "trusted"
			if p.changed {
				h.CurrentHash = "sha256:" + strings.Repeat("b", 64)
			}
		}
		result = map[string]any{"data": []map[string]any{{"cwd": p.workspace, "hooks": []launchHook{h}}}}
	case "config/batchWrite":
		b, _ := json.Marshal(params)
		p.writes = append(p.writes, string(b))
		p.reloaded = true
		result = map[string]string{"status": "ok"}
	}
	b, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func TestSetupTrustUsesCurrentHookIdentityAndExactWorkspace(t *testing.T) {
	hash := "sha256:" + strings.Repeat("a", 64)
	for _, tt := range []struct {
		name, cwd, source, hash string
		valid, changed, wantErr bool
		writes                  int
	}{
		{"accept", "/work", "user", hash, true, false, false, 1},
		{"revoked", "/work", "user", hash, false, false, true, 0},
		{"unknown-source", "/work", "unknown", hash, true, false, true, 0},
		{"unverified-hash", "/work", "user", "missing", true, false, true, 0},
		{"concurrent-edit", "/work", "user", hash, true, true, true, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := launchHook{Key: "user-hook", Source: tt.source, SourcePath: "/work/.codex/hooks.json", CurrentHash: tt.hash, Enabled: true, TrustStatus: "modified"}
			p := &hookSetupProbe{workspace: "/work", hook: h, changed: tt.changed}
			err := applyCodexLaunchTrust(context.Background(), p, setuptrust.Consent{Workspace: tt.cwd, ProfileHooks: true}, func() bool { return tt.valid })
			if (err != nil) != tt.wantErr || len(p.writes) != tt.writes {
				t.Fatalf("err=%v writes=%v", err, p.writes)
			}
			if !tt.wantErr && (!strings.Contains(p.writes[0], hash) || !strings.Contains(p.writes[0], `"keyPath":"hooks.state"`)) {
				t.Fatalf("unexpected trust edit %s", p.writes[0])
			}
		})
	}
}

func TestAgySetupTrustAtFinalWorkspace(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Setenv("HOME", t.TempDir())
	} else {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	}
	cwd := t.TempDir()
	project, err := setuptrust.Set(cwd, true, false)
	if err != nil {
		if errors.Is(err, setuptrust.ErrWorkspaceIdentity) {
			t.Skipf("fixture filesystem: %v", err)
		}
		t.Fatal(err)
	}
	d := &Daemon{}
	args := []string{"agy", "--settings", `{"statusLine":{"type":"command"}}`, "--prompt-interactive", "--settings"}
	spec := daemon.LaunchSpec{AgentType: "agy", Argv: args, Cwd: cwd, CLIIdentity: &persist.CLIIdentity{Version: "1.5.13"}}
	result, err := d.finalizeAgentLaunch("session", spec)
	if err != nil {
		t.Fatal(err)
	}
	var observed struct {
		Trusted    []string `json:"trustedWorkspaces"`
		StatusLine struct{ Type string }
	}
	if err = json.Unmarshal([]byte(result.Argv[2]), &observed); err != nil || !slices.Equal(observed.Trusted, []string{project}) || observed.StatusLine.Type != "command" || !slices.Equal(result.Argv[3:], args[3:]) {
		t.Fatalf("unexpected argv: %v err=%v", result.Argv, err)
	}
	spec.CLIIdentity.Version = "1.5.14"
	result, err = d.finalizeAgentLaunch("session", spec)
	if err != nil || !slices.Equal(result.Argv, args) {
		t.Fatalf("unqualified: %v err=%v", result.Argv, err)
	}
	spec.CLIIdentity.Version = "1.5.13"
	sub, err := os.MkdirTemp(cwd, "child")
	if err != nil {
		t.Fatal(err)
	}
	spec.Cwd = sub
	result, err = d.finalizeAgentLaunch("session", spec)
	if err != nil || !slices.Equal(result.Argv, args) {
		t.Fatalf("unenrolled: %v err=%v", result.Argv, err)
	}
}

func TestSetupTrustProfileHooksRequireSeparateConsent(t *testing.T) {
	h := launchHook{Key: "k", CurrentHash: "sha256:" + strings.Repeat("a", 64), Source: "plugin", TrustStatus: "untrusted", Enabled: true}
	p := &hookSetupProbe{workspace: "/work", hook: h}
	err := applyCodexLaunchTrust(context.Background(), p, setuptrust.Consent{Workspace: "/work"}, func() bool { return true })
	if err == nil || len(p.writes) != 0 {
		t.Fatalf("profile hook received workspace approval: err=%v writes=%d", err, len(p.writes))
	}
}

func TestSetupTrustUnknownHookStatusNeedsReview(t *testing.T) {
	h := launchHook{Key: "k", Source: "user", CurrentHash: "sha256:" + strings.Repeat("a", 64), TrustStatus: "new-status", Enabled: true}
	p := &hookSetupProbe{workspace: "/work", hook: h}
	err := applyCodexLaunchTrust(context.Background(), p, setuptrust.Consent{Workspace: "/work", ProfileHooks: true}, func() bool { return true })
	if err == nil || len(p.writes) != 0 {
		t.Fatalf("unrecognized status: err=%v writes=%d", err, len(p.writes))
	}
}
