package protocol

import (
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

type workerFileTestDaemon struct {
	*stubDaemon
	paths chan []string
}

func (d *workerFileTestDaemon) WorkerMail(_, _ string, r schema.WorkerMailReq, _ bool) (schema.WorkerMailReceipt, error) {
	d.paths <- r.Paths
	return schema.WorkerMailReceipt{ID: r.ID, State: "queued",
		Files: []schema.PublicationFile{{ID: "file", Name: "output.csv", ContentType: "text/csv"}}}, nil
}

func TestWorkerFileMailboxRequiresExplicitLocalCapability(t *testing.T) {
	d := &workerFileTestDaemon{stubDaemon: newStubDaemon(), paths: make(chan []string, 2)}
	socket := serveContextGuardAPI(t, d)
	req := schema.WorkerMailReq{
		Action: "message", ID: "message", Kind: "complete", Proof: strings.Repeat("p", 64),
		Paths: []string{"/fixture/work/output.csv"},
	}

	old := dialClient(t, socket, []string{CapWorkerMail})
	if _, err := old.WorkerMail("fixture", req); err == nil {
		t.Fatalf("old daemon capability should refuse files before queuing text: %v", err)
	}

	raw := rawDial(t, socket)
	hello := raw.hello(Version, []string{CapWorkerMail})
	raw.writeControl(Control{Op: OpWorkerMail, EndpointID: hello.EndpointID, SessionID: NamespacedID(hello.EndpointID, "fixture"), WorkerMail: &req})
	if r := raw.readControl(); r.Op != OpError || r.ErrorCode != CodeCapabilityRefused {
		t.Fatalf("raw file request bypassed negotiation: %+v", r)
	}

	current := dialClient(t, socket, []string{CapWorkerMail, CapWorkerMailFiles})
	got, err := current.WorkerMail("fixture", req)
	if err != nil || got.ID != req.ID || len(got.Files) != 1 {
		t.Fatalf("negotiated worker file metadata: %+v %v", got, err)
	}
	if paths := <-d.paths; len(paths) != 1 || paths[0] != req.Paths[0] || len(d.paths) != 0 {
		t.Fatalf("expected one exact authenticated file request: %v", paths)
	}
}
