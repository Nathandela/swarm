package appserver

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

func TestRespondAuthTokensRechecksOwnershipAfterWriteContention(t *testing.T) {
	// No socket is installed: a stale/cancelled response must never reach the write.
	for _, cancelled := range []bool{false, true} {
		c := &Client{}
		c.writeMu.Lock()
		var current atomic.Bool
		current.Store(true)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- c.RespondAuthTokens(ctx, json.RawMessage(`1`), struct{}{}, current.Load) }()
		if cancelled {
			cancel()
		} else {
			current.Store(false)
		}
		c.writeMu.Unlock()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("stale auth response accepted")
			}
		case <-time.After(time.Second):
			t.Fatal("auth writer did not release")
		}
		cancel()
	}
}
