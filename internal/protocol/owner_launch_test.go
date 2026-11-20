package protocol

import (
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

type ownerLaunchStub struct {
	*durableComposerStub
	reads atomic.Int32
}

func (s *ownerLaunchStub) OwnerLaunchReplay(string, string) (persist.Meta, bool, error) {
	return persist.Meta{}, false, nil
}
func (s *ownerLaunchStub) OwnerLaunchOutcome(string) (schema.OperationOutcomeView, bool) {
	s.reads.Add(1)
	return schema.OperationOutcomeView{State: schema.OutcomeUnknown}, true
}

func TestOwnerLaunchRefusesUnnegotiatedAndRemoteBeforeCustody(t *testing.T) {
	for _, remote := range []bool{false, true} {
		b := &ownerLaunchStub{durableComposerStub: newDurableComposerStub()}
		sock := serveOwnerAPI(t, b)
		caps := []string{}
		if remote {
			sock = serveRemoteAPI(t, b)
			caps = []string{CapRemoteGateway, CapOwnerLaunch, CapTeammateWorkerBridge}
		}
		raw := rawDial(t, sock)
		hello := raw.hello(Version, caps)
		if slices.Contains(hello.Capabilities, CapOwnerLaunch) || slices.Contains(hello.Capabilities, CapTeammateWorkerBridge) {
			t.Fatal("owner capability offered to remote")
		}
		raw.writeControl(Control{Op: "owner_launch_status", EndpointID: hello.EndpointID, SubjectOperationID: "launch:old"})
		if got := raw.readControl(); got.Op != OpError || b.reads.Load() != 0 {
			t.Fatalf("unauthorized status = %+v", got)
		}
		raw.writeControl(Control{Op: OpOwnerLaunch, EndpointID: hello.EndpointID, OperationID: "launch:old", Launch: &LaunchReq{Agent: "fake", Cwd: t.TempDir(), Cols: 80, Rows: 24}})
		if got := raw.readControl(); got.Op != OpError {
			t.Fatalf("unauthorized mutation: %+v", got)
		}
	}
}

func TestOwnerLaunchClientRefusesUnnegotiatedMutation(t *testing.T) {
	b := &ownerLaunchStub{durableComposerStub: newDurableComposerStub()}
	c, err := Dial(serveOwnerAPI(t, b), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	id, err := NewLaunchOperationID()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.LaunchOperation(id, LaunchReq{}); err == nil {
		t.Fatal("unnegotiated client launched")
	}
}

func TestOwnerLaunchStatusReadsExpiredIDWithoutLaunch(t *testing.T) {
	b := &ownerLaunchStub{durableComposerStub: newDurableComposerStub()}
	c, err := Dial(serveOwnerAPI(t, b), []string{CapOwnerLaunch})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	id := "launch:1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	out, err := c.LaunchOperationStatus(id)
	if err != nil || out.State != schema.OutcomeUnknown || b.reads.Load() != 1 {
		t.Fatalf("expired read = %+v, %v", out, err)
	}
}

func TestOwnerLaunchHashBindsEveryRequestField(t *testing.T) {
	req := LaunchReq{Agent: "codex", Cwd: "/repo", Env: []string{"A=b"}, Options: map[string]string{"model": "one"}}
	base := ownerLaunchHash(&req)
	for _, mutate := range []func(*LaunchReq){
		func(r *LaunchReq) { r.Env = []string{"A=c"} },
		func(r *LaunchReq) { r.Cols = 80 },
		func(r *LaunchReq) { r.Worktree = true },
		func(r *LaunchReq) { r.Options = map[string]string{OptionResumeFrom: "machine/old"} },
		func(r *LaunchReq) { r.SpawnedFrom = "source" },
		func(r *LaunchReq) { r.Supervision = "manual" },
		func(r *LaunchReq) { r.InitialPrompt = "new task" },
		func(r *LaunchReq) { r.ResumeSourceInstance = "different-incarnation" },
		func(r *LaunchReq) { r.WorkerBridge = "/tmp/bridge.sock:" + strings.Repeat("ab", 32) },
	} {
		changed := req
		mutate(&changed)
		if ownerLaunchHash(&changed) == base {
			t.Fatal("changed request did not change binding")
		}
	}
	id, err := NewLaunchOperationID()
	if err != nil || !validLaunchOperationID(id, time.Now()) {
		t.Fatalf("operation ID = %q, %v", id, err)
	}
}

type boundResumeOwnerStub struct {
	*ownerLaunchStub
	seen chan daemon.LaunchSpec
}

func (*boundResumeOwnerStub) OwnerBoundResumeSupported() bool { return true }

func (s *boundResumeOwnerStub) Launch(spec daemon.LaunchSpec) (persist.Meta, error) {
	s.seen <- spec
	return persist.Meta{}, daemon.ErrResumeSourceChanged
}

func TestOwnerBoundResumeNegotiatesAndPreservesStaleInstanceRefusal(t *testing.T) {
	b := &boundResumeOwnerStub{ownerLaunchStub: &ownerLaunchStub{durableComposerStub: newDurableComposerStub()}, seen: make(chan daemon.LaunchSpec, 2)}
	socket := serveOwnerAPI(t, b)
	for _, caps := range [][]string{{CapOwnerLaunch}, {CapOwnerLaunch, CapOwnerBoundResume}} {
		c, err := Dial(socket, caps)
		if err != nil {
			t.Fatal(err)
		}
		id, err := NewLaunchOperationID()
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = c.LaunchOperation(id, LaunchReq{
			Agent: "codex", Cwd: t.TempDir(), Cols: 120, Rows: 32, ResumeSourceInstance: "bound-instance",
			Options: map[string]string{OptionResumeFrom: NamespacedID(c.EndpointID(), "source")},
		})
		_ = c.Close()
		if len(caps) == 1 {
			if err == nil || len(b.seen) != 0 {
				t.Fatal("unnegotiated bound resume reached the backend", err)
			}
		} else {
			if !errors.Is(err, ErrOwnerResumeStaleSource) {
				t.Fatal("stale source error lost across owner wire", err)
			}
			spec := <-b.seen
			if spec.ResumeSource == nil || spec.ResumeSource.Instance != "bound-instance" ||
				spec.OwnerLaunchHash == "" || spec.OperationID != id {
				t.Fatalf("owner dispatch changed exact source authority: %+v", spec)
			}
		}
	}
}

func TestTeammateWorkerBridgeRequiresNegotiatedOwnerDelegate(t *testing.T) {
	b := &boundResumeOwnerStub{ownerLaunchStub: &ownerLaunchStub{durableComposerStub: newDurableComposerStub()}, seen: make(chan daemon.LaunchSpec, 1)}
	sock := serveOwnerAPI(t, b)
	id, err := NewLaunchOperationID()
	if err != nil {
		t.Fatal(err)
	}
	req := LaunchReq{Agent: "codex", Cwd: t.TempDir(), Cols: 80, Rows: 24,
		SpawnedFrom: "manager", WorkerBridge: "/tmp/bridge.sock:" + strings.Repeat("ab", 32)}
	c, err := Dial(sock, []string{CapOwnerLaunch})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = c.LaunchOperation(id, req); err == nil || len(b.seen) != 0 {
		t.Fatalf("worker bridge reached daemon without negotiated support: %v", err)
	}
	_ = c.Close()
	c, err = Dial(sock, []string{CapOwnerLaunch, CapTeammateWorkerBridge})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if _, _, err = c.LaunchOperation(id, req); err == nil || len(b.seen) != 0 {
		t.Fatalf("a non-delegating launch carried a bridge: %v", err)
	}
	req.SpawnIntent = SpawnIntentDelegate
	_, _, _ = c.LaunchOperation(id, req)
	select {
	case spec := <-b.seen:
		if spec.WorkerBridge != req.WorkerBridge {
			t.Fatal("owner dispatch lost the worker bridge capability")
		}
	default:
		t.Fatal("qualified delegated launch did not reach the daemon")
	}
}
