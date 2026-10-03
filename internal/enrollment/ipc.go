package enrollment

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/attach"
	"github.com/Nathandela/swarm/internal/vt"
	"github.com/creack/pty"
)

const maxIPCFrame = 1 << 20

type request struct {
	Op         string          `json:"op"`
	Generation uint64          `json:"generation"`
	Worker     ProcessIdentity `json:"worker"`
	Data       []byte          `json:"data,omitempty"`
	Cols       int             `json:"cols,omitempty"`
	Rows       int             `json:"rows,omitempty"`
}

type response struct {
	Type      string      `json:"type"`
	Status    *LiveStatus `json:"status,omitempty"`
	Data      []byte      `json:"data,omitempty"`
	ErrorCode string      `json:"error_code,omitempty"`
}

type loginWorker struct {
	mu     sync.Mutex
	cfg    Config
	files  *jobFiles
	status LiveStatus
	cancel context.CancelFunc
	ln     *net.UnixListener

	termMu        sync.Mutex // atomic snapshot/subscription boundary
	emu           *vt.Emulator
	ptmx          *os.File
	attached      chan []byte
	terminalEnded bool
}

func (w *loginWorker) publish(phase string, children []ProcessIdentity, err error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.status.Progress.Phase = phase
	w.status.Progress.Children = append([]ProcessIdentity(nil), children...)
	w.status.Progress.UpdatedAt = time.Now().UTC()
	if plain(w.status.Email, 1024) {
		w.status.Progress.Email = w.status.Email
	}
	if plain(w.status.Plan, 128) {
		w.status.Progress.Plan = w.status.Plan
	}
	if err != nil {
		w.status.Progress.ErrorCode = classifyError(err)
	}
	if phase == PhaseReady || phase == PhaseCanceled || phase == PhaseFailed {
		w.status.Device = nil
	}
	return w.files.write(ProgressFile, w.status.Progress)
}

func (w *loginWorker) live() LiveStatus {
	w.mu.Lock()
	defer w.mu.Unlock()
	status := w.status
	status.Progress.Children = append([]ProcessIdentity(nil), status.Progress.Children...)
	if status.Device != nil {
		device := *status.Device
		status.Device = &device
	}
	if w.cfg.Provider == "claude" && status.Progress.Phase == PhaseAuthenticating {
		status.LoginSocket = filepath.Join(w.files.path, SocketFile)
	}
	return status
}

func (w *loginWorker) serve() error {
	if err := w.files.safe(); err != nil {
		return err
	}
	path := filepath.Join(w.files.path, SocketFile)
	if _, err := w.files.root.Lstat(SocketFile); err == nil {
		return ErrStale
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrUnsafe
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return ErrUnavailable
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return ErrUnsafe
	}
	w.ln = ln
	go func() {
		for {
			conn, err := ln.AcceptUnix()
			if err != nil {
				return
			}
			go w.serveConn(conn)
		}
	}()
	return nil
}

func scanner(conn net.Conn) *bufio.Scanner {
	s := bufio.NewScanner(conn)
	s.Buffer(make([]byte, 4096), maxIPCFrame)
	return s
}

func decodeRequest(s *bufio.Scanner) (request, bool) {
	if !s.Scan() {
		return request{}, false
	}
	if len(s.Bytes()) > 16<<10 {
		return request{}, false
	}
	var req request
	if json.Unmarshal(s.Bytes(), &req) != nil {
		return request{}, false
	}
	return req, true
}

func send(conn net.Conn, resp response) error {
	_ = conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	return json.NewEncoder(conn).Encode(resp)
}

func (w *loginWorker) serveConn(conn *net.UnixConn) {
	defer func() { _ = conn.Close() }()
	if !ownerPeer(conn) {
		return
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	s := scanner(conn)
	req, ok := decodeRequest(s)
	status := w.live()
	if !ok || req.Generation != w.cfg.Generation || req.Worker != status.Progress.Worker {
		_ = send(conn, response{Type: "error", ErrorCode: "stale_generation"})
		return
	}
	switch req.Op {
	case "status":
		_ = send(conn, response{Type: "status", Status: &status})
	case "cancel":
		// IPC cannot outrank durable daemon intent. A caller must establish the
		// canonical cancellation and its generation fence before this request.
		if !w.files.canceled(w.cfg.Generation) {
			_ = send(conn, response{Type: "error", ErrorCode: "cancel_not_recorded"})
			return
		}
		w.cancel()
		_ = send(conn, response{Type: "status", Status: &status})
	case "attach":
		w.serveAttach(conn, s)
	default:
		_ = send(conn, response{Type: "error", ErrorCode: "unsupported_operation"})
	}
}

func (w *loginWorker) serveAttach(conn *net.UnixConn, s *bufio.Scanner) {
	w.termMu.Lock()
	if w.emu == nil || w.terminalEnded || w.attached != nil {
		w.termMu.Unlock()
		_ = send(conn, response{Type: "error", ErrorCode: "terminal_unavailable"})
		return
	}
	snapshot, err := w.emu.Snapshot()
	if err != nil {
		w.termMu.Unlock()
		return
	}
	frames := make(chan []byte, 64)
	w.attached = frames
	w.termMu.Unlock()
	defer func() {
		w.termMu.Lock()
		if w.attached == frames {
			w.attached = nil
		}
		w.termMu.Unlock()
	}()
	if send(conn, response{Type: "snapshot", Data: snapshot}) != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			req, ok := decodeRequest(s)
			if !ok || req.Op == "detach" || req.Generation != w.cfg.Generation {
				return
			}
			w.termMu.Lock()
			if w.terminalEnded || w.ptmx == nil {
				w.termMu.Unlock()
				return
			}
			switch req.Op {
			case "input":
				if len(req.Data) <= 4096 {
					_, _ = w.ptmx.Write(req.Data)
				}
			case "resize":
				if req.Cols > 0 && req.Rows > 0 && req.Cols <= 200 && req.Rows <= 100 {
					_ = pty.Setsize(w.ptmx, &pty.Winsize{Cols: uint16(req.Cols), Rows: uint16(req.Rows)})
					w.emu.Resize(req.Cols, req.Rows)
				}
			default:
				w.termMu.Unlock()
				return
			}
			w.termMu.Unlock()
		}
	}()
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				_ = send(conn, response{Type: "end"})
				return
			}
			if send(conn, response{Type: "frame", Data: frame}) != nil {
				return
			}
		case <-done:
			return
		}
	}
}

func (w *loginWorker) feed(data []byte) {
	w.termMu.Lock()
	defer w.termMu.Unlock()
	if w.emu == nil || w.terminalEnded {
		return
	}
	w.emu.Feed(data)
	if w.attached != nil {
		select {
		case w.attached <- append([]byte(nil), data...):
		default:
			close(w.attached)
			w.attached = nil
		}
	}
}

func (w *loginWorker) endTerminal() {
	w.termMu.Lock()
	defer w.termMu.Unlock()
	w.terminalEnded = true
	if w.attached != nil {
		close(w.attached)
		w.attached = nil
	}
	if w.ptmx != nil {
		_ = w.ptmx.Close()
		w.ptmx = nil
	}
	if w.emu != nil {
		_ = w.emu.Close()
		w.emu = nil
	}
}

func connect(ctx context.Context, ref Ref) (*net.UnixConn, error) {
	if ref.Generation == 0 || !ref.Worker.Alive() {
		return nil, ErrStale
	}
	f, err := openJob(ref.StateRoot, ref.JobID, false)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.root.Lstat(SocketFile)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, ErrUnsafe
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return nil, ErrUnsafe
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", ref.SocketPath())
	if err != nil {
		return nil, ErrUnavailable
	}
	unixConn, ok := conn.(*net.UnixConn)
	if !ok || !ownerPeer(unixConn) {
		_ = conn.Close()
		return nil, ErrUnsafe
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	}
	return unixConn, nil
}

func control(ctx context.Context, ref Ref, op string) (LiveStatus, error) {
	conn, err := connect(ctx, ref)
	if err != nil {
		return LiveStatus{}, err
	}
	defer func() { _ = conn.Close() }()
	if json.NewEncoder(conn).Encode(request{Op: op, Generation: ref.Generation, Worker: ref.Worker}) != nil {
		return LiveStatus{}, ErrUnavailable
	}
	s := scanner(conn)
	if !s.Scan() {
		return LiveStatus{}, ErrUnavailable
	}
	var resp response
	if json.Unmarshal(s.Bytes(), &resp) != nil || resp.Type != "status" || resp.Status == nil {
		return LiveStatus{}, ErrUnavailable
	}
	status := *resp.Status
	if status.Progress.Worker != ref.Worker || status.Progress.Generation != ref.Generation || status.Progress.JobID != ref.JobID {
		return LiveStatus{}, ErrStale
	}
	return status, nil
}

func Status(ctx context.Context, ref Ref) (LiveStatus, error) { return control(ctx, ref, "status") }

// Cancel requires FenceCancel first and waits for the supervisor's writer-death
// proof; a successful native cancellation RPC alone is never sufficient.
func Cancel(ctx context.Context, ref Ref) (Progress, error) {
	f, err := openJob(ref.StateRoot, ref.JobID, false)
	if err != nil {
		return Progress{}, err
	}
	defer f.Close()
	if !f.canceled(ref.Generation) {
		return Progress{}, ErrInvalid
	}
	_, _ = control(ctx, ref, "cancel")
	// A killed supervisor's runner receives PDEATHSIG and normally drains its
	// native writers. If it remains live, terminate only its recorded identity.
	if !ref.Worker.Alive() {
		if p, err := ReadProgress(ref.StateRoot, ref.JobID); err == nil && p.Worker == ref.Worker && p.Generation == ref.Generation {
			_ = signalIdentity(p.Runner, syscall.SIGTERM)
		}
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		p, err := ReadProgress(ref.StateRoot, ref.JobID)
		if err == nil {
			if p.Generation != ref.Generation || p.Worker != ref.Worker {
				return Progress{}, ErrStale
			}
			if p.WritersStopped && !p.Runner.Alive() {
				return p, nil
			}
			if !ref.Worker.Alive() && !p.Runner.Alive() && p.NativeWritersStopped {
				p.WritersStopped = true
				p.Phase = PhaseCanceled
				return p, nil
			}
		}
		select {
		case <-ctx.Done():
			return Progress{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

// RefFromSocket validates the fixed private socket location and reads its
// nonsecret process generation. It supports the existing TUI attach interface.
func RefFromSocket(socket string) (Ref, error) {
	if filepath.Base(socket) != SocketFile || filepath.Clean(socket) != socket {
		return Ref{}, ErrUnsafe
	}
	jobDir := filepath.Dir(socket)
	jobID := filepath.Base(jobDir)
	jobs := filepath.Dir(jobDir)
	accountDir := filepath.Dir(jobs)
	if filepath.Base(jobs) != "jobs" || filepath.Base(accountDir) != "accounts" {
		return Ref{}, ErrUnsafe
	}
	stateRoot := filepath.Dir(accountDir)
	p, err := ReadProgress(stateRoot, jobID)
	if err != nil {
		return Ref{}, err
	}
	return Ref{StateRoot: stateRoot, JobID: jobID, Generation: p.Generation, Worker: p.Worker}, nil
}

func AttachSocket(ctx context.Context, socket string) (attach.Session, error) {
	ref, err := RefFromSocket(socket)
	if err != nil {
		return nil, err
	}
	return Attach(ctx, ref)
}

type attachment struct {
	conn      *net.UnixConn
	ref       Ref
	snapshot  []byte
	frames    chan []byte
	mu        sync.Mutex
	once      sync.Once
	closeOnce sync.Once
	closed    chan struct{}
}

var _ attach.Session = (*attachment)(nil)

func Attach(ctx context.Context, ref Ref) (attach.Session, error) {
	conn, err := connect(ctx, ref)
	if err != nil {
		return nil, err
	}
	if json.NewEncoder(conn).Encode(request{Op: "attach", Generation: ref.Generation, Worker: ref.Worker}) != nil {
		_ = conn.Close()
		return nil, ErrUnavailable
	}
	s := scanner(conn)
	if !s.Scan() {
		_ = conn.Close()
		return nil, ErrUnavailable
	}
	var resp response
	if json.Unmarshal(s.Bytes(), &resp) != nil || resp.Type != "snapshot" {
		_ = conn.Close()
		return nil, ErrUnavailable
	}
	if _, err := vt.DecodeSnapshot(resp.Data); err != nil {
		_ = conn.Close()
		return nil, ErrInvalid
	}
	_ = conn.SetDeadline(time.Time{})
	a := &attachment{conn: conn, ref: ref, snapshot: resp.Data, frames: make(chan []byte, 64), closed: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			a.closeConnection()
		case <-a.closed:
		}
	}()
	go func() {
		defer close(a.frames)
		defer a.closeConnection()
		for s.Scan() {
			var resp response
			if json.Unmarshal(s.Bytes(), &resp) != nil || resp.Type != "frame" {
				return
			}
			select {
			case a.frames <- resp.Data:
			case <-ctx.Done():
				return
			case <-a.closed:
				return
			}
		}
	}()
	return a, nil
}

func (a *attachment) Snapshot() []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]byte(nil), a.snapshot...)
}
func (a *attachment) Frames() <-chan []byte { return a.frames }
func (a *attachment) Generation() uint64    { return a.ref.Generation }
func (a *attachment) write(req request) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	req.Generation = a.ref.Generation
	_ = a.conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
	return json.NewEncoder(a.conn).Encode(req)
}
func (a *attachment) Input(data []byte) error {
	if len(data) > 4096 {
		return ErrInvalid
	}
	return a.write(request{Op: "input", Data: data})
}
func (a *attachment) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 || cols > 200 || rows > 100 {
		return ErrInvalid
	}
	return a.write(request{Op: "resize", Cols: cols, Rows: rows})
}
func (a *attachment) Detach() error {
	var err error
	a.once.Do(func() { err = a.write(request{Op: "detach"}); a.closeConnection() })
	return err
}

func (a *attachment) closeConnection() {
	a.closeOnce.Do(func() {
		_ = a.conn.Close()
		close(a.closed)
		a.mu.Lock()
		a.snapshot = nil
		a.mu.Unlock()
	})
}
