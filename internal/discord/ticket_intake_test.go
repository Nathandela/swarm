package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func TestTicketIngressRequiresBearerAndSurvivesStoreRestart(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	enrollment, event := ticketFixture()
	service := NewService(Config{Mode: "teammate", ManagerCLI: "claude", InstallationID: "install", TicketIntake: &enrollment}, store, nil, nil)
	service.ticketBearer = "Bearer private test relay authentication"
	req := func(method, target, auth string, event ticketEvent) *httptest.ResponseRecorder {
		t.Helper()
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(method, target, bytes.NewReader(data))
		request.Header.Set("Authorization", auth)
		response := httptest.NewRecorder()
		service.localHandler(response, request)
		return response
	}
	const endpoint = "/ticket-admission"
	reply := req(http.MethodPost, endpoint, "Bearer wrong", event)
	if reply.Code != http.StatusForbidden {
		t.Fatalf("unauthenticated input: %d %s", reply.Code, reply.Body)
	}
	bearer := service.ticketBearer
	if reply = req(http.MethodPost, endpoint, bearer, event); reply.Code != http.StatusOK {
		t.Fatalf("first claim: %d %s", reply.Code, reply.Body)
	}
	if pending, err := store.teammatePending("install"); err != nil || len(pending) != 1 {
		t.Fatalf("expected one queued input: %v, %v", pending, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	service.Store = store
	enrollment.ManagerCLI = "codex"
	duplicate := req(http.MethodPost, endpoint, bearer, event)
	status := req(http.MethodGet, endpoint+"?event_id="+event.EventID, bearer, ticketEvent{})
	if duplicate.Code != http.StatusOK || status.Code != http.StatusOK ||
		duplicate.Body.String() != reply.Body.String() || status.Body.String() != reply.Body.String() {
		t.Fatalf("restart lost receipt: first %s duplicate %s status %s", reply.Body, duplicate.Body, status.Body)
	}
	pinned, err := store.getTicket("install", event.EventID)
	if err != nil || pinned.ManagerCLI != "claude" {
		t.Fatalf("replay changed the original provider: %+v %v", pinned, err)
	}
	pending, err := store.teammatePending("install")
	if err != nil || len(pending) != 1 {
		t.Fatalf("queued receipt lost its pending admission: %v %v", pending, err)
	}
	bridge := teammateConnector{service: service}
	if err := bridge.process(context.Background(), pending[0]); err != nil {
		t.Fatal(err)
	}
	if rejected, err := store.getTicket("install", event.EventID); err != nil || rejected.Receipt.State != ticketBlocked {
		t.Fatalf("provider mismatch after restart was not blocked before remote effects: %+v %v", rejected, err)
	}
	event.Summary = "altered summary"
	if reply := req(http.MethodPost, endpoint, bearer, event); reply.Code != http.StatusConflict {
		t.Fatalf("changed replay: %d %s", reply.Code, reply.Body)
	}
	event.ChannelID = "88"
	if reply := req(http.MethodPost, endpoint, bearer, event); reply.Code != http.StatusForbidden {
		t.Fatalf("wrong channel: %d %s", reply.Code, reply.Body)
	}
}

func TestTicketDispatchRecoversThreadAndNeverAdmitsColdHuman(t *testing.T) {
	enrollment, event := ticketFixture()
	enrollment.ManagerCLI = "codex"
	opener := teammateTestMessage("201", "20", "87", "Ticket\n"+event.AnnouncementMarker, false)
	opener.Author.Bot, opener.WebhookID = true, enrollment.WebhookID
	early := teammateTestMessage("202", "201", "77", "<@99> runs first", true)
	follow := teammateTestMessage("203", "201", "88", "Please check the regression", false)
	var threadCreates, publicationPosts int
	bridge, manager := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/20":
			_, _ = fmt.Fprint(w, teammateParent)
		case "/channels/20/permissions/member/99":
			teammateTestPermissions(w)
		case "/channels/20/messages/201":
			_ = json.NewEncoder(w).Encode(opener)
		case "/channels/20/messages/201/threads":
			threadCreates++
			w.WriteHeader(http.StatusInternalServerError)
		case "/channels/201":
			if threadCreates == 0 {
				w.WriteHeader(http.StatusNotFound)
			} else {
				_, _ = fmt.Fprint(w, teammatePublic)
			}
		case "/channels/201/messages/202":
			_ = json.NewEncoder(w).Encode(early)
		case "/channels/201/messages/203":
			_ = json.NewEncoder(w).Encode(follow)
		case "/channels/201/messages":
			publicationPosts++
			_, _ = fmt.Fprint(w, `{"id":"209","channel_id":"201","content":"draft ready","author":{"id":"99","bot":true}}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	ctx := context.Background()
	grantStatus := configureTicketGrantFixture(t, bridge, &enrollment, event, http.StatusNoContent)
	store := bridge.service.Store
	install := bridge.service.Config.InstallationID
	teammateTestEvent(t, bridge, 1, "MESSAGE_CREATE", opener)
	if pending, err := store.teammatePending(install); err != nil || len(pending) != 0 {
		t.Fatalf("webhook bypassed trusted admission: %v, %v", pending, err)
	}
	if _, err := store.queueTicket(install, event, bridge.service.Config.EffectiveTicketManagerCLI()); err != nil {
		t.Fatal(err)
	}
	process := func(nextState string) ticketRecord {
		t.Helper()
		pending, err := bridge.service.Store.teammatePending(install)
		if err != nil || len(pending) != 1 {
			t.Fatalf("expected one pending admission: %v, %v", pending, err)
		}
		if err = bridge.process(ctx, pending[0]); err != nil {
			t.Fatal(err)
		}
		record, err := bridge.service.Store.getTicket(install, event.EventID)
		if err != nil || record.Receipt.State != nextState {
			t.Fatalf("receipt: %+v, %v", record.Receipt, err)
		}
		return record
	}
	manager.transition = TeammateAdmissionAccepted
	if record := process(ticketAwaitingNative); record.Receipt.ThreadID != "201" || threadCreates != 1 || len(manager.admitted) != 1 {
		t.Fatalf("thread acknowledgement: %+v creates=%d admissions=%d", record, threadCreates, len(manager.admitted))
	}
	original := manager.admitted[0]
	if original.Authority != AuthorityTicketAssignment || original.Discussion.TicketEventID != event.EventID ||
		original.Discussion.TicketProject != enrollment.Project || original.Discussion.TicketSupervisor != enrollment.Supervisor ||
		original.Discussion.SupervisorSocket != enrollment.Supervisor.Socket || original.Discussion.TicketManagerCLI != "codex" {
		t.Fatalf("machine provenance missing: %+v", original)
	}
	if _, _, err := bridge.resolve(ctx, teammateCandidate{
		Route: Route{GuildID: "10", ChannelID: "201", MessageID: early.ID}, Author: Actor{ID: early.Author.ID}, Opening: true,
	}); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("cold human mention accepted: %v", err)
	}
	q := TeammateHistoryQuery{Source: "admission", AdmissionID: original.OperationID, Revision: original.Revision, MessageID: original.Route.MessageID, AuthorID: original.Author.ID}
	if _, err := bridge.history(original.Discussion)(ctx, q); err != nil {
		t.Fatalf("legitimate provenance: %v", err)
	}
	q.Source = "grant"
	grantStatus.Store(http.StatusConflict)
	if _, err := bridge.history(original.Discussion)(ctx, q); !errors.Is(err, ErrTeammateSourceChanged) {
		t.Fatalf("revoked grant accepted: %v", err)
	}
	grantStatus.Store(http.StatusServiceUnavailable)
	if _, err := bridge.history(original.Discussion)(ctx, q); err == nil || errors.Is(err, ErrTeammateSourceChanged) {
		t.Fatalf("unavailable grant did not fail closed and retryably: %v", err)
	}
	grantStatus.Store(http.StatusNoContent)
	if _, err := bridge.history(original.Discussion)(ctx, q); err != nil {
		t.Fatalf("eligible grant did not recover: %v", err)
	}
	q.Revision = "forged"
	if _, err := bridge.history(original.Discussion)(ctx, q); !errors.Is(err, ErrTeammateSourceChanged) {
		t.Fatalf("forged provenance: %v", err)
	}
	if _, err := bridge.history(original.Discussion)(ctx, TeammateHistoryQuery{Source: "parent"}); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("ticket can read neighboring parent messages: %v", err)
	}
	dir := filepath.Dir(store.db.Path())
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	bridge.service.Store = reopened
	manager.phase = TeammateAdmissionCutover
	record := process(ticketNativeCutover)
	if record.Receipt.DiscussionID != original.Discussion.ID || threadCreates != 1 || len(manager.admitted) != 1 {
		t.Fatalf("duplicate effect after restart: %+v creates=%d admissions=%d", record, threadCreates, len(manager.admitted))
	}
	teammateTestEvent(t, bridge, 2, "MESSAGE_CREATE", follow)
	process(ticketNativeCutover)
	if len(manager.admitted) != 2 || manager.admitted[1].Author.ID != follow.Author.ID || manager.admitted[1].Discussion.ID != original.Discussion.ID {
		t.Fatalf("human follow-up lost: %+v", manager.admitted)
	}
	p := TeammatePublication{ID: "draft-receipt", AdmissionID: original.OperationID, Body: "draft ready"}
	if err := reopened.teammateQueueOutput(original.Discussion, p, time.Now()); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := bridge.sendPending(ctx, original.Discussion); err != nil {
			t.Fatal(err)
		}
	}
	if publicationPosts != 1 {
		t.Fatalf("duplicate or missing publication to the original thread: %d", publicationPosts)
	}
	checkout := &ticketCheckoutFixture{teammateTestManager: manager, err: ErrConflict}
	bridge.service.teammateManager = checkout
	read := func() []byte {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/ticket-admission?event_id="+event.EventID, nil)
		req.Header.Set("Authorization", bridge.service.ticketBearer)
		out := httptest.NewRecorder()
		bridge.service.localHandler(out, req)
		if out.Code != http.StatusOK {
			t.Fatalf("completion lookup affected intake status: %d %s", out.Code, out.Body)
		}
		return out.Body.Bytes()
	}
	if raw := read(); bytes.Contains(raw, []byte("completion")) || !bytes.Contains(raw, []byte(ticketNativeCutover)) {
		t.Fatalf("checkout error corrupted the existing receipt: %s", raw)
	}
	checkout.err = nil
	checkout.proof = &schema.TicketWorkerCompletion{AdmissionID: "foreign", WorkerSession: "place/worker", WorkerInstance: "instance"}
	if raw := read(); bytes.Contains(raw, []byte("completion")) {
		t.Fatalf("cross-ticket proof leaked: %s", raw)
	}
	checkout.proof.AdmissionID = original.OperationID
	if raw := read(); !bytes.Contains(raw, []byte(`"worker_session":"place/worker"`)) {
		t.Fatalf("exact completion was omitted: %s", raw)
	}
}

type ticketCheckoutFixture struct {
	*teammateTestManager
	proof *schema.TicketWorkerCompletion
	err   error
}

func (m *ticketCheckoutFixture) CheckoutStatus(TeammateDiscussion) (TeammateNativeStatus, error) {
	return TeammateNativeStatus{TicketCompletion: m.proof}, m.err
}

func ticketFixture() (TicketIntake, ticketEvent) {
	intake := TicketIntake{GuildID: "10", ChannelID: "20", WebhookID: "89", Repo: "Quanthome/fii", Project: "fii", GrantSocket: "/tmp/relay.sock",
		Supervisor: TicketSupervisor{StateDir: "/tmp/tickets", Socket: "/tmp/tickets/daemon.sock", Endpoint: "tickets"},
	}
	event := ticketEvent{EventID: "event-1", AssignmentID: "assignment-1", TicketID: "ticket-1",
		GuildID: "10", ChannelID: "20", AnnouncementMessageID: "201", AnnouncementMarker: "delivery-marker-example", Repo: "Quanthome/fii", Summary: "Synthetic staging ticket."}
	return intake, event
}

func TestTicketRejectsUnverifiedAnnouncementBeforeAnyEffect(t *testing.T) {
	enrollment, event := ticketFixture()
	for _, test := range []struct {
		name, webhookID, marker string
		grantStatus             int32
	}{
		{"wrong webhook", "88", event.AnnouncementMarker, http.StatusNoContent},
		{"partial marker", enrollment.WebhookID, "copied " + event.AnnouncementMarker, http.StatusNoContent},
		{"revoked assignment", enrollment.WebhookID, event.AnnouncementMarker, http.StatusConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			webhookID, marker := test.webhookID, test.marker
			bridge, manager := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/channels/20":
					_, _ = fmt.Fprint(w, teammateParent)
				case "/channels/20/permissions/member/99":
					teammateTestPermissions(w)
				case "/channels/20/messages/201":
					forged := teammateTestMessage(event.AnnouncementMessageID, event.ChannelID, "88", marker, false)
					forged.Author.Bot, forged.WebhookID = true, webhookID
					_ = json.NewEncoder(w).Encode(forged)
				default:
					t.Errorf("unverified ticket caused unexpected %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			})
			bridge.service.Config.ManagerCLI = "claude"
			configureTicketGrantFixture(t, bridge, &enrollment, event, test.grantStatus)
			install := bridge.service.Config.InstallationID
			store := bridge.service.Store
			if _, err := store.queueTicket(install, event, bridge.service.Config.EffectiveTicketManagerCLI()); err != nil {
				t.Fatal(err)
			}
			duplicateAssignment := event
			duplicateAssignment.EventID, duplicateAssignment.AnnouncementMessageID = "new-event", "210"
			if _, err := store.queueTicket(install, duplicateAssignment, bridge.service.Config.EffectiveTicketManagerCLI()); !errors.Is(err, ErrConflict) {
				t.Fatalf("one assignment got another event or thread: %v", err)
			}
			pending, err := store.teammatePending(install)
			if err != nil || len(pending) != 1 {
				t.Fatalf("pending: %+v %v", pending, err)
			}
			if err = bridge.process(context.Background(), pending[0]); err != nil {
				t.Fatal(err)
			}
			record, err := store.getTicket(install, event.EventID)
			if err != nil || record.Receipt.State != ticketBlocked || record.Receipt.DiscussionID != "" || len(manager.admitted) != 0 {
				t.Fatalf("unverified webhook acquired authority: %+v, %v", record, err)
			}
		})
	}
}

func TestTicketIntakeRequiresDedicatedSupervisor(t *testing.T) {
	enrollment, _ := ticketFixture()
	enrollment.TokenFile = "/tmp/relay"
	cfg := Config{
		Version: 1, Name: "test", InstallationID: "install", Mode: "teammate", ManagerCLI: "codex",
		GuildIDs: []string{"10"}, ChannelIDs: []string{"20"}, ApplicationID: "99", TicketIntake: &enrollment,
		TokenFile: "/tmp/discord", DaemonSocket: "/tmp/swarm.sock", WorkDir: "/tmp/fii", Projects: map[string]string{"fii": "/tmp/fii"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.EffectiveTicketManagerCLI() != cfg.ManagerCLI {
		t.Fatal("ticket lane did not inherit the configured manager")
	}
	enrollment.ManagerCLI = "claude"
	if err := cfg.Validate(); err != nil || cfg.EffectiveTicketManagerCLI() != "claude" || cfg.ManagerCLI != "codex" {
		t.Fatalf("ticket override changed the shared manager: %v", err)
	}
	enrollment.ManagerCLI = "opencode"
	if err := cfg.Validate(); err == nil {
		t.Fatal("ticket lane accepted an unsupported manager")
	}
	enrollment.ManagerCLI = ""
	cfg.ProjectAccess = ProjectAccessVM
	if err := cfg.Validate(); err != nil {
		t.Fatalf("dedicated tickets changed regular VM discussions: %v", err)
	}
	enrollment.Supervisor.Socket = cfg.DaemonSocket
	if err := cfg.Validate(); err == nil {
		t.Fatal("ticket intake accepted the shared supervisor socket")
	}
	cfg.TicketIntake = nil
	if err := cfg.Validate(); err != nil {
		t.Fatalf("ordinary VM mode was changed: %v", err)
	}
}

func configureTicketGrantFixture(t *testing.T, bridge *teammateConnector, enrollment *TicketIntake, event ticketEvent, code int32) *atomic.Int32 {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ticket-grant-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	specified := *enrollment
	specified.GrantSocket = filepath.Join(dir, "g.sock")
	listener, err := net.Listen("unix", specified.GrantSocket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := os.Chmod(specified.GrantSocket, 0600); err != nil {
		t.Fatal(err)
	}
	const token = "Bearer fixture-relay-secret"
	bridge.service.ticketBearer = token
	bridge.service.Config.TicketIntake = &specified
	var status atomic.Int32
	status.Store(code)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data map[string]string
		if r.Method != http.MethodPost || r.URL.Path != "/ticket-grant" || r.Header.Get("Authorization") != token ||
			json.NewDecoder(r.Body).Decode(&data) != nil || data["event_id"] != event.EventID ||
			data["assignment_id"] != event.AssignmentID || data["ticket_id"] != event.TicketID ||
			data["stage"] != ticketStageNativeAdmission && data["stage"] != ticketStageWorkerGrant {
			t.Error("incorrect grant verification request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(int(status.Load()))
	})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() { _ = server.Close(); <-done })
	return &status
}
