package main

// `swarm relogin` behind fakes: the ownership rule (watcher-owned rows are
// reported, never double-recycled), the --force assertion for pre-stamping
// sessions, the opt-out path where this verb IS the recycle, and the
// never-delete-without-a-replacement guarantee.

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
}

func (f *fakeReloginClient) List() ([]protocol.SessionView, error) {
	out := make([]protocol.SessionView, len(f.views))
	copy(out, f.views)
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

// seedSession writes the meta.json half and returns the view half of one
// running codex session.
func seedSession(t *testing.T, stateDir, local, stamp, convID string, turn status.Turn) protocol.SessionView {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(stateDir, local), 0o700); err != nil {
		t.Fatal(err)
	}
	m := persist.Meta{
		SchemaVersion: 1, ID: local, AgentType: "codex", Name: "n-" + local,
		Cwd: "/work", ConversationID: convID, AuthIdentity: stamp,
	}
	raw, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(stateDir, local, "meta.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return protocol.SessionView{
		ID: "ep-t/" + local, Agent: "codex", Name: "n-" + local, Cwd: "/work",
		Status: status.Status{Process: status.ProcessRunning, Turn: turn},
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
	if code := runRelogin(nil, c, dir, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr %s", code, errb.String())
	}
	if len(c.killed) != 0 {
		t.Fatalf("killed %v; a stamped row belongs to the enabled watcher (double-recycle race)", c.killed)
	}
	if !strings.Contains(out.String(), "watcher") {
		t.Fatalf("output %q does not hand the row to the watcher", out.String())
	}
}

func TestReloginForceRecyclesOnlyUnstampedRows(t *testing.T) {
	withReloginSeams(t, reloginIDCurrent)
	dir := t.TempDir()
	c := &fakeReloginClient{}
	c.views = []protocol.SessionView{
		seedSession(t, dir, "stamped", reloginIDOld, "01a05600-0000-7000-8000-000000000002", status.TurnIdle),
		seedSession(t, dir, "unstamped", "", "01a05600-0000-7000-8000-000000000003", status.TurnIdle),
	}
	var out, errb bytes.Buffer
	if code := runRelogin([]string{"--force"}, c, dir, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr %s", code, errb.String())
	}
	if len(c.killed) != 1 || c.killed[0] != "ep-t/unstamped" {
		t.Fatalf("killed %v; --force asserts only what the watcher cannot judge", c.killed)
	}
	if len(c.launched) != 1 || c.launched[0].Options[protocol.OptionResumeFrom] != "ep-t/unstamped" {
		t.Fatalf("launched %+v; want one resume of the unstamped row", c.launched)
	}
	if len(c.deleted) != 1 || c.deleted[0] != "ep-t/unstamped" {
		t.Fatalf("deleted %v; want the recycled row removed", c.deleted)
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
		seedSession(t, dir, "stamped", reloginIDOld, "01a05600-0000-7000-8000-000000000004", status.TurnIdle),
		seedSession(t, dir, "unstamped", "", "01a05600-0000-7000-8000-000000000005", status.TurnIdle),
		seedSession(t, dir, "current", reloginIDCurrent, "01a05600-0000-7000-8000-000000000006", status.TurnIdle),
	}
	var out, errb bytes.Buffer
	if code := runRelogin(nil, c, dir, &out, &errb); code != 0 {
		t.Fatalf("exit = %d, stderr %s", code, errb.String())
	}
	if len(c.killed) != 2 {
		t.Fatalf("killed %v; with the watcher off this verb IS the recycle path (and the current row stays)", c.killed)
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
	c.views = []protocol.SessionView{seedSession(t, dir, "s1", reloginIDOld, "01a05600-0000-7000-8000-000000000007", status.TurnIdle)}
	var out, errb bytes.Buffer
	if code := runRelogin([]string{"--dry-run"}, c, dir, &out, &errb); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if len(c.killed) != 0 || len(c.launched) != 0 || len(c.deleted) != 0 {
		t.Fatal("--dry-run mutated the daemon")
	}
	if !strings.Contains(out.String(), "would") {
		t.Fatalf("output %q does not report what would happen", out.String())
	}
}

func TestReloginDefersMidTurnAndSkipsUnresumable(t *testing.T) {
	withReloginSeams(t, reloginIDCurrent)
	dir := t.TempDir()
	if err := skeleton.SetAuthWatchDisabled(dir, true); err != nil {
		t.Fatal(err)
	}
	c := &fakeReloginClient{}
	c.views = []protocol.SessionView{
		seedSession(t, dir, "busy", reloginIDOld, "01a05600-0000-7000-8000-000000000008", status.TurnActive),
		seedSession(t, dir, "noconv", reloginIDOld, "", status.TurnIdle),
	}
	var out, errb bytes.Buffer
	if code := runRelogin(nil, c, dir, &out, &errb); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if len(c.killed) != 0 {
		t.Fatalf("killed %v; a mid-turn or unresumable session must never be killed here", c.killed)
	}
	if !strings.Contains(out.String(), "deferred") || !strings.Contains(out.String(), "manual") {
		t.Fatalf("output %q does not explain the two holds", out.String())
	}
}

func TestReloginFailedResumeKeepsTheEndedRow(t *testing.T) {
	withReloginSeams(t, reloginIDCurrent)
	dir := t.TempDir()
	if err := skeleton.SetAuthWatchDisabled(dir, true); err != nil {
		t.Fatal(err)
	}
	c := &fakeReloginClient{launchErr: errors.New("binary missing")}
	c.views = []protocol.SessionView{seedSession(t, dir, "s1", reloginIDOld, "01a05600-0000-7000-8000-000000000009", status.TurnIdle)}
	var out, errb bytes.Buffer
	if code := runRelogin(nil, c, dir, &out, &errb); code != 1 {
		t.Fatalf("exit = %d; a failed resume must be a failed run", code)
	}
	if len(c.deleted) != 0 {
		t.Fatalf("deleted %v after a FAILED resume; the ended row is the user's only handle", c.deleted)
	}
}

func TestReloginAutoTogglesTheWatcherFile(t *testing.T) {
	withReloginSeams(t, reloginIDCurrent)
	dir := t.TempDir()
	var out, errb bytes.Buffer
	if code := runRelogin([]string{"--auto", "off"}, &fakeReloginClient{}, dir, &out, &errb); code != 0 {
		t.Fatalf("--auto off exit = %d", code)
	}
	if !skeleton.AuthWatchDisabled(dir) {
		t.Fatal("--auto off did not disable the watcher")
	}
	if code := runRelogin([]string{"--auto", "on"}, &fakeReloginClient{}, dir, &out, &errb); code != 0 {
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
	c.views = []protocol.SessionView{seedSession(t, dir, "s1", reloginIDOld, "01a05600-0000-7000-8000-00000000000a", status.TurnIdle)}
	var out, errb bytes.Buffer
	if code := runRelogin(nil, c, dir, &out, &errb); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if len(c.killed) != 0 {
		t.Fatalf("killed %v with unknown credentials; nothing may be judged", c.killed)
	}
}
