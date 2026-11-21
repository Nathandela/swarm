package voice

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/protocol"
)

// Replay a long pre-reservation outage using an already-aged intent. Time in
// the queue alone must neither invalidate the human request nor duplicate it.
func TestDiscussionResumeAfterSixteenMinutePrerequisiteDelay(t *testing.T) {
	dir := privateConfigDir(t)
	cfg := testConfig()
	refs := ManagerBridgeRefs{Socket: "/private/bridge.sock", AuthFile: "/private/bridge.key"}
	runtime := bootstrapNativeFixture(cfg, refs)
	op := fmt.Sprintf("launch:%d:%s", time.Now().Add(-16*time.Minute-48*time.Second).UnixMilli(), strings.Repeat("b", 32))
	file := managerIntentName(cfg.ConversationID)
	intent := managerLaunchIntent{Version: 1, ConversationID: cfg.ConversationID, OperationID: op, Phase: "prepared", Request: managerLaunchRequest(cfg, refs)}
	if err := saveManagerIntent(dir, file, intent); err != nil {
		t.Fatal(err)
	}
	runtime.beforeLaunch = func(id string, _ protocol.LaunchReq) {
		if id != op {
			t.Fatal("retry changed the operation", id)
		}
	}
	runtime.refusal = protocol.ErrManagerLaunchPrerequisite
	for range 3 {
		_, err := bootstrapManager(t.Context(), dir, cfg, refs, "endpoint", nil, runtime)
		if !errors.Is(err, protocol.ErrManagerLaunchPrerequisite) || len(runtime.rows) != 0 {
			t.Fatal("refusal reserved a session", err)
		}
	}
	runtime.refusal = nil
	for range 2 {
		if _, err := bootstrapManager(t.Context(), dir, cfg, refs, "endpoint", nil, runtime); err != nil {
			t.Fatal(err)
		}
	}
	if runtime.launches != 4 || len(runtime.rows) != 1 {
		t.Fatalf("three refused attempts and one applied launch expected: %d attempts, %d sessions", runtime.launches, len(runtime.rows))
	}
}
