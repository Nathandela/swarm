package skeleton

import (
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenCodeLaunchPinsExecutableIdentityAndPreservesSelectors(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "opencode")
	write := func(v string) {
		t.Helper()
		if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' '"+v+"'\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	write("1.19.3")
	a := &coreAPI{}
	spec := daemon.LaunchSpec{AgentType: "opencode", Cwd: dir, Argv: []string{bin, "--model", "opencode/deepseek-v4.1-flash-free", "--agent", "plan", "--prompt", "genuine"}, ClientEnv: []string{"PATH=" + dir}, OpenCodeEnv: []string{"XDG_DATA_HOME=" + dir}}
	if err := a.qualifyOpenCodeLaunch(&spec); err != nil {
		t.Fatal(err)
	}
	if spec.RequiredRuntime != persist.RequiredRuntimeOpenCode || spec.CLIIdentity == nil || spec.CLIIdentity.Version != "1.19.3" || spec.ExpectedCLIIdentity == nil {
		t.Fatal("native launch was not pinned")
	}
	if len(spec.Argv) != 7 || spec.Argv[2] != "opencode/deepseek-v4.1-flash-free" || spec.Argv[4] != "plan" {
		t.Fatal("native selections rewritten")
	}
	write("1.19.4")
	if a.qualifyOpenCodeLaunch(&spec) == nil {
		t.Fatal("changed native executable identity admitted")
	}
}

func TestOpenCodeLaunchAcceptsMinimumStableNativeVersion(t *testing.T) {
	for _, tc := range []struct {
		version string
		allowed bool
	}{{"1.19.3", true}, {"1.19.4", true}, {"1.20.0", true}, {"2.0.0", true}, {"10000.0.0", true}, {"1.19.2", false}, {"1.19.3-rc.1", false}, {"1.20.0+build", false}, {"01.20.0", false}, {"1.20", false}, {"invalid", false}} {
		t.Run(tc.version, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "opencode")
			if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' '"+tc.version+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			spec := daemon.LaunchSpec{AgentType: "opencode", Cwd: dir, Argv: []string{bin}, ClientEnv: []string{"PATH=" + dir}}
			err := (&coreAPI{}).qualifyOpenCodeLaunch(&spec)
			if (err == nil) != tc.allowed {
				t.Fatalf("native %q accepted=%v, want %v: %v", tc.version, err == nil, tc.allowed, err)
			}
			if tc.allowed && (spec.CLIIdentity == nil || spec.CLIIdentity.Version != tc.version || spec.ExpectedCLIIdentity == nil || *spec.CLIIdentity != *spec.ExpectedCLIIdentity) {
				t.Fatal("launch lost exact executable identity")
			}
		})
	}
}

func TestOpenCodeLaunchRejectsLegacyExternalResume(t *testing.T) {
	a := &coreAPI{}
	spec := daemon.LaunchSpec{AgentType: "opencode", ConversationID: "ses_0123456789abcdef0123456789abcdef"}
	if a.qualifyOpenCodeLaunch(&spec) == nil {
		t.Fatal("resume without exact durable native identity admitted")
	}
}
