// Phase G / R-G1 (issue agents-tracker-5gv, plan .claude/tmp/cli-duo-implementation-
// plan.md): the production-path end-to-end for the v1.1 CLI duo. Phases A-F wired
// agy and opencode into the registry, launch composition, and the engine's grid
// rules (internal/engine/gridrules.go), and internal/engine/gridrules_fixture_test.go
// already proves — OFFLINE, byte-exact — that replaying each committed fixture
// (internal/adapter/{agy,opencode}/testdata) through evaluateGridWithRules never
// misclassifies. What that test does NOT prove is that the REAL assembled stack
// reproduces the same verdicts: registry.New -> composeLaunchSpec argv resolution ->
// daemon/shim spawn -> the shim's PTY -> the daemon's 200ms grid-sample cadence
// (skeleton/serve.go gridPoll/tapGrids) -> engine.OnOutput -> persisted status ->
// protocol.Client.List.
//
// This test closes that gap with two REPLAY BINARIES named exactly "agy" and
// "opencode" (built from generated Go source, stdlib-only, into a temp PATH
// directory — see buildReplayBinaries): each reads ITS OWN committed fixture (the
// path is baked into the compiled binary at build time, not passed via env var —
// see the deviation note on replaySource) and writes its raw PTY capture to stdout
// in SEGMENTS cut at the Phase B marker-memo byte offsets (docs/verification/
// cli-duo-adapters-evidence.md), holding output at each target state for >=1s (>=2s
// for the settled/idle holds) so the daemon's 200ms sampler deterministically
// observes it — comfortably longer than a single sample tick, per R-G1.
//
// Both sessions are launched through the CLIENT protocol exactly as a real launch
// would be (Agent: "agy" / "opencode", no fixture-only shortcut), so this is a
// genuine production-path exercise. COST: zero — the replay binaries are not the
// real CLIs, so there is no billable run (the real-CLI smoke is R-G2, env-gated).
package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// Fixture paths, relative to this package (internal/e2e), matching the convention
// already used by internal/engine/gridrules_fixture_test.go for the same fixtures.
const (
	agyFixturePath      = "../adapter/agy/testdata/agy.json"
	opencodeFixturePath = "../adapter/opencode/testdata/opencode.json"
)

// Expected exit-screen conversation ids, byte-identical to the ones the Phase B
// evidence memo records for these exact committed fixtures (docs/verification/
// cli-duo-adapters-evidence.md, "Conversation-id extraction (R-B2a)"). Because the
// replay binaries stream the REAL committed bytes verbatim, the production
// ExtractConversationID path must recover these exact strings.
const (
	agyConvID      = "fb5e3e02-e5ef-4d25-b398-aead20366441"
	opencodeConvID = "ses_08b642915ffeYL3T6ea1DnJZDd"
)

// replaySegment is one held-output slice of a replay binary's schedule: bytes
// [priorEnd, End) of the fixture's raw PTY capture are written to stdout, then Hold
// elapses before the next segment starts — long enough (per R-G1: >=1s for a busy
// hold, >=2s for a settled hold) that the daemon's 200ms grid-sample cadence
// (skeleton/serve.go gridPoll) samples the resulting terminal state on more than
// one tick, so observation is deterministic rather than a lucky race.
type replaySegment struct {
	End  int
	Hold time.Duration
}

// agySegments cuts the agy fixture (internal/adapter/agy/testdata/agy.json, 10092
// bytes) at the Phase B memo's phase-window offsets: [0,3802) startup; [3802,6150)
// busy; [6150,6228) the "hard-frame" region (the offset~6132 false-idle repro class
// R-C1/R-C5 guard against — "esc to cancel" persists while a bare ">" + border is
// ALSO present); [6228,6300) the documented 72-byte marker-gap transient where
// neither declared busy marker is intact; [6300,7262) busy tail; [7262,10035)
// settled idle; [10035,10092) the exit screen carrying "agy --conversation=<uuid>".
var agySegments = []replaySegment{
	{End: 3802, Hold: 300 * time.Millisecond},
	{End: 6150, Hold: 1200 * time.Millisecond},
	{End: 6228, Hold: 1200 * time.Millisecond},
	{End: 6300, Hold: 1200 * time.Millisecond},
	{End: 7262, Hold: 1200 * time.Millisecond},
	{End: 10035, Hold: 2200 * time.Millisecond},
	{End: 10092, Hold: 300 * time.Millisecond}, // exit screen; brief settle before exit(0)
}

// opencodeSegments cuts the opencode fixture (testdata/opencode.json, 76281 bytes)
// at the memo's offsets: [0,33547) startup (incl. the "Update Available" modal,
// which never overlaps the bottom-6 status rows, per the memo); [33547,67787) busy
// ("esc interrupt", proven zero-gap at true byte granularity); [67787,76243)
// settled (opencode declares no idle rule, so this must classify unknown, never
// idle); [76243,76281) the exit id line "opencode -s ses_<id>".
var opencodeSegments = []replaySegment{
	{End: 33547, Hold: 300 * time.Millisecond},
	{End: 67787, Hold: 1700 * time.Millisecond},
	{End: 76243, Hold: 2200 * time.Millisecond},
	{End: 76281, Hold: 300 * time.Millisecond}, // exit id line; brief settle before exit(0)
}

// replaySourceTemplate is a tiny, stdlib-only Go program: it reads its own
// (build-time-baked) fixture path, JSON-decodes the fixture's "pty_capture" field
// (Go's encoding/json base64-decodes a []byte field automatically, matching how
// adapter.Fixture round-trips — field name Pty_capture matches the JSON key
// "pty_capture" via encoding/json's case-insensitive fallback, so no struct tag,
// and therefore no backtick, is needed inside this outer raw string), then writes
// it to stdout in the segment schedule baked in below. It drains stdin (no
// interactive input is scripted for R-G1) and exits promptly on SIGTERM.
//
// DEVIATION from the plan's suggested "pass the fixture path via env var": a
// custom env var does not reach the exec'd agent process. internal/persist/env.go's
// FilterEnv is a normative ALLOWLIST (ADR-004 item 6, S-2) applied to
// LaunchSpec.ClientEnv before it becomes the agent's env — PATH/HOME/SHELL/TERM/
// locale/venv/provider-key vars only; an arbitrary SWARM_REPLAY_FIXTURE would be
// silently dropped. Baking the fixture's absolute path into the compiled binary
// (one build per CLI name) sidesteps this correctly: it is a build-time constant
// of the TEST HARNESS, not a production launch input, so the allowlist — which is
// exactly the production security boundary this test's launch goes through — is
// left completely undisturbed.
const replaySourceTemplate = `package main

import (
	"encoding/json"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type segment struct {
	End  int
	Hold time.Duration
}

var segments = []segment{
%[2]s}

func main() {
	go io.Copy(io.Discard, os.Stdin)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM)
	go func() {
		<-sig
		os.Exit(0)
	}()

	data, err := os.ReadFile(%[1]q)
	if err != nil {
		os.Exit(1)
	}
	var fx struct {
		Pty_capture []byte
	}
	if err := json.Unmarshal(data, &fx); err != nil {
		os.Exit(1)
	}

	cursor := 0
	for _, s := range segments {
		end := s.End
		if end > len(fx.Pty_capture) {
			end = len(fx.Pty_capture)
		}
		if end > cursor {
			os.Stdout.Write(fx.Pty_capture[cursor:end])
			cursor = end
		}
		if s.Hold > 0 {
			time.Sleep(s.Hold)
		}
	}
	os.Exit(0)
}
`

// replaySource renders replaySourceTemplate for one fixture: its absolute path
// (the built binary's cwd is the session's launch cwd, not this package, so a
// relative path would not resolve at run time) and its segment schedule.
func replaySource(fixtureAbsPath string, segs []replaySegment) string {
	var b strings.Builder
	for _, s := range segs {
		fmt.Fprintf(&b, "\t{%d, %d},\n", s.End, int64(s.Hold))
	}
	return fmt.Sprintf(replaySourceTemplate, fixtureAbsPath, b.String())
}

// buildReplayBinaries compiles the two replay programs, named exactly "agy" and
// "opencode" (so PATH resolution in composeLaunchSpec's resolveArgv0 finds them
// under those bare names, exactly as the real CLIs would be found), into one temp
// directory returned for the caller to prepend to the launch env's PATH.
func buildReplayBinaries(t *testing.T) string {
	t.Helper()
	agyAbs, err := filepath.Abs(agyFixturePath)
	if err != nil {
		t.Fatalf("resolve agy fixture path: %v", err)
	}
	opencodeAbs, err := filepath.Abs(opencodeFixturePath)
	if err != nil {
		t.Fatalf("resolve opencode fixture path: %v", err)
	}

	binDir := t.TempDir()
	build := func(name, fixtureAbs string, segs []replaySegment) {
		srcPath := filepath.Join(t.TempDir(), name+".go")
		if err := os.WriteFile(srcPath, []byte(replaySource(fixtureAbs, segs)), 0o644); err != nil {
			t.Fatalf("write %s replay source: %v", name, err)
		}
		out := filepath.Join(binDir, name)
		cmd := exec.Command("go", "build", "-o", out, srcPath)
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("build %s replay binary: %v", name, err)
		}
	}
	build("agy", agyAbs, agySegments)
	build("opencode", opencodeAbs, opencodeSegments)
	return binDir
}

// statusSample is one polled observation of a session's status, timestamped
// relative to a shared collection start.
type statusSample struct {
	elapsed time.Duration
	status  status.Status
}

// waitSessionsListed blocks until every id in ids appears in List(), so status
// sampling below starts only once both launches are visible.
func waitSessionsListed(t *testing.T, c *protocol.Client, ids ...string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		views, err := c.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		found := make(map[string]bool, len(views))
		for _, v := range views {
			found[v.ID] = true
		}
		allFound := true
		for _, id := range ids {
			if !found[id] {
				allFound = false
			}
		}
		if allFound {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("sessions %v never all appeared in List within 10s", ids)
}

// collectStatusSamples polls List() every 50ms, recording a timestamped sample for
// every id in ids on every poll, until every session's Process has left "running"
// (both replay binaries have exited) or overallBound elapses. Polling BOTH sessions
// off the same List() call is itself a small proof of skeleton.go's no-head-of-
// line-blocking design (each running session is grid-sampled in its own goroutine,
// FIX 7): the two sessions run and are classified concurrently, not serially.
func collectStatusSamples(t *testing.T, c *protocol.Client, ids []string, start time.Time, overallBound time.Duration) map[string][]statusSample {
	t.Helper()
	samples := make(map[string][]statusSample, len(ids))
	deadline := start.Add(overallBound)
	for time.Now().Before(deadline) {
		views, err := c.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		byID := make(map[string]protocol.SessionView, len(views))
		for _, v := range views {
			byID[v.ID] = v
		}
		allEnded := true
		for _, id := range ids {
			v, ok := byID[id]
			if !ok {
				allEnded = false
				continue
			}
			samples[id] = append(samples[id], statusSample{elapsed: time.Since(start), status: v.Status})
			if v.Status.Process == status.ProcessRunning {
				allEnded = false
			}
		}
		if allEnded {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return samples
}

func firstIndex(samples []statusSample, pred func(status.Status) bool) int {
	for i, s := range samples {
		if pred(s.status) {
			return i
		}
	}
	return -1
}

func firstIndexAfter(samples []statusSample, from int, pred func(status.Status) bool) int {
	for i := from + 1; i < len(samples); i++ {
		if pred(samples[i].status) {
			return i
		}
	}
	return -1
}

func countInRange(samples []statusSample, lo, hi int, pred func(status.Status) bool) int {
	n := 0
	for i := lo; i <= hi && i >= 0 && i < len(samples); i++ {
		if pred(samples[i].status) {
			n++
		}
	}
	return n
}

func anyMatch(samples []statusSample, pred func(status.Status) bool) bool {
	return firstIndex(samples, pred) >= 0
}

func isActive(s status.Status) bool  { return s.Turn == status.TurnActive }
func isIdle(s status.Status) bool    { return s.Turn == status.TurnIdle }
func isUnknown(s status.Status) bool { return s.Turn == status.TurnUnknown }

// TestE2E_ReplayProductionPath_AgyOpencode is R-G1: drives the REAL assembled
// stack (registry -> composeLaunchSpec -> daemon/shim -> engine grid rules ->
// persisted status -> client List) for both v1.1 CLIs using deterministic replay
// binaries, and asserts the classifications the offline byte-exact replay
// (internal/engine/gridrules_fixture_test.go) already proved, now observed through
// the daemon's real 200ms sampling cadence:
//
//   - agy: turn=active is observed sustained across the busy holds (spanning both
//     the pre-6150 window and the [6150,6228) hard-frame region — the offset~6132
//     false-idle repro class), turn=idle is never observed before that busy run
//     completes, and turn=idle IS observed once the settled hold begins.
//   - opencode: turn=active is observed during its busy hold; turn=idle is NEVER
//     observed at any point (opencode declares no idle rule, R-B4); after its busy
//     hold ends, turn=unknown is observed (the honest "settled -> unknown" T-4
//     outcome) and the final observed turn is not active.
//   - both: the exit-screen conversation id is extracted and persisted (Epic 11
//     C1's transcript-tail capture, independent of any live attach).
func TestE2E_ReplayProductionPath_AgyOpencode(t *testing.T) {
	buildBinaries(t)
	replayBinDir := buildReplayBinaries(t)
	env := newDaemonEnv(t)
	startDaemon(t, env)
	c := dial(t, env.sock)

	agentEnv := []string{"PATH=" + replayBinDir + ":" + os.Getenv("PATH")}

	agyID, err := c.Launch(protocol.LaunchReq{
		Agent: "agy", Cwd: t.TempDir(), Options: map[string]string{},
		Env: agentEnv, Cols: 100, Rows: 30,
	})
	if err != nil {
		t.Fatalf("launch agy: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(agyID) })

	opencodeID, err := c.Launch(protocol.LaunchReq{
		Agent: "opencode", Cwd: t.TempDir(), Options: map[string]string{},
		Env: agentEnv, Cols: 100, Rows: 30,
	})
	if err != nil {
		t.Fatalf("launch opencode: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(opencodeID) })

	waitSessionsListed(t, c, agyID, opencodeID)

	start := time.Now()
	const overallBound = 40 * time.Second
	samples := collectStatusSamples(t, c, []string{agyID, opencodeID}, start, overallBound)

	agySamples := samples[agyID]
	ocSamples := samples[opencodeID]
	if len(agySamples) == 0 {
		t.Fatal("no status samples collected for the agy session")
	}
	if len(ocSamples) == 0 {
		t.Fatal("no status samples collected for the opencode session")
	}

	// --- agy: sustained active across both busy holds, then idle at settled. ---
	activeIdx := firstIndex(agySamples, isActive)
	if activeIdx < 0 {
		t.Fatalf("agy: turn=active never observed (the busy-contains grid rule never fired through the real "+
			"daemon sampler); samples=%+v", agySamples)
	}
	idleIdx := firstIndexAfter(agySamples, activeIdx, isIdle)
	if idleIdx < 0 {
		t.Fatalf("agy: turn=idle never observed after active (the settled hold [7262,10035) never classified "+
			"idle); samples=%+v", agySamples)
	}
	if gap := agySamples[idleIdx].elapsed - agySamples[activeIdx].elapsed; gap < 3*time.Second {
		t.Fatalf("agy: idle observed only %s after the first active sample (want >= 3s: the busy holds "+
			"[3802,7262) — including the [6150,6228) hard-frame region, the offset~6132 false-idle regression "+
			"class — must fully play out as active before settling); samples=%+v", gap, agySamples)
	}
	if n := countInRange(agySamples, activeIdx, idleIdx-1, isActive); n < 5 {
		t.Fatalf("agy: only %d active samples observed across the busy holds (want >= 5: sustained observation "+
			"spanning both the pre-6150 hold and the hard-frame hold, not a single lucky tick); samples=%+v", n, agySamples)
	}
	// Direct form of R-G1's "no idle emission during either busy hold": a false
	// idle fired during ANY busy hold is necessarily followed by the remaining busy
	// holds' active samples, so once idle is observed, active must never reappear.
	// (Timing-free: unlike a wall-clock bound, this cannot flake on launch latency.)
	if reactivated := firstIndexAfter(agySamples, idleIdx, isActive); reactivated >= 0 {
		t.Fatalf("agy: turn=active observed again at sample %d after the first idle at sample %d — that idle was "+
			"a false emission during a busy hold, not the settled state; samples=%+v",
			reactivated, idleIdx, agySamples)
	}
	waitForConversationID(t, env.stateDir, localOf(t, agyID), agyConvID)

	// --- opencode: active during busy, NEVER idle, settled -> unknown. ---
	ocActiveIdx := firstIndex(ocSamples, isActive)
	if ocActiveIdx < 0 {
		t.Fatalf("opencode: turn=active never observed during its busy hold [33547,67787); samples=%+v", ocSamples)
	}
	if anyMatch(ocSamples, isIdle) {
		t.Fatalf("opencode: turn=idle observed at some point, but opencode declares no idle rule (R-B4) — it "+
			"must NEVER report idle; samples=%+v", ocSamples)
	}
	lastActiveIdx := ocActiveIdx
	for i, s := range ocSamples {
		if isActive(s.status) {
			lastActiveIdx = i
		}
	}
	if !anyMatch(ocSamples[lastActiveIdx+1:], isUnknown) {
		t.Fatalf("opencode: never observed turn=unknown after its busy hold ended at sample %d (the settled "+
			"hold [67787,76243) should classify unknown, the honest no-idle-rule T-4 outcome); samples=%+v",
			lastActiveIdx, ocSamples)
	}
	if final := ocSamples[len(ocSamples)-1].status.Turn; final == status.TurnActive {
		t.Fatalf("opencode: final observed turn is still active; want settled (unknown) by session end")
	}
	waitForConversationID(t, env.stateDir, localOf(t, opencodeID), opencodeConvID)
}
