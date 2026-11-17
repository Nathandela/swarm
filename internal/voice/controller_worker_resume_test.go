package voice

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

func workerResumeFixture() (*controllerWorkerFixture, protocol.SessionView, protocol.SessionView) {
	source := protocol.SessionView{ID: "machine/source", Agent: "codex", Name: "original", Cwd: "/tmp",
		Capabilities: &schema.SessionCapabilities{SessionInstance: "old-instance"}, Status: status.Status{Process: status.ProcessExited}}
	newWorker := protocol.SessionView{ID: "machine/created", Agent: source.Agent, Cwd: source.Cwd, Name: source.Name,
		Capabilities: &schema.SessionCapabilities{SessionInstance: "resumed-instance"}, Status: status.Status{Process: status.ProcessRunning}}
	return &controllerWorkerFixture{rows: []protocol.SessionView{source, newWorker}}, source, newWorker
}

func resumeWorkerCall(t *testing.T, c *Controller, actionID, session, instance string) (json.RawMessage, error) {
	t.Helper()
	raw, err := json.Marshal(ResumeInput{SessionInput: SessionInput{Session: session, SessionInstance: instance}, ActionID: actionID})
	if err != nil {
		t.Fatal(err)
	}
	return c.ManagerCall(context.Background(), ManagerToolCall{Name: "swarm_resume", Arguments: raw})
}

func TestControllerResumeWorkerUsesExactSourceAndReplaysAfterSourceIsGone(t *testing.T) {
	c, s, _, b := provenControllerFixture(t)
	worker, source, resumed := workerResumeFixture()
	c.workers = worker
	worker.beforeLaunch = func(op string, req protocol.LaunchReq) {
		snap, err := s.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		ref, err := c.actionReference(snap, "resume", 1, true)
		if err != nil {
			t.Fatal(err)
		}
		action := snap.Actions[key(b.ConversationID, ref)]
		if action.State != "executing" || action.OperationID != op || action.Target != key(source.ID, "old-instance") ||
			req.Options[protocol.OptionResumeFrom] != source.ID || req.ResumeSourceInstance != "old-instance" ||
			req.Manager != nil || req.InitialPrompt != "" || req.Worktree {
			t.Fatal("resume lacked exact durable source, or silently changed the request")
		}
	}
	if _, err := resumeWorkerCall(t, c, "wrong", source.ID, "new-incarnation"); !errors.Is(err, ErrStale) {
		t.Fatalf("cross-instance request: %v", err)
	}
	worker.rows[0].Status.Process = status.ProcessRunning
	if _, err := resumeWorkerCall(t, c, "busy", source.ID, "old-instance"); !errors.Is(err, ErrStale) {
		t.Fatalf("active source was resumable: %v", err)
	}
	worker.rows[0].Status.Process = status.ProcessExited
	first, err := resumeWorkerCall(t, c, "resume", source.ID, "old-instance")
	if err != nil {
		t.Fatal(err)
	}
	var receipt createdWorkerResult
	if json.Unmarshal(first, &receipt) != nil || receipt.State != "resumed" || receipt.Session != resumed.ID || receipt.SessionInstance != "resumed-instance" {
		t.Fatalf("resume receipt %s", first)
	}
	worker.rows = []protocol.SessionView{resumed}
	repeat, err := resumeWorkerCall(t, c, "resume", source.ID, "old-instance")
	if err != nil || string(repeat) != string(first) || worker.launches != 1 {
		t.Fatalf("exact retry after source retired: %s, %v, launches=%d", repeat, err, worker.launches)
	}
	if _, err = resumeWorkerCall(t, c, "resume", source.ID, "new-incarnation"); !errors.Is(err, ErrConflict) {
		t.Fatalf("same action could name a different instance: %v", err)
	}
}

func TestControllerResumeWorkerReconcilesUnknownReceiptAfterRestart(t *testing.T) {
	c, s, r, b := provenControllerFixture(t)
	worker, source, resumed := workerResumeFixture()
	worker.launchErr = errors.New("lost owner reply")
	worker.outcome = schema.OperationOutcomeView{State: schema.OutcomeUnknown}
	c.workers = worker
	if _, err := resumeWorkerCall(t, c, "resume", source.ID, "old-instance"); !errors.Is(err, ErrControllerUncertain) {
		t.Fatalf("unrelated roster row was mistaken for an exact launch receipt: %v", err)
	}
	dir := filepath.Dir(s.db.Path())
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	current, err := reopened.BindManager(b)
	if err != nil {
		t.Fatal(err)
	}
	c, err = NewController(reopened, r, current, c.refs)
	if err != nil {
		t.Fatal(err)
	}
	c.workers = worker
	if _, err = resumeWorkerCall(t, c, "resume", source.ID, "old-instance"); !errors.Is(err, ErrControllerUncertain) || worker.launches != 1 {
		t.Fatalf("restart replayed an unknown effect: %v, launches=%d", err, worker.launches)
	}
	for _, invalid := range []string{"created", "other-machine/created"} {
		worker.outcome = schema.OperationOutcomeView{State: schema.OutcomeApplied, SessionID: invalid}
		if _, err = resumeWorkerCall(t, c, "resume", source.ID, "old-instance"); !errors.Is(err, ErrControllerUncertain) || worker.launches != 1 {
			t.Fatalf("unscoped receipt %q resolved or replayed the effect: %v", invalid, err)
		}
	}
	worker.outcome = schema.OperationOutcomeView{State: schema.OutcomeApplied, SessionID: "machine/created"}
	got, err := resumeWorkerCall(t, c, "resume", source.ID, "old-instance")
	var receipt createdWorkerResult
	if err != nil || json.Unmarshal(got, &receipt) != nil || receipt.Session != resumed.ID || receipt.State != "resumed" || worker.launches != 1 {
		t.Fatalf("exact local owner receipt did not reconcile after restart: %s %v", got, err)
	}
}

func TestControllerInterruptWithoutExactOwnerReceiptFailsClosed(t *testing.T) {
	c, s, _, _ := provenControllerFixture(t)
	worker, source, _ := workerResumeFixture()
	worker.rows[0].Capabilities.Interrupt = true
	c.workers = worker
	raw, _ := json.Marshal(InterruptInput{SessionInput: SessionInput{Session: source.ID, SessionInstance: "old-instance"}, ActionID: "stop", NativeTurnID: "turn"})
	if _, err := c.ManagerCall(context.Background(), ManagerToolCall{Name: "swarm_interrupt", Arguments: raw}); !errors.Is(err, ErrControllerUnsupported) {
		t.Fatalf("interrupt pretended the remote capability was an owner contract: %v", err)
	}
	snap, err := s.Snapshot()
	if err != nil || len(snap.Actions) != 0 || worker.launches != 0 {
		t.Fatalf("unsupported interrupt created effect authority: %+v %v", snap.Actions, err)
	}
}

func TestControllerResumeWorkerPreservesKnownStaleRefusal(t *testing.T) {
	c, s, _, b := provenControllerFixture(t)
	worker, source, _ := workerResumeFixture()
	c.workers = worker
	worker.launchErr = protocol.ErrOwnerResumeStaleSource
	if _, err := resumeWorkerCall(t, c, "resume", source.ID, "old-instance"); !errors.Is(err, ErrStale) {
		t.Fatalf("authoritatively refused source became uncertain: %v", err)
	}
	raw, err := resumeWorkerCall(t, c, "resume", source.ID, "old-instance")
	var receipt struct {
		State string `json:"state"`
	}
	if err != nil || json.Unmarshal(raw, &receipt) != nil || receipt.State != "rejected" || worker.launches != 1 {
		t.Fatalf("known pre-effect refusal did not replay durably: %s %v", raw, err)
	}
	snap, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	a := snap.Actions[key(b.ConversationID, c.businessReference("action", 1, "", "resume"))]
	if a.State != "settled" || len(snap.Workers) != 0 {
		t.Fatal("known rejection stranded a worker reservation")
	}
}
