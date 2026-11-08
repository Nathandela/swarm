package protocol

import (
	"slices"
	"sync/atomic"
	"testing"
	"time"

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
			caps = []string{CapRemoteGateway, "owner-launch"}
		}
		raw := rawDial(t, sock)
		hello := raw.hello(Version, caps)
		if slices.Contains(hello.Capabilities, "owner-launch") {
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
	defer c.Close()
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
	defer c.Close()
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
