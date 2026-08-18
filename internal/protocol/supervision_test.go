package protocol

// FAILING-FIRST suite for ADR-010 Amendment 3, slice 1: the supervision mode of a
// handoff child on the wire (LaunchReq.Supervision), on the roster (SessionView.
// Supervision + SupervisionPending) and the daemon-authored write seam
// (Server.SendInput) the supervisor uses to notify the source session.
//
// FROZEN API (schema.go, re-exported by types.go like SpawnIntent*):
//
//	const SupervisionPassive, SupervisionManual, SupervisionNone = "passive", "manual", "none"
//	LaunchReq.Supervision           string `json:"supervision,omitempty"`
//	SessionView.Supervision         string `json:"supervision,omitempty"`
//	SessionView.SupervisionPending  bool   `json:"supervision_pending,omitempty"`
//	func (s *Server) SetSupervisionPendingFunc(fn func(local string) bool) // nil clears
//	func (s *Server) SendInput(local string, req SendInputReq) error
//
// handleLaunch: a Supervision outside the closed vocabulary, or a non-empty one
// without SpawnIntent == handoff, is refused CodeInvalidField before any daemon side
// effect; an accepted value reaches daemon.LaunchSpec.Supervision verbatim.
//
// RED today: none of the identifiers above exist, so this file fails to compile.

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/status"
)

// TestSupervision_Vocabulary pins the three mode strings the CLI, the TUI form and
// the daemon all spell the same way.
func TestSupervision_Vocabulary(t *testing.T) {
	want := map[string]string{SupervisionPassive: "passive", SupervisionManual: "manual", SupervisionNone: "none"}
	for got, exp := range want {
		if got != exp {
			t.Errorf("supervision constant = %q, want %q", got, exp)
		}
	}
}

// TestSupervision_WireKeysOmitEmpty: the new keys ride the wire under snake_case names
// and are absent when unset, so an unsupervised launch and an ordinary roster row
// serialize exactly as before (the additive-field discipline every prior field kept).
func TestSupervision_WireKeysOmitEmpty(t *testing.T) {
	cases := []struct {
		name    string
		v       any
		present []string
		absent  []string
	}{
		{"launch with mode", LaunchReq{Agent: "claude", Supervision: SupervisionPassive},
			[]string{`"supervision":"passive"`}, nil},
		{"launch without mode", LaunchReq{Agent: "claude"},
			nil, []string{"supervision"}},
		{"view with mode and pending", SessionView{ID: "e/c", Supervision: SupervisionPassive, SupervisionPending: true},
			[]string{`"supervision":"passive"`, `"supervision_pending":true`}, nil},
		{"view without either", SessionView{ID: "e/c"},
			nil, []string{"supervision"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.v)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			for _, p := range tc.present {
				if !strings.Contains(string(b), p) {
					t.Errorf("JSON %s lacks %s", b, p)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(string(b), a) {
					t.Errorf("JSON %s emitted %q although the field is unset (omitempty)", b, a)
				}
			}
		})
	}
}

// TestSupervision_LaunchValidation is the handleLaunch table: the closed vocabulary
// ("" allowed) and the pairing rule (a mode only makes sense on a handoff). Accepted
// values are forwarded verbatim; refusals are CodeInvalidField with nothing launched.
func TestSupervision_LaunchValidation(t *testing.T) {
	cases := []struct {
		name        string
		spawnedFrom string
		intent      string
		supervision string
		ok          bool
	}{
		{"no supervision, no lineage", "", "", "", true},
		{"passive under handoff", "sess-parent-1", SpawnIntentHandoff, SupervisionPassive, true},
		{"manual under handoff", "sess-parent-1", SpawnIntentHandoff, SupervisionManual, true},
		{"none under handoff", "sess-parent-1", SpawnIntentHandoff, SupervisionNone, true},
		{"unknown mode", "sess-parent-1", SpawnIntentHandoff, "bogus", false},
		{"wrong case", "sess-parent-1", SpawnIntentHandoff, "Passive", false},
		{"mode without lineage", "", "", SupervisionPassive, false},
		{"mode with spawned_from but no intent", "sess-parent-1", "", SupervisionManual, false},
		{"mode under delegate", "sess-parent-1", SpawnIntentDelegate, SupervisionNone, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newStubDaemon()
			rc := rawDial(t, serveStub(t, stub))
			rep := rc.hello(Version, nil)

			req := lineageLaunchReq(t)
			req.SpawnedFrom, req.SpawnIntent, req.Supervision = tc.spawnedFrom, tc.intent, tc.supervision
			rc.writeControl(Control{Op: OpLaunch, EndpointID: rep.EndpointID, Launch: &req})
			got := rc.readControl()
			specs := stub.launchSpecs()

			if !tc.ok {
				if got.Op != OpError || got.ErrorCode != CodeInvalidField {
					t.Fatalf("launch with supervision %q (intent %q) = op %q code %q; want error/invalid_field",
						tc.supervision, tc.intent, got.Op, got.ErrorCode)
				}
				if !strings.Contains(got.Error, "supervision") {
					t.Errorf("refusal %q does not name the supervision field", got.Error)
				}
				if len(specs) != 0 {
					t.Fatalf("daemon launched %d sessions for a refused supervision; want 0", len(specs))
				}
				return
			}
			if got.Op == OpError {
				t.Fatalf("launch with supervision %q refused: %q / %q", tc.supervision, got.Error, got.ErrorCode)
			}
			if len(specs) != 1 {
				t.Fatalf("DaemonAPI.Launch called %d times, want 1", len(specs))
			}
			if specs[0].Supervision != tc.supervision {
				t.Errorf("daemon LaunchSpec.Supervision = %q, want %q carried verbatim", specs[0].Supervision, tc.supervision)
			}
		})
	}
}

// TestSupervision_RosterCarriesModeAndPending: stampView copies the persisted mode onto
// every view (list and subscribe), and stamps SupervisionPending from the registered
// source: false when none is registered, true only for the id the source names, and
// cleared again by registering nil.
func TestSupervision_RosterCarriesModeAndPending(t *testing.T) {
	child := persist.Meta{
		ID: "child9", AgentType: "codex", Cwd: "/tmp",
		Status:      status.Status{Process: status.ProcessRunning, Turn: status.TurnIdle, Interaction: status.InteractionNone},
		SpawnedFrom: "parent1", SpawnIntent: SpawnIntentHandoff, Supervision: SupervisionPassive,
	}
	parent := statusMeta("parent1", status.TurnIdle, status.InteractionNone)

	stub := newStubDaemon()
	stub.setMetas(parent, child)
	sock, srv := serveStubServer(t, stub)
	c := dialClient(t, sock, []string{"subscribe"})

	byLocal := func() map[string]SessionView {
		t.Helper()
		views, err := c.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		out := map[string]SessionView{}
		for _, v := range views {
			_, local, ok := ParseID(v.ID)
			if !ok {
				t.Fatalf("roster row id %q is not namespaced", v.ID)
			}
			out[local] = v
		}
		return out
	}

	rows := byLocal()
	if rows["child9"].Supervision != SupervisionPassive || rows["parent1"].Supervision != "" {
		t.Errorf("list supervision = child %q parent %q; want %q and empty (carried from meta)",
			rows["child9"].Supervision, rows["parent1"].Supervision, SupervisionPassive)
	}
	if rows["child9"].SupervisionPending || rows["parent1"].SupervisionPending {
		t.Errorf("no pending source registered, yet a row is stamped pending: child=%v parent=%v",
			rows["child9"].SupervisionPending, rows["parent1"].SupervisionPending)
	}

	var pending atomic.Bool
	srv.SetSupervisionPendingFunc(func(local string) bool { return pending.Load() && local == "child9" })
	if rows = byLocal(); rows["child9"].SupervisionPending {
		t.Errorf("source answers false, yet child9 is stamped pending")
	}
	pending.Store(true)
	rows = byLocal()
	if !rows["child9"].SupervisionPending {
		t.Errorf("child9 has a pending supervision event but its list row is not stamped")
	}
	if rows["parent1"].SupervisionPending {
		t.Errorf("parent1 has no pending event but its list row is stamped")
	}

	events, err := c.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	stub.pushStatus(child)
	select {
	case ev := <-events:
		if ev.Session.Supervision != SupervisionPassive || !ev.Session.SupervisionPending {
			t.Errorf("subscribe view = supervision %q pending %v; want %q and true",
				ev.Session.Supervision, ev.Session.SupervisionPending, SupervisionPassive)
		}
	case <-time.After(recvTimeout):
		t.Fatal("no subscribe event within the deadline")
	}

	srv.SetSupervisionPendingFunc(nil)
	if rows = byLocal(); rows["child9"].SupervisionPending {
		t.Errorf("SetSupervisionPendingFunc(nil) did not clear the source; child9 still stamped pending")
	}
}

// TestSupervision_ServerSendInputWritesTheClientFrames: the daemon-authored seam writes
// EXACTLY what a client send_input writes for the same request (text as one paste,
// then the CR alone, one gap apart), through the same per-session serialization.
func TestSupervision_ServerSendInputWritesTheClientFrames(t *testing.T) {
	d := newSendInputDaemon()
	d.setMetas(statusMeta("sess1", status.TurnIdle, status.InteractionNone))
	sock := tmpSock(t)
	srv, err := Serve(d, sock)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	req := SendInputReq{Text: "hi", Submit: true}
	rc := rawDial(t, sock)
	rep := rc.hello(Version, nil)
	rc.writeControl(Control{Op: OpSendInput, EndpointID: rep.EndpointID, SessionID: rep.EndpointID + "/sess1", SendInput: &req})
	if got := nextControl(t, rc); got.Op != OpOK {
		t.Fatalf("client send_input reply = op %q error %q; want OpOK", got.Op, got.Error)
	}
	if err := srv.SendInput("sess1", req); err != nil {
		t.Fatalf("Server.SendInput: %v", err)
	}
	if n := d.attachCount(); n != 2 {
		t.Fatalf("daemon opened %d upstream streams, want 2 (one per message, released after each)", n)
	}
	viaClient, direct := d.stream(0).written(), d.stream(1).written()
	assertPasteThenSubmit(t, direct, "hi")
	if len(viaClient) != len(direct) {
		t.Fatalf("Server.SendInput wrote %d frames, the client path %d; they must be identical", len(direct), len(viaClient))
	}
	for i := range direct {
		if string(direct[i].payload) != string(viaClient[i].payload) {
			t.Errorf("frame %d: Server.SendInput wrote %q, the client path %q", i, direct[i].payload, viaClient[i].payload)
		}
	}
}

// TestSupervision_ServerSendInputRefusals: a non-running or unknown session, an
// over-bound text and a closed Server are all refused with an error and NO write
// (no upstream stream is even opened).
func TestSupervision_ServerSendInputRefusals(t *testing.T) {
	running := statusMeta("sess1", status.TurnIdle, status.InteractionNone)
	exited := running
	exited.Status.Process = status.ProcessExited
	cases := []struct {
		name   string
		metas  []persist.Meta
		local  string
		req    SendInputReq
		closed bool
	}{
		{"exited session", []persist.Meta{exited}, "sess1", SendInputReq{Text: "hi", Submit: true}, false},
		{"unknown session", []persist.Meta{running}, "nope", SendInputReq{Text: "hi", Submit: true}, false},
		{"text over the bound", []persist.Meta{running}, "sess1", SendInputReq{Text: strings.Repeat("a", MaxSendInputText+1), Submit: true}, false},
		{"empty request", []persist.Meta{running}, "sess1", SendInputReq{}, false},
		{"closed server", []persist.Meta{running}, "sess1", SendInputReq{Text: "hi", Submit: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newSendInputDaemon()
			d.setMetas(tc.metas...)
			srv, err := Serve(d, tmpSock(t))
			if err != nil {
				t.Fatalf("Serve: %v", err)
			}
			t.Cleanup(func() { _ = srv.Close() })
			if tc.closed {
				_ = srv.Close()
			}
			if err := srv.SendInput(tc.local, tc.req); err == nil {
				t.Fatalf("Server.SendInput (%s) returned nil; want an error", tc.name)
			}
			if n := d.attachCount(); n != 0 {
				t.Fatalf("a refused Server.SendInput (%s) opened %d upstream streams; want 0", tc.name, n)
			}
		})
	}
}

// TestSupervision_RemoteTierLaunchRefusesAMode (review finding on slice 1): a
// supervision mode makes spawned_from ACTIONABLE — the daemon will later type into that
// session — and LaunchContentHash does not bind lineage, so a gateway could graft
// `supervision=passive` onto a validly signed launch. The remote tier refuses any mode
// with CodePolicy before any daemon side effect; the owner tier is unaffected.
func TestSupervision_RemoteTierLaunchRefusesAMode(t *testing.T) {
	stub := newStubDaemon()
	sock := serveRemoteAPI(t, allowAllLaunchPolicy{stub})
	rc := rawDial(t, sock)
	rep := rc.hello(Version, []string{CapRemoteGateway})

	req := policyLaunchReq(t)
	req.SpawnedFrom, req.SpawnIntent, req.Supervision = "src1", SpawnIntentHandoff, SupervisionPassive
	rc.writeControl(remoteLaunchControl(rep.EndpointID, req))
	got := rc.readControl()
	if got.Op != OpError || got.ErrorCode != CodePolicy || !strings.Contains(got.Error, "supervision") {
		t.Fatalf("remote launch with supervision = op %q code %q err %q; want error/%s naming supervision", got.Op, got.ErrorCode, got.Error, CodePolicy)
	}
	if n := len(stub.launchSpecs()); n != 0 {
		t.Fatalf("daemon launched %d sessions for a refused remote op; want 0", n)
	}
}

// TestSupervision_ServerSendInputIsOwnerTierOnly: the seam's atomicity claim is owner-tier
// only (distinct leases per tier), so a remote-tier Server refuses it outright.
func TestSupervision_ServerSendInputIsOwnerTierOnly(t *testing.T) {
	d := newSendInputDaemon()
	d.setMetas(statusMeta("s1", status.TurnIdle, status.InteractionNone))
	srv := NewServer(d, "ep")
	srv.remoteTier = true
	if err := srv.SendInput("s1", SendInputReq{Text: "hi", Submit: true}); err == nil {
		t.Fatal("remote-tier Server.SendInput succeeded; want a refusal")
	}
	if n := d.attachCount(); n != 0 {
		t.Fatalf("remote-tier SendInput opened %d upstream streams; want 0", n)
	}
}
