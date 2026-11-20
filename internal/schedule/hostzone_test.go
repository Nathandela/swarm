package schedule

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func switchHost(t *testing.T, store *Store, name string) {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	store.hostZone = func() (*time.Location, string, error) { return loc, name, nil }
}

func TestHostZoneChangeDoesNotReplayWallSlots(t *testing.T) {
	for _, kind := range []string{CalendarDaily, CalendarOnce} {
		t.Run(kind, func(t *testing.T) {
			s, spec, _ := makeStore(t)
			switchHost(t, s, "Europe/Zurich")
			spec.Calendar = Calendar{Kind: kind, TimeZone: HostTimeZone, Time: "09:00"}
			if kind == CalendarOnce {
				spec.Calendar.Date = "2026-11-19"
			}
			if _, err := s.CreateJob(spec, instant(t, "2026-11-19T07:00:00Z")); err != nil {
				t.Fatal(err)
			}
			check := func(at string, count int) {
				t.Helper()
				runs, err := s.ClaimDue(instant(t, at))
				if err != nil || len(runs) != count {
					t.Fatalf("%s: got %v, err %v", at, runs, err)
				}
				if len(runs) > 0 && runs[0].DueAt.Format("15:04") != "09:00" {
					t.Fatalf("historical due lost wall offset: %v", runs[0].DueAt)
				}
			}
			check("2026-11-19T08:00:15Z", 1)
			var err error
			s, err = Open(filepath.Dir(s.dir))
			if err != nil {
				t.Fatal(err)
			}
			switchHost(t, s, "America/New_York")
			check("2026-11-19T12:00:00Z", 0)
			check("2026-11-19T14:00:05Z", 0)
			nextDay := 0
			if kind == CalendarDaily {
				nextDay = 1
			}
			check("2026-11-20T14:00:00Z", nextDay)
		})
	}
}

func TestHostChangeRebasesWhileExplicitZoneKeepsCatchUp(t *testing.T) {
	s, spec, _ := makeStore(t)
	switchHost(t, s, "America/New_York")
	spec.Grace = 24 * time.Hour
	spec.Calendar = Calendar{Kind: CalendarDaily, TimeZone: HostTimeZone, Time: "09:00"}
	created := instant(t, "2026-11-18T23:00:00Z")
	if _, err := s.CreateJob(spec, created); err != nil {
		t.Fatal(err)
	}
	spec.Calendar.TimeZone = "Europe/Zurich"
	fixed, err := s.CreateJob(spec, created)
	if err != nil {
		t.Fatal(err)
	}
	switchHost(t, s, "Europe/Zurich")
	at := instant(t, "2026-11-19T09:30:00Z")
	runs, err := s.ClaimDue(at)
	if err != nil || len(runs) != 1 || runs[0].JobID != fixed.ID || !runs[0].DueAt.Equal(instant(t, "2026-11-19T08:00:00Z")) {
		t.Fatalf("move: got %+v %v", runs, err)
	}
	runs, err = s.ClaimDue(at.Add(time.Minute))
	if err != nil || len(runs) != 0 {
		t.Fatalf("replayed catch-up: %+v %v", runs, err)
	}
}

func TestHostFollowsDSTWithoutTreatingOffsetChangeAsTravel(t *testing.T) {
	for _, tc := range []struct{ name, created, tick, due string }{
		{"gap", "2026-03-28T12:00:00Z", "2026-03-30T00:30:00Z", "2026-03-30T00:30:00Z"},
		{"repeat", "2026-10-24T12:00:00Z", "2026-10-25T01:30:00Z", "2026-10-25T00:30:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, spec, _ := makeStore(t)
			switchHost(t, s, "Europe/Zurich")
			spec.Calendar = Calendar{Kind: CalendarDaily, TimeZone: HostTimeZone, Time: "02:30"}
			if _, err := s.CreateJob(spec, instant(t, tc.created)); err != nil {
				t.Fatal(err)
			}
			if tc.name == "gap" {
				runs, err := s.ClaimDue(instant(t, "2026-03-29T03:00:00Z"))
				if err != nil || len(runs) != 0 {
					t.Fatalf("spring gap fired: %+v %v", runs, err)
				}
			}
			runs, err := s.ClaimDue(instant(t, tc.tick))
			if err != nil || len(runs) != 1 || !runs[0].DueAt.Equal(instant(t, tc.due)) {
				t.Fatalf("wrong DST instant: %+v %v", runs, err)
			}
		})
	}
}

func TestHostZoneReadsOSFileAfterReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "localtime")
	for _, name := range []string{"Europe/Zurich", "America/New_York"} {
		source := filepath.Join("/usr/share/zoneinfo", name)
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(dir, "zoneinfo", name)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0600); err != nil {
			t.Fatal(err)
		}
		link := path + ".next"
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(link, path); err != nil {
			t.Fatal(err)
		}
		loc, id, err := readHostZone(path)
		if err != nil || loc.String() != name || id == "" {
			t.Fatalf("read %s: %v %s %v", name, loc, id, err)
		}
	}
}

func TestEditorSaveCommitsPausedDefinitionAndRevisionTogether(t *testing.T) {
	s, spec, now := makeStore(t)
	job, err := s.SaveJob("", "", spec, JobPaused, now)
	if err != nil || job.State != JobPaused {
		t.Fatalf("create paused: %+v %v", job, err)
	}
	if runs, err := s.ClaimDue(now.Add(time.Minute)); err != nil || len(runs) != 0 {
		t.Fatalf("paused creation dispatched: %+v %v", runs, err)
	}
	spec.Prompt = "new task"
	enabled, err := s.SaveJob(job.ID, job.RevisionID, spec, JobActive, now.Add(time.Minute))
	if err != nil || enabled.State != JobActive || enabled.Spec.Prompt != spec.Prompt {
		t.Fatalf("enable: %+v %v", enabled, err)
	}
	if _, err := s.SaveJob(job.ID, job.RevisionID, spec, JobPaused, now.Add(time.Minute)); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale edit changed active state: %v", err)
	}
	paused, err := s.SaveJob(job.ID, enabled.RevisionID, spec, JobPaused, now.Add(6*time.Minute))
	if err != nil || paused.State != JobPaused {
		t.Fatalf("pause: %+v %v", paused, err)
	}
	history, err := s.History(job.ID)
	if err != nil || len(history) != 1 || history[0].State != OccurrenceMissed || history[0].RevisionID != enabled.RevisionID {
		t.Fatalf("pause boundary left pending work: %+v %v", history, err)
	}
}
