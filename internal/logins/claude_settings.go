package logins

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/Nathandela/swarm/internal/processcontain"
)

const claudeSettingsPath = "provider/claude/native/settings.json"

// Called only under the registry fence; existing preferences are never replaced.
func (s *Store) ensureClaudeSettings(r Registry) error {
	if _, err := s.root.Lstat(claudeSettingsPath); err == nil {
		return s.settleClaudeSettings()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := s.claudeMutationScopeAllowed(r, nil, true); err != nil {
		return err
	}
	homes := []string{s.Home(ProviderClaude)}
	for identity := range r.ClaudeProfiles {
		home, err := s.ClaudeProfileHome(identity)
		if err != nil {
			return err
		}
		homes = append(homes, home)
	}
	slices.Sort(homes)
	scan := s.grantWriters
	if scan == nil {
		scan = processcontain.EnvironmentWriters
	}
	if writers, err := scan(homes, nil); err != nil || len(writers) != 0 {
		return ErrInUse
	}
	if _, err := durableCreate(s.root, claudeSettingsPath, []byte("{}\n"), s.writeOps); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return s.settleClaudeSettings()
}

// Adopt only one anchored private inode, including a concurrent creator's winner.
func (s *Store) settleClaudeSettings() (err error) {
	defer func() {
		if errors.Is(err, ErrDurabilityUncertain) {
			s.uncertain = true
		}
	}()
	before, err := checkRelative(s.root, claudeSettingsPath, false)
	if err != nil {
		return err
	}
	parent, err := openPrivateDir(s.root, filepath.Dir(claudeSettingsPath))
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	f, err := parent.OpenFile(filepath.Base(claudeSettingsPath), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return ErrUnsafePath
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !privateInfo(info, false) || !os.SameFile(before, info) || info.Size() > maxCredentialBytes {
		return ErrUnsafePath
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxCredentialBytes+1))
	if err != nil || len(raw) > maxCredentialBytes {
		return ErrUnsafePath
	}
	if err := ValidateAuthSettings(ProviderClaude, raw); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return ErrDurabilityUncertain
	}
	current, err := checkRelative(s.root, claudeSettingsPath, false)
	if err != nil || !os.SameFile(info, current) {
		return ErrUnsafePath
	}
	syncDir := s.writeOps.syncDir
	if syncDir == nil {
		syncDir = func(string) error { return syncRootDir(parent, ".") }
	}
	if err := syncDir(filepath.Dir(claudeSettingsPath)); err != nil {
		return ErrDurabilityUncertain
	}
	return nil
}
