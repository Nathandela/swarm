package main

// FAILING-FIRST suite for ADR-010 Phase 2 PIECE 2: `swarm spawn`, the agent-facing
// verb that launches a NEW session with continuity of context (D1/D2, Amendment A4).
//
//	swarm spawn --cli <agent> [--dir d] [--model m] [--worktree] [--name n]
//	            (--prompt <text> | --handoff <file> | --delegate <file>)
//
// FROZEN API (the Phase 1 pattern — a narrow, stub-friendly daemon surface so every
// verb is unit testable with no daemon and no socket):
//
//	type agentClient interface {
//	    List() ([]protocol.SessionView, error)
//	    Subscribe() (<-chan protocol.Event, error)
//	    Kill(id string) error
//	    Launch(protocol.LaunchReq) (id, name string, err error)   // == protocol.Client.Launch
//	}
//
//	func runSpawn(args []string, c agentClient, stdout, stderr io.Writer) int
//
// Frozen behavior:
//   - --cli is required; --dir defaults to the caller's cwd; --model becomes
//     Options["model"]; --worktree sets LaunchReq.Worktree; --name is optional (the
//     daemon defaults it).
//   - EXACTLY ONE of --prompt/--handoff/--delegate. --prompt: InitialPrompt is the
//     text verbatim, intent "delegate". --handoff/--delegate: the file is COPIED to a
//     private <os.TempDir()>/swarm-handoff-*/ directory of its own (0700, one per
//     handoff; ADR-010 Amendment 5 F3) as <timestamped-unique>.md and InitialPrompt is
//     the one-line pointer "Read and follow the instructions in <abs dest>." — so
//     instructions never travel as argv (A4).
//   - SpawnedFrom comes from SWARM_SESSION_ID; when the caller is a plain terminal
//     (no session env) NEITHER lineage field is set — an intent without a parent is
//     refused by the daemon (handleLaunch), so the human path must send neither.
//   - Env is os.Environ() (the daemon allowlist-filters); Cols 80, Rows 24.
//   - Success prints the new session id on stdout (name on stderr for humans);
//     errors exit 1; flag misuse exits 2 (the runLS convention).
//
// RED today: runSpawn and the agentClient.Launch method do not exist, so the package
// fails to compile on the undefined production symbols.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/protocol"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// Launch on the Phase 1 fake keeps it satisfying the widened agentClient interface
// without touching its file. The spawn tests use fakeSpawnClient, which records; a
// call landing here is a wiring mistake, so it reports one.
func (f *fakeAgentClient) Launch(protocol.LaunchReq) (string, string, error) {
	return "", "", errStub("fakeAgentClient.Launch: the spawn tests use fakeSpawnClient")
}

// fakeSpawnClient records every LaunchReq the verb builds — the whole point of these
// tests is the exact request shape — and can inject a daemon-side failure.
type fakeSpawnClient struct {
	*fakeAgentClient

	mu       sync.Mutex
	launches []protocol.LaunchReq
	id       string
	name     string
	err      error
}

func newFakeSpawnClient() *fakeSpawnClient {
	return &fakeSpawnClient{fakeAgentClient: newFakeAgentClient(), id: "local/new1", name: "spawned-session"}
}

func (f *fakeSpawnClient) Launch(req protocol.LaunchReq) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.launches = append(f.launches, req)
	if f.err != nil {
		return "", "", f.err
	}
	return f.id, f.name, nil
}

func (f *fakeSpawnClient) reqs() []protocol.LaunchReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]protocol.LaunchReq(nil), f.launches...)
}

// The fake must satisfy the WIDENED narrow interface — that interface is the whole
// testability contract (no daemon, no socket).
var _ agentClient = (*fakeSpawnClient)(nil)

// onlyLaunch returns the single LaunchReq the verb must have issued.
func onlyLaunch(t *testing.T, c *fakeSpawnClient) protocol.LaunchReq {
	t.Helper()
	got := c.reqs()
	if len(got) != 1 {
		t.Fatalf("Launch called %d times, want exactly 1", len(got))
	}
	return got[0]
}

// useTempHandoffRoot points os.TempDir -- where each handoff copy gets a private
// directory of its own (ADR-010 Amendment 5 F3) -- at a per-test root, and returns it.
func useTempHandoffRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	return root
}

// handoffDirs lists the private handoff directories under root.
func handoffDirs(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "swarm-handoff-") {
			dirs = append(dirs, filepath.Join(root, e.Name()))
		}
	}
	return dirs
}

// handoffCopies lists the copied documents under root, checking the contract on the
// way: one 0700 directory per handoff holding exactly one copied document.
func handoffCopies(t *testing.T, root string) []string {
	t.Helper()
	var copies []string
	for _, dir := range handoffDirs(t, root) {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Errorf("%s mode = %04o, want 0700", dir, perm)
		}
		files, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(files) != 1 {
			t.Fatalf("%s holds %d entries, want exactly one copied document", dir, len(files))
		}
		copies = append(copies, filepath.Join(dir, files[0].Name()))
	}
	return copies
}

// inSession sets the per-session env the daemon injects into every agent, so the
// verb sees the caller's session id exactly as a real in-session agent would.
func inSession(t *testing.T, local string) {
	t.Helper()
	t.Setenv(hookclient.EnvSessionID, local)
}

// ---------------------------------------------------------------------------
// --prompt: the inline-instructions path
// ---------------------------------------------------------------------------

// TestRunSpawn_PromptBuildsLaunchReq pins the exact request the verb composes for
// each flag combination: agent, cwd, options, worktree, name and prompt, plus the
// invariants every spawn carries — the mobile-default 80x24 grid, the caller's
// environment (the daemon allowlist-filters it), and the "delegate" intent linked to
// the caller's own session id.
func TestRunSpawn_PromptBuildsLaunchReq(t *testing.T) {
	dir := t.TempDir()

	cases := []struct {
		name  string
		args  []string
		check func(t *testing.T, req protocol.LaunchReq)
	}{
		{
			name: "minimal",
			args: []string{"--cli", "claude", "--dir", dir, "--prompt", "refactor the parser"},
			check: func(t *testing.T, req protocol.LaunchReq) {
				if req.Agent != "claude" {
					t.Errorf("Agent = %q, want %q", req.Agent, "claude")
				}
				if req.Cwd != dir {
					t.Errorf("Cwd = %q, want the --dir value %q", req.Cwd, dir)
				}
				if req.InitialPrompt != "refactor the parser" {
					t.Errorf("InitialPrompt = %q, want the --prompt text verbatim", req.InitialPrompt)
				}
				if req.Worktree {
					t.Error("Worktree = true without --worktree")
				}
				if req.Name != "" {
					t.Errorf("Name = %q without --name, want empty (the daemon defaults it)", req.Name)
				}
				if req.Options["model"] != "" {
					t.Errorf("Options[model] = %q without --model, want unset", req.Options["model"])
				}
			},
		},
		{
			name: "model becomes a launch option",
			args: []string{"--cli", "codex", "--dir", dir, "--model", "gpt-5.5", "--prompt", "go"},
			check: func(t *testing.T, req protocol.LaunchReq) {
				if req.Options["model"] != "gpt-5.5" {
					t.Errorf("Options[model] = %q, want %q", req.Options["model"], "gpt-5.5")
				}
			},
		},
		{
			name: "worktree toggle",
			args: []string{"--cli", "claude", "--dir", dir, "--worktree", "--prompt", "go"},
			check: func(t *testing.T, req protocol.LaunchReq) {
				if !req.Worktree {
					t.Error("Worktree = false with --worktree; the toggle must reach the wire")
				}
			},
		},
		{
			name: "name",
			args: []string{"--cli", "claude", "--dir", dir, "--name", "parser-work", "--prompt", "go"},
			check: func(t *testing.T, req protocol.LaunchReq) {
				if req.Name != "parser-work" {
					t.Errorf("Name = %q, want %q", req.Name, "parser-work")
				}
			},
		},
		{
			name: "a multi-line prompt travels verbatim",
			args: []string{"--cli", "claude", "--dir", dir, "--prompt", "line one\nline two  --not-a-flag"},
			check: func(t *testing.T, req protocol.LaunchReq) {
				if req.InitialPrompt != "line one\nline two  --not-a-flag" {
					t.Errorf("InitialPrompt = %q, want the text verbatim (no re-parsing, no trimming)", req.InitialPrompt)
				}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			useTempHandoffRoot(t)
			inSession(t, "sess-parent-1")
			t.Setenv("SWARM_SPAWN_ENV_MARKER", "present")

			client := newFakeSpawnClient()
			var stdout, stderr bytes.Buffer
			if exit := runSpawn(c.args, client, &stdout, &stderr); exit != 0 {
				t.Fatalf("runSpawn(%v) exit = %d, want 0; stderr=%q", c.args, exit, stderr.String())
			}
			req := onlyLaunch(t, client)

			// Invariants every spawn carries.
			if req.Cols != 80 || req.Rows != 24 {
				t.Errorf("grid = %dx%d, want 80x24 (the mobile default)", req.Cols, req.Rows)
			}
			if req.SpawnedFrom != "sess-parent-1" {
				t.Errorf("SpawnedFrom = %q, want the caller's %s value %q", req.SpawnedFrom, hookclient.EnvSessionID, "sess-parent-1")
			}
			if req.SpawnIntent != "delegate" {
				t.Errorf("SpawnIntent = %q, want %q for --prompt", req.SpawnIntent, "delegate")
			}
			if !envHas(req.Env, "SWARM_SPAWN_ENV_MARKER=present") {
				t.Errorf("Env does not carry the caller's environment (os.Environ; the daemon allowlist-filters it)")
			}
			c.check(t, req)
		})
	}
}

// TestRunSpawn_DirDefaultsToCallerCwd: an agent that omits --dir means "here".
func TestRunSpawn_DirDefaultsToCallerCwd(t *testing.T) {
	useTempHandoffRoot(t)
	inSession(t, "sess-parent-1")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	client := newFakeSpawnClient()
	var stdout, stderr bytes.Buffer
	if exit := runSpawn([]string{"--cli", "claude", "--prompt", "go"}, client, &stdout, &stderr); exit != 0 {
		t.Fatalf("runSpawn exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	if got := onlyLaunch(t, client).Cwd; got != wd {
		t.Errorf("Cwd = %q, want the caller's cwd %q", got, wd)
	}
}

// TestRunSpawn_NoSessionEnvSendsNoLineage: run by a human from a plain terminal there
// is no parent session, so NEITHER lineage field is sent. An intent without a
// spawned_from is refused server-side (handleLaunch), so sending one here would make
// every human-run spawn fail.
func TestRunSpawn_NoSessionEnvSendsNoLineage(t *testing.T) {
	useTempHandoffRoot(t)
	t.Setenv(hookclient.EnvSessionID, "")    // registers the restore...
	_ = os.Unsetenv(hookclient.EnvSessionID) // ...then genuinely unset it

	client := newFakeSpawnClient()
	var stdout, stderr bytes.Buffer
	if exit := runSpawn([]string{"--cli", "claude", "--dir", t.TempDir(), "--prompt", "go"}, client, &stdout, &stderr); exit != 0 {
		t.Fatalf("runSpawn exit = %d, want 0 (a human-run spawn is legal); stderr=%q", exit, stderr.String())
	}
	req := onlyLaunch(t, client)
	if req.SpawnedFrom != "" {
		t.Errorf("SpawnedFrom = %q with no session env, want empty", req.SpawnedFrom)
	}
	if req.SpawnIntent != "" {
		t.Errorf("SpawnIntent = %q with no parent session, want empty — the daemon refuses an intent without a spawned_from", req.SpawnIntent)
	}
}

// ---------------------------------------------------------------------------
// --handoff / --delegate: the document path
// ---------------------------------------------------------------------------

// TestRunSpawn_HandoffCopiesFileAndPointsPrompt pins D2's mechanics for both flags,
// which differ ONLY in recorded intent: the agent-authored document is COPIED under
// the swarm state dir (never left in the repo, never read from a path the source
// session may rewrite), and the child's initial prompt is a one-line POINTER at the
// copy — the instructions themselves never travel as argv (A4).
func TestRunSpawn_HandoffCopiesFileAndPointsPrompt(t *testing.T) {
	const body = "# Handoff\n\nHANDOFF-BODY-MARKER: finish the parser refactor.\n"

	for _, tc := range []struct {
		flag   string
		intent string
	}{
		{"--handoff", "handoff"},
		{"--delegate", "delegate"},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			root := useTempHandoffRoot(t)
			inSession(t, "sess-parent-1")

			src := filepath.Join(t.TempDir(), "handoff.md")
			if err := os.WriteFile(src, []byte(body), 0o600); err != nil {
				t.Fatalf("write source handoff: %v", err)
			}

			client := newFakeSpawnClient()
			var stdout, stderr bytes.Buffer
			args := []string{"--cli", "claude", "--dir", t.TempDir(), tc.flag, src}
			if exit := runSpawn(args, client, &stdout, &stderr); exit != 0 {
				t.Fatalf("runSpawn(%v) exit = %d, want 0; stderr=%q", args, exit, stderr.String())
			}

			copies := handoffCopies(t, root)
			if len(copies) != 1 {
				t.Fatalf("handoff copies %v, want exactly one copied document", copies)
			}
			dest := copies[0]
			if !strings.HasSuffix(dest, ".md") {
				t.Errorf("copied handoff %q does not end in .md", dest)
			}
			if dest == src {
				t.Fatalf("the handoff must be COPIED into a private directory, not referenced in place (%s)", src)
			}
			got, err := os.ReadFile(dest)
			if err != nil {
				t.Fatalf("read copied handoff: %v", err)
			}
			if string(got) != body {
				t.Errorf("copied handoff content = %q, want the source verbatim", string(got))
			}

			req := onlyLaunch(t, client)
			want := "Read and follow the instructions in " + dest + "."
			if req.InitialPrompt != want {
				t.Errorf("InitialPrompt = %q, want the one-line pointer %q", req.InitialPrompt, want)
			}
			if strings.Contains(req.InitialPrompt, "HANDOFF-BODY-MARKER") {
				t.Errorf("the document body travelled in the prompt (%q); instructions must never travel as argv (A4)", req.InitialPrompt)
			}
			if req.SpawnIntent != tc.intent {
				t.Errorf("SpawnIntent = %q, want %q for %s", req.SpawnIntent, tc.intent, tc.flag)
			}
			if req.SpawnedFrom != "sess-parent-1" {
				t.Errorf("SpawnedFrom = %q, want the caller's session id", req.SpawnedFrom)
			}
		})
	}
}

// TestRunSpawn_HandoffCopiesGetUniqueNames: two handoffs from the same source file
// must not collide — the second would otherwise overwrite the document the first
// child is still being told to read.
func TestRunSpawn_HandoffCopiesGetUniqueNames(t *testing.T) {
	root := useTempHandoffRoot(t)
	inSession(t, "sess-parent-1")

	src := filepath.Join(t.TempDir(), "handoff.md")
	if err := os.WriteFile(src, []byte("one"), 0o600); err != nil {
		t.Fatalf("write source handoff: %v", err)
	}

	client := newFakeSpawnClient()
	for i := 0; i < 2; i++ {
		var stdout, stderr bytes.Buffer
		args := []string{"--cli", "claude", "--dir", t.TempDir(), "--handoff", src}
		if exit := runSpawn(args, client, &stdout, &stderr); exit != 0 {
			t.Fatalf("runSpawn #%d exit = %d, want 0; stderr=%q", i+1, exit, stderr.String())
		}
	}

	if copies := handoffCopies(t, root); len(copies) != 2 {
		t.Fatalf("handoff copies %v after two spawns, want two distinct copies", copies)
	}
	reqs := client.reqs()
	if len(reqs) != 2 || reqs[0].InitialPrompt == reqs[1].InitialPrompt {
		t.Fatalf("both spawns pointed at the same document: %q", reqs[0].InitialPrompt)
	}
}

// TestRunSpawn_MissingHandoffFileIsAnError: an unreadable document is exit 1 with the
// path named, and NOTHING is launched — a child told to read a file that does not
// exist is worse than no child.
func TestRunSpawn_MissingHandoffFileIsAnError(t *testing.T) {
	useTempHandoffRoot(t)
	inSession(t, "sess-parent-1")
	missing := filepath.Join(t.TempDir(), "nope.md")

	client := newFakeSpawnClient()
	var stdout, stderr bytes.Buffer
	exit := runSpawn([]string{"--cli", "claude", "--dir", t.TempDir(), "--handoff", missing}, client, &stdout, &stderr)
	if exit != 1 {
		t.Fatalf("runSpawn with a missing handoff exit = %d, want 1; stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), missing) {
		t.Errorf("stderr = %q, want the unreadable path named", stderr.String())
	}
	if n := len(client.reqs()); n != 0 {
		t.Errorf("Launch called %d times after an unreadable handoff; want 0", n)
	}
}

// ---------------------------------------------------------------------------
// Flag misuse and error paths
// ---------------------------------------------------------------------------

// TestRunSpawn_FlagMisuse pins exit 2 (the runLS convention) for every argument-level
// refusal, each of which must refuse BEFORE dialing anything.
func TestRunSpawn_FlagMisuse(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		args []string
		want string // substring the message must carry
	}{
		{"missing --cli", []string{"--dir", dir, "--prompt", "go"}, "cli"},
		{"no instruction source", []string{"--cli", "claude", "--dir", dir}, "prompt"},
		{"prompt and handoff", []string{"--cli", "claude", "--dir", dir, "--prompt", "go", "--handoff", "/tmp/h.md"}, "prompt"},
		{"handoff and delegate", []string{"--cli", "claude", "--dir", dir, "--handoff", "/tmp/h.md", "--delegate", "/tmp/d.md"}, "handoff"},
		{"unknown flag", []string{"--cli", "claude", "--prompt", "go", "--bogus"}, "bogus"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			useTempHandoffRoot(t)
			inSession(t, "sess-parent-1")
			client := newFakeSpawnClient()
			var stdout, stderr bytes.Buffer
			if exit := runSpawn(c.args, client, &stdout, &stderr); exit != 2 {
				t.Fatalf("runSpawn(%v) exit = %d, want 2 (flag misuse); stdout=%q stderr=%q", c.args, exit, stdout.String(), stderr.String())
			}
			if n := len(client.reqs()); n != 0 {
				t.Errorf("Launch called %d times on an argument refusal; want 0", n)
			}
			if !strings.Contains(stderr.String(), c.want) {
				t.Errorf("stderr = %q, want a message naming %q", stderr.String(), c.want)
			}
		})
	}
}

// TestRunSpawn_LaunchError: a daemon refusal is exit 1 with the cause on stderr and
// no session id on stdout for a caller to parse.
func TestRunSpawn_LaunchError(t *testing.T) {
	useTempHandoffRoot(t)
	inSession(t, "sess-parent-1")

	client := newFakeSpawnClient()
	client.err = errFakeDaemon

	var stdout, stderr bytes.Buffer
	exit := runSpawn([]string{"--cli", "claude", "--dir", t.TempDir(), "--prompt", "go"}, client, &stdout, &stderr)
	if exit != 1 {
		t.Fatalf("runSpawn with a failing Launch exit = %d, want 1; stdout=%q", exit, stdout.String())
	}
	if !strings.Contains(stderr.String(), errFakeDaemon.Error()) {
		t.Errorf("stderr = %q, want the daemon error %q", stderr.String(), errFakeDaemon)
	}
	if strings.TrimSpace(stdout.String()) != "" {
		t.Errorf("stdout = %q, want nothing when the launch failed", stdout.String())
	}
}

// TestRunSpawn_PrintsSessionID: stdout carries the new session id ALONE (an agent
// pipes it straight into `swarm watch`), with the human-facing name on stderr.
func TestRunSpawn_PrintsSessionID(t *testing.T) {
	useTempHandoffRoot(t)
	inSession(t, "sess-parent-1")

	client := newFakeSpawnClient()
	client.id, client.name = "local/child9", "parser-work"

	var stdout, stderr bytes.Buffer
	if exit := runSpawn([]string{"--cli", "claude", "--dir", t.TempDir(), "--prompt", "go"}, client, &stdout, &stderr); exit != 0 {
		t.Fatalf("runSpawn exit = %d, want 0; stderr=%q", exit, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "local/child9" {
		t.Errorf("stdout = %q, want the bare session id %q", stdout.String(), "local/child9")
	}
	if !strings.Contains(stderr.String(), "parser-work") {
		t.Errorf("stderr = %q, want the session name for humans", stderr.String())
	}
}

// TestUsage_ListsSpawnVerb pins that `swarm spawn` is discoverable from bare `swarm`,
// as the Phase 1 verbs are.
func TestUsage_ListsSpawnVerb(t *testing.T) {
	if !strings.Contains(usage, "swarm spawn") {
		t.Errorf("usage does not document `swarm spawn`; got:\n%s", usage)
	}
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

func envHas(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}
