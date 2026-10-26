package skeleton

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
	"github.com/Nathandela/swarm/internal/vt"
)

func claudeResolver(string) (adapter.Adapter, bool) { return claude.New(), true }

type gateUpstream struct {
	*fakeUpstream
	attempts    [][]byte
	fail        error
	before      func()
	order       []string
	unsupported bool
}

func (u *gateUpstream) TryControlInput(keys []byte) error {
	if u.unsupported {
		return protocol.ErrControlInputUnsupported
	}
	if u.before != nil {
		u.before()
	}
	u.attempts = append(u.attempts, append([]byte(nil), keys...))
	u.order = append(u.order, "write")
	if strings.Contains(string(keys), "\r") && len(keys) != 1 {
		return errors.New("fake CLI rejects combined selection/Enter")
	}
	return u.fail
}

type gateRig struct {
	d        *Daemon
	id       string
	identity launchGateIdentity
	up       *gateUpstream
	anchor   *tapSub
}

func newGateRig(t *testing.T, core *daemon.Daemon, meta persist.Meta, snap *vt.Snap) *gateRig {
	t.Helper()
	raw, _ := snapJSON(snap)
	up := &gateUpstream{fakeUpstream: newFakeUpstream(raw)}
	d := &Daemon{core: core, closing: make(chan struct{})}
	d.initInteractions()
	d.setAdapterForTest(claudeResolver)
	identity := launchGateIdentity{"test-instance", meta.ShimPID, meta.ShimStartTime}
	d.capStore.instances = map[string]string{meta.ID: identity.instance}
	d.capStore.incarnations = map[string]sessionIncarnation{meta.ID: {ShimPID: identity.shimPID, ShimStartTime: identity.shimStart}}
	mgr := newTapManager(func(string) (protocol.SessionStream, error) { return up, nil })
	mgr.gateIdentity = d.cachedLaunchGateIdentity
	mgr.foreignWrite = d.abandonLaunchGateBinding
	mgr.gateFault = d.abandonLaunchGateBinding
	d.api = &coreAPI{core: core, tap: mgr}
	anchor, err := mgr.subscribe(meta.ID, readOnly)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = anchor.Close() })
	return &gateRig{d, meta.ID, identity, up, anchor}
}
func snapJSON(snap *vt.Snap) ([]byte, error) { return json.Marshal(snap) }
func (r *gateRig) snap(t *testing.T) *vt.Snap {
	t.Helper()
	s, err := r.anchor.launchGateSnapshot(r.identity)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func (r *gateRig) paint(t *testing.T, s *vt.Snap) {
	t.Helper()
	r.anchor.t.mu.Lock()
	r.anchor.t.mirror.Feed(vt.RenderSnapshotClipped(s, 0, 0))
	r.anchor.t.mu.Unlock()
}
func (r *gateRig) settle() {
	r.d.gateMu.Lock()
	r.d.gateDone[r.id].observed = time.Now().Add(-time.Second)
	r.d.gateMu.Unlock()
}
func (r *gateRig) poll(t *testing.T) { r.d.answerLaunchGate(r.id, r.snap(t), r.identity) }
func (r *gateRig) phase() launchGatePhase {
	r.d.gateMu.Lock()
	defer r.d.gateMu.Unlock()
	return r.d.gateDone[r.id].phase
}
func trustSelected(s *vt.Snap) *vt.Snap {
	raw, _ := snapJSON(s)
	copy, _ := vt.DecodeSnapshot(raw)
	for y, line := range copy.Lines {
		var text strings.Builder
		for _, run := range line.Runs {
			text.WriteString(run.Text)
		}
		row := text.String()
		if strings.Contains(row, "No, exit") {
			row = strings.Replace(row, "❯", " ", 1)
		} else if strings.Contains(row, "Yes, I trust this folder") {
			row = strings.Replace(row, "   Yes,", " ❯ Yes,", 1)
			copy.CursorY = y
		} else {
			continue
		}
		copy.Lines[y] = vt.Line{Runs: []vt.Run{{Text: row, Width: copy.Cols}}}
	}
	return copy
}
func loadGateFixture(t *testing.T, name string) *vt.Snap {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("../adapter/claude/testdata/trustdialog", name+".snap.json"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := vt.DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestLaunchGateStagedSelectionAndSingleConfirmation(t *testing.T) {
	sk := assemble(t)
	m := launchFakeSized(t, sk, "idle 600s\n", 150, 44)
	for _, version := range []string{"2.1.258", "2.1.310"} {
		t.Run(version, func(t *testing.T) {
			r := newGateRig(t, sk.core, m, loadGateFixture(t, "trust-dialog-"+version))
			r.poll(t)
			r.poll(t)
			if len(r.up.attempts) != 0 {
				t.Fatal("wrote before initial settle")
			}
			r.settle()
			r.poll(t)
			if !reflect.DeepEqual(r.up.attempts, [][]byte{[]byte("\x1b[B")}) {
				t.Fatalf("movement = %q", r.up.attempts)
			}
			r.paint(t, trustSelected(r.snap(t)))
			r.poll(t)
			r.poll(t)
			if len(r.up.attempts) != 1 {
				t.Fatal("Enter before selected Yes settled")
			}
			r.settle()
			r.poll(t)
			r.poll(t)
			if !reflect.DeepEqual(r.up.attempts, [][]byte{[]byte("\x1b[B"), []byte("\r")}) || r.phase() != launchGateConsumed {
				t.Fatalf("attempts=%q phase=%v", r.up.attempts, r.phase())
			}
		})
	}
}

func TestLaunchGateInitiallySelectedYesNeedsOnlySettledEnter(t *testing.T) {
	sk := assemble(t)
	m := launchFakeSized(t, sk, "idle 600s\n", 150, 44)
	raw, err := os.ReadFile("../adapter/claude/testdata/permdialog/neg-trust-dialog-2.1.231.snap.json")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := vt.DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	r := newGateRig(t, sk.core, m, snap)
	r.poll(t)
	if len(r.up.attempts) != 0 {
		t.Fatal("initial Yes confirmed immediately")
	}
	r.settle()
	r.poll(t)
	r.poll(t)
	if !reflect.DeepEqual(r.up.attempts, [][]byte{[]byte("\r")}) {
		t.Fatalf("initial Yes=%q", r.up.attempts)
	}
}

func TestLaunchGateRefusalsNeverRetry(t *testing.T) {
	sk := assemble(t)
	m := launchFakeSized(t, sk, "idle 600s\n", 150, 44)
	cases := []struct {
		name   string
		change func(*gateRig)
	}{
		{"foreign input", func(r *gateRig) { _ = r.anchor.t.newWriteSubForGateTest().Input([]byte("owner")) }},
		{"foreign control", func(r *gateRig) { _ = r.anchor.t.newWriteSubForGateTest().ControlKeys([]byte("\x03")) }},
		{"direct marker", func(r *gateRig) { r.d.directInput.unresolved = map[string]directInputRecord{r.id: {}} }},
		{"direct marker fsync busy", func(r *gateRig) { r.d.directInput.mu.Lock(); t.Cleanup(r.d.directInput.mu.Unlock) }},
		{"capability author busy", func(r *gateRig) { r.d.capStore.mu.Lock(); t.Cleanup(r.d.capStore.mu.Unlock) }},
		{"lifecycle busy", func(r *gateRig) { lane := r.d.composerLaneFor(r.id); lane.endMu.Lock(); t.Cleanup(lane.endMu.Unlock) }},
		{"FIFO busy", func(r *gateRig) { r.d.composerLaneFor(r.id).nextTicket++ }},
		{"Stop barrier", func(r *gateRig) { r.d.composerLaneFor(r.id).stopsInFlight++ }},
		{"sticky parser fault", func(r *gateRig) { r.anchor.t.mu.Lock(); r.anchor.t.gateInvalid = true; r.anchor.t.mu.Unlock() }},
		{"unknown binding", func(r *gateRig) { r.anchor.t.gateBinding = launchGateIdentity{} }},
		{"closing", func(r *gateRig) { close(r.d.closing) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newGateRig(t, sk.core, m, launchTrustSnapshot(t))
			r.poll(t)
			r.settle()
			// Local test cleanup must release held locks before the next poll/phase read.
			switch tc.name {
			case "direct marker fsync busy":
				r.d.directInput.mu.Lock()
				r.d.answerLaunchGate(r.id, r.snap(t), r.identity)
				r.d.directInput.mu.Unlock()
			case "capability author busy":
				r.d.capStore.mu.Lock()
				r.d.answerLaunchGate(r.id, r.snap(t), r.identity)
				r.d.capStore.mu.Unlock()
			default:
				tc.change(r)
				r.d.answerLaunchGate(r.id, launchTrustSnapshot(t), r.identity)
			}
			if len(r.up.attempts) != 0 || r.phase() != launchGateAbandoned {
				t.Fatalf("refusal wrote %q, phase=%v", r.up.attempts, r.phase())
			}
			lane := r.d.composerLaneFor(r.id)
			if tc.name != "FIFO busy" && lane.nextTicket != lane.servingTicket {
				t.Fatal("gate leaked FIFO reservation")
			}
		})
	}
}

func (t *tap) newWriteSubForGateTest() *tapSub { return &tapSub{t: t, mode: readWrite} }

func TestLaunchGateUncertainWritesAndResetConsumeAttempt(t *testing.T) {
	sk := assemble(t)
	m := launchFakeSized(t, sk, "idle 600s\n", 150, 44)
	for _, phase := range []string{"movement error", "Enter error", "reset", "different modal"} {
		t.Run(phase, func(t *testing.T) {
			r := newGateRig(t, sk.core, m, launchTrustSnapshot(t))
			r.poll(t)
			r.settle()
			if phase == "movement error" {
				r.up.fail = errors.New("partial write")
			}
			r.poll(t)
			if phase == "movement error" {
				r.up.fail = nil
				r.poll(t)
				if len(r.up.attempts) != 1 || r.phase() != launchGateAbandoned {
					t.Fatal("movement retried")
				}
				return
			}
			r.paint(t, trustSelected(r.snap(t)))
			r.poll(t)
			r.settle()
			switch phase {
			case "Enter error":
				r.up.fail = errors.New("deadline clear failed")
			case "reset":
				r.paint(t, launchTrustSnapshot(t))
			case "different modal":
				r.paint(t, &vt.Snap{Version: 1, Cols: 150, Rows: 44, Lines: make([]vt.Line, 44)})
			}
			r.poll(t)
			r.poll(t)
			want := 1
			if phase == "Enter error" {
				want = 2
			}
			if len(r.up.attempts) != want {
				t.Fatalf("repeated/stray input %q", r.up.attempts)
			}
		})
	}
}

func TestLaunchGateCurrentMirrorAndExactRetirement(t *testing.T) {
	sk := assemble(t)
	m := launchFakeSized(t, sk, "idle 600s\n", 150, 44)
	t.Run("current mirror instead of immutable seed", func(t *testing.T) {
		r := newGateRig(t, sk.core, m, launchTrustSnapshot(t))
		r.poll(t)
		r.settle()
		sample := r.snap(t)
		r.d.api.tap.onSubscribe = func(string) { r.paint(t, &vt.Snap{Version: 1, Cols: 150, Rows: 44, Lines: make([]vt.Line, 44)}) }
		r.d.answerLaunchGate(r.id, sample, r.identity)
		if len(r.up.attempts) != 0 || r.phase() != launchGateAbandoned {
			t.Fatal("typed from stale seed")
		}
	})
	t.Run("replacement cannot inherit or be retired by old callback", func(t *testing.T) {
		r := newGateRig(t, sk.core, m, launchTrustSnapshot(t))
		r.poll(t)
		old := r.d.gateDone[r.id]
		r.d.capStore.instances[r.id] = "replacement"
		replacement, _ := r.d.cachedLaunchGateIdentity(r.id)
		r.d.answerLaunchGate(r.id, r.snap(t), replacement)
		next := r.d.gateDone[r.id]
		if next == old || next.phase != launchGateInitial || len(r.up.attempts) != 0 {
			t.Fatal("replacement inherited phase")
		}
		r.d.retireLaunchGate(r.id, old)
		r.d.abandonLaunchGateBinding(r.id, old.identity)
		if r.d.gateDone[r.id] != next || next.phase != launchGateInitial {
			t.Fatal("old callback retired successor")
		}
	})
	t.Run("observer failure and late retirement before wire", func(t *testing.T) {
		for _, fail := range []bool{true, false} {
			r := newGateRig(t, sk.core, m, launchTrustSnapshot(t))
			r.poll(t)
			r.settle()
			r.d.api.tap.beforeWrite = func(string) error {
				r.up.order = append(r.up.order, "fence")
				if fail {
					return errors.New("execution fence refused")
				}
				r.d.retireLaunchGate(r.id, r.d.gateDone[r.id])
				return nil
			}
			r.poll(t)
			if len(r.up.attempts) != 0 || !reflect.DeepEqual(r.up.order, []string{"fence"}) {
				t.Fatalf("observer/retirement crossed write %v", r.up.order)
			}
		}
	})
}

func TestLaunchGateTryReadsAndReleaseOrder(t *testing.T) {
	sk := assemble(t)
	m := launchFakeSized(t, sk, "idle 600s\n", 150, 44)
	r := newGateRig(t, sk.core, m, launchTrustSnapshot(t))
	r.anchor.t.mu.Lock()
	if _, err := r.anchor.launchGateSnapshot(r.identity); err == nil {
		t.Fatal("waited on busy mirror")
	}
	r.anchor.t.mu.Unlock()
	r.poll(t)
	r.settle()
	r.up.before = func() {
		if !r.d.gateMu.TryLock() {
			t.Fatal("gateMu held during I/O")
		}
		r.d.gateMu.Unlock()
		if !r.anchor.t.mu.TryLock() {
			t.Fatal("tap.mu held during I/O")
		}
		r.anchor.t.mu.Unlock()
	}
	r.poll(t)
	lane := r.d.composerLaneFor(r.id)
	if !lane.mu.TryLock() {
		t.Fatal("lane.mu leaked")
	}
	lane.mu.Unlock()
	if !lane.endMu.TryLock() {
		t.Fatal("endMu leaked")
	}
	lane.endMu.Unlock()
	if lane.nextTicket != lane.servingTicket {
		t.Fatal("FIFO reservation leaked")
	}
	if len(r.up.attempts) != 1 {
		t.Fatal("missing positive write")
	}
}

func TestLaunchGateTapObserversPreserveOwnMovementAndReadOnly(t *testing.T) {
	sk := assemble(t)
	m := launchFakeSized(t, sk, "idle 600s\n", 150, 44)
	r := newGateRig(t, sk.core, m, launchTrustSnapshot(t))
	r.poll(t)
	_ = r.anchor.Input([]byte("read-only"))
	_ = r.anchor.ControlKeys([]byte("read-only"))
	_ = r.anchor.Resize(80, 24)
	if r.phase() != launchGateInitial {
		t.Fatal("read-only observer cancelled gate")
	}
	r.d.api.tap.beforeWrite = func(string) error { r.up.order = append(r.up.order, "fence"); return nil }
	r.settle()
	r.poll(t)
	if r.phase() != launchGateAwaitYes || !reflect.DeepEqual(r.up.order, []string{"fence", "write"}) {
		t.Fatalf("own movement/observer order: %v %v", r.phase(), r.up.order)
	}
}

func TestLaunchGateSampleFailureAndStickyParserFault(t *testing.T) {
	sk := assemble(t)
	m := launchFakeSized(t, sk, "idle 600s\n", 150, 44)
	r := newGateRig(t, sk.core, m, launchTrustSnapshot(t))
	r.poll(t)
	r.d.sampleGridFrom(r.id, func() ([]byte, error) { return nil, errors.New("snapshot failed") })
	if r.phase() != launchGateAbandoned {
		t.Fatal("sample failure retained gate")
	}
	failed := make(chan struct{})
	r.d.api.tap.gateFault = func(id string, identity launchGateIdentity) {
		if !r.anchor.t.mu.TryLock() {
			t.Error("fault callback held tap mutex")
		} else {
			r.anchor.t.mu.Unlock()
		}
		r.d.abandonLaunchGateBinding(id, identity)
		close(failed)
	}
	r.up.frames <- []byte("\x1b[1;94r\x1bM")
	select {
	case <-failed:
	case <-time.After(time.Second):
		t.Fatal("parser fault did not invalidate gate")
	}
	if r.phase() != launchGateAbandoned {
		t.Fatal("parser fault retained gate")
	}
	if _, err := r.anchor.launchGateSnapshot(r.identity); err == nil {
		t.Fatal("parser fault was not sticky")
	}
}

func TestLaunchGateBriefOwnerAttachAndBusyRemoteFence(t *testing.T) {
	sk := assemble(t)
	m := launchFakeSized(t, sk, "idle 600s\n", 150, 44)
	t.Run("brief attach", func(t *testing.T) {
		r := newGateRig(t, sk.core, m, launchTrustSnapshot(t))
		r.poll(t)
		sk.srv.SetInputGateFunc(func(local string) error { r.d.abandonLaunchGate(local); return nil })
		att, err := dialClient(t, sk).Attach(protocol.NamespacedID(sk.api.endpointID, m.ID))
		if err != nil {
			t.Fatal(err)
		}
		_ = att.Detach()
		r.settle()
		r.poll(t)
		if r.phase() != launchGateAbandoned || len(r.up.attempts) != 0 {
			t.Fatal("brief owner takeover was missed")
		}
	})
	var ownerServer, remoteServer *protocol.Server
	t.Run("busy remote releases owner reservation", func(t *testing.T) {
		r := newGateRig(t, sk.core, m, launchTrustSnapshot(t))
		r.poll(t)
		r.settle()
		r.d.srv = protocol.NewServer(r.d.api, "owner")
		t.Cleanup(func() { _ = r.d.srv.Close() })
		r.d.remoteSrv = protocol.NewServer(r.d.api, "remote")
		ownerServer, remoteServer = r.d.srv, r.d.remoteSrv
		t.Cleanup(func() { _ = r.d.remoteSrv.Close() })
		entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		joined := make(chan struct{})
		releaseFence := sync.OnceFunc(func() { close(release) })
		t.Cleanup(func() { releaseFence(); <-joined })
		go func() {
			defer close(joined)
			done <- r.d.remoteSrv.WithInputFence(r.id, func() error { close(entered); <-release; return nil })
		}()
		select {
		case <-entered:
		case err := <-done:
			t.Fatalf("remote input fence returned before entry: %v", err)
		case <-time.After(time.Second):
			t.Fatal("remote input fence did not enter within 1s")
		}
		r.poll(t)
		if len(r.up.attempts) != 0 || r.phase() != launchGateAbandoned {
			t.Fatal("busy remote fence allowed gate")
		}
		called := false
		if err := r.d.srv.TryWithInputFence(r.id, func() error { called = true; return nil }); err != nil || !called {
			t.Fatal("owner fence leaked")
		}
		releaseFence()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
	for name, server := range map[string]*protocol.Server{"owner": ownerServer, "remote": remoteServer} {
		if server == nil {
			t.Errorf("%s server was not created", name)
			continue
		}
		called := false
		err := server.TryWithInputFence("closed-server-proof", func() error { called = true; return nil })
		if err == nil || called {
			t.Errorf("%s server remained usable after subtest cleanup: err=%v callback=%v", name, err, called)
		}
	}
}

func TestLaunchGateInvalidationDuringEnterObserverPreventsWireAndRetry(t *testing.T) {
	sk := assemble(t)
	m := launchFakeSized(t, sk, "idle 600s\n", 150, 44)
	for _, kind := range []string{"parser", "foreign", "sample"} {
		t.Run(kind, func(t *testing.T) {
			r := newGateRig(t, sk.core, m, launchTrustSnapshot(t))
			r.poll(t)
			r.settle()
			r.poll(t)
			r.paint(t, trustSelected(r.snap(t)))
			r.poll(t)
			r.settle()
			r.d.api.tap.beforeWrite = func(string) error {
				switch kind {
				case "parser":
					r.d.api.tap.gateFault(r.id, r.identity)
				case "foreign":
					r.d.api.tap.foreignWrite(r.id, r.identity)
				default:
					r.d.abandonLaunchGate(r.id)
				}
				return nil
			}
			r.poll(t)
			r.poll(t)
			if len(r.up.attempts) != 1 || r.phase() != launchGateAbandoned {
				t.Fatalf("invalidated Enter escaped or retried: %q phase=%v", r.up.attempts, r.phase())
			}
		})
	}
}

func TestLaunchGateUnsupportedTransportNeverFallsBack(t *testing.T) {
	sk := assemble(t)
	m := launchFakeSized(t, sk, "idle 600s\n", 150, 44)
	r := newGateRig(t, sk.core, m, launchTrustSnapshot(t))
	r.poll(t)
	r.settle()
	r.up.unsupported = true
	r.poll(t)
	r.poll(t)
	if len(r.up.attempts) != 0 || len(r.up.inputs) != 0 || r.phase() != launchGateAbandoned {
		t.Fatal("unsupported gate transport used fallback")
	}
}

func TestLaunchGateOldSampleCannotReplaceOrRetireSuccessor(t *testing.T) {
	sk := assemble(t)
	m := launchFakeSized(t, sk, "idle 600s\n", 150, 44)
	for _, outcome := range []string{"frame", "error", "decode"} {
		t.Run(outcome, func(t *testing.T) {
			r := newGateRig(t, sk.core, m, launchTrustSnapshot(t))
			r.d.eng = sk.eng
			r.poll(t)
			old := r.d.gateDone[r.id]
			raw, _ := json.Marshal(r.snap(t))
			replacement := r.identity
			replacement.instance = "next"
			next := &launchGateEntry{identity: replacement, phase: launchGateConsumed}
			r.d.sampleGridFrom(r.id, func() ([]byte, error) {
				r.d.capStore.mu.Lock()
				r.d.capStore.instances[r.id] = replacement.instance
				r.d.capStore.mu.Unlock()
				r.d.gateMu.Lock()
				r.d.gateDone[r.id] = next
				r.d.gateMu.Unlock()
				if outcome == "error" {
					return nil, errors.New("old read failed")
				}
				if outcome == "decode" {
					return []byte("old corrupt snapshot"), nil
				}
				return raw, nil
			})
			r.d.observeLaunchGate(r.id, old.identity, claude.New(), launchTrustSnapshot(t), time.Now())
			r.d.abandonLaunchGateBinding(r.id, launchGateIdentity{})
			if r.d.gateDone[r.id] != next || next.phase != launchGateConsumed || len(r.up.attempts) != 0 {
				t.Fatal("old observation/error retired successor or reopened its budget")
			}
		})
	}
	t.Run("late first observation after process end", func(t *testing.T) {
		r := newGateRig(t, sk.core, m, launchTrustSnapshot(t))
		if err := sk.core.Kill(m.ID); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			meta, _ := sk.core.Get(m.ID)
			if meta.Status.Process != status.ProcessRunning {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		meta, _ := sk.core.Get(m.ID)
		if meta.Status.Process == status.ProcessRunning {
			t.Fatal("fake process did not stop")
		}
		r.d.observeLaunchGate(r.id, r.identity, claude.New(), launchTrustSnapshot(t), time.Now())
		if r.d.gateDone[r.id] != nil {
			t.Fatal("late first observation published after process end")
		}
	})
}

func TestLaunchGateBusyFIFOAddsNoQueuedSampler(t *testing.T) {
	sk := assemble(t)
	m := launchFakeSized(t, sk, "idle 600s\n", 150, 44)
	r := newGateRig(t, sk.core, m, launchTrustSnapshot(t))
	r.poll(t)
	r.settle()
	snap := r.snap(t)
	lane := r.d.composerLaneFor(r.id)
	lane.nextTicket++
	r.d.sampling = map[string]struct{}{}
	r.d.sampleFn = func(id string) { r.d.answerLaunchGate(id, snap, r.identity) }
	r.d.sampleGridAsync(context.Background(), r.id)
	done := make(chan struct{})
	go func() { r.d.sampleWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("gate queued behind an existing FIFO ticket")
	}
	close(r.d.closing)
	if lane.nextTicket != 1 || lane.servingTicket != 0 || len(r.up.attempts) != 0 {
		t.Fatal("gate changed busy FIFO or emitted input")
	}
}

func TestLaunchGateBoundedWriteReleasesStopBarrier(t *testing.T) {
	sk := assemble(t)
	m := launchFakeSized(t, sk, "idle 600s\n", 150, 44)
	r := newGateRig(t, sk.core, m, launchTrustSnapshot(t))
	r.poll(t)
	r.settle()
	snap := r.snap(t)
	writing := make(chan struct{})
	r.up.before = func() { close(writing); time.Sleep(100 * time.Millisecond) }
	gateDone := make(chan struct{})
	go func() { r.d.answerLaunchGate(r.id, snap, r.identity); close(gateDone) }()
	<-writing
	stopped := make(chan struct{})
	lane := r.d.composerLaneFor(r.id)
	go func() { lane.mu.Lock(); lane.barrier++; lane.mu.Unlock(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("bounded gate write held Stop barrier")
	}
	<-gateDone
}

func TestLaunchGateCapturedIdentityCannotTakeDifferentEntry(t *testing.T) {
	sk := assemble(t)
	m := launchFakeSized(t, sk, "idle 600s\n", 150, 44)
	r := newGateRig(t, sk.core, m, launchTrustSnapshot(t))
	nextIdentity := r.identity
	nextIdentity.instance = "B"
	next := &launchGateEntry{identity: nextIdentity, phase: launchGateConsumed}
	r.d.gateDone = map[string]*launchGateEntry{r.id: next}
	if r.d.launchGateEntryForIdentity(r.id, r.identity) != nil || r.d.launchGateEntryForIdentity(r.id, launchGateIdentity{}) != nil {
		t.Fatal("old/unknown identity captured successor entry")
	}
	r.d.sampleGridFrom(r.id, func() ([]byte, error) { return nil, errors.New("old A read failed") })
	if r.d.gateDone[r.id] != next || next.phase != launchGateConsumed {
		t.Fatal("A read failure retired mismatched B entry")
	}
}

func TestLaunchGateMissingBoundedInterfaceHasNoLegacyFallback(t *testing.T) {
	up := &ownerFenceUpstream{fakeUpstream: newFakeUpstream(mustSnap(t, "legacy"))}
	sub := &tapSub{mode: readWrite, t: &tap{mgr: newTapManager(nil), up: up}}
	if err := sub.tryLaunchGateControl([]byte("\r"), func() bool { return true }); err == nil {
		t.Fatal("missing bounded interface accepted")
	}
	if len(up.order) != 0 || len(up.inputs) != 0 {
		t.Fatalf("ordinary control/input fallback called: %v %q", up.order, up.inputs)
	}
}
