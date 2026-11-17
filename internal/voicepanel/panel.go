// Package voicepanel serves the local desktop manager view.
package voicepanel

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/jsontext"
	"github.com/Nathandela/swarm/internal/voice"
)

//go:embed web/*
var assets embed.FS

type manager interface {
	Status(context.Context) (voice.ManagerStatus, error)
	SubmitText(context.Context, string, string) (voice.Admission, error)
	BeginPTT(context.Context, string) (voice.PTTCapture, error)
	FinalizePTT(context.Context, string, []byte) (voice.Admission, error)
	Receipt(context.Context, string) (voice.Admission, error)
	Publications(context.Context, string) ([]voice.Publication, error)
	PreviewPublication(context.Context, string, string) ([]byte, error)
}

// Serve opens a same-origin browser panel. The launch URL grants authority for
// this process lifetime and should only be shared with the local browser.
func Serve(ctx context.Context, dir string, ready func(string) error) error {
	client := voice.NewManagerClient(dir)
	defer client.Close()
	return serve(ctx, client, false, ready)
}

func serve(ctx context.Context, client manager, previewUnavailable bool, ready func(string) error) error {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer func() { _ = l.Close() }()
	var token [32]byte
	if _, err = rand.Read(token[:]); err != nil {
		return err
	}
	secret := base64.RawURLEncoding.EncodeToString(token[:])
	host := l.Addr().String()
	s := &http.Server{
		Handler:           panel{host: host, secret: secret, manager: client, previewUnavailable: previewUnavailable},
		ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 45 * time.Second,
		IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	defer func() { _ = s.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = s.Close() })
	defer stop()
	served := make(chan error, 1)
	go func() { served <- s.Serve(l) }()
	if err = ready("http://" + host + "/#" + secret); err != nil {
		return err
	}
	if err = <-served; errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

type panel struct {
	host, secret       string
	manager            manager
	previewUnavailable bool
}

func (p panel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; worker-src 'self'; frame-ancestors 'none'; form-action 'none'; base-uri 'none'")
	w.Header().Set("Permissions-Policy", "microphone=(self), camera=(), geolocation=()")
	origin := r.Header.Get("Origin")
	site := r.Header.Get("Sec-Fetch-Site")
	if r.Host != p.host || r.URL.RawQuery != "" || (origin != "" && origin != "http://"+p.host) || (site != "" && site != "none" && site != "same-origin") {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+p.secret)) != 1 || (r.Method != http.MethodGet && origin != "http://"+p.host) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		p.api(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	switch r.URL.Path {
	case "/":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case "/panel.js", "/capture.js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	case "/panel.css", "/slate.css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	default:
		http.NotFound(w, r)
		return
	}
	name := r.URL.Path
	if name == "/" {
		name = "/index.html"
	}
	body, err := assets.ReadFile("web" + name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write(body)
}

type input struct {
	ID            string `json:"id"`
	PublicationID string `json:"publication_id,omitempty"`
	Text          string `json:"text,omitempty"`
}

func (p panel) api(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	var value any
	var err error
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/status":
		var status voice.ManagerStatus
		status, err = p.manager.Status(ctx)
		value = struct {
			voice.ManagerStatus
			PreviewUnavailable bool `json:"preview_unavailable,omitempty"`
		}{status, p.previewUnavailable}
	case r.Method == http.MethodPost:
		value, err = p.submit(w, r.WithContext(ctx))
	default:
		err = voice.ErrInvalid
	}
	if pcm, ok := value.([]byte); ok && err == nil {
		if len(pcm) > 0 && len(pcm) <= 24_000*2*6 && len(pcm)%2 == 0 {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(pcm)
			return
		}
		err = voice.ErrPreviewUnavailable
	}
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		status, label := http.StatusServiceUnavailable, "unavailable"
		switch {
		case errors.Is(err, voice.ErrInvalid):
			status, label = 400, "invalid"
		case errors.Is(err, voice.ErrNotFound):
			status, label = 404, "not_found"
		case errors.Is(err, voice.ErrConflict):
			status, label = 409, "conflict"
		}
		w.WriteHeader(status)
		value = map[string]string{"error": label}
	}
	_ = json.NewEncoder(w).Encode(value)
}

func (p panel) submit(w http.ResponseWriter, r *http.Request) (any, error) {
	if r.URL.Path == "/api/ptt/finalize" {
		id := r.Header.Get("Swarm-Utterance-ID")
		if !validID(id) || r.Header.Get("Content-Type") != "application/octet-stream" {
			return nil, voice.ErrInvalid
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, voice.MaxPTTBytes))
		if err != nil || len(raw) == 0 || len(raw)%2 != 0 {
			return nil, voice.ErrInvalid
		}
		return p.manager.FinalizePTT(r.Context(), id, raw)
	}
	if r.Header.Get("Content-Type") != "application/json" {
		return nil, voice.ErrInvalid
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 6*voice.MaxUtteranceBytes+256))
	var req input
	if err != nil || jsontext.ValidateUnicode(raw) != nil || json.Unmarshal(raw, &req) != nil || !validID(req.ID) || len(req.Text) > voice.MaxUtteranceBytes {
		return nil, voice.ErrInvalid
	}
	switch r.URL.Path {
	case "/api/text":
		return p.manager.SubmitText(r.Context(), req.ID, req.Text)
	case "/api/ptt/begin":
		return p.manager.BeginPTT(r.Context(), req.ID)
	case "/api/receipt":
		return p.manager.Receipt(r.Context(), req.ID)
	case "/api/read-aloud":
		if req.PublicationID == "" || len(req.PublicationID) > 128 {
			return nil, voice.ErrInvalid
		}
		return p.manager.PreviewPublication(r.Context(), req.ID, req.PublicationID)
	case "/api/publications":
		return p.manager.Publications(r.Context(), req.ID)
	default:
		return nil, voice.ErrInvalid
	}
}

func validID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range []byte(id) {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return false
			}
		}
	}
	return true
}
