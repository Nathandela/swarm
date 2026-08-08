// Package hookclient is the Epic 10 hook transport (E10.1, G4): the thin poster
// that `swarm hook <event>` uses to send an authenticated status callback to the
// daemon socket, plus the daemon-side Decode that reverses it.
//
// A hook invocation reads its per-session authentication from the environment
// injected at spawn — session id, live token, daemon socket path, and a monotonic
// sequence — and composes an engine.Callback (FromEnv). Post writes it to the
// daemon socket; the daemon Decodes it and feeds engine.HandleCallback, which
// authenticates it (S6/G5) and applies the status. Post/Decode are an inverse
// pair; the wire encoding is theirs to choose (JSON here). The token is installed
// per invocation and never persisted, so a hook cannot be spoofed by another
// local process (ADR-004).
package hookclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/Nathandela/swarm/internal/engine"
)

// Environment keys the daemon injects into a session's hook environment at spawn.
const (
	EnvSessionID = "SWARM_SESSION_ID"
	EnvToken     = "SWARM_HOOK_TOKEN"
	// EnvSocket intentionally reuses the daemon's socket variable
	// (daemon.EnvSocket == "SWARM_DAEMON_SOCK") so the hook dials the same socket
	// the daemon serves.
	EnvSocket = "SWARM_DAEMON_SOCK"
	// EnvSequenceFile names the per-session monotonic counter FILE the daemon
	// injects at spawn (G5). Each `swarm hook` invocation atomically increments it
	// to obtain a strictly increasing sequence, so per-event callbacks are never
	// rejected as replays. This is the sole sequence source.
	EnvSequenceFile = "SWARM_HOOK_SEQ_FILE"
	// EnvCapture lists the session adapter's capture=raw events (ADR-010 §6),
	// comma-separated. A hook invocation keeps the CLI's own event body only for an
	// event named here.
	//
	// It is injected for the same reason the token is: the hook runs as a child of
	// the AGENT and knows its event name but not the adapter that declared it, so
	// the core resolves adapter.CaptureEvents once at spawn and hands down the
	// answer. An empty or absent value means this session captures nothing, which is
	// every adapter that implements no capture extension.
	EnvCapture = "SWARM_HOOK_CAPTURE"
)

// CaptureEnv encodes capture rows for EnvCapture; CapturesRaw decodes them. They
// are an inverse pair, like Post/Decode, so the separator is one package's
// business and not a convention two packages have to agree on by hand.
func CaptureEnv(events []string) string { return strings.Join(events, ",") }

// CapturesRaw reports whether event is one of the capture rows the core declared
// for this session. It is total: an unset, empty or malformed value captures
// nothing, which is the safe answer — a body kept for nobody is bytes on the
// socket, and the daemon's own adapter lookup is what ultimately admits it.
func CapturesRaw(getenv func(string) string, event string) bool {
	if event == "" {
		return false
	}
	for _, e := range strings.Split(getenv(EnvCapture), ",") {
		if e == event {
			return true
		}
	}
	return false
}

// FromEnv composes a Callback from the injected environment plus the event name
// and payload the hook wiring supplies. getenv abstracts os.Getenv for testing. A
// missing token fails rather than compose a tokenless (unauthenticatable)
// callback (S6, client side).
func FromEnv(getenv func(string) string, event string, payload map[string]string) (engine.Callback, error) {
	token := getenv(EnvToken)
	if token == "" {
		return engine.Callback{}, errors.New("hookclient: missing session token in environment")
	}
	seq, err := sequenceFromEnv(getenv)
	if err != nil {
		return engine.Callback{}, err
	}
	return engine.Callback{
		SessionID: getenv(EnvSessionID),
		Token:     token,
		Sequence:  seq,
		Event:     event,
		Payload:   payload,
	}, nil
}

// sequenceFromEnv obtains this invocation's monotonic sequence from the daemon-
// injected per-session counter file (EnvSequenceFile): the file's atomically-
// incremented next value, so every per-event invocation gets a strictly
// increasing number (G5).
func sequenceFromEnv(getenv func(string) string) (uint64, error) {
	path := getenv(EnvSequenceFile)
	if path == "" {
		return 0, errors.New("hookclient: missing sequence file in environment")
	}
	return nextSequence(path)
}

// nextSequence atomically increments the decimal counter stored at path and
// returns the new value. An exclusive advisory lock (flock) serializes concurrent
// `swarm hook` processes for the same session, so every invocation observes a
// distinct, strictly increasing sequence even under concurrency — the property a
// naive append-then-stat cannot guarantee. The counter starts at 0 (a fresh file
// reads empty), so the first invocation returns 1.
func nextSequence(path string) (uint64, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return 0, fmt.Errorf("hookclient: open sequence file %s: %w", path, err)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return 0, fmt.Errorf("hookclient: lock sequence file %s: %w", path, err)
	}
	// The unlock error is not actionable: f.Close() below releases the same flock
	// as a side effect, so a failed explicit unlock leaves nothing stuck.
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()

	data, err := io.ReadAll(f)
	if err != nil {
		return 0, fmt.Errorf("hookclient: read sequence file %s: %w", path, err)
	}
	var cur uint64
	if trimmed := strings.TrimSpace(string(data)); trimmed != "" {
		if cur, err = strconv.ParseUint(trimmed, 10, 64); err != nil {
			return 0, fmt.Errorf("hookclient: parse sequence file %s: %w", path, err)
		}
	}
	next := cur + 1
	out := []byte(strconv.FormatUint(next, 10))
	if _, err := f.WriteAt(out, 0); err != nil {
		return 0, fmt.Errorf("hookclient: write sequence file %s: %w", path, err)
	}
	// The counter only grows, so the new text is at least as long as the old; the
	// truncate is a cheap guard against any shorter successor leaving stale digits.
	if err := f.Truncate(int64(len(out))); err != nil {
		return 0, fmt.Errorf("hookclient: truncate sequence file %s: %w", path, err)
	}
	return next, nil
}

// Post dials the daemon's unix socket and writes cb, then closes the connection.
func Post(socketPath string, cb engine.Callback) error {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return fmt.Errorf("hookclient: dial %s: %w", socketPath, err)
	}
	defer conn.Close()
	// SetEscapeHTML(false): Callback.Raw is an untrusted CLI body carried for the
	// shaper. Default HTML escaping rewrites <, > and & inside it (6 bytes each),
	// so a body near the ingest cap could expand past a transport limit and take
	// the session's STATUS post down with it - the inversion ADR-010 section 6
	// forbids. With escaping off the bytes cross the wire verbatim.
	var buf bytes.Buffer
	e := json.NewEncoder(&buf)
	e.SetEscapeHTML(false)
	if err := e.Encode(cb); err != nil {
		return fmt.Errorf("hookclient: encode callback: %w", err)
	}
	if _, err := conn.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("hookclient: write callback: %w", err)
	}
	return nil
}

// Decode reads a single callback that Post wrote. It is the daemon-side inverse
// of Post.
func Decode(r io.Reader) (engine.Callback, error) {
	var cb engine.Callback
	if err := json.NewDecoder(r).Decode(&cb); err != nil {
		return engine.Callback{}, fmt.Errorf("hookclient: decode callback: %w", err)
	}
	return cb, nil
}
