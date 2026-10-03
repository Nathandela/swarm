//go:build linux

// Package processcontain contains only the descendants of a dedicated owner
// process. EnableSubreaper must run before its first child, never in the daemon
// or a shared test process.
package processcontain

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func ChildAttrs() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}

func EnableSubreaper() error {
	fd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		return ErrUnsupported
	}
	_ = unix.Close(fd)
	return unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)
}

type procEntry struct {
	identity Identity
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
		return procEntry{}, ErrUnavailable
	}
	fields := strings.Fields(string(raw)[i+1:])
	if len(fields) < 20 {
		return procEntry{}, ErrUnavailable
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return procEntry{}, err
	}
	start, err := strconv.ParseInt(fields[19], 10, 64)
	if err != nil {
		return procEntry{}, err
	}
	return procEntry{Identity{pid, start}, ppid, fields[0]}, nil
}

func Running(pid int) bool {
	p, err := readProc(pid)
	return err == nil && p.state != "Z" && p.state != "X"
}

func Descendants(pid int) ([]Identity, error) {
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
	var result []Identity
	for child := range owned {
		if child != pid && procs[child].state != "Z" && procs[child].state != "X" {
			result = append(result, procs[child].identity)
		}
	}
	return result, nil
}

// The supervisor is a subreaper, so double-forked or new-session descendants
// remain its children. Never signal a PID unless its creation identity matches.
func ContainDescendants(pid int) error {
	deadline := time.Now().Add(3 * time.Second)
	for {
		children, err := Descendants(pid)
		if err != nil {
			return err
		}
		for _, child := range children {
			if err := SignalIdentity(child, syscall.SIGKILL); err != nil {
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

func ReapOrphans() {
	for {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
		if pid <= 0 || err != nil {
			return
		}
	}
}

func SignalIdentity(p Identity, signal syscall.Signal) error {
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

// ContainAndReap is called only after every direct child's exec.Cmd.Wait has
// joined. ECHILD proves the owner has no live or zombie descendants remaining.
func ContainAndReap() error {
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := ContainDescendants(os.Getpid()); err != nil {
			return err
		}
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
		if errors.Is(err, syscall.ECHILD) {
			return nil
		}
		if err != nil && !errors.Is(err, syscall.EINTR) {
			return err
		}
		if pid <= 0 {
			if time.Now().After(deadline) {
				return ErrUnavailable
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}
