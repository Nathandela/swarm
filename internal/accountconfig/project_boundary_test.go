package accountconfig

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func homeProject(t *testing.T, f *projectionFixture) {
	t.Helper()
	f.cwd = filepath.Join(f.home, "projects", "repository")
	if err := os.MkdirAll(f.cwd, 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestProjectBoundaryClaudeFreezesHomeAndSelectedAlias(t *testing.T) {
	f := fixture(t, "claude")
	homeProject(t, &f)
	selected, next := filepath.Join(filepath.Dir(f.root), "selected"), filepath.Join(filepath.Dir(f.root), "next")
	for _, p := range []string{selected, next} {
		put(t, filepath.Join(p, "settings.json"), `{"model":"claude-sonnet-4-6"}`)
	}
	alias := filepath.Join(filepath.Dir(f.root), "selected-alias")
	if err := os.Symlink(selected, alias); err != nil {
		t.Fatal(err)
	}
	f.env = append(f.env, "CLAUDE_CONFIG_DIR="+alias)
	put(t, filepath.Join(f.source, "settings.json"), `{"hooks":{"Stop":[]}}`)
	put(t, filepath.Join(f.source, "settings.local.json"), `{"hooks":{"SessionStart":[]}}`)
	p, err := prepareTestProjection(f.root, "claude", f.candidate, f.cwd, f.env, []string{"claude"}, "")
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(f.source, "settings.json"), `{"hooks":{"PreToolUse":[]}}`)
	if err := Revalidate(f.root, p.Ref, "claude", f.candidate, f.cwd); err != nil {
		t.Fatalf("unselected HOME settings gained authority: %v", err)
	}
	resume, err := prepareTestProjection(f.root, "claude", f.candidate, f.cwd, f.env, []string{"claude", "--resume", "synthetic"}, p.Ref)
	if err != nil || resume.Ref != p.Ref || resume.Generation != p.Generation {
		t.Fatalf("unchanged owned alias lost frozen projection: %v", err)
	}
	changed := append([]string(nil), f.env...)
	changed[0] = "HOME=" + filepath.Dir(f.home)
	if _, err := prepareTestProjection(f.root, "claude", f.candidate, f.cwd, changed, []string{"claude"}, p.Ref); err == nil {
		t.Fatal("resume accepted changed HOME boundary")
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(next, alias); err != nil {
		t.Fatal(err)
	}
	if err := Revalidate(f.root, p.Ref, "claude", f.candidate, f.cwd); err == nil {
		t.Fatal("retargeted selected settings alias accepted")
	}
}

func TestProjectBoundaryClaudeRetainsGenuineSources(t *testing.T) {
	for _, location := range []string{"cwd", "ancestor", "home-mcp", "home-cwd"} {
		t.Run(location, func(t *testing.T) {
			f := fixture(t, "claude")
			homeProject(t, &f)
			path := filepath.Join(f.cwd, ".claude", "settings.json")
			switch location {
			case "ancestor":
				path = filepath.Join(filepath.Dir(f.cwd), ".claude", "settings.local.json")
			case "home-mcp":
				path = filepath.Join(f.home, ".mcp.json")
			case "home-cwd":
				f.cwd = f.home
				path = filepath.Join(f.home, ".claude", "settings.json")
			}
			put(t, path, `{}`)
			if _, err := prepareTestProjection(f.root, "claude", f.candidate, f.cwd, f.env, []string{"claude"}, ""); err == nil {
				t.Fatal("genuine project source accepted")
			}
		})
	}
	f := fixture(t, "claude")
	homeProject(t, &f)
	put(t, filepath.Join(f.source, "settings.json"), `{"model":"claude-sonnet-4-6"}`)
	p, err := prepareTestProjection(f.root, "claude", f.candidate, f.cwd, f.env, []string{"claude"}, "")
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(f.source, "settings.json"), `{"model":"claude-opus-4-6"}`)
	if err := Revalidate(f.root, p.Ref, "claude", f.candidate, f.cwd); err == nil {
		t.Fatal("selected original global settings stopped being frozen")
	}
}

func TestProjectBoundaryClaudeContainedCheckUnderHome(t *testing.T) {
	f, policy, _ := availabilityFixture(t)
	f.root = filepath.Join(f.home, ".local", "state", "swarm")
	f.candidate = filepath.Join(f.root, "accounts", "profiles", "synthetic")
	f.cwd = filepath.Join(f.root, "accounts", "checks", "synthetic", "native-cwd")
	for _, p := range []string{f.candidate, f.cwd} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{"HOME=" + f.home, "CLAUDE_CONFIG_DIR=" + f.candidate, "CLAUDE_SECURESTORAGE_CONFIG_DIR=" + f.candidate}
	put(t, filepath.Join(f.source, "settings.json"), `{"hooks":{"Stop":[]}}`)
	put(t, filepath.Join(f.source, "settings.local.json"), `{"hooks":{"SessionStart":[]}}`)
	if err := validateAvailabilityCheck(f.root, f.candidate, f.cwd, env, policy); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(f.home, ".mcp.json"), `{}`)
	if err := validateAvailabilityCheck(f.root, f.candidate, f.cwd, env, policy); err == nil {
		t.Fatal("contained check bypassed HOME MCP source")
	}
}

func TestProjectBoundaryCodexRangeAndDefaultMarkers(t *testing.T) {
	for _, kind := range []string{"repository", "no-root", "empty-marker"} {
		t.Run(kind, func(t *testing.T) {
			f := fixture(t, "codex")
			homeProject(t, &f)
			if kind == "repository" {
				put(t, filepath.Join(f.cwd, ".git", "HEAD"), "ref: refs/heads/main\n")
			}
			if kind == "empty-marker" {
				if err := os.Mkdir(filepath.Join(f.cwd, ".git"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			put(t, filepath.Join(f.source, "config.toml"), `model="gpt-current"`)
			// Not a native project layer in any of these layouts.
			put(t, filepath.Join(filepath.Dir(f.cwd), ".codex", "config.toml"), "[hooks]\n")
			p, err := prepareTestProjection(f.root, "codex", f.candidate, f.cwd, f.env, []string{"codex"}, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := Revalidate(f.root, p.Ref, "codex", f.candidate, f.cwd); err != nil {
				t.Fatal(err)
			}
			put(t, filepath.Join(f.cwd, ".codex", "config.toml"), "[hooks]\n")
			if err := Revalidate(f.root, p.Ref, "codex", f.candidate, f.cwd); err == nil {
				t.Fatal("current native project source bypassed guard")
			}
		})
	}
	// HOME is not exempt when it really is the native repository root.
	f := fixture(t, "codex")
	homeProject(t, &f)
	put(t, filepath.Join(f.home, ".git", "HEAD"), "ref: refs/heads/main\n")
	put(t, filepath.Join(f.source, "config.toml"), `model="gpt-current"`)
	if _, err := prepareTestProjection(f.root, "codex", f.candidate, f.cwd, f.env, []string{"codex"}, ""); err == nil {
		t.Fatal("real HOME project root was blindly exempted")
	}
}

func TestProjectBoundaryCodexMarkerChangesHoldButOrdinaryHEADDoesNot(t *testing.T) {
	for _, change := range []string{"new-cwd-marker", "removed-root-head", "changed-head", "no-root-new-marker"} {
		t.Run(change, func(t *testing.T) {
			f := fixture(t, "codex")
			homeProject(t, &f)
			root := filepath.Dir(f.cwd)
			if change != "no-root-new-marker" {
				put(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")
			}
			p, err := prepareTestProjection(f.root, "codex", f.candidate, f.cwd, f.env, []string{"codex"}, "")
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "changed-head":
				put(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/other\n")
			case "removed-root-head":
				if err := os.Remove(filepath.Join(root, ".git", "HEAD")); err != nil {
					t.Fatal(err)
				}
			default:
				put(t, filepath.Join(f.cwd, ".git", "HEAD"), "ref: refs/heads/nested\n")
			}
			err = Revalidate(f.root, p.Ref, "codex", f.candidate, f.cwd)
			if (change == "changed-head") != (err == nil) {
				t.Fatalf("marker replay %s: %v", change, err)
			}
			_, resumeErr := prepareTestProjection(f.root, "codex", f.candidate, f.cwd, f.env, []string{"codex", "resume", "synthetic"}, p.Ref)
			if (change == "changed-head") != (resumeErr == nil) {
				t.Fatalf("prior-reference marker replay %s: %v", change, resumeErr)
			}
		})
	}
}

func TestProjectBoundaryCodexSelectedNativeProfileIsExcluded(t *testing.T) {
	f := fixture(t, "codex")
	homeProject(t, &f)
	f.candidate = filepath.Join(f.cwd, ".codex")
	put(t, filepath.Join(f.candidate, "config.toml"), `model="gpt-destination-default"`)
	p, err := prepareTestProjection(f.root, "codex", f.candidate, f.cwd, f.env, []string{"codex", "--model", "gpt-requested"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := Revalidate(f.root, p.Ref, "codex", f.candidate, f.cwd); err != nil {
		t.Fatal(err)
	}
	// Moving auth selection out of cwd makes its former config a real native
	// project source, so the same frozen manifest must now refuse it.
	other := filepath.Join(filepath.Dir(f.root), "other-profile")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Revalidate(f.root, p.Ref, "codex", other, f.cwd); err == nil {
		t.Fatal("former selected profile became an unchecked project source")
	}
}

func TestProjectBoundaryCodexRefusesUnprovenMarkerLayouts(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo", "world-writable", "unproven-pointer"} {
		t.Run(kind, func(t *testing.T) {
			f := fixture(t, "codex")
			path := filepath.Join(f.cwd, ".git")
			switch kind {
			case "symlink":
				if err := os.Symlink(f.source, path); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				cmd := exec.Command("mkfifo", path)
				if err := cmd.Run(); err != nil {
					t.Fatal(err)
				}
			case "world-writable":
				put(t, path, "gitdir: unsupported\n")
				if err := os.Chmod(path, 0o666); err != nil {
					t.Fatal(err)
				}
			case "unproven-pointer":
				put(t, path, "gitdir: unsupported\n")
			}
			if _, err := prepareTestProjection(f.root, "codex", f.candidate, f.cwd, f.env, []string{"codex"}, ""); err == nil {
				t.Fatal("unsafe or uncertain marker granted a project boundary")
			}
		})
	}
}

func gitWorktreeFixture(t *testing.T, f *projectionFixture) string {
	t.Helper()
	repo := filepath.Join(f.home, "main-repository")
	work := filepath.Join(repo, ".swarm", "worktrees", "synthetic")
	env := []string{}
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			env = append(env, e)
		}
	}
	env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test")
	for _, args := range [][]string{{"init", "-q", repo}, {"-C", repo, "commit", "-q", "--allow-empty", "-m", "fixture"}, {"-C", repo, "worktree", "add", "-q", "--detach", work}} {
		cmd := exec.Command("git", args...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("synthetic git fixture: %v: %s", err, out)
		}
	}
	f.cwd = filepath.Join(work, "nested")
	if err := os.Mkdir(f.cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestProjectBoundaryCodexWorktreeKeepsMainHooksAndPointerProof(t *testing.T) {
	for _, change := range []string{"root-hooks", "nested-hooks", "commondir", "backpointer", "git-pointer"} {
		t.Run(change, func(t *testing.T) {
			f := fixture(t, "codex")
			repo := gitWorktreeFixture(t, &f)
			p, err := prepareTestProjection(f.root, "codex", f.candidate, f.cwd, f.env, []string{"codex"}, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := Revalidate(f.root, p.Ref, "codex", f.candidate, f.cwd); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "root-hooks":
				put(t, filepath.Join(repo, ".codex", "config.toml"), "[hooks]\n")
			case "nested-hooks":
				put(t, filepath.Join(repo, "nested", ".codex", "config.toml"), "[hooks]\n")
			case "commondir":
				put(t, filepath.Join(repo, ".git", "worktrees", "synthetic", "commondir"), "../unproven\n")
			case "backpointer":
				put(t, filepath.Join(repo, ".git", "worktrees", "synthetic", "gitdir"), filepath.Join(repo, "unproven", ".git"))
			case "git-pointer":
				put(t, filepath.Join(filepath.Dir(f.cwd), ".git"), "gitdir: missing\n")
			}
			if err := Revalidate(f.root, p.Ref, "codex", f.candidate, f.cwd); err == nil {
				t.Fatal("changed worktree hook/marker authority accepted")
			}
		})
	}
}

func TestProjectBoundaryMetadataCompatibilityIsIntrinsic(t *testing.T) {
	f := fixture(t, "claude")
	homeProject(t, &f)
	p, err := prepareTestProjection(f.root, "claude", f.candidate, f.cwd, f.env, []string{"claude"}, "")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(f.root, "accounts", "configurations", p.Ref, "projection.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Intrinsic validation succeeds even when mutable source settings disappear.
	if err := os.RemoveAll(f.home); err != nil {
		t.Fatal(err)
	}
	if got, err := ProjectionCompatibility(raw); err != nil || got != 2 {
		t.Fatalf("new capability: %d %v", got, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "ProjectBoundary")
	legacy, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ProjectionCompatibility(legacy); err != nil || got != 1 {
		t.Fatalf("legacy capability: %d %v", got, err)
	}
	for _, bad := range []string{"null", "true", "[]", `{}`, `{"Home":"relative"}`, `{"Home":"` + f.cwd + `"}`, `{"Home":"` + f.home + `","Unknown":true}`} {
		fields["ProjectBoundary"] = json.RawMessage(bad)
		invalid, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ProjectionCompatibility(invalid); err == nil {
			t.Fatalf("malformed boundary accepted: %s", bad)
		}
	}
}
