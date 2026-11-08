package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/status"
)

type fakePilotLauncher struct {
	*fakeSpawnClient
	path          string
	operations    []string
	requests      []protocol.LaunchReq
	loseReply     bool
	statusReads   int
	statusOutcome schema.OperationOutcomeView
}

func (f *fakePilotLauncher) LaunchOperationStatus(string) (schema.OperationOutcomeView, error) {
	f.statusReads++
	if f.statusOutcome.State != "" {
		return f.statusOutcome, nil
	}
	return schema.OperationOutcomeView{State: schema.OutcomeUnknown}, nil
}

func TestPilotLaunchDoesNotPersistUnrelatedSecrets(t *testing.T) {
	t.Setenv("AWS_SECRET_ACCESS_KEY", "review-dummy-secret")
	f := &fakePilotLauncher{fakeSpawnClient: newFakeSpawnClient()}
	f.path = enterPilotForTest(t, f)
	if code, _, err := pilotCall(f, "--context", f.path, "create", "--cli", "codex", "--prompt", "exact task"); code != 0 {
		t.Fatal(code, err)
	}
	body, err := os.ReadFile(filepath.Join(f.path, "launch-pending"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte("review-dummy-secret")) {
		t.Fatal("unrelated shell credential persisted")
	}
}

func TestPilotLaunchAppliedStatusSettlesOnlyMatchingReportedPending(t *testing.T) {
	for _, kind := range []string{"applied", "unknown", "different_id", "missing_session", "lost_stdout"} {
		t.Run(kind, func(t *testing.T) {
			f := &fakePilotLauncher{fakeSpawnClient: newFakeSpawnClient(), loseReply: true, statusOutcome: schema.OperationOutcomeView{State: schema.OutcomeApplied, SessionID: "local/deleted-original"}}
			f.path = enterPilotForTest(t, f)
			if code, _, _ := pilotCall(f, "--context", f.path, "create", "--cli", "codex", "--prompt", "first task"); code == 0 {
				t.Fatal("expected lost reply")
			}
			pendingPath := filepath.Join(f.path, "launch-pending")
			body, err := os.ReadFile(pendingPath)
			if err != nil {
				t.Fatal(err)
			}
			var pending pilotLaunchPending
			if err := json.Unmarshal(body, &pending); err != nil {
				t.Fatal(err)
			}
			// Reconciliation remains available after the original mutation expires.
			pending.OperationID = "launch:1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			if err := savePilotRecord(pendingPath, pending); err != nil {
				t.Fatal(err)
			}
			id := pending.OperationID
			switch kind {
			case "unknown":
				f.statusOutcome.State = schema.OutcomeUnknown
			case "different_id":
				id = "another-operation"
			case "missing_session":
				f.statusOutcome.SessionID = ""
			}
			args := []string{"--context", f.path, "launch-status", id}
			if kind == "lost_stdout" {
				var stderr bytes.Buffer
				output := cleanTestWriter(func([]byte) (int, error) { return 0, errors.New("lost stdout") })
				if code := runPilot(args, f, output, &stderr); code == 0 {
					t.Fatal("lost status output reported success")
				}
			} else if code, _, err := pilotCall(f, args...); code != 0 {
				t.Fatal(code, err)
			}
			code, _, errText := pilotCall(f, "--context", f.path, "create", "--cli", "codex", "--prompt", "second task")
			if kind == "applied" {
				if code != 0 {
					t.Fatalf("applied status still blocks: %s", errText)
				}
			} else if code == 0 || len(f.operations) != 1 {
				t.Fatal("unmatched or unreported status cleared guard")
			}
		})
	}
}

func (f *fakePilotLauncher) LaunchOperation(id string, req protocol.LaunchReq) (string, string, error) {
	body, err := os.ReadFile(filepath.Join(f.path, "launch-pending"))
	if err != nil {
		return "", "", errors.New("request not durable before dispatch")
	}
	var pending pilotLaunchPending
	if json.Unmarshal(body, &pending) != nil || pending.OperationID != id || !reflect.DeepEqual(pending.Request, req) {
		return "", "", errors.New("saved request differs from dispatch")
	}
	f.operations = append(f.operations, id)
	f.requests = append(f.requests, req)
	if f.loseReply {
		f.loseReply = false
		return "", "", errors.New("lost reply")
	}
	return f.id, f.name, nil
}

func TestPilotLaunchPersistsAndReusesExactLostReply(t *testing.T) {
	for _, kind := range []string{"create", "resume"} {
		t.Run(kind, func(t *testing.T) {
			f := &fakePilotLauncher{fakeSpawnClient: newFakeSpawnClient(), loseReply: true}
			f.sessions = []protocol.SessionView{view("local/old", "codex", "old", status.GroupCompleted)}
			f.path = enterPilotForTest(t, f)
			args := []string{"--context", f.path, "create", "--cli", "codex", "--prompt", "exact task"}
			if kind == "resume" {
				args = []string{"--context", f.path, "resume", "local/old"}
			}
			if code, _, err := pilotCall(f, args...); code == 0 || !strings.Contains(err, "lost reply") {
				t.Fatalf("first call: %d %s", code, err)
			}
			if code, _, _ := pilotCall(f, "--context", f.path, "create", "--cli", "codex", "--prompt", "different"); code == 0 || len(f.operations) != 1 {
				t.Fatal("different work overtook unresolved launch")
			}
			f.sessions = nil // Reconcile resume before consulting its mutable/deleted source.
			if code, _, err := pilotCall(f, args...); code != 0 {
				t.Fatalf("exact retry: %d %s", code, err)
			}
			if len(f.operations) != 2 || f.operations[0] != f.operations[1] || !reflect.DeepEqual(f.requests[0], f.requests[1]) || len(f.reqs()) != 0 {
				t.Fatal("lost receipt changed operation/request or fell back to legacy launch")
			}
		})
	}
}

func TestPilotCreateRefusesLegacyDaemon(t *testing.T) {
	f := newFakeSpawnClient()
	path := enterPilotForTest(t, f)
	if code, _, _ := pilotCall(f, "--context", path, "create", "--cli", "codex", "--prompt", "exact"); code == 0 || len(f.reqs()) != 0 {
		t.Fatal("legacy launch fallback executed")
	}
}

func TestPilotLaunchLostOutputKeepsGuardAndStatusDoesNotDispatch(t *testing.T) {
	f := &fakePilotLauncher{fakeSpawnClient: newFakeSpawnClient()}
	f.path = enterPilotForTest(t, f)
	args := []string{"--context", f.path, "create", "--cli", "codex", "--prompt", "exact task"}
	var stderr bytes.Buffer
	output := cleanTestWriter(func([]byte) (int, error) { return 0, errors.New("lost stdout") })
	if code := runPilot(args, f, output, &stderr); code == 0 {
		t.Fatal("lost output reported success")
	}
	if code, _, err := pilotCall(f, args...); code != 0 {
		t.Fatalf("retry after output loss = %d %s", code, err)
	}
	if len(f.operations) != 2 || f.operations[0] != f.operations[1] {
		t.Fatal("lost stdout cleared original launch guard")
	}
	if code, body, err := pilotCall(f, "--context", f.path, "launch-status", f.operations[0]); code != 0 || !strings.Contains(body, schema.OutcomeUnknown) || f.statusReads != 1 || len(f.operations) != 2 {
		t.Fatalf("status dispatched or failed: %d %s %s", code, body, err)
	}
}
