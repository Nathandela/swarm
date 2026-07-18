//go:build darwin || linux

package attach

import (
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/muesli/cancelreader"
	"golang.org/x/sys/unix"
)

// realTerm is the production TermControl over the real controlling terminal fds.
// In() is a cancelable reader so the input pump can be unblocked on teardown (the
// restore func cancels it), and MakeRaw drives the termios into raw mode with a
// cfmakeraw-equivalent flag set, IXON cleared so Ctrl+S reaches the agent and Ctrl+Q
// arrives as a plain byte (consumed as the detach key by default, not XON/XOFF).
type realTerm struct {
	in  *os.File
	out *os.File
	cr  cancelreader.CancelReader
}

// NewTermControl builds the production TermControl over in/out (stdin/stdout).
func NewTermControl(in, out *os.File) (TermControl, error) {
	cr, err := cancelreader.NewReader(in)
	if err != nil {
		return nil, err
	}
	return &realTerm{in: in, out: out, cr: cr}, nil
}

func (t *realTerm) In() io.Reader  { return t.cr }
func (t *realTerm) Out() io.Writer { return t.out }

// MakeRaw puts the terminal in raw mode and returns an idempotent restore that puts
// the termios back and cancels the input reader so a blocked read unwinds.
func (t *realTerm) MakeRaw() (func() error, error) {
	fd := int(t.in.Fd())
	old, err := readTermios(fd)
	if err != nil {
		return nil, err
	}

	raw := *old
	// cfmakeraw-equivalent: canonical/echo/signal input processing off, output
	// post-processing off, 8-bit clean. IXON (XON/XOFF) is in the Iflag clear so
	// Ctrl+S reaches the agent rather than freezing the local terminal, and Ctrl+Q
	// arrives as a byte (consumed as the detach key by default) rather than XON (A-1).
	raw.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP |
		unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	raw.Cflag &^= unix.CSIZE | unix.PARENB
	raw.Cflag |= unix.CS8
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
	if err := writeTermios(fd, &raw); err != nil {
		return nil, err
	}

	var once sync.Once
	return func() error {
		var e error
		once.Do(func() {
			e = writeTermios(fd, old)
			t.cr.Cancel()
			_ = t.cr.Close()
		})
		return e
	}, nil
}

// Size reports the terminal size in cells via TIOCGWINSZ.
func (t *realTerm) Size() (int, int, error) {
	ws, err := unix.IoctlGetWinsize(int(t.in.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return 0, 0, err
	}
	return int(ws.Col), int(ws.Row), nil
}

// Resizes yields one tick per SIGWINCH. stop releases the notification and closes
// the tick channel so a ranging consumer unwinds.
func (t *realTerm) Resizes() (<-chan struct{}, func()) {
	events := make(chan struct{}, 1)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		defer close(events)
		for {
			select {
			case <-sig:
				select {
				case events <- struct{}{}:
				default: // coalesce: a pending tick already asks for the current size
				}
			case <-done:
				return
			}
		}
	}()
	return events, func() {
		signal.Stop(sig)
		close(done)
	}
}

// Signals yields the termination signals the loop restores-then-exits on. stop
// releases the notification.
func (t *realTerm) Signals() (<-chan os.Signal, func()) {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	return sig, func() { signal.Stop(sig) }
}
