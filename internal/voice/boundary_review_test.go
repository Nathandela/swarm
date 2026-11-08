package voice

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBoundaryReviewConfigAcceptedWriteMustRemainReadable(t *testing.T) {
	dir := privateConfigDir(t)
	c := testConfig()
	if err := SaveConfig(dir, c); err != nil {
		t.Fatal(err)
	}
	c.WorkDir = "/" + strings.Repeat(strings.Repeat("<", 240)+"/", 12) + "project"
	if e := c.Validate(); e != nil {
		t.Fatal(e)
	}
	if e := SaveConfig(dir, c); !errors.Is(e, ErrCapacity) {
		t.Fatalf("escaped configuration was not refused before write: %v", e)
	}
	if got, e := LoadConfig(dir); e != nil || got != testConfig() {
		t.Fatalf("refused configuration changed prior settings: %+v %v", got, e)
	}
}
func TestBoundaryReviewBridgeCancellationAfterHeadersPreservesCause(t *testing.T) {
	headers := make(chan struct{})
	reading := make(chan struct{})
	release := make(chan struct{})
	c := bridgeFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, `{"data":`)
		w.(http.Flusher).Flush()
		close(headers)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	base := c.http.Transport
	c.http.Transport = boundaryReviewRoundTrip(func(r *http.Request) (*http.Response, error) {
		res, e := base.RoundTrip(r)
		if e == nil {
			res.Body = &boundaryReviewReadSignal{ReadCloser: res.Body, started: reading}
		}
		return res, e
	})
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, e := c.Call(ctx, ManagerToolCall{Name: "swarm_list", Arguments: json.RawMessage(`{}`)})
		done <- e
	}()
	select {
	case <-headers:
	case <-time.After(time.Second):
		t.Fatal("no headers")
	}
	select {
	case <-reading:
	case <-time.After(time.Second):
		t.Fatal("body read not started")
	}
	cancel()
	select {
	case e := <-done:
		if !errors.Is(e, context.Canceled) {
			t.Fatalf("lost cancellation cause after headers: %v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not finish")
	}
}
func TestBoundaryReviewBridgeRejectsAmbiguousResponseEnvelope(t *testing.T) {
	for _, raw := range []string{`{"error":"stale","error":"","data":{"accepted":true}}`, `{"data":{"accepted":false},"data":{"accepted":true}}`, `{"Data":{"accepted":true}}`} {
		t.Run(raw, func(t *testing.T) {
			c := bridgeFixture(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, raw) })
			data, e := c.Call(context.Background(), ManagerToolCall{Name: "swarm_status", Arguments: json.RawMessage(`{}`)})
			if e == nil {
				t.Fatalf("ambiguous/noncanonical envelope accepted as success: %s", data)
			}
		})
	}
}

type boundaryReviewRoundTrip func(*http.Request) (*http.Response, error)

func (f boundaryReviewRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type boundaryReviewReadSignal struct {
	io.ReadCloser
	started chan struct{}
	sent    bool
}

func (r *boundaryReviewReadSignal) Read(p []byte) (int, error) {
	if !r.sent {
		close(r.started)
		r.sent = true
	}
	return r.ReadCloser.Read(p)
}
func TestBoundaryReviewBridgeRequiresPrivateSocketCustody(t *testing.T) {
	for _, kind := range []string{"public_socket", "public_parent", "symlink", "regular"} {
		t.Run(kind, func(t *testing.T) {
			authDir := privateConfigDir(t)
			if e := writeVoiceFile(authDir, "auth", []byte(strings.Repeat("a", 64))); e != nil {
				t.Fatal(e)
			}
			dir, e := os.MkdirTemp(os.TempDir(), "bs-")
			if e != nil {
				t.Fatal(e)
			}
			defer func() { _ = os.RemoveAll(dir) }()
			socket := filepath.Join(dir, "b.sock")
			if kind == "regular" {
				if e := os.WriteFile(socket, []byte("not a socket"), 0600); e != nil {
					t.Fatal(e)
				}
			} else {
				l, e := net.Listen("unix", socket)
				if e != nil {
					t.Fatal(e)
				}
				defer func() { _ = l.Close() }()
				if e = os.Chmod(socket, 0600); e != nil {
					t.Fatal(e)
				}
			}
			switch kind {
			case "public_socket":
				if e := os.Chmod(socket, 0777); e != nil {
					t.Fatal(e)
				}
			case "public_parent":
				if e := os.Chmod(dir, 0755); e != nil {
					t.Fatal(e)
				}
			case "symlink":
				link := socket + "-alias"
				if e := os.Symlink(socket, link); e != nil {
					t.Fatal(e)
				}
				socket = link
			}
			c, e := NewManagerBridgeClient(socket, filepath.Join(authDir, "auth"), "conversation", "session")
			if c != nil {
				c.Close()
			}
			if e == nil {
				t.Fatalf("accepted unsafe socket custody: %s", kind)
			}
		})
	}
}
