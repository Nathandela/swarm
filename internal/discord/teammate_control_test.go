package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	bolt "go.etcd.io/bbolt"
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
			if p.Content != "The dispatcher is unavailable. Your message is saved." {
				t.Fatal("missing queue and follow-up guidance")
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

type teammatePublicationManager struct {
	teammateControlManager
	generation uint64
	grantErr   error
	last       TeammatePublication
	validated  []TeammatePublication
}

func (m *teammatePublicationManager) Status(TeammateDiscussion) (TeammateNativeStatus, error) {
	return TeammateNativeStatus{State: "parked", Generation: m.generation}, nil
}
func (m *teammatePublicationManager) ValidatePublication(_ context.Context, _ TeammateDiscussion, p TeammatePublication) error {
	m.last = p
	m.validated = append(m.validated, p)
	return m.grantErr
}

func TestTeammateParkedStartupNoticeIsBoundToGeneration(t *testing.T) {
	posts := 0
	c, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/20":
			_, _ = fmt.Fprint(w, teammateParent)
		case "/channels/201":
			_, _ = fmt.Fprint(w, teammatePublic)
		case "/channels/20/permissions/member/99":
			teammateTestPermissions(w)
		case "/channels/201/messages":
			posts++
			var p struct{ Content string }
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(discordgo.Message{ID: fmt.Sprint(400 + posts), ChannelID: "201", Content: p.Content, Author: &discordgo.User{ID: "99", Bot: true}})
		default:
			w.WriteHeader(404)
		}
	})
	m := &teammatePublicationManager{generation: 3, grantErr: ErrNotAuthorized}
	c.manager = m
	d := TeammateDiscussion{InstallationID: "install", ID: "discussion", Route: Route{GuildID: "10", ChannelID: "201", ParentID: "20"}}
	if err := c.service.Store.teammateBind(teammateBinding{TeammateDiscussion: d, Opened: true}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, generation := range []uint64{3, 3, 4, 4} {
		m.generation = generation
		if err := c.managerStartupError(ctx, d, ErrTeammateManagerUnavailable); !errors.Is(err, ErrTeammateManagerUnavailable) {
			t.Fatal(err)
		}
	}
	if posts != 2 || len(m.validated) != 0 {
		t.Fatalf("posted %d connector notices, made %d native grant checks", posts, len(m.validated))
	}
}

func TestTeammateWorkerPublicationHoldsRevokedGrantAndNeverRepostsUncertainChunk(t *testing.T) {
	posts := 0
	c, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/20":
			_, _ = fmt.Fprint(w, teammateParent)
		case "/channels/201":
			_, _ = fmt.Fprint(w, teammatePublic)
		case "/channels/20/permissions/member/99":
			teammateTestPermissions(w)
		case "/channels/201/messages":
			posts++
			w.WriteHeader(500)
		default:
			w.WriteHeader(404)
		}
	})
	d := TeammateDiscussion{InstallationID: "install", ID: "discussion", Route: Route{GuildID: "10", ChannelID: "201", ParentID: "20"}}
	if err := c.service.Store.teammateBind(teammateBinding{TeammateDiscussion: d, Opened: true}); err != nil {
		t.Fatal(err)
	}
	p := TeammatePublication{ID: "late-result", JobID: "job", AttemptID: "attempt", AdmissionID: "wake", Body: "verified result"}
	if err := c.service.Store.teammateQueueOutput(d, p, c.service.Now()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	restarted := &teammatePublicationManager{grantErr: ErrNotAuthorized}
	c.manager = restarted
	if err := c.sendPending(ctx, d); !errors.Is(err, ErrNotAuthorized) || posts != 0 {
		t.Fatalf("revoked effect posts=%d err=%v", posts, err)
	}
	if restarted.last.JobID != p.JobID || restarted.last.AttemptID != p.AttemptID || restarted.last.BodyHash != PublicationBodyHash(p.Body) {
		t.Fatalf("wrong native authority %v", restarted.last)
	}
	restarted.grantErr = nil
	if err := c.sendPending(ctx, d); !teammateAPIStatus(err, http.StatusInternalServerError) || posts != 1 {
		t.Fatalf("initial post count %d err %v", posts, err)
	}
	restarted.grantErr = ErrNotAuthorized
	if err := c.sendPending(ctx, d); !errors.Is(err, ErrDeliveryUncertain) || posts != 1 {
		t.Fatalf("reposted uncertain result posts=%d err=%v", posts, err)
	}
	pending, err := c.service.Store.teammateOutputs("install", d.ID)
	if err != nil || len(pending) != 1 {
		t.Fatal(pending, err)
	}
	if err = c.service.Store.teammateObserve("install", d.Route, "500", pending[0].Chunks[0].ID, p.Body, c.service.Now()); err != nil {
		t.Fatal(err)
	}
	pending, err = c.service.Store.teammateOutputs("install", d.ID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("reconciled result %+v %v", pending, err)
	}
}

func TestTeammateNonJobPublicationsStillRevalidateNativeAuthority(t *testing.T) {
	c, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/20":
			_, _ = fmt.Fprint(w, teammateParent)
		case "/channels/201":
			_, _ = fmt.Fprint(w, teammatePublic)
		case "/channels/20/permissions/member/99":
			teammateTestPermissions(w)
		default:
			t.Errorf("publication bypassed native grant before %s %s", r.Method, r.URL.Path)
			w.WriteHeader(403)
		}
	})
	d := TeammateDiscussion{InstallationID: "install", ID: "discussion", Route: Route{GuildID: "10", ChannelID: "201", ParentID: "20"}}
	if err := c.service.Store.teammateBind(teammateBinding{TeammateDiscussion: d, Opened: true}); err != nil {
		t.Fatal(err)
	}
	responses := []TeammatePublication{
		{ID: "reply", AdmissionID: "input", Sequence: 1, Body: "Here are the cached results from both workers."},
		{ID: "question", AdmissionID: "input", Sequence: 2, QuestionID: "question", QuestionHash: "hash", Body: "Should I continue?"},
		{ID: "continuity", SystemCause: TeammateSystemContinuityGap, Sequence: 3, Body: "The manager recovered a discussion gap."},
	}
	for _, p := range responses {
		if err := c.service.Store.teammateQueueOutput(d, p, c.service.Now()); err != nil {
			t.Fatal(err)
		}
	}
	restarted := &teammatePublicationManager{grantErr: ErrNotAuthorized}
	c.manager = restarted
	if err := c.sendPending(context.Background(), d); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("grant after reconnect: %v", err)
	}
	if len(restarted.validated) != len(responses) {
		t.Fatalf("proof checks = %+v", restarted.validated)
	}
	for i, p := range restarted.validated {
		if p.ID != responses[i].ID || p.BodyHash != PublicationBodyHash(responses[i].Body) || p.JobID != "" {
			t.Fatalf("proof check %+v expected publication %s", p, responses[i].ID)
		}
	}
}

func TestTeammateDormantRoutesDoNotExhaustActiveQuota(t *testing.T) {
	c, _ := teammateTestConnector(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(404) })
	s := c.service.Store
	first := TeammateDiscussion{InstallationID: "install", ID: "discussion-0", Route: Route{GuildID: "10", ChannelID: "200", ParentID: "20"}}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		for i := range teammateDiscussionLimit + 2 {
			d := first
			d.ID = fmt.Sprint("discussion-", i)
			d.Route.ChannelID = fmt.Sprint(200 + i)
			b := teammateBinding{TeammateDiscussion: d, Opened: true, Boundary: "150"}
			k := teammateKey(d.InstallationID, d.Route.ChannelID)
			if err := putJSON(tx, teammateDiscussionBucket, k, b); err != nil {
				return err
			}
			if err := teammateIndexBindingID(tx, b, k); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fresh := TeammateDiscussion{InstallationID: "install", ID: "fresh", Route: Route{GuildID: "10", ChannelID: "9000", ParentID: "20"}}
	if err := s.teammateBind(teammateBinding{TeammateDiscussion: fresh, Opened: true}); err != nil {
		t.Fatalf("new binding after historical quota: %v", err)
	}
	if err := s.teammatePark(fresh, 0, nil); err != nil {
		t.Fatal(err)
	}
	active, err := s.teammateActiveBindings("install")
	if err != nil || len(active) != 0 {
		t.Fatalf("active historical discussions=%d err=%v", len(active), err)
	}
	ready := map[string]any{"session_id": "old", "application": map[string]string{"id": "99"}, "user": map[string]string{"id": "99"}}
	teammateTestEvent(t, c, 1, "READY", ready)
	ready["session_id"] = "new"
	teammateTestEvent(t, c, 2, "READY", ready)
	parked, err := s.teammateBindingByID("install", first.ID)
	if err != nil || parked.Route != first.Route || parked.RepairTargetEpoch != 0 {
		t.Fatalf("parked binding %+v err=%v", parked, err)
	}
	teammateTestEvent(t, c, 3, "MESSAGE_CREATE", teammateTestMessage("400", first.Route.ChannelID, "88", "ordinary follow-up", false))
	if err = s.teammatePark(first, 0, nil); err != nil {
		t.Fatal(err)
	}
	active, err = s.teammateActiveBindings("install")
	if err != nil || len(active) != 1 || active[0].ID != first.ID || active[0].RepairTargetEpoch != 1 || active[0].RepairLower != "150" {
		t.Fatalf("reactivation %+v err=%v", active, err)
	}
	pending, err := s.teammatePending("install")
	if err != nil || len(pending) != 1 || pending[0].Opening || pending[0].Route.ChannelID != first.Route.ChannelID {
		t.Fatalf("untagged input %+v err=%v", pending, err)
	}
}

func TestIdleSyncRetainsDiagnosticForBlockedHumanMessage(t *testing.T) {
	c, _ := teammateTestConnector(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	d := TeammateDiscussion{InstallationID: "install", ID: "discussion", Route: Route{GuildID: "10", ChannelID: "201", ParentID: "20"}}
	const failure = "manager process exited before readiness could be verified, exit code 1"
	if err := c.service.Store.teammateBind(teammateBinding{TeammateDiscussion: d, Opened: true, Error: failure}); err != nil {
		t.Fatal(err)
	}
	teammateTestEvent(t, c, 1, "MESSAGE_CREATE", teammateTestMessage("202", "201", "77", "check via gcloud", false))
	pending, err := c.service.Store.teammatePending("install")
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending %d, %v", len(pending), err)
	}
	var tasks sync.WaitGroup
	done := make(chan teammateTaskResult, 1)
	if err := c.schedule(context.Background(), 1, map[string]bool{}, map[string]bool{}, map[string]time.Time{}, map[string]time.Time{}, done, &tasks); err != nil {
		t.Fatal(err)
	}
	tasks.Wait()
	select {
	case result := <-done:
		if result.id != "201" || !result.sync || result.retry {
			t.Fatalf("idle sync: %+v", result)
		}
	default:
		t.Fatal("idle sync was not scheduled")
	}
	b, err := c.service.Store.teammateBindingByID("install", d.ID)
	if err != nil || b.Error != failure {
		t.Fatalf("pending input diagnostic %q, %v", b.Error, err)
	}
}

func TestTeammateStagesSecondHumanPingWhileFirstAwaitsCutover(t *testing.T) {
	message := func(id string) *discordgo.Message {
		text := "hello"
		if id == "202" {
			text = "<@99> hello"
		}
		return teammateTestMessage(id, "201", "77", text, id == "202")
	}
	c, m := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/20":
			if _, err := fmt.Fprint(w, teammateParent); err != nil {
				t.Error(err)
			}
		case "/channels/201":
			if _, err := fmt.Fprint(w, teammatePublic); err != nil {
				t.Error(err)
			}
		case "/channels/20/permissions/member/99":
			teammateTestPermissions(w)
		case "/channels/201/messages/202", "/channels/201/messages/203", "/channels/201/messages/204":
			id := strings.TrimPrefix(r.URL.Path, "/channels/201/messages/")
			if err := json.NewEncoder(w).Encode(message(id)); err != nil {
				t.Error(err)
			}
		default:
			w.WriteHeader(404)
		}
	})
	post := func(seq int64, id string) {
		teammateTestEvent(t, c, seq, "MESSAGE_CREATE", message(id))
	}
	tick := func() {
		var tasks sync.WaitGroup
		if err := c.schedule(t.Context(), 1, map[string]bool{}, map[string]bool{}, nil, map[string]time.Time{"201": time.Now().Add(time.Hour)}, make(chan teammateTaskResult, 1), &tasks); err != nil {
			t.Fatal(err)
		}
		tasks.Wait()
	}
	post(1, "202")
	tick()
	m.phase = TeammateAdmissionAccepted
	post(2, "203")
	post(3, "204")
	post(4, "204")
	for range 4 {
		tick()
	}
	if len(m.admitted) != 3 || m.admitted[1].Route.MessageID != "203" || m.admitted[2].Route.MessageID != "204" {
		t.Fatalf("expected one admission per original message, in order: %+v", m.admitted)
	}
}
