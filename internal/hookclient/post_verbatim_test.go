package hookclient

import (
	"encoding/json"
	"net"
	"path/filepath"
	"testing"

	"github.com/Nathandela/swarm/internal/engine"
)

// TestPost_RawBytesCrossTheWireVerbatim fences the carriage fix for the review
// finding on the i1 slice: json.Marshal HTML-escapes <, > and & inside a
// json.RawMessage (6 wire bytes per input byte), so an untrusted body near the
// ingest cap could expand past a transport limit and take the session's status
// post down with it - the inversion ADR-010 section 6 forbids. Post now encodes
// with SetEscapeHTML(false); this test drives the REAL Post over a real socket
// and asserts the three rewritable bytes cross byte-exact. Before the fix this
// test fails with &/</> in out.Raw (measured in the i1 review).
func TestPost_RawBytesCrossTheWireVerbatim(t *testing.T) {
	raw := json.RawMessage(`{"tool_name":"Bash","tool_input":{"command":"grep -c a b && echo <done>"}}`)

	sock := filepath.Join(t.TempDir(), "hook.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	decoded := make(chan engine.Callback, 1)
	errc := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			errc <- err
			return
		}
		defer conn.Close()
		cb, err := Decode(conn)
		if err != nil {
			errc <- err
			return
		}
		decoded <- cb
	}()

	if err := Post(sock, engine.Callback{SessionID: "s1", Token: "tok1", Sequence: 1, Event: "PreToolUse", Raw: raw}); err != nil {
		t.Fatalf("Post: %v", err)
	}

	select {
	case err := <-errc:
		t.Fatalf("server side: %v", err)
	case out := <-decoded:
		if string(out.Raw) != string(raw) {
			t.Errorf("Raw crossed the wire as %s; want the CLI's own bytes %s - Post must not rewrite a captured body", out.Raw, raw)
		}
	}
}
