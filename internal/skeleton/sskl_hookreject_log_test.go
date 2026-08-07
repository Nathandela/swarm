package skeleton

// agents-tracker-sskl FIX C: a hook callback the engine rejects (bad token,
// unknown session, replayed sequence) used to be discarded silently, so a whole
// session's status signal could be dead with nothing in the daemon log to say
// so. The rejection is now logged; the accept path is unchanged, so a rejected
// post still must not wedge the shared socket.

import (
	"bytes"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a mutex-guarded log sink: the daemon writes from its own
// connection goroutine while the test polls.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestSSKL_RejectedHookCallbackIsLogged(t *testing.T) {
	var sink syncBuffer
	log.SetOutput(&sink)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	sk := assemble(t)
	postBogusHook(t, sk.SocketPath())

	// serveHook runs on the daemon's own connection goroutine, so poll.
	const want = "skeleton: hook callback rejected"
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(sink.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("a rejected hook callback logged nothing containing %q; log was:\n%s", want, sink.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The accept path is untouched: the shared socket still serves clients.
	c := dialClient(t, sk, "attach")
	if _, err := c.List(); err != nil {
		t.Fatalf("client socket wedged after a rejected hook post: %v", err)
	}
}
