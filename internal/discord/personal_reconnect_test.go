package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	bolt "go.etcd.io/bbolt"
)

func TestPersonalReconnectRecoversMissedOriginalIDs(t *testing.T) {
	c, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/20":
			_, _ = fmt.Fprint(w, personalFixture)
		case "/channels/20/messages":
			_, _ = fmt.Fprint(w, `[{"id":"52","channel_id":"20","content":"second","author":{"id":"30","username":"Owner"},"type":0},{"id":"51","channel_id":"20","content":"first","author":{"id":"30","username":"Owner"},"type":0}]`)
		default:
			http.NotFound(w, r)
		}
	})
	cfg := &c.service.Config
	cfg.Mode, cfg.OwnerID, cfg.DMChannelID = "personal", "30", "20"
	cfg.GuildIDs, cfg.UserIDs = nil, nil
	b := teammateBinding{TeammateDiscussion: TeammateDiscussion{ID: "existing", InstallationID: cfg.InstallationID, Route: Route{ChannelID: "20"}}, Opened: true, Boundary: "50", RepairLower: "50", RepairTargetEpoch: 1, PersonalScanEpoch: 1}
	if err := c.service.Store.teammateBind(b); err != nil {
		t.Fatal(err)
	}
	if err := c.repair(t.Context(), b, 1); err != nil {
		t.Fatal(err)
	}
	pending, err := c.service.Store.teammatePending(cfg.InstallationID)
	if err != nil || len(pending) != 2 {
		t.Fatalf("did not recover the two missed owner messages: n=%d, %v", len(pending), err)
	}
}

type personalReconnectManager struct {
	teammateTestManager
	syncs     int
	retryText bool
}

func (m *personalReconnectManager) AdmissionState(ctx context.Context, a TeammateAdmission) (string, error) {
	if a.Voice.Generation != 0 {
		return TeammateAdmissionMissing, nil
	}
	return m.teammateTestManager.AdmissionState(ctx, a)
}

func (m *personalReconnectManager) Admit(ctx context.Context, a TeammateAdmission, reader TeammateHistoryReader) error {
	if m.retryText && a.Route.MessageID == "51" {
		return errors.New("native manager temporarily offline")
	}
	return m.teammateTestManager.Admit(ctx, a, reader)
}

func (m *personalReconnectManager) Sync(context.Context, TeammateDiscussion, TeammateHistoryReader, func(TeammatePublication) error) error {
	m.syncs++
	return nil
}

func (m *personalReconnectManager) PendingWorkerDiscussions() (func(TeammateDiscussion) bool, error) {
	return func(TeammateDiscussion) bool { return true }, nil
}

func TestPersonalReconnectResumesVerifiedPagesAndKeepsNativeFIFO(t *testing.T) {
	const received = "existing manager result"
	var nonce string
	broken := true
	var requests []string
	var httpState sync.Mutex
	ownerMessage := func(id, content string) map[string]any {
		return map[string]any{"id": id, "channel_id": "20", "content": content, "type": 0, "author": map[string]string{"id": "30", "username": "Owner"}}
	}
	botMessage := func(id, content string) map[string]any {
		return map[string]any{"id": id, "channel_id": "20", "content": content, "type": 0, "author": map[string]any{"id": "99", "bot": true}}
	}
	c, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		httpState.Lock()
		defer httpState.Unlock()
		switch {
		case r.Method != http.MethodGet:
			t.Errorf("history recovery made an unintended Discord effect: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		case r.URL.Path == "/channels/20":
			_, _ = fmt.Fprint(w, personalFixture)
		case r.URL.Path == "/channels/20/messages":
			before := r.URL.Query().Get("before")
			requests = append(requests, before)
			switch before {
			case "":
				var rows []map[string]any
				for n := 150; n >= 51; n-- {
					id := strconv.Itoa(n)
					switch n {
					case 51, 135:
						rows = append(rows, ownerMessage(id, "same request"))
					case 80:
						rows = append(rows, ownerMessage(id, "already delivered"))
					case 120:
						m := botMessage(id, received)
						m["nonce"] = nonce
						rows = append(rows, m)
					default:
						rows = append(rows, botMessage(id, "unrelated bot message"))
					}
				}
				_ = json.NewEncoder(w).Encode(rows)
			case "51":
				if broken {
					http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
					return
				}
				_ = json.NewEncoder(w).Encode([]map[string]any{ownerMessage("50", "initial request"), ownerMessage("49", "before consent")})
			default:
				t.Errorf("unexpected recovery cursor %q", before)
				http.NotFound(w, r)
			}
		case strings.HasPrefix(r.URL.Path, "/channels/20/messages/"):
			id := filepath.Base(r.URL.Path)
			contents := map[string]string{"51": "same request, edited before delivery", "135": "same request", "151": "same request plus a new instruction"}
			message := ownerMessage(id, contents[id])
			if id == "51" {
				message["edited_timestamp"] = "2026-11-20T12:00:00Z"
			}
			_ = json.NewEncoder(w).Encode(message)
		default:
			http.NotFound(w, r)
		}
	})
	cfg := &c.service.Config
	cfg.Mode, cfg.OwnerID, cfg.DMChannelID, cfg.DaemonSocket = "personal", "30", "20", "/tmp/supervisor.sock"
	cfg.Calls = &DispatcherCallsConfig{Mode: "personal", DiscussionID: "20"}
	cfg.GuildIDs, cfg.UserIDs = nil, nil
	m := &personalReconnectManager{}
	c.manager = m
	c.verifyCallTurn = func(context.Context, TeammateDiscussion, schema.VoiceOrigin, bool) error { return nil }
	s := c.service.Store
	d := TeammateDiscussion{ID: "existing", InstallationID: cfg.InstallationID, Route: Route{ChannelID: "20"}}
	b := teammateBinding{TeammateDiscussion: d, Opened: true, Boundary: "150", RepairEpoch: 1, RepairTargetEpoch: 1, NotifiedEpoch: 1}
	if err := s.teammateBind(b); err != nil {
		t.Fatal(err)
	}
	if err := s.teammatePark(d, 0, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		for _, id := range []string{"50", "80"} {
			g := teammateIngressGuard{ActorID: "30", OperationID: "prior-" + id, DiscussionID: d.ID, Disposition: StateSent,
				Route: Route{ChannelID: "20", MessageID: id}}
			if err := putJSON(tx, teammateIngressGuardBucket, teammateKey(d.InstallationID, id), g); err != nil {
				return err
			}
		}
		if err := teammateCheckpointUpdate(tx, d.InstallationID, "first", 1, false, ""); err != nil {
			return err
		}
		return teammateCheckpointUpdate(tx, d.InstallationID, "after-gap", 1, true, "")
	}); err != nil {
		t.Fatal(err)
	}
	const publicationID = "lost-response"
	if err := s.teammateQueue(d.InstallationID, d.ID, d.Route, TeammatePublication{ID: publicationID, Body: received}, c.service.Now(), true); err != nil {
		t.Fatal(err)
	}
	outbox, err := s.teammateOutputs(d.InstallationID, d.ID)
	if err != nil || len(outbox) != 1 {
		t.Fatalf("prepare exact missing receipt: %+v %v", outbox, err)
	}
	httpState.Lock()
	nonce = outbox[0].Chunks[0].ID
	httpState.Unlock()
	if err := s.teammateChangeOutput(d.InstallationID, outbox[0], func(_ *bolt.Tx, _ []byte, p *teammateOutput) error {
		p.Chunks[0].State = StateUncertain
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.queueGap(d); err != nil {
		t.Fatal(err)
	}
	teammateTestEvent(t, c, 2, "MESSAGE_CREATE", ownerMessage("151", "same request plus a new instruction"))
	tick := func(ingress bool) {
		t.Helper()
		var group sync.WaitGroup
		done := make(chan teammateTaskResult, 1)
		after := map[string]time.Time{}
		if ingress {
			after["20"] = time.Now().Add(time.Hour)
		}
		if err := c.schedule(t.Context(), 1, map[string]bool{}, map[string]bool{}, map[string]time.Time{}, after, done, &group); err != nil {
			t.Fatal(err)
		}
		group.Wait()
	}
	tick(false)
	active, err := s.teammateBindingByID(d.InstallationID, d.ID)
	if err != nil || active.RepairAfter != "51" || active.PersonalRepairEpoch != 0 || active.Error == "" || len(m.admitted) != 0 || m.syncs != 0 {
		t.Fatalf("partial recovery advanced native work or lost cursor: %+v, %v", active, err)
	}
	if _, err := c.history(d)(t.Context(), TeammateHistoryQuery{Source: "audience"}); !errors.Is(err, errPersonalHistoryRepair) {
		t.Fatalf("preexisting native sync could bypass recovery with a late worker note: %v", err)
	}
	var observed teammateOutput
	if err = s.db.View(func(tx *bolt.Tx) error {
		return teammateGet(tx.Bucket([]byte(teammateOutboxBucket)), teammateOutputKey(d.InstallationID, d.ID, publicationID), &observed)
	}); err != nil || observed.Chunks[0].State != StateSent || observed.Chunks[0].MessageID != "120" {
		t.Fatalf("missed publication receipt was not reconciled: %+v %v", observed, err)
	}
	path := filepath.Dir(s.db.Path())
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	c.service.Store = s
	t.Cleanup(func() { _ = s.Close() })
	httpState.Lock()
	broken = false
	httpState.Unlock()
	tick(false)
	httpState.Lock()
	gotRequests := append([]string(nil), requests...)
	httpState.Unlock()
	if len(gotRequests) != 3 || gotRequests[0] != "" || gotRequests[1] != "51" || gotRequests[2] != "51" || len(m.admitted) != 0 || m.syncs != 0 {
		t.Fatalf("reopen forgot durable page or ran late worker first: pages=%v admissions=%d syncs=%d", gotRequests, len(m.admitted), m.syncs)
	}
	active, err = s.teammateBindingByID(d.InstallationID, d.ID)
	if err != nil || active.Error != "" || active.PersonalConsentID != "50" || active.PersonalRepairEpoch != 1 {
		t.Fatalf("completed recovery retained stale failure or lost consent fence: %+v, %v", active, err)
	}
	if out, err := s.teammateOutputs(d.InstallationID, d.ID); err != nil || len(out) != 0 {
		t.Fatalf("prepared legacy restate was not suppressed: %+v %v", out, err)
	}
	// Even with a completed scan, a voice retry must wait across store reopen
	// and an older DM's failed first attempt to transfer custody.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	c.service.Store = s
	t.Cleanup(func() { _ = s.Close() })
	voice := func(want error) {
		t.Helper()
		_, err := c.submitCallTurn(t.Context(), d, "call", "turn", 1, Actor{ID: "30"}, "new voice request")
		if !errors.Is(err, want) {
			t.Fatalf("new call admission = %v, want %v", err, want)
		}
	}
	voice(errPersonalHistoryPending)
	m.retryText = true
	tick(true)
	if pending, err := s.teammatePending(d.InstallationID); err != nil || len(pending) != 3 || len(m.admitted) != 0 || !slices.ContainsFunc(pending, func(c teammateCandidate) bool {
		return c.Route.MessageID == "51" && c.State == StateSending
	}) {
		t.Fatalf("failed text attempt did not retain durable custody: %+v, %v", pending, err)
	}
	voice(errPersonalHistoryPending)
	m.retryText = false
	tick(true)
	voice(errPersonalHistoryPending)
	tick(true)
	voice(nil) // Live DM 151 was outside the scan and follows ordinary text scheduling.
	tick(true)
	if len(m.admitted) != 4 || m.admitted[2].Voice.Generation == 0 {
		t.Fatalf("recovered DMs and fresh voice did not enter native custody in order: %+v", m.admitted)
	}
	want := []struct{ id, text string }{{"51", "same request, edited before delivery"}, {"135", "same request"}, {"151", "same request plus a new instruction"}}
	for i, a := range []TeammateAdmission{m.admitted[0], m.admitted[1], m.admitted[3]} {
		operation := "da_" + digest([]string{d.InstallationID, want[i].id})[:32]
		if a.Route.MessageID != want[i].id || a.Text != want[i].text || a.OperationID != operation {
			t.Fatalf("admission %d: %+v", i, a)
		}
	}
	for _, id := range []string{"51", "135", "151"} {
		teammateTestEvent(t, c, 3, "MESSAGE_CREATE", ownerMessage(id, "same request"))
	}
	teammateTestEvent(t, c, 4, "MESSAGE_UPDATE", ownerMessage("51", "edited after cutover"))
	if p, err := s.teammatePending(d.InstallationID); err != nil || len(p) != 0 || len(m.admitted) != 4 {
		t.Fatalf("repeat/late edit replayed an effect: %v %v, admissions %d", p, err, len(m.admitted))
	}
}

func TestPersonalReconnectContextStopsAtInvokingMessageAndConsent(t *testing.T) {
	c, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/20":
			_, _ = fmt.Fprint(w, personalFixture)
		case "/channels/20/messages/52":
			_, _ = fmt.Fprint(w, `{"id":"52","channel_id":"20","content":"current","author":{"id":"30","username":"Owner"},"type":19,"message_reference":{"message_id":"49","channel_id":"20"}}`)
		case "/channels/20/messages":
			if got := r.URL.Query().Get("before"); got != "52" {
				t.Errorf("initial context was not anchored to current admission: before=%q", got)
			}
			_, _ = fmt.Fprint(w, `[{"id":"51","channel_id":"20","content":"earlier","author":{"id":"30"}},{"id":"50","channel_id":"20","content":"consent","author":{"id":"30"}},{"id":"49","channel_id":"20","content":"before consent","author":{"id":"30"}}]`)
		default:
			t.Errorf("context read outside the consent boundary: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	cfg := &c.service.Config
	cfg.Mode, cfg.OwnerID, cfg.DMChannelID = "personal", "30", "20"
	cfg.GuildIDs, cfg.UserIDs = nil, nil
	d := TeammateDiscussion{ID: "existing", InstallationID: cfg.InstallationID, Route: Route{ChannelID: "20"}}
	if err := c.service.Store.teammateBind(teammateBinding{TeammateDiscussion: d, Opened: true, PersonalConsentID: "50"}); err != nil {
		t.Fatal(err)
	}
	page, err := c.history(d, "52")(t.Context(), TeammateHistoryQuery{})
	if err != nil || len(page.Messages) != 2 || page.Messages[0].ID != "50" || page.Messages[1].ID != "51" || page.Cursor != "" {
		t.Fatalf("context crossed consent or invocation: %+v %v", page, err)
	}
}

func TestPersonalReconnectHistoryHoldNoticeIsScopedAndDurable(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		page                       int
		consent, audience, delayed bool
	}{
		{name: "missing consent", audience: true},
		{name: "read forbidden", page: http.StatusForbidden, consent: true, audience: true},
		{name: "network outage", page: http.StatusServiceUnavailable, consent: true, audience: true, delayed: true},
		{name: "audience inaccessible", consent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var posts int
			var body, nonce string
			var httpState sync.Mutex
			c, m := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
				httpState.Lock()
				defer httpState.Unlock()
				switch {
				case r.URL.Path == "/channels/20":
					if tc.audience {
						_, _ = fmt.Fprint(w, personalFixture)
					} else {
						_, _ = fmt.Fprint(w, `{"id":"20","type":3,"recipients":[{"id":"30"},{"id":"31"}]}`)
					}
				case r.URL.Path == "/channels/20/messages" && r.Method == http.MethodGet:
					http.Error(w, "history unavailable", tc.page)
				case r.URL.Path == "/channels/20/messages" && r.Method == http.MethodPost:
					var req struct{ Content, Nonce string }
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Error(err)
					}
					posts++
					body, nonce = req.Content, req.Nonce
					// Discord created the notice, but REST did not return a valid receipt.
					_, _ = fmt.Fprint(w, "{}")
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
					http.NotFound(w, r)
				}
			})
			cfg := &c.service.Config
			cfg.Mode, cfg.OwnerID, cfg.DMChannelID = "personal", "30", "20"
			cfg.GuildIDs, cfg.UserIDs = nil, nil
			now := time.Date(2026, 11, 20, 19, 0, 0, 0, time.UTC)
			c.service.Now = func() time.Time { return now }
			store := c.service.Store
			d := TeammateDiscussion{ID: "existing", InstallationID: cfg.InstallationID, Route: Route{ChannelID: "20"}}
			b := teammateBinding{TeammateDiscussion: d, Opened: true, Boundary: "50"}
			if tc.consent {
				b.PersonalConsentID = "50"
			}
			if err := store.teammateBind(b); err != nil {
				t.Fatal(err)
			}
			gap := func() {
				t.Helper()
				if err := store.db.Update(func(tx *bolt.Tx) error {
					return teammateCheckpointUpdate(tx, d.InstallationID, "first", 1, true, "")
				}); err != nil {
					t.Fatal(err)
				}
			}
			gap()
			gap()
			tick := func() {
				t.Helper()
				var group sync.WaitGroup
				done := make(chan teammateTaskResult, 1)
				if err := c.schedule(t.Context(), 1, map[string]bool{}, map[string]bool{}, map[string]time.Time{}, map[string]time.Time{}, done, &group); err != nil {
					t.Fatal(err)
				}
				group.Wait()
			}
			count := func() int {
				httpState.Lock()
				defer httpState.Unlock()
				return posts
			}
			tick()
			if tc.delayed {
				if count() != 0 {
					t.Fatal("posted before sustained history failure")
				}
				now = now.Add(personalHistoryNoticeDelay + time.Second)
				tick()
			}
			if !tc.audience {
				if count() != 0 {
					t.Fatal("sent notice to unavailable audience")
				}
				return
			}
			if count() != 1 {
				t.Fatalf("expected one owner notice, got %d", count())
			}
			httpState.Lock()
			noticeBody, noticeNonce := body, nonce
			httpState.Unlock()
			if strings.Contains(strings.ToLower(noticeBody), "restate") || noticeNonce == "" || !strings.Contains(noticeBody, "saved in order") {
				t.Fatalf("incorrect notice: %q", noticeBody)
			}
			teammateTestEvent(t, c, 2, "MESSAGE_CREATE", map[string]any{"id": "150", "channel_id": "20", "content": noticeBody, "nonce": noticeNonce,
				"author": map[string]any{"id": "99", "bot": true}})
			gap() // A new READY while unavailable is still the same recovery episode.
			tick()
			tick()
			if count() != 1 || len(m.admitted) != 0 {
				t.Fatalf("history outage duplicated notice or admitted unverified work: %d, %d", count(), len(m.admitted))
			}
		})
	}
}
