// Package voiceclient contains the app-owned transport for remote voice.
package voiceclient

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"
)

const sshSetupTimeout = 10 * time.Second

var (
	ErrSSHConfig    = errors.New("voice: invalid SSH configuration")
	ErrHostKey      = errors.New("voice: SSH host key does not match its pin")
	ErrSSHHandshake = errors.New("voice: SSH connection or public-key authentication failed")
	ErrForward      = errors.New("voice: SSH server refused the voice forward")
)

// SSHConfig describes an explicitly enrolled SSH destination. HostKeySHA256 is
// the full OpenSSH SHA256 fingerprint, including the SHA256: prefix.
type SSHConfig struct {
	Address            string
	User               string
	HostKeySHA256      string
	RemoteVoiceAddress string
}

// DialSSH creates one direct-tcpip channel to the configured remote voice
// endpoint. The context owns the entire connection lifetime. Canceling it or
// closing the returned stream closes the TCP transport and unblocks channel I/O.
// SSH channel deadlines are unsupported; callers use a cancellable context.
// DialSSH establishes transport only. The voice endpoint must authenticate its
// own device grant before admitting media or actions.
func DialSSH(ctx context.Context, cfg SSHConfig, signer ssh.Signer) (net.Conn, error) {
	if ctx == nil || signer == nil || !validSSHConfig(cfg) {
		return nil, ErrSSHConfig
	}
	setup, cancelSetup := context.WithTimeout(ctx, sshSetupTimeout)
	defer cancelSetup()
	var dialer net.Dialer
	raw, err := dialer.DialContext(setup, "tcp", cfg.Address)
	if err != nil {
		if setup.Err() != nil {
			return nil, setup.Err()
		}
		return nil, ErrSSHHandshake
	}
	closeRaw := func() { _ = raw.Close() }
	stopSetup := context.AfterFunc(setup, closeRaw)
	defer stopSetup()
	stopLifetime := context.AfterFunc(ctx, closeRaw)
	success := false
	defer func() {
		if !success {
			stopLifetime()
			closeRaw()
		}
	}()
	sshConfig := &ssh.ClientConfig{
		User: cfg.User,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			presented := ssh.FingerprintSHA256(key)
			if subtle.ConstantTimeCompare([]byte(presented), []byte(cfg.HostKeySHA256)) != 1 {
				return ErrHostKey
			}
			return nil
		},
	}
	connection, channels, requests, err := ssh.NewClientConn(raw, cfg.Address, sshConfig)
	if err != nil {
		if setup.Err() != nil {
			return nil, setup.Err()
		}
		if errors.Is(err, ErrHostKey) {
			return nil, ErrHostKey
		}
		return nil, ErrSSHHandshake
	}
	client := ssh.NewClient(connection, channels, requests)
	stream, err := client.DialContext(setup, "tcp", cfg.RemoteVoiceAddress)
	if err != nil {
		if setup.Err() != nil {
			return nil, setup.Err()
		}
		return nil, ErrForward
	}
	if setup.Err() != nil {
		return nil, setup.Err()
	}
	stopSetup()
	success = true
	return &sshStream{Conn: stream, raw: raw, stop: stopLifetime}, nil
}

func validSSHConfig(cfg SSHConfig) bool {
	if !validSSHField(cfg.User, 256) || !validSSHField(cfg.Address, 512) {
		return false
	}
	host, port, err := net.SplitHostPort(cfg.Address)
	if err != nil || host == "" {
		return false
	}
	if p, err := strconv.ParseUint(port, 10, 16); err != nil || p == 0 {
		return false
	}
	pin, prefixed := strings.CutPrefix(cfg.HostKeySHA256, "SHA256:")
	if !prefixed || len(pin) != base64.RawStdEncoding.EncodedLen(32) {
		return false
	}
	raw, err := base64.RawStdEncoding.DecodeString(pin)
	if err != nil || len(raw) != 32 || base64.RawStdEncoding.EncodeToString(raw) != pin {
		return false
	}
	destination, err := netip.ParseAddrPort(cfg.RemoteVoiceAddress)
	if err != nil || destination.Port() == 0 || destination.String() != cfg.RemoteVoiceAddress {
		return false
	}
	return destination.Addr() == netip.AddrFrom4([4]byte{127, 0, 0, 1}) || destination.Addr() == netip.IPv6Loopback()
}

func validSSHField(value string, max int) bool {
	return value != "" && len(value) <= max && utf8.ValidString(value) &&
		strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) < 0
}

type sshStream struct {
	net.Conn
	raw  net.Conn
	stop func() bool
	once sync.Once
	err  error
}

func (s *sshStream) Close() error {
	s.once.Do(func() {
		s.stop()
		if err := s.raw.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			s.err = err
		}
	})
	return s.err
}
