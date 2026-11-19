package tui

import (
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
		case scheduleResultMsg:
			next, _ = next.Update(msg)
		default:
			t.Fatalf("unexpected command result %T", msg)
		}
	}
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
		Open:   func() (ScheduleCatalog, error) { return db, nil },
		Prompt: func(prompt string) string { return prompt + "\nCompletion instructions" },
	}))
	model := send(m, tea.WindowSizeMsg{Width: 78, Height: 16}).(rootModel)
	model = send(model, keyRune('m')).(rootModel)
	model = scheduleTestUpdate(t, model, keyRune('c'))
	if model.schedules.draft.directory.text != dir || model.schedules.page != scheduleTask {
		t.Fatalf("schedule action failed to capture selected directory: %+v", model.schedules)
	}
	model = send(model, tea.PasteMsg{Content: "Nightly review"}).(rootModel)
	model = send(model, keyDown).(rootModel)
	model = send(model, tea.PasteMsg{Content: "First line\r\nSecond line"}).(rootModel)
	model = send(model, tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}).(rootModel)
	model = scheduleTestUpdate(t, model, keyEnter)
	templates, err := db.Templates()
	if err != nil || len(templates) != 1 || templates[0].Prompt != "First line\nSecond line" {
		t.Fatalf("template prompt snapshot = %+v, %v", templates, err)
	}
	model = send(model, keyEnter).(rootModel)
	model = send(model, keyDown).(rootModel)
	model = sendType(model, "08:00").(rootModel)
	model = send(model, keyEnter).(rootModel)
	model = scheduleTestUpdate(t, model, keyEnter)
	if model.schedules.page != scheduleRoute || model.schedules.errorText == "" {
		t.Fatal("validation must preserve the route draft")
	}
	model = send(model, tea.KeyPressMsg{Code: tea.KeyLeft}).(rootModel) // selected directory
	model = send(model, keyDown).(rootModel)
	model = send(model, tea.KeyPressMsg{Code: tea.KeyLeft}).(rootModel) // configured authority
	for range 2 {
		model = send(model, keyDown).(rootModel)
	}
	model = send(model, keyRune(' ')).(rootModel) // codex
	model = send(model, keyDown).(rootModel)
	model = send(model, keyRune(' ')).(rootModel) // claude
	model = send(model, keyRune('-')).(rootModel) // promote Claude
	model = scheduleTestUpdate(t, model, keyEnter)
	if model.schedules.page != scheduleReview || !strings.Contains(model.schedules.preview, "claude → codex") || !strings.Contains(model.schedules.preview, "Completion instructions") {
		t.Fatalf("unexpected review: %q / %q", model.schedules.preview, model.schedules.errorText)
	}
	model = scheduleTestUpdate(t, model, keyEnter)
	jobs, err := db.Jobs()
	if err != nil || len(jobs) != 1 {
		t.Fatalf("job was not persisted: %+v, %v", jobs, err)
	}
	if !reflect.DeepEqual(jobs[0].Spec.Agents, []string{"claude", "codex"}) || jobs[0].Spec.Prompt != templates[0].Prompt || filepath.Base(jobs[0].Spec.Directory) != filepath.Base(dir) || model.schedules.page != scheduleJob {
		t.Fatalf("saved draft differs from review: %+v", jobs[0].Spec)
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
		run: schedule.Occurrence{State: schedule.OccurrenceReady, Attempts: []schedule.Attempt{{Agent: "codex", SessionID: session.ID}}},
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
