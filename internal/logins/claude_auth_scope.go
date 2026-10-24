package logins

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/Nathandela/swarm/internal/processcontain"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const claudeAuthScopeSchemaVersion = 9
const claudeAuthScopeStopped = "scope-tree"

// ClaudeAuthScope is an immutable observed kernel/unit binding, not a claim
// that effective confinement checks have passed. The pinned worker checks those
// itself before Claim. All fields are comparable for exact durable CAS checks.
type ClaudeAuthScope struct {
	HostMountNS  string                  `json:"host_mount_namespace"`
	HostUserNS   string                  `json:"host_user_namespace"`
	Profile      string                  `json:"profile"`
	Unit         string                  `json:"unit"`
	BootID       string                  `json:"boot_id"`
	BusID        string                  `json:"bus_id"`
	ManagerOwner string                  `json:"manager_owner"`
	Manager      processcontain.Identity `json:"manager"`
	MountID      uint64                  `json:"mount_id"`
	Device       uint64                  `json:"device"`
	Inode        uint64                  `json:"inode"`
	Cgroup       string                  `json:"cgroup"`
	InvocationID string                  `json:"invocation_id"`
	Worker       processcontain.Identity `json:"worker"`
}

func validClaudeAuthScope(ref ClaudeAuthRef, scope ClaudeAuthScope) bool {
	if !claudeAuthNamespace(scope.HostMountNS, "mnt") || !claudeAuthNamespace(scope.HostUserNS, "user") {
		return false
	}
	if scope.Profile != "systemd249-user-v1" || scope.Unit != "swarm-claude-auth-"+ref.Nonce+".service" || !validClaudeAuthHex(scope.BootID, 36, true) || !validClaudeAuthHex(scope.BusID, 32, false) || !validClaudeAuthHex(scope.InvocationID, 32, false) {
		return false
	}
	if !claudeAuthManagerOwner(scope.ManagerOwner) || scope.Manager.PID <= 0 || scope.Manager.StartTime <= 0 || scope.Worker.PID <= 0 || scope.Worker.StartTime <= 0 || scope.MountID == 0 || scope.Device == 0 || scope.Inode == 0 {
		return false
	}
	return filepath.IsAbs(scope.Cgroup) && filepath.Clean(scope.Cgroup) == scope.Cgroup && len(scope.Cgroup) <= 4096 && utf8.ValidString(scope.Cgroup) && !strings.ContainsFunc(scope.Cgroup, unicode.IsControl) && strings.HasSuffix(scope.Cgroup, "/"+scope.Unit)
}
func claudeAuthNamespace(value, kind string) bool {
	prefix := kind + ":["
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, "]") || len(value) > 64 {
		return false
	}
	digits := strings.TrimSuffix(strings.TrimPrefix(value, prefix), "]")
	return claudeAuthDecimal(digits, false)
}
func claudeAuthDecimal(value string, zero bool) bool {
	if value == "" || len(value) > 20 || len(value) > 1 && value[0] == '0' {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}
	n, err := strconv.ParseUint(value, 10, 64)
	return err == nil && (zero || n > 0)
}
func claudeAuthManagerOwner(value string) bool {
	if !strings.HasPrefix(value, ":") {
		return false
	}
	parts := strings.Split(value[1:], ".")
	return len(parts) == 2 && claudeAuthDecimal(parts[0], false) && claudeAuthDecimal(parts[1], true)
}
func validClaudeAuthHex(s string, n int, dashes bool) bool {
	if len(s) != n {
		return false
	}
	for i, c := range s {
		if dashes && (i == 8 || i == 13 || i == 18 || i == 23) {
			if c != '-' {
				return false
			}
			continue
		}
		if c < '0' || c > '9' {
			if c < 'a' || c > 'f' {
				return false
			}
		}
	}
	return true
}

// BindClaudeAuthScope is called only by the launcher after independently observing
// this exact unit, kernel group and live worker. It never authorizes native work.
func (s *Store) BindClaudeAuthScope(ref ClaudeAuthRef, scope ClaudeAuthScope) error {
	if !validClaudeAuthScope(ref, scope) || !scope.Worker.Alive() {
		return ErrIneligible
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.locked(func() error {
		r, err := s.load()
		if err != nil {
			return err
		}
		intent, err := s.readClaudeAuthIntent(ref.LoginID)
		if err != nil {
			return err
		}
		if intent.Ref != ref || intent.Phase != "prepared" || claudeAuthTarget(r, ref) != nil {
			return ErrConflict
		}
		if intent.Scope != (ClaudeAuthScope{}) {
			if intent.Scope == scope {
				return nil
			}
			return ErrConflict
		}
		if r.SchemaVersion < claudeAuthScopeSchemaVersion {
			if r.Revision == ^uint64(0) {
				return ErrPending
			}
			r.SchemaVersion = claudeAuthScopeSchemaVersion
			r.Revision++
			if err := s.save(r); err != nil {
				return err
			}
		}
		intent.Version = 2
		intent.Scope = scope
		return s.writeClaudeAuthIntent(intent)
	})
}

// RecordClaudeAuthScopeContained is the exact live worker's assertion after
// qualified positive own-scope containment/reaping. Native publication may have
// failed or raced exit; Native==0 is not evidence of no effects.
func (s *Store) RecordClaudeAuthScopeContained(ref ClaudeAuthRef, attempt int, scope ClaudeAuthScope) error {
	return s.recordClaudeAuthScopeStopped(ref, attempt, scope, false)
}

// RecordClaudeAuthScopeRetired is only for the outside qualified verifier after
// observing the exact retained invocation retired. PID death alone is insufficient.
func (s *Store) RecordClaudeAuthScopeRetired(ref ClaudeAuthRef, attempt int, scope ClaudeAuthScope) error {
	return s.recordClaudeAuthScopeStopped(ref, attempt, scope, true)
}
func (s *Store) recordClaudeAuthScopeStopped(ref ClaudeAuthRef, attempt int, scope ClaudeAuthScope, retired bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.locked(func() error {
		intent, err := s.readClaudeAuthIntent(ref.LoginID)
		if err != nil {
			return err
		}
		if intent.Ref != ref || intent.Attempt != attempt || intent.Phase != "running" || !validClaudeAuthScope(ref, scope) || intent.Scope != scope || intent.Worker != scope.Worker || intent.Native.Alive() {
			return ErrConflict
		}
		if retired {
			if intent.Worker.Alive() {
				return ErrInUse
			}
			intent.Phase = "failed"
			intent.UnitRetired = true
		} else if intent.Worker.PID != os.Getpid() || !intent.Worker.Alive() {
			return ErrInUse
		}
		intent.StopProof = claudeAuthScopeStopped
		return s.writeClaudeAuthIntent(intent)
	})
}

// ClaudeAuthWorkerConfig is immutable launch input, never progress authority.
type ClaudeAuthWorkerConfig struct {
	StateRoot         string        `json:"state_root"`
	Ref               ClaudeAuthRef `json:"ref"`
	NativePath        string        `json:"native_path"`
	NativeVersion     string        `json:"native_version"`
	NativeFingerprint string        `json:"native_fingerprint"`
}

// PrepareClaudeAuthWorker writes only inside the existing intent's exact nonce,
// under its registry lock, so prepared revocation cannot race a late mkdir/write.
func (s *Store) PrepareClaudeAuthWorker(ref ClaudeAuthRef, path, version, fingerprint string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || version != "2.1.310" || fingerprint == "" || len(fingerprint) > 256 {
		return "", ErrIneligible
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var result string
	err := s.locked(func() error {
		intent, err := s.readClaudeAuthIntent(ref.LoginID)
		if err != nil {
			return err
		}
		if intent.Ref != ref || intent.Phase != "prepared" || intent.Scope != (ClaudeAuthScope{}) {
			return ErrConflict
		}
		r, err := s.load()
		if err != nil {
			return err
		}
		if claudeAuthTarget(r, ref) != nil {
			return ErrConflict
		}
		if r.SchemaVersion < claudeAuthScopeSchemaVersion {
			if r.Revision == ^uint64(0) {
				return ErrPending
			}
			r.SchemaVersion = claudeAuthScopeSchemaVersion
			r.Revision++
			if err := s.save(r); err != nil {
				return err
			}
		}
		base := filepath.Join(claudeAuthPayload(ref.LoginID), ref.Nonce)
		for _, dir := range []string{claudeAuthPayload(ref.LoginID), base} {
			if err := mkdirPrivate(s.root, dir, s.writeOps); err != nil {
				return err
			}
		}
		name := filepath.Join(base, "worker.json")
		raw, err := json.Marshal(ClaudeAuthWorkerConfig{s.StateRoot(), ref, path, version, fingerprint})
		if err != nil || len(raw) > 8192 {
			return ErrCapacity
		}
		if previous, err := readPrivate(s.root, name, 8192); err == nil {
			if !bytes.Equal(previous, raw) {
				return ErrConflict
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		} else {
			if _, err := durableReplace(s.root, name, raw, s.writeOps); err != nil {
				if errors.Is(err, ErrDurabilityUncertain) {
					s.uncertain = true
				}
				return err
			}
		}
		result = filepath.Join(s.path, name)
		return nil
	})
	return result, err
}

// ObserveClaudeAuthProfile is read-only polling of a fresh native profile. It
// does not publish authentication or require unchanged generation: a renewal
// may trigger the second fresh attempt only after containment and Finish.
func (s *Store) ObserveClaudeAuthProfile(ref ClaudeAuthRef, attempt int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.locked(func() error {
		r, err := s.load()
		if err != nil {
			return err
		}
		intent, err := s.readClaudeAuthIntent(ref.LoginID)
		if err != nil {
			return err
		}
		if intent.Ref != ref || intent.Attempt != attempt || intent.Phase != "running" || intent.Worker.PID != os.Getpid() || !intent.Worker.Alive() || claudeAuthTarget(r, ref) != nil || intent.Selection != r.Providers[ProviderClaude] {
			return ErrConflict
		}
		now := time.Now()
		if !now.Before(intent.Deadline) {
			return ErrPending
		}
		_, grant, err := s.claudeAuthGeneration(ref.LoginID)
		if err != nil {
			return err
		}
		profile, err := readPrivate(s.root, filepath.Join("claude-auth", ref.LoginID, ref.Nonce, strconv.Itoa(attempt), "config", ".claude.json"), maxCredentialBytes)
		if err != nil {
			return err
		}
		_, err = validateClaudeAuthReceipt(profile, grant, ref.Identity, intent.StartedAt, now)
		return err
	})
}

// FailClaudeAuthAttempt rejects malformed/unfinished native protocol only after
// positive containment. It cannot release a live or unpublished native writer.
func (s *Store) FailClaudeAuthAttempt(ref ClaudeAuthRef, attempt int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.locked(func() error {
		intent, err := s.readClaudeAuthIntent(ref.LoginID)
		if err != nil {
			return err
		}
		if intent.Ref != ref || intent.Attempt != attempt || intent.Phase != "running" || intent.Worker.PID != os.Getpid() || !intent.Worker.Alive() {
			return ErrConflict
		}
		if err := s.proveClaudeAuthStopped(intent); err != nil {
			return err
		}
		intent.Phase = "failed"
		return s.writeClaudeAuthIntent(intent)
	})
}

// CheckClaudeAuthScratch applies the existing bounded nonce-payload walker while
// native is live. Quotas are monitored; per-file OS limits bound one write, not
// total-volume overshoot. It never scans the shared secure authority.
func (s *Store) CheckClaudeAuthScratch(ref ClaudeAuthRef, attempt int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	intent, err := s.readClaudeAuthIntent(ref.LoginID)
	if err != nil {
		return err
	}
	if intent.Ref != ref || intent.Attempt != attempt || intent.Phase != "running" || intent.Worker.PID != os.Getpid() {
		return ErrConflict
	}
	return s.checkClaudeAuthScratch(intent)
}
func (s *Store) checkClaudeAuthScratch(intent claudeAuthIntent) error {
	root, err := openPrivateDir(s.root, claudeAuthPayload(intent.Ref.LoginID))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	count, bytes := 0, int64(0)
	return boundClaudeAuthPayload(root, intent.Ref.Nonce, 0, &count, &bytes)
}

// RevokeClaudeAuthPrepared durably closes native admission but retains the exact
// unit binding and payload until the outside observer resolves process custody.
func (s *Store) RevokeClaudeAuthPrepared(ref ClaudeAuthRef) (ClaudeAuthResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result ClaudeAuthResult
	err := s.locked(func() error {
		intent, err := s.readClaudeAuthIntent(ref.LoginID)
		if err != nil {
			return err
		}
		if intent.Ref != ref {
			return ErrConflict
		}
		if intent.Phase == "retiring" && intent.StopProof == claudeAuthUnadmitted {
			result = claudeAuthResult(intent)
			return nil
		}
		if intent.Phase != "prepared" {
			return ErrInUse
		}
		intent.Phase = "retiring"
		intent.StopProof = claudeAuthUnadmitted
		if err := s.writeClaudeAuthIntent(intent); err != nil {
			return err
		}
		result = claudeAuthResult(intent)
		return nil
	})
	return result, err
}

// RecordClaudeAuthUnitRetired is an outside qualified observer assertion. It
// resolves unit custody only after native admission closed or native containment
// was durably recorded; reset/stop/unload must follow this durable receipt.
func (s *Store) RecordClaudeAuthUnitRetired(ref ClaudeAuthRef, scope ClaudeAuthScope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.locked(func() error {
		intent, err := s.readClaudeAuthIntent(ref.LoginID)
		if err != nil {
			return err
		}
		if intent.Ref != ref || intent.Scope != scope || !validClaudeAuthScope(ref, scope) || scope.Worker.Alive() || intent.Phase == "prepared" || intent.Phase == "running" {
			return ErrConflict
		}
		if err := s.proveClaudeAuthStopped(intent); err != nil {
			return err
		}
		intent.UnitRetired = true
		return s.writeClaudeAuthIntent(intent)
	})
}
