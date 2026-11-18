package voice

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagerPTTDurableExactFinalization(t *testing.T) {
	dir := privateServerDir(t)
	cfg := testConfig()
	refs := ManagerBridgeRefs{Socket: filepath.Join(dir, "bridge.sock"), AuthFile: filepath.Join(dir, managerBridgeAuth)}
	native := bootstrapNativeFixture(cfg, refs)
	native.runtime.ReadyForInput = false
	runtime := &serviceRuntimeFixture{bootstrapFixture: native, controllerRuntimeFixture: &controllerRuntimeFixture{status: native.runtime}}
	var calls atomic.Int32
	entered := make(chan struct{}, 2)
	finished := make(chan struct{}, 2)
	release := make(chan struct{})
	prepare := func() (pttTranscriber, error) {
		return func(ctx context.Context, _ []byte) (string, error) {
			calls.Add(1)
			entered <- struct{}{}
			defer func() { finished <- struct{}{} }()
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-release:
				return "Canonical transcript", nil
			}
		}, nil
	}
	client := NewManagerClient(dir)
	defer client.Close()
	start := func() func() {
		t.Helper()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- serveManager(ctx, dir, "endpoint", cfg, runtime, prepare, nil) }()
		stop := sync.OnceFunc(func() {
			cancel()
			if err := <-done; err != nil {
				t.Error(err)
			}
		})
		t.Cleanup(stop)
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if state, err := client.Status(ctx); err == nil && state.Ready {
				return stop
			}
		}
		stop()
		t.Fatal("PTT controller never became ready")
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	pcm := []byte{1, 2, 3, 4}
	otherAudio := []byte{2, 3}
	stop := start()
	if _, err := client.FinalizePTT(ctx, "not-begun", pcm); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unbegun capture: %v", err)
	}
	if capture, err := client.BeginPTT(ctx, "interrupted"); err != nil || capture.Phase != "capturing" {
		t.Fatalf("capture: %+v, %v", capture, err)
	}
	if _, err := client.SubmitText(ctx, "interrupted", "spoofed text"); !errors.Is(err, ErrConflict) {
		t.Fatalf("text overwrote capture: %v", err)
	}
	audioCtx, interrupt := context.WithCancel(ctx)
	interrupted := make(chan error, 1)
	go func() {
		_, err := client.FinalizePTT(audioCtx, "interrupted", pcm)
		interrupted <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("provider was not entered")
	}
	if _, err := client.FinalizePTT(ctx, "interrupted", otherAudio); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed audio: %v", err)
	}
	if _, err := client.FinalizePTT(ctx, "interrupted", pcm); !errors.Is(err, ErrPTTUnconfirmed) {
		t.Fatalf("concurrent repeat: %v", err)
	}
	interrupt()
	if err := <-interrupted; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request: %v", err)
	}
	stop()
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("provider did not observe cancellation")
	}

	stop = start()
	if _, err := client.Receipt(ctx, "interrupted"); !errors.Is(err, ErrPTTUnconfirmed) {
		t.Fatalf("interrupted receipt after restart: %v", err)
	}
	if _, err := client.FinalizePTT(ctx, "interrupted", pcm); !errors.Is(err, ErrPTTUnconfirmed) || calls.Load() != 1 {
		t.Fatalf("interrupted repeat after restart: %v, calls=%d", err, calls.Load())
	}
	if _, err := client.BeginPTT(ctx, "finished"); err != nil {
		t.Fatal(err)
	}
	close(release)
	receipt, err := client.FinalizePTT(ctx, "finished", pcm)
	if err != nil {
		t.Fatal(err)
	}
	stop()
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Snapshot()
	_ = store.Close()
	if err != nil {
		t.Fatal(err)
	}
	stored := state.Utterances[key(cfg.ConversationID, "finished")]
	if stored.Text != "Canonical transcript" || stored.Admission != receipt.Sequence {
		t.Fatalf("canonical text and receipt disagree: %+v, %+v", stored, receipt)
	}
	stop = start()
	defer stop()
	repeat, err := client.FinalizePTT(ctx, "finished", pcm)
	if err != nil || repeat.Sequence != receipt.Sequence || calls.Load() != 2 {
		t.Fatalf("completed repeat: %+v, %v; provider calls=%d", repeat, err, calls.Load())
	}
	if _, err := client.FinalizePTT(ctx, "finished", otherAudio); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed audio after success and restart: %v", err)
	}
	repeat, err = client.Receipt(ctx, "finished")
	if err != nil || repeat.Sequence != receipt.Sequence {
		t.Fatalf("completed receipt: %+v, %v", repeat, err)
	}
}
