package scheduledexec

import (
	"errors"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/schedule"
)

type runOperationBackend struct {
	Backend
	launches []Launch
	stopped  bool
}

func (b *runOperationBackend) Launch(req Launch) (string, error) {
	b.launches = append(b.launches, req)
	return req.AttemptID, nil
}

func (b *runOperationBackend) Session(sessionID, attemptID string) (Session, bool) {
	return Session{Running: !b.stopped, Stopped: b.stopped}, sessionID == attemptID
}

func (*runOperationBackend) ProjectCompletion(string, string) error { return nil }

func TestRunOperationReplayAfterLostReplyCompletionAndArchive(t *testing.T) {
	now := time.Now()
	stateDir := t.TempDir()
	store, err := schedule.Open(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateJob(schedule.JobSpec{
		Name: "Review", Prompt: "Review the work", Directory: t.TempDir(),
		Authority: schedule.AuthorityObserve, Workspace: schedule.WorkspaceDirectory,
		Agents: []string{"codex"}, Calendar: schedule.Calendar{Kind: "daily", TimeZone: "UTC", Time: "08:00"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	b := &runOperationBackend{}
	e := New(store, stateDir, b)
	firstKey := schedule.NewID(now)
	if _, err := e.Run(job.ID, firstKey, now); err != nil {
		t.Fatal(err)
	}
	if len(b.launches) != 1 {
		t.Fatalf("first request launched %d workers", len(b.launches))
	}
	first := b.launches[0]

	// Recover using a new Engine and Store as after a daemon restart.
	store, err = schedule.Open(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	e = New(store, stateDir, b)
	checkReplay := func(at time.Time) {
		t.Helper()
		id, err := e.Run(job.ID, firstKey, at)
		if err != nil || id != first.RunID || len(b.launches) != 1 {
			t.Fatalf("replay returned %q, %v with %d worker launches", id, err, len(b.launches))
		}
	}
	checkReplay(now.Add(time.Second))

	b.stopped = true
	if err := e.Complete(first.RunID, first.AttemptID, first.AttemptID, first.Token, schedule.Completion{Summary: "Reviewed"}, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	completed, err := store.Occurrence(first.RunID)
	if err != nil || completed.State != schedule.OccurrenceReady || completed.CompletedAt.IsZero() {
		t.Fatalf("completed occurrence = %s at %v, %v", completed.State, completed.CompletedAt, err)
	}
	checkReplay(now.Add(3 * time.Second))
	b.stopped = false
	secondKey := schedule.NewID(now)
	id, err := e.Run(job.ID, secondKey, now.Add(4*time.Second))
	if err != nil || id == first.RunID || len(b.launches) != 2 {
		t.Fatalf("deliberate second run returned %q, %v with %d worker launches", id, err, len(b.launches))
	}
	if _, err := store.SetJobState(job.ID, schedule.JobArchived, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	id, err = e.Run(job.ID, secondKey, now.Add(6*time.Second))
	if err != nil || id != b.launches[1].RunID || len(b.launches) != 2 {
		t.Fatalf("archived active replay returned %q, %v with %d worker launches", id, err, len(b.launches))
	}
	id, err = e.Run(job.ID, firstKey, now.Add(6*time.Second))
	if err != nil || id != first.RunID || len(b.launches) != 2 {
		t.Fatalf("archived replay returned %q, %v with %d worker launches", id, err, len(b.launches))
	}
	if _, err := e.Run(job.ID, schedule.NewID(now), now.Add(7*time.Second)); !errors.Is(err, schedule.ErrArchived) || len(b.launches) != 2 {
		t.Fatalf("new archived run returned %v with %d worker launches", err, len(b.launches))
	}
}
