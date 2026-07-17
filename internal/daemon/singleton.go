package daemon

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"syscall"
	"time"
)

// ErrAlreadyRunning is returned by Open when another daemon holds the lock.
var ErrAlreadyRunning = errors.New("daemon: another instance is already running")

// acquireLock opens the lock file (0600, under the 0700 state dir) and takes a
// non-blocking exclusive flock. flock is per open-file-description, so a second
// Open — even in the same process — contends and loses with ErrAlreadyRunning
// (S12). A pre-existing lock file is hardened to 0600.
func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		f.Close()
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrAlreadyRunning
		}
		return nil, err
	}
	return f, nil
}

// releaseLock unlocks and closes the lock file, releasing the singleton so a
// fresh daemon can take over (the clean-restart path).
func releaseLock(f *os.File) error {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return f.Close()
}

// bindSocket unlinks any stale socket file (safe only because the caller already
// holds the lock — S12) and binds the daemon UDS created private (0600) from the
// start. A tight umask brackets the bind to close the chmod-after-bind TOCTOU
// window (mirrors the shim's listen).
func bindSocket(path string) (net.Listener, error) {
	_ = os.Remove(path) // unlink a stale socket from a crashed prior daemon
	old := syscall.Umask(0o177)
	l, err := net.Listen("unix", path)
	syscall.Umask(old)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

// acceptLoop serves the daemon socket until the listener is closed. Each client
// gets the minimal version handshake, unless cfg.ConnHandler was supplied — the
// Epic 8 assembly hands connections to protocol.Server on this same socket.
func (d *Daemon) acceptLoop() {
	defer d.wg.Done()
	handle := d.serveClient
	if d.cfg.ConnHandler != nil {
		handle = d.cfg.ConnHandler
	}
	for {
		conn, err := d.listener.Accept()
		if err != nil {
			return // listener closed by Close/abandon
		}
		go handle(conn)
	}
}

// serveClient performs the client<->daemon version handshake: read the client's
// 4-byte big-endian version, reply with ProtocolVersion. The client (Dial) is the
// side that classifies a mismatch as ErrVersionSkew. Epic 6 extends the exchange;
// for Epic 5 the connection is closed after the version is reported.
func (d *Daemon) serveClient(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(helloIO))
	var hdr [4]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		return
	}
	var out [4]byte
	binary.BigEndian.PutUint32(out[:], uint32(ProtocolVersion))
	_, _ = conn.Write(out[:])
}
