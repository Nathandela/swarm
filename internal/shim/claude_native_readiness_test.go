package shim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/shimwire"
)

type claudeFixtureOwnedStarter interface {
	startClaudeChatPrimary(*exec.Cmd, <-chan os.Signal) error
}

func TestClaudeChatRelayConsumedStopBeforeFixtureStart(t *testing.T) {
	srv := newServer(nil, "", nil, nil, nil, 0, time.Second, &Metrics{})
	defer srv.finishEscalation()
	starter, ok := any(srv).(claudeFixtureOwnedStarter)
	if !ok {
		t.Fatal("APIabsence: fixture Start has no server pending-Stop arbitration")
	}
	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGTERM
	acknowledged := make(chan struct{})
	go func() { <-signals; srv.onSignal(shimwire.SigTerm); close(acknowledged) }()
	select {
	case <-acknowledged:
	case <-time.After(time.Second):
		t.Fatal("relay did not acknowledge Stop")
	}
	if len(signals) != 0 {
		t.Fatal("test did not force relay-consumed channel")
	}
	srv.pgidMu.Lock()
	pending := srv.pendingSig
	srv.pgidMu.Unlock()
	if pending != syscall.SIGTERM {
		t.Fatal("test has no sticky Stop witness")
	}
	// The missing executable is a Start-attempt canary: ENOENT is not cancellation.
	cmd := exec.Command(filepath.Join(t.TempDir(), "must-not-exec"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	err := starter.startClaudeChatPrimary(cmd, signals)
	if !errors.Is(err, context.Canceled) || errors.Is(err, os.ErrNotExist) || cmd.Process != nil || cmd.ProcessState != nil {
		t.Fatalf("relay-consumed Stop attempted Start: %v", err)
	}
}

// Counts the actual nonblocking read attempts; the pipe remains idle throughout.
type claudeIdleReadCounter struct {
	calls   atomic.Int32
	read    claudePipeReader
	entered chan struct{}
	again   chan struct{}
}

func (r *claudeIdleReadCounter) Read(p []byte) (int, error) {
	n := r.calls.Add(1)
	if n == 1 {
		close(r.entered)
	}
	if n == 2 {
		close(r.again)
	}
	return r.read.Read(p)
}

func TestClaudeChatIdleReadinessDoesNotPollEveryTenMilliseconds(t *testing.T) {
	spool, _, _ := producerFixture(t)
	srv := newServer(nil, "", nil, nil, nil, 0, time.Second, &Metrics{})
	c := newClaudeChatCapture(spool, srv, claudeChatPrimaryIdentity{})
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := claudePipeNonblock(r); err != nil {
		t.Fatal(err)
	}
	c.stdout = r
	counter := &claudeIdleReadCounter{read: claudePipeReader{file: r}, entered: make(chan struct{}), again: make(chan struct{})}
	done := make(chan struct{})
	go func() { defer close(done); c.readStdout(counter) }()
	t.Cleanup(func() {
		if cancel, ok := any(c).(interface{ cancelReadiness() }); ok {
			cancel.cancelReadiness()
		}
		_ = w.Close()
		_ = r.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("idle reader did not join after pipe close")
		}
		close(c.queue)
		<-c.done
		c.retireSignals()
		srv.finishEscalation()
	})
	select {
	case <-counter.entered:
	case <-time.After(time.Second):
		t.Fatal("reader never entered")
	}
	select {
	case <-counter.again:
		t.Fatal("idle readiness repeatedly re-read an unchanged pipe")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestClaudeChatCancelledReadinessJoinsWithWriterStillOpen(t *testing.T) {
	spool, _, _ := producerFixture(t)
	srv := newServer(nil, "", nil, nil, nil, 0, time.Second, &Metrics{})
	c := newClaudeChatCapture(spool, srv, claudeChatPrimaryIdentity{})
	cancel, ok := any(c).(interface{ cancelReadiness() })
	if !ok {
		close(c.queue)
		<-c.done
		t.Fatal("APIabsence: stdout readiness has no lifetime cancellation wake")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := claudePipeNonblock(r); err != nil {
		t.Fatal(err)
	}
	c.stdout = r
	counter := &claudeIdleReadCounter{read: claudePipeReader{file: r}, entered: make(chan struct{}), again: make(chan struct{})}
	done := make(chan struct{})
	go func() { defer close(done); c.readStdout(counter) }()
	t.Cleanup(func() {
		cancel.cancelReadiness()
		_ = w.Close()
		_ = r.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("cancelled reader did not join")
		}
		close(c.queue)
		<-c.done
		c.retireSignals()
		srv.finishEscalation()
	})
	select {
	case <-counter.entered:
	case <-time.After(time.Second):
		t.Fatal("reader never entered")
	}
	cancel.cancelReadiness()
	cancel.cancelReadiness() // idempotent lifetime retirement
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation waited for native writer EOF")
	}
	// Reader cancellation does not manufacture a primary record or native EOF.
	c.mu.Lock()
	eof, seq := c.eof[0], c.sequence
	c.mu.Unlock()
	if eof || seq != 0 {
		t.Fatal("cancellation manufactured stdout EOF/frame authority")
	}
}

func TestClaudeChatNativeVerboseStderrRetainsBoundedTail(t *testing.T) {
	spool, _, _ := producerFixture(t)
	srv := newServer(nil, "", nil, nil, nil, 0, time.Second, &Metrics{})
	c := newClaudeChatCapture(spool, srv, claudeChatPrimaryIdentity{})
	input := append(bytes.Repeat([]byte("x"), claudeChatBytes+37), []byte("EXACT_LAST_DIAGNOSTIC")...)
	var sink bytes.Buffer
	c.readStderr(bytes.NewReader(input), &sink)
	close(c.queue)
	<-c.done
	c.retireSignals()
	srv.finishEscalation()
	if c.lost || !c.eof[1] {
		t.Fatal("ordinary lifetime stderr verbosity killed capture or lost EOF")
	}
	if sink.Len() > 64<<10 || !bytes.Equal(sink.Bytes(), input[len(input)-(64<<10):]) {
		t.Fatalf("stderr tail bytes=%d, expected exact last64KiB", sink.Len())
	}
	body, err := c.result() // stdout has not ended; inspect diagnostics without granting completion.
	if err == nil {
		t.Fatal("stderr EOF falsely closed primary")
	}
	var diagnostics struct {
		Bytes     uint64 `json:"stderr_bytes"`
		Truncated bool   `json:"stderr_truncated"`
	}
	if json.Unmarshal(body, &diagnostics) != nil || diagnostics.Bytes != uint64(len(input)) || !diagnostics.Truncated {
		t.Fatal("stderr retained tail lacks honest total/truncation facts")
	}
	rows, _, _, err := spool.ReadFrom(0)
	if err != nil || len(rows) != 0 {
		t.Fatal("stderr became primary provenance")
	}
}

// Keep error semantics covered independently of the relaxed lifetime byte total.
func TestClaudeChatNativeStderrFaultsStillLoseCapture(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    io.Reader
		w    io.Writer
	}{{"read", &claudeChatDataError{}, io.Discard}, {"write", bytes.NewBufferString("diagnostic"), claudeChatErrorWriter{}}} {
		t.Run(tc.name, func(t *testing.T) {
			spool, _, _ := producerFixture(t)
			srv := newServer(nil, "", nil, nil, nil, 0, time.Second, &Metrics{})
			c := newClaudeChatCapture(spool, srv, claudeChatPrimaryIdentity{})
			c.readStderr(tc.r, tc.w)
			close(c.queue)
			<-c.done
			c.retireSignals()
			srv.finishEscalation()
			if !c.lost {
				t.Fatal("stderr fault was silently discarded")
			}
		})
	}
}
