package discord

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

type attachmentTransport func(*http.Request) (*http.Response, error)

func (f attachmentTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPersonalAttachmentOnlyHandoff(t *testing.T) {
	var marker [8]byte
	if _, err := rand.Read(marker[:]); err != nil {
		t.Fatal(err)
	}
	doc := []byte(hex.EncodeToString(marker[:]))
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	pixel := color.RGBA{marker[0], marker[1], marker[2], 255}
	img.Set(0, 0, pixel)
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	msg := discordgo.Message{ID: "200", ChannelID: "20", Author: &discordgo.User{ID: "30", Username: "fixture"}}
	for i, b := range [][]byte{doc, encoded.Bytes()} {
		id := fmt.Sprint(40 + i)
		name := []string{"../../note.txt", "photo.png"}[i]
		msg.Attachments = append(msg.Attachments, &discordgo.MessageAttachment{ID: id, Filename: name, Size: len(b), ContentType: []string{"text/plain", "image/png"}[i], URL: "https://cdn.discordapp.com/attachments/20/" + id + "/opaque?token=expired"})
	}
	conn, manager := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/20":
			_, _ = fmt.Fprint(w, personalFixture)
		case "/channels/20/messages/200":
			_ = json.NewEncoder(w).Encode(msg)
		default:
			t.Errorf("unexpected REST %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	fixtureDir := os.Getenv("SWARM_TEAMMATE_TEST_ATTACHMENT_FIXTURE")
	if fixtureDir != "" {
		if !filepath.IsAbs(fixtureDir) {
			t.Fatal("fixture directory must be absolute")
		}
		store, err := OpenStore(filepath.Join(fixtureDir, "connector"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		conn.service.Store = store
	}
	cfg := &conn.service.Config
	cfg.Mode, cfg.OwnerID, cfg.DMChannelID, cfg.ProjectAccess = "personal", "30", "20", ProjectAccessVM
	cdn := 0
	pngUnavailable := true
	conn.client.http.Transport = attachmentTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "cdn.discordapp.com" {
			return http.DefaultTransport.RoundTrip(r)
		}
		if r.Header.Get("Authorization") != "" {
			t.Fatal("bot token on attachment GET")
		}
		cdn++
		status := 200
		var data []byte
		switch {
		case strings.HasSuffix(r.URL.Path, "40/opaque") && r.URL.Query().Get("token") == "expired":
			status = 403
			refreshed := *msg.Attachments[0]
			refreshed.URL = strings.Replace(refreshed.URL, "expired", "fresh", 1)
			msg.Attachments[0] = &refreshed
		case strings.HasSuffix(r.URL.Path, "40/opaque"):
			data = doc
		default:
			if pngUnavailable {
				status = http.StatusServiceUnavailable
				pngUnavailable = false
			} else {
				data = encoded.Bytes()
			}
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data))}, nil
	})
	teammateTestEvent(t, conn, 1, "MESSAGE_CREATE", msg)
	pending, err := conn.service.Store.teammatePending(cfg.InstallationID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("queue %v %v", pending, err)
	}
	if err = conn.process(context.Background(), pending[0]); !errors.Is(err, errAttachmentRetry) || len(manager.admitted) != 0 {
		t.Fatalf("transient outage lost original admission: %v", err)
	}
	dir := filepath.Dir(conn.service.Store.db.Path())
	if err = conn.service.Store.Close(); err != nil {
		t.Fatal(err)
	}
	conn.service.Store, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.service.Store.Close() })
	pending, err = conn.service.Store.teammatePending(cfg.InstallationID)
	if err != nil || len(pending) != 1 || pending[0].Route.MessageID != "200" {
		t.Fatalf("original DM not pending after restart: %v %v", pending, err)
	}
	if err = conn.process(context.Background(), pending[0]); err != nil {
		t.Fatal(err)
	}
	if len(manager.admitted) != 1 || cdn != 4 {
		t.Fatalf("admissions %d downloads %d", len(manager.admitted), cdn)
	}
	a := manager.admitted[0]
	if a.Text != "" || !filepath.IsAbs(a.AttachmentManifest) {
		t.Fatalf("missing attachment-only handoff: %+v", a)
	}
	raw, err := os.ReadFile(a.AttachmentManifest)
	if err != nil {
		t.Fatal(err)
	}
	var files []attachmentFile
	if json.Unmarshal(raw, &files) != nil || len(files) != 2 || strings.Contains(string(raw), "token=") {
		t.Fatal("invalid manifest")
	}
	for i, expected := range [][]byte{doc, encoded.Bytes()} {
		if files[i].File != fmt.Sprintf("file-%d", i+1) {
			t.Fatalf("unexpected local path %q", files[i].File)
		}
		got, err := os.ReadFile(filepath.Join(filepath.Dir(a.AttachmentManifest), files[i].File))
		if err != nil || !bytes.Equal(got, expected) {
			t.Fatal("download mismatch", err)
		}
	}
	// Simulate a later handoff after all signed URLs cease working.
	conn.client.http.Transport = attachmentTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("replayed attachment fetched from network")
		return nil, nil
	})
	replay := a
	if err := conn.captureAttachments(context.Background(), &replay, &msg); err != nil || replay.AttachmentManifest != a.AttachmentManifest {
		t.Fatal("snapshot lost on replay", replay.AttachmentManifest, err)
	}
	if fixtureDir != "" {
		fixture := struct{ Manifest, Document, Pixel string }{a.AttachmentManifest, string(doc), fmt.Sprintf("%02x%02x%02x", pixel.R, pixel.G, pixel.B)}
		raw, err := json.Marshal(fixture)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(fixtureDir, "expected.json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}

	foreign := a
	foreign.Route.ChannelID = "21"
	if err := conn.captureAttachments(context.Background(), &foreign, &msg); err == nil {
		t.Fatal("foreign route read")
	}
}

func TestPersonalAttachmentFailuresAndLimits(t *testing.T) {
	conn, _ := teammateTestConnector(t, func(http.ResponseWriter, *http.Request) { t.Fatal("unexpected REST request") })
	cfg := &conn.service.Config
	cfg.Mode, cfg.OwnerID, cfg.DMChannelID, cfg.ProjectAccess = "personal", "30", "20", ProjectAccessVM
	m := &discordgo.Message{ID: "200", ChannelID: "20", Author: &discordgo.User{ID: "30"}}
	add := func(id, name, url string, size int) {
		m.Attachments = append(m.Attachments, &discordgo.MessageAttachment{ID: id, Filename: name, URL: url, Size: size})
	}
	add("40", "oversized", "https://cdn.discordapp.com/attachments/20/40/test", attachmentMaxFile+1)
	add("41", "foreign", "https://cdn.discordapp.com/attachments/21/41/test", 5)
	add("42", "redirect", "https://cdn.discordapp.com/attachments/20/42/test", 5)
	add("43", "short", "https://cdn.discordapp.com/attachments/20/43/test", 5)
	add("44", "other", "https://example.org/private", 5)
	conn.client.http.Transport = attachmentTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "cdn.discordapp.com" {
			t.Fatal("unexpected destination")
		}
		status := 200
		switch r.URL.Path {
		case "/attachments/20/42/test":
			status = 302
		case "/attachments/20/43/test":
		default:
			t.Fatal("unexpected fetch", r.URL.Path)
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("bad")), Header: http.Header{"Location": []string{"https://example.org/private"}}, ContentLength: -1}, nil
	})
	a := TeammateAdmission{Author: Actor{ID: "30"}, Discussion: TeammateDiscussion{ID: "d", InstallationID: cfg.InstallationID, Route: Route{ChannelID: "20"}}, Route: Route{ChannelID: "20", MessageID: "200"}, Revision: teammateRevision(m)}
	err := conn.captureAttachments(context.Background(), &a, m)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(a.AttachmentManifest)
	if err != nil {
		t.Fatal(err)
	}
	var files []attachmentFile
	if err = json.Unmarshal(raw, &files); err != nil || len(files) != 5 {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Unavailable == "" || f.File != "" {
			t.Fatalf("unavailable file reported as delivered: %+v", f)
		}
	}
	dir, err := os.ReadDir(filepath.Dir(a.AttachmentManifest))
	if err != nil || len(dir) != 2 || dir[0].Name() != ".discussion" || dir[1].Name() != "files.json" {
		t.Fatalf("partial files retained: %v %v", dir, err)
	}
}

func TestPersonalAttachmentOutageNotifiesOnceAndReleasesFIFO(t *testing.T) {
	m := discordgo.Message{ID: "200", ChannelID: "20", Author: &discordgo.User{ID: "30"}, Attachments: []*discordgo.MessageAttachment{{ID: "40", Filename: "sample", Size: 7, URL: "https://cdn.discordapp.com/attachments/20/40/sample"}}}
	var posts int
	conn, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /channels/20":
			_, _ = fmt.Fprint(w, personalFixture)
		case "POST /channels/20/messages":
			posts++
			var p struct{ Content string }
			if json.NewDecoder(r.Body).Decode(&p) != nil || !strings.Contains(p.Content, "kept your request") || !strings.Contains(p.Content, "reattach") {
				t.Error("missing actionable attachment notice")
			}
			_ = json.NewEncoder(w).Encode(discordgo.Message{ID: "500", ChannelID: "20", Author: &discordgo.User{ID: "99", Bot: true}, Content: p.Content})
		default:
			t.Fatalf("unexpected API request %v", r.URL)
		}
	})
	cfg := &conn.service.Config
	cfg.Mode, cfg.OwnerID, cfg.DMChannelID, cfg.ProjectAccess = "personal", "30", "20", ProjectAccessVM
	now := time.Now()
	conn.service.Now = func() time.Time { return now }
	var attempts int
	conn.client.http.Transport = attachmentTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "cdn.discordapp.com" {
			return http.DefaultTransport.RoundTrip(r)
		}
		attempts++
		return &http.Response{StatusCode: 502, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	a := TeammateAdmission{Author: Actor{ID: "30"}, Discussion: TeammateDiscussion{ID: "d", InstallationID: cfg.InstallationID, Route: Route{ChannelID: "20"}}, Route: Route{ChannelID: "20", MessageID: "200"}, Revision: teammateRevision(&m)}
	for range 2 {
		if err := conn.captureAttachments(t.Context(), &a, &m); !errors.Is(err, errAttachmentRetry) {
			t.Fatal(err)
		}
		outs, err := conn.service.Store.teammateOutputs(cfg.InstallationID, "")
		if err != nil || len(outs) > 1 {
			t.Fatal("notice duplicated", err)
		}
		for _, out := range outs {
			if err = conn.sendOutput(t.Context(), cfg.InstallationID, out); err != nil {
				t.Fatal(err)
			}
		}
	}
	if posts != 1 {
		t.Fatalf("expected one notice, got %d", posts)
	}
	now = now.Add(attachmentRetryWindow + time.Second)
	err := conn.captureAttachments(t.Context(), &a, &m)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(a.AttachmentManifest)
	if err != nil || !strings.Contains(string(raw), "Reattach") || attempts != 2 {
		t.Fatalf("outage did not release original request after bounded retries: %s, %d, %v", raw, attempts, err)
	}
}

func TestGuildAttachmentIngressUsesVisibleForwardAndReply(t *testing.T) {
	var marker [8]byte
	if _, err := rand.Read(marker[:]); err != nil {
		t.Fatal(err)
	}
	var picture bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	pixel := color.RGBA{marker[0], marker[1], marker[2], 255}
	img.Set(0, 0, pixel)
	if err := png.Encode(&picture, img); err != nil {
		t.Fatal(err)
	}
	doc := []byte(hex.EncodeToString(marker[:]))
	attach := func(id, name, kind, channel, query string, body []byte) *discordgo.MessageAttachment {
		return &discordgo.MessageAttachment{ID: id, Filename: name, ContentType: kind, Size: len(body), URL: "https://cdn.discordapp.com/attachments/" + channel + "/" + id + "/" + name + "?" + query}
	}
	forward := teammateTestMessage("230", "20", "88", "", false)
	forward.MessageReference = &discordgo.MessageReference{Type: discordgo.MessageReferenceTypeForward, GuildID: "11", ChannelID: "66", MessageID: "91"}
	forward.MessageSnapshots = []discordgo.MessageSnapshot{{Message: &discordgo.Message{
		Content:     "<@99> quoted instruction with transaction date",
		Mentions:    []*discordgo.User{{ID: "99"}},
		Attachments: []*discordgo.MessageAttachment{attach("310", "date.png", "image/png", "20", "signed=expired", picture.Bytes())},
	}}}
	reply := teammateTestMessage("240", "20", "77", "<@99> Check the screenshot please", true)
	reply.Type = discordgo.MessageTypeReply
	reply.MessageReference = &discordgo.MessageReference{ChannelID: "20", GuildID: "10", MessageID: forward.ID}
	reply.Attachments = []*discordgo.MessageAttachment{attach("319", "reply.csv", "text/csv", "20", "signed=valid", doc)}
	direct := teammateTestMessage("250", "240", "88", "", false)
	direct.Attachments = []*discordgo.MessageAttachment{attach("320", "sample.csv", "text/csv", "240", "signed=valid", doc)}
	deleted := teammateTestMessage("260", "240", "88", "<@99> Check this other file", true)
	deleted.Type = discordgo.MessageTypeReply
	deleted.MessageReference = &discordgo.MessageReference{ChannelID: "240", GuildID: "10", MessageID: "251"}
	foreignReply := teammateTestMessage("270", "240", "88", "<@99> Check the earlier mention", true)
	foreignReply.Type = discordgo.MessageTypeReply
	foreignReply.MessageReference = &discordgo.MessageReference{ChannelID: "66", GuildID: "11", MessageID: "91"}
	foreignReply.ReferencedMessage = forward
	withoutPermission := teammateTestMessage("280", "240", "88", "", false)
	withoutPermission.Attachments = direct.Attachments
	nativeForward := teammateTestMessage("281", "240", "88", "", false)
	nativeForward.MessageReference = forward.MessageReference
	nativeForward.MessageSnapshots = []discordgo.MessageSnapshot{{Message: &discordgo.Message{
		Content:     forward.MessageSnapshots[0].Message.Content,
		Mentions:    forward.MessageSnapshots[0].Message.Mentions,
		Attachments: []*discordgo.MessageAttachment{attach("321", "date.png", "image/png", "240", "signed=valid", picture.Bytes())},
	}}}
	directReply := teammateTestMessage("282", "240", "88", "Review the CSV here", false)
	directReply.Type = discordgo.MessageTypeReply
	directReply.MessageReference = &discordgo.MessageReference{ChannelID: "240", GuildID: "10", MessageID: direct.ID}

	var visibleReads, brokenReads int
	bridge, manager := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/channels/20":
			_, _ = fmt.Fprint(w, teammateParent)
		case "/channels/20/permissions/member/99":
			teammateTestPermissions(w)
		case "/channels/20/messages/240":
			_ = json.NewEncoder(w).Encode(reply)
		case "/channels/20/messages/230":
			visibleReads++
			state := "signed=expired"
			if visibleReads > 1 {
				state = "signed=valid"
			}
			shown := *forward
			snapshot := *forward.MessageSnapshots[0].Message
			snapshot.Attachments = []*discordgo.MessageAttachment{attach("310", "date.png", "image/png", "20", state, picture.Bytes())}
			shown.MessageSnapshots = []discordgo.MessageSnapshot{{Message: &snapshot}}
			_ = json.NewEncoder(w).Encode(&shown)
		case "/channels/240":
			_, _ = fmt.Fprint(w, strings.ReplaceAll(teammatePublic, "201", "240"))
		case "/channels/240/messages/250":
			_ = json.NewEncoder(w).Encode(direct)
		case "/channels/240/messages/251":
			brokenReads++
			w.WriteHeader(http.StatusNotFound)
		case "/channels/240/messages/260":
			_ = json.NewEncoder(w).Encode(deleted)
		case "/channels/240/messages/270":
			_ = json.NewEncoder(w).Encode(foreignReply)
		case "/channels/240/messages/280":
			_ = json.NewEncoder(w).Encode(withoutPermission)
		case "/channels/240/messages/281":
			_ = json.NewEncoder(w).Encode(nativeForward)
		case "/channels/240/messages/282":
			_ = json.NewEncoder(w).Encode(directReply)
		default:
			t.Errorf("unexpected API route %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	cfg := &bridge.service.Config
	cfg.ChannelIDs = []string{"20"}
	cfg.ProjectAccess = ProjectAccessVM
	var imageDownloads, docDownloads int
	bridge.client.http.Transport = attachmentTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "cdn.discordapp.com" {
			return http.DefaultTransport.RoundTrip(r)
		}
		if r.Header.Get("Authorization") != "" {
			t.Fatal("Discord token passed to the CDN")
		}
		var body []byte
		status := http.StatusOK
		switch r.URL.Path {
		case "/attachments/20/310/date.png":
			imageDownloads++
			if r.URL.Query().Get("signed") == "expired" {
				status = http.StatusForbidden
			} else {
				body = picture.Bytes()
			}
		case "/attachments/240/321/date.png":
			imageDownloads++
			body = picture.Bytes()
		case "/attachments/240/320/sample.csv", "/attachments/20/319/reply.csv":
			docDownloads++
			body = doc
		default:
			t.Fatalf("unexpected CDN route %s", r.URL.Path)
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body))}, nil
	})

	teammateTestEvent(t, bridge, 1, "MESSAGE_CREATE", forward)
	if pending, err := bridge.service.Store.teammatePending("install"); err != nil || len(pending) != 0 {
		t.Fatalf("quoted mention opened a discussion: %v %v", pending, err)
	}
	teammateTestEvent(t, bridge, 2, "MESSAGE_CREATE", reply)
	teammateTestEvent(t, bridge, 3, "MESSAGE_CREATE", direct)
	// An attachment-only message is admitted once its discussion is open.
	process := func() {
		t.Helper()
		pending, err := bridge.service.Store.teammatePending("install")
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range pending {
			if err := bridge.process(t.Context(), item); err != nil {
				t.Fatal(err)
			}
		}
	}
	process()
	teammateTestEvent(t, bridge, 4, "MESSAGE_CREATE", direct)
	teammateTestEvent(t, bridge, 5, "MESSAGE_CREATE", deleted)
	teammateTestEvent(t, bridge, 6, "MESSAGE_CREATE", foreignReply)
	process()
	cfg.ProjectAccess = ProjectAccessEnrolled
	teammateTestEvent(t, bridge, 7, "MESSAGE_CREATE", withoutPermission)
	process()
	cfg.ProjectAccess = ProjectAccessVM
	teammateTestEvent(t, bridge, 8, "MESSAGE_CREATE", nativeForward)
	teammateTestEvent(t, bridge, 9, "MESSAGE_CREATE", directReply)
	process()
	if len(manager.admitted) != 7 || imageDownloads != 3 || visibleReads != 2 || docDownloads != 3 || brokenReads != 1 {
		t.Fatalf("admissions=%d image=%d visible=%d document=%d missing=%d", len(manager.admitted), imageDownloads, visibleReads, docDownloads, brokenReads)
	}
	first, second, third, fourth, fifth := manager.admitted[0], manager.admitted[1], manager.admitted[2], manager.admitted[3], manager.admitted[4]
	if first.Text != reply.Content || len(first.ForwardedContext) != 1 || first.ForwardedContext[0].QuotedText != forward.MessageSnapshots[0].Message.Content ||
		second.Text != "" || first.Discussion.ID != second.Discussion.ID {
		t.Fatalf("reply and direct admission context: %+v %+v", first, second)
	}
	if !strings.Contains(third.AttachmentNotice, "reattach") || fourth.AttachmentManifest != "" || len(fourth.ForwardedContext) != 0 ||
		!strings.Contains(fifth.AttachmentManifest, "enrolled-project") {
		t.Fatalf("restricted access results: %+v %+v %+v", third, fourth, fifth)
	}
	sixth, seventh := manager.admitted[5], manager.admitted[6]
	if sixth.Text != "" || sixth.ExpectReply || sixth.Discussion.ID != first.Discussion.ID ||
		len(sixth.ForwardedContext) != 1 || !filepath.IsAbs(sixth.AttachmentManifest) ||
		seventh.Text != directReply.Content || len(seventh.ForwardedContext) != 0 || !filepath.IsAbs(seventh.AttachmentManifest) {
		t.Fatalf("direct forward and reply-to-upload context: %+v %+v", sixth, seventh)
	}
	for i, a := range []TeammateAdmission{first, second} {
		manifest, err := os.ReadFile(a.AttachmentManifest)
		if err != nil || bytes.Contains(manifest, []byte("signed")) || bytes.Contains(manifest, []byte("http")) {
			t.Fatalf("bad manifest evidence %v", err)
		}
		var files []attachmentFile
		err = json.Unmarshal(manifest, &files)
		count := []int{2, 1}[i]
		if err != nil || len(files) != count || files[0].Forwarded || files[0].MessageID != []string{"240", "250"}[i] || files[0].Unavailable != "" {
			t.Fatalf("unexpected manifest %s %v", manifest, err)
		}
		if count == 2 && (!files[1].Forwarded || files[1].MessageID != "230" || files[1].Unavailable != "") {
			t.Fatalf("unexpected forwarded part %s", manifest)
		}
		for j, file := range files {
			raw, err := os.ReadFile(filepath.Join(filepath.Dir(a.AttachmentManifest), file.File))
			if err != nil || !bytes.Equal(raw, [][]byte{doc, picture.Bytes()}[j]) {
				t.Fatalf("worker's artifact changed %v", err)
			}
		}
	}
	if fixtureDir := os.Getenv("SWARM_TEAMMATE_TEST_GUILD_ATTACHMENT_FIXTURE"); fixtureDir != "" {
		writeGuildAttachmentFixture(t, fixtureDir, first.AttachmentManifest, string(doc), fmt.Sprintf("%02x%02x%02x", pixel.R, pixel.G, pixel.B))
	}
	entry := teammateEvidence(forward)
	if entry.Text != "" || entry.ReplyTo != "" || len(entry.Attachments) != 0 || len(entry.Forwarded) != 1 || len(entry.Forwarded[0].Attachments) != 1 {
		t.Fatalf("unexpected forward projection %+v", entry)
	}
	metadata, _ := json.Marshal(entry)
	if bytes.Contains(metadata, []byte("signed")) || bytes.Contains(metadata, []byte("https:")) {
		t.Fatalf("history leaked CDN URL %s", metadata)
	}
}

func writeGuildAttachmentFixture(t *testing.T, dir, manifest, doc, pixel string) {
	t.Helper()
	if !filepath.IsAbs(dir) {
		t.Fatal("fixture path must be absolute")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"files.json", "file-1.csv", "file-2.png"} {
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(manifest), name))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(struct{ Manifest, Document, Pixel string }{filepath.Join(dir, "files.json"), doc, pixel})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "expected.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestGuildAttachmentRetentionThroughActiveDiscussion(t *testing.T) {
	bridge, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected REST reads: %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	bridge.service.Config.ProjectAccess = ProjectAccessVM
	now := time.Now()
	bridge.service.Now = func() time.Time { return now }
	bridge.client.http.Transport = attachmentTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, ContentLength: 1, Body: io.NopCloser(strings.NewReader("a"))}, nil
	})
	current := TeammateDiscussion{ID: "original", InstallationID: "install", Route: Route{ChannelID: "201", GuildID: "10"}}
	later := TeammateDiscussion{ID: "other", InstallationID: "install", Route: Route{ChannelID: "202", GuildID: "10"}}
	if err := bridge.service.Store.teammateBind(teammateBinding{TeammateDiscussion: current, Opened: true}); err != nil {
		t.Fatal(err)
	}
	stage := func(d TeammateDiscussion, id string) string {
		t.Helper()
		m := teammateTestMessage(id, d.Route.ChannelID, "77", "", false)
		m.Attachments = []*discordgo.MessageAttachment{{ID: "300", Filename: "fixture.txt", Size: 1, URL: "https://cdn.discordapp.com/attachments/" + d.Route.ChannelID + "/300/fixture.txt"}}
		route := d.Route
		route.MessageID = m.ID
		a := TeammateAdmission{Discussion: d, Route: route, Author: Actor{ID: "77"}, Revision: teammateRevision(m)}
		if err := bridge.captureAttachments(t.Context(), &a, m); err != nil || !filepath.IsAbs(a.AttachmentManifest) {
			t.Fatalf("stage %q: %v", a.AttachmentManifest, err)
		}
		return a.AttachmentManifest
	}
	original := stage(current, "210")
	now = now.Add(8 * 24 * time.Hour)
	stage(later, "220")
	if _, err := os.Stat(original); err != nil {
		t.Fatal("live discussion lost its original files", err)
	}
	if err := bridge.service.Store.teammatePark(current, 0, nil); err != nil {
		t.Fatal(err)
	}
	stage(later, "221")
	if _, err := os.Stat(original); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("settled discussion's expired files were not reclaimed", err)
	}
}

func TestGuildAttachmentOutageNotifiesItsProvisionalThreadOnce(t *testing.T) {
	var posts, fetches int
	bridge, _ := teammateTestConnector(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /channels/201":
			_, _ = fmt.Fprint(w, teammatePublic)
		case "GET /channels/20":
			_, _ = fmt.Fprint(w, teammateParent)
		case "GET /channels/20/permissions/member/99":
			teammateTestPermissions(w)
		case "POST /channels/201/messages":
			posts++
			var p struct{ Content string }
			if json.NewDecoder(r.Body).Decode(&p) != nil || !strings.Contains(p.Content, "kept your request") {
				t.Error("missing scoped retry notice")
			}
			_ = json.NewEncoder(w).Encode(discordgo.Message{ID: "500", ChannelID: "201", Author: &discordgo.User{ID: "99", Bot: true}, Content: p.Content})
		default:
			t.Error("unexpected REST request", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
	bridge.service.Config.ProjectAccess = ProjectAccessVM
	bridge.client.http.Transport = attachmentTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "cdn.discordapp.com" {
			return http.DefaultTransport.RoundTrip(r)
		}
		fetches++
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	d := TeammateDiscussion{ID: "provisional", InstallationID: "install", Route: Route{ChannelID: "201", GuildID: "10", ParentID: "20"}}
	if err := bridge.service.Store.teammateBind(teammateBinding{TeammateDiscussion: d}); err != nil {
		t.Fatal(err)
	}
	m := teammateTestMessage("210", "201", "77", "<@99> Check this file", true)
	m.Attachments = []*discordgo.MessageAttachment{{ID: "310", Filename: "failed.csv", URL: "https://cdn.discordapp.com/attachments/201/310/failed.csv", Size: 10}}
	a := TeammateAdmission{Discussion: d, Route: Route{ChannelID: "201", GuildID: "10", MessageID: m.ID}, Author: Actor{ID: "77"}, Revision: teammateRevision(m)}
	bridge.client.attachmentMu.Lock()
	err := bridge.captureAttachments(t.Context(), &a, m)
	bridge.client.attachmentMu.Unlock()
	if !errors.Is(err, errAttachmentBusy) || posts != 0 || fetches != 0 {
		t.Fatalf("busy staging made a network request: %v posts=%d downloads=%d", err, posts, fetches)
	}
	for range 2 {
		if err := bridge.captureAttachments(t.Context(), &a, m); !errors.Is(err, errAttachmentRetry) {
			t.Fatal(err)
		}
	}
	unbound, err := bridge.service.Store.teammateOutputs(d.InstallationID, "")
	if err != nil || len(unbound) != 0 || posts != 1 || fetches != 2 {
		t.Fatalf("retry must post once in the exact thread: posts=%d downloads=%d unbound=%d error=%v", posts, fetches, len(unbound), err)
	}
}
