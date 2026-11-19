package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

type teammateControlManager struct {
	teammateTestManager
	resumeCall         LocalTeammateResumeManual
	unavailable        bool
	cancelBeforeNotice context.CancelFunc
}

func (m *teammateControlManager) Admit(ctx context.Context, a TeammateAdmission, h TeammateHistoryReader) error {
	if m.unavailable {
		if m.cancelBeforeNotice != nil {
			m.cancelBeforeNotice()
			m.cancelBeforeNotice = nil
		}
		return ErrTeammateManagerUnavailable
	}
	return m.teammateTestManager.Admit(ctx, a, h)
}

func (m *teammateControlManager) ResumeManual(_ context.Context, d TeammateDiscussion, session, instance string, input uint64) error {
	m.resumeCall = LocalTeammateResumeManual{Discussion: d.ID, Session: session, Instance: instance, ExternalInput: input}
	return nil
}

func (m *teammateControlManager) Status(TeammateDiscussion) (TeammateNativeStatus, error) {
	return TeammateNativeStatus{State: "paused", ExternalInput: 23}, nil
}

func TestTeammateLocalResumeUsesExactPersistedDiscussion(t *testing.T) {
	c, _ := teammateTestConnector(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) })
	d := TeammateDiscussion{InstallationID: "install", ID: "discussion", Route: Route{GuildID: "10", ChannelID: "201", ParentID: "20"}}
	if err := c.service.Store.teammateBind(teammateBinding{TeammateDiscussion: d, Opened: true}); err != nil {
		t.Fatal(err)
	}
	m := &teammateControlManager{}
	c.service.teammateManager = m
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"discussion":"other","session":"manager","instance":"exact","external_input":23}`, http.StatusBadRequest},
		{`{"discussion":"discussion","session":"manager","instance":"exact"}`, http.StatusBadRequest},
		{`{"discussion":"discussion","session":"manager","instance":"exact","external_input":23}`, http.StatusOK},
	} {
		w := httptest.NewRecorder()
		c.service.localHandler(w, httptest.NewRequest(http.MethodPost, "/resume-manual", strings.NewReader(tc.body)))
		if w.Code != tc.status {
			t.Fatalf("status %d, body %s", w.Code, w.Body)
		}
	}
	expected := LocalTeammateResumeManual{Discussion: d.ID, Session: "manager", Instance: "exact", ExternalInput: 23}
	if m.resumeCall != expected {
		t.Fatalf("resume %+v, want %+v", m.resumeCall, expected)
	}
	status, err := c.service.localStatus(context.Background())
	if err != nil || status.Mode != "teammate" || status.Teammate == nil || len(status.Teammate.Discussions) != 1 ||
		status.Teammate.Discussions[0].Native == nil || status.Teammate.Discussions[0].Native.ExternalInput != 23 {
		t.Fatalf("mode and exact native counter %+v %v", status, err)
	}
}

func TestTeammateGrantUsesCompactedExactSource(t *testing.T) {
	source := teammateTestMessage("202", "201", "77", "<@99> read this", true)
	current := *source
	c, m := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/20":
			_, _ = fmt.Fprint(w, teammateParent)
		case "/channels/201":
			_, _ = fmt.Fprint(w, teammatePublic)
		case "/channels/20/permissions/member/99":
			teammateTestPermissions(w)
		case "/channels/201/messages/202":
			_ = json.NewEncoder(w).Encode(current)
		default:
			w.WriteHeader(404)
		}
	})
	ctx := context.Background()
	m.phase = TeammateAdmissionAccepted
	teammateTestEvent(t, c, 1, "MESSAGE_CREATE", source)
	pending, err := c.service.Store.teammatePending("install")
	if err != nil || len(pending) != 1 {
		t.Fatal(pending, err)
	}
	if err = c.process(ctx, pending[0]); err != nil {
		t.Fatal(err)
	}
	a := m.admitted[0]
	q := TeammateHistoryQuery{Source: "grant", AdmissionID: a.OperationID, Revision: a.Revision, MessageID: a.Route.MessageID, AuthorID: a.Author.ID}
	if _, err = c.history(a.Discussion)(ctx, q); !errors.Is(err, ErrTeammateSourceChanged) {
		t.Fatalf("grant before cutover: %v", err)
	}
	m.phase = TeammateAdmissionCutover
	if _, err = c.history(a.Discussion)(ctx, q); err != nil {
		t.Fatalf("grant at live cutover: %v", err)
	}
	pending, err = c.service.Store.teammatePending("install")
	if err != nil || len(pending) != 1 {
		t.Fatal(pending, err)
	}
	if err = c.reconcileAccepted(ctx, pending[0]); err != nil {
		t.Fatal(err)
	}
	if _, err = c.history(a.Discussion)(ctx, q); err != nil {
		t.Fatalf("verified persisted grant: %v", err)
	}
	forged := q
	forged.AuthorID = "88"
	if _, err = c.history(a.Discussion)(ctx, forged); !errors.Is(err, ErrTeammateSourceChanged) {
		t.Fatalf("forged grant: %v", err)
	}
	current.Content = "edited"
	if _, err = c.history(a.Discussion)(ctx, q); !errors.Is(err, ErrTeammateSourceChanged) {
		t.Fatalf("changed grant: %v", err)
	}
}

func TestTeammateStartupBlockPostsOneNoticeWhileProvisional(t *testing.T) {
	opener := teammateTestMessage("202", "201", "77", "<@99> check this", true)
	posts := 0
	c, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/20":
			_, _ = fmt.Fprint(w, teammateParent)
		case "/channels/201":
			_, _ = fmt.Fprint(w, teammatePublic)
		case "/channels/20/permissions/member/99":
			teammateTestPermissions(w)
		case "/channels/201/messages/202":
			_ = json.NewEncoder(w).Encode(opener)
		case "/channels/201/messages":
			posts++
			var p struct{ Content string }
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(discordgo.Message{ID: "303", ChannelID: "201", Content: p.Content, Author: &discordgo.User{ID: "99", Bot: true}})
		default:
			w.WriteHeader(404)
		}
	})
	firstCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.manager = &teammateControlManager{unavailable: true, cancelBeforeNotice: cancel}
	teammateTestEvent(t, c, 1, "MESSAGE_CREATE", opener)
	for i := range 3 {
		pending, err := c.service.Store.teammatePending("install")
		if err != nil || len(pending) != 1 {
			t.Fatal(pending, err)
		}
		ctx := context.Background()
		if i == 0 {
			ctx = firstCtx
		}
		if err = c.process(ctx, pending[0]); !errors.Is(err, ErrTeammateManagerUnavailable) {
			t.Fatal(err)
		}
		if i == 0 && posts != 0 {
			t.Fatal("startup notice sent after deadline")
		}
	}
	rows, err := c.service.Store.teammateBindings("install")
	if err != nil || len(rows) != 1 || rows[0].Opened || posts != 1 {
		t.Fatalf("bindings %+v posts=%d err=%v", rows, posts, err)
	}
}
