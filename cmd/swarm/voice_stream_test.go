package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"testing/iotest"
	"time"

	"github.com/Nathandela/swarm/internal/voice/openailive"
)

type liveVoiceFixture struct {
	input     chan []byte
	finished  chan struct{}
	events    chan openailive.Event
	readError error
}

func newLiveVoiceFixture() *liveVoiceFixture {
	return &liveVoiceFixture{input: make(chan []byte, 8), finished: make(chan struct{}), events: make(chan openailive.Event, 8)}
}

func (s *liveVoiceFixture) SendAudio(ctx context.Context, pcm []byte) error {
	select {
	case s.input <- bytes.Clone(pcm):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *liveVoiceFixture) Next(ctx context.Context) (openailive.Event, error) {
	select {
	case event, ok := <-s.events:
		if !ok {
			return openailive.Event{}, s.readError
		}
		return event, nil
	case <-ctx.Done():
		return openailive.Event{}, ctx.Err()
	}
}

func (s *liveVoiceFixture) Finish(context.Context) error {
	close(s.finished)
	return nil
}

func voiceStreamPipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	return reader, writer
}

func waitVoiceStream[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("live voice fixture stalled")
		var zero T
		return zero
	}
}

func TestLiveVoicePumpsBothDirectionsAndFinishesOnEOF(t *testing.T) {
	in, producer := voiceStreamPipe(t)
	consumer, out := voiceStreamPipe(t)
	s := newLiveVoiceFixture()
	done := make(chan error, 1)
	go func() { done <- pumpLiveVoice(t.Context(), s, in, out, time.Second) }()
	if _, err := producer.Write([]byte{1, 2}); err != nil {
		t.Fatal(err)
	}
	if got := waitVoiceStream(t, s.input); !bytes.Equal(got, []byte{1, 2}) {
		t.Fatalf("sent %v", got)
	}
	s.events <- openailive.Event{Type: "session.input_transcript.delta", Text: "not raw PCM"}
	s.events <- openailive.Event{Type: "session.output_audio.delta", Audio: []byte{3, 4}}
	pcm := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 2)
		_, err := io.ReadFull(consumer, buf)
		if err != nil {
			buf = nil
		}
		pcm <- buf
	}()
	if got := waitVoiceStream(t, pcm); !bytes.Equal(got, []byte{3, 4}) {
		t.Fatalf("received %v", got)
	}
	if err := producer.Close(); err != nil {
		t.Fatal(err)
	}
	waitVoiceStream(t, s.finished)
	s.events <- openailive.Event{Type: "session.closed"}
	if err := waitVoiceStream(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestLiveVoicePreservesSamplesAndRejectsTrailingByte(t *testing.T) {
	for _, pcm := range [][]byte{{1, 2, 3, 4}, {1, 2, 3}} {
		s := newLiveVoiceFixture()
		err := sendLiveVoiceAudio(t.Context(), s, iotest.OneByteReader(bytes.NewReader(pcm)))
		if len(pcm)%2 == 0 {
			if err != nil {
				t.Fatal(err)
			}
			waitVoiceStream(t, s.finished)
		} else {
			if err == nil || err.Error() != "voice input ends with a partial PCM16 sample" {
				t.Fatalf("partial sample returned %v", err)
			}
			select {
			case <-s.finished:
				t.Fatal("invalid PCM was finalized")
			default:
			}
		}
		for i := 0; i+1 < len(pcm); i += 2 {
			if got := waitVoiceStream(t, s.input); !bytes.Equal(got, pcm[i:i+2]) {
				t.Fatalf("sent %v", got)
			}
		}
	}
}

func TestLiveVoiceDisconnectInterruptsStalledInput(t *testing.T) {
	in, _ := voiceStreamPipe(t)
	s := newLiveVoiceFixture()
	s.readError = errors.New("synthetic disconnect")
	done := make(chan error, 1)
	go func() { done <- pumpLiveVoice(t.Context(), s, in, io.Discard, time.Second) }()
	close(s.events)
	if err := waitVoiceStream(t, done); !errors.Is(err, s.readError) {
		t.Fatalf("disconnect returned %v", err)
	}
	if _, err := in.Stat(); err != nil {
		t.Fatal("pump closed caller input", err)
	}
}

func TestLiveVoiceStalledPipeOutputIsBounded(t *testing.T) {
	for _, cancelRun := range []bool{true, false} {
		in, _ := voiceStreamPipe(t)
		_, out := voiceStreamPipe(t)
		s := newLiveVoiceFixture()
		done := make(chan error, 1)
		ctx, cancel := context.WithCancel(t.Context())
		for range 4 {
			s.events <- openailive.Event{Type: "session.output_audio.delta", Audio: make([]byte, 64<<10)}
		}
		timeout := 100 * time.Millisecond
		if cancelRun {
			timeout = time.Minute
		}
		go func() { done <- pumpLiveVoice(ctx, s, in, out, timeout) }()
		if cancelRun {
			deadline := time.Now().Add(time.Second)
			for len(s.events) == 4 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			cancel()
		}
		err := waitVoiceStream(t, done)
		cancel()
		if cancelRun && !errors.Is(err, context.Canceled) || !cancelRun && !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("stalled output gave %v", err)
		}
		if _, err := out.Stat(); err != nil {
			t.Fatal("pump closed caller output", err)
		}
	}
}

func TestLiveVoiceDoesNotWaitForeverForClosure(t *testing.T) {
	in, producer := voiceStreamPipe(t)
	if err := producer.Close(); err != nil {
		t.Fatal(err)
	}
	s := newLiveVoiceFixture()
	done := make(chan error, 1)
	go func() { done <- pumpLiveVoice(t.Context(), s, in, io.Discard, 20*time.Millisecond) }()
	waitVoiceStream(t, s.finished)
	if err := waitVoiceStream(t, done); err == nil || err.Error() != "OpenAI Live did not confirm session closure" {
		t.Fatalf("missing close confirmation returned %v", err)
	}
}

func TestLiveVoiceDelegationRequiresManager(t *testing.T) {
	in, _ := voiceStreamPipe(t)
	s := newLiveVoiceFixture()
	s.events <- openailive.Event{Type: "session.delegation.created"}
	err := pumpLiveVoice(t.Context(), s, in, io.Discard, time.Second)
	if err == nil || err.Error() != "native manager connection required for OpenAI Live delegation" {
		t.Fatalf("delegation returned %v", err)
	}
}

func TestLiveVoicePacesFastInputThroughEOF(t *testing.T) {
	s := newLiveVoiceFixture()
	start := time.Now()
	if err := sendLiveVoiceAudio(t.Context(), s, bytes.NewReader(make([]byte, 8192))); err != nil {
		t.Fatal(err)
	}
	waitVoiceStream(t, s.finished)
	if elapsed := time.Since(start); elapsed < 165*time.Millisecond {
		t.Fatalf("finished before 8192 PCM bytes could play: %v", elapsed)
	}
}

func TestLiveVoiceCancellationDuringPCMPacing(t *testing.T) {
	s := newLiveVoiceFixture()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- sendLiveVoiceAudio(ctx, s, bytes.NewReader(make([]byte, 4096))) }()
	waitVoiceStream(t, s.input)
	cancel()
	if err := waitVoiceStream(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("pacing cancellation returned %v", err)
	}
	select {
	case <-s.finished:
		t.Fatal("canceled PCM was finalized")
	default:
	}
}
