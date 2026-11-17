package voice

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

type privateHTTP struct {
	socket       string
	identity     os.FileInfo
	server       *http.Server
	listener     *net.UnixListener
	lock         *os.File
	cancel       context.CancelFunc
	done, closed chan struct{}
	once         sync.Once
	err          error
}

func startPrivateHTTP(parent context.Context, dir string, handler http.Handler) (*privateHTTP, error) {
	return startPrivateHTTPOn(parent, dir, "bridge.sock", "controller.lock", handler)
}

func startPrivateHTTPOn(parent context.Context, dir, socketName, lockName string, handler http.Handler) (*privateHTTP, error) {
	d, err := openVoiceDir(dir, true)
	if err != nil {
		return nil, err
	}
	_ = d.Close()
	lock, err := os.OpenFile(filepath.Join(dir, lockName), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	locked := false
	fail := func(err error) (*privateHTTP, error) {
		if locked {
			_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		}
		_ = lock.Close()
		return nil, err
	}
	info, err := lock.Stat()
	if err != nil || !privateRegular(info) || info.Mode().Perm() != 0600 {
		return fail(ErrInvalid)
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fail(errors.New("voice: controller already running"))
	}
	locked = true
	path := filepath.Join(dir, socketName)
	if info, err := os.Lstat(path); err == nil {
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != uint32(os.Getuid()) || owner.Nlink != 1 || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 {
			return fail(errors.New("voice: unsafe existing controller endpoint"))
		}
		conn, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			return fail(errors.New("voice: controller endpoint already active"))
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) {
			return fail(dialErr)
		}
		current, err := os.Lstat(path)
		if err != nil || !os.SameFile(info, current) {
			return fail(errors.New("voice: controller endpoint changed"))
		}
		if err = os.Remove(path); err != nil {
			return fail(err)
		}
	} else if !os.IsNotExist(err) {
		return fail(err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return fail(err)
	}
	listener.SetUnlinkOnClose(false)
	identity, err := os.Lstat(path)
	if err != nil {
		_ = listener.Close()
		return fail(err)
	}
	if err = os.Chmod(path, 0600); err != nil {
		_ = listener.Close()
		removeOwnedSocket(path, identity)
		return fail(err)
	}
	ctx, cancel := context.WithCancel(parent)
	s := &privateHTTP{socket: path, identity: identity, listener: listener, lock: lock, cancel: cancel, done: make(chan struct{}), closed: make(chan struct{})}
	s.server = &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 35 * time.Second, MaxHeaderBytes: 8192, BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() { s.err = s.server.Serve(listener); close(s.done) }()
	go func() {
		select {
		case <-ctx.Done():
		case <-s.done:
		}
		_ = s.Close()
	}()
	return s, nil
}

func removeOwnedSocket(path string, identity os.FileInfo) {
	if current, err := os.Lstat(path); err == nil && os.SameFile(identity, current) {
		_ = os.Remove(path)
	}
}

func (s *privateHTTP) Close() error {
	s.once.Do(func() {
		s.cancel()
		_ = s.server.Close()
		<-s.done
		removeOwnedSocket(s.socket, s.identity)
		_ = syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
		_ = s.lock.Close()
		close(s.closed)
	})
	if errors.Is(s.err, http.ErrServerClosed) {
		return nil
	}
	return s.err
}
