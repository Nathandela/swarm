package swarmmobile

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/phonecore"
	"github.com/Nathandela/swarm/internal/remote/transport"
)

func TestRewindRelayCursorResetsTheLiveWaitAckGeneration(t *testing.T) {
	app, err := NewApp(&Config{StateDir: t.TempDir(), MachineID: "m"}, r4r3Custody{})
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	if err := app.core.Mutate(func(st *phonecore.State) { st.RelayCursor = 53 }); err != nil {
		t.Fatalf("seed relay cursor: %v", err)
	}

	var mu sync.Mutex
	var got []uint64
	batcher := transport.NewAckBatcher(func(_ context.Context, cursor uint64) error {
		mu.Lock()
		got = append(got, cursor)
		mu.Unlock()
		return nil
	})
	batcher.RecordGeneration(53, batcher.Generation())
	app.setAckReset(batcher.Reset)
	if err := app.rewindRelayCursor(); err != nil {
		t.Fatalf("rewindRelayCursor: %v", err)
	}
	batcher.RecordGeneration(1, batcher.Generation())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); batcher.Run(ctx) }()
	time.Sleep(2 * time.Second / transport.MaxDrainAcksPerSec)
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != 1 {
		t.Fatalf("acks after manual rewind = %v, want only new-generation cursor 1", got)
	}
}
