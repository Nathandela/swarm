package main

// `swarm relogin` — the manual face of the ADR-024 auth watcher. The daemon
// recycles credential-stranded sessions on its own; this verb exists for the
// cases the watcher deliberately will not decide:
//
//   - the opt-out user (--auto off makes this THE recycle path);
//   - sessions that predate identity stamping (an unstamped session is
//     ambiguous to the daemon, but the human typing `relogin --force` IS the
//     missing assertion that a re-login happened);
//   - a SAME-ACCOUNT logout/login (audit H2): the account identity is
//     unchanged, so no stamp can ever look stale, yet the old processes hold
//     revoked tokens. `--force` asserts that too — it recycles every running
//     probed-agent session the watcher will not touch (unstamped and
//     current-stamped alike).
//
// OWNERSHIP RULE (no double recycle): with the watcher enabled, stamped-STALE
// sessions belong to the watcher — this verb only reports them. The daemon
// additionally dedups resume_from launches per source, so even a racing pair
// of actors converges on one replacement.
//
// The report is local reads (the doctor precedent: meta.json + the credentials
// file); the recycle drives exactly the protocol ops the TUI's manual Ctrl+X +
// r gesture drives (Kill, Launch with resume_from, Delete). Because those two
// halves MUST describe the same daemon, the verb refuses a socket whose
// endpoint id is not the one derived from the local state dir (audit M4:
// SWARM_DAEMON_SOCK can point anywhere).

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/skeleton"
	"github.com/Nathandela/swarm/internal/status"
)

// reloginClient is the narrow daemon surface the recycle drives; *protocol.Client
// satisfies it as-is (the agentClient precedent, plus Delete for the locked
// one-row-per-conversation rule and EndpointID for the coherence check).
type reloginClient interface {
	EndpointID() string
	List() ([]protocol.SessionView, error)
	Kill(id string) error
	Launch(protocol.LaunchReq) (id, name string, err error)
	Delete(id string) error
}

// Package-level seams (the persist.writeTemp precedent) so runRelogin is testable
// with no adapter registry reads and no real clock.
var (
	reloginIdentity = skeleton.CurrentAuthIdentity
	reloginAgents   = skeleton.AuthProbedAgents
	reloginSleep    = time.Sleep
)

// reloginExitWait bounds the wait for a killed session's exit to be recorded
// before the resume launch (which validates its source is ended).
const (
	reloginExitWait = 10 * time.Second
	reloginExitPoll = 250 * time.Millisecond
)

func dispatchRelogin(args []string, stdout, stderr io.Writer) int {
	cc, err := clientConfig()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "swarm: %v\n", err)
		return 1
	}
	// The dial is LAZY: --auto and --dry-run classification are local file
	// operations and must work with no daemon running (audit L3).
	dial := func() (reloginClient, error) {
		client, err := protocol.Dial(cc.SocketPath, nil)
		if err != nil {
			return nil, fmt.Errorf("%v (no daemon running? start swarm first)", err)
		}
		if got, want := client.EndpointID(), skeleton.EndpointIDForStateDir(cc.StateDir); got != want {
			_ = client.Close()
			return nil, fmt.Errorf("the daemon at %s is endpoint %s, not this state dir's %s; run relogin on the daemon's own machine and configuration", cc.SocketPath, got, want)
		}
		return client, nil
	}
	return runRelogin(args, dial, cc.StateDir, stdout, stderr)
}

func runRelogin(args []string, dial func() (reloginClient, error), stateDir string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("relogin", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dry := fs.Bool("dry-run", false, "report what would be recycled; recycle nothing")
	force := fs.Bool("force", false, "assert a re-login happened: also recycle the sessions the watcher cannot judge (unstamped, and same-account re-logins whose stamps still match)")
	auto := fs.String("auto", "", "on|off: enable or disable the daemon's automatic watcher, then exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	switch *auto {
	case "":
	case "on", "off":
		if err := skeleton.SetAuthWatchDisabled(stateDir, *auto == "off"); err != nil {
			_, _ = fmt.Fprintf(stderr, "relogin: %v\n", err)
			return 1
		}
		_, _ = fmt.Fprintf(stdout, "automatic auth watcher: %s\n", *auto)
		return 0
	default:
		_, _ = fmt.Fprintln(stderr, "relogin: --auto takes on or off")
		return 2
	}

	c, err := dial()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "swarm: %v\n", err)
		return 1
	}
	watcherOff := skeleton.AuthWatchDisabled(stateDir)
	views, err := c.List()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "relogin: %v\n", err)
		return 1
	}
	identities := map[string]string{}
	for _, agent := range reloginAgents() {
		identities[agent] = reloginIdentity(agent)
	}

	failed := false
	acted := 0
	metaless := 0
	for _, v := range views {
		id, probed := identities[v.Agent]
		if !probed || v.Status.Process != status.ProcessRunning {
			continue
		}
		if id == "" {
			_, _ = fmt.Fprintf(stdout, "hold      %s  %s: %s credentials unreadable (mid-login?); nothing judged\n", v.ID, v.Name, v.Agent)
			continue
		}
		m, ok := reloginMeta(stateDir, v.ID)
		if !ok {
			metaless++
			continue
		}
		stamp := m.AuthIdentity
		stale := stamp != id // empty stamps included: unverifiable, judged only by the human
		if !stale && !*force {
			continue // launched under the current account, and no re-login asserted
		}
		switch {
		case v.Status.Turn != status.TurnIdle || v.Status.Interaction != status.InteractionNone:
			_, _ = fmt.Fprintf(stdout, "deferred  %s  %s: mid-turn or awaiting input; rerun when it is quiet\n", v.ID, v.Name)
		case m.ConversationID == "":
			_, _ = fmt.Fprintf(stdout, "manual    %s  %s: no captured conversation id; restart it yourself\n", v.ID, v.Name)
		case m.LaunchOptions[protocol.OptionWorktree] == "true":
			// Audit C1: a resume cannot follow the conversation into its
			// worktree, and the delete would remove the checkout with force.
			_, _ = fmt.Fprintf(stdout, "manual    %s  %s: worktree-isolated; recycle it yourself when its work is committed\n", v.ID, v.Name)
		case stale && stamp != "" && !watcherOff:
			_, _ = fmt.Fprintf(stdout, "watcher   %s  %s: stale; the daemon recycles it within %s\n", v.ID, v.Name, authWatchIntervalHuman)
		case stale && stamp == "" && !watcherOff && !*force:
			_, _ = fmt.Fprintf(stdout, "unstamped %s  %s: predates identity stamping; rerun with --force to recycle it\n", v.ID, v.Name)
		case *dry:
			_, _ = fmt.Fprintf(stdout, "would     %s  %s\n", v.ID, v.Name)
		default:
			newID, skipped, err := reloginRecycle(c, v)
			switch {
			case err != nil:
				_, _ = fmt.Fprintf(stderr, "relogin: %s (%s): %v\n", v.ID, v.Name, err)
				failed = true
			case skipped:
				_, _ = fmt.Fprintf(stdout, "deferred  %s  %s: went busy since the roster was read; rerun when it is quiet\n", v.ID, v.Name)
			default:
				acted++
				_, _ = fmt.Fprintf(stdout, "recycled  %s -> %s  %s\n", v.ID, newID, v.Name)
			}
		}
	}
	if metaless > 0 {
		_, _ = fmt.Fprintf(stdout, "note: %d running session(s) had no readable meta under %s (another endpoint's rows, or a state-dir mismatch)\n", metaless, stateDir)
	}
	if acted == 0 && !failed {
		_, _ = fmt.Fprintln(stdout, "nothing recycled")
	}
	if failed {
		return 1
	}
	return 0
}

// authWatchIntervalHuman is spelled here rather than exported from skeleton:
// it is a sentence fragment, not a contract.
const authWatchIntervalHuman = "30s"

// reloginMeta reads the session's persisted meta directly (the doctor
// precedent), keyed by the local half of the namespaced id.
func reloginMeta(stateDir, namespacedID string) (persist.Meta, bool) {
	local := namespacedID
	if i := strings.LastIndex(namespacedID, "/"); i >= 0 {
		local = namespacedID[i+1:]
	}
	raw, err := os.ReadFile(filepath.Join(stateDir, local, "meta.json"))
	if err != nil {
		return persist.Meta{}, false
	}
	var m persist.Meta
	if json.Unmarshal(raw, &m) != nil {
		return persist.Meta{}, false
	}
	return m, true
}

// reloginRecycle is one session's kill -> wait-ended -> resume -> delete, the
// exact TUI gesture. The roster is refetched immediately before the kill (the
// classification above may be many seconds old by the time a long sweep reaches
// this row): a session that went busy is SKIPPED, never killed. A failed resume
// keeps the ended row (never Delete without a successful replacement).
func reloginRecycle(c reloginClient, v protocol.SessionView) (newID string, skipped bool, err error) {
	views, err := c.List()
	if err != nil {
		return "", false, fmt.Errorf("refresh before kill: %w", err)
	}
	fresh := false
	for _, cur := range views {
		if cur.ID != v.ID {
			continue
		}
		if cur.Status.Process != status.ProcessRunning {
			return "", true, nil // already ended by other hands
		}
		if cur.Status.Turn != status.TurnIdle || cur.Status.Interaction != status.InteractionNone {
			return "", true, nil
		}
		fresh = true
	}
	if !fresh {
		return "", true, nil // vanished since the roster was read
	}
	if err := c.Kill(v.ID); err != nil {
		return "", false, fmt.Errorf("kill: %w", err)
	}
	deadline := time.Now().Add(reloginExitWait)
	for {
		views, err := c.List()
		if err != nil {
			return "", false, fmt.Errorf("list after kill: %w", err)
		}
		running := false
		for _, cur := range views {
			if cur.ID == v.ID && cur.Status.Process == status.ProcessRunning {
				running = true
				break
			}
		}
		if !running {
			break
		}
		if time.Now().After(deadline) {
			return "", false, fmt.Errorf("session did not record its exit in time")
		}
		reloginSleep(reloginExitPoll)
	}
	newID, _, err = c.Launch(protocol.LaunchReq{
		Agent:   v.Agent,
		Name:    v.Name,
		Cwd:     v.Cwd,
		Env:     os.Environ(), // resolve the agent binary on the CALLER's PATH (B1)
		Cols:    spawnCols,
		Rows:    spawnRows,
		Options: map[string]string{protocol.OptionResumeFrom: v.ID},
	})
	if err != nil {
		return "", false, fmt.Errorf("resume: %w (the ended row remains for a manual resume)", err)
	}
	if err := c.Delete(v.ID); err != nil {
		return newID, false, fmt.Errorf("delete the stale row: %w", err)
	}
	return newID, false, nil
}
