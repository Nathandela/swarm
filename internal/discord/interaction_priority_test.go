package discord

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestInteractionAcknowledgementDoesNotWaitForMessageRateLimit(t *testing.T) {
	limited := make(chan struct{})
	var messages atomic.Int32
	base := mockEndpoints(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/interactions/") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if messages.Add(1) == 1 {
			close(limited)
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"retry_after":1}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"message"}`))
	})
	c, _ := NewDiscordClient("fake")
	c.apiBase = base
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.Post(ctx, Outbound{Route: Route{ChannelID: "20"}, Text: "progress"}); done <- err }()
	<-limited
	ackCtx, stop := context.WithTimeout(ctx, 200*time.Millisecond)
	defer stop()
	if err := c.REST(ackCtx, http.MethodPost, "/interactions/1/token/callback", map[string]int{"type": 5}, nil); err != nil {
		t.Fatal("interactive acknowledgement blocked behind progress:", err)
	}
	cancel()
	<-done
}
