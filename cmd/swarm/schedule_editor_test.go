package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/schedule"
	"github.com/Nathandela/swarm/internal/tui"
)

func editorTestJob(t *testing.T) (*schedule.Store, schedule.JobSpec) {
	t.Helper()
	store, err := schedule.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store, schedule.JobSpec{Name: "Review", Prompt: "Review changes", Directory: t.TempDir(), Agents: []string{schedule.RouteCodex}, Authority: schedule.AuthorityObserve, Workspace: schedule.WorkspaceDirectory, Calendar: schedule.Calendar{Kind: schedule.CalendarDaily, TimeZone: schedule.HostTimeZone, Time: "09:00"}}
}

func TestScheduleEditorRetriesAndTimerConsent(t *testing.T) {
	store, spec := editorTestJob(t)
	allowed := func(schedule.JobSpec) (tui.SchedulePreflight, error) { return tui.SchedulePreflight{}, nil }
	working := func() (string, error) { return "installed, active", nil }
	stopped := func() (string, error) { return "installed, inactive", nil }
	installCalls := 0
	failInstall := func() error { installCalls++; return errors.New("system manager unavailable") }
	for _, consent := range []bool{false, true} {
		_, err := persistScheduleEditor(store, spec, "", "", true, consent, allowed, stopped, failInstall)
		if err == nil {
			t.Fatalf("save succeeded with inactive timer (consent=%v)", consent)
		}
		if !consent && installCalls != 0 {
			t.Fatal("installed without consent")
		}
	}
	jobs, err := store.Jobs()
	if err != nil || len(jobs) != 0 || installCalls != 1 {
		t.Fatalf("after failures: %+v %v, installs %d", jobs, err, installCalls)
	}
	_, err = persistScheduleEditor(store, spec, "", "", true, true, allowed, stopped, func() error { return nil })
	if err == nil || !strings.Contains(err.Error(), "verify") && !strings.Contains(err.Error(), "reports") {
		t.Fatalf("accepted an inactive timer after installation: %v", err)
	}
	job, err := persistScheduleEditor(store, spec, "", "", true, false, allowed, working, failInstall)
	if err != nil || job.State != schedule.JobActive {
		t.Fatalf("retry: %+v %v", job, err)
	}
	jobs, err = store.Jobs()
	if err != nil || len(jobs) != 1 || installCalls != 1 {
		t.Fatalf("retry duplicated work: %v %v %d", jobs, err, installCalls)
	}
	history, err := store.History(job.ID)
	if err != nil || len(history) != 0 {
		t.Fatalf("save launched a run: %v %v", history, err)
	}
}

func TestScheduleEditorPausedAndPreflightFailures(t *testing.T) {
	store, spec := editorTestJob(t)
	blocked := func(schedule.JobSpec) (tui.SchedulePreflight, error) {
		return tui.SchedulePreflight{Blocking: []string{"workspace overlaps /busy"}}, nil
	}
	neverStatus := func() (string, error) { t.Fatal("touched host timer"); return "", nil }
	neverInstall := func() error { t.Fatal("installed host timer"); return nil }
	neverPreflight := func(schedule.JobSpec) (tui.SchedulePreflight, error) {
		t.Fatal("paused save performed preflight")
		return tui.SchedulePreflight{}, nil
	}
	job, err := persistScheduleEditor(store, spec, "", "", false, false, neverPreflight, neverStatus, neverInstall)
	if err != nil || job.State != schedule.JobPaused {
		t.Fatalf("draft: %v %v", job, err)
	}
	_, err = persistScheduleEditor(store, spec, job.ID, job.RevisionID, true, true, blocked, neverStatus, neverInstall)
	if err == nil || !strings.Contains(err.Error(), "/busy") {
		t.Fatalf("blocked enable: %v", err)
	}
	got, err := store.Job(job.ID)
	if err != nil || got.State != schedule.JobPaused || got.RevisionID != job.RevisionID {
		t.Fatalf("failed enable changed draft: %+v %v", got, err)
	}
	_, err = persistScheduleEditor(store, spec, job.ID, "stale", true, true, neverPreflight, neverStatus, neverInstall)
	if !errors.Is(err, schedule.ErrConflict) {
		t.Fatalf("stale edit: %v", err)
	}
}

// Only hello is exercised. No job operation is authorized on this old peer.
type legacyEditorDaemon struct{ protocol.DaemonAPI }

func (legacyEditorDaemon) Events() <-chan persist.Meta { return nil }

func TestScheduleCatalogGuardsHostCalendarAgainstLegacyDaemon(t *testing.T) {
	store, spec := editorTestJob(t)
	short, err := os.MkdirTemp("/tmp", "sc-old-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	sock := filepath.Join(short, "s")
	t.Setenv("SWARM_DAEMON_SOCK", sock)
	legacy, err := protocol.Serve(legacyEditorDaemon{}, sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = legacy.Close() }()
	write := func() {
		t.Helper()
		b, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(short, "spec.json"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write()
	path := filepath.Join(short, "spec.json")
	if _, err := scheduleCatalog(store, []string{"create", "--spec-file", path}, io.Discard); err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatalf("legacy accepted active host job: %v", err)
	}
	draft, err := store.SaveJob("", "", spec, schedule.JobPaused, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scheduleCatalog(store, []string{"resume", draft.ID}, io.Discard); err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatalf("legacy resumed host job: %v", err)
	}
	spec.Calendar.TimeZone = "UTC"
	write()
	v, err := scheduleCatalog(store, []string{"create", "--spec-file", path}, io.Discard)
	if err != nil {
		t.Fatalf("legacy named zone create: %v", err)
	}
	job := v.(schedule.Job)
	spec.Calendar.TimeZone = schedule.HostTimeZone
	write()
	if _, err := scheduleCatalog(store, []string{"edit", job.ID, "--spec-file", path}, io.Discard); err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatalf("legacy edited to host calendar: %v", err)
	}
	// The CLI can still create definitions when the daemon has never started.
	t.Setenv("SWARM_DAEMON_SOCK", filepath.Join(short, "offline"))
	if _, err := scheduleCatalog(store, []string{"create", "--spec-file", path}, io.Discard); err != nil {
		t.Fatalf("offline catalog create: %v", err)
	}
}

type editorPreflightDaemon struct{ legacyEditorDaemon }

func (editorPreflightDaemon) ScheduleTick() error { panic("preflight ticked") }
func (editorPreflightDaemon) ScheduleRun(string, string) (string, error) {
	panic("preflight launched a job")
}
func (editorPreflightDaemon) ScheduleComplete(string, schema.ScheduleCompletion) error {
	panic("preflight completed a job")
}
func (editorPreflightDaemon) ScheduleRouteAvailability(agent string) string {
	if agent == schedule.RouteClaude {
		return "sign in to Claude"
	}
	return ""
}
func (editorPreflightDaemon) ScheduleWorkspaceConflict(string) string { return "" }

func TestScheduleEditorPreflightUsesOwnerRouteAndWorkspace(t *testing.T) {
	short, err := os.MkdirTemp("/tmp", "sc-preview-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	t.Setenv("SWARM_DAEMON_STATE", filepath.Join(short, "state"))
	sock := filepath.Join(short, "s")
	t.Setenv("SWARM_DAEMON_SOCK", sock)
	srv, err := protocol.Serve(editorPreflightDaemon{}, sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	_, spec := editorTestJob(t)
	spec.Authority = schedule.AuthorityAgentDefault
	spec.Agents = []string{schedule.RouteClaude}
	report, err := preflightSchedule(spec)
	if err != nil || len(report.Blocking) != 1 || !strings.Contains(report.Blocking[0], "sign in") {
		t.Fatalf("only route unavailable: %+v %v", report, err)
	}
	spec.Agents = append(spec.Agents, schedule.RouteCodex)
	report, err = preflightSchedule(spec)
	if err != nil || len(report.Blocking) != 0 || len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0], "sign in") {
		t.Fatalf("healthy fallback: %+v %v", report, err)
	}
}
