package skeleton

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/schedule"
	"github.com/Nathandela/swarm/internal/scheduledexec"
)

type scheduledShimFixture struct {
	scheduledexec.Backend
	api      *coreAPI
	script   string
	thread   string
	attempts []scheduledexec.Launch
}

func (f *scheduledShimFixture) Launch(request scheduledexec.Launch) (string, error) {
	f.attempts = append(f.attempts, request)
	// The script stands in for the provider process while a real Swarm shim
	// provides custody and joined-stop evidence for both worker attempts.
	m, err := f.api.core.Launch(daemon.LaunchSpec{
		AgentType: schedule.RouteClaude, Argv: []string{fakeAgentBin, f.script}, Cwd: request.Workspace,
		ConversationID: f.thread, Cols: 80, Rows: 24,
		OperationID: request.OperationID, OwnerLaunchHash: request.AttemptID,
		Scheduled: &daemon.ScheduledLaunch{AttemptID: request.AttemptID},
	})
	if err != nil {
		return "", err
	}
	return protocol.NamespacedID(f.api.endpointID, m.ID), nil
}

func TestScheduleNativeQuotaFallbackAfterVerifiedShimStop(t *testing.T) {
	buildBinaries(t)
	state, err := os.MkdirTemp("/tmp", "ss-quota-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	cfg := daemon.Config{StateDir: state, SocketPath: filepath.Join(state, "d.sock"), LockPath: filepath.Join(state, "d.lock"),
		LogPath: filepath.Join(state, "d.log"), ShimBinary: swarmBin, MaxSessions: 3}
	core, err := daemon.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	api := &coreAPI{core: core, endpointID: "fixture"}
	t.Cleanup(func() {
		for _, m := range api.core.List() {
			_ = api.core.Kill(m.ID)
		}
		_ = api.core.Close()
	})
	store, err := schedule.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	now := time.Now()
	job, err := store.CreateJob(schedule.JobSpec{
		Name: "Review", Prompt: "Review the fixture", Directory: workspace, Workspace: schedule.WorkspaceDirectory,
		Authority: schedule.AuthorityAgentDefault, Agents: []string{schedule.RouteCodex, schedule.RouteClaude},
		Calendar: schedule.Calendar{Kind: schedule.CalendarDaily, Time: "00:00", TimeZone: "UTC"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(state, "worker.txt")
	if err := os.WriteFile(script, []byte("idle 120s\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f := &scheduledShimFixture{Backend: scheduleBackend{api: api}, api: api, script: script,
		thread: "01910000-0000-7000-8000-000000000001"}
	engine := scheduledexec.New(store, state, f)
	id, err := engine.Run(job.ID, schedule.NewID(now), now)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.Occurrence(id)
	if err != nil || len(run.Attempts) != 1 || run.Attempts[0].SessionID == "" {
		t.Fatalf("first run did not launch: %+v (%v)", run, err)
	}
	attempt := run.Attempts[0]
	_, local, _ := protocol.ParseID(attempt.SessionID)
	m, _ := core.Get(local)
	// Replay the synthetic Codex event through the normal observer against
	// the first shim's authentic persisted process and attempt identities.
	if err := core.Close(); err != nil {
		t.Fatal(err)
	}
	m.AgentType = schedule.RouteCodex
	metadata, err := persist.NewStore(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := metadata.Save(m); err != nil {
		t.Fatal(err)
	}
	reopened, err := daemon.Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	api.core = reopened
	d := &Daemon{core: reopened}
	frame := func(thread string) []byte {
		return []byte(fmt.Sprintf(`{"method":"turn/completed","params":{"threadId":%q,"turn":{"status":"failed","error":{"codexErrorInfo":"usageLimitExceeded"}}}}`, thread))
	}
	d.observeScheduledCodexFailure(local, "turn/completed", frame("foreign-thread"))
	if got, _ := reopened.Get(local); got.ScheduleExhausted {
		t.Fatal("foreign native thread changed scheduling state")
	}
	d.observeScheduledCodexFailure(local, "turn/completed", frame(f.thread))
	if got, _ := reopened.Get(local); !got.ScheduleExhausted {
		t.Fatal("terminal usage limit did not persist")
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(f.attempts) < 2 && time.Now().Before(deadline) {
		if err := engine.Tick(time.Now()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(f.attempts) != 2 || f.attempts[1].Agent != schedule.RouteClaude ||
		f.attempts[1].Workspace != f.attempts[0].Workspace || !reopened.ScheduledExit(local, attempt.ID) {
		t.Fatalf("successor did not start after verified stop in the saved workspace: attempts=%d", len(f.attempts))
	}
}

func TestScheduleClosingMessageOnlyMatchesLatestCompletedAssistant(t *testing.T) {
	row := func(id, kind, state, message string) protocol.JournalRecord {
		return protocol.JournalRecord{Type: "interaction", Item: json.RawMessage(fmt.Sprintf(
			`{"item_id":%q,"kind":%q,"status":%q,"text":%q}`, id, kind, state, message))}
	}
	history := []protocol.JournalRecord{
		row("first", "agent_message", "in_progress", "Build "),
		row("first", "agent_message", "completed", "passed."),
		row("second", "agent_message", "in_progress", "Correction: "),
		row("second", "agent_message", "completed", "Build failed."),
		row("tool", "tool_run", "completed", "Build passed."),
	}
	if closingMessageInHistory(history, "Build passed.") {
		t.Fatal("an earlier assistant message or later tool result satisfied completion")
	}
	if !closingMessageInHistory(history, "Correction: Build failed.") {
		t.Fatal("latest assistant message lost its streamed prefix")
	}
}
