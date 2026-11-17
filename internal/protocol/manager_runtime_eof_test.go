package protocol

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/Nathandela/swarm/internal/managerproof"
)

type managerReplyBeforeEOF struct {
	net.Conn
	deliver func()
}

func (c managerReplyBeforeEOF) Write(p []byte) (int, error) {
	c.deliver()
	return len(p), nil
}

func TestManagerRequestKeepsCustodyErrorWhenReplyAndEOFAreReady(t *testing.T) {
	for range 50 {
		c := &Client{respCh: make(chan Control, 1), done: make(chan struct{})}
		c.conn = managerReplyBeforeEOF{deliver: func() {
			c.dispatchControl(Control{Op: OpError, ErrorCode: CodeManagerCustodyLost})
			close(c.done)
		}}
		if _, err := c.managerRequest(context.Background(), Control{Op: OpManagerRuntime}); !errors.Is(err, managerproof.ErrQuarantined) {
			t.Fatalf("buffered custody error lost to EOF: %v", err)
		}
	}
}
