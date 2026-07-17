package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Nathandela/swarm/internal/protocol"
)

// E7.6 / N-1 — first paint with 50 sessions is fast. This measures the client
// render path (model init + first View) directly; the real <=100 ms p95 @50 is
// the Epic 14 perf gate. Because N-1 requires the sessions to be present AT first
// paint, New performs the initial List() eagerly (the eager-load pin), so the
// first View already lists all 50.
//
// firstPaintBudget is generous: a pure render of 50 rows is sub-millisecond, so
// exceeding N-1's own 100 ms number here signals a pathological render path.
const firstPaintBudget = 100 * time.Millisecond

func fiftySessions() *fakeClient {
	sessions := make([]protocol.SessionView, 0, 50)
	groups := []func(id, agent, cwd, summary string, ago time.Duration) protocol.SessionView{
		sNeedsInput, sWorking, sReview, sCompleted,
	}
	for i := 0; i < 50; i++ {
		mk := groups[i%len(groups)]
		id := fmt.Sprintf("endpoint/s%02d", i)
		summary := fmt.Sprintf("session-%02d summary", i)
		sessions = append(sessions, mk(id, "claude", "~/Code/proj", summary, time.Duration(i+1)*time.Minute))
	}
	return newFakeClient(sessions...)
}

func TestFirstPaint_FiftySessionsUnderBudget(t *testing.T) {
	f := fiftySessions()

	start := time.Now()
	m := New(f, detectMixed())
	m, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 200}) // tall enough to list all 50
	out := m.View().Content
	elapsed := time.Since(start)

	if elapsed > firstPaintBudget {
		t.Fatalf("first paint of 50 sessions took %s, budget %s (N-1)", elapsed, firstPaintBudget)
	}

	// N-1 says "50 sessions listed": the first paint must already include them.
	plain := stripANSI(out)
	if !strings.Contains(plain, "session-49 summary") {
		t.Fatalf("first paint did not list all sessions (missing session-49); New must load the list eagerly:\n%s", plain)
	}
}
