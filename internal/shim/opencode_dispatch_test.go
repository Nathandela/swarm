package shim

import (
	"encoding/json"
	"github.com/Nathandela/swarm/internal/opencodebridge"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenCodeDispatchHoldsExistingPTYInputFence(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ocd-")
	if err != nil {
		t.Fatal(err)
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	b := opencodebridge.Binding{Version: 1, WorkerID: "worker", SessionInstance: strings.Repeat("a", 32), RootID: "ses_" + strings.Repeat("b", 32), Directory: dir, BackendGeneration: strings.Repeat("a", 32), SocketPath: filepath.Join(dir, "oc.sock"), Token: strings.Repeat("c", 64), Mode: "create"}
	raw, _ := json.Marshal(b)
	path := filepath.Join(dir, OpenCodeBindingFile)
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	entered, finish := make(chan struct{}, 1), make(chan struct{})
	listener, err := net.Listen("unix", b.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+b.Token {
			w.WriteHeader(401)
			return
		}
		entered <- struct{}{}
		<-finish
		_ = json.NewEncoder(w).Encode(opencodebridge.Receipt{Status: "admitted"})
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	f, err := os.Create(filepath.Join(dir, "input"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	p := &ptyWriter{f: f}
	cfg := &OpenCodeConfig{RootID: b.RootID, Generation: b.BackendGeneration, BindingFile: path}
	req := opencodebridge.DispatchRequest{Version: 1, SessionInstance: b.SessionInstance, BackendGeneration: b.BackendGeneration, RootID: b.RootID, Kind: "prompt", Text: "genuine"}
	done := make(chan error, 1)
	go func() { _, err := p.dispatchOpenCode(cfg, req); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("bridge was not invoked")
	}
	typed := make(chan struct{})
	go func() { _, _ = p.WriteInput([]byte("owner")); close(typed) }()
	select {
	case <-typed:
		t.Fatal("owner input crossed native write")
	case <-time.After(40 * time.Millisecond):
	}
	close(finish)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case <-typed:
	case <-time.After(time.Second):
		t.Fatal("input fence did not release")
	}
	if _, err := p.dispatchOpenCode(cfg, req); err == nil {
		t.Fatal("dirty owner draft admitted")
	}
	p.inputLine.reset()
	req.RootID = "ses_" + strings.Repeat("d", 32)
	if _, err := p.dispatchOpenCode(cfg, req); err == nil {
		t.Fatal("foreign native binding admitted")
	}
	p.embargo = &inputEmbargo{refusal: errOpenCodeStarting}
	p.embargo.held.Store(true)
	req.RootID = b.RootID
	if _, err := p.dispatchOpenCode(cfg, req); err == nil {
		t.Fatal("embargo bypassed")
	}
}
