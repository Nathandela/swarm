package skeleton

import (
	"context"
	"encoding/json"
	"testing"
)

func TestDiscussionCodexNativeIdleRequiresExactMetadataOnlyThread(t *testing.T) {
	const thread = "019c0000-0000-7000-8000-000000000001"
	for _, test := range []struct {
		id, state string
		idle      bool
	}{
		{thread, "idle", true},
		{thread, "active", false},
		{"different-thread", "idle", false},
	} {
		conn := &codexManagerTestConn{call: func(_ context.Context, method string, params, out any) error {
			body := params.(map[string]any)
			if method != "thread/read" || body["threadId"] != thread || body["includeTurns"] != false {
				t.Fatalf("native readiness inspected the wrong request: %s %+v", method, body)
			}
			return json.Unmarshal([]byte(`{"thread":{"id":"`+test.id+`","status":{"type":"`+test.state+`"}}}`), out)
		}}
		if idle := discussionCodexThreadIdle(&sessionBackend{threadID: thread, conn: conn}); idle != test.idle {
			t.Fatalf("thread %s status %s: idle=%t", test.id, test.state, idle)
		}
	}
}
