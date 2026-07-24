package tui

// v0.4 P1 launch/daemon FLOW suite (bd agents-tracker-stc + agents-tracker-5jl).
// Failing-first tests for the two field-test-3 fixes:
//
//   stc — a successful launch (or resume) auto-attaches into the new session,
//         reusing the exact attach path Enter uses on a running row, instead of
//         dropping the user back on the board to hunt for the new row. A launch
//         FAILURE keeps the banner; an attach that then fails banners per the
//         existing attachDoneMsg path.
//
//   5jl — when the hello reveals a daemon build OLDER than this client (client
//         newer), the client auto-restarts the daemon ("upgrading daemon <dv> ->
//         <cv>..."), reconnects, and guards once-per-process against loops. The
//         passive skew notice is kept ONLY for the other direction (daemon newer
//         than client), which the client cannot self-heal.
//
// FROZEN additions the implementer must land (undefined-RED until then):
//
//	type DaemonRestarter func() (Client, error)
//	func WithDaemonRestarter(r DaemonRestarter) Option
//	launchResultMsg{ id, agent string; err error }   // id/agent added
//	classifySkew(daemonVer, clientVer string) skew   // skewNone/skewNotify/skewUpgrade
//	(m rootModel) shouldAutoUpgrade() bool
//	beginUpgradeMsg{} ; daemonRestartedMsg{ client Client; err error }

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// ---------------------------------------------------------------------------
// Test seams shared by the flow suite.
// ---------------------------------------------------------------------------

// withClientVersion overrides the router's own build version, which is "dev" under
// `go test` (version.Version) — and dev builds always suppress skew. The auto-restart
// path only arms for a real stamped client, so the tests inject one.
func withClientVersion(v string) Option { return func(m *rootModel) { m.clientVersion = v } }

// fakeRestarter records auto-restart invocations and returns a canned reconnect.
type fakeRestarter struct {
	mu     sync.Mutex
	calls  int
	client Client
	err    error
}

func (r *fakeRestarter) restart() (Client, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return r.client, r.err
}

func (r *fakeRestarter) called() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// ---------------------------------------------------------------------------
// stc — auto-attach on launch success (consumer side, launchResultMsg handling).
// ---------------------------------------------------------------------------

// A successful launch flows straight into the attach path for the returned session
// id, read-write (the freshly launched session is running).
func TestLaunchResult_SuccessAutoAttachesIntoNewSession(t *testing.T) {
	f := newFakeClient()
	r := &recordingRunner{}
	m := New(f, detectMixed(), WithAttachRunner(r.run))
	m, _ = m.Update(tea.WindowSizeMsg{Width: testCols, Height: testRows})

	// The launch command's success result, carrying the daemon-returned id + agent.
	m2, cmd := m.Update(launchResultMsg{id: "endpoint/new-1", agent: "claude"})
	if cmd != nil {
		if msg := cmd(); msg != nil { // run the (fake) attach and settle
			_, _ = m2.Update(msg) // settle; result deliberately unused
		}
	}

	calls := r.recorded()
	if len(calls) != 1 {
		t.Fatalf("a successful launch must auto-attach exactly once; runner called %d times", len(calls))
	}
	if calls[0].session.ID != "endpoint/new-1" {
		t.Fatalf("auto-attach session = %q, want endpoint/new-1", calls[0].session.ID)
	}
	if calls[0].readOnly {
		t.Fatal("a freshly launched (running) session must attach read-write")
	}
}

// A launch FAILURE keeps the banner behavior and never attaches.
func TestLaunchResult_FailureBannersAndDoesNotAttach(t *testing.T) {
	f := newFakeClient()
	r := &recordingRunner{}
	m := New(f, detectMixed(), WithAttachRunner(r.run))
	m, _ = m.Update(tea.WindowSizeMsg{Width: testCols, Height: testRows})

	m = send(m, launchResultMsg{err: errors.New("daemon: agent not on PATH")})

	if len(r.recorded()) != 0 {
		t.Fatalf("a failed launch must never attach; runner called %d times", len(r.recorded()))
	}
	if v := view(m); !strings.Contains(v, "launch failed") || !strings.Contains(v, "not on PATH") {
		t.Fatalf("a failed launch must keep the banner; view:\n%s", v)
	}
}

// Without an injected runner (the Epic-7 placeholder path), a successful launch
// still switches into the attach screen for the new session.
func TestLaunchResult_SuccessNoRunnerShowsAttachScreen(t *testing.T) {
	f := newFakeClient()
	m := newModel(t, f, detectMixed()) // no attach runner

	m = send(m, launchResultMsg{id: "endpoint/new-1", agent: "claude"})

	if got := m.(rootModel).screen; got != screenAttach {
		t.Fatalf("a successful launch with no runner must show the attach screen; screen = %v", got)
	}
	if m.(rootModel).attach.session.ID != "endpoint/new-1" {
		t.Fatalf("the attach screen must carry the new session id; got %q", m.(rootModel).attach.session.ID)
	}
}

// If the auto-attach itself fails, the reason banners via the existing attachDoneMsg
// path (never a silent drop back to the board).
func TestLaunchResult_AttachFailureBanners(t *testing.T) {
	f := newFakeClient()
	r := &recordingRunner{returnErr: errors.New("daemon: session \"new-1\" is not running")}
	m := New(f, detectMixed(), WithAttachRunner(r.run))
	m, _ = m.Update(tea.WindowSizeMsg{Width: testCols, Height: testRows})

	m2, cmd := m.Update(launchResultMsg{id: "endpoint/new-1", agent: "claude"})
	if cmd == nil {
		t.Fatal("a successful launch must issue the attach command")
	}
	m2, _ = m2.Update(cmd()) // the runner returns its error as attachDoneMsg

	if v := view(m2); !strings.Contains(v, "attach failed") || !strings.Contains(v, "is not running") {
		t.Fatalf("a failed auto-attach must banner the reason; view:\n%s", v)
	}
}

// A launch result with no id (an older producer that does not carry it) stays on the
// board and never attaches — the guard that keeps the change safe.
func TestLaunchResult_NoIDStaysOnBoard(t *testing.T) {
	f := newFakeClient(sWorking("endpoint/s1", "claude", "~/Code/x", "building", time.Minute))
	r := &recordingRunner{}
	m := New(f, detectMixed(), WithAttachRunner(r.run))
	m, _ = m.Update(tea.WindowSizeMsg{Width: testCols, Height: testRows})

	m = send(m, launchResultMsg{}) // success, but no id

	if len(r.recorded()) != 0 {
		t.Fatalf("a launch result with no id must not attach; runner called %d times", len(r.recorded()))
	}
	if got := m.(rootModel).screen; got != screenGeneral {
		t.Fatalf("a launch result with no id must stay on the board; screen = %v", got)
	}
}

// ---------------------------------------------------------------------------
// stc — resume (general.go producer) also auto-attaches into its new session.
// ---------------------------------------------------------------------------

// Pressing 'r' on an ended row issues the resume launch AND, on success, auto-attaches
// into the returned new session (resume is a launch-as-new-session; the user resumed
// precisely to interact with it).
func TestResume_SuccessAutoAttachesIntoNewSession(t *testing.T) {
	f := newFakeClient(sCompleted("endpoint/done1", "claude", "~/Code/x", "exit 0", time.Hour))
	f.launchID = "endpoint/resumed-1"
	r := &recordingRunner{}
	m := New(f, detectMixed(), WithAttachRunner(r.run))
	m, _ = m.Update(tea.WindowSizeMsg{Width: testCols, Height: testRows})

	_, cmd := m.Update(keyRune('r')) // resume the ended row
	if cmd == nil {
		t.Fatal("pressing 'r' on an ended row issued no command")
	}
	m2, acmd := m.Update(cmd()) // feed the launchResultMsg back
	if acmd != nil {
		if msg := acmd(); msg != nil {
			_, _ = m2.Update(msg) // settle; result deliberately unused
		}
	}

	calls := r.recorded()
	if len(calls) != 1 {
		t.Fatalf("a successful resume must auto-attach exactly once; runner called %d times", len(calls))
	}
	if calls[0].session.ID != "endpoint/resumed-1" {
		t.Fatalf("resume auto-attach session = %q, want endpoint/resumed-1", calls[0].session.ID)
	}
}

// End-to-end: submitting a valid launch FORM flows straight into the attach path for
// the daemon-returned session, proving the launch producer (launchCmd carries the id)
// and the auto-attach consumer are wired together.
func TestLaunchForm_SubmitAutoAttachesIntoNewSession(t *testing.T) {
	f := newFakeClient()
	f.launchID = "endpoint/launched-1"
	r := &recordingRunner{}
	m := New(f, detectMixed(), WithAttachRunner(r.run))
	m, _ = m.Update(tea.WindowSizeMsg{Width: testCols, Height: testRows})
	m = send(m, detectMsg{agents: detectMixed()()}) // seed detection so an agent is usable
	m = send(m, keyRune('n'))                       // open the launch form

	// Clear the prefilled cwd and type a real, existing directory so submit is valid.
	for launchOf(m).cwd != "" {
		m = send(m, keyBackspace)
	}
	m = sendType(m, t.TempDir())

	_, cmd := m.Update(keyEnter) // submit
	if cmd == nil {
		t.Fatal("submitting a valid form issued no command")
	}
	// Submit batches launchCmd with an (idempotent) enterGeneral. Walk the batch, feed
	// the launch result back so the auto-attach fires, then settle the attach.
	m2 := m
	for _, msg := range drainBatch(cmd) {
		if _, ok := msg.(launchResultMsg); !ok {
			continue
		}
		var acmd tea.Cmd
		m2, acmd = m2.Update(msg)
		if acmd != nil {
			if amsg := acmd(); amsg != nil {
				m2, _ = m2.Update(amsg)
			}
		}
	}

	calls := r.recorded()
	if len(calls) != 1 {
		t.Fatalf("submitting a valid form must auto-attach into the new session; runner called %d times", len(calls))
	}
	if calls[0].session.ID != "endpoint/launched-1" {
		t.Fatalf("auto-attach session = %q, want endpoint/launched-1", calls[0].session.ID)
	}
	if calls[0].readOnly {
		t.Fatal("a freshly launched session must attach read-write")
	}
}

// drainBatch runs a (possibly batched) command one level deep and returns the messages
// its children produced, so a test can feed a specific one back into Update.
func drainBatch(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	var out []tea.Msg
	for _, c := range batch {
		if c != nil {
			if m := c(); m != nil {
				out = append(out, m)
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// 5jl — skew classification (pure) + the direction-sensitive passive notice.
// ---------------------------------------------------------------------------

func TestClassifySkew_Directions(t *testing.T) {
	cases := []struct {
		daemon, client string
		want           skew
		note           string
	}{
		{"0.1.0", "0.2.0", skewUpgrade, "daemon older than client -> auto-restart"},
		{"0.2.0", "0.3.0", skewUpgrade, "daemon older (minor) -> auto-restart"},
		{"0.3.0", "0.2.0", skewNotify, "daemon newer than client -> passive notice"},
		{"0.2.0", "0.2.0", skewNone, "equal -> nothing"},
		{"dev", "0.2.0", skewNone, "dev daemon -> suppressed"},
		{"0.1.0", "dev", skewNone, "dev client -> suppressed"},
		{"", "0.2.0", skewNone, "no daemon version -> suppressed"},
		{"0.1.0", "", skewNone, "no client version -> suppressed"},
	}
	for _, c := range cases {
		if got := classifySkew(c.daemon, c.client); got != c.want {
			t.Errorf("classifySkew(%q,%q) = %v, want %v (%s)", c.daemon, c.client, got, c.want, c.note)
		}
	}
}

// The passive notice is now kept ONLY for the downgrade direction (daemon newer than
// client), which the client cannot self-heal; the client-newer direction auto-restarts
// and shows no passive notice.
func TestSkewNotice_ShownOnlyWhenDaemonNewer(t *testing.T) {
	got := skewNotice("0.3.0", "0.2.0") // daemon newer
	if !strings.Contains(got, "daemon 0.3.0") || !strings.Contains(got, "swarm 0.2.0") {
		t.Fatalf("skewNotice(daemon newer) must name both versions; got %q", got)
	}
	if !strings.Contains(got, "swarm daemon restart") {
		t.Fatalf("skewNotice(daemon newer) must nudge the restart; got %q", got)
	}
	if got := skewNotice("0.1.0", "0.2.0"); got != "" { // client newer -> auto-restart, no notice
		t.Fatalf("skewNotice(client newer) = %q; want empty (the client auto-restarts)", got)
	}
}

// ---------------------------------------------------------------------------
// 5jl — auto-restart arming + reconnect handling.
// ---------------------------------------------------------------------------

// shouldAutoUpgrade arms only when a restarter is injected, the client is newer than
// the daemon, and no restart has been attempted yet (the once-per-process guard).
func TestShouldAutoUpgrade_Conditions(t *testing.T) {
	r := &fakeRestarter{}
	base := func() rootModel {
		return rootModel{restarter: r.restart, daemonVersion: "0.1.0", clientVersion: "0.2.0"}
	}
	if !base().shouldAutoUpgrade() {
		t.Fatal("client-newer with a restarter must arm the auto-upgrade")
	}
	noR := base()
	noR.restarter = nil
	if noR.shouldAutoUpgrade() {
		t.Fatal("no restarter -> must not arm")
	}
	attempted := base()
	attempted.restartAttempted = true
	if attempted.shouldAutoUpgrade() {
		t.Fatal("already attempted -> must not arm again (once-per-process guard)")
	}
	newer := base()
	newer.daemonVersion = "0.3.0" // daemon newer than client -> notice, not upgrade
	if newer.shouldAutoUpgrade() {
		t.Fatal("daemon-newer must not auto-upgrade (that direction is the passive notice)")
	}
}

// beginUpgradeMsg banners "upgrading daemon <dv> -> <cv>...", marks the attempt, and
// fires the restart command.
func TestBeginUpgrade_BannersAndFiresRestart(t *testing.T) {
	r := &fakeRestarter{client: newFakeClient()}
	gm := newGeneralModel(nil)
	gm.width = testCols
	m := rootModel{general: gm, width: testCols, height: testRows, restarter: r.restart, daemonVersion: "0.1.0", clientVersion: "0.2.0"}

	m2, cmd := m.Update(beginUpgradeMsg{})
	rm := m2.(rootModel)
	if !rm.restartAttempted {
		t.Fatal("beginUpgradeMsg must mark the restart attempt (once-per-process guard)")
	}
	if v := stripANSI(m2.View().Content); !strings.Contains(v, "upgrading daemon 0.1.0 -> 0.2.0") {
		t.Fatalf("beginUpgradeMsg must banner the upgrade; view:\n%s", v)
	}
	execCmd(cmd) // run the restart command
	if r.called() != 1 {
		t.Fatalf("beginUpgradeMsg must fire the restart exactly once; got %d", r.called())
	}
}

// A second beginUpgradeMsg is a no-op: the once-per-process guard prevents restart loops.
func TestBeginUpgrade_GuardsAgainstLoops(t *testing.T) {
	r := &fakeRestarter{client: newFakeClient()}
	gm := newGeneralModel(nil)
	gm.width = testCols
	m := rootModel{general: gm, width: testCols, height: testRows, restarter: r.restart, daemonVersion: "0.1.0", clientVersion: "0.2.0"}

	m2, cmd := m.Update(beginUpgradeMsg{})
	execCmd(cmd)
	_, cmd2 := m2.Update(beginUpgradeMsg{}) // second attempt
	execCmd(cmd2)

	if r.called() != 1 {
		t.Fatalf("a second beginUpgradeMsg must not restart again; restarter called %d times", r.called())
	}
}

// On a successful reconnect the router swaps to the fresh client, re-reads its (now
// matching) build version, and acknowledges — clearing the skew.
func TestDaemonRestarted_SuccessSwapsClientAndClearsSkew(t *testing.T) {
	newDaemon := buildVersionClient{fakeClient: newFakeClient(), daemonVersion: "0.2.0"}
	gm := newGeneralModel(nil)
	gm.width = testCols
	m := rootModel{general: gm, width: testCols, height: testRows, daemonVersion: "0.1.0", clientVersion: "0.2.0", restartAttempted: true}

	m2 := send(m, daemonRestartedMsg{client: newDaemon})
	rm := m2.(rootModel)
	if rm.daemonVersion != "0.2.0" {
		t.Fatalf("reconnect must adopt the replacement's build version; got %q", rm.daemonVersion)
	}
	// The skew notice must be gone now that the versions match.
	if v := stripANSI(m2.View().Content); strings.Contains(v, "differs from swarm") {
		t.Fatalf("after a successful upgrade the skew notice must be gone; view:\n%s", v)
	}
}

// A failed reconnect banners the reason (never a silent failure).
func TestDaemonRestarted_FailureBanners(t *testing.T) {
	gm := newGeneralModel(nil)
	gm.width = testCols
	m := rootModel{general: gm, width: testCols, height: testRows, daemonVersion: "0.1.0", clientVersion: "0.2.0", restartAttempted: true}

	m2 := send(m, daemonRestartedMsg{err: errors.New("previous daemon did not release the lock")})

	if v := stripANSI(m2.View().Content); !strings.Contains(v, "daemon upgrade failed") || !strings.Contains(v, "release the lock") {
		t.Fatalf("a failed reconnect must banner the reason; view:\n%s", v)
	}
}

// End-to-end within the tui runtime: a client-newer New auto-restarts on Init, shows
// the upgrade banner, drives the (fake) restart, and reconnects to the replacement.
func TestAutoUpgrade_EndToEndOverProgram(t *testing.T) {
	if testing.Short() {
		t.Skip("drives the tea runtime")
	}
	f := newFakeClient(sWorking("endpoint/s1", "claude", "~/Code/x", "building", time.Minute))
	oldDaemon := buildVersionClient{fakeClient: f, daemonVersion: "0.1.0"}
	reconnected := buildVersionClient{fakeClient: newFakeClient(), daemonVersion: "0.2.0"}
	r := &fakeRestarter{client: reconnected}

	m := New(oldDaemon, detectMixed(), WithDaemonRestarter(r.restart), withClientVersion("0.2.0"))
	tm := startTM(t, m)

	// Init auto-restarts (the "upgrading daemon..." banner is asserted transiently in the
	// beginUpgradeMsg unit test; the fake restart resolves instantly here). Wait on the
	// stable post-reconnect ack, which proves the whole Init -> restart -> reconnect chain.
	waitContains(t, tm, "daemon upgraded")
	quitTM(t, tm)

	if r.called() != 1 {
		t.Fatalf("the client-newer skew must trigger exactly one auto-restart; got %d", r.called())
	}
}
