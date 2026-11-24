package discord

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/workermail"
	"github.com/bwmarrin/discordgo"
	bolt "go.etcd.io/bbolt"
)

type fileOutboxTestManager struct {
	teammateTestManager
	data            [][]byte
	reads, releases int
}

func (m *fileOutboxTestManager) ReadPublicationFiles(context.Context, TeammateDiscussion, TeammatePublication) ([][]byte, error) {
	m.reads++
	return m.data, nil
}

func (m *fileOutboxTestManager) ReleasePublicationFiles(context.Context, TeammateDiscussion, TeammatePublication) error {
	m.releases++
	m.data = nil
	return nil
}

func outboxTestFile(id, name, contentType string, data []byte) schema.PublicationFile {
	sum := sha256.Sum256(data)
	return schema.PublicationFile{ID: id, Name: name, ContentType: contentType, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
}

func outboxTestScope(w http.ResponseWriter, r *http.Request, attach bool) bool {
	switch r.URL.Path {
	case "/channels/20":
		_, _ = fmt.Fprint(w, teammateParent)
	case "/channels/201":
		_, _ = fmt.Fprint(w, teammatePublic)
	case "/channels/20/permissions/member/99":
		bits := discordgo.PermissionViewChannel | discordgo.PermissionReadMessageHistory | discordgo.PermissionSendMessagesInThreads
		if attach {
			bits |= discordgo.PermissionAttachFiles
		}
		_, _ = fmt.Fprintf(w, `{"permissions":"%d"}`, bits)
	default:
		return false
	}
	return true
}

func bindFileOutboxTest(t *testing.T, c *teammateConnector) TeammateDiscussion {
	t.Helper()
	d := TeammateDiscussion{ID: "d1", InstallationID: "install", Route: Route{GuildID: "10", ChannelID: "201", ParentID: "20"}}
	if err := c.service.Store.teammateBind(teammateBinding{TeammateDiscussion: d}); err != nil {
		t.Fatal(err)
	}
	return d
}

func assertOutboxFileEvidence(t *testing.T, store *Store, d TeammateDiscussion, p TeammatePublication, expectedState string, messages, files []string) {
	t.Helper()
	pub := workermail.PublicationStatus{ID: p.ID}
	proof := p.Proof()
	err := store.db.View(func(tx *bolt.Tx) error {
		return readPublicationStatus(tx, d, &pub, &proof)
	})
	if err != nil || pub.State != expectedState || !slices.Equal(pub.MessageIDs, messages) || !slices.Equal(pub.AttachmentIDs, files) {
		t.Fatalf("receipt = %+v, error = %v", pub, err)
	}
}

func TestTeammateFilePublicationMultipartAndScopedMention(t *testing.T) {
	ctx := context.Background()
	data := [][]byte{[]byte("title,value\nplan,5\n"), []byte("%PDF-1.4\n")}
	p := TeammatePublication{
		ID: "files", AdmissionID: "input", Body: "Ready @everyone <@88>\n" + strings.Repeat("x", 1930), RecipientID: "77",
		Files: []schema.PublicationFile{
			outboxTestFile("file1", "table.csv", "text/csv", data[0]),
			outboxTestFile("file2", "notes.pdf", "application/pdf", data[1]),
		},
	}
	parts := p.chunks()
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "<@77> ") {
		t.Fatalf("bad rendering %+v", parts)
	}
	var calls atomic.Int32
	var firstMultipart []byte
	var firstType, nonce string
	c, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		if outboxTestScope(w, r, true) {
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/channels/201/messages" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
			return
		}
		n := calls.Add(1)
		var payload struct {
			Content         string
			Nonce           string
			EnforceNonce    bool `json:"enforce_nonce"`
			AllowedMentions struct {
				Parse       []string
				Users       []string
				RepliedUser bool `json:"replied_user"`
			} `json:"allowed_mentions"`
			Attachments []struct {
				ID       int
				Filename string
			}
		}
		if n <= 2 {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			if n == 1 {
				firstMultipart, firstType = raw, r.Header.Get("Content-Type")
			} else if !bytes.Equal(raw, firstMultipart) || firstType != r.Header.Get("Content-Type") {
				t.Error("429 did not reuse the same encoded request")
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			multipart, err := r.MultipartReader()
			if err != nil {
				t.Error(err)
				return
			}
			jsonPart, err := multipart.NextPart()
			if err != nil || jsonPart.FormName() != "payload_json" || json.NewDecoder(jsonPart).Decode(&payload) != nil {
				t.Errorf("missing payload_json: %v", err)
				return
			}
			if len(payload.Attachments) != len(data) {
				t.Errorf("attachment placeholders = %+v", payload.Attachments)
				return
			}
			for i, f := range p.Files {
				filePart, err := multipart.NextPart()
				if err != nil {
					t.Error(err)
					return
				}
				got, err := io.ReadAll(filePart)
				if err != nil || !bytes.Equal(got, data[i]) || filePart.FormName() != fmt.Sprintf("files[%d]", i) ||
					filePart.FileName() != f.Name || filePart.Header.Get("Content-Type") != f.ContentType ||
					payload.Attachments[i].ID != i || payload.Attachments[i].Filename != f.Name {
					t.Errorf("wrong file upload at position %d: %v", i, err)
				}
			}
			if _, err := multipart.NextPart(); !errors.Is(err, io.EOF) {
				t.Errorf("extra multipart data: %v", err)
			}
			if payload.Content != parts[0] || !payload.EnforceNonce || payload.Nonce == "" || payload.AllowedMentions.Parse == nil ||
				len(payload.AllowedMentions.Parse) != 0 || !slices.Equal(payload.AllowedMentions.Users, []string{"77"}) || payload.AllowedMentions.RepliedUser {
				t.Errorf("first message allowlist or nonce not scoped: %+v", payload)
			}
			if n == 1 {
				nonce = payload.Nonce
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"retry_after":0.001}`)
				return
			}
			if nonce != payload.Nonce {
				t.Error("nonce changed after 429")
			}
		} else {
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
				return
			}
			if n != 3 || payload.Content != parts[1] || len(payload.Attachments) != 0 || len(payload.AllowedMentions.Users) != 0 ||
				len(payload.AllowedMentions.Parse) != 0 || payload.AllowedMentions.RepliedUser || payload.Nonce == nonce || payload.Nonce == "" {
				t.Errorf("subsequent chunk repeated files or mention: %+v", payload)
			}
		}
		message := discordgo.Message{ID: "301", ChannelID: "201", Author: &discordgo.User{ID: "99"}, Content: payload.Content}
		if n == 2 {
			message.Attachments = []*discordgo.MessageAttachment{
				{ID: "401", Filename: p.Files[0].Name, Size: len(data[0])},
				{ID: "402", Filename: p.Files[1].Name, Size: len(data[1])},
			}
		} else {
			message.ID = "302"
		}
		_ = json.NewEncoder(w).Encode(message)
	})
	d := bindFileOutboxTest(t, c)
	mgr := &fileOutboxTestManager{data: append([][]byte(nil), data...)}
	c.manager = mgr
	if err := c.queuePublication(ctx, d, p); err != nil {
		t.Fatal(err)
	}
	if err := c.queuePublication(ctx, d, p); err != nil || mgr.reads != 1 || mgr.releases != 2 {
		t.Fatalf("queue replay must not fetch released bytes: reads=%d releases=%d err=%v", mgr.reads, mgr.releases, err)
	}
	other := p
	other.ID = "files-distinct"
	other.Files = []schema.PublicationFile{outboxTestFile("new", p.Files[0].Name, p.Files[0].ContentType, []byte("changed"))}
	if other.Proof().BodyHash == p.Proof().BodyHash {
		t.Fatal("identical caption hid a different artifact")
	}
	other.ID = p.ID
	if err := c.queuePublication(ctx, d, other); !errors.Is(err, ErrConflict) || mgr.reads != 1 {
		t.Fatalf("mutated retry must be rejected before fetching bytes: %v", err)
	}
	if err := c.sendPending(ctx, d); err != nil || calls.Load() != 3 {
		t.Fatalf("delivery got %d calls: %v", calls.Load(), err)
	}
	assertOutboxFileEvidence(t, c.service.Store, d, p, StateSent, []string{"301", "302"}, []string{"401", "402"})
	if err := c.service.Store.teammateAckOutputs(d, []string{p.ID}); err != nil {
		t.Fatal(err)
	}
	if err := c.service.Store.teammateCompactOutbox(d, time.Now().Add(GuardTTL+time.Second), nil); err != nil {
		t.Fatal(err)
	}
	assertOutboxFileEvidence(t, c.service.Store, d, p, StateSent, []string{"301", "302"}, []string{"401", "402"})
	if err := c.queuePublication(ctx, d, p); err != nil || mgr.reads != 1 {
		t.Fatalf("compacted replay read source bytes: %v", err)
	}
	if err := c.sendPending(ctx, d); err != nil || calls.Load() != 3 {
		t.Fatalf("compacted replay reposted: posts=%d err=%v", calls.Load(), err)
	}
}

func TestTeammateFilePublicationResumesReceiptAfterRestart(t *testing.T) {
	ctx := context.Background()
	raw := []byte("name,value\nreport,5\n")
	p := TeammatePublication{ID: "file-only", AdmissionID: "input", Files: []schema.PublicationFile{outboxTestFile("file1", "report.csv", "text/csv", raw)}}
	var allowAttach atomic.Bool
	var posts atomic.Int32
	c, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		if outboxTestScope(w, r, allowAttach.Load()) {
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/channels/201/messages" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
			return
		}
		posts.Add(1)
		// Simulate Discord accepting bytes before the connector loses its response.
		w.WriteHeader(500)
	})
	mgr := &fileOutboxTestManager{data: [][]byte{raw}}
	c.manager = mgr
	d := bindFileOutboxTest(t, c)
	store := c.service.Store
	if err := c.queuePublication(ctx, d, p); err != nil {
		t.Fatal(err)
	}
	if err := c.sendPending(ctx, d); !errors.Is(err, errDiscordAttachFiles) || posts.Load() != 0 {
		t.Fatalf("missing Attach Files permission started a POST: %v", err)
	}
	assertOutboxFileEvidence(t, store, d, p, StatePrepared, nil, nil)
	allowAttach.Store(true)
	if err := c.sendPending(ctx, d); !teammateAPIStatus(err, http.StatusInternalServerError) || posts.Load() != 1 {
		t.Fatalf("expected one attempted upload with lost response: %v", err)
	}
	path := filepath.Dir(store.db.Path())
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	c.service.Store = reopened
	c.service.Now = func() time.Time { return time.Now().Add(teammateNonceWindow + time.Second) }
	if err := c.sendPending(ctx, d); !errors.Is(err, ErrDeliveryUncertain) || posts.Load() != 1 {
		t.Fatalf("restart repeated an uncertain upload: posts=%d err=%v", posts.Load(), err)
	}
	pending, err := reopened.teammateOutputs(d.InstallationID, d.ID)
	if err != nil || len(pending) != 1 || !strings.Contains(pending[0].Error, "Check the discussion") {
		t.Fatalf("missing actionable uncertainty %+v: %v", pending, err)
	}
	pinned, err := reopened.readFilePayload(d.InstallationID, pending[0])
	if err != nil || len(pinned) != 1 || !bytes.Equal(pinned[0], raw) {
		t.Fatalf("file custody lost after restart: %v", err)
	}
	nonce := pending[0].Chunks[0].ID
	message := map[string]any{
		"id": "301", "channel_id": "201", "guild_id": "10", "content": "", "nonce": nonce, "author": map[string]string{"id": "99"},
		"attachments": []map[string]any{{"id": "401", "filename": "other.csv", "size": len(raw)}},
	}
	teammateTestEvent(t, c, 1, "MESSAGE_CREATE", message)
	assertOutboxFileEvidence(t, reopened, d, p, StateUncertain, nil, nil)
	message["attachments"] = []map[string]any{{"id": "401", "filename": p.Files[0].Name, "size": len(raw)}}
	message["channel_id"] = "202"
	teammateTestEvent(t, c, 2, "MESSAGE_CREATE", message)
	assertOutboxFileEvidence(t, reopened, d, p, StateUncertain, nil, nil)
	message["channel_id"] = "201"
	teammateTestEvent(t, c, 3, "MESSAGE_CREATE", message)
	assertOutboxFileEvidence(t, reopened, d, p, StateSent, []string{"301"}, []string{"401"})
	teammateTestEvent(t, c, 4, "MESSAGE_CREATE", message)
	assertOutboxFileEvidence(t, reopened, d, p, StateSent, []string{"301"}, []string{"401"})
	pinned, err = reopened.readFilePayload(d.InstallationID, pending[0])
	if err != nil || len(pinned) != 0 {
		t.Fatalf("settled bytes still pinned: %v", err)
	}
	if err := c.sendPending(ctx, d); err != nil || posts.Load() != 1 {
		t.Fatalf("reconciled upload resent: posts=%d err=%v", posts.Load(), err)
	}
}

func TestTeammateMentionQuestionWaitsForVerifiedReceipt(t *testing.T) {
	ctx := context.Background()
	p := TeammatePublication{ID: "question", AdmissionID: "input", QuestionID: "q", QuestionHash: "answer-schema", Body: "Which report?", RecipientID: "77"}
	var posts atomic.Int32
	var first string
	c, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		if outboxTestScope(w, r, false) {
			return
		}
		var input struct {
			Nonce, Content  string
			AllowedMentions discordgo.MessageAllowedMentions `json:"allowed_mentions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
			return
		}
		if input.Content != "<@77> Which report?" || !slices.Equal(input.AllowedMentions.Users, []string{"77"}) || input.AllowedMentions.RepliedUser ||
			input.AllowedMentions.Parse == nil || len(input.AllowedMentions.Parse) != 0 || input.Nonce == "" {
			t.Errorf("question mention changed scope: %+v", input)
		}
		n := posts.Add(1)
		if n == 1 {
			first = input.Nonce
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"retry_after":0.001}`)
			return
		}
		if input.Nonce != first {
			t.Error("question retried with a different nonce")
		}
		w.WriteHeader(500)
	})
	d := bindFileOutboxTest(t, c)
	if err := c.queuePublication(ctx, d, p); err != nil {
		t.Fatal(err)
	}
	if err := c.sendPending(ctx, d); !teammateAPIStatus(err, 500) {
		t.Fatalf("first send lost ack: %v", err)
	}
	if err := c.sendPending(ctx, d); !errors.Is(err, ErrDeliveryUncertain) || posts.Load() != 2 {
		t.Fatalf("uncertain mention was resent: %v", err)
	}
	pending, err := c.service.Store.teammateOutputs(d.InstallationID, d.ID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("missing uncertain output: %+v %v", pending, err)
	}
	message := map[string]any{
		"id": "301", "channel_id": "201", "guild_id": "10", "content": p.chunks()[0], "nonce": pending[0].Chunks[0].ID,
		"author": map[string]string{"id": "99"},
	}
	teammateTestEvent(t, c, 1, "MESSAGE_CREATE", message)
	got, hash, err := c.service.Store.teammateQuestion(d, "301")
	if err != nil || got != p.QuestionID || hash != p.QuestionHash {
		t.Fatalf("quoted question lost: %q %q %v", got, hash, err)
	}
}

func TestPersonalFilePublicationReconcilesFromSameDMHistory(t *testing.T) {
	raw := []byte("name,value\nreport,7\n")
	p := TeammatePublication{ID: "owner-result", AdmissionID: "input", Files: []schema.PublicationFile{outboxTestFile("file1", "report.csv", "text/csv", raw)}}
	var posts atomic.Int32
	var nonce atomic.Value
	c, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /channels/20":
			_, _ = fmt.Fprint(w, personalFixture)
		case "POST /channels/20/messages":
			posts.Add(1)
			if err := r.ParseMultipartForm(1024); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			var payload struct{ Nonce, Content string }
			if err := json.Unmarshal([]byte(r.FormValue("payload_json")), &payload); err != nil || payload.Nonce == "" || payload.Content != "" {
				t.Errorf("invalid attachment-only request: %+v %v", payload, err)
			}
			nonce.Store(payload.Nonce)
			w.WriteHeader(500)
		case "GET /channels/20/messages":
			_, _ = fmt.Fprintf(w, `[{"id":"301","channel_id":"20","author":{"id":"99","bot":true},"content":"","nonce":%q,"attachments":[{"id":"401","filename":"report.csv","size":%d}]}]`, nonce.Load(), len(raw))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	cfg := &c.service.Config
	cfg.Mode, cfg.OwnerID, cfg.DMChannelID = "personal", "30", "20"
	cfg.GuildIDs, cfg.UserIDs = nil, nil
	c.manager = &fileOutboxTestManager{data: [][]byte{raw}}
	b := teammateBinding{
		TeammateDiscussion: TeammateDiscussion{ID: "existing", InstallationID: cfg.InstallationID, Route: Route{ChannelID: "20"}},
		Opened:             true, Boundary: "200", RepairLower: "200", RepairTargetEpoch: 1, PersonalScanEpoch: 1,
	}
	if err := c.service.Store.teammateBind(b); err != nil {
		t.Fatal(err)
	}
	if err := c.queuePublication(t.Context(), b.TeammateDiscussion, p); err != nil {
		t.Fatal(err)
	}
	if err := c.sendPending(t.Context(), b.TeammateDiscussion); !teammateAPIStatus(err, 500) || posts.Load() != 1 {
		t.Fatalf("same DM upload did not reach the API: posts=%d err=%v", posts.Load(), err)
	}
	if err := c.repair(t.Context(), b, 1); err != nil {
		t.Fatal(err)
	}
	assertOutboxFileEvidence(t, c.service.Store, b.TeammateDiscussion, p, StateSent, []string{"301"}, []string{"401"})
	if err := c.sendPending(t.Context(), b.TeammateDiscussion); err != nil || posts.Load() != 1 {
		t.Fatalf("history-reconciled upload was resent: posts=%d err=%v", posts.Load(), err)
	}
}
