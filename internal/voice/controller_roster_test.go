package voice

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

func TestControllerWorkerListPagesByExactEncodedBudgetWithoutLosingIdentity(t *testing.T) {
	c, _, _, _ := provenControllerFixture(t)
	rows := make([]protocol.SessionView, 120)
	for i := range rows {
		rows[i] = protocol.SessionView{ID: fmt.Sprintf("machine/worker-%03d", i), Agent: "codex", Cwd: "/" + strings.Repeat("<", 4000), Capabilities: &schema.SessionCapabilities{SessionInstance: fmt.Sprintf("instance-%03d", i)}}
	}
	c.workers = &controllerWorkerFixture{rows: rows}
	seen := map[string]bool{}
	after := ""
	for {
		args, _ := json.Marshal(ListInput{AfterSessionID: after})
		raw, err := c.ManagerCall(context.Background(), ManagerToolCall{Name: "swarm_list", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) > MaxUtteranceBytes {
			t.Fatal("roster page exceeded byte budget")
		}
		var page WorkerPage
		if err = json.Unmarshal(raw, &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Workers) == 0 {
			t.Fatal("page made no progress")
		}
		for _, row := range page.Workers {
			if seen[row.Session] || row.Cwd != "/"+strings.Repeat("<", 4000) || row.SessionInstance == "" || row.Capabilities == nil {
				t.Fatal("pagination truncated or repeated worker identity/cwd")
			}
			seen[row.Session] = true
		}
		if page.NextSessionID == "" {
			break
		}
		if page.NextSessionID <= after {
			t.Fatal("cursor did not advance")
		}
		after = page.NextSessionID
	}
	if len(seen) != len(rows) {
		t.Fatalf("lost workers: got %d want %d", len(seen), len(rows))
	}
}
func TestControllerWorkerListEmptyInputKeepsBoundedDefault(t *testing.T) {
	c, _, _, _ := provenControllerFixture(t)
	rows := make([]protocol.SessionView, 80)
	for i := range rows {
		rows[i] = protocol.SessionView{ID: fmt.Sprintf("machine/%03d", i), Agent: "codex"}
	}
	c.workers = &controllerWorkerFixture{rows: rows}
	raw, err := c.ManagerCall(context.Background(), ManagerToolCall{Name: "swarm_list", Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	var page WorkerPage
	if err = json.Unmarshal(raw, &page); err != nil || len(page.Workers) != 50 || page.NextSessionID == "" {
		t.Fatal("empty input did not retain bounded compatible default", err)
	}
	if _, err = c.ManagerCall(context.Background(), ManagerToolCall{Name: "swarm_list", Arguments: json.RawMessage(`{"limit":101}`)}); err == nil {
		t.Fatal("list ceiling ignored")
	}
}
