package main

// FAILING-FIRST (TDD RED, GG-5) for ADR-010 §6's carriage, layer 2: the `swarm hook` role keeps
// the CLI's own event body -- under the existing 1 MiB hookStdinLimit -- for events whose
// adapter descriptor declares capture=raw, and for no others.
//
// WHY THE HOOK PROCESS NEEDS TELLING. `swarm hook <event>` runs as a CHILD OF THE CLI: it knows
// its event name and nothing about which adapter launched it, so it cannot decide on its own
// which rows declare capture. The daemon derives that list from the session adapter's
// SignalSources and injects it at spawn (hookclient.EnvCapture), exactly as it already injects
// the session id, the token, the socket and the sequence file. That is the one design question
// docs/verification/a1b-claude-producer.md §10 recorded as needing a slice of its own.
//
// The posts here travel the PRODUCTION transport (hookclient.Post over a unix socket) and are
// read back with the daemon's own decoder, so what is asserted is the callback a real daemon
// would see.

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/hookclient"
)

// preToolUseBody is the shape spike S-B recorded off the live CLI: the fields the transcript
// needs -- `tool_input` above all -- are NESTED OBJECTS, which is precisely what the top-level
// string flattener drops on the floor.
const preToolUseBody = `{"session_id":"e8a4368a","hook_event_name":"PreToolUse",` +
	`"tool_name":"Read","tool_input":{"file_path":"/tmp/edit-target3.txt"}}`

// claudeCaptureRows are Claude Code's five capture=raw rows (ADR-010 §5 plus its 2026-08-07
// Stop amendment), spelled as the daemon would inject them.
var claudeCaptureRows = []string{"UserPromptSubmit", "PreToolUse", "PostToolUse", "PermissionRequest", "Stop"}

// hookSink stands up a unix socket that accepts one `swarm hook` post and decodes it with the
// daemon's own decoder. The returned func blocks until a callback arrives.
func hookSink(t *testing.T) (sock string, next func() engine.Callback) {
	t.Helper()
	// /tmp keeps the socket path under the 104-byte sun_path limit (t.TempDir does not).
	dir, err := os.MkdirTemp("/tmp", "swhook")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock = filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen %s: %v", sock, err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	got := make(chan engine.Callback, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			cb, derr := hookclient.Decode(conn)
			_ = conn.Close()
			if derr == nil {
				got <- cb
			}
		}
	}()
	return sock, func() engine.Callback {
		t.Helper()
		select {
		case cb := <-got:
			return cb
		case <-time.After(10 * time.Second):
			t.Fatal("no hook callback reached the socket")
			return engine.Callback{}
		case <-t.Context().Done():
			// The test this sink belongs to has already completed -- e.g. a caller
			// (assertNothingArrivesAt, r6_hookwiring_test.go) that gave up waiting on
			// this func in a detached goroutine before the socket was ever expected to
			// receive anything. Touching t past this point (even t.Fatal) panics the
			// whole binary ("Fail in goroutine after Test has completed"); returning
			// silently is the only safe outcome for an abandoned caller.
			return engine.Callback{}
		}
	}
}

// hookEnv installs the per-session environment the daemon injects at spawn, including the
// capture rows.
func hookEnv(t *testing.T, sock string, capture []string) {
	t.Helper()
	t.Setenv(hookclient.EnvSessionID, "sid-1")
	t.Setenv(hookclient.EnvToken, "tok-abc")
	t.Setenv(hookclient.EnvSocket, sock)
	t.Setenv(hookclient.EnvSequenceFile, filepath.Join(t.TempDir(), "hook.seq"))
	t.Setenv(hookclient.EnvCapture, hookclient.CaptureEnv(capture))
}

// TestRunHook_KeepsTheCLIsOwnBodyOnACaptureRow is the carriage itself: a PreToolUse post
// carries the recorded body BYTE FOR BYTE, so `tool_input` -- an object the flattener cannot
// represent at all -- reaches the daemon intact.
func TestRunHook_KeepsTheCLIsOwnBodyOnACaptureRow(t *testing.T) {
	sock, next := hookSink(t)
	hookEnv(t, sock, claudeCaptureRows)

	if code := runHook([]string{"PreToolUse"}, strings.NewReader(preToolUseBody), io.Discard); code != 0 {
		t.Fatalf("runHook exit code %d; want 0", code)
	}
	cb := next()
	if string(cb.Raw) != preToolUseBody {
		t.Errorf("the callback carried raw body %s;\nwant the CLI's own body verbatim %s", cb.Raw, preToolUseBody)
	}

	// THE STATUS PATH IS UNTOUCHED: the flattened payload is exactly what the flattener alone
	// produces, so a capture row's status derivation is byte-identical to before the carriage.
	if want := parseHookStdin(strings.NewReader(preToolUseBody)); !reflect.DeepEqual(cb.Payload, want) {
		t.Errorf("the callback's flattened payload = %v; want %v -- ADR-010 §6 leaves the string-flattening "+
			"loop and the turn/interaction injection guard untouched", cb.Payload, want)
	}
}

// TestRunHook_KeepsNoBodyOnANonCaptureRow. Notification is a real Claude Code hook row that
// declares NO capture, and every event of every adapter that shapes nothing is in the same
// position: the body must not ride along. Carrying it anyway would put unbounded, unshaped tool
// output on the daemon socket for a shaper that will never look at it.
func TestRunHook_KeepsNoBodyOnANonCaptureRow(t *testing.T) {
	sock, next := hookSink(t)
	hookEnv(t, sock, claudeCaptureRows)

	const body = `{"hook_event_name":"Notification","notification_type":"idle","message":"waiting"}`
	if code := runHook([]string{"Notification"}, strings.NewReader(body), io.Discard); code != 0 {
		t.Fatalf("runHook exit code %d; want 0", code)
	}
	cb := next()
	if len(cb.Raw) != 0 {
		t.Errorf("a Notification post carried a raw body %s; Notification declares no capture=raw row, "+
			"so ADR-010 §6 keeps nothing", cb.Raw)
	}
	if want := parseHookStdin(strings.NewReader(body)); !reflect.DeepEqual(cb.Payload, want) {
		t.Errorf("the callback's flattened payload = %v; want %v", cb.Payload, want)
	}
}

// TestRunHook_KeepsNoBodyWhenTheDaemonDeclaredNoCaptureRows is the default posture: a session
// whose adapter implements no capture extension (every shipped adapter but claude) gets an
// EMPTY capture list, and an absent variable -- an older daemon, or a hook invoked outside a
// supervised session -- must read the same way. Nothing captures unless something declared it.
func TestRunHook_KeepsNoBodyWhenTheDaemonDeclaredNoCaptureRows(t *testing.T) {
	for _, tc := range []struct {
		name   string
		absent bool
	}{
		{name: "an adapter with no capture extension declares an empty list"},
		{name: "the variable absent entirely (an older daemon, or an unsupervised hook)", absent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sock, next := hookSink(t)
			hookEnv(t, sock, nil)
			if tc.absent {
				// t.Setenv already recorded the original value, so this is restored on cleanup.
				_ = os.Unsetenv(hookclient.EnvCapture)
			}
			if code := runHook([]string{"PreToolUse"}, strings.NewReader(preToolUseBody), io.Discard); code != 0 {
				t.Fatalf("runHook exit code %d; want 0", code)
			}
			if cb := next(); len(cb.Raw) != 0 {
				t.Errorf("a PreToolUse post carried %s with no declared capture row; the descriptor is the "+
					"ONLY thing that turns capture on", cb.Raw)
			}
		})
	}
}

// TestRunHook_CapsTheBodyAndStillPostsTheStatus is the cap, and the reason it is a DROP rather
// than a clip. §6 keeps "the whole body, under the existing 1 MiB hookStdinLimit"; a body over
// that limit is not the whole body, and a clipped fragment is invalid JSON -- which would make
// the whole callback unmarshalable and take the SESSION'S STATUS down with it. Untrusted tool
// output must never be able to do that (§6: raw bodies never influence status).
func TestRunHook_CapsTheBodyAndStillPostsTheStatus(t *testing.T) {
	sock, next := hookSink(t)
	hookEnv(t, sock, claudeCaptureRows)

	oversized := `{"hook_event_name":"Stop","last_assistant_message":"` +
		strings.Repeat("x", hookStdinLimit) + `"}`
	if code := runHook([]string{"Stop"}, strings.NewReader(oversized), io.Discard); code != 0 {
		t.Fatalf("runHook exit code %d on an oversized body; want 0 -- the status post must survive "+
			"a body the cap refuses", code)
	}
	cb := next()
	if len(cb.Raw) > hookStdinLimit {
		t.Errorf("the callback carried %d raw bytes; the ingest cap is %d", len(cb.Raw), hookStdinLimit)
	}
	if len(cb.Raw) != 0 && !json.Valid(cb.Raw) {
		t.Errorf("the callback carried %d bytes of INVALID JSON as its raw body; a truncated body is a "+
			"partial item (IS-ENV-3) and cannot even be re-encoded onto the wire", len(cb.Raw))
	}
	if cb.Event != "Stop" {
		t.Errorf("the callback's event = %q; want Stop -- the status post itself must still arrive", cb.Event)
	}
}
