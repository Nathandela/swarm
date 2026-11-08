package daemon

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"syscall"
)

// SessionClaudeInputEnrollment distinguishes an opted-in session with lost proof
// from an ordinary session, using the private launch record that survives restart.
func (d *Daemon) SessionClaudeInputEnrollment(id string) (bool, error) {
	m, ok := d.Get(id)
	if !ok || m.AgentType != "claude" {
		return false, errors.New("claude input enrollment: unknown session")
	}
	root, err := os.OpenRoot(d.sessionDir(id))
	if err != nil {
		return false, err
	}
	defer func() { _ = root.Close() }()
	f, err := root.OpenFile(shimLaunchConfigFile, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return false, errors.New("claude input enrollment: launch custody unavailable")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 256<<10 {
		return false, errors.New("claude input enrollment: unsafe launch custody")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Getuid()) || st.Nlink != 1 {
		return false, errors.New("claude input enrollment: unsafe launch custody")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (256<<10)+1))
	if err != nil || len(raw) > 256<<10 {
		return false, errors.New("claude input enrollment: unreadable launch custody")
	}
	var lc shimSpawnConfig
	if json.Unmarshal(raw, &lc) != nil || lc.SessionID != id || lc.SessionDir != d.sessionDir(id) || lc.Cwd != m.ProviderCwd() {
		return false, errors.New("claude input enrollment: launch custody mismatch")
	}
	current, ok := d.Get(id)
	if !ok || current.ShimPID != m.ShimPID || current.ShimStartTime != m.ShimStartTime {
		return false, errors.New("claude input enrollment: session changed")
	}
	return lc.ClaudeInput, nil
}
