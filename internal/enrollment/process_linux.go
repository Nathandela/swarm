//go:build linux

package enrollment

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func detachedAttrs() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
func childAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGTERM}
}
func nativeAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
func nativePTYAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true, Setctty: true, Pdeathsig: syscall.SIGKILL}
}
func enableSubreaper() error {
	fd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		return ErrUnsupported
	}
	_ = unix.Close(fd)
	return unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)
}

func ownerPeer(conn *net.UnixConn) bool {
	raw, err := conn.SyscallConn()
	if err != nil {
		return false
	}
	var uid uint32
	var credErr error
	if raw.Control(func(fd uintptr) {
		var cred *unix.Ucred
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if credErr == nil {
			uid = cred.Uid
		}
	}) != nil {
		return false
	}
	return credErr == nil && uid == uint32(os.Getuid())
}

type procEntry struct {
	identity ProcessIdentity
	ppid     int
	state    string
}

func readProc(pid int) (procEntry, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return procEntry{}, err
	}
	i := strings.LastIndexByte(string(raw), ')')
	if i < 0 {
		return procEntry{}, ErrInvalid
	}
	fields := strings.Fields(string(raw)[i+1:])
	if len(fields) < 20 {
		return procEntry{}, ErrInvalid
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return procEntry{}, err
	}
	start, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return procEntry{}, err
	}
	return procEntry{ProcessIdentity{pid, start}, ppid, fields[0]}, nil
}

func processRunning(pid int) bool {
	p, err := readProc(pid)
	return err == nil && p.state != "Z" && p.state != "X"
}

func descendants(pid int) ([]ProcessIdentity, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	procs := make(map[int]procEntry)
	for _, entry := range entries {
		child, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		p, err := readProc(child)
		if err == nil {
			procs[child] = p
		}
	}
	owned := map[int]bool{pid: true}
	for changed := true; changed; {
		changed = false
		for child, p := range procs {
			if !owned[child] && owned[p.ppid] {
				owned[child] = true
				changed = true
			}
		}
	}
	var result []ProcessIdentity
	for child := range owned {
		if child != pid && procs[child].state != "Z" && procs[child].state != "X" {
			result = append(result, procs[child].identity)
		}
	}
	return result, nil
}

// The supervisor is a subreaper, so double-forked or new-session descendants
// remain its children. Never signal a PID unless its creation identity matches.
func containDescendants(pid int) error {
	deadline := time.Now().Add(3 * time.Second)
	for {
		children, err := descendants(pid)
		if err != nil {
			return err
		}
		for _, child := range children {
			if err := signalIdentity(child, syscall.SIGKILL); err != nil {
				return err
			}
		}
		if len(children) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return ErrUnavailable
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func reapOrphans() {
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
		if pid <= 0 || err != nil {
			return
		}
	}
}

func signalIdentity(p ProcessIdentity, signal syscall.Signal) error {
	if p.PID <= 0 || p.StartTime <= 0 {
		return nil
	}
	fd, err := unix.PidfdOpen(p.PID, 0)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	// Bind the pidfd first, then validate creation time. Signaling through the
	// descriptor cannot hit another process if this PID is recycled afterward.
	if !p.Alive() {
		return nil
	}
	err = unix.PidfdSendSignal(fd, unix.Signal(signal), nil, 0)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
