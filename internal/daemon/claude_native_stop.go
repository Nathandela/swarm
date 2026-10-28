package daemon

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shimwire"
	"github.com/Nathandela/swarm/internal/wire"
)

var ErrClaudeNativeStopUnconfirmed = errors.New("daemon: native Claude Stop unconfirmed")

func validClaudeStopBinding(b shimwire.ClaudeStopBinding, discovery bool) bool {
	stateRoot := b.Runtime.Home
	for range 5 {
		stateRoot = filepath.Dir(stateRoot)
	}
	if b.Version != 1 || b.SchemaVersion != persist.ClaudeChatSchemaVersion || !persist.ValidID(b.SessionID) || b.RequiredRuntime != persist.RequiredRuntimeClaudeChat || !adapter.IsCanonicalConversationID(b.NativeSessionID) || b.ShimPID <= 0 || b.ShimStartTime <= 0 || b.Incarnation != fmt.Sprintf("%d:%d", b.ShimPID, b.ShimStartTime) || !logins.IsClaudeProfileRuntime(stateRoot, b.Runtime) || !filepath.IsAbs(b.Cwd) || filepath.Clean(b.Cwd) != b.Cwd || len(b.Cwd) > 4096 || strings.ContainsAny(b.Cwd, "\x00\r\n") || !filepath.IsAbs(b.CLIIdentity.Path) || filepath.Clean(b.CLIIdentity.Path) != b.CLIIdentity.Path || strings.ContainsAny(b.CLIIdentity.Path, "\x00\r\n") || b.CLIIdentity.Version != "2.1.310" || !logins.IsClaudeCLIFingerprint(b.CLIIdentity.Fingerprint) {
		return false
	}
	if discovery && b.ScopeGeneration == "" {
		return true
	}
	generation, err := hex.DecodeString(b.ScopeGeneration)
	return err == nil && len(generation) == 16 && hex.EncodeToString(generation) == b.ScopeGeneration
}

func claudeStopSocket(path string) (os.FileInfo, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrClaudeNativeStopUnconfirmed
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return nil, ErrClaudeNativeStopUnconfirmed
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return nil, ErrClaudeNativeStopUnconfirmed
	}
	owner, ok := parent.Sys().(*syscall.Stat_t)
	if !ok || !parent.IsDir() || parent.Mode().Perm()&0022 != 0 || owner.Uid != uint32(os.Getuid()) {
		return nil, ErrClaudeNativeStopUnconfirmed
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, ErrClaudeNativeStopUnconfirmed
	}
	owner, ok = info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0600 || owner.Uid != uint32(os.Getuid()) || owner.Nlink != 1 {
		return nil, ErrClaudeNativeStopUnconfirmed
	}
	return info, nil
}

// Internal peers encode one canonical typed reply. Refuse duplicate/alias fields
// rather than allowing the tolerant general shimwire decoder to choose authority.
func readClaudeStopControl(c net.Conn) (shimwire.Control, error) {
	typ, raw, err := wire.ReadFrame(c)
	if err != nil || typ != wire.TControl {
		return shimwire.Control{}, ErrClaudeNativeStopUnconfirmed
	}
	reply, err := shimwire.Decode(raw)
	encoded, encodeErr := shimwire.Encode(reply)
	if err != nil || encodeErr != nil || !bytes.Equal(raw, encoded) {
		return shimwire.Control{}, ErrClaudeNativeStopUnconfirmed
	}
	return reply, nil
}

func dialClaudeNativeStop(socket string, expected shimwire.ClaudeStopBinding, discovery bool, deadline time.Time) (net.Conn, shimwire.ClaudeStopBinding, os.FileInfo, error) {
	fail := func() (net.Conn, shimwire.ClaudeStopBinding, os.FileInfo, error) {
		return nil, shimwire.ClaudeStopBinding{}, nil, ErrClaudeNativeStopUnconfirmed
	}
	if !validClaudeStopBinding(expected, discovery) {
		return fail()
	}
	info, err := claudeStopSocket(socket)
	if err != nil {
		return fail()
	}
	c, err := net.DialTimeout("unix", socket, time.Until(deadline))
	if err != nil {
		return fail()
	}
	success := false
	defer func() {
		if !success {
			_ = c.Close()
		}
	}()
	if c.SetDeadline(deadline) != nil || !claudeNativeStopPeer(c, expected) {
		return fail()
	}
	raw, _ := shimwire.Encode(shimwire.Control{Type: shimwire.TypeHello, WireVersion: shimwire.Version, ClaudeStopV1: true})
	if wire.WriteFrame(c, wire.TControl, raw) != nil {
		return fail()
	}
	hello, err := readClaudeStopControl(c)
	if err != nil || hello.Type != shimwire.TypeHello || hello.WireVersion != shimwire.Version || !hello.ClaudeStopV1 || hello.ClaudeStopBinding == nil || hello.ClaudePrimary != nil || !validClaudeStopBinding(*hello.ClaudeStopBinding, false) {
		return fail()
	}
	binding := *hello.ClaudeStopBinding
	match := binding
	if discovery && expected.ScopeGeneration == "" {
		match.ScopeGeneration = ""
	}
	if match != expected {
		return fail()
	}
	current, err := claudeStopSocket(socket)
	if err != nil || !os.SameFile(info, current) || !claudeNativeStopPeer(c, expected) {
		return fail()
	}
	success = true
	return c, binding, info, nil
}

// ReadClaudeNativeStopBinding discovers only the scope generation. All other
// reservation facts and the kernel shim birth must already be known exactly.
func ReadClaudeNativeStopBinding(socket string, expected shimwire.ClaudeStopBinding) (shimwire.ClaudeStopBinding, error) {
	deadline := time.Now().Add(3 * time.Second)
	c, binding, _, err := dialClaudeNativeStop(socket, expected, true, deadline)
	if c != nil {
		_ = c.Close()
	}
	return binding, err
}

// StopClaudeNative contains the retained scope without any daemon, consumer or
// Store lock. Acknowledged delivery is separate from positive stopped proof.
func StopClaudeNative(socket string, expected shimwire.ClaudeStopBinding) error {
	deadline := time.Now().Add(3 * time.Second)
	c, binding, info, err := dialClaudeNativeStop(socket, expected, false, deadline)
	if err != nil {
		return ErrClaudeNativeStopUnconfirmed
	}
	defer func() { _ = c.Close() }()
	current, err := claudeStopSocket(socket)
	if err != nil || !os.SameFile(info, current) || !claudeNativeStopPeer(c, expected) {
		return ErrClaudeNativeStopUnconfirmed
	}
	raw, _ := shimwire.Encode(shimwire.Control{Type: shimwire.TypeClaudeStop, ClaudeStop: &shimwire.ClaudeStopRequest{Version: 1, Binding: binding}})
	if wire.WriteFrame(c, wire.TControl, raw) != nil {
		return ErrClaudeNativeStopUnconfirmed
	}
	reply, err := readClaudeStopControl(c)
	if err != nil || reply.Type != shimwire.TypeClaudeStopResult || reply.ClaudeStopResult == nil || reply.ClaudeStopResult.Version != 1 || reply.ClaudeStopResult.Binding != binding || reply.ClaudeStopResult.Status != "delivered" {
		return ErrClaudeNativeStopUnconfirmed
	}
	return nil
}
