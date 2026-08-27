package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
)

const (
	testClaudeLiveID    = "2d276424-44e3-4efc-b351-a7035b5ca501"
	testClaudeSettledID = "4a7a2465-d8f0-4c05-a7a9-c44d8077b22b"
)

type fakeClaudeSource struct {
	sessions []claudeAgentSession
	listErr  error
	stopErr  error
	stops    int
	events   *[]string
}

func (f *fakeClaudeSource) List(bool) ([]claudeAgentSession, error) {
	return append([]claudeAgentSession(nil), f.sessions...), f.listErr
}

func (f *fakeClaudeSource) Stop() error {
	f.stops++
	if f.events != nil {
		*f.events = append(*f.events, "stop")
	}
	return f.stopErr
}

type fakeReattachClient struct {
	*fakeAgentClient
	launches  []protocol.LaunchReq
	launchErr error
	events    *[]string
}

func newFakeReattachClient() *fakeReattachClient {
	return &fakeReattachClient{fakeAgentClient: newFakeAgentClient()}
}

func (f *fakeReattachClient) Capabilities() []string {
	return []string{protocol.CapExternalResume}
}

func (f *fakeReattachClient) Launch(req protocol.LaunchReq) (string, string, error) {
	if f.events != nil {
		*f.events = append(*f.events, "launch")
	}
	f.launches = append(f.launches, req)
	if f.launchErr != nil {
		return "", "", f.launchErr
	}
	return "swarm-" + req.Options[protocol.OptionResumeConversationID][:8], req.Name, nil
}

func claudeSession(t *testing.T, id, state string) claudeAgentSession {
	t.Helper()
	return claudeAgentSession{
		ID:        id[:8],
		SessionID: id,
		Name:      "session " + id[:8],
		Cwd:       t.TempDir(),
		Kind:      "background",
		State:     state,
	}
}

func TestRunReattachRefusesDaemonWithoutExternalResumeCapability(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runReattach([]string{"--cli", "claude"}, newFakeAgentClient(), &stdout, &stderr); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "restart it with this CLI version") {
		t.Fatalf("stderr = %q; want version-safe capability refusal", stderr.String())
	}
}

func TestClaudeReattachRequiresExplicitTakeoverBeforeLiveLaunch(t *testing.T) {
	source := &fakeClaudeSource{sessions: []claudeAgentSession{claudeSession(t, testClaudeLiveID, "blocked")}}
	client := newFakeReattachClient()
	var stdout, stderr bytes.Buffer
	if code := runClaudeReattach(false, false, false, source, client, &stdout, &stderr); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if source.stops != 0 || len(client.launches) != 0 {
		t.Fatalf("refusal caused side effects: stops=%d launches=%d", source.stops, len(client.launches))
	}
	if !strings.Contains(stderr.String(), "--take-over") {
		t.Fatalf("stderr = %q; want takeover guidance", stderr.String())
	}
}

func TestClaudeReattachStopsSourceBeforeLaunchingLiveSessions(t *testing.T) {
	var events []string
	source := &fakeClaudeSource{
		sessions: []claudeAgentSession{claudeSession(t, testClaudeLiveID, "idle")},
		events:   &events,
	}
	client := newFakeReattachClient()
	client.events = &events
	var stdout, stderr bytes.Buffer
	if code := runClaudeReattach(false, true, false, source, client, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if got := strings.Join(events, ","); got != "stop,launch" {
		t.Fatalf("events = %q, want stop,launch", got)
	}
	if len(client.launches) != 1 {
		t.Fatalf("launches = %d, want 1", len(client.launches))
	}
	req := client.launches[0]
	if req.Agent != "claude" || req.Options[protocol.OptionResumeConversationID] != testClaudeLiveID {
		t.Fatalf("launch request = %#v; want Claude external resume", req)
	}
	if req.InitialPrompt != "" || req.Cols != 80 || req.Rows != 24 {
		t.Fatalf("launch request prompt/dimensions = %#v", req)
	}
}

func TestClaudeReattachAllIncludesSettledAndFiltersInteractiveAndDuplicates(t *testing.T) {
	settled := claudeSession(t, testClaudeSettledID, "done")
	interactive := claudeSession(t, testClaudeLiveID, "idle")
	interactive.Kind = "interactive"
	source := &fakeClaudeSource{sessions: []claudeAgentSession{settled, interactive, settled}}
	client := newFakeReattachClient()
	var stdout, stderr bytes.Buffer
	if code := runClaudeReattach(true, false, false, source, client, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if source.stops != 0 || len(client.launches) != 1 {
		t.Fatalf("stops=%d launches=%d; want one settled background launch", source.stops, len(client.launches))
	}
}

func TestClaudeReattachWithoutAllSkipsSettledSessions(t *testing.T) {
	source := &fakeClaudeSource{sessions: []claudeAgentSession{claudeSession(t, testClaudeSettledID, "stopped")}}
	client := newFakeReattachClient()
	var stdout, stderr bytes.Buffer
	if code := runClaudeReattach(false, false, false, source, client, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if len(client.launches) != 0 {
		t.Fatalf("settled session launched without --all: %#v", client.launches)
	}
}

func TestClaudeReattachDryRunHasNoSideEffects(t *testing.T) {
	source := &fakeClaudeSource{sessions: []claudeAgentSession{claudeSession(t, testClaudeLiveID, "busy")}}
	client := newFakeReattachClient()
	var stdout, stderr bytes.Buffer
	if code := runClaudeReattach(false, false, true, source, client, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if source.stops != 0 || len(client.launches) != 0 {
		t.Fatalf("dry run caused side effects: stops=%d launches=%d", source.stops, len(client.launches))
	}
	if !strings.Contains(stdout.String(), testClaudeLiveID) {
		t.Fatalf("stdout = %q; want native session id", stdout.String())
	}
}

func TestClaudeReattachReportsLaunchFailure(t *testing.T) {
	source := &fakeClaudeSource{sessions: []claudeAgentSession{claudeSession(t, testClaudeSettledID, "done")}}
	client := newFakeReattachClient()
	client.launchErr = errors.New("daemon refused")
	var stdout, stderr bytes.Buffer
	if code := runClaudeReattach(true, false, false, source, client, &stdout, &stderr); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "daemon refused") {
		t.Fatalf("stderr = %q; want launch failure", stderr.String())
	}
}
