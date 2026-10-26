package discord

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func localRig(t *testing.T) (*Service, string, context.CancelFunc, chan error) {
	t.Helper()
	s, _, _ := serviceRig(t)
	dir, e := os.MkdirTemp("", "dl")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ctx, cancel := context.WithCancel(context.Background())
	s.Stop = cancel
	done := make(chan error, 1)
	go func() { done <- s.ServeLocal(ctx, dir) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("local server did not stop")
		}
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		var status LocalStatus
		e = CallLocal(ctx, dir, "status", nil, &status)
		if e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(e)
		}
		time.Sleep(time.Millisecond)
	}
	return s, dir, cancel, done
}
func TestLocalContextExactAndReplyExplicit(t *testing.T) {
	s, dir, _, _ := localRig(t)
	in := incoming()
	in.Text = strings.Repeat("界🙂 quoted '\" $()\n", 600)
	r, e := s.Handle(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	var result LocalContext
	if e = CallLocal(context.Background(), dir, "context", LocalContextRequest{Ref: r.Ref}, &result); e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(result.Request)
	if strings.Contains(string(raw), "quoted") || strings.Contains(string(raw), "native_evidence") {
		t.Fatal("header repeats source/evidence", string(raw))
	}
	if result.Request.Author.ID != in.Actor.ID || result.Page.Ref != r.Ref {
		t.Fatal(result)
	}
	var exact strings.Builder
	for _, message := range result.Page.Messages {
		exact.WriteString(message.Text)
	}
	if exact.String() != in.Text || result.Page.NextOffset != -1 {
		t.Fatal("context changed source text", result.Page.NextOffset)
	}
	var p Publication
	if e = CallLocal(context.Background(), dir, "reply", LocalReply{Ref: r.Ref, Body: "exact answer\n"}, &p); e != nil {
		t.Fatal(e)
	}
	if p.State != StateSent || p.Body != "exact answer\n" {
		t.Fatal(p)
	}
	fi, e := os.Lstat(filepath.Join(dir, "bridge.sock"))
	if e != nil || fi.Mode().Perm() != 0600 {
		t.Fatal(fi, e)
	}
}
func TestLocalExactOptOutAndIncludeRemainDiscoverable(t *testing.T) {
	_, dir, _, _ := localRig(t)
	ctx := context.Background()
	var status LocalStatus
	if e := CallLocal(ctx, dir, "exclude", LocalTarget{TargetID: "local"}, &status); e == nil {
		t.Fatal("prefix target accepted")
	}
	if e := CallLocal(ctx, dir, "exclude", LocalTarget{TargetID: "local/one"}, &status); e != nil {
		t.Fatal(e)
	}
	if e := CallLocal(ctx, dir, "roster", nil, &status); e != nil || len(status.Workers) != 1 || !status.Workers[0].Excluded {
		t.Fatal(status, e)
	}
	if e := CallLocal(ctx, dir, "include", LocalTarget{TargetID: "local/one"}, &status); e != nil {
		t.Fatal(e)
	}
	if e := CallLocal(ctx, dir, "status", nil, &status); e != nil || status.Workers[0].Excluded {
		t.Fatal(status, e)
	}
}
func TestLocalCancellationStaleSocketAndForeignPath(t *testing.T) {
	s, dir, cancel, done := localRig(t)
	cancel()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
		done <- nil
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not stop")
	}
	path := filepath.Join(dir, "bridge.sock")
	listener, e := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if e != nil {
		t.Fatal(e)
	}
	listener.SetUnlinkOnClose(false)
	if e = listener.Close(); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- s.ServeLocal(ctx, dir) }()
	defer cancel()
	for deadline := time.Now().Add(time.Second); ; {
		var status LocalStatus
		e = CallLocal(ctx, dir, "status", nil, &status)
		if e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stale socket not recovered", e)
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if e = <-finished; e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, []byte("foreign"), 0644); e != nil {
		t.Fatal(e)
	}
	if e = s.ServeLocal(context.Background(), dir); e == nil {
		t.Fatal("foreign file replaced")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != "foreign" {
		t.Fatal("foreign path touched")
	}
}
func TestLocalRefusesConcurrentOwnerAndMalformedRequests(t *testing.T) {
	s, dir, _, _ := localRig(t)
	if e := s.ServeLocal(context.Background(), dir); e == nil {
		t.Fatal("second local owner admitted")
	}
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(dir, "bridge.sock"))
	}}}
	for _, body := range []string{`{"ref":"x","body":"x","unknown":true}`, `{"ref":"x","body":"x"} {}`, strings.Repeat("x", MaxPublicationBytes*6+5000)} {
		response, e := client.Post("http://local/reply", "application/json", strings.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		_ = response.Body.Close()
		if response.StatusCode == 200 {
			t.Fatal("malformed request admitted")
		}
	}
	if e := CallLocal(context.Background(), dir, "../reply", nil, nil); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
}

func TestLocalShutdownAcknowledgesBeforeConnectorStops(t *testing.T) {
	_, dir, _, done := localRig(t)
	if e := CallLocal(context.Background(), dir, "shutdown", struct{}{}, nil); e != nil {
		t.Fatal("shutdown receipt lost", e)
	}
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
		done <- nil
	case <-time.After(3 * time.Second):
		t.Fatal("connector did not stop")
	}
	if _, e := os.Lstat(filepath.Join(dir, "bridge.sock")); !os.IsNotExist(e) {
		t.Fatal("shutdown left listening socket", e)
	}
}

func TestLocalRejectsLinkedSocketWithoutTouchingSource(t *testing.T) {
	s, _, _ := serviceRig(t)
	dir, e := os.MkdirTemp("", "dll")
	if e != nil {
		t.Fatal(e)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	state := filepath.Join(dir, "state")
	if e = os.Mkdir(state, 0700); e != nil {
		t.Fatal(e)
	}
	original := filepath.Join(dir, "original.sock")
	listener, e := net.ListenUnix("unix", &net.UnixAddr{Name: original, Net: "unix"})
	if e != nil {
		t.Fatal(e)
	}
	listener.SetUnlinkOnClose(false)
	if e = listener.Close(); e != nil {
		t.Fatal(e)
	}
	linked := filepath.Join(state, "bridge.sock")
	if e = os.Link(original, linked); e != nil {
		t.Fatal(e)
	}
	if e = s.ServeLocal(context.Background(), state); e == nil {
		t.Fatal("linked socket accepted")
	}
	if _, e = os.Lstat(original); e != nil {
		t.Fatal("source socket touched", e)
	}
	if e = os.Remove(linked); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(original, linked); e != nil {
		t.Fatal(e)
	}
	if e = s.ServeLocal(context.Background(), state); e == nil {
		t.Fatal("symlinked socket accepted")
	}
	if _, e = os.Lstat(original); e != nil {
		t.Fatal("symlink source touched", e)
	}
}
