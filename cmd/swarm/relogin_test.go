package main

// `swarm relogin` behind fakes: the ownership rule (watcher-owned rows are
// reported, never double-recycled), the --force assertion (pre-stamping rows
// AND same-account re-logins), the opt-out path where this verb IS the
// recycle, the never-delete-without-a-replacement guarantee, the pre-kill
// freshness recheck, and the C1 worktree hold.

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/skeleton"
	"github.com/Nathandela/swarm/internal/status"
)

const (
	reloginIDCurrent = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	reloginIDOld     = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
)

type fakeReloginClient struct {
	views     []protocol.SessionView
	killed    []string
	deleted   []string
	launched  []protocol.LaunchReq
	launchErr error
	// busyOnRecheck flips the named session to mid-turn on every List AFTER the
	// first, modeling a prompt that starts between classification and kill.
	busyOnRecheck string
	lists         int
}

func (f *fakeReloginClient) EndpointID() string { return "ep-t" }

func (f *fakeReloginClient) List() ([]protocol.SessionView, error) {
	f.lists++
	out := make([]protocol.SessionView, len(f.views))
	copy(out, f.views)
	if f.busyOnRecheck != "" && f.lists > 1 {
		for i := range out {
			if out[i].ID == f.busyOnRecheck {
				out[i].Status.Turn = status.TurnActive
			}
		}
	}
	return out, nil
}

func (f *fakeReloginClient) Kill(id string) error {
	f.killed = append(f.killed, id)
	for i := range f.views {
		if f.views[i].ID == id {
			f.views[i].Status.Process = status.ProcessExited
		}
	}
	return nil
}

func (f *fakeReloginClient) Launch(req protocol.LaunchReq) (string, string, error) {
	if f.launchErr != nil {
		return "", "", f.launchErr
	}
	f.launched = append(f.launched, req)
	return "ep-t/freshid", req.Name, nil
}

func (f *fakeReloginClient) Delete(id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

// dialTo wraps the fake in the lazy-dial seam runRelogin takes.
func dialTo(c reloginClient) func() (reloginClient, error) {
	return func() (reloginClient, error) { return c, nil }
}

// seedSession writes the meta.json half and returns the view half of one
// running codex session.
func seedSession(t *testing.T, stateDir, local, stamp, convID string, turn status.Turn) protocol.SessionView {
	t.Helper()
	return seedSessionOptions(t, stateDir, local, stamp, convID, turn, nil)
}

func seedSessionOptions(t *testing.T, stateDir, local, stamp, convID string, turn status.Turn, options map[string]string) protocol.SessionView {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(stateDir, local), 0o700); err != nil {
		t.Fatal(err)
	}
	m := persist.Meta{
		SchemaVersion: 1, ID: local, AgentType: "codex", Name: "n-" + local,
		Cwd: "/work", ConversationID: convID, AuthIdentity: stamp,
		LaunchOptions: options,
	}
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(stateDir, local, "meta.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return protocol.SessionView{
		ID: "ep-t/" + local, Agent: "codex", Name: "n-" + local, Cwd: "/work",
		Status: status.Status{Process: status.ProcessRunning, Turn: turn, Interaction: status.InteractionNone},
	}
}

// withReloginSeams pins the package seams to a fixed identity and a no-op
// sleep, restoring them when the test ends.
func withReloginSeams(t *testing.T, identity string) {
	t.Helper()
	prevID, prevAgents, prevSleep := reloginIdentity, reloginAgents, reloginSleep
	reloginIdentity = func(string) string { return identity }
	reloginAgents = func() []string { return []string{"codex"} }
	reloginSleep = func(time.Duration) {}
	t.Cleanup(func() { reloginIdentity, reloginAgents, reloginSleep = prevID, prevAgents, prevSleep })
}

func TestReloginLeavesStampedRowsToTheEnabledWatcher(t *testing.T) {
	withReloginSeams(t, reloginIDCurrent)
	dir := t.TempDir()
	c := &fakeReloginClient{}
	c.views = []protocol.SessionView{seedSession(t, dir, "s1", reloginIDOld, "01a05600-0000-7000-8000-000000000001", status.TurnIdle)}
	var out, errb bytes.Buffer
	if code := runRelogin(nil, dialTo(c), dir, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr %s", code, errb.String())
	}
	if len(c.killed) != 0 {
		t.Fatalf("killed %v; a stamped row belongs to the enabled watcher (double-recycle race)", c.killed)
	}
	if !strings.Contains(out.String(), "watcher") {
		t.Fatalf("output %q does not hand the row to the watcher", out.String())
	}
}

func TestReloginForceRecyclesWhatTheWatcherCannotJudge(t *testing.T) {
	withReloginSeams(t, reloginIDCurrent)
	dir := t.TempDir()
	c := &fakeReloginClient{}
	c.views = []protocol.SessionView{
		seedSession(t, dir, "stamped", reloginIDOld, "01a05600-0000-7000-8000-000000000002", status.TurnIdle),
		seedSession(t, dir, "unstamped", "", "01a05600-0000-7000-8000-000000000003", status.TurnIdle),
		// The H2 case: a SAME-ACCOUNT logout/login leaves the stamp matching
		// while the in-memory tokens are revoked. Only the human can assert it.
		seedSession(t, dir, "sameacct", reloginIDCurrent, "01a05600-0000-7000-8000-000000000004", status.TurnIdle),
	}
	var out, errb bytes.Buffer
	if code := runRelogin([]string{"--force"}, dialTo(c), dir, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr %s", code, errb.String())
	}
	for _, want := range []string{"ep-t/unstamped", "ep-t/sameacct"} {
		found := false
		for _, k := range c.killed {
			if k == want {
				found = true
			}
		}
		if !found {
			t.Errorf("killed %v; --force must recycle %s (the watcher cannot judge it)", c.killed, want)
		}
	}
	for _, k := range c.killed {
		if k == "ep-t/stamped" {
			t.Errorf("killed %v; the stamped-stale row stays watcher-owned even under --force", c.killed)
		}
	}
}

func TestReloginRecyclesEverythingWhenTheWatcherIsOff(t *testing.T) {
	withReloginSeams(t, reloginIDCurrent)
	dir := t.TempDir()
	if err := skeleton.SetAuthWatchDisabled(dir, true); err != nil {
		t.Fatal(err)
	}
	c := &fakeReloginClient{}
	c.views = []protocol.SessionView{
		seedSession(t, dir, "stamped", reloginIDOld, "01a05600-0000-7000-8000-000000000005", status.TurnIdle),
		seedSession(t, dir, "unstamped", "", "01a05600-0000-7000-8000-000000000006", status.TurnIdle),
		seedSession(t, dir, "current", reloginIDCurrent, "01a05600-0000-7000-8000-000000000007", status.TurnIdle),
	}
	var out, errb bytes.Buffer
	if code := runRelogin(nil, dialTo(c), dir, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr %s", code, errb.String())
	}
	if len(c.killed) != 2 {
		t.Fatalf("killed %v; with the watcher off this verb IS the recycle path (and the current row stays without --force)", c.killed)
	}
	for _, req := range c.launched {
		if req.Name == "" || req.Cwd == "" || len(req.Env) == 0 {
			t.Fatalf("launch request lost its identity or env: %+v", req)
		}
	}
}

func TestReloginDryRunActsOnNothing(t *testing.T) {
	withReloginSeams(t, reloginIDCurrent)
	dir := t.TempDir()
	if err := skeleton.SetAuthWatchDisabled(dir, true); err != nil {
		t.Fatal(err)
	}
	c := &fakeReloginClient{}
	c.views = []protocol.SessionView{seedSession(t, dir, "s1", reloginIDOld, "01a05600-0000-7000-8000-000000000008", status.TurnIdle)}
	var out, errb bytes.Buffer
	if code := runRelogin([]string{"--dry-run"}, dialTo(c), dir, &out, &errb); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if len(c.killed) != 0 || len(c.launched) != 0 || len(c.deleted) != 0 {
		t.Fatal("--dry-run mutated the daemon")
	}
	if !strings.Contains(out.String(), "would") {
		t.Fatalf("output %q does not report what would happen", out.String())
	}
}

func TestReloginDefersHoldsAndWorktrees(t *testing.T) {
	withReloginSeams(t, reloginIDCurrent)
	dir := t.TempDir()
	if err := skeleton.SetAuthWatchDisabled(dir, true); err != nil {
		t.Fatal(err)
	}
	c := &fakeReloginClient{}
	c.views = []protocol.SessionView{
		seedSession(t, dir, "busy", reloginIDOld, "01a05600-0000-7000-8000-000000000009", status.TurnActive),
		seedSession(t, dir, "noconv", reloginIDOld, "", status.TurnIdle),
		seedSessionOptions(t, dir, "isolated", reloginIDOld, "01a05600-0000-7000-8000-00000000000a", status.TurnIdle,
			map[string]string{protocol.OptionWorktree: "true"}),
	}
	var out, errb bytes.Buffer
	if code := runRelogin(nil, dialTo(c), dir, &out, &errb); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if len(c.killed) != 0 {
		t.Fatalf("killed %v; mid-turn, unresumable and worktree-isolated sessions must never be killed here", c.killed)
	}
	for _, want := range []string{"deferred", "manual", "worktree-isolated"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output %q does not explain the %q hold", out.String(), want)
		}
	}
}

// TestReloginRechecksFreshnessBeforeTheKill pins codex finding 6 for the
// manual verb: the roster snapshot is stale by the time a long sweep reaches a
// row, so a session that went busy since is skipped, never killed.
func TestReloginRechecksFreshnessBeforeTheKill(t *testing.T) {
	withReloginSeams(t, reloginIDCurrent)
	dir := t.TempDir()
	if err := skeleton.SetAuthWatchDisabled(dir, true); err != nil {
		t.Fatal(err)
	}
	c := &fakeReloginClient{busyOnRecheck: "ep-t/s1"}
	c.views = []protocol.SessionView{seedSession(t, dir, "s1", reloginIDOld, "01a05600-0000-7000-8000-00000000000b", status.TurnIdle)}
	var out, errb bytes.Buffer
	if code := runRelogin(nil, dialTo(c), dir, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr %s", code, errb.String())
	}
	if len(c.killed) != 0 {
		t.Fatalf("killed %v; a session that went busy since the roster was read must be skipped", c.killed)
	}
	if !strings.Contains(out.String(), "went busy") {
		t.Fatalf("output %q does not explain the skip", out.String())
	}
}

func TestReloginFailedResumeKeepsTheEndedRow(t *testing.T) {
	withReloginSeams(t, reloginIDCurrent)
	dir := t.TempDir()
	if err := skeleton.SetAuthWatchDisabled(dir, true); err != nil {
		t.Fatal(err)
	}
	c := &fakeReloginClient{launchErr: errors.New("binary missing")}
	c.views = []protocol.SessionView{seedSession(t, dir, "s1", reloginIDOld, "01a05600-0000-7000-8000-00000000000c", status.TurnIdle)}
	var out, errb bytes.Buffer
	if code := runRelogin(nil, dialTo(c), dir, &out, &errb); code != 1 {
		t.Fatalf("exit = %d; a failed resume must be a failed run", code)
	}
	if len(c.deleted) != 0 {
		t.Fatalf("deleted %v after a FAILED resume; the ended row is the user's only handle", c.deleted)
	}
}

// TestReloginAutoNeedsNoDaemon pins audit L3: toggling the watcher is a local
// file operation and must work with nothing to dial.
func TestReloginAutoNeedsNoDaemon(t *testing.T) {
	withReloginSeams(t, reloginIDCurrent)
	dir := t.TempDir()
	dead := func() (reloginClient, error) { return nil, errors.New("no daemon") }
	var out, errb bytes.Buffer
	if code := runRelogin([]string{"--auto", "off"}, dead, dir, &out, &errb); code != 0 {
		t.Fatalf("--auto off exit = %d, stderr %s", code, errb.String())
	}
	if !skeleton.AuthWatchDisabled(dir) {
		t.Fatal("--auto off did not disable the watcher")
	}
	if code := runRelogin([]string{"--auto", "on"}, dead, dir, &out, &errb); code != 0 {
		t.Fatalf("--auto on exit = %d", code)
	}
	if skeleton.AuthWatchDisabled(dir) {
		t.Fatal("--auto on did not re-enable the watcher")
	}
}

func TestReloginHoldsWhenCredentialsAreUnknown(t *testing.T) {
	withReloginSeams(t, "") // mid-login: no parseable identity
	dir := t.TempDir()
	if err := skeleton.SetAuthWatchDisabled(dir, true); err != nil {
		t.Fatal(err)
	}
	c := &fakeReloginClient{}
	c.views = []protocol.SessionView{seedSession(t, dir, "s1", reloginIDOld, "01a05600-0000-7000-8000-00000000000d", status.TurnIdle)}
	var out, errb bytes.Buffer
	if code := runRelogin(nil, dialTo(c), dir, &out, &errb); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if len(c.killed) != 0 {
		t.Fatalf("killed %v with unknown credentials; nothing may be judged", c.killed)
	}
}
