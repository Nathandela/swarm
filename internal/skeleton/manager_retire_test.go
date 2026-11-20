package skeleton

import (
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/managerproof"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

func TestManagerRetireRefusesChangedNativeTurnControllerLeaseAndManualDraft(t *testing.T) {
	d, binding, _ := newCodexManagerSendRig(t)
	current, err := d.managerRuntime(binding.Session, binding.SessionInstance)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := d.core.Get(binding.Session)
	target := persist.ManagerRetirement{OperationID: "retire", Instance: binding.SessionInstance, NativeSessionID: binding.NativeSessionID,
		NativeGeneration: binding.NativeGeneration, Generation: 1, Checkpoint: strings.Repeat("a", 64), ShimPID: m.ShimPID, ShimStartTime: m.ShimStartTime}
	req := protocol.ManagerRetireReq{Binding: binding, Generation: target.Generation, Checkpoint: target.Checkpoint, ExpectedTurn: current.Turn}
	stale := req
	stale.ExpectedTurn.ExternalInput++
	if claimed, err := d.retireManager(t.Context(), binding.Session, stale, target); claimed || !errors.Is(err, managerproof.ErrStale) {
		t.Fatalf("outdated observation reached the retirement effect: %v %v", claimed, err)
	}
	api := newCoreAPIUnstarted(d.core, "", "machine")
	d.srv = protocol.NewServer(api, "machine")
	defer api.close()
	defer func() { _ = d.srv.Close() }()
	socket := filepath.Join(d.stateDir, "owner-test.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		if conn, err := listener.Accept(); err == nil {
			d.srv.ServeConn(conn)
		}
	}()
	owner, err := protocol.Dial(socket, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close() }()
	attach, err := owner.Attach(protocol.NamespacedID(owner.EndpointID(), binding.Session))
	if err != nil {
		t.Fatal(err)
	}
	if claimed, err := d.retireManager(t.Context(), binding.Session, req, target); claimed || !errors.Is(err, managerproof.ErrUnavailable) {
		t.Fatalf("controlled terminal reached the retirement effect: %v %v", claimed, err)
	}
	if err := attach.Detach(); err != nil {
		t.Fatal(err)
	}
	if err := d.markDirectInputUnresolved(binding.Session, protocol.DirectInputDraft); err != nil {
		t.Fatal(err)
	}
	if claimed, err := d.retireManager(t.Context(), binding.Session, req, target); claimed || !errors.Is(err, managerproof.ErrUnavailable) {
		t.Fatalf("manual draft reached the retirement effect: %v %v", claimed, err)
	}
	final, _ := d.core.Get(binding.Session)
	if final.ManagerRetirement.OperationID != "" {
		t.Fatal("refused requests acquired a retirement checkpoint")
	}
}

func TestManagerRetireRedriveAllowsFreshObservationOnlyForTheSameIdleProcess(t *testing.T) {
	binding := codexProofFixture()
	last := protocol.ManagerNativeTurn{State: "completed", NativeTurnID: "last-turn", ExternalInput: 3}
	req := protocol.ManagerRetireReq{Binding: binding, ExpectedTurn: last}
	observed := protocol.ManagerRuntimeStatus{Binding: binding, ReadyForInput: true, Turn: last}
	observed.Turn.ExternalInput++
	if retirementReady(req, observed, true) {
		t.Fatal("same observer accepted changed turn")
	}
	observed.Binding.ObservationGeneration = "fresh-observer"
	observed.Turn = protocol.ManagerNativeTurn{State: "idle"}
	if retirementReady(req, observed, false) || !retirementReady(req, observed, true) {
		t.Fatal("fresh idle observation is valid only for a retained retirement claim")
	}
	observed.Turn.State = "running"
	if retirementReady(req, observed, true) {
		t.Fatal("a running turn was accepted during retirement recovery")
	}
	observed.Turn.State = "idle"
	observed.Binding.NativeGeneration = "other-native-process"
	if retirementReady(req, observed, true) {
		t.Fatal("a new physical process inherited the retirement claim")
	}
}

func TestManagerRetireReleasesTheOldComposerLaneOnExit(t *testing.T) {
	d := &Daemon{}
	const local = "manager"
	if err := d.withSessionStopFence(local, false, managerRetireReason, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	d.retireComposerLane(local)
	if _, present := d.composerLanes.Load(local); present {
		t.Fatal("a parked manager retained its process-only composer lane")
	}
}

func TestDiscordDraftSurvivesReconnectAndFencesNativeAdmission(t *testing.T) {
	d, b, _ := newCodexManagerSendRigForConversation(t, "discord-teammate-owner-dm")
	d.restoreDirectInputState([]string{b.Session})
	if err := d.markDirectInputUnresolved(b.Session, protocol.DirectInputDraft); err != nil {
		t.Fatal(err)
	}
	d.restoreDirectInputState([]string{b.Session})
	delete(d.backend.live, b.Session)
	if _, err := d.managerRuntime(b.Session, b.SessionInstance); !errors.Is(err, managerproof.ErrDirectInputUnresolved) {
		t.Fatalf("native proof masked the draft: %v", err)
	}
	if _, err := d.managerRuntime(b.Session, "replacement"); errors.Is(err, managerproof.ErrDirectInputUnresolved) {
		t.Fatal("draft attributed to the wrong incarnation")
	}
	req := protocol.ComposerSendReq{Session: "machine/" + b.Session, SessionInstance: b.SessionInstance, Text: "first", ManagerAdmission: &protocol.ManagerAdmission{Binding: b, Nonce: strings.Repeat("a", 64)}}
	code, err := d.semanticSendTransactional("machine", "first", req, func() error { t.Fatal("native admission began over a direct-input draft"); return nil })
	if code != protocol.CodeInputBusy || !errors.Is(err, managerproof.ErrDirectInputUnresolved) || !d.directInputUnresolved(b.Session) {
		t.Fatalf("draft fence: %s %v", code, err)
	}
}
