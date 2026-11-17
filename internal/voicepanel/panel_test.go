package voicepanel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/voice"
)

const fixtureID = "00000000-1234-4567-89ab-000000000001"

type fixtureManager struct {
	ids     []string
	audio   []byte
	preview [2]string
}

func (*fixtureManager) Status(context.Context) (voice.ManagerStatus, error) {
	return voice.ManagerStatus{Ready: true}, nil
}
func (m *fixtureManager) SubmitText(_ context.Context, id, text string) (voice.Admission, error) {
	m.ids = append(m.ids, id)
	return voice.Admission{Sequence: 1, InputText: text}, nil
}
func (*fixtureManager) BeginPTT(_ context.Context, id string) (voice.PTTCapture, error) {
	return voice.PTTCapture{ID: id, Phase: "capturing"}, nil
}
func (m *fixtureManager) FinalizePTT(_ context.Context, id string, pcm []byte) (voice.Admission, error) {
	m.audio = pcm
	return voice.Admission{Sequence: 1}, nil
}
func (*fixtureManager) Receipt(context.Context, string) (voice.Admission, error) {
	return voice.Admission{Sequence: 1}, nil
}
func (*fixtureManager) Publications(context.Context, string) ([]voice.Publication, error) {
	return []voice.Publication{{Text: "Manager result"}}, nil
}

func (m *fixtureManager) PreviewPublication(_ context.Context, id, publication string) ([]byte, error) {
	m.preview = [2]string{id, publication}
	return []byte{0, 1}, nil
}

func TestPanelAuthorizationAndOrigin(t *testing.T) {
	p := panel{host: "127.0.0.1:12345", secret: "test-token", manager: &fixtureManager{}}
	for _, tc := range []struct {
		name, host, path, auth, origin, site string
		status                               int
	}{
		{"page", p.host, "/", "", "", "none", 200},
		{"missing-key", p.host, "/api/status", "", "", "same-origin", 403},
		{"correct", p.host, "/api/status", "test-token", "", "same-origin", 200},
		{"foreign-host", "attacker.invalid", "/api/status", "test-token", "", "same-origin", 403},
		{"foreign-origin", p.host, "/api/status", "test-token", "http://attacker.invalid", "", 403},
		{"cross-site", p.host, "/panel.js", "", "", "cross-site", 403},
		{"query-key", p.host, "/api/status?token=test-token", "test-token", "", "same-origin", 403},
		{"directory", p.host, "/web/", "", "", "same-origin", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://"+tc.host+tc.path, nil)
			r.Header.Set("Authorization", "Bearer "+tc.auth)
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Sec-Fetch-Site", tc.site)
			w := httptest.NewRecorder()
			p.ServeHTTP(w, r)
			if w.Code != tc.status || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("status %d headers %v", w.Code, w.Header())
			}
		})
	}
}

func TestPanelRejectsResourceAbuseAndKeepsExactID(t *testing.T) {
	m := &fixtureManager{}
	p := panel{host: "127.0.0.1:12345", secret: "test-token", manager: m}
	post := func(path, content, body, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "http://"+p.host+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer test-token")
		r.Header.Set("Content-Type", content)
		r.Header.Set("Swarm-Utterance-ID", fixtureID)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		return w
	}
	origin := "http://" + p.host
	text := `{"id":"` + fixtureID + `","text":"question"}`
	if w := post("/api/text", "application/json", text, ""); w.Code != 403 {
		t.Fatalf("no origin: %d", w.Code)
	}
	for range 2 {
		if w := post("/api/text", "application/json", text, origin); w.Code != 200 {
			t.Fatalf("text: %d", w.Code)
		}
	}
	if len(m.ids) != 2 || m.ids[0] != fixtureID || m.ids[1] != fixtureID {
		t.Fatalf("action IDs: %v", m.ids)
	}
	if w := post("/api/ptt/begin", "application/json", `{"id":"`+fixtureID+`"}`, origin); w.Code != 200 {
		t.Fatalf("begin: %d", w.Code)
	}
	for _, body := range []string{"", "a", strings.Repeat("x", voice.MaxPTTBytes+2)} {
		if w := post("/api/ptt/finalize", "application/octet-stream", body, origin); w.Code != 400 {
			t.Fatalf("invalid PCM length %d: %d", len(body), w.Code)
		}
	}
	if w := post("/api/text", "application/json", strings.Repeat("x", 6*voice.MaxUtteranceBytes+257), origin); w.Code != 400 {
		t.Fatalf("oversize control: %d", w.Code)
	}
	if w := post("/api/ptt/finalize", "application/octet-stream", "\x00\x80", origin); w.Code != 200 || string(m.audio) != "\x00\x80" {
		t.Fatalf("PCM finalization: %d %v", w.Code, m.audio)
	}
	if w := post("/api/read-aloud", "application/json", `{"id":"`+fixtureID+`","publication_id":"pub-exact"}`, origin); w.Code != 200 || w.Header().Get("Content-Type") != "application/octet-stream" || w.Body.String() != "\x00\x01" || m.preview != [2]string{fixtureID, "pub-exact"} {
		t.Fatalf("exact audio preview: %d %v", w.Code, m.preview)
	}
	if w := post("/api/read-aloud", "application/json", `{"id":"`+fixtureID+`"}`, origin); w.Code != 400 {
		t.Fatalf("empty publication: %d", w.Code)
	}
	if w := post("/api/publications", "application/json", `{"id":"`+fixtureID+`"}`, origin); w.Code != 200 || !strings.Contains(w.Body.String(), "Manager result") {
		t.Fatalf("publications: %d %s", w.Code, w.Body.String())
	}
}

func TestSlateTokensMatchSource(t *testing.T) {
	raw, err := os.ReadFile("../design/tokens.json")
	if err != nil {
		t.Fatal(err)
	}
	var tokens struct{ Tokens map[string]string }
	if err = json.Unmarshal(raw, &tokens); err != nil {
		t.Fatal(err)
	}
	css, err := assets.ReadFile("web/slate.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(css), "\n") {
		name, value, ok := strings.Cut(strings.TrimSpace(line), ": ")
		if ok && tokens.Tokens[name] != strings.TrimSuffix(value, ";") {
			t.Fatalf("Slate token drift: %s", name)
		}
	}
}

func TestBrowserFixtures(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for browser API and AudioWorklet fixtures")
	}
	if output, err := exec.CommandContext(t.Context(), node, "browser_test.cjs").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
}

func TestPanelServesEphemeralLoopbackAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reported := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, "/no-manager-in-this-fixture", func(url string) error {
			reported <- url
			return nil
		})
	}()
	var launchURL string
	select {
	case launchURL = <-reported:
	case err := <-done:
		t.Fatalf("server stopped before reporting URL: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("no launch URL")
	}
	parsed, err := url.Parse(launchURL)
	if err != nil || parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" || len(parsed.Fragment) != 43 {
		t.Fatalf("unexpected launch URL: %v", err)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(launchURL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("panel returned %d", resp.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("panel did not stop")
	}
}
