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
	if got, err := conn.captureAttachments(context.Background(), a, &msg); err != nil || got != a.AttachmentManifest {
		t.Fatal("snapshot lost on replay", got, err)
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
	if _, err := conn.captureAttachments(context.Background(), foreign, &msg); err == nil {
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
	manifest, err := conn.captureAttachments(context.Background(), a, m)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(manifest)
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
	dir, err := os.ReadDir(filepath.Dir(manifest))
	if err != nil || len(dir) != 1 {
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
		if _, err := conn.captureAttachments(t.Context(), a, &m); !errors.Is(err, errAttachmentRetry) {
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
	manifest, err := conn.captureAttachments(t.Context(), a, &m)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(manifest)
	if err != nil || !strings.Contains(string(raw), "Reattach") || attempts != 2 {
		t.Fatalf("outage did not release original request after bounded retries: %s, %d, %v", raw, attempts, err)
	}
}
