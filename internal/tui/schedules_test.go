package tui

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/schedule"
	"github.com/charmbracelet/x/ansi"
)

type lostScheduleReplyClient struct {
	*fakeClient
	store *schedule.Store
	keys  []string
}

func (c *lostScheduleReplyClient) ScheduleRun(jobID, operationID string) (string, error) {
	c.keys = append(c.keys, operationID)
	run, err := c.store.ClaimManualWithID(jobID, operationID, time.Now())
	if err == nil && len(c.keys) == 1 {
		return "", errors.New("connection closed before the run reply")
	}
	return run.ID, err
}

func TestScheduleRunNowRetriesLostReplyWithSameOperationID(t *testing.T) {
	store, err := schedule.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.CreateJob(schedule.JobSpec{
		Name: "Review", Prompt: "Review the work", Directory: t.TempDir(),
		Authority: schedule.AuthorityObserve, Workspace: schedule.WorkspaceDirectory,
		Agents: []string{"codex"}, Calendar: schedule.Calendar{Kind: "daily", TimeZone: "UTC", Time: "08:00"},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	client := &lostScheduleReplyClient{fakeClient: newFakeClient(), store: store}
	m := send(New(client, nil, WithSchedules(ScheduleConfig{
		Open: func() (ScheduleCatalog, error) { return store, nil },
	})), tea.WindowSizeMsg{Width: 120, Height: 16}).(rootModel)
	m = send(m, keyRune('m')).(rootModel)
	m = scheduleTestUpdate(t, m, keyRune('a'))
	m = scheduleTestUpdate(t, m, keyEnter)
	m = scheduleTestUpdate(t, m, keyRune('r'))
	if len(client.keys) != 1 || client.keys[0] == "" {
		t.Fatalf("first Run now sent %q", client.keys)
	}
	key := client.keys[0]
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Retry Run now") || !strings.Contains(view, "--operation-id "+key) {
		t.Fatalf("Run now recovery missing from view: %q", view)
	}

	// Leaving and reopening Automations preserves the pending operation.
	m = scheduleTestUpdate(t, m, keyEsc)
	m = send(m, keyEsc).(rootModel)
	m = scheduleTestUpdate(t, m, keyRune('a'))
	m = scheduleTestUpdate(t, m, keyEnter)
	m = scheduleTestUpdate(t, m, keyRune('r'))
	if !reflect.DeepEqual(client.keys, []string{key, key}) || m.schedules.page != scheduleRun {
		t.Fatalf("Run now retry sent %q, page %v", client.keys, m.schedules.page)
	}
	runs, err := store.History(job.ID)
	if err != nil || len(runs) != 1 || runs[0].ID != m.schedules.run.ID {
		t.Fatalf("recovered runs = %+v, %v", runs, err)
	}

	m = send(m, keyEsc).(rootModel)
	m = send(m, keyEsc).(rootModel)
	m = scheduleTestUpdate(t, m, keyRune('r'))
	if len(client.keys) != 3 || client.keys[2] == key || m.schedules.run.ID == runs[0].ID {
		t.Fatalf("deliberate new Run now sent %q, run %q", client.keys, m.schedules.run.ID)
	}
}

func scheduleTestUpdate(t *testing.T, model tea.Model, msg tea.Msg) rootModel {
	t.Helper()
	next, cmd := model.Update(msg)
	if cmd != nil {
		switch msg := cmd().(type) {
		case scheduleResultMsg, scheduleTranscriptMsg, scheduleOpenResultMsg:
			next, _ = next.Update(msg)
		default:
			t.Fatalf("unexpected command result %T", msg)
		}
	}
	return next.(rootModel)
}

func scheduleEditorKey(t *testing.T, model rootModel, row string, key tea.KeyPressMsg) rootModel {
	t.Helper()
	model.selectScheduleRow(row)
	if got := model.selectedScheduleRow(); got != row {
		t.Fatalf("row %q unavailable, selected %q", row, got)
	}
	return send(model, key).(rootModel)
}

func applyScheduleCheck(t *testing.T, model rootModel) rootModel {
	t.Helper()
	next, cmd := model.Update(schedulePreflightTick{generation: model.scheduleEditorGeneration})
	if cmd == nil {
		t.Fatal("editor did not start preflight")
	}
	next, _ = next.Update(cmd())
	return next.(rootModel)
}

func TestScheduleFromBoardKeepsDraftAndPersistsOrderedRoutes(t *testing.T) {
	db, err := schedule.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	fake := newFakeClient(sWorking("endpoint/a", "codex", dir, "current", time.Minute))
	m := New(fake, nil, WithSchedules(ScheduleConfig{
		Open: func() (ScheduleCatalog, error) { return db, nil },
	}))
	model := send(m, tea.WindowSizeMsg{Width: 78, Height: 16}).(rootModel)
	model = send(model, keyRune('m')).(rootModel)
	model = scheduleTestUpdate(t, model, keyRune('c'))
	if model.schedules.draft.directory.text != dir || model.schedules.page != scheduleEditor {
		t.Fatalf("schedule action failed to capture selected directory: %+v", model.schedules)
	}
	defaults := model.schedules.draft.spec()
	if defaults.Calendar.Kind != schedule.CalendarDaily || defaults.Calendar.TimeZone != schedule.HostTimeZone ||
		defaults.Calendar.Time != "09:00" || defaults.Authority != schedule.AuthorityObserve || defaults.Workspace != schedule.WorkspaceDirectory {
		t.Fatalf("unexpected defaults: %+v", defaults)
	}
	if strings.Contains(ansi.Strip(model.View().Content), "Fallback 1") {
		t.Fatal("advanced editor opened by default")
	}
	model = send(model, tea.PasteMsg{Content: "Nightly review"}).(rootModel)
	model = send(model, keyDown).(rootModel)
	model = send(model, tea.PasteMsg{Content: "First line\r\nSecond line"}).(rootModel)
	model = send(model, scheduleActionKey("ctrl+e")).(rootModel)
	model = scheduleTestUpdate(t, model, keyEnter)
	templates, err := db.Templates()
	if err != nil || len(templates) != 1 || templates[0].Prompt != "First line\nSecond line" {
		t.Fatalf("template prompt snapshot = %+v, %v", templates, err)
	}
	model = scheduleTestUpdate(t, model, scheduleActionKey("ctrl+t"))
	model = send(model, keyEnter).(rootModel)
	model = scheduleEditorKey(t, model, "primary", scheduleActionKey("right"))
	if model.schedules.draft.authority != schedule.AuthorityObserve {
		t.Fatal("selecting Claude broadened authority")
	}
	model = scheduleEditorKey(t, model, "authority", scheduleActionKey("left"))
	model = scheduleEditorKey(t, model, "advanced", keyEnter)
	model = scheduleEditorKey(t, model, "fallback-agy", keyEnter)
	model = scheduleEditorKey(t, model, "fallback-codex", keyEnter)
	model = scheduleEditorKey(t, model, "fallback-codex", keyRune('-'))
	model = applyScheduleCheck(t, model)
	if !strings.Contains(model.schedules.nextFiring, "09:00") {
		t.Fatalf("missing next firing: %q", model.schedules.nextFiring)
	}
	model = scheduleTestUpdate(t, model, scheduleActionKey("ctrl+d"))
	jobs, err := db.Jobs()
	if err != nil || len(jobs) != 1 {
		t.Fatalf("job was not persisted: %+v, %v", jobs, err)
	}
	if !reflect.DeepEqual(jobs[0].Spec.Agents, []string{"claude", "codex", "agy"}) || jobs[0].Spec.Prompt != templates[0].Prompt ||
		filepath.Base(jobs[0].Spec.Directory) != filepath.Base(dir) || jobs[0].State != schedule.JobPaused || model.schedules.page != scheduleJob {
		t.Fatalf("saved draft differs from editor: %+v", jobs[0])
	}
}

func TestScheduleHistoryOpenUsesBoardAttachAndTagControlsMarker(t *testing.T) {
	db, err := schedule.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	job, err := db.CreateJob(schedule.JobSpec{
		Name: "Nightly review", Prompt: "Summarize", Directory: dir,
		Authority: schedule.AuthorityObserve, Workspace: schedule.WorkspaceDirectory,
		Agents: []string{"codex"}, Calendar: schedule.Calendar{Kind: "daily", TimeZone: "UTC", Time: "08:00"},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	session := sReview("endpoint/a", "codex", dir, "ready", time.Minute)
	session.ScheduledCompleted = true
	session.Tag = "automation"
	model := send(New(newFakeClient(session), nil), tea.WindowSizeMsg{Width: 80, Height: 18}).(rootModel)
	if !strings.Contains(ansi.Strip(model.View().Content), "◆") {
		t.Fatal("board omitted the completion marker")
	}
	changed := session
	changed.Tag = "another-tag"
	model = send(model, eventMsg{ev: protocol.Event{Session: changed}}).(rootModel)
	if strings.Contains(ansi.Strip(model.View().Content), "◆") {
		t.Fatal("changing the automation tag must remove the marker")
	}
	model.screen = screenSchedules
	model.schedules = scheduleModel{
		catalog: db, page: scheduleRun, job: job,
		run: schedule.Occurrence{State: schedule.OccurrenceNeedsInput, Attempts: []schedule.Attempt{{Agent: "codex", SessionID: session.ID}}},
	}
	if !strings.Contains(ansi.Strip(model.View().Content), "Open worker") {
		t.Fatal("run detail omitted the control for addressing native input")
	}
	model = send(model, keyRune('o')).(rootModel)
	if model.screen != screenAttach || model.attach.session.ID != session.ID {
		t.Fatalf("run did not attach its recorded session: %+v", model.attach.session)
	}
	model = send(model, keyEsc).(rootModel)
	if model.screen != screenSchedules || model.schedules.page != scheduleRun {
		t.Fatal("detach did not return to run detail")
	}
}

func TestScheduleTimerEnableRequiresConfirmation(t *testing.T) {
	db, err := schedule.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	enabled := false
	model := New(newFakeClient(), nil, WithSchedules(ScheduleConfig{
		Open: func() (ScheduleCatalog, error) { return db, nil },
		TimerStatus: func() (string, error) {
			if enabled {
				return "installed, active", nil
			}
			return "not installed, inactive", nil
		},
		InstallTimer: func() error {
			enabled = true
			return nil
		},
	}))
	m := send(model, tea.WindowSizeMsg{Width: 64, Height: 10}).(rootModel)
	m = send(m, keyRune('m')).(rootModel)
	m = scheduleTestUpdate(t, m, keyRune('a'))
	if !strings.Contains(ansi.Strip(m.View().Content), "Timer not installed, inactive") {
		t.Fatal("timer status missing")
	}
	m = send(m, keyRune('i')).(rootModel)
	m = send(m, keyEsc).(rootModel)
	if enabled || m.schedules.page != scheduleList {
		t.Fatal("cancelling confirmation enabled the timer")
	}
	m = send(m, keyRune('i')).(rootModel)
	m = scheduleTestUpdate(t, m, keyRune('y'))
	if !enabled || !strings.Contains(ansi.Strip(m.View().Content), "Timer installed, active") {
		t.Fatal("enabling the timer did not refresh status")
	}
	m = send(m, tea.WindowSizeMsg{Width: 24, Height: 4}).(rootModel)
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	if len(lines) != 4 || !strings.Contains(lines[0], " · active") || !strings.Contains(lines[3], "[esc]") {
		t.Fatalf("narrow screen lost navigation: %q", lines)
	}
}

func TestScheduleCompletedWorkerReopensNativeConversation(t *testing.T) {
	session := sCompleted("endpoint/completed-worker", "codex", t.TempDir(), "worker exited", time.Minute)
	session.ScheduledCompleted = true
	session.Tag = "automation"
	fake := newFakeClient(session)
	m := send(New(fake, nil), tea.WindowSizeMsg{Width: 80, Height: 18}).(rootModel)
	if !strings.Contains(ansi.Strip(m.View().Content), "[Open (⏎)]") {
		t.Fatal("ended completed worker is missing the board Open action")
	}
	board, cmd := m.Update(keyEnter)
	if cmd == nil {
		t.Fatal("board Open did not request the native conversation")
	}
	board, _ = board.Update(cmd())
	if board.(rootModel).screen != screenAttach {
		t.Fatal("board Open did not attach native resume")
	}

	db, err := schedule.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.screen = screenSchedules
	m.schedules = scheduleModel{
		catalog: db, page: scheduleRun,
		run: schedule.Occurrence{
			ID: "saved-run", State: schedule.OccurrenceReady,
			Attempts: []schedule.Attempt{{Agent: session.Agent, SessionID: session.ID}},
		},
	}
	next, cmd := m.Update(keyRune('o'))
	if cmd == nil {
		t.Fatal("history Open did not request the native conversation")
	}
	next, _ = next.Update(cmd())
	resumed := next.(rootModel)
	if resumed.screen != screenAttach || resumed.attach.session.ID != fake.launchID {
		t.Fatalf("history Open did not attach native resume: %+v", resumed.attach.session)
	}
	if len(fake.launched) != 2 {
		t.Fatalf("native resume requests = %d", len(fake.launched))
	}
	for _, request := range fake.launched {
		if request.Options[protocol.OptionResumeFrom] != session.ID || request.Cwd != session.Cwd {
			t.Fatalf("request did not pin original conversation and checkout: %+v", request)
		}
	}
	resumed = send(resumed, keyEsc).(rootModel)
	if resumed.screen != screenSchedules || resumed.schedules.run.ID != "saved-run" || scheduleSession(resumed.schedules.run) != session.ID {
		t.Fatal("review continuation overwrote run history identity")
	}
}

type scheduleTranscriptClient struct {
	*fakeClient
	requested string
}

func (c *scheduleTranscriptClient) InteractionHistory(id string, _ int) ([]protocol.JournalRecord, error) {
	c.requested = id
	return []protocol.JournalRecord{
		{Type: "interaction", Item: json.RawMessage(`{"kind":"tool_run","item_id":"tool","text":"hidden tool output"}`)},
		{Type: "interaction", Item: json.RawMessage(`{"kind":"agent_message","item_id":"closing","status":"in_progress","text":"Fixture is "}`)},
		{Type: "interaction", Item: json.RawMessage(`{"kind":"agent_message","item_id":"closing","status":"completed","text":"ready for review.\u001b[2J"}`)},
	}, nil
}

func TestScheduleRunRetainsRecentNativeMessagesWhenResumeFails(t *testing.T) {
	session := sCompleted("endpoint/finished", "codex", t.TempDir(), "worker exited", time.Minute)
	client := &scheduleTranscriptClient{fakeClient: newFakeClient(session)}
	client.launchErr = errors.New("Native project workspace is untrusted")
	model := send(New(client, nil), tea.WindowSizeMsg{Width: 100, Height: 25}).(rootModel)
	catalog, err := schedule.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	model.screen = screenSchedules
	model.schedules = scheduleModel{catalog: catalog, page: scheduleHistory, runs: []schedule.Occurrence{{
		ID: "retained-run", State: schedule.OccurrenceReady,
		Attempts: []schedule.Attempt{{Agent: session.Agent, SessionID: session.ID}},
	}}}
	model = scheduleTestUpdate(t, model, keyEnter)
	if client.requested != session.ID {
		t.Fatalf("transcript request = %q, want recorded worker %q", client.requested, session.ID)
	}
	model = scheduleTestUpdate(t, model, keyRune('o'))
	view := model.View().Content
	for _, text := range []string{"Fixture is ready for review.", "Open native chat", "Native project workspace is untrusted"} {
		if !strings.Contains(ansi.Strip(view), text) {
			t.Fatalf("run detail after native resume refusal lacks %q", text)
		}
	}
	if strings.Contains(view, "hidden tool output") || strings.Contains(view, "\x1b[2J") {
		t.Fatal("run detail rendered hidden tool output or a terminal escape from chat")
	}
}

func TestScheduleEditorLegacyZoneRequiresExplicitConversion(t *testing.T) {
	db, err := schedule.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := db.CreateJob(schedule.JobSpec{
		Name: "Keep my schedule", Prompt: "Review fixture", Directory: t.TempDir(),
		Authority: schedule.AuthorityObserve, Workspace: schedule.WorkspaceDirectory,
		Agents:   []string{schedule.RouteCodex},
		Calendar: schedule.Calendar{Kind: schedule.CalendarWeekly, TimeZone: "America/New_York", Weekday: "Friday", Time: "08:45"},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	model := send(New(newFakeClient(), nil), tea.WindowSizeMsg{Width: 90, Height: 22}).(rootModel)
	model.screen = screenSchedules
	model.schedules = scheduleModel{catalog: db, page: scheduleJob, job: legacy}
	model = send(model, keyRune('e')).(rootModel)
	if !strings.Contains(ansi.Strip(model.View().Content), "America/New_York (fixed)") {
		t.Fatal("legacy zone not displayed")
	}
	model = scheduleEditorKey(t, model, "zone", scheduleActionKey("right"))
	model = scheduleEditorKey(t, model, "convert-zone", keyEnter)
	if model.schedules.page != scheduleConvertZone {
		t.Fatal("conversion should show its effect before accepting")
	}
	model = send(model, keyEsc).(rootModel)
	if model.schedules.draft.spec().Calendar != legacy.Spec.Calendar {
		t.Fatal("opening editor and cancelling conversion changed the legacy calendar")
	}
	model = scheduleTestUpdate(t, model, scheduleActionKey("ctrl+s"))
	if model.schedules.job.Spec.Calendar != legacy.Spec.Calendar {
		t.Fatal("saving a changed job silently converted its legacy zone")
	}
	model = send(model, keyRune('e')).(rootModel)
	model = scheduleEditorKey(t, model, "convert-zone", keyEnter)
	model = send(model, keyRune('y')).(rootModel)
	model = scheduleTestUpdate(t, model, scheduleActionKey("ctrl+s"))
	want := legacy.Spec.Calendar
	want.TimeZone = schedule.HostTimeZone
	if model.schedules.job.Spec.Calendar != want {
		t.Fatalf("converted calendar = %+v, want %+v", model.schedules.job.Spec.Calendar, want)
	}
}

func TestScheduleEditorFailedEnableKeepsFieldsAndRetryDoesNotDuplicate(t *testing.T) {
	db, err := schedule.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var attempts int
	timer := "not installed, inactive"
	model := send(New(newFakeClient(), nil, WithSchedules(ScheduleConfig{
		TimerStatus: func() (string, error) { return timer, nil },
		Save: func(spec schedule.JobSpec, id, rev string, enable, allowTimerInstall bool) (schedule.Job, error) {
			if !enable || !allowTimerInstall {
				t.Fatal("enable confirmation did not authorize timer installation")
			}
			attempts++
			if attempts == 1 {
				return schedule.Job{}, errors.New("timer start failed")
			}
			timer = "installed, active"
			return db.SaveJob(id, rev, spec, schedule.JobActive, time.Now())
		},
	})), tea.WindowSizeMsg{Width: 80, Height: 20}).(rootModel)
	model.screen = screenSchedules
	model.schedules = scheduleModel{catalog: db, page: scheduleEditor, timerStatus: timer, draft: blankSchedule(t.TempDir())}
	model.schedules.draft.name.set("Review")
	model.schedules.draft.prompt.set("Inspect previous run")
	model = send(model, scheduleActionKey("ctrl+s")).(rootModel)
	if model.schedules.page != scheduleEditorEnable || attempts != 0 ||
		!strings.Contains(ansi.Strip(model.View().Content), "this user's catalog") {
		t.Fatal("enable skipped host timer confirmation")
	}
	model = scheduleTestUpdate(t, model, keyRune('y'))
	if model.schedules.page != scheduleEditor || model.schedules.draft.prompt.text != "Inspect previous run" ||
		!strings.Contains(model.schedules.errorText, "timer start failed") {
		t.Fatal("failed install discarded the editor or hid the error")
	}
	jobs, err := db.Jobs()
	if err != nil || len(jobs) != 0 {
		t.Fatalf("failed install persisted a job: %+v, %v", jobs, err)
	}
	model = send(model, scheduleActionKey("ctrl+s")).(rootModel)
	model = scheduleTestUpdate(t, model, keyRune('y'))
	jobs, err = db.Jobs()
	if err != nil || len(jobs) != 1 || jobs[0].State != schedule.JobActive ||
		model.schedules.page != scheduleJob || model.schedules.timerStatus != "installed, active" {
		t.Fatalf("successful retry = %+v, %v", jobs, err)
	}
	history, err := db.History(jobs[0].ID)
	if err != nil || len(history) != 0 {
		t.Fatalf("saving unexpectedly started a run: %+v, %v", history, err)
	}
}

func TestScheduleEditorPreflightUpdatesAfterFixAndSupportsSmallMouse(t *testing.T) {
	db, err := schedule.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	conflict, fixed := t.TempDir(), t.TempDir()
	client := newFakeClient()
	model := send(New(client, nil, WithSchedules(ScheduleConfig{
		Preflight: func(spec schedule.JobSpec) (SchedulePreflight, error) {
			if spec.Directory == conflict {
				return SchedulePreflight{Blocking: []string{"Conflicting checkout. Select an isolated Git worktree."}}, nil
			}
			if spec.Directory != fixed {
				t.Fatalf("preflight directory = %q", spec.Directory)
			}
			return SchedulePreflight{Warnings: []string{"Task may need GitHub network access."}}, nil
		},
	})), tea.WindowSizeMsg{Width: 48, Height: 10}).(rootModel)
	model.screen = screenSchedules
	model.schedules = scheduleModel{catalog: db, page: scheduleEditor, draft: blankSchedule(conflict)}
	model = applyScheduleCheck(t, model)
	model = send(model, scheduleActionKey("ctrl+s")).(rootModel)
	if !strings.Contains(model.schedules.errorText, "Conflicting checkout") {
		t.Fatal("editor omitted overlap remediation")
	}

	model.schedules.draft.directory.set(fixed)
	model = send(model, scheduleActionKey("ctrl+r")).(rootModel)
	staleGeneration := model.scheduleEditorGeneration - 1
	model = send(model, schedulePreflightResult{generation: staleGeneration, report: SchedulePreflight{Blocking: []string{"stale"}}}).(rootModel)
	model = applyScheduleCheck(t, model)
	if got := model.schedules.preflight; len(got.Blocking) > 0 || len(got.Warnings) != 1 {
		t.Fatalf("new preflight = %+v", got)
	}

	model.selectScheduleRow("advanced")
	layout := model.scheduleLayout()
	y := model.schedules.sel - layout.top + 1
	model = send(model, tea.MouseClickMsg{X: 3, Y: y, Button: tea.MouseLeft}).(rootModel)
	model = send(model, tea.MouseReleaseMsg{X: 3, Y: y, Button: tea.MouseLeft}).(rootModel)
	if !model.schedules.draft.advanced {
		t.Fatal("mouse did not expand Advanced")
	}
	model = send(model, tea.WindowSizeMsg{Width: 24, Height: 4}).(rootModel)
	model.selectScheduleRow("save-draft")
	view := ansi.Strip(model.View().Content)
	if lines := strings.Split(view, "\n"); len(lines) != 4 || !strings.Contains(lines[3], "[esc]") || !strings.Contains(lines[2], "Save draft") {
		t.Fatalf("small editor lost focused control or Back: %q", view)
	}
}
