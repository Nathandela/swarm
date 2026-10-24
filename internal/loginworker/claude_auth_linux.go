//go:build linux

package loginworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
	"golang.org/x/sys/unix"
)

var claudeAuthProperties = []string{"Type=exec", "RemainAfterExit=yes", "Restart=no", "RestartForceExitStatus=", "KillMode=control-group", "SendSIGKILL=yes", "TimeoutStopSec=2s", "StandardOutput=null", "StandardError=null", "LimitCORE=0", "LimitFSIZE=2097152", "TasksMax=128", "MemoryMax=805306368", "Delegate=no", "NoNewPrivileges=yes", "PrivateUsers=yes", "ReadOnlyPaths=/sys/fs/cgroup", "RestrictAddressFamilies=AF_INET AF_INET6", "SystemCallArchitectures=native", "CapabilityBoundingSet=", "RestrictNamespaces=yes", "SystemCallFilter=~@mount"}

var claudeAuthCommand = runClaudeAuthCommand

func runClaudeAuthCommand(ctx context.Context, program string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Env = cleanEnvironment(os.Environ())
	// Only the outside launcher/verifier receives the user's manager address.
	for _, key := range []string{"XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	out := &boundedBuffer{limit: 32 << 10}
	cmd.Stdout = out
	cmd.Stderr = &boundedBuffer{limit: 4096}
	if err := cmd.Run(); err != nil {
		return "", ErrUnavailable
	}
	raw, exceeded := out.Bytes()
	if exceeded {
		return "", ErrInvalid
	}
	return strings.TrimSpace(string(raw)), nil
}
func claudeAuthUnit(ctx context.Context, unit string) (map[string]string, error) {
	if !strings.HasPrefix(unit, "swarm-claude-auth-") || !strings.HasSuffix(unit, ".service") || strings.ContainsAny(unit, "/\x00\n ") {
		return nil, ErrInvalid
	}
	raw, err := claudeAuthCommand(ctx, "systemctl", "--user", "show", unit, "--property=Id,RemainAfterExit,Transient,LoadState,ActiveState,SubState,InvocationID,MainPID,ControlGroup,Job,Restart,NRestarts,RestartForceExitStatus,Type,KillMode,SendSIGKILL,Delegate,NoNewPrivileges,PrivateUsers,ReadOnlyPaths,RestrictAddressFamilies,SystemCallArchitectures,CapabilityBoundingSet,RestrictNamespaces,SystemCallFilter,RuntimeMaxUSec,TimeoutStopUSec,LimitFSIZE,MemoryMax,TasksMax,StandardOutput,StandardError")
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return nil, ErrInvalid
		}
		if _, exists := result[key]; exists {
			return nil, ErrInvalid
		}
		result[key] = value
	}
	return result, nil
}
func qualifiedClaudeAuthManager(ctx context.Context) error {
	raw, err := claudeAuthCommand(ctx, "systemctl", "--user", "--version")
	if err != nil {
		return err
	}
	fields := strings.Fields(strings.Split(raw, "\n")[0])
	if len(fields) < 2 || fields[0] != "systemd" || fields[1] != "249" {
		return ErrUnsupported
	}
	raw, err = claudeAuthCommand(ctx, "busctl", "--user", "get-property", "org.freedesktop.systemd1", "/org/freedesktop/systemd1", "org.freedesktop.systemd1.Manager", "Version")
	if err != nil {
		return err
	}
	var version string
	if !strings.HasPrefix(raw, "s ") || json.Unmarshal([]byte(strings.TrimPrefix(raw, "s ")), &version) != nil {
		return ErrUnsupported
	}
	major := strings.FieldsFunc(version, func(c rune) bool { return c == '.' || c == '-' })
	if len(major) == 0 || major[0] != "249" {
		return ErrUnsupported
	}
	return nil
}
func claudeAuthManager(ctx context.Context) (string, string, processcontain.Identity, error) {
	call := func(method string, args ...string) (string, error) {
		return claudeAuthCommand(ctx, "busctl", append([]string{"--user", "call", "org.freedesktop.DBus", "/org/freedesktop/DBus", "org.freedesktop.DBus", method}, args...)...)
	}
	decode := func(raw string) (string, error) {
		var value string
		if !strings.HasPrefix(raw, "s ") || json.Unmarshal([]byte(strings.TrimPrefix(raw, "s ")), &value) != nil {
			return "", ErrInvalid
		}
		return value, nil
	}
	raw, err := call("GetId")
	if err != nil {
		return "", "", processcontain.Identity{}, err
	}
	bus, err := decode(raw)
	if err != nil {
		return "", "", processcontain.Identity{}, err
	}
	raw, err = call("GetNameOwner", "s", "org.freedesktop.systemd1")
	if err != nil {
		return "", "", processcontain.Identity{}, err
	}
	owner, err := decode(raw)
	if err != nil {
		return "", "", processcontain.Identity{}, err
	}
	raw, err = call("GetConnectionUnixProcessID", "s", owner)
	if err != nil || !strings.HasPrefix(raw, "u ") {
		return "", "", processcontain.Identity{}, ErrUnavailable
	}
	pid, err := strconv.Atoi(strings.TrimPrefix(raw, "u "))
	if err != nil {
		return "", "", processcontain.Identity{}, ErrInvalid
	}
	start, err := procstart.StartTime(pid)
	if err != nil {
		return "", "", processcontain.Identity{}, ErrUnavailable
	}
	if err := qualifiedClaudeAuthManager(ctx); err != nil {
		return "", "", processcontain.Identity{}, err
	}
	raw, err = call("GetNameOwner", "s", "org.freedesktop.systemd1")
	if err != nil {
		return "", "", processcontain.Identity{}, err
	}
	again, err := decode(raw)
	if err != nil || again != owner {
		return "", "", processcontain.Identity{}, ErrStale
	}
	return bus, owner, processcontain.Identity{PID: pid, StartTime: start}, nil
}
func claudeAuthGroup(pid int) (string, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return "", ErrUnavailable
	}
	var result string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "0::") {
			if result != "" {
				return "", ErrInvalid
			}
			result = strings.TrimPrefix(line, "0::")
		}
	}
	if !strings.HasPrefix(result, "/") || strings.Contains(result, "..") {
		return "", ErrInvalid
	}
	return result, nil
}
func claudeAuthGroupIdentity(path string) (uint64, uint64, error) {
	var stat syscall.Stat_t
	if err := syscall.Lstat(path, &stat); err != nil {
		return 0, 0, err
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return 0, 0, ErrUnsafe
	}
	return uint64(stat.Dev), stat.Ino, nil
}
func claudeAuthMountID() (uint64, error) {
	raw, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return 0, ErrUnavailable
	}
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) > 8 && f[4] == "/sys/fs/cgroup" && strings.Contains(line, " - cgroup2 ") {
			return strconv.ParseUint(f[0], 10, 64)
		}
	}
	return 0, ErrUnsupported
}
func claudeAuthExpectedProperties(unit map[string]string) bool {
	for key, value := range map[string]string{"Transient": "yes", "RemainAfterExit": "yes", "LoadState": "loaded", "Type": "exec", "Restart": "no", "NRestarts": "0", "RestartForceExitStatus": "", "KillMode": "control-group", "SendSIGKILL": "yes", "Delegate": "no", "NoNewPrivileges": "yes", "PrivateUsers": "yes", "CapabilityBoundingSet": "", "LimitFSIZE": "2097152", "MemoryMax": "805306368", "TasksMax": "128", "StandardOutput": "null", "StandardError": "null"} {
		if observed, ok := unit[key]; !ok || observed != value {
			return false
		}
	}
	runtimeLimit, err := time.ParseDuration(strings.Join(strings.Fields(unit["RuntimeMaxUSec"]), ""))
	if err != nil || runtimeLimit <= 0 || runtimeLimit > 45*time.Second {
		return false
	}
	stopLimit, err := time.ParseDuration(strings.Join(strings.Fields(unit["TimeoutStopUSec"]), ""))
	if err != nil || stopLimit != 2*time.Second {
		return false
	}
	families := strings.Fields(unit["RestrictAddressFamilies"])
	return len(families) == 2 && ((families[0] == "AF_INET" && families[1] == "AF_INET6") || (families[0] == "AF_INET6" && families[1] == "AF_INET")) && unit["ReadOnlyPaths"] == "/sys/fs/cgroup" && unit["RestrictNamespaces"] == "yes" && claudeAuthMountFilter(unit["SystemCallFilter"]) && unit["SystemCallArchitectures"] == "native"
}
func claudeAuthMountFilter(value string) bool {
	expected := "~chroot fsconfig fsmount fsopen fspick mount mount_setattr move_mount open_tree pivot_root umount umount2"
	return strings.Join(strings.Fields(value), " ") == expected
}
func startClaudeAuthScope(ctx context.Context, executable, path string, ref logins.ClaudeAuthRef, deadline time.Time) (logins.ClaudeAuthScope, error) {
	var scope logins.ClaudeAuthScope
	if err := qualifiedClaudeAuthManager(ctx); err != nil {
		return scope, err
	}
	scope.Profile = "systemd249-user-v1"
	scope.Unit = "swarm-claude-auth-" + ref.Nonce + ".service"
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > 45*time.Second {
		return scope, ErrStale
	}
	bus, owner, manager, err := claudeAuthManager(ctx)
	if err != nil {
		return scope, err
	}
	mount, err := claudeAuthMountID()
	if err != nil {
		return scope, err
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return scope, ErrUnavailable
	}
	scope.BootID = strings.TrimSpace(string(boot))
	scope.BusID = bus
	scope.ManagerOwner = owner
	scope.Manager = manager
	scope.MountID = mount
	scope.HostMountNS, err = os.Readlink("/proc/self/ns/mnt")
	if err != nil {
		return scope, ErrUnavailable
	}
	scope.HostUserNS, err = os.Readlink("/proc/self/ns/user")
	if err != nil {
		return scope, ErrUnavailable
	}
	args := []string{"--user", "--quiet", "--unit=" + scope.Unit}
	for _, p := range claudeAuthProperties {
		args = append(args, "--property="+p)
	}
	remaining = time.Until(deadline)
	if remaining <= 0 {
		return scope, ErrStale
	}
	args = append(args, "--property=RuntimeMaxSec="+strconv.FormatFloat(remaining.Seconds(), 'f', 3, 64), executable, "internal", "login-claude-auth", path)
	if _, err := claudeAuthCommand(ctx, "systemd-run", args...); err != nil {
		return scope, err
	}
	unit, err := claudeAuthUnit(ctx, scope.Unit)
	if err != nil || !claudeAuthExpectedProperties(unit) || unit["ActiveState"] != "active" {
		return scope, ErrUnavailable
	}
	scope.InvocationID = unit["InvocationID"]
	scope.Cgroup = unit["ControlGroup"]
	pid, err := strconv.Atoi(unit["MainPID"])
	if err != nil || pid <= 0 {
		return scope, ErrInvalid
	}
	start, err := procstart.StartTime(pid)
	if err != nil {
		return scope, ErrUnavailable
	}
	scope.Worker = processcontain.Identity{PID: pid, StartTime: start}
	group, err := claudeAuthGroup(pid)
	if err != nil || group != scope.Cgroup {
		return scope, ErrStale
	}
	scope.Device, scope.Inode, err = claudeAuthGroupIdentity("/sys/fs/cgroup" + scope.Cgroup)
	if err != nil {
		return scope, ErrUnavailable
	}
	bus2, owner2, manager2, err := claudeAuthManager(ctx)
	if err != nil || bus2 != bus || owner2 != owner || manager2 != manager {
		return scope, ErrStale
	}
	return scope, nil
}

func verifyClaudeAuthScopeRetired(ctx context.Context, scope logins.ClaudeAuthScope) error {
	if err := qualifiedClaudeAuthManager(ctx); err != nil {
		return err
	}
	if scope.Worker.Alive() {
		return ErrUnavailable
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil || strings.TrimSpace(string(boot)) != scope.BootID {
		return ErrStale
	}
	mount, err := claudeAuthMountID()
	if err != nil || mount != scope.MountID {
		return ErrStale
	}
	bus, owner, manager, err := claudeAuthManager(ctx)
	if err != nil || bus != scope.BusID || owner != scope.ManagerOwner || manager != scope.Manager {
		return ErrStale
	}
	check := func() (map[string]string, error) {
		unit, err := claudeAuthUnit(ctx, scope.Unit)
		if err != nil || !claudeAuthExpectedProperties(unit) || unit["InvocationID"] != scope.InvocationID || unit["MainPID"] != "0" || !claudeAuthUnitQuiescent(unit) || (unit["Job"] != "" && unit["Job"] != "0") || (unit["ControlGroup"] != "" && unit["ControlGroup"] != scope.Cgroup) {
			return nil, ErrStale
		}
		return unit, nil
	}
	if _, err := check(); err != nil {
		return err
	}
	path := "/sys/fs/cgroup" + scope.Cgroup
	device, inode, err := claudeAuthGroupIdentity(path)
	if err == nil {
		if device != scope.Device || inode != scope.Inode {
			return ErrStale
		}
		raw, err := os.ReadFile(filepath.Join(path, "cgroup.events"))
		if err != nil || !strings.Contains("\n"+string(raw), "\npopulated 0\n") {
			return ErrUnavailable
		}
	} else if !os.IsNotExist(err) {
		return ErrUnavailable
	}
	// ENOENT is accepted only for the previously durably observed exact kernel
	// group with the same retained manager invocation. Unknown paths are not proof.
	if _, err := check(); err != nil {
		return err
	}
	bus2, owner2, manager2, err := claudeAuthManager(ctx)
	if err != nil || bus2 != bus || owner2 != owner || manager2 != manager {
		return ErrStale
	}
	return nil
}
func collectClaudeAuthScope(ctx context.Context, scope logins.ClaudeAuthScope) error {
	if scope == (logins.ClaudeAuthScope{}) {
		return nil
	}
	unit, err := claudeAuthUnit(ctx, scope.Unit)
	if err != nil {
		return err
	}
	if unit["LoadState"] == "not-found" {
		return nil
	} // caller already has durable UnitRetired receipt
	if err := verifyClaudeAuthScopeRetired(ctx, scope); err != nil {
		return err
	}
	if _, err := claudeAuthCommand(ctx, "systemctl", "--user", "stop", scope.Unit); err != nil {
		return err
	}
	unit, err = claudeAuthUnit(ctx, scope.Unit)
	if err != nil {
		return err
	}
	if unit["LoadState"] == "not-found" {
		return nil
	}
	if unit["InvocationID"] != scope.InvocationID || !claudeAuthUnitQuiescent(unit) {
		return ErrStale
	}
	_, err = claudeAuthCommand(ctx, "systemctl", "--user", "reset-failed", scope.Unit)
	return err
}
func claudeAuthUnitQuiescent(unit map[string]string) bool {
	return unit["ActiveState"] == "inactive" && unit["SubState"] == "dead" || unit["ActiveState"] == "failed" && unit["SubState"] == "failed" || unit["ActiveState"] == "active" && unit["SubState"] == "exited" && unit["RemainAfterExit"] == "yes"
}
func stopClaudeAuthRevokedWorker(ctx context.Context, scope logins.ClaudeAuthScope) error {
	bus, owner, manager, err := claudeAuthManager(ctx)
	if err != nil || bus != scope.BusID || owner != scope.ManagerOwner || manager != scope.Manager {
		return ErrStale
	}
	unit, err := claudeAuthUnit(ctx, scope.Unit)
	if err != nil || unit["InvocationID"] != scope.InvocationID {
		return ErrStale
	}
	if unit["MainPID"] == "0" {
		return verifyClaudeAuthScopeRetired(ctx, scope)
	}
	if unit["MainPID"] != strconv.Itoa(scope.Worker.PID) {
		return ErrStale
	}
	if scope.Worker.Alive() {
		group, err := claudeAuthGroup(scope.Worker.PID)
		if scope.Worker.Alive() && (err != nil || group != scope.Cgroup) {
			return ErrStale
		}
	}
	// SIGKILL main makes a failed retained unit; Stop could unload successful
	// metadata before the durable Store retirement receipt is published.
	if err := processcontain.SignalIdentity(scope.Worker, syscall.SIGKILL); err != nil {
		return ErrUnavailable
	}
	deadline := time.Now().Add(2500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if verifyClaudeAuthScopeRetired(ctx, scope) == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	return ErrUnavailable
}

func claudeAuthLoopback(address string) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return ErrUnsupported
	}
	defer func() { _ = listener.Close() }()
	client, err := net.DialTimeout("tcp", listener.Addr().String(), 500*time.Millisecond)
	if err != nil {
		return ErrUnsupported
	}
	defer func() { _ = client.Close() }()
	if tcp, ok := listener.(*net.TCPListener); ok {
		_ = tcp.SetDeadline(time.Now().Add(500 * time.Millisecond))
	}
	accepted, err := listener.Accept()
	if err != nil {
		return ErrUnsupported
	}
	defer func() { _ = accepted.Close() }()
	_ = client.SetDeadline(time.Now().Add(500 * time.Millisecond))
	_ = accepted.SetDeadline(time.Now().Add(500 * time.Millisecond))
	if _, err := client.Write([]byte{1}); err != nil {
		return ErrUnsupported
	}
	var body [1]byte
	if _, err := io.ReadFull(accepted, body[:]); err != nil || body[0] != 1 {
		return ErrUnsupported
	}
	return nil
}
func checkClaudeAuthConfinement(scope logins.ClaudeAuthScope) error {
	if scope.Worker.PID != os.Getpid() || !scope.Worker.Alive() {
		return ErrStale
	}
	mountNS, err := os.Readlink("/proc/self/ns/mnt")
	if err != nil || mountNS == scope.HostMountNS {
		return ErrUnsupported
	}
	userNS, err := os.Readlink("/proc/self/ns/user")
	if err != nil || userNS == scope.HostUserNS {
		return ErrUnsupported
	}
	group, err := claudeAuthGroup(os.Getpid())
	if err != nil || group != scope.Cgroup {
		return ErrStale
	}
	// Mount IDs differ in a private mount namespace; exact kernel group identity
	// still must match the observed host device/inode.
	device, inode, err := claudeAuthGroupIdentity("/sys/fs/cgroup" + group)
	if err != nil || device != scope.Device || inode != scope.Inode {
		return ErrStale
	}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return ErrUnavailable
	}
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry.Name())
		if err != nil {
			return ErrInvalid
		}
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			if err != unix.EBADF {
				return ErrUnavailable
			}
		} else if st.Mode&unix.S_IFMT == unix.S_IFSOCK {
			return ErrUnsafe
		}
	}
	if fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0); err == nil {
		_ = unix.Close(fd)
		return ErrUnsupported
	} else if err != unix.EAFNOSUPPORT && err != unix.EPERM && err != unix.EACCES {
		return ErrUnsupported
	}
	for _, address := range []string{"127.0.0.1:0", "[::1]:0"} {
		if err := claudeAuthLoopback(address); err != nil {
			return err
		}
	}

	mountInfo, mountErr := os.ReadFile("/proc/self/mountinfo")
	if mountErr != nil {
		return ErrUnavailable
	}
	readonly := false
	for _, line := range strings.Split(string(mountInfo), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 8 && fields[4] == "/sys/fs/cgroup" && strings.Contains(line, " - cgroup2 ") {
			for _, flag := range strings.Split(fields[5], ",") {
				if flag == "ro" {
					readonly = true
				}
			}
		}
	}
	if !readonly {
		return ErrUnsupported
	}
	fd, err := unix.Open("/sys/fs/cgroup"+group+"/cgroup.procs", unix.O_WRONLY|unix.O_CLOEXEC, 0)
	if err == nil {
		_ = unix.Close(fd)
		return ErrUnsupported
	}
	if err != unix.EROFS && err != unix.EACCES && err != unix.EPERM {
		return ErrUnsupported
	}
	raw, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return ErrUnavailable
	}
	status := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok {
			status[k] = strings.TrimSpace(v)
		}
	}
	if status["NoNewPrivs"] != "1" || status["Seccomp"] != "2" || status["CapEff"] != "0000000000000000" {
		return ErrUnsupported
	}
	// RestrictNamespaces and @mount are verified by the caller's own pinned
	// executable in a separate inert internal mode; never fork Go unsafely.
	for _, operation := range []string{"mount", "user", "remount"} {
		executable, err := os.Executable()
		if err != nil {
			return ErrUnavailable
		}
		cmd := exec.Command(executable, "internal", "login-claude-check", operation)
		cmd.Env = []string{"PATH=/usr/bin:/bin"}
		cmd.Stdout = nil
		cmd.Stderr = nil
		err = cmd.Run()
		if err != nil {
			exit, ok := err.(*exec.ExitError)
			if !ok {
				return ErrUnsupported
			}
			status, ok := exit.Sys().(syscall.WaitStatus)
			if !ok || !status.Signaled() || status.Signal() != syscall.SIGSYS {
				return ErrUnsupported
			}
		}
	}
	return nil
}

// RunClaudeAuthConfinementCheck performs an inert syscall in a separate owned
// process. Parent already verified private namespaces; it writes no state.
func RunClaudeAuthConfinementCheck(operation string) error {
	raw, readErr := os.ReadFile("/proc/self/status")
	if readErr != nil || !strings.Contains(string(raw), "NoNewPrivs:\t1\n") || !strings.Contains(string(raw), "Seccomp:\t2\n") {
		return ErrUnsupported
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "CapEff:") && strings.TrimLeft(strings.TrimSpace(strings.TrimPrefix(line, "CapEff:")), "0") != "" {
			return ErrUnsupported
		}
	}
	var err error
	switch operation {
	case "mount":
		err = unix.Unshare(unix.CLONE_NEWNS)
	case "user":
		err = unix.Unshare(unix.CLONE_NEWUSER)
	case "remount":
		err = unix.Mount("", "/sys/fs/cgroup", "", unix.MS_REMOUNT|unix.MS_BIND, "")
	default:
		return ErrInvalid
	}
	if err != unix.EPERM && err != unix.EACCES {
		return ErrUnsupported
	}
	return nil
}

func containClaudeAuthScope(scope logins.ClaudeAuthScope, keepPID int) error {
	if scope.Worker.PID != os.Getpid() || !scope.Worker.Alive() {
		return ErrStale
	}
	group, err := claudeAuthGroup(os.Getpid())
	if err != nil || group != scope.Cgroup {
		return ErrStale
	}
	path := "/sys/fs/cgroup" + group
	device, inode, err := claudeAuthGroupIdentity(path)
	if err != nil || device != scope.Device || inode != scope.Inode {
		return ErrStale
	}
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(path)
		if err != nil {
			return ErrUnavailable
		}
		for _, entry := range entries {
			if entry.IsDir() {
				return ErrUnavailable
			}
		}
		raw, err := os.ReadFile(filepath.Join(path, "cgroup.procs"))
		if err != nil || len(raw) > 64<<10 {
			return ErrUnavailable
		}
		others := false
		seenWorker := false
		for _, value := range strings.Fields(string(raw)) {
			pid, err := strconv.Atoi(value)
			if err != nil {
				return ErrInvalid
			}
			if pid == os.Getpid() {
				seenWorker = true
				continue
			}
			others = true
			start, err := procstart.StartTime(pid)
			if err != nil {
				continue
			}
			actual, err := claudeAuthGroup(pid)
			if err != nil || actual != group {
				return ErrStale
			}
			_ = processcontain.SignalIdentity(processcontain.Identity{PID: pid, StartTime: start}, syscall.SIGKILL)
		}
		if !processcontain.ReapAdoptedZombies(keepPID) {
			return ErrUnavailable
		}
		if seenWorker && !others {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return ErrUnavailable
}

// openClaudeAuthImage retains the actual qualified executable inode. Atomic
// installer replacement cannot redirect /proc/self/fd/3. Owner in-place mutation
// is outside this operational boundary; this is not an immutable/sealed image.
func openClaudeAuthImage(c ClaudeAuthConfig) (*os.File, error) {
	return openClaudeAuthImageForDigest(c, "b73676430af12f97316a798b8371f81d640f90a58bdcb8f3ce56a835bb47df56")
}

// Digest parameter is a private fixture seam; production has one fixed pin.
func openClaudeAuthImageForDigest(c ClaudeAuthConfig, expected string) (*os.File, error) {
	if runtime.GOARCH != "amd64" || c.NativeVersion != "2.1.310" {
		return nil, ErrUnsupported
	}
	if err := ValidateNativePath(c.NativePath); err != nil {
		return nil, err
	}
	before, err := os.Lstat(c.NativePath)
	if err != nil || before.Size() <= 0 || before.Size() > 1<<30 {
		return nil, ErrUnsafe
	}
	file, err := os.OpenFile(c.NativePath, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrUnsafe
	}
	accepted := false
	defer func() {
		if !accepted {
			_ = file.Close()
		}
	}()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, ErrStale
	}
	hash := sha256.New()
	count, err := io.Copy(hash, io.LimitReader(file, 1<<30+1))
	if err != nil || count != before.Size() || hex.EncodeToString(hash.Sum(nil)) != expected {
		return nil, ErrUnsupported
	}
	after, err := file.Stat()
	current, pathErr := os.Lstat(c.NativePath)
	if err != nil || pathErr != nil || !os.SameFile(opened, current) || !os.SameFile(opened, after) || opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) || !persist.MatchCLIFingerprint(c.NativePath, c.NativeFingerprint) {
		return nil, ErrStale
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, ErrUnavailable
	}
	accepted = true
	return file, nil
}

// Called only after direct exec.Cmd.Wait joined, so global reaping cannot steal it.
func reapClaudeAuthScope() error { return processcontain.ContainAndReap() }
