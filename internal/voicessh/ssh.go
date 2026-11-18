// Package voicessh uses the desktop's installed OpenSSH for the scoped voice device route.
package voicessh

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const setupTimeout = 12 * time.Second

var (
	ErrInvalidAlias = errors.New("voice: use an OpenSSH host alias containing only letters, numbers, dots, hyphens or underscores")
	ErrSSH          = errors.New("voice: SSH did not reach the selected host; check its alias, known hosts, agent and remote Swarm service")
	ErrForward      = errors.New("voice: SSH device forwarding failed; check the selected host's forwarding policy and voice service")
	ErrHostConfig   = errors.New("voice: the selected SSH route cannot enforce known-host checks")
)

func ValidAlias(alias string) bool {
	if len(alias) == 0 || len(alias) > 255 || !asciiAlnum(alias[0]) {
		return false
	}
	for i := range len(alias) {
		c := alias[i]
		if !asciiAlnum(c) && c != '.' && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func asciiAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func validEndpoint(address string) bool {
	ip, err := netip.ParseAddrPort(address)
	return err == nil && ip.String() == address && ip.Port() > 0 &&
		(ip.Addr() == netip.AddrFrom4([4]byte{127, 0, 0, 1}) || ip.Addr() == netip.IPv6Loopback())
}

type connector struct {
	binary string
	config string // test fixtures can supply an isolated configuration
	alias  string
}

func (s connector) command(ctx context.Context, args ...string) *exec.Cmd {
	if s.config != "" {
		args = append([]string{"-F", s.config}, args...)
	}
	cmd := exec.CommandContext(ctx, s.binary, args...)
	cmd.Env = append(os.Environ(), "SSH_ASKPASS_REQUIRE=never")
	// No password/host confirmation prompt and no orphaned ProxyJump subprocesses.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		} else {
			return err
		}
	}
	cmd.WaitDelay = time.Second
	return cmd
}

func sshOptions() []string {
	return []string{"-T", "-S", "none",
		"-o", "ControlMaster=no", "-o", "ForkAfterAuthentication=no", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes",
		"-o", "VerifyHostKeyDNS=no", "-o", "NoHostAuthenticationForLocalhost=no", "-o", "UpdateHostKeys=no", "-o", "ForwardAgent=no",
		"-o", "ForwardX11=no", "-o", "PermitLocalCommand=no", "-o", "RemoteCommand=none", "-o", "EscapeChar=none",
		"-o", "ConnectTimeout=8", "-o", "ConnectionAttempts=1", "-o", "ExitOnForwardFailure=yes", "-o", "ClearAllForwardings=yes"}
}

func (s connector) capture(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, setupTimeout)
	defer cancel()
	cmd := s.command(ctx, args...)
	out := &boundedOutput{}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil || out.overflow {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrSSH
	}
	return out.Bytes(), nil
}

type boundedOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	const limit = 64 << 10
	if len(p) > limit-b.Len() {
		b.overflow = true
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

// identity detects an alias retarget before the client sends an existing grant.
func (s connector) identity(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, setupTimeout)
	defer cancel()
	fingerprints := sha256.New()
	seen := map[string]bool{}
	var inspect func(sshHop, bool) error
	inspect = func(hop sshHop, primary bool) error {
		if len(seen) >= 8 || seen[hop.String()] {
			return ErrHostConfig
		}
		seen[hop.String()] = true
		args := []string{"-G"}
		if primary {
			args = append(args, sshOptions()...)
		}
		if hop.user != "" {
			args = append(args, "-l", hop.user)
		}
		if hop.port != "" {
			args = append(args, "-p", hop.port)
		}
		raw, err := s.capture(ctx, append(args, "--", hop.host)...)
		if err != nil {
			return err
		}
		settings := map[string]string{}
		for line := range strings.SplitSeq(string(raw), "\n") {
			if key, value, ok := strings.Cut(line, " "); ok {
				settings[key] = value
			}
		}
		for _, field := range []string{"hostname", "user", "port", "hostkeyalias", "proxyjump"} {
			_, _ = fmt.Fprintln(fingerprints, settings[field])
		}
		if settings["hostname"] == "" || settings["user"] == "" || settings["port"] == "" ||
			(specified(settings["proxycommand"]) || specified(settings["knownhostscommand"])) {
			return ErrHostConfig
		}
		if !primary && (!strictHostKeys(settings["stricthostkeychecking"]) ||
			(settings["verifyhostkeydns"] != "no" && settings["verifyhostkeydns"] != "false") ||
			settings["nohostauthenticationforlocalhost"] == "yes" || settings["nohostauthenticationforproxycommand"] == "yes" ||
			specified(settings["controlpath"])) {
			return ErrHostConfig
		}
		if specified(settings["proxyjump"]) {
			for raw := range strings.SplitSeq(settings["proxyjump"], ",") {
				next, err := parseHop(raw)
				if err != nil {
					return err
				}
				if err = inspect(next, false); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := inspect(sshHop{host: s.alias}, true); err != nil {
		return "", err
	}
	return hex.EncodeToString(fingerprints.Sum(nil)), nil
}

func specified(s string) bool { return s != "" && s != "none" }

func strictHostKeys(setting string) bool {
	// Default ask refuses unknown keys without a controlling TTY or askpass.
	return setting == "ask" || setting == "yes" || setting == "true"
}

type sshHop struct{ host, port, user string }

func (h sshHop) String() string { return h.user + "@" + h.host + ":" + h.port }

func parseHop(raw string) (sshHop, error) {
	raw, _ = strings.CutPrefix(raw, "ssh://")
	h := sshHop{host: raw}
	if name, host, ok := strings.Cut(raw, "@"); ok {
		if !ValidAlias(name) {
			return sshHop{}, ErrHostConfig
		}
		h.user, h.host = name, host
	}
	if strings.Contains(h.host, ":") {
		if host, port, err := net.SplitHostPort(h.host); err == nil {
			h.host, h.port = host, port
			if p, err := strconv.ParseUint(h.port, 10, 16); err != nil || p == 0 {
				return sshHop{}, ErrHostConfig
			}
		} else if strings.HasPrefix(h.host, "[") && strings.HasSuffix(h.host, "]") {
			h.host = h.host[1 : len(h.host)-1]
		}
	}
	if !ValidAlias(h.host) {
		if ip, err := netip.ParseAddr(h.host); err != nil || !ip.Is6() || ip.Zone() != "" {
			return sshHop{}, ErrHostConfig
		}
	}
	return h, nil
}

func (s connector) remote(ctx context.Context, command ...string) ([]byte, error) {
	args := append(sshOptions(), "-n", "--", s.alias, "swarm", "voice")
	return s.capture(ctx, append(args, command...)...)
}

func (s connector) forward(ctx context.Context, endpoint string) (net.Conn, error) {
	if !validEndpoint(endpoint) || ctx.Err() != nil {
		return nil, ErrForward
	}
	// A socket pair lets SSH own stdio directly while HTTP keeps real deadlines.
	syscall.ForkLock.RLock()
	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		syscall.ForkLock.RUnlock()
		return nil, ErrForward
	}
	syscall.CloseOnExec(pair[0])
	syscall.CloseOnExec(pair[1])
	syscall.ForkLock.RUnlock()
	local := os.NewFile(uintptr(pair[0]), "voice-ssh-client")
	peer := os.NewFile(uintptr(pair[1]), "voice-ssh-stdio")
	defer func() { _ = local.Close(); _ = peer.Close() }()
	conn, err := net.FileConn(local)
	if err != nil {
		return nil, ErrForward
	}
	ctx, cancel := context.WithCancel(ctx)
	cmd := s.command(ctx, append(sshOptions(), "-W", endpoint, "--", s.alias)...)
	cmd.Stdin, cmd.Stdout = peer, peer
	if err = cmd.Start(); err != nil {
		cancel()
		_ = conn.Close()
		return nil, ErrForward
	}
	f := &forwardStream{Conn: conn, cancel: cancel, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		cancel()
		close(f.done)
	}()
	return f, nil
}

type forwardStream struct {
	net.Conn
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
}

func (s *forwardStream) Close() error {
	s.closeOnce.Do(func() {
		s.cancel()
		_ = s.Conn.Close()
		select {
		case <-s.done:
		case <-time.After(2 * time.Second):
			s.closeErr = ErrForward
		}
	})
	return s.closeErr
}

func (s *forwardStream) Read(p []byte) (int, error) {
	n, err := s.Conn.Read(p)
	if n == 0 && err != nil && !errors.Is(err, net.ErrClosed) {
		return n, fmt.Errorf("%w: %w", ErrForward, err)
	}
	return n, err
}
