package schedule

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func makeStore(t testing.TB) (*Store, JobSpec, time.Time) {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store, JobSpec{
		Name: "Scheduled inspection", Prompt: "Read the workspace.\nWrite a report.",
		Authority: AuthorityObserve, Workspace: WorkspaceWorktree,
		Directory: t.TempDir(), Agents: []string{RouteCodex},
		Calendar: Calendar{Kind: CalendarCron, TimeZone: "UTC", Cron: "*/5 * * * *"},
		Grace:    time.Hour, AllowOverlap: true,
	}, time.Date(2026, time.November, 19, 9, 0, 0, 0, time.UTC)
}

func TestClaimReplaysLatestAndManualDoesNotConsumeCalendar(t *testing.T) {
	store, spec, now := makeStore(t)
	job, err := store.CreateJob(spec, now)
	if err != nil {
		t.Fatal(err)
	}
	at := now.Add(16 * time.Minute)
	manual, err := store.ClaimManual(job.ID, at)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimDue(at)
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 1 || !claims[0].DueAt.Equal(now.Add(15*time.Minute)) ||
		manual.ID == claims[0].ID || !manual.Manual || claims[0].Manual {
		t.Fatalf("manual=%+v claims=%+v", manual, claims)
	}
	afterRestart, err := Open(filepath.Dir(store.dir))
	if err != nil {
		t.Fatal(err)
	}
	if again, err := afterRestart.ClaimDue(at); err != nil || len(again) != 0 {
		t.Fatalf("replayed calendar claim = %+v, %v", again, err)
	}
	history, err := afterRestart.History(job.ID)
	if err != nil || len(history) != 2 {
		t.Fatalf("history = %+v, %v", history, err)
	}
}

func TestMissedAndPausedCalendar(t *testing.T) {
	store, spec, now := makeStore(t)
	spec.Grace = 30 * time.Second
	job, err := store.CreateJob(spec, now)
	if err != nil {
		t.Fatal(err)
	}
	if claims, err := store.ClaimDue(now.Add(time.Minute)); err != nil || len(claims) != 0 {
		t.Fatalf("missed claims = %+v, %v", claims, err)
	}
	history, err := store.History(job.ID)
	if err != nil || len(history) != 1 || history[0].State != OccurrenceMissed || history[0].Detail == "" {
		t.Fatalf("missed history = %+v, %v", history, err)
	}
	if _, err := store.SetJobState(job.ID, JobPaused, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if claims, err := store.ClaimDue(now.Add(5 * time.Minute)); err != nil || len(claims) != 0 {
		t.Fatalf("paused claims = %+v, %v", claims, err)
	}
	if _, err := store.SetJobState(job.ID, JobActive, now.Add(5*time.Minute+time.Second)); err != nil {
		t.Fatal(err)
	}
	if claims, err := store.ClaimDue(now.Add(5*time.Minute + 2*time.Second)); err != nil || len(claims) != 0 {
		t.Fatalf("resumed catchup = %+v, %v", claims, err)
	}
}

func TestConcurrentTickCommitsSingleOccurrence(t *testing.T) {
	store, spec, now := makeStore(t)
	if _, err := store.CreateJob(spec, now); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	claimed := make(chan int, 16)
	for i := 0; i < cap(claimed); i++ {
		group.Go(func() {
			other, err := Open(filepath.Dir(store.dir))
			if err != nil {
				t.Error(err)
				return
			}
			claims, err := other.ClaimDue(now)
			if err != nil {
				t.Error(err)
			}
			claimed <- len(claims)
		})
	}
	group.Wait()
	close(claimed)
	total := 0
	for count := range claimed {
		total += count
	}
	if total != 1 {
		t.Fatalf("%d calendar claims committed", total)
	}
}

func TestClaimIsFencedFromEditArchiveAndMutation(t *testing.T) {
	store, spec, now := makeStore(t)
	job, err := store.CreateJob(spec, now)
	if err != nil {
		t.Fatal(err)
	}
	old, err := store.ClaimManual(job.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	spec.Prompt = "Revised prompt"
	revised, err := store.EditJobRevision(job.ID, job.RevisionID, spec, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EditJobRevision(job.ID, job.RevisionID, spec, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale edit error = %v", err)
	}
	if revised.RevisionID == job.RevisionID {
		t.Fatal("edit reused revision")
	}
	if _, err := store.UpdateOccurrence(old.ID, func(o *Occurrence) error {
		o.Spec.Agents[0] = RouteClaude
		return nil
	}); err == nil {
		t.Fatal("occurrence mutation committed")
	}
	if _, err := store.SetJobState(job.ID, JobArchived, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimManual(job.ID, now.Add(10*time.Minute)); !errors.Is(err, ErrArchived) {
		t.Fatalf("archived manual claim error = %v", err)
	}
	if claims, err := store.ClaimDue(now.Add(10 * time.Minute)); err != nil || len(claims) != 0 {
		t.Fatalf("archived claims = %+v, %v", claims, err)
	}
	run, err := store.Occurrence(old.ID)
	if err != nil || run.Spec.Prompt != job.Spec.Prompt || run.Spec.Agents[0] != RouteCodex ||
		run.RequestHash != old.RequestHash || run.RevisionID != job.RevisionID {
		t.Fatalf("original claim = %+v, %v", run, err)
	}
	rev, err := store.Revision(job.RevisionID)
	if err != nil || rev.Spec.Prompt != job.Spec.Prompt {
		t.Fatalf("original revision = %+v, %v", rev, err)
	}
}

func TestOverlapRequiresIsolation(t *testing.T) {
	store, spec, now := makeStore(t)
	spec.Workspace, spec.AllowOverlap = WorkspaceDirectory, false
	job, err := store.CreateJob(spec, now)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.ClaimManual(job.ID, now)
	if err != nil || run.State != OccurrenceQueued {
		t.Fatalf("first manual run = %+v, %v", run, err)
	}
	other, err := store.CreateJob(spec, now)
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := store.ClaimManual(other.ID, now)
	if err != nil || blocked.State != OccurrenceBlocked {
		t.Fatalf("overlap = %+v, %v", blocked, err)
	}
}

func TestTemplateSnapshotsAndInterruptedWrite(t *testing.T) {
	store, spec, now := makeStore(t)
	tmpl, err := store.CreateTemplate("Review", spec.Prompt, now)
	if err != nil {
		t.Fatal(err)
	}
	spec.Prompt = tmpl.Prompt
	job, err := store.CreateJob(spec, now)
	if err != nil {
		t.Fatal(err)
	}
	interrupted := errors.New("interrupted writer")
	if err := store.update(func(tx *bolt.Tx) error {
		if err := tx.Bucket([]byte(jobsBucket)).Delete([]byte(job.ID)); err != nil {
			return err
		}
		return interrupted
	}); !errors.Is(err, interrupted) {
		t.Fatalf("interrupted write = %v", err)
	}
	reopened, err := Open(filepath.Dir(store.dir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.EditTemplate(tmpl.ID, tmpl.Name, "Replacement", now); err != nil {
		t.Fatal(err)
	}
	if err := reopened.DeleteTemplate(tmpl.ID); err != nil {
		t.Fatal(err)
	}
	entries, err := reopened.Templates()
	if err != nil || len(entries) != 0 {
		t.Fatalf("templates = %+v, %v", entries, err)
	}
	saved, err := reopened.Job(job.ID)
	if err != nil || saved.Spec.Prompt != spec.Prompt {
		t.Fatalf("job after template changes = %+v, %v", saved, err)
	}
	for path, want := range map[string]os.FileMode{
		store.dir: 0o700, filepath.Join(store.dir, stateFile): 0o600,
		filepath.Join(store.dir, "state.lock"): 0o600,
	} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("%s mode = %v, %v; want %s", path, info, err, want)
		}
	}
}

func TestULID(t *testing.T) {
	now := time.Date(2026, time.November, 19, 9, 0, 0, 0, time.UTC)
	first, second := NewID(now), NewID(now.Add(time.Millisecond))
	format := regexp.MustCompile(`^[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	if !format.MatchString(first) || !format.MatchString(second) || first >= second {
		t.Fatalf("ULIDs %q, %q invalid or out of timestamp order", first, second)
	}
}

func TestDSTRepeatTickAfterRestartKeepsFirstOccurrence(t *testing.T) {
	store, spec, _ := makeStore(t)
	spec.Calendar = Calendar{Kind: CalendarDaily, TimeZone: "Europe/Zurich", Time: "02:30"}
	created := instant(t, "2026-10-24T10:00:00Z")
	if _, err := store.CreateJob(spec, created); err != nil {
		t.Fatal(err)
	}
	first := instant(t, "2026-10-25T00:30:00Z")
	second := instant(t, "2026-10-25T01:30:00Z")
	runs, err := store.ClaimDue(second)
	if err != nil || len(runs) != 1 || !runs[0].DueAt.Equal(first) {
		t.Fatalf("repeated wall time runs = %+v, %v", runs, err)
	}
	reopened, err := Open(filepath.Dir(store.dir))
	if err != nil {
		t.Fatal(err)
	}
	if runs, err := reopened.ClaimDue(second.Add(time.Second)); err != nil || len(runs) > 0 {
		t.Fatalf("duplicate fall back runs = %+v, %v", runs, err)
	}
}

func TestOneTimeRejectsPastTriggerAndFiresOnce(t *testing.T) {
	store, spec, now := makeStore(t)
	spec.Calendar = Calendar{Kind: CalendarOnce, TimeZone: "UTC", Date: now.Format("2006-01-02"), Time: "08:59"}
	if _, err := store.CreateJob(spec, now); err == nil {
		t.Fatal("created one-time job in the past")
	}
	spec.Calendar.Time = "09:01"
	if _, err := store.CreateJob(spec, now); err != nil {
		t.Fatal(err)
	}
	for i, count := range []int{1, 0, 0} {
		t.Run(fmt.Sprintf("tick %d", i), func(t *testing.T) {
			runs, err := store.ClaimDue(now.Add(time.Duration(i+1) * time.Minute))
			if err != nil || len(runs) != count {
				t.Fatalf("one-time runs = %+v, %v", runs, err)
			}
		})
	}
}

func TestClockRewindResumesCalendarWithoutReexecuting(t *testing.T) {
	store, spec, now := makeStore(t)
	job, err := store.CreateJob(spec, now)
	if err != nil {
		t.Fatal(err)
	}
	jump := now.Add(30 * time.Minute)
	first, err := store.ClaimDue(jump)
	if err != nil || len(first) != 1 {
		t.Fatalf("forward jump = %+v, %v", first, err)
	}
	if runs, err := store.ClaimDue(now.Add(6 * time.Minute)); err != nil || len(runs) != 0 {
		t.Fatalf("clock rewind replayed stale due = %+v, %v", runs, err)
	}
	runs, err := store.ClaimDue(now.Add(12 * time.Minute))
	if err != nil || len(runs) != 1 || !runs[0].DueAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("normal slot after rewind = %+v, %v", runs, err)
	}
	if runs, err := store.ClaimDue(jump); err != nil || len(runs) != 0 {
		t.Fatalf("normal progress duplicated forward jump = %+v, %v", runs, err)
	}
	history, err := store.History(job.ID)
	if err != nil || len(history) != 2 {
		t.Fatalf("history after clock recovery = %+v, %v", history, err)
	}
}

func TestEditAndArchiveEvaluateOutstandingTriggerBeforeChangingRevision(t *testing.T) {
	for _, archive := range []bool{false, true} {
		t.Run(fmt.Sprintf("archive=%v", archive), func(t *testing.T) {
			store, spec, now := makeStore(t)
			job, err := store.CreateJob(spec, now.Add(-time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			changed := now.Add(time.Second)
			expected := OccurrenceQueued
			if archive {
				expected = OccurrenceMissed
				_, err = store.SetJobState(job.ID, JobArchived, changed)
			} else {
				spec.Prompt = "Updated prompt"
				_, err = store.EditJobRevision(job.ID, job.RevisionID, spec, changed)
			}
			if err != nil {
				t.Fatal(err)
			}
			history, err := store.History(job.ID)
			if err != nil || len(history) != 1 || history[0].State != expected ||
				history[0].Spec.Prompt != job.Spec.Prompt || history[0].RevisionID != job.RevisionID {
				t.Fatalf("unclaimed trigger lost or changed: %+v, %v", history, err)
			}
		})
	}
}

func TestManualInvocationSurvivesRetriesAndIsBoundToOneJob(t *testing.T) {
	store, spec, now := makeStore(t)
	job, err := store.CreateJob(spec, now)
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.CreateJob(spec, now)
	if err != nil {
		t.Fatal(err)
	}
	id := NewID(now)
	first, err := store.ClaimManualWithID(job.ID, id, now)
	if err != nil || first.ID != id {
		t.Fatalf("first claim = %+v, %v", first, err)
	}
	if _, err := store.SetJobState(job.ID, JobArchived, now); err != nil {
		t.Fatal(err)
	}
	retry, err := store.ClaimManualWithID(job.ID, id, now.Add(time.Minute))
	if err != nil || retry.RequestHash != first.RequestHash {
		t.Fatalf("retry = %+v, %v", retry, err)
	}
	if _, err := store.ClaimManualWithID(other.ID, id, now); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-job invocation identity conflict = %v", err)
	}
}

func populateHistory(t testing.TB, store *Store, job Job, now time.Time, count int) {
	t.Helper()
	err := store.update(func(tx *bolt.Tx) error {
		for i := range count {
			at := now.Add(time.Duration(i-count) * time.Minute)
			o, err := newOccurrence(job, at, at, "")
			if err != nil {
				return err
			}
			o.State, o.CompletedAt, o.ProjectedAt, o.ReleasedAt = OccurrenceReady, at, at, at
			if err := putOccurrence(tx, o, true); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestActiveIndexExcludesLongHistoryAndWaitsForReceiptAndWorkspace(t *testing.T) {
	store, spec, now := makeStore(t)
	job, err := store.CreateJob(spec, now)
	if err != nil {
		t.Fatal(err)
	}
	populateHistory(t, store, job, now, 1200)
	for range 20 {
		runs, err := store.ClaimDue(now.Add(-time.Second))
		if err != nil || len(runs) != 0 {
			t.Fatalf("idle claims found old runs: %+v, %v", runs, err)
		}
		active, err := store.ActiveOccurrences()
		if err != nil || len(active) != 0 {
			t.Fatalf("idle active index = %d, %v", len(active), err)
		}
	}
	run, err := store.ClaimManual(job.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, err := store.UpdateOccurrence(run.ID, func(o *Occurrence) error {
			o.State = OccurrenceReady
			switch i {
			case 1:
				o.ProjectedAt = now
			case 2:
				o.ReleasedAt = now
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		want := 1
		if i == 2 {
			want = 0
		}
		if active, err := store.ActiveOccurrences(); err != nil || len(active) != want {
			t.Fatalf("phase %d active index = %d, %v; want %d", i, len(active), err, want)
		}
	}
	history, err := store.History(job.ID)
	if err != nil || len(history) != 1201 {
		t.Fatalf("history size = %d, %v", len(history), err)
	}
}

func BenchmarkIdleTickWithHistory(b *testing.B) {
	for _, count := range []int{0, 1200} {
		b.Run(fmt.Sprintf("runs=%d", count), func(b *testing.B) {
			store, spec, now := makeStore(b)
			job, err := store.CreateJob(spec, now)
			if err != nil {
				b.Fatal(err)
			}
			populateHistory(b, store, job, now, count)
			for b.Loop() {
				if _, err := store.ClaimDue(now.Add(-time.Second)); err != nil {
					b.Fatal(err)
				}
				if _, err := store.ActiveOccurrences(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
