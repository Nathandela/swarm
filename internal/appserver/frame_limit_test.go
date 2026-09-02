package appserver

import "testing"

// TestInboundWebSocketFrameLimitRemainsEightMiB makes the companion resume regression honest:
// the fix is to ask thread/resume not to replay history, not to weaken the allocation boundary.
func TestInboundWebSocketFrameLimitRemainsEightMiB(t *testing.T) {
	if maxFrameBytes != 8<<20 {
		t.Fatalf("maxFrameBytes = %d, want 8 MiB; oversized thread history must be excluded at resume", maxFrameBytes)
	}
}
