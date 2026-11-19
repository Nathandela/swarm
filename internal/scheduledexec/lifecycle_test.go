package scheduledexec

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/schedule"
)

type lifecycleBackend struct {
	launches  []Launch
	sessions  map[string]Session
	receipts  map[string]string
	stops     []string
	projected []string
	launchErr error
}

func (b *lifecycleBackend) Launch(r Launch) (string, error) {
	b.launches = append(b.launches, r)
	if b.launchErr != nil {
		return "", b.launchErr
	}
	b.sessions[r.AttemptID] = Session{Running: true}
	b.receipts[r.OperationID] = r.AttemptID
	return r.AttemptID, nil
}

func (b *lifecycleBackend) Reconcile(operation string) (string, bool) {
	session, found := b.receipts[operation]
	return session, found
}

func (b *lifecycleBackend) Session(session, attempt string) (Session, bool) {
	current, found := b.sessions[session]
	return current, found && session == attempt
}

func (b *lifecycleBackend) Stop(session, attempt string) error {
	b.stops = append(b.stops, session)
	return nil
}

func (b *lifecycleBackend) ProjectCompletion(session, attempt string) error {
	b.projected = append(b.projected, session)
	return nil
}

func (b *lifecycleBackend) HandoffEvidence(session, attempt string) string {
	if b.sessions[session].Stopped {
		return " Verified prior observation: external review finished without tracked file changes."
	}
	return ""
}

func lifecycleFixture(t *testing.T) (*Engine, *schedule.Store, *lifecycleBackend, schedule.Job, time.Time) {
	t.Helper()
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := schedule.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateJob(schedule.JobSpec{
		Name: "Review", Prompt: "Review the work", Directory: t.TempDir(),
		Authority: schedule.AuthorityAgentDefault, Workspace: schedule.WorkspaceDirectory,
		Agents:   []string{schedule.RouteCodex, schedule.RouteClaude},
		Calendar: schedule.Calendar{Kind: "daily", TimeZone: "UTC", Time: "08:00"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	b := &lifecycleBackend{sessions: make(map[string]Session), receipts: make(map[string]string)}
	return New(store, dir, b), store, b, job, now
}

func TestVerifiedStopFencesFallbackAndReceipts(t *testing.T) {
	e, store, b, job, now := lifecycleFixture(t)
	id, err := e.Run(job.ID, schedule.NewID(now), now)
	if err != nil || len(b.launches) != 1 {
		t.Fatalf("start: %q %v with %d launches", id, err, len(b.launches))
	}
	first := b.launches[0]
	b.sessions[first.AttemptID] = Session{Running: true, Exhausted: true}
	if err := e.Tick(now.Add(time.Second)); err != nil || len(b.launches) != 1 || len(b.stops) != 1 {
		t.Fatalf("exhaustion before verified stop: %v, %d launches, %d stops", err, len(b.launches), len(b.stops))
	}
	report := schedule.Completion{Summary: "Finished with verified evidence", Links: []string{"notes.md"}}
	if err := e.Complete(id, first.AttemptID, first.AttemptID, first.Token, report, now); err == nil {
		t.Fatal("stopping first worker completed the occurrence")
	}
	b.sessions[first.AttemptID] = Session{Exhausted: true} // no shim receipt
	if err := e.Tick(now.Add(2 * time.Second)); err != nil || len(b.launches) != 1 {
		t.Fatalf("fallback launched without verified stop: %v, %d", err, len(b.launches))
	}
	b.sessions[first.AttemptID] = Session{Exhausted: true, Stopped: true}
	if err := e.Tick(now.Add(3 * time.Second)); err != nil || len(b.launches) != 2 {
		t.Fatalf("fallback after verified stop: %v, %d launches", err, len(b.launches))
	}
	second := b.launches[1]
	if second.Workspace != first.Workspace || second.Agent != schedule.RouteClaude ||
		!strings.Contains(second.Prompt, "external review finished") {
		t.Fatalf("fallback lost its workspace, agent, or prior evidence: %+v", second)
	}
	if err := e.Complete(id, second.AttemptID, first.AttemptID, second.Token, report, now); err == nil {
		t.Fatal("different native session completed the active attempt")
	}
	if err := e.Complete(id, second.AttemptID, second.AttemptID, first.Token, report, now); err == nil {
		t.Fatal("previous attempt token completed the active attempt")
	}
	for range 2 {
		if err := e.Complete(id, second.AttemptID, second.AttemptID, second.Token, report, now); err != nil {
			t.Fatal(err)
		}
	}
	run, err := store.Occurrence(id)
	if err != nil || run.State != schedule.OccurrenceReady || len(run.Attempts) != 2 ||
		run.Attempts[0].State != schedule.AttemptStopped || run.Attempts[1].State != schedule.AttemptComplete ||
		len(b.projected) != 1 || b.projected[0] != second.AttemptID {
		t.Fatalf("unexpected completed run: %+v (%v), projections %v", run, err, b.projected)
	}
}

func TestUnknownLaunchDoesNotFallbackAndLateStopUnblocks(t *testing.T) {
	e, store, b, job, now := lifecycleFixture(t)
	b.launchErr = errors.New("simulated lost launch receipt")
	id, err := e.Run(job.ID, schedule.NewID(now), now)
	if err != nil || len(b.launches) != 1 {
		t.Fatalf("start: %q %v with %d launches", id, err, len(b.launches))
	}
	first := b.launches[0]
	if err := e.Tick(now.Add(time.Minute)); err != nil || len(b.launches) != 1 {
		t.Fatalf("unconfirmed outcome redrove launch: %v, %d", err, len(b.launches))
	}
	run, err := store.Occurrence(id)
	if err != nil || run.State != schedule.OccurrenceAttention ||
		!strings.Contains(run.Detail, "simulated lost launch receipt") {
		t.Fatalf("unconfirmed outcome was not reported: %+v %v", run, err)
	}
	b.sessions[first.AttemptID] = Session{Stopped: true}
	b.receipts[first.OperationID] = first.AttemptID
	if err := e.Tick(now.Add(2 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	run, err = store.Occurrence(id)
	if err != nil || run.State != schedule.OccurrenceIncomplete || run.Completion != nil || len(b.launches) != 1 {
		t.Fatalf("late verified stop changed original outcome to success or redrove: %+v %v", run, err)
	}
}

func TestReadyWorkerRetainsDirectoryUntilVerifiedStop(t *testing.T) {
	e, store, b, job, now := lifecycleFixture(t)
	id, err := e.Run(job.ID, schedule.NewID(now), now)
	if err != nil {
		t.Fatal(err)
	}
	first := b.launches[0]
	report := schedule.Completion{Summary: "Reviewed"}
	if err := e.Complete(id, first.AttemptID, first.AttemptID, first.Token, report, now); err != nil {
		t.Fatal(err)
	}
	overlap, err := e.Run(job.ID, schedule.NewID(now), now)
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := store.Occurrence(overlap)
	if err != nil || blocked.State != schedule.OccurrenceBlocked || len(b.launches) != 1 {
		t.Fatalf("workspace released at completion before stop: %+v %v", blocked, err)
	}
	b.sessions[first.AttemptID] = Session{Stopped: true}
	if err := e.Tick(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(job.ID, schedule.NewID(now), now.Add(time.Second)); err != nil || len(b.launches) != 2 {
		t.Fatalf("verified stop did not release directory: %v, %d launches", err, len(b.launches))
	}
}

type verifyingBackend struct {
	*lifecycleBackend
	showClosingMessage bool
}

func (b *verifyingBackend) ClosingMessage(_, _ string, summary string) bool {
	return b.showClosingMessage && summary == "Reviewed"
}

func TestReceiptWaitsForAnAssistantMessage(t *testing.T) {
	e, store, mock, job, now := lifecycleFixture(t)
	b := &verifyingBackend{lifecycleBackend: mock}
	e.backend = b
	id, err := e.Run(job.ID, schedule.NewID(now), now)
	if err != nil {
		t.Fatal(err)
	}
	a := b.launches[0]
	if err := e.Complete(id, a.AttemptID, a.AttemptID, a.Token, schedule.Completion{Summary: "Reviewed"}, now); err != nil {
		t.Fatal(err)
	}
	pending, err := store.Occurrence(id)
	if err != nil || pending.State != schedule.OccurrenceWorking || pending.Completion == nil || len(b.projected) != 0 {
		t.Fatalf("completion was shown before the assistant message: %+v, %v", pending, err)
	}
	b.showClosingMessage = true
	if err := e.Tick(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	ready, err := store.Occurrence(id)
	if err != nil || ready.State != schedule.OccurrenceReady || ready.CompletedAt.IsZero() || len(b.projected) != 1 {
		t.Fatalf("verified completion did not project: %+v, %v", ready, err)
	}
}
