// Epic 11 ENGINE→DAEMON status-wiring end-to-end suite (the carry-forward from
// Epic 8b/10, tracked as agents-tracker-bf1). Epic 8 left the hook transport live
// (hook post → socket demux → serveHook → engine.HandleCallback) but the engine
// INERT: sessions were never registered with it (so auth no-ops / every callback is
// rejected as "unregistered"), shim output was never tapped into engine.OnOutput,
// and engine.Emit reached the fan-out but never persisted. Epic 11 completes the
// three seams so status detection actually FUNCTIONS:
//
//	(a) daemon launch registers the session with the engine using the SAME
//	    per-session hook TOKEN it injects into the agent env
//	    (engine.RegisterSession(localID, token, shimPID, adapter.SignalSources()));
//	(b) the shim's PTY output is tapped into engine.OnOutput(localID, grid) so the
//	    grid heuristic runs;
//	(c) engine.Emit persists the status (the daemon status-write seam, G6) AND
//	    pushes it to the Epic 6 fan-out, so a change reaches List AND Subscribe.
//
// RED STATE: every test here COMPILES (all referenced symbols exist today) and
// FAILS AT RUNTIME because those three seams are not wired — a hook with the real
// token is rejected as "unregistered", so no status change is ever observed, and
// the idle grid is never evaluated. They turn green when the assembly wires the
// carry-forward. COST: these drive the FAKE agent only; no billable real-CLI run.
//
// Shared helpers (buildBinaries, newDaemonEnv, startDaemon, dial, launchFakeSession,
// waitOneView, localOf, alive) live in skeleton_e2e_test.go (same package).
package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

// l1Bound is the functional propagation window this suite waits within. The spec's
// L1 is ≤1 s and E10.7 asserts the ≤500 ms server half precisely; here the concern
// is only that the wiring PROPAGATES at all, so the window is generous to absorb
// the subprocess + roster-poll cadence without making the test flaky.
const l1Bound = 3 * time.Second

// readHookToken reads a launched session's per-session hook token out of the
// daemon-written shim-launch.json (the 0600 config in the session dir, whose env
// carries SWARM_HOOK_TOKEN). This is the SAME token the daemon must register with
// the engine (seam a): a hook bearing it authenticates (S6).
func readHookToken(t *testing.T, stateDir, local string) string {
	t.Helper()
	path := filepath.Join(stateDir, local, "shim-launch.json")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			var cfg struct {
				Env []string `json:"env"`
			}
			if json.Unmarshal(data, &cfg) == nil {
				for _, kv := range cfg.Env {
					if v, ok := envValue(kv, hookclient.EnvToken); ok && v != "" {
						return v
					}
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("hook token (%s) never appeared in %s", hookclient.EnvToken, path)
	return ""
}

// envValue splits a "KEY=VALUE" pair and returns VALUE when KEY matches.
func envValue(kv, key string) (string, bool) {
	if len(kv) > len(key)+1 && kv[:len(key)] == key && kv[len(key)] == '=' {
		return kv[len(key)+1:], true
	}
	return "", false
}

// waitForStatus subscribes-style polls the client's List until the session's status
// satisfies pred, or the window elapses. Using List (not Subscribe) keeps the check
// robust to event coalescing; the persisted-vs-fanned-out distinction is asserted
// by which SEAM each test exercises.
func waitForStatus(t *testing.T, c *protocol.Client, id string, within time.Duration, pred func(status.Status) bool) (status.Status, bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	var last status.Status
	for time.Now().Before(deadline) {
		views, err := c.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, v := range views {
			if v.ID == id {
				last = v.Status
				if pred(v.Status) {
					return v.Status, true
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return last, false
}

// TestE2E_EngineWiring_HookChangesStatus is the headline carry-forward test
// (scenarios 4 & 5, T-2/P-3/V-2/V-5): a hook posted with the session's REAL token
// authenticates, mutates the session's status, and the change is observable to a
// client. It also proves the auth boundary end-to-end (S6): a FOREIGN-token hook is
// a no-op even though the session is registered.
func TestE2E_EngineWiring_HookChangesStatus(t *testing.T) {
	buildBinaries(t)
	env := newDaemonEnv(t)
	startDaemon(t, env)
	c := dial(t, env.sock)

	id := launchFakeSession(t, c, "print RUNNING\nidle 120s\n")
	waitOneView(t, c)
	local := localOf(t, id)
	token := readHookToken(t, env.stateDir, local)

	// (1) A hook with the REAL token flips the session to needs-input. The callback
	// carries the LOCAL session id (what the daemon injects as SWARM_SESSION_ID and
	// must register with the engine) and sequence 1 (fresh; the engine's high-water
	// starts at 0).
	good := engine.Callback{
		SessionID: local,
		Token:     token,
		Sequence:  1,
		Event:     "Notification",
		Payload: map[string]string{
			engine.PayloadKeyTurn:        string(status.TurnIdle),
			engine.PayloadKeyInteraction: string(status.InteractionPermission),
		},
	}
	if err := hookclient.Post(env.sock, good); err != nil {
		t.Fatalf("post authenticated hook: %v", err)
	}

	st, ok := waitForStatus(t, c, id, l1Bound, func(s status.Status) bool {
		return s.Interaction == status.InteractionPermission && s.Turn == status.TurnIdle
	})
	if !ok {
		t.Fatalf("authenticated hook did not change status within %v (last=%+v); the engine→daemon "+
			"carry-forward is not wired: the daemon must engine.RegisterSession(localID, token, ...) at "+
			"launch and route engine.Emit to persist + fan-out", l1Bound, st)
	}
	// The derived group must be the needs-input group the general view highlights (V-5).
	if g := status.Derive(st); g != status.GroupNeedsInput {
		t.Errorf("derived group = %q after permission hook; want %q (Needs input)", g, status.GroupNeedsInput)
	}

	// (2) Auth boundary (S6): a FOREIGN-token hook targeting the same, registered
	// session is rejected — it must NOT further change the status. We fire it with a
	// higher sequence and a DISTINCT payload (interaction=prompt) and confirm the
	// status never becomes prompt.
	foreign := engine.Callback{
		SessionID: local,
		Token:     "not-the-session-token",
		Sequence:  2,
		Event:     "Notification",
		Payload:   map[string]string{engine.PayloadKeyInteraction: string(status.InteractionPrompt)},
	}
	if err := hookclient.Post(env.sock, foreign); err != nil {
		t.Fatalf("post foreign-token hook: %v", err)
	}
	if leaked, changed := waitForStatus(t, c, id, time.Second, func(s status.Status) bool {
		return s.Interaction == status.InteractionPrompt
	}); changed {
		t.Errorf("foreign-token hook mutated status to %+v; tokens authenticate callbacks (S6) — a "+
			"foreign token must be a no-op", leaked)
	}
}

// TestE2E_EngineWiring_StatusPersisted proves seam (c)'s PERSIST half (G6): after an
// authenticated hook changes a session's status, the change is durable — a client
// that reconnects to the SAME daemon still sees it. This distinguishes real
// persistence (the daemon status-write seam) from a transient fan-out overlay: a
// fresh client's List reads the roster the daemon persists, so it only reflects the
// change if the engine status was written back through the sole meta writer.
func TestE2E_EngineWiring_StatusPersisted(t *testing.T) {
	buildBinaries(t)
	env := newDaemonEnv(t)
	startDaemon(t, env)
	c := dial(t, env.sock)

	id := launchFakeSession(t, c, "print RUNNING\nidle 120s\n")
	waitOneView(t, c)
	local := localOf(t, id)
	token := readHookToken(t, env.stateDir, local)

	cb := engine.Callback{
		SessionID: local,
		Token:     token,
		Sequence:  1,
		Event:     "Notification",
		Payload: map[string]string{
			engine.PayloadKeyTurn:        string(status.TurnIdle),
			engine.PayloadKeyInteraction: string(status.InteractionPermission),
		},
	}
	if err := hookclient.Post(env.sock, cb); err != nil {
		t.Fatalf("post hook: %v", err)
	}
	if _, ok := waitForStatus(t, c, id, l1Bound, func(s status.Status) bool {
		return s.Interaction == status.InteractionPermission
	}); !ok {
		t.Fatalf("hook did not change status (carry-forward not wired)")
	}

	// A FRESH client reads the daemon's persisted roster; the change must be there.
	c2 := dial(t, env.sock)
	st, ok := waitForStatus(t, c2, id, l1Bound, func(s status.Status) bool {
		return s.Interaction == status.InteractionPermission
	})
	if !ok {
		t.Fatalf("a reconnecting client did not see the hook-driven status (last=%+v); engine.Emit must "+
			"PERSIST through the daemon's sole meta writer (G6), not only fan out transiently", st)
	}
}

// TestE2E_EngineWiring_OutputTapDrivesHeuristic proves seam (b): the shim's PTY
// output is tapped into engine.OnOutput so the grid heuristic runs WITHOUT any
// typed signal. The fake agent prints a prompt and blocks on input (ask), so its
// grid settles at a parked prompt sentinel — which the generic heuristic classifies
// as turn=idle. With the tap wired, the session moves off its turn=unknown register
// baseline to idle purely from output; without it, OnOutput is never called and the
// turn never settles.
func TestE2E_EngineWiring_OutputTapDrivesHeuristic(t *testing.T) {
	buildBinaries(t)
	env := newDaemonEnv(t)
	startDaemon(t, env)
	c := dial(t, env.sock)

	// `ask` prints its text with NO trailing newline and blocks reading stdin, so the
	// cursor parks right after the ">" sentinel — the settled idle-prompt posture.
	id := launchFakeSession(t, c, "ask READY>\n")
	waitOneView(t, c)

	st, ok := waitForStatus(t, c, id, l1Bound, func(s status.Status) bool {
		return s.Turn == status.TurnIdle
	})
	if !ok {
		t.Fatalf("session never settled to turn=idle from its grid (last=%+v); the shim→engine output tap "+
			"(engine.OnOutput) is not wired, so the grid heuristic never runs", st)
	}
}
