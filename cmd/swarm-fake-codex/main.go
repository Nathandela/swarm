// Command swarm-fake-codex is a stand-in for the real `codex` binary IN ITS TWO MODES, so a
// test can drive the whole Wave R7 topology -- daemon -> shim -> app-server -> agent -- without
// a real ChatGPT account and without spending the owner's money (hard rule 10). It is a
// dev/test binary only, never shipped, exactly as swarm-fake-agent is.
//
// THE TWO MODES ARE THE TWO THE R1 GATE RECORDED (r1-codex-gate.md:53 and :60):
//
//	swarm-fake-codex app-server --listen unix://PATH   # the backend the shim owns
//	swarm-fake-codex agent ... --remote unix://PATH    # the agent, as a CLIENT of that backend
//
// WHY THE AGENT HALF DIALS. The point of the go-ahead handshake is that the daemon appends
// `--remote unix://SOCK` to the AGENT's argv, which is the only thing that makes the terminal
// and the mirror one conversation. A fake agent that merely printed its argv would let a test
// assert the string and nothing else; this one ATTACHES, and the fake app-server announces
// `thread/started` only when it does -- so "the daemon adopted a thread" is downstream of the
// agent having really been pointed at the socket.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// fakeThreadID is the thread the fake app-server announces when the agent attaches. It is a
// UUIDv7-shaped literal because that is what the real server emits (frame-samples.json), and a
// test asserts the daemon adopted THIS id rather than one of its own.
const fakeThreadID = "01997f00-face-7000-8000-00000000cde0"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: swarm-fake-codex app-server --listen unix://PATH | agent [args...]")
		os.Exit(2)
	}
	if os.Args[1] == "app-server" {
		os.Exit(runAppServer(os.Args[2:]))
	}
	os.Exit(runAgent(os.Args[1:]))
}

// endpointFor extracts the UDS path from the recorded `--flag unix://PATH` argv shape.
func endpointFor(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return strings.TrimPrefix(args[i+1], "unix://")
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// app-server mode
// ---------------------------------------------------------------------------

type fakeAppServer struct {
	mu    sync.Mutex
	conns map[*websocket.Conn]*sync.Mutex // one write lock per connection
	// rollout models the ONE recorded integration constraint (r1-codex-gate.md:112-115):
	// `thread/resume` fails with `no rollout found for thread id <uuid>` for a thread that
	// exists but has not yet run its first turn, because the rollout file is created when that
	// turn starts. A stand-in that answered every resume immediately would let an assembled
	// test pass on a topology the real server refuses, and would make a FRESH launch look like
	// a thread with prior history.
	rollout bool
	// turnStarts counts `turn/start` requests, which is how a test observes that the phone's
	// words really crossed the socket as an RPC.
	turnStarts int
}

func runAppServer(args []string) int {
	path := endpointFor(args, "--listen")
	if path == "" {
		fmt.Fprintln(os.Stderr, "swarm-fake-codex app-server: --listen unix://PATH is required")
		return 2
	}
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "swarm-fake-codex app-server: listen:", err)
		return 1
	}
	s := &fakeAppServer{conns: map[*websocket.Conn]*sync.Mutex{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc", s.serveRPC)
	mux.HandleFunc("/probe", s.probe)
	mux.HandleFunc("/agent-attach", func(w http.ResponseWriter, _ *http.Request) {
		s.broadcastThreadStarted()
		w.WriteHeader(http.StatusOK)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	// Serve until killed: the shim owns this process's lifetime and reaps it with the
	// session, which is the containment property the topology test also exercises.
	if err := srv.Serve(ln); err != nil {
		return 1
	}
	return 0
}

// serveRPC is the WebSocket JSON-RPC endpoint. The upgrade is the R1 gate's single most
// expensive finding: `--listen unix://PATH` is a WEBSOCKET endpoint at GET /rpc, and bare
// newline-delimited JSON produces total silence.
func (s *fakeAppServer) serveRPC(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	writeMu := &sync.Mutex{}
	s.mu.Lock()
	s.conns[c] = writeMu
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		_ = c.CloseNow()
	}()

	ctx := context.Background()
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		var fr struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(data, &fr) != nil {
			continue
		}
		if len(fr.ID) == 0 {
			continue // a notification (`initialized`) is answered with nothing
		}
		reply, err := json.Marshal(s.answer(fr.ID, fr.Method))
		if err != nil {
			continue
		}
		writeMu.Lock()
		err = c.Write(ctx, websocket.MessageText, reply)
		writeMu.Unlock()
		if err != nil {
			return
		}
	}
}

// answer builds the JSON-RPC reply for one request, applying the recorded rollout constraint.
//
//	thread/resume   -> the RECORDED -32600 `no rollout found for thread id <uuid>` until a turn
//	                   has started; the thread object afterwards
//	turn/start      -> creates the rollout, exactly as the real server does, and is counted
//	everything else -> an empty result, as before
func (s *fakeAppServer) answer(id json.RawMessage, method string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]any{"jsonrpc": "2.0", "id": id}
	switch method {
	case "thread/resume":
		if !s.rollout {
			out["error"] = map[string]any{
				"code":    -32600,
				"message": "no rollout found for thread id " + fakeThreadID,
			}
			return out
		}
		out["result"] = map[string]any{"thread": map[string]any{"id": fakeThreadID}}
	case "turn/start":
		s.rollout = true
		s.turnStarts++
		out["result"] = map[string]any{"turn": map[string]any{"id": "01a0033b-d0be-77e1-88e7-584ddeea562d", "status": "inProgress"}}
	default:
		out["result"] = map[string]any{}
	}
	return out
}

// probe reports what this server has been asked, so an assembled test can observe the RPCs from
// outside the process that received them.
func (s *fakeAppServer) probe(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	body := map[string]any{"turnStarts": s.turnStarts, "rollout": s.rollout}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// broadcastThreadStarted fires the recorded `thread/started` notification to every attached
// client, which is what the real server does when the AGENT creates the session's thread.
func (s *fakeAppServer) broadcastThreadStarted() {
	frame, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  "thread/started",
		"params":  map[string]any{"thread": map[string]any{"id": fakeThreadID}},
	})
	if err != nil {
		return
	}
	s.mu.Lock()
	targets := make(map[*websocket.Conn]*sync.Mutex, len(s.conns))
	for c, mu := range s.conns {
		targets[c] = mu
	}
	s.mu.Unlock()
	ctx := context.Background()
	for c, mu := range targets {
		mu.Lock()
		_ = c.Write(ctx, websocket.MessageText, frame)
		mu.Unlock()
	}
}

// ---------------------------------------------------------------------------
// agent mode
// ---------------------------------------------------------------------------

// runAgent prints its own argv on the PTY -- which is what puts it in the session transcript,
// where a test can read exactly what the shim appended -- attaches to the backend if it was
// pointed at one, and then idles until its stdin closes.
func runAgent(args []string) int {
	fmt.Printf("ARGV %s\n", strings.Join(args, " "))
	if path := endpointFor(args, "--remote"); path != "" {
		if err := attach(path); err != nil {
			fmt.Printf("ATTACH-FAILED %v\n", err)
		} else {
			fmt.Printf("ATTACHED %s\n", path)
		}
	} else {
		fmt.Println("NO-REMOTE")
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	return 0
}

// attach is the agent's whole client role in this fake: one plain HTTP GET over the UDS, which
// makes the fake server announce the thread. The real agent speaks the same JSON-RPC the
// daemon does; nothing in this test needs that, and inventing it would be inventing protocol.
func attach(path string) error {
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", path)
			},
		},
	}
	resp, err := client.Get("http://backend/agent-attach")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agent-attach: %s", resp.Status)
	}
	return nil
}
