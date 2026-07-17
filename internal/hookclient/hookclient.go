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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"

	"github.com/Nathandela/swarm/internal/engine"
)

// Environment keys the daemon injects into a session's hook environment at spawn.
const (
	EnvSessionID = "SWARM_SESSION_ID"
	EnvToken     = "SWARM_HOOK_TOKEN"
	// EnvSocket intentionally reuses the daemon's socket variable
	// (daemon.EnvSocket == "SWARM_DAEMON_SOCK") so the hook dials the same socket
	// the daemon serves.
	EnvSocket   = "SWARM_DAEMON_SOCK"
	EnvSequence = "SWARM_HOOK_SEQ"
)

// FromEnv composes a Callback from the injected environment plus the event name
// and payload the hook wiring supplies. getenv abstracts os.Getenv for testing. A
// missing token fails rather than compose a tokenless (unauthenticatable)
// callback (S6, client side).
func FromEnv(getenv func(string) string, event string, payload map[string]string) (engine.Callback, error) {
	token := getenv(EnvToken)
	if token == "" {
		return engine.Callback{}, errors.New("hookclient: missing session token in environment")
	}
	seqStr := getenv(EnvSequence)
	seq, err := strconv.ParseUint(seqStr, 10, 64)
	if err != nil {
		return engine.Callback{}, fmt.Errorf("hookclient: invalid sequence %q: %w", seqStr, err)
	}
	return engine.Callback{
		SessionID: getenv(EnvSessionID),
		Token:     token,
		Sequence:  seq,
		Event:     event,
		Payload:   payload,
	}, nil
}

// Post dials the daemon's unix socket and writes cb, then closes the connection.
func Post(socketPath string, cb engine.Callback) error {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return fmt.Errorf("hookclient: dial %s: %w", socketPath, err)
	}
	defer conn.Close()
	enc, err := json.Marshal(cb)
	if err != nil {
		return fmt.Errorf("hookclient: encode callback: %w", err)
	}
	if _, err := conn.Write(enc); err != nil {
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
