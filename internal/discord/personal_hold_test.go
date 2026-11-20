package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Keep the native admission when the connector loses Admit's first response.
type personalHoldManager struct {
	teammateTestManager
	held         bool
	firstMissing bool
	attempts     int
	delivered    int
}

func (m *personalHoldManager) Admit(ctx context.Context, a TeammateAdmission, r TeammateHistoryReader) error {
	m.attempts++
	if m.firstMissing && m.attempts == 1 {
		return errors.New("interrupted before native admission")
	}
	if err := m.teammateTestManager.Admit(ctx, a, r); err != nil {
		return err
	}
	if m.attempts == 1 {
		return errors.New("admission response lost")
	}
	return nil
}

func (m *personalHoldManager) AdmissionState(_ context.Context, a TeammateAdmission) (string, error) {
	for i, prior := range m.admitted {
		if prior.OperationID == a.OperationID {
			if i < m.delivered {
				return TeammateAdmissionCutover, nil
			}
			return TeammateAdmissionAccepted, nil
		}
	}
	return TeammateAdmissionMissing, nil
}

func (m *personalHoldManager) Sync(_ context.Context, _ TeammateDiscussion, _ TeammateHistoryReader, emit func(TeammatePublication) error) error {
	if m.delivered == len(m.admitted) {
		return nil
	}
	a := m.admitted[m.delivered]
	if m.held {
		return &TeammateInputHold{AdmissionID: a.OperationID, DirectInput: true}
	}
	if err := emit(TeammatePublication{ID: a.OperationID, AdmissionID: a.OperationID, Body: "Read " + a.Text, Sequence: a.Sequence}); err != nil {
		return err
	}
	m.delivered++
	return nil
}

func TestPersonalInputHoldReconnectAndOrderedRecovery(t *testing.T) {
	for _, firstMissing := range []bool{false, true} {
		t.Run(fmt.Sprintf("first-native-admission-missing-%t", firstMissing), func(t *testing.T) {
			testPersonalHoldRecovery(t, firstMissing)
		})
	}
}

func testPersonalHoldRecovery(t *testing.T, firstMissing bool) {
	msg := func(id string) string {
		return fmt.Sprintf(`{"id":%q,"channel_id":"20","content":%q,"author":{"id":"30","username":"Owner"},"type":0}`, id, "message "+id)
	}
	var posts []string
	var noticeNonce string
	c, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/channels/20":
			_, _ = fmt.Fprint(w, personalFixture)
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/channels/20/messages/"):
			_, _ = fmt.Fprint(w, msg(filepath.Base(r.URL.Path)))
		case r.Method == "POST" && r.URL.Path == "/channels/20/messages":
			var req struct{ Content, Nonce string }
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			posts = append(posts, req.Content)
			if len(posts) == 1 {
				noticeNonce = req.Nonce
				// Discord created the message; a malformed REST ack leaves it uncertain.
				_, _ = fmt.Fprint(w, "{}")
				return
			}
			_, _ = fmt.Fprintf(w, `{"id":%q,"channel_id":"20","content":%q,"author":{"id":"99","bot":true}}`, fmt.Sprint(300+len(posts)), req.Content)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})
	cfg := &c.service.Config
	cfg.Mode, cfg.OwnerID, cfg.DMChannelID = "personal", "30", "20"
	cfg.GuildIDs, cfg.UserIDs = nil, nil
	m := &personalHoldManager{held: true, firstMissing: firstMissing}
	c.manager = m
	for n, id := range []string{"200", "201"} {
		teammateTestEvent(t, c, int64(n+1), "MESSAGE_CREATE", json.RawMessage(msg(id)))
	}
	rows := func() []teammateCandidate {
		p, err := c.service.Store.teammatePending("install")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	ctx := context.Background()
	if err := c.process(ctx, rows()[0]); err == nil {
		t.Fatal("expected lost admission response")
	}
	p := rows()
	if len(p) != 2 || p[0].State != StateSending || p[1].State != StatePrepared {
		t.Fatalf("lost response failed to preserve both inputs: %+v", p)
	}
	reopen := func() {
		dir := filepath.Dir(c.service.Store.db.Path())
		if err := c.service.Store.Close(); err != nil {
			t.Fatal(err)
		}
		store, err := OpenStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		c.service.Store = store
		t.Cleanup(func() { _ = store.Close() })
	}
	reopen()
	tick := func(ingress bool) {
		after := map[string]time.Time{}
		if ingress {
			after["20"] = time.Now().Add(time.Hour)
		}
		var tasks sync.WaitGroup
		done := make(chan teammateTaskResult, 1)
		if err := c.schedule(ctx, 1, map[string]bool{}, map[string]bool{}, map[string]time.Time{}, after, done, &tasks); err != nil {
			t.Fatal(err)
		}
		tasks.Wait()
	}
	tick(true)
	tick(false)
	if len(posts) != 1 || !strings.Contains(posts[0], "unfinished input") || noticeNonce == "" {
		t.Fatalf("missing operational notice: %q", posts)
	}
	reopen()
	ack := map[string]any{"id": "301", "channel_id": "20", "content": posts[0], "nonce": noticeNonce, "author": map[string]any{"id": "99", "bot": true}}
	teammateTestEvent(t, c, 3, "MESSAGE_CREATE", ack)
	tick(false)
	tick(false)
	p = rows()
	if len(posts) != 1 || len(p) != 2 || p[0].State != teammateStateAccepted || p[1].State != StatePrepared || len(m.admitted) != 1 {
		t.Fatalf("repeat sync duplicated notice or advanced held input: posts=%q admitted=%d queue=%+v", posts, len(m.admitted), p)
	}
	m.held = false
	tick(false) // First native result is produced.
	tick(false) // First cutover is confirmed, opening the DM and delivering the result.
	tick(true)  // Second input can now enter native ownership.
	tick(false)
	tick(false)
	if len(rows()) != 0 || len(m.admitted) != 2 || len(posts) != 3 || posts[1] != "Read message 200" || posts[2] != "Read message 201" {
		t.Fatalf("ordered recovery: queue=%v admissions=%d posts=%q", rows(), len(m.admitted), posts)
	}
}

func TestPersonalInputNoticeScopeCooldownAndImmutableCause(t *testing.T) {
	var bodies []string
	c, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /channels/20":
			_, _ = fmt.Fprint(w, personalFixture)
		case "POST /channels/20/messages":
			var req struct{ Content string }
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			bodies = append(bodies, req.Content)
			_, _ = fmt.Fprintf(w, `{"id":%q,"channel_id":"20","content":%q,"author":{"id":"99","bot":true}}`, fmt.Sprint(500+len(bodies)), req.Content)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})
	cfg := &c.service.Config
	cfg.Mode, cfg.OwnerID, cfg.DMChannelID = "personal", "30", "20"
	d := TeammateDiscussion{ID: "bound", InstallationID: cfg.InstallationID, Route: Route{ChannelID: "20"}}
	if err := c.service.Store.teammateBind(teammateBinding{TeammateDiscussion: d, Opened: true}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	c.service.Now = func() time.Time { return now }
	notify := func(d TeammateDiscussion, id string, direct bool) {
		if err := c.sendInputNotice(context.Background(), d, &TeammateInputHold{AdmissionID: id, DirectInput: direct}); err != nil {
			t.Fatal(err)
		}
	}
	foreign := d
	foreign.Route.ChannelID = "21"
	notify(foreign, "one", true)
	cfg.Mode = "teammate"
	notify(d, "one", true)
	cfg.Mode = "personal"
	notify(d, "one", false)
	notify(d, "one", true)
	notify(d, "two", true)
	if len(bodies) != 1 || !strings.Contains(bodies[0], "cannot confirm") {
		t.Fatalf("unexpected scope, cause or cooldown %q", bodies)
	}
	now = now.Add(teammateInputNoticeInterval)
	if err := c.sendInputNotice(context.Background(), d, &TeammateInputHold{AdmissionID: "two", DirectInput: true, CustodyLost: true}); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || !strings.Contains(bodies[1], "unfinished input") || !strings.Contains(bodies[1], "exit the manager normally") || strings.Contains(bodies[1], "resume-manual") {
		t.Fatalf("persistent second hold after cooldown: %q", bodies)
	}
	hold := &TeammateInputHold{AdmissionID: "two", EffectUncertain: true}
	for range 2 {
		if err := c.sendInputNotice(context.Background(), d, hold); err != nil {
			t.Fatal(err)
		}
	}
	if len(bodies) != 3 || !strings.Contains(bodies[2], "cannot confirm the outcome") || strings.Contains(bodies[2], "ready for the next message") {
		t.Fatalf("uncertain native effect did not publish its own receipt-backed notice: %q", bodies)
	}

}
