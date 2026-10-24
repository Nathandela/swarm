package skeleton

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
)

func TestCLIIdentityProbe(t *testing.T) {
	for _, provider := range []string{"claude", "codex", "opencode"} {
		t.Run(provider, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, provider)
			write := func(version string) {
				t.Helper()
				prefix, suffix := "", " (Claude Code)"
				if provider == "codex" {
					prefix, suffix = "codex-cli ", ""
				} else if provider == "opencode" {
					suffix = ""
				}
				// Native installers replace the executable atomically. Distinct inodes
				// also avoid relying on sub-millisecond filesystem mtimes.
				next := filepath.Join(dir, "next-cli")
				if err := os.WriteFile(next, []byte("#!/bin/sh\nprintf '%s\\n' '"+prefix+version+suffix+"'\n"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(next, path); err != nil {
					t.Fatal(err)
				}
			}
			write("2.2.3")
			env := []string{"PATH=" + dir}
			before, err := probeCLIIdentity(provider, "", env, dir)
			if err != nil {
				t.Fatal(err)
			}
			if before.Path != path || before.Version != "2.2.3" {
				t.Fatalf("unexpected: %+v", before)
			}
			write("2.2.4")
			after, err := probeCLIIdentity(provider, path, env, dir)
			if err != nil {
				t.Fatal(err)
			}
			if !cliIdentityNewer(after, before) || after.Fingerprint == before.Fingerprint {
				t.Fatal("update not observed")
			}
			if _, err := probeCLIIdentity(provider, "", []string{"PATH=/missing"}, dir); err == nil {
				t.Fatal("used daemon PATH")
			}
		})
	}
}

func TestCLIIdentityProbeFailures(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"malformed", "echo nonsense"}, {"failed", "echo '2.2.3 (Claude Code)'; exit 1"},
		{"self_change", "echo '# update' >> \"$0\"; echo '2.2.3 (Claude Code)'"},
		{"prerelease", "echo '2.2.4-beta.1 (Claude Code)'"},
		{"build_metadata", "echo '2.2.4+build.1 (Claude Code)'"},
		{"out_of_range", "echo '1.0.0 (Claude Code)'"},
		{"bounded", "while :; do :; done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "claude")
			if err := os.WriteFile(path, []byte("#!/bin/sh\n"+tc.body+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			got, err := probeCLIIdentity("claude", path, []string{"PATH=" + dir}, dir)
			if err == nil || got != nil {
				t.Fatalf("accepted broken probe: %+v %v", got, err)
			}
			if time.Since(start) > cliProbeTimeout+2*time.Second {
				t.Fatal("probe unbounded")
			}
		})
	}
}

func TestCLIIdentitySymlinkAndStderr(t *testing.T) {
	dir := t.TempDir()
	a, b, link := filepath.Join(dir, "a"), filepath.Join(dir, "b"), filepath.Join(dir, "claude")
	for _, path := range []string{a, b} {
		if err := os.WriteFile(path, []byte("#!/bin/sh\necho '2.2.3 (Claude Code)' >&2\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	first, err := probeCLIIdentity("claude", link, []string{"PATH=" + dir}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(b, link); err != nil {
		t.Fatal(err)
	}
	second, err := probeCLIIdentity("claude", link, []string{"PATH=" + dir}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint == second.Fingerprint || first.Path != a || second.Path != b {
		t.Fatal("retarget missed or installation target lost")
	}
	if cliIdentityNewer(second, first) {
		t.Fatal("same-version replacement considered upgrade")
	}
}

func TestCLIIdentityRetainsTargetAcrossLauncherUpgrade(t *testing.T) {
	for _, provider := range []string{"claude", "codex", "opencode"} {
		t.Run(provider, func(t *testing.T) {
			dir := t.TempDir()
			old, next, link := filepath.Join(dir, "old"), filepath.Join(dir, "next"), filepath.Join(dir, provider)
			for path, version := range map[string]string{old: "2.2.3", next: "2.2.4"} {
				output := version + " (Claude Code)"
				if provider == "codex" {
					output = "codex-cli " + version
				} else if provider == "opencode" {
					output = version
				}
				if err := os.WriteFile(path, []byte("#!/bin/sh\necho '"+output+"'\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(old, link); err != nil {
				t.Fatal(err)
			}
			env := []string{"PATH=" + dir}
			before, err := probeCLIIdentity(provider, "", env, dir)
			if err != nil {
				t.Fatal(err)
			}
			bound, err := probeCLIIdentity(provider, before.Path, env, dir)
			if err != nil || bound == nil || *bound != *before {
				t.Fatalf("canonical launch no longer matches expected identity: %+v %v", bound, err)
			}
			if err := os.Remove(link); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(next, link); err != nil {
				t.Fatal(err)
			}
			if before.Path != old || !persist.MatchCLIFingerprint(before.Path, before.Fingerprint) {
				t.Fatalf("old native identity followed upgraded launcher: %+v", before)
			}
			after, err := probeCLIIdentity(provider, "", env, dir)
			if err != nil {
				t.Fatal(err)
			}
			if after.Path != next || !cliIdentityNewer(after, before) || persist.MatchCLIFingerprint(after.Path, before.Fingerprint) {
				t.Fatalf("new launch did not discover upgraded target: %+v -> %+v", before, after)
			}
			if err := os.WriteFile(old, []byte("changed installation"), 0700); err != nil {
				t.Fatal(err)
			}
			if persist.MatchCLIFingerprint(before.Path, before.Fingerprint) {
				t.Fatal("changed retained target still matched")
			}
		})
	}
}

func TestCLIIdentityRejectsLauncherRetargetDuringProbe(t *testing.T) {
	dir := t.TempDir()
	old, next, link := filepath.Join(dir, "old"), filepath.Join(dir, "next"), filepath.Join(dir, "claude")
	body := "#!/bin/sh\n/bin/rm \"$CLI_LINK\" && /bin/ln -s \"$CLI_TARGET\" \"$CLI_LINK\"\necho '2.2.3 (Claude Code)'\n"
	if err := os.WriteFile(old, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(next, []byte("#!/bin/sh\necho '2.2.4 (Claude Code)'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(old, link); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + dir, "CLI_LINK=" + link, "CLI_TARGET=" + next}
	if got, err := probeCLIIdentity("claude", link, env, dir); err == nil || got != nil {
		t.Fatalf("accepted launcher changed during probe: %+v %v", got, err)
	}
}

func TestCLIIdentityNewer(t *testing.T) {
	for _, tc := range []struct {
		new, old string
		want     bool
	}{
		{"2.2.4", "2.2.3", true}, {"2.0.0", "1.99.99", true}, {"1.10.0", "1.9.9", true},
		{"2.2.3", "2.2.3", false}, {"1.2.2", "2.2.3", false}, {"2.2.4-beta", "2.2.3", false},
		{"2.2.4", "2.2.3-beta", false}, {"1.2", "1.1.0", false}, {"v2.0.0", "1.0.0", false},
		{"18446744073709551616.0.0", "1.0.0", false},
	} {
		t.Run(tc.new+"/"+tc.old, func(t *testing.T) {
			if got := cliIdentityNewer(&persist.CLIIdentity{Version: tc.new}, &persist.CLIIdentity{Version: tc.old}); got != tc.want {
				t.Fatalf("got %v", got)
			}
		})
	}
	if cliIdentityNewer(nil, &persist.CLIIdentity{}) || cliIdentityNewer(&persist.CLIIdentity{}, nil) {
		t.Fatal("nil considered upgrade")
	}
}

func TestCLIIdentityCodexPrereleaseIsUnknown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "codex")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho 'codex-cli 2.1.3-beta.1'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if got, err := probeCLIIdentity("codex", path, []string{"PATH=" + dir}, dir); err == nil || got != nil {
		t.Fatalf("prerelease accepted: %+v %v", got, err)
	}
}
