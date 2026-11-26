package discord

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const externalTestBearer = "Bearer test-service-secret-00000000000000000000"

func externalTestRequest(t *testing.T, bridge *teammateConnector, method, query, bearer string, event externalInput, status int) externalReceipt {
	t.Helper()
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, "/admissions"+query, bytes.NewReader(raw))
	req.Header.Set("Authorization", bearer)
	res := httptest.NewRecorder()
	bridge.externalHandler(externalTestBearer, res, req)
	if res.Code != status {
		t.Fatalf("%s %s = %d, want %d: %s", method, query, res.Code, status, res.Body.String())
	}
	var receipt externalReceipt
	if status == http.StatusOK {
		if err = json.Unmarshal(res.Body.Bytes(), &receipt); err != nil {
			t.Fatal(err)
		}
	}
	return receipt
}

type externalUncertainManager struct {
	*teammateTestManager
}

func (m *externalUncertainManager) Admit(ctx context.Context, a TeammateAdmission, reader TeammateHistoryReader) error {
	if len(m.admitted) != 0 {
		return m.teammateTestManager.Admit(ctx, a, reader)
	}
	if _, err := reader(ctx, TeammateHistoryQuery{Source: "admission", AdmissionID: a.OperationID, Revision: a.Revision, MessageID: a.Route.MessageID, AuthorID: a.Author.ID}); err != nil {
		return err
	}
	if err := m.teammateTestManager.Admit(ctx, a, reader); err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}

func TestExternalAdmissionRecoversThreadAndNativeCutover(t *testing.T) {
	ctx := t.Context()
	now := time.Now().UTC()
	message := teammateTestMessage("201", "20", "89", "Automation announcement", false)
	message.WebhookID, message.Author.Bot = "89", true
	var threadPosts atomic.Int32
	followup := teammateTestMessage("202", "201", "77", "Please include the report", false)
	t.Run("replay", func(t *testing.T) {
		bridge, manager := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
			switch r.Method + " " + r.URL.Path {
			case "GET /channels/20":
				fmt.Fprint(w, `{"id":"20","guild_id":"10","type":0}`)
			case "GET /channels/20/permissions/member/99":
				teammateTestPermissions(w)
			case "GET /channels/20/messages/201":
				_ = json.NewEncoder(w).Encode(message)
			case "GET /channels/201":
				if threadPosts.Load() == 0 {
					http.NotFound(w, r)
					return
				}
				fmt.Fprint(w, teammatePublic)
			case "POST /channels/20/messages/201/threads":
				threadPosts.Add(1)
				http.Error(w, "response lost after creation", http.StatusBadGateway)
			case "GET /channels/201/messages/202":
				_ = json.NewEncoder(w).Encode(followup)
			default:
				t.Errorf("unexpected REST route %s %s", r.Method, r.URL.Path)
				http.NotFound(w, r)
			}
		})
		bridge.external = externalInputScope{GuildID: "10", ChannelID: "20", WebhookID: "89", Project: t.TempDir()}
		bridge.service.Now = func() time.Time { return now }
		bridge.manager = &externalUncertainManager{manager}
		event := externalInput{EventID: "job-1", MessageID: "201",
			ContentSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(message.Content))),
			Text:          "Prepare the report", ExpiresAt: now.Add(time.Minute).Format(time.RFC3339Nano)}

		openReceipt := externalTestRequest(t, bridge, http.MethodPost, "", externalTestBearer, event, http.StatusOK)
		if openReceipt.State != externalQueued {
			t.Fatal(openReceipt)
		}
		sibling := event
		sibling.EventID = "job-2"
		externalTestRequest(t, bridge, http.MethodPost, "", externalTestBearer, sibling, http.StatusConflict)
		teammateTestEvent(t, bridge, 1, "MESSAGE_CREATE", message)
		rows, err := bridge.service.Store.teammatePending("install")
		if err != nil || len(rows) != 1 {
			t.Fatalf("pending %v: %v", rows, err)
		}
		if err = bridge.process(ctx, rows[0]); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("initial attempt: %v", err)
		}
		waiting := externalTestRequest(t, bridge, http.MethodGet, "?event_id=job-1", externalTestBearer, externalInput{}, http.StatusOK)
		if waiting.State != externalDispatching || waiting.ThreadID != "201" || waiting.DiscussionID == "" {
			t.Fatal(waiting)
		}
		dir := filepath.Dir(bridge.service.Store.db.Path())
		if err = bridge.service.Store.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := OpenStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		bridge.service.Store = reopened
		now = now.Add(2 * time.Minute)

		rows, err = reopened.teammatePending("install")
		if err != nil || len(rows) != 1 {
			t.Fatalf("pending after restart %v: %v", rows, err)
		}
		if err = bridge.process(ctx, rows[0]); err != nil {
			t.Fatal(err)
		}
		accepted := externalTestRequest(t, bridge, http.MethodGet, "?event_id=job-1", externalTestBearer, externalInput{}, http.StatusOK)
		if accepted.State != externalAccepted || accepted.DiscussionID != waiting.DiscussionID || accepted.ThreadID != waiting.ThreadID ||
			externalTestRequest(t, bridge, http.MethodPost, "", externalTestBearer, event, http.StatusOK) != accepted {
			t.Fatal(accepted)
		}
		changed := event
		changed.ExpiresAt = now.Add(time.Minute).Format(time.RFC3339Nano)
		externalTestRequest(t, bridge, http.MethodPost, "", externalTestBearer, changed, http.StatusConflict)
		if len(manager.admitted) != 1 || threadPosts.Load() != 1 || manager.admitted[0].Text != event.Text ||
			manager.admitted[0].Author.ID != "89" || manager.admitted[0].Authority != AuthorityExternal ||
			manager.admitted[0].Discussion.FixedProject != bridge.external.Project {
			t.Fatalf("native inputs %v, thread creates %d", manager.admitted, threadPosts.Load())
		}
		origin := manager.admitted[0]
		if err := bridge.verifyAdmissionMessage(ctx, origin.Discussion, TeammateHistoryQuery{
			Source: "grant", MessageID: origin.Route.MessageID, AuthorID: origin.Author.ID,
			AdmissionID: origin.OperationID, Revision: origin.Revision,
		}, nil, nil); err != nil {
			t.Fatal(err)
		}

		teammateTestEvent(t, bridge, 2, "MESSAGE_CREATE", followup)
		rows, err = reopened.teammatePending("install")
		if err != nil || len(rows) != 1 {
			t.Fatalf("pending human followup %v: %v", rows, err)
		}
		if err = bridge.process(ctx, rows[0]); err != nil {
			t.Fatal(err)
		}
		if len(manager.admitted) != 2 || manager.admitted[1].Text != followup.Content ||
			manager.admitted[1].Discussion.ID != origin.Discussion.ID {
			t.Fatalf("human continuity lost: %v", manager.admitted)
		}
	})
}

func TestExternalAdmissionRejectsExpiredAndChangedSource(t *testing.T) {
	for _, tc := range []string{"expired", "expiry while creating thread", "wrong webhook", "edited content", "deleted before dispatch"} {
		t.Run(tc, func(t *testing.T) {
			now := time.Now().UTC()
			message := teammateTestMessage("201", "20", "89", "Original automation announcement", false)
			message.WebhookID, message.Author.Bot = "89", true
			event := externalInput{EventID: "job-1", MessageID: message.ID, Text: "Test this report",
				ContentSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(message.Content))), ExpiresAt: now.Add(time.Minute).Format(time.RFC3339Nano)}
			var threadCreated atomic.Bool
			bridge, manager := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "GET /channels/20":
					fmt.Fprint(w, `{"id":"20","guild_id":"10","type":0}`)
				case "GET /channels/20/permissions/member/99":
					teammateTestPermissions(w)
				case "GET /channels/20/messages/201":
					_ = json.NewEncoder(w).Encode(message)
				case "GET /channels/201":
					if !threadCreated.Load() {
						http.NotFound(w, r)
						return
					}
					fmt.Fprint(w, teammatePublic)
				case "POST /channels/20/messages/201/threads":
					if tc != "expiry while creating thread" {
						t.Errorf("created a thread for %s", tc)
					}
					threadCreated.Store(true)
					fmt.Fprint(w, teammatePublic)
				default:
					t.Errorf("unexpected REST route %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			})
			bridge.external = externalInputScope{GuildID: "10", ChannelID: "20", WebhookID: "89", Project: t.TempDir()}
			bridge.service.Now = func() time.Time {
				if threadCreated.Load() {
					return now.Add(time.Minute)
				}
				return now
			}
			externalTestRequest(t, bridge, http.MethodPost, "", "Bearer unauthorized", event, http.StatusForbidden)
			externalTestRequest(t, bridge, http.MethodGet, "?event_id=job-1", externalTestBearer, externalInput{}, http.StatusNotFound)
			externalTestRequest(t, bridge, http.MethodPost, "", externalTestBearer, event, http.StatusOK)
			switch tc {
			case "expired":
				now = now.Add(time.Minute)
			case "expiry while creating thread":
			case "wrong webhook":
				message.WebhookID = "93"
			case "edited content":
				message.Content = "Replacement announcement"
			case "deleted before dispatch":
				teammateTestEvent(t, bridge, 1, "MESSAGE_DELETE", map[string]string{"id": "201", "channel_id": "20"})
			}
			if tc != "deleted before dispatch" {
				rows, err := bridge.service.Store.teammatePending("install")
				if err != nil || len(rows) != 1 {
					t.Fatalf("pending %v: %v", rows, err)
				}
				if err = bridge.process(t.Context(), rows[0]); err != nil {
					t.Fatal(err)
				}
			}
			receipt := externalTestRequest(t, bridge, http.MethodGet, "?event_id=job-1", externalTestBearer, externalInput{}, http.StatusOK)
			if receipt.State != externalBlocked || len(manager.admitted) != 0 {
				t.Fatalf("receipt %v, native inputs %v", receipt, manager.admitted)
			}
		})
	}
}

func TestExternalAdmissionSocketDoesNotExposeOwnerControls(t *testing.T) {
	dir, err := os.MkdirTemp("", "swarm-external-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, externalInputSocket+".sock")
	ctx, cancel := context.WithCancel(t.Context())
	bridge, _ := teammateTestConnector(t, nil)
	done := make(chan error, 1)
	go func() {
		done <- serveLocalSocket(ctx, dir, externalInputSocket, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			bridge.externalHandler(externalTestBearer, w, r)
		}))
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://local/shutdown", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", externalTestBearer)
	client := &http.Client{Transport: transport, Timeout: time.Second}
	for deadline := time.Now().Add(time.Second); ; {
		res, err := client.Do(req)
		if err == nil {
			_ = res.Body.Close()
			if res.StatusCode != http.StatusNotFound {
				t.Fatalf("external socket exposed owner endpoint: %d", res.StatusCode)
			}
			info, err := os.Lstat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("external socket permissions: %v, %v", info, err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestExternalAdmissionEnrollmentIsOptInAndScoped(t *testing.T) {
	dir := t.TempDir()
	config := Config{
		Version: 1, Name: "Assistant", InstallationID: "install", ApplicationID: "99",
		Mode: "teammate", ManagerCLI: "claude", GuildIDs: []string{"10"}, ChannelIDs: []string{"20"},
		WorkDir: dir, Projects: map[string]string{"docs": dir},
		TokenFile: filepath.Join(dir, "bot-token"), DaemonSocket: filepath.Join(dir, "daemon.sock"),
	}
	bridge, _ := teammateTestConnector(t, nil)
	bridge.service.Config = config
	if bearer, err := bridge.externalInputAuth(); bearer != "" || err != nil {
		t.Fatalf("disabled external enrollment returned a credential: %v", err)
	}
	enroll := ExternalInputConfig{
		GuildID: "10", ChannelID: "20", WebhookID: "89", Project: "docs", TokenFile: filepath.Join(dir, "service-token"),
	}
	config.ExternalInput = &enroll
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []func(*ExternalInputConfig){
		func(e *ExternalInputConfig) { e.ChannelID = "21" },
		func(e *ExternalInputConfig) { e.GuildID = "11" },
		func(e *ExternalInputConfig) { e.Project = "another" },
		func(e *ExternalInputConfig) { e.TokenFile = config.TokenFile },
	} {
		bad := enroll
		tc(&bad)
		config.ExternalInput = &bad
		if err := config.Validate(); err == nil {
			t.Fatal("accepted unenrolled scope or shared credential")
		}
	}
	if err := os.WriteFile(enroll.TokenFile, []byte(strings.TrimPrefix(externalTestBearer, "Bearer ")), 0600); err != nil {
		t.Fatal(err)
	}
	config.ExternalInput = &enroll
	bridge.service.Config = config
	if bearer, err := bridge.externalInputAuth(); bearer != externalTestBearer || err != nil {
		t.Fatalf("cannot open enrolled external admission: %v", err)
	}
}
