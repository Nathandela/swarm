//go:build darwin || linux

package attach

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/muesli/cancelreader"
)

type heldTermCancelReader struct {
	entered, canceled, release, closed chan struct{}
	enterOnce, cancelOnce, closeOnce   sync.Once
	closeErr                           error
	reads                              atomic.Int32
}

func (r *heldTermCancelReader) Read([]byte) (int, error) {
	r.reads.Add(1)
	r.enterOnce.Do(func() { close(r.entered) })
	<-r.canceled
	<-r.release
	return 0, cancelreader.ErrCanceled
}
func (r *heldTermCancelReader) Cancel() bool {
	r.cancelOnce.Do(func() { close(r.canceled) })
	return true
}
func (r *heldTermCancelReader) Close() error {
	r.closeOnce.Do(func() { close(r.closed) })
	return r.closeErr
}

func TestRealTermRestoreCancelsJoinsReadBeforeClosing(t *testing.T) {
	master, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = master.Close() }()
	defer func() { _ = tty.Close() }()
	reader := &heldTermCancelReader{entered: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}), closed: make(chan struct{}), closeErr: errors.New("synthetic polling close error")}
	term := &realTerm{in: tty, out: tty, cr: reader}
	restore, err := term.MakeRaw()
	if err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(reader.release) }) }
	defer release()
	readDone := make(chan struct{})
	go func() { defer close(readDone); _, _ = term.In().Read(make([]byte, 1)) }()
	select {
	case <-reader.entered:
	case <-time.After(time.Second):
		t.Fatal("owned input read did not enter")
	}
	restoreDone := make(chan error, 1)
	go func() { restoreDone <- restore() }()
	select {
	case <-reader.canceled:
	case <-time.After(time.Second):
		release()
		t.Fatal("restore did not cancel active input read")
	}
	// Hold the poll-ready/underlying-Read window. Switching to canonical mode
	// here could make that Read wait for a newline instead of honoring Cancel.
	if raw, err := isRaw(tty.Fd()); err != nil || !raw {
		t.Error("terminal became canonical before active Read joined", err)
	}
	// Keep the canceled Read deliberately alive; its polling fds remain owned.

	restoreFinished := false
	var restoreErr error
	select {
	case <-reader.closed:
		t.Error("cancelreader closed before its active Read joined")
	case restoreErr = <-restoreDone:
		restoreFinished = true
		t.Error("restore returned before its active Read joined")
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("canceled input Read did not join")
	}
	if !restoreFinished {
		select {
		case restoreErr = <-restoreDone:
		case <-time.After(time.Second):
			t.Fatal("restore did not join after canceled Read")
		}
	}
	if restoreErr != nil {
		t.Fatal(restoreErr)
	}
	if raw, err := isRaw(tty.Fd()); err != nil || raw {
		t.Fatal("termios not restored after reader cleanup", err)
	}
	select {
	case <-reader.closed:
	default:
		t.Fatal("joined input reader polling fds were not closed")
	}
	before := reader.reads.Load()
	n, err := term.In().Read(make([]byte, 1))
	if n != 0 || !errors.Is(err, cancelreader.ErrCanceled) || reader.reads.Load() != before {
		t.Fatal("post-restore Read touched retired cancelreader")
	}
	if _, err := term.MakeRaw(); !errors.Is(err, cancelreader.ErrCanceled) {
		t.Fatal("retired terminal reader was rearmed without a new lifetime")
	}
}
