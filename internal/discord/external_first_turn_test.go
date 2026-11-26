package discord

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

type firstTurnCheckout struct {
	*teammateTestManager
	status TeammateNativeStatus
	err    error
	reads  int
}

func (m *firstTurnCheckout) CheckoutStatus(TeammateDiscussion) (TeammateNativeStatus, error) {
	m.reads++
	return m.status, m.err
}

func TestExternalWorkerFirstTurnReceiptIsOptInAndEventScoped(t *testing.T) {
	bridge, manager := teammateTestConnector(t, nil)
	store, install := bridge.service.Store, bridge.service.Config.InstallationID
	bridge.external = externalInputScope{GuildID: "10", ChannelID: "20", WebhookID: "89", Project: t.TempDir()}
	input := externalInput{
		EventID: "request", MessageID: "201", ContentSHA256: strings.Repeat("a", 64), Text: "Generate a report",
		ExpiresAt: time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano),
	}
	externalTestRequest(t, bridge, http.MethodPost, "", externalTestBearer, input, http.StatusOK)
	rows, err := store.teammatePending(install)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	candidate := rows[0]
	discussion := TeammateDiscussion{
		ID: "discussion", InstallationID: install, Route: Route{GuildID: "10", ChannelID: "201", ParentID: "20"},
		Starter: candidate.Route, FixedProject: bridge.external.Project,
	}
	if err := store.teammateBind(teammateBinding{TeammateDiscussion: discussion}); err != nil {
		t.Fatal(err)
	}
	operation := externalInputOperation(install, input.EventID)
	admission := TeammateAdmission{
		Discussion: discussion, OperationID: operation, Revision: digest(input), Route: candidate.Route, Author: candidate.Author,
	}
	if err := store.teammateCandidateChange(install, candidate, StateSending, "", &admission); err != nil {
		t.Fatal(err)
	}
	candidate.State, candidate.Admission = StateSending, &admission
	if err := store.teammateCandidateChange(install, candidate, StateSent, "", nil); err != nil {
		t.Fatal(err)
	}
	first := schema.WorkerFirstTurnReceipt{
		AdmissionID: operation, ManagerSession: "endpoint/original", ManagerInstance: "original-instance",
		WorkerSession: "endpoint/worker", WorkerInstance: "worker-instance", JobID: "launch",
		AttemptID: "launch", ActionID: "launch", NativeTurnID: "verified-turn",
		Origin: schema.MessageOrigin{Kind: string(AuthorityExternal), RequestID: operation,
			InstallationID: install, DiscussionID: discussion.ID, GuildID: discussion.Route.GuildID,
			ChannelID: discussion.Route.ChannelID, MessageID: input.MessageID, Revision: admission.Revision,
			ActorID: candidate.Author.ID, RequesterID: candidate.Author.ID, ActorName: "Private source title"},
	}
	wrongOrigin := first
	wrongOrigin.Origin.Revision = "wrong"
	wrongOrigin.NativeTurnID = "unrelated-turn"
	checkout := &firstTurnCheckout{teammateTestManager: manager, status: TeammateNativeStatus{WorkerFirstTurns: []schema.WorkerFirstTurnReceipt{wrongOrigin, first}}}
	bridge.manager = checkout

	read := func(include, bearer string, status int) []schema.WorkerFirstTurnReceipt {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/admissions?event_id=request"+include, nil)
		req.Header.Set("Authorization", bearer)
		res := httptest.NewRecorder()
		bridge.externalHandler(externalTestBearer, res, req)
		if res.Code != status {
			t.Fatalf("receipt status %d: %s", res.Code, res.Body.String())
		}
		if status != http.StatusOK {
			return nil
		}
		var receipt struct {
			externalReceipt
			WorkerFirstTurns []schema.WorkerFirstTurnReceipt `json:"worker_first_turns"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &receipt); err != nil || receipt.State != externalAccepted || receipt.DiscussionID != discussion.ID {
			t.Fatal(receipt, err)
		}
		if strings.Contains(res.Body.String(), first.Origin.ActorName) {
			t.Fatal("wire receipt leaked source metadata")
		}
		return receipt.WorkerFirstTurns
	}

	if turns := read("", externalTestBearer, http.StatusOK); len(turns) != 0 || checkout.reads != 0 {
		t.Fatal(turns, checkout.reads)
	}
	read("&include=worker_first_turns", "Bearer wrong", http.StatusForbidden)
	if checkout.reads != 0 {
		t.Fatal("unauthorized checkout read")
	}
	turns := read("&include=worker_first_turns", externalTestBearer, http.StatusOK)
	if len(turns) != 1 || turns[0].NativeTurnID != first.NativeTurnID || turns[0].AdmissionID != first.AdmissionID ||
		turns[0].ActionID != first.ActionID || turns[0].WorkerInstance != first.WorkerInstance || checkout.reads != 1 {
		t.Fatal(turns, checkout.reads)
	}
	checkout.err = errors.New("checkout unavailable")
	if turns = read("&include=worker_first_turns", externalTestBearer, http.StatusOK); len(turns) != 0 {
		t.Fatalf("unavailable proof reader returned evidence: %v", turns)
	}
}
