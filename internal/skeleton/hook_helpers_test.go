package skeleton

import (
	"testing"

	"github.com/Nathandela/swarm/internal/engine"
	"github.com/Nathandela/swarm/internal/hookclient"
)

// postBogusHook sends one unauthenticated hook callback (raw JSON) to the daemon
// socket. The engine rejects it (S6: wrong token / unknown session), so the status
// is unchanged — but the assembly must have DEMUXED it to the hook path rather than
// mis-reading it as a framed client op that corrupts the connection loop. The
// error (if any) is intentionally ignored; the assertion is on the socket's
// continued health afterward.
func postBogusHook(t *testing.T, sock string) {
	t.Helper()
	_ = hookclient.Post(sock, engine.Callback{
		SessionID: "no-such-session",
		Token:     "bogus-token",
		Sequence:  1,
		Event:     "Stop",
		Payload:   map[string]string{engine.PayloadKeyTurn: "idle"},
	})
}
