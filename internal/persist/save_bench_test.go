package persist

// R1.4.1(f) perf baseline benchmark (docs/verification/perf-baseline-2026-07-18.md):
// Store.Save latency (marshal + temp-write + fsync + rename), the per-status-
// change disk cost item 3.1 (agents-tracker-tid) will optimize against.

import (
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/status"
)

func BenchmarkSave(b *testing.B) {
	store, err := NewStore(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	m := Meta{
		ID:            "bench-session",
		AgentType:     "claude",
		Cwd:           "/tmp/bench",
		LaunchOptions: map[string]string{"worktree": "true"},
		Env:           []string{"HOME=/home/bench", "PATH=/usr/bin:/bin", "SHELL=/bin/bash"},
		CreatedAt:     time.Now(),
		Status: status.Status{
			Process:     status.ProcessRunning,
			Turn:        status.TurnActive,
			Interaction: status.InteractionNone,
		},
		LastActivity:   time.Now(),
		ShimPID:        4242,
		ShimStartTime:  time.Now().UnixNano(),
		ConversationID: "conv-bench-0001",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := store.Save(m); err != nil {
			b.Fatal(err)
		}
	}
}
