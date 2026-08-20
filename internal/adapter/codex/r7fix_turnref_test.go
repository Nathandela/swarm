package codex_test

// WAVE R7, ROUND 2 -- Interaction.TurnRef, the adapter half of review BLOCKING 1.
//
// The daemon's `turn_id` is a ULID IT mints for the phone (IS-ENV-1 is and stays the daemon's
// rule). The app-server's turn ids are UUIDv7 and it takes ITS OWN as a PRECONDITION on
// turn/steer ("Required active turn id precondition. The request fails when it does not match
// the currently active turn") and as the subject of turn/interrupt. The adapter is the ONLY
// party that sees the CLI's own id, so if it does not source it, nothing downstream can name
// it -- which is exactly what round 1 shipped: the daemon steered with an id it minted itself
// and every mid-turn phone send was rejected by a real server.
//
// EVERY FRAME HERE IS VERBATIM from docs/verification/r1-codex-fixtures/. `turnId` is on EVERY
// content-bearing notification (item/started, item/completed, item/agentMessage/delta and both
// */requestApproval), and `turn/completed` names the turn in `params.turn.id` instead.

import (
	"testing"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/codex"
)

const (
	fixTurnID   = "01a0033b-d0be-77e1-88e7-584ddeea562d"
	fixApprTurn = "01a00335-d9d8-7d72-8682-775ad560a74c"
)

func fixShape(t *testing.T, event, raw string) []adapter.Interaction {
	t.Helper()
	src, ok := adapter.AsInteractionSource(codex.New())
	if !ok {
		t.Fatal("the codex adapter proves no InteractionSource")
	}
	return src.Interactions(adapter.HookPayload{Event: event, Raw: []byte(raw)})
}

// TestR7FixTurnRef_EveryShapedFrameCarriesTheCLIsOwnTurnId is the whole finding in one table.
// A kind that drops the turn id is a kind after which the daemon can no longer steer or
// interrupt, because nativeTurns is refreshed by whatever arrives.
func TestR7FixTurnRef_EveryShapedFrameCarriesTheCLIsOwnTurnId(t *testing.T) {
	cases := []struct {
		name  string
		event string
		raw   string
		want  string
	}{
		{
			name:  "item/started userMessage -- the frame that OPENS the turn",
			event: "item/started",
			raw:   `{"method":"item/started","params":{"item":{"type":"userMessage","id":"01a0033b-d17f-7070-9744-a3fb14dee165","clientId":null,"content":[{"type":"text","text":"Count from 1 to 40."}]},"threadId":"01a00339-a80e-72a0-966f-116427b6b9ce","turnId":"01a0033b-d0be-77e1-88e7-584ddeea562d","startedAtMs":1786760647039}}`,
			want:  fixTurnID,
		},
		{
			name:  "item/started commandExecution",
			event: "item/started",
			raw:   `{"method":"item/started","params":{"item":{"type":"commandExecution","id":"exec-1","command":"ls -la","status":"inProgress"},"threadId":"t","turnId":"01a0033b-d0be-77e1-88e7-584ddeea562d"}}`,
			want:  fixTurnID,
		},
		{
			name:  "item/completed commandExecution",
			event: "item/completed",
			raw:   `{"method":"item/completed","params":{"item":{"type":"commandExecution","id":"exec-1","command":"ls -la","status":"completed","aggregatedOutput":"a\nb\n","exitCode":0},"threadId":"t","turnId":"01a0033b-d0be-77e1-88e7-584ddeea562d"}}`,
			want:  fixTurnID,
		},
		{
			name:  "item/agentMessage/delta",
			event: "item/agentMessage/delta",
			raw:   `{"method":"item/agentMessage/delta","params":{"threadId":"01a00339-a80e-72a0-966f-116427b6b9ce","turnId":"01a0033b-d0be-77e1-88e7-584ddeea562d","itemId":"msg_09","delta":"One"}}`,
			want:  fixTurnID,
		},
		{
			name:  "turn/completed -- names the turn in params.turn.id, not params.turnId",
			event: "turn/completed",
			raw:   `{"method":"turn/completed","params":{"threadId":"t","turn":{"id":"01a0033b-d0be-77e1-88e7-584ddeea562d","items":[],"itemsView":"notLoaded","status":"interrupted"}}}`,
			want:  fixTurnID,
		},
		{
			name:  "item/fileChange/requestApproval",
			event: "item/fileChange/requestApproval",
			raw:   `{"method":"item/fileChange/requestApproval","id":0,"params":{"threadId":"01a00335-9a50-79e2-8253-e08861d67c4d","turnId":"01a00335-d9d8-7d72-8682-775ad560a74c","itemId":"exec-29bcdedd-84f6-423c-931d-0f0433cc3328","startedAtMs":1786760259641,"reason":null,"grantRoot":null}}`,
			want:  fixApprTurn,
		},
		{
			name:  "item/commandExecution/requestApproval",
			event: "item/commandExecution/requestApproval",
			raw:   `{"method":"item/commandExecution/requestApproval","id":1,"params":{"threadId":"th","turnId":"01a00335-d9d8-7d72-8682-775ad560a74c","itemId":"exec-2","command":"rm -rf /tmp/x"}}`,
			want:  fixApprTurn,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			items := fixShape(t, tc.event, tc.raw)
			if len(items) != 1 {
				t.Fatalf("shaped %d items, want 1", len(items))
			}
			if items[0].TurnRef != tc.want {
				t.Errorf("TurnRef = %q, want the CLI's own %q. Without it the daemon cannot name "+
					"this turn on turn/steer or turn/interrupt, and round 1's answer was to send "+
					"an id it minted itself -- which the server rejects every time",
					items[0].TurnRef, tc.want)
			}
		})
	}
}

// TestR7FixTurnRef_AFrameWithoutATurnIdSourcesNONERatherThanInventingOne. The daemon's rule is
// that an EMPTY ref never clears a recorded one, which only holds if the adapter reports
// absence honestly. An adapter that substituted the item id or the thread id here would put a
// value the server has never seen into the steer precondition -- the same defect wearing a
// different id.
func TestR7FixTurnRef_AFrameWithoutATurnIdSourcesNONERatherThanInventingOne(t *testing.T) {
	items := fixShape(t, "item/agentMessage/delta",
		`{"method":"item/agentMessage/delta","params":{"threadId":"01a00339-a80e-72a0-966f-116427b6b9ce","itemId":"msg_09","delta":"One"}}`)
	if len(items) != 1 {
		t.Fatalf("shaped %d items, want 1", len(items))
	}
	if items[0].TurnRef != "" {
		t.Errorf("TurnRef = %q for a frame carrying no turnId; absence must be reported as "+
			"absence, never as the thread id or the item id wearing a turn's name", items[0].TurnRef)
	}
}
