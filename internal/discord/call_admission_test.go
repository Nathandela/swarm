package discord

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func TestCallAdmissionPinsOriginalPersonalDiscussionAndReplays(t *testing.T) {
	t.Run("pinned DM", func(t *testing.T) {
		conn, manager := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/channels/20" {
				t.Errorf("voice admitted through unexpected Discord request %s %s", r.Method, r.URL.Path)
				http.NotFound(w, r)
				return
			}
			_, _ = fmt.Fprint(w, personalFixture)
		})
		cfg := &conn.service.Config
		cfg.Mode, cfg.OwnerID, cfg.DMChannelID, cfg.DaemonSocket = "personal", "30", "20", "/current/supervisor.sock"
		cfg.PreviousSupervisor = &PreviousSupervisor{Socket: "/historic/supervisor.sock"}
		cfg.Calls = &DispatcherCallsConfig{Mode: "personal", GuildID: "10", RoomID: "21", DiscussionID: "20"}
		bound := TeammateDiscussion{ID: "dt_" + digest([]string{cfg.InstallationID, "", "20"})[:32], InstallationID: cfg.InstallationID, Route: Route{ChannelID: "20"}}
		if err := conn.service.Store.teammateBind(teammateBinding{TeammateDiscussion: bound, Opened: true}); err != nil {
			t.Fatal(err)
		}
		live := true
		conn.verifyCallTurn = func(_ context.Context, d TeammateDiscussion, v schema.VoiceOrigin, current bool) error {
			if !live || !current || d.ID != bound.ID || v.SpeakerID != "30" || v.Generation != 1 {
				return ErrNotAuthorized
			}
			return nil
		}
		resolved, err := conn.callDiscussion(t.Context(), "", "20")
		if err != nil || resolved != bound {
			t.Fatalf("resolve = %#v, %v", resolved, err)
		}
		speaker := Actor{ID: "30", Name: "Owner"}
		op, err := conn.submitCallTurn(t.Context(), resolved, "call", "turn", 1, speaker, "check the marker")
		if err != nil || op == "" || len(manager.admitted) != 1 {
			t.Fatalf("submit = %q, %v, admissions %#v", op, err, manager.admitted)
		}
		a := manager.admitted[0]
		if a.Discussion != bound || a.Route.MessageID != "" || a.Authority != AuthorityOwnerDirect ||
			a.Voice.SupervisorSocket != cfg.PreviousSupervisor.Socket || a.Voice.BodyHash != digest(a.Text) {
			t.Fatalf("admission = %#v", a)
		}
		live = false
		reopened := &teammateConnector{service: conn.service, client: conn.client, manager: manager, verifyCallTurn: conn.verifyCallTurn}
		replay, err := reopened.submitCallTurn(t.Context(), resolved, "call", "turn", 1, speaker, "check the marker")
		if err != nil || replay != op || len(manager.admitted) != 1 {
			t.Fatalf("replay = %q, %v, admissions %d", replay, err, len(manager.admitted))
		}
		if _, err := reopened.submitCallTurn(t.Context(), resolved, "call", "turn", 1, speaker, "changed"); !errors.Is(err, ErrConflict) {
			t.Fatalf("changed transcript replay = %v", err)
		}
		if _, err := reopened.submitCallTurn(t.Context(), resolved, "call", "turn", 2, speaker, "check the marker"); !errors.Is(err, ErrConflict) {
			t.Fatalf("changed generation replay = %v", err)
		}
		q := TeammateHistoryQuery{Source: "admission", AdmissionID: op, Revision: a.Revision, AuthorID: speaker.ID, Voice: a.Voice, Authority: a.Authority}
		if _, err := reopened.history(bound)(t.Context(), q); !errors.Is(err, ErrTeammateSourceChanged) {
			t.Fatalf("ended generation could admit = %v", err)
		}
		cfg.Calls = nil
		q.Source = "grant"
		if _, err := reopened.history(bound)(t.Context(), q); err != nil {
			t.Fatalf("disabled calls lost written continuation grant = %v", err)
		}
		page, err := conn.service.Store.teammatePending(cfg.InstallationID)
		if err != nil || len(page) != 0 {
			t.Fatalf("voice entered text spool: %v %v", page, err)
		}
	})
}

func TestCallSpeakerChecksLiveTeamAudience(t *testing.T) {
	var revoked atomic.Bool
	var archived atomic.Bool
	conn, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/20":
			_, _ = fmt.Fprint(w, teammateParent)
		case "/channels/201":
			_, _ = fmt.Fprintf(w, `{"id":"201","guild_id":"10","parent_id":"20","type":12,"thread_metadata":{"archived":%t,"locked":false}}`, archived.Load())
		case "/channels/20/permissions/member/99", "/channels/20/permissions/member/30":
			teammateTestPermissions(w)
		case "/channels/201/thread-members/99":
			_, _ = fmt.Fprint(w, `{"id":"201","user_id":"99"}`)
		case "/channels/201/thread-members/30":
			if revoked.Load() {
				http.NotFound(w, r)
			} else {
				_, _ = fmt.Fprint(w, `{"id":"201","user_id":"30"}`)
			}
		default:
			t.Errorf("unexpected Discord request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	conn.service.Config.Calls = &DispatcherCallsConfig{Mode: "team", GuildID: "10", RoomID: "21", DiscussionID: "201", UserIDs: []string{"30"}}
	bound := TeammateDiscussion{InstallationID: "install", Route: Route{GuildID: "10", ChannelID: "201", ParentID: "20"}}
	if err := conn.checkCallSpeaker(t.Context(), bound, "30"); err != nil {
		t.Fatal(err)
	}
	if err := conn.checkCallSpeaker(t.Context(), bound, "31"); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("unenrolled speaker = %v", err)
	}
	archived.Store(true)
	if err := conn.checkCallSpeaker(t.Context(), bound, "30"); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("archived live thread = %v", err)
	}
	if err := conn.checkCallSpeakerAudience(t.Context(), bound, "30", false); err != nil {
		t.Fatalf("archive removed durable grant = %v", err)
	}
	archived.Store(false)
	revoked.Store(true)
	if err := conn.checkCallSpeaker(t.Context(), bound, "30"); !teammateAPIStatus(err, http.StatusNotFound) {
		t.Fatalf("revoked private thread membership = %v", err)
	}
}
