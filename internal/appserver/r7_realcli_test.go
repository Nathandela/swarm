//go:build realcli

package appserver_test

// THE REAL-CLI GATE for Wave R7 (hard rule 7, the established D1/D2 pattern):
// `//go:build realcli` + `SWARM_REALCLI=1`, NEVER run in CI. Every CI-facing R7 test is
// fixture-driven, and the fixtures are RECORDED from the real protocol, never invented.
//
// RUNNING THIS COSTS THE OWNER MONEY AND TOUCHES A REAL ACCOUNT. The installed CLI is
// codex-cli 0.147.0 at /usr/local/bin/codex with real ChatGPT auth. This file therefore
// requires an ISOLATED CODEX_HOME and a SCRATCH WORKSPACE, exactly as the R1 gate did
// (r1-codex-gate.md:10-11), refuses to run without them, and keeps the live work to the
// minimum that proves the integration.
//
//	SWARM_REALCLI=1 \
//	SWARM_CODEX_HOME=$SCRATCH/codex-home \
//	SWARM_CODEX_WS=$SCRATCH/ws \
//	go test -tags realcli -run TestR7RealCLI -count=1 ./internal/appserver/
//
// IT EXISTS TO DISCHARGE TWO NAMED OBLIGATIONS, both recorded as open in
// docs/verification/r1-codex-fixtures/r7-open-questions.md:
//
//   - Q3, ROLLOUT-TO-RESUME. ADR-013 §R7.2e makes this the ONE measurement R7 owes before the
//     no-flag path is relied on. The rejected draft called the join window "wide" on the basis
//     of the recorded ~2.1 s from turn/start to first delta; that is the WRONG QUANTITY. The
//     one that binds is: how long after a thread's FIRST TURN STARTS does thread/resume stop
//     returning `no rollout found for thread id`? The gate's 15-17 s is BOOT-to-resume and does
//     not answer it. If the measured number exceeds the first turn's duration, the no-flag path
//     emits a structured_gap for that turn rather than pretending to have seen it.
//   - Q4, whether thread/read {includeTurns:true} backfills LOSSLESSLY. The binding says
//     Thread.turns is populated and TurnItemsView has a `full` member; it does not say which
//     view thread/read chooses, and the gate observed `summary` on one turn/completed and
//     `notLoaded` on another. A `summary` backfill is lossy for a long turn, and that decides
//     whether a daemon restart is silent or is an honest structured_gap (§R7.6).

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/Nathandela/swarm/internal/appserver"
)

// r7RealEnv refuses to run without the isolation the R1 gate used.
func r7RealEnv(t *testing.T) (codexHome, workspace string) {
	t.Helper()
	if os.Getenv("SWARM_REALCLI") != "1" {
		t.Skip("set SWARM_REALCLI=1 to drive the real codex CLI")
	}
	codexHome = os.Getenv("SWARM_CODEX_HOME")
	workspace = os.Getenv("SWARM_CODEX_WS")
	if codexHome == "" || workspace == "" {
		t.Fatal("SWARM_CODEX_HOME and SWARM_CODEX_WS are REQUIRED. Running this against the owner's " +
			"real ~/.codex would touch a live account's session store; the R1 gate used an isolated " +
			"CODEX_HOME and a scratch git workspace containing only README.md and this must too")
	}
	if strings.HasPrefix(codexHome, os.Getenv("HOME")+"/.codex") {
		t.Fatalf("SWARM_CODEX_HOME=%q is the owner's real codex home", codexHome)
	}
	return codexHome, workspace
}

// r7StartAppServer starts `codex app-server --listen unix://PATH` under the isolated home and
// waits for the socket to be SERVABLE -- never for a stream, because R1 leg 1 recorded that
// this process writes nothing to stdout or stderr for an entire session.
func r7StartAppServer(t *testing.T, codexHome string) string {
	t.Helper()
	// A SHORT /tmp PREFIX, not t.TempDir(): macOS caps sun_path at 104 bytes and Go's
	// per-test temp directory name alone blows through it, so every bind fails EINVAL and
	// the server appears never to start. Same reason, same fix, as the CI-facing rigs.
	dir, err := os.MkdirTemp("/tmp", "r7cx")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "c.sock")
	// SPAWNED THROUGH `arch -arm64`, and this is not incidental. The Go toolchain on this host
	// is amd64 under Rosetta, so a test-spawned UNIVERSAL binary inherits the translated
	// parent's x86_64 preference -- `node` then runs as x86_64, and codex's npm launcher dies
	// with "Missing optional dependency @openai/codex-darwin-x64" before it ever binds a
	// socket. The R1 gate never hit this because it drove codex from an arm64 Python. Without
	// the prefix this whole file fails as an unexplained "never became servable" timeout.
	cmd := exec.Command("/usr/bin/arch", "-arm64", "/usr/local/bin/codex", "app-server", "--listen", "unix://"+sock)
	cmd.Env = append(os.Environ(), "CODEX_HOME="+codexHome)
	// R1 leg 1 recorded that this process writes NOTHING to either stream for an entire
	// session -- so anything that does appear is a startup failure, and losing it turns
	// "never became servable" into an unexplainable 30 s timeout.
	log, lerr := os.Create(filepath.Join(dir, "app-server.log"))
	if lerr != nil {
		t.Fatalf("app-server log: %v", lerr)
	}
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatalf("start app-server: %v", err)
	}
	t.Cleanup(func() {
		_ = log.Close()
		if t.Failed() {
			if body, rerr := os.ReadFile(filepath.Join(dir, "app-server.log")); rerr == nil && len(body) > 0 {
				t.Logf("app-server said:\n%s", body)
			}
		}
	})
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			c, derr := appserver.Dial(ctx, sock, appserver.Options{})
			cancel()
			if derr == nil {
				_ = c.Close()
				return sock
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the app-server never served %s within 60s", sock)
	return ""
}

// TestR7RealCLI_RolloutToResumeIsMeasuredAndRecorded discharges Q3.
func TestR7RealCLI_RolloutToResumeIsMeasuredAndRecorded(t *testing.T) {
	home, ws := r7RealEnv(t)
	sock := r7StartAppServer(t, home)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	owner, err := appserver.Dial(ctx, sock, appserver.Options{})
	if err != nil {
		t.Fatalf("dial as the thread owner: %v", err)
	}
	defer func() { _ = owner.Close() }()
	if err := owner.Call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "swarm-r7", "title": "swarm-r7", "version": "0.0.1"},
		"capabilities": map[string]any{"experimentalApi": true, "requestAttestation": false},
	}, nil); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := owner.Notify(ctx, "initialized", map[string]any{}); err != nil {
		t.Fatalf("initialized: %v", err)
	}

	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := owner.Call(ctx, "thread/start", map[string]any{
		"cwd": ws, "sandbox": "read-only", "approvalPolicy": "on-request",
	}, &started); err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	threadID := started.Thread.ID
	if threadID == "" {
		t.Fatal("thread/start returned no thread id")
	}

	// The SMALLEST turn that creates a rollout. Keep the live work minimal: it costs money.
	turnStart := time.Now()
	if err := owner.Call(ctx, "turn/start", map[string]any{
		"threadId": threadID,
		"input":    []any{map[string]any{"type": "text", "text": "Reply with exactly the word OK.", "text_elements": []any{}}},
	}, nil); err != nil {
		t.Fatalf("turn/start: %v", err)
	}

	// A SECOND client, as the daemon would be, retrying thread/resume until the rollout exists.
	observer, err := appserver.Dial(ctx, sock, appserver.Options{})
	if err != nil {
		t.Fatalf("dial as the observer: %v", err)
	}
	defer func() { _ = observer.Close() }()
	if err := observer.Call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "swarm-r7-obs", "title": "swarm-r7-obs", "version": "0.0.1"},
		"capabilities": map[string]any{"experimentalApi": true, "requestAttestation": false},
	}, nil); err != nil {
		t.Fatalf("observer initialize: %v", err)
	}
	if err := observer.Notify(ctx, "initialized", map[string]any{}); err != nil {
		t.Fatalf("observer initialized: %v", err)
	}

	var rolloutToResume time.Duration
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		err := observer.Call(ctx, "thread/resume", map[string]any{"threadId": threadID}, nil)
		if err == nil {
			rolloutToResume = time.Since(turnStart)
			break
		}
		if !strings.Contains(err.Error(), "no rollout found") {
			t.Fatalf("thread/resume failed for a reason other than the rollout race: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if rolloutToResume == 0 {
		t.Fatal("thread/resume never succeeded within 2 minutes of the first turn starting; the " +
			"no-flag path of §R7.2e cannot be relied on at all and the handshake MUST carry a " +
			"pre-created thread id")
	}
	t.Logf("MEASURED rollout-to-resume: %s (turn/start -> thread/resume stops returning "+
		"`no rollout found for thread id`). ADR-013 §R7.2e's named obligation. The R1 gate's "+
		"15-17s figure was BOOT-to-resume and did not answer this.", rolloutToResume)

	if rolloutToResume > 10*time.Second {
		t.Errorf("rollout-to-resume is %s, which is long enough that the FIRST TURN of a no-flag "+
			"session is plausibly un-joinable. §R7.2e's rule then applies: emit a structured_gap "+
			"for that turn rather than pretending to have seen it", rolloutToResume)
	}
}

// TestR7RealCLI_ThreadReadWithIncludeTurnsBackfillsTheTurnsItems discharges Q4, and the answer
// decides whether a daemon restart on a Codex session is silent (§R7.6) or is an honest
// structured_gap (§R7.10 consequence 1).
func TestR7RealCLI_ThreadReadWithIncludeTurnsBackfillsTheTurnsItems(t *testing.T) {
	home, ws := r7RealEnv(t)
	sock := r7StartAppServer(t, home)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c, err := appserver.Dial(ctx, sock, appserver.Options{})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "swarm-r7", "title": "swarm-r7", "version": "0.0.1"},
		"capabilities": map[string]any{"experimentalApi": true, "requestAttestation": false},
	}, nil); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := c.Notify(ctx, "initialized", map[string]any{}); err != nil {
		t.Fatalf("initialized: %v", err)
	}

	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := c.Call(ctx, "thread/start", map[string]any{
		"cwd": ws, "sandbox": "read-only", "approvalPolicy": "on-request",
	}, &started); err != nil {
		t.Fatalf("thread/start: %v", err)
	}

	done := make(chan struct{})
	var once bool
	c2, err := appserver.Dial(ctx, sock, appserver.Options{
		OnNotify: func(method string, _ json.RawMessage) {
			if method == "turn/completed" && !once {
				once = true
				close(done)
			}
		},
	})
	if err != nil {
		t.Fatalf("dial the watcher: %v", err)
	}
	defer func() { _ = c2.Close() }()

	if err := c.Call(ctx, "turn/start", map[string]any{
		"threadId": started.Thread.ID,
		"input":    []any{map[string]any{"type": "text", "text": "Reply with exactly the word OK.", "text_elements": []any{}}},
	}, nil); err != nil {
		t.Fatalf("turn/start: %v", err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Minute):
		t.Fatal("the turn never completed")
	}

	var read struct {
		Thread struct {
			Turns []struct {
				ID        string            `json:"id"`
				ItemsView string            `json:"itemsView"`
				Items     []json.RawMessage `json:"items"`
			} `json:"turns"`
		} `json:"thread"`
	}
	if err := c.Call(ctx, "thread/read", map[string]any{
		"threadId": started.Thread.ID, "includeTurns": true,
	}, &read); err != nil {
		t.Fatalf("thread/read {includeTurns:true}: %v", err)
	}
	if len(read.Thread.Turns) == 0 {
		t.Fatal("thread/read {includeTurns:true} returned NO turns; there is no lossless post-outage " +
			"backfill, so every daemon outage on a Codex session is an honest structured_gap " +
			"(ADR-013 §R7.10 consequence 1) and the reconnect path must emit one")
	}
	last := read.Thread.Turns[len(read.Thread.Turns)-1]
	t.Logf("MEASURED thread/read backfill: itemsView=%q with %d items on the last turn",
		last.ItemsView, len(last.Items))
	if last.ItemsView != "full" {
		t.Errorf("thread/read returned itemsView=%q, not \"full\". A `summary` backfill is LOSSY for "+
			"a long turn, so §R7.6's silent reconnect is not available and the daemon must emit a "+
			"structured_gap for the un-backfillable interval instead", last.ItemsView)
	}
	if len(last.Items) == 0 {
		t.Error("the backfilled turn carries no items at all")
	}
}

// r7Frames is a thread-safe recorder for one connection's inbound notifications. The probe
// below asserts on WHAT ARRIVED WITHOUT BEING ASKED FOR, so every frame has to be kept.
type r7Frames struct {
	mu      sync.Mutex
	methods []string
	params  map[string][]json.RawMessage
}

func newR7Frames() *r7Frames {
	return &r7Frames{params: map[string][]json.RawMessage{}}
}

func (f *r7Frames) add(method string, params json.RawMessage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.methods = append(f.methods, method)
	f.params[method] = append(f.params[method], append(json.RawMessage(nil), params...))
}

func (f *r7Frames) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.params[method])
}

func (f *r7Frames) first(method string) (json.RawMessage, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.params[method]) == 0 {
		return nil, false
	}
	return f.params[method][0], true
}

// waitFor blocks until method has been seen at least once, or the deadline passes.
func (f *r7Frames) waitFor(method string, within time.Duration) (json.RawMessage, bool) {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if p, ok := f.first(method); ok {
			return p, true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, false
}

// r7ScratchWorkspace mirrors the R1 gate's scratch workspace: a git repo containing one file.
// codex refuses to run in an untrusted directory, and a repo it can trust is the cheapest way
// to a usable session that touches nothing of the owner's.
func r7ScratchWorkspace(t *testing.T, ws string) {
	t.Helper()
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatalf("scratch workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws, "README.md"), []byte("scratch\n"), 0o600); err != nil {
		t.Fatalf("scratch README: %v", err)
	}
	for _, args := range [][]string{{"init"}, {"add", "-A"}, {"-c", "user.email=s@s", "-c", "user.name=s", "commit", "-m", "scratch"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = ws
		_ = cmd.Run()
	}
}

// TestR7RealCLI_TheDaemonAdoptsTheTUIsOwnThreadAndSteersTheCLIsOwnTurnId is the topology probe
// ADR-013 §R7.2e's open question Q2 asks for, and the ONE live round-trip round 2 spends.
//
// IT ANSWERS THREE QUESTIONS THAT NO FIXTURE CAN, each of which was a round-1 defect:
//
//  1. TOPOLOGY (review BLOCKING 3). Round 1 called `thread/start` from the daemon and launched
//     the agent with `--remote unix://SOCK` and nothing else, so the agent created a SECOND
//     thread and the two surfaces were never on one conversation. This probe connects the
//     daemon FIRST, starts NO thread, launches the real TUI, and asserts that (a) the daemon
//     is told the TUI's thread id by `thread/started`, (b) exactly ONE thread exists, and
//     (c) the daemon's own `turn/start` on that thread is accepted.
//  2. THE STREAM WITHOUT A RESUME. The R1 gate's observer connected AFTER the TUI and had to
//     `thread/resume` (and race the rollout file for 15-17 s). A client connected BEFORE the
//     thread existed may not need to: this asserts item frames reach it with NO resume call,
//     which is what removes the rollout race from the topology rather than retrying around it.
//  3. THE TURN ID (review BLOCKING 1). `turn/steer` is sent with a DELIBERATELY WRONG
//     `expectedTurnId` -- a daemon-minted ULID, exactly what round 1 shipped -- and the real
//     server's refusal is recorded. Then the CLI'S OWN turn id from `turn/started` is used and
//     must be accepted. This is the assertion that makes the fix's necessity a measured fact.
func TestR7RealCLI_TheDaemonAdoptsTheTUIsOwnThreadAndSteersTheCLIsOwnTurnId(t *testing.T) {
	home, ws := r7RealEnv(t)
	r7ScratchWorkspace(t, ws)
	sock := r7StartAppServer(t, home)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	// THE DAEMON'S CLIENT, connected BEFORE the agent process exists -- which is precisely what
	// the §R7.2e go-ahead handshake buys in production.
	frames := newR7Frames()
	dae, err := appserver.Dial(ctx, sock, appserver.Options{
		OnNotify: func(method string, params json.RawMessage) { frames.add(method, params) },
	})
	if err != nil {
		t.Fatalf("dial as the daemon: %v", err)
	}
	defer func() { _ = dae.Close() }()
	if err := dae.Call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "swarm", "title": "swarm", "version": "1"},
		"capabilities": map[string]any{"experimentalApi": true, "requestAttestation": false},
	}, nil); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := dae.Notify(ctx, "initialized", map[string]any{}); err != nil {
		t.Fatalf("initialized: %v", err)
	}

	// THE REAL TUI, attached to that same socket, in a PTY, exactly as the shim launches it.
	tui := exec.Command("/usr/bin/arch", "-arm64", "/usr/local/bin/codex", "--remote", "unix://"+sock)
	tui.Dir = ws
	tui.Env = append(os.Environ(), "CODEX_HOME="+home, "TERM=xterm-256color")
	ptmx, err := pty.StartWithSize(tui, &pty.Winsize{Rows: 40, Cols: 120})
	if err != nil {
		t.Fatalf("start the TUI under a pty: %v", err)
	}
	t.Cleanup(func() {
		_ = ptmx.Close()
		_ = tui.Process.Kill()
		_, _ = tui.Process.Wait()
	})
	// KEEP THE TUI'S OWN SCREEN. When this probe fails the question is always "did the agent
	// boot at all", and the app-server's log answers a different one. A run under a CODEX_HOME
	// whose credential cannot refresh dies here, with the reason on this stream and nowhere
	// else.
	var screenMu sync.Mutex
	screen := make([]byte, 0, 64<<10)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				screenMu.Lock()
				screen = append(screen, buf[:n]...)
				if len(screen) > 64<<10 {
					screen = screen[len(screen)-(64<<10):]
				}
				screenMu.Unlock()
			}
			if rerr != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		screenMu.Lock()
		defer screenMu.Unlock()
		t.Logf("the TUI's own screen (last %d bytes):\n%s", len(screen), screen)
	})

	// DISMISS THE UPDATE PROMPT, WITHOUT PRESSING ENTER. A 0.147.0 TUI whose registry has a
	// newer release boots into "Update available! 1. Update now (runs `npm install -g
	// @openai/codex`) 2. Skip 3. Skip until next version" and goes no further -- so the probe
	// times out on thread/started for a reason that has nothing to do with the protocol, which
	// is exactly what happened the first time it was run. The digit alone selects and acts;
	// ENTER IS NEVER SENT, because the default selection is "Update now" and this test must
	// never upgrade the owner's codex installation as a side effect.
	time.Sleep(4 * time.Second)
	if _, werr := ptmx.Write([]byte("2")); werr != nil {
		t.Fatalf("dismiss the update prompt: %v", werr)
	}

	// (a) THE DAEMON IS TOLD THE TUI'S THREAD ID.
	startedRaw, ok := frames.waitFor("thread/started", 90*time.Second)
	if !ok {
		t.Fatal("the daemon connection never received thread/started for the TUI's thread. The " +
			"no-flag topology of ADR-013 §R7.2e depends on exactly this notification: without it " +
			"the daemon cannot learn which thread the agent is on, and the ONLY remaining option " +
			"is a pre-created thread id carried in agent_args")
	}
	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(startedRaw, &started); err != nil || started.Thread.ID == "" {
		t.Fatalf("thread/started carried no usable thread id: %v / %s", err, startedRaw)
	}
	t.Logf("MEASURED: the daemon was told the TUI's thread id %q by thread/started, having "+
		"started no thread of its own", started.Thread.ID)

	// (b) EXACTLY ONE THREAD EXISTS. Round 1's defect was two.
	var loaded struct {
		Data []string `json:"data"`
	}
	if err := dae.Call(ctx, "thread/loaded/list", map[string]any{}, &loaded); err != nil {
		t.Fatalf("thread/loaded/list: %v", err)
	}
	if len(loaded.Data) != 1 || loaded.Data[0] != started.Thread.ID {
		t.Errorf("thread/loaded/list shows %d threads (%v); the daemon and the TUI must be on ONE, "+
			"and round 1's two-thread topology is exactly what this counts", len(loaded.Data), loaded.Data)
	} else {
		t.Logf("MEASURED: exactly ONE thread exists (%q) and it is the TUI's -- the daemon started none",
			loaded.Data[0])
	}

	// (c) THE DAEMON'S OWN TURN ON THE TUI'S THREAD, with NO thread/resume anywhere.
	if err := dae.Call(ctx, "turn/start", map[string]any{
		"threadId": started.Thread.ID,
		"input": []any{map[string]any{
			"type": "text",
			"text": "Count from 1 to 40. Put each number on its own line and spell the word out.",
		}},
	}, nil); err != nil {
		t.Fatalf("turn/start on the TUI's own thread, with no thread/resume: %v. If this is a "+
			"subscription error the no-flag topology needs a resume after all, and Q3's "+
			"rollout-to-resume measurement becomes binding on the first turn", err)
	}

	// (2) THE STREAM REACHES A CLIENT THAT NEVER RESUMED.
	turnRaw, ok := frames.waitFor("turn/started", 60*time.Second)
	if !ok {
		t.Fatal("no turn/started reached the daemon connection, which never called thread/resume")
	}
	var turn struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(turnRaw, &turn); err != nil || turn.Turn.ID == "" {
		t.Fatalf("turn/started carried no turn id: %v / %s", err, turnRaw)
	}
	t.Logf("MEASURED: the CLI's OWN turn id is %q (a UUIDv7). A daemon-minted ULID is NOT this "+
		"value, which is what review BLOCKING 1 says", turn.Turn.ID)

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) && frames.count("item/agentMessage/delta") == 0 {
		time.Sleep(200 * time.Millisecond)
	}
	if frames.count("item/agentMessage/delta") == 0 {
		t.Error("NO item/agentMessage/delta reached the daemon connection. A client that never " +
			"resumed does not receive the token stream, so the no-flag topology must resume and " +
			"the rollout race is back")
	} else {
		t.Logf("MEASURED: %d item/agentMessage/delta frames reached a client that never called "+
			"thread/resume", frames.count("item/agentMessage/delta"))
	}

	// (3) THE WRONG TURN ID IS REFUSED -- the exact value round 1 sent.
	wrong := "01M0EZR7QB3ANMVBNVNNF8CJC8"
	werr := dae.Call(ctx, "turn/steer", map[string]any{
		"threadId":       started.Thread.ID,
		"expectedTurnId": wrong,
		"input":          []any{map[string]any{"type": "text", "text": "ignored"}},
	}, nil)
	if werr == nil {
		t.Errorf("turn/steer with a daemon-minted ULID %q was ACCEPTED; expectedTurnId is "+
			"documented as a precondition and this probe assumed it enforced one", wrong)
	} else {
		t.Logf("MEASURED, and this is review BLOCKING 1 reproduced against the real server: "+
			"turn/steer with the daemon-minted ULID %q -> %v", wrong, werr)
	}

	// ... and the CLI's own id is accepted, mid-turn.
	if serr := dae.Call(ctx, "turn/steer", map[string]any{
		"threadId":       started.Thread.ID,
		"expectedTurnId": turn.Turn.ID,
		"input":          []any{map[string]any{"type": "text", "text": "Stop counting. Reply with only the word STEERED."}},
	}, nil); serr != nil {
		t.Errorf("turn/steer with the CLI's OWN turn id %q was refused: %v. If this is "+
			"`no active turn` the turn simply finished first and the probe raced; anything else "+
			"means the fix is not sufficient", turn.Turn.ID, serr)
	} else {
		t.Logf("MEASURED: turn/steer with the CLI's own turn id %q was ACCEPTED mid-turn", turn.Turn.ID)
	}
}

// TestR7RealCLI_TheProtocolPRECONDITIONSBehaveAsTheFixASSUMES is the CHEAPEST live probe that
// still answers something, and it is the one round 2 actually ran.
//
// IT COSTS THE OWNER NOTHING AND TOUCHES NO ACCOUNT STATE: no turn is ever started, so no model
// is called. It exercises only server-side bookkeeping -- the WebSocket upgrade, initialize,
// thread/start, thread/loaded/list, and the two turn PRECONDITIONS -- against the real installed
// 0.147.0.
//
// The three things it settles, all of which round 1 assumed:
//
//  1. `thread/loaded/list` really does answer `{"data": ["<threadId>", ...]}` as plain id
//     strings (r1-codex-gate.md:96, never captured as a frame). rejoinSessionBackend decodes
//     exactly this shape to find a live thread after a daemon restart.
//  2. `turn/steer` with a DAEMON-MINTED ULID as `expectedTurnId` -- precisely what R7 round 1
//     sent -- is REFUSED by the server. This is review BLOCKING 1 as a measured fact.
//  3. `turn/interrupt` with an id the server never minted answers `no active turn to
//     interrupt`, which is the string benignInterruptError swallows -- so round 1's Stop
//     reported SUCCESS for a turn it never touched.
func TestR7RealCLI_TheProtocolPRECONDITIONSBehaveAsTheFixASSUMES(t *testing.T) {
	home, ws := r7RealEnv(t)
	sock := r7StartAppServer(t, home)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c, err := appserver.Dial(ctx, sock, appserver.Options{})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "swarm", "title": "swarm", "version": "1"},
		"capabilities": map[string]any{"experimentalApi": true, "requestAttestation": false},
	}, nil); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := c.Notify(ctx, "initialized", map[string]any{}); err != nil {
		t.Fatalf("initialized: %v", err)
	}

	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := c.Call(ctx, "thread/start", map[string]any{
		"cwd": ws, "sandbox": "read-only", "approvalPolicy": "on-request",
	}, &started); err != nil {
		t.Fatalf("thread/start: %v", err)
	}
	if started.Thread.ID == "" {
		t.Fatal("thread/start returned no thread id")
	}

	// (1) THE REJOIN DISCOVERY SHAPE.
	var loaded struct {
		Data []string `json:"data"`
	}
	if err := c.Call(ctx, "thread/loaded/list", map[string]any{}, &loaded); err != nil {
		t.Fatalf("thread/loaded/list: %v", err)
	}
	found := false
	for _, id := range loaded.Data {
		if id == started.Thread.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("thread/loaded/list returned %v, which does not decode as {\"data\":[\"<id>\"...]} "+
			"containing the thread just started. rejoinSessionBackend decodes exactly this shape to "+
			"find a live thread after a daemon restart", loaded.Data)
	} else {
		t.Logf("MEASURED: thread/loaded/list -> {\"data\": %v}, the shape rejoinSessionBackend decodes",
			loaded.Data)
	}

	// (2) THE STEER PRECONDITION, against the exact value round 1 sent.
	const mintedULID = "01M0EZR7QB3ANMVBNVNNF8CJC8"
	serr := c.Call(ctx, "turn/steer", map[string]any{
		"threadId":       started.Thread.ID,
		"expectedTurnId": mintedULID,
		"input":          []any{map[string]any{"type": "text", "text": "ignored"}},
	}, nil)
	if serr == nil {
		t.Errorf("turn/steer with a DAEMON-MINTED ULID %q was ACCEPTED. expectedTurnId is "+
			"documented as a precondition; if it is not enforced, review BLOCKING 1's severity is "+
			"lower than stated and this test is the record of that", mintedULID)
	} else {
		t.Logf("MEASURED, review BLOCKING 1 as a fact: turn/steer with the daemon-minted ULID %q "+
			"-> %v", mintedULID, serr)
	}

	// (3) THE INTERRUPT ANSWER THAT benignInterruptError SWALLOWS.
	ierr := c.Call(ctx, "turn/interrupt", map[string]any{
		"threadId": started.Thread.ID,
		"turnId":   mintedULID,
	}, nil)
	if ierr == nil {
		t.Errorf("turn/interrupt with a turn id the server never minted was ACCEPTED")
	} else {
		t.Logf("MEASURED: turn/interrupt with a daemon-minted turn id -> %v", ierr)
		if strings.Contains(ierr.Error(), "no active turn to interrupt") {
			t.Logf("...and that is EXACTLY the string benignInterruptError swallows, so round 1's " +
				"Stop reported SUCCESS to the phone for a turn it never touched")
		}
	}
}
