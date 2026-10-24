package logins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
)

const claudeAuthSchemaVersion = 8
const claudeAuthDeadline = 45 * time.Second
const claudeAuthTreeStopped = "native-tree"
const claudeAuthNotStarted = "not-started"
const claudeAuthUnadmitted = "unadmitted"

// ClaudeAuthRef names one bounded helper operation, not selection or adoption.
type ClaudeAuthRef struct {
	LoginID            string `json:"login_id"`
	Identity           string `json:"identity"`
	CredentialRevision uint64 `json:"credential_revision"`
	Nonce              string `json:"nonce"`
}

type ClaudeAuthAttempt struct {
	Number              int
	ConfigHome          string
	AuthorityHome       string
	StartedAt, Deadline time.Time
}

// ClaudeAuthLaunch is an unpersisted first-issuer capability, never recovery data.
type ClaudeAuthLaunch struct {
	mu      sync.Mutex
	ref     ClaudeAuthRef
	attempt int
	issuer  processcontain.Identity
	anchor  os.FileInfo
	handed  bool
	used    bool
}

func (h *ClaudeAuthLaunch) Handoff() error {
	if h == nil {
		return ErrConflict
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.used || h.handed || h.issuer.PID != os.Getpid() || !h.issuer.Alive() {
		return ErrConflict
	}
	h.handed = true
	return nil
}

func (s *Store) AbortClaudeAuthLaunch(h *ClaudeAuthLaunch, refusal error) error {
	if h == nil {
		return ErrConflict
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.used || h.issuer.PID != os.Getpid() || !h.issuer.Alive() || !os.SameFile(h.anchor, s.anchor) {
		return ErrConflict
	}
	if h.handed {
		var proof *claudeAuthLaunchRefusal
		if !errors.As(refusal, &proof) || proof.ref != h.ref || proof.attempt != h.attempt || proof.issuer != h.issuer {
			return ErrConflict
		}
	} else if refusal != nil {
		return ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.locked(func() error {
		intent, err := s.readClaudeAuthIntent(h.ref.LoginID)
		if err != nil {
			return err
		}
		if intent.Ref != h.ref || intent.Attempt != h.attempt || intent.Phase != "running" || intent.Worker != h.issuer || intent.Native != (processcontain.Identity{}) || intent.StopProof != "" {
			return ErrConflict
		}
		intent.Phase, intent.StopProof = "failed", claudeAuthNotStarted
		return s.writeClaudeAuthIntent(intent)
	})
	if err == nil {
		h.used = true
	}
	return err
}

type claudeAuthLaunchRefusal struct {
	ref     ClaudeAuthRef
	attempt int
	issuer  processcontain.Identity
	cause   error
}

func (e *claudeAuthLaunchRefusal) Error() string { return "native auth launch did not start" }
func (e *claudeAuthLaunchRefusal) Unwrap() error { return e.cause }

// ClaudeAuthLaunchNotStarted is only for the actual launcher's definite refusal,
// never a timeout, missing PID, or uncertain post-start failure.
func ClaudeAuthLaunchNotStarted(ref ClaudeAuthRef, attempt int, cause error) error {
	started, err := procstart.StartTime(os.Getpid())
	if err != nil || cause == nil || !validClaudeAuthRef(ref) || attempt < 1 || attempt > 2 {
		return ErrInUse
	}
	return &claudeAuthLaunchRefusal{ref, attempt, processcontain.Identity{PID: os.Getpid(), StartTime: started}, cause}
}

// ClaudeAuthResult reports historical authentication, never policy or adoption.
type ClaudeAuthResult struct {
	Ref         ClaudeAuthRef
	Phase       string
	Attempt     int
	Deadline    time.Time
	ExpiresAt   time.Time
	Scope       ClaudeAuthScope
	UnitRetired bool
}

type claudeAuthIntent struct {
	Scope       ClaudeAuthScope         `json:"scope,omitempty"`
	UnitRetired bool                    `json:"unit_retired,omitempty"`
	Version     int                     `json:"version"`
	Ref         ClaudeAuthRef           `json:"ref"`
	Selection   ProviderState           `json:"selection"`
	Deadline    time.Time               `json:"deadline"`
	Attempt     int                     `json:"attempt"`
	Phase       string                  `json:"phase"`
	StartedAt   time.Time               `json:"started_at,omitempty"`
	Generation  string                  `json:"generation,omitempty"`
	Worker      processcontain.Identity `json:"worker"`
	Native      processcontain.Identity `json:"native"`
	StopProof   string                  `json:"stop_proof,omitempty"`
	ExpiresAt   time.Time               `json:"expires_at,omitempty"`
}

func claudeAuthPath(id string) string    { return filepath.Join("claude-auth", id+".json") }
func claudeAuthPayload(id string) string { return filepath.Join("claude-auth", id) }

func validClaudeAuthRef(ref ClaudeAuthRef) bool {
	return validID(ref.LoginID) && validDigest(ref.Identity) && ref.CredentialRevision != 0 && validID(ref.Nonce)
}

func (s *Store) readClaudeAuthIntent(id string) (claudeAuthIntent, error) {
	var intent claudeAuthIntent
	if !validID(id) {
		return intent, ErrIneligible
	}
	raw, err := readPrivate(s.root, claudeAuthPath(id), 8192)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if _, dirErr := checkRelative(s.root, claudeAuthPayload(id), true); dirErr == nil {
				return intent, ErrPending
			} else if !errors.Is(dirErr, os.ErrNotExist) {
				return intent, dirErr
			}
		}
		return intent, err
	}
	doc, err := claudeAuthObject(raw, "version", "ref", "selection", "deadline", "attempt", "phase", "started_at", "generation", "worker", "native", "stop_proof", "expires_at", "scope", "unit_retired")
	if err != nil {
		return intent, err
	}
	for key, fields := range map[string][]string{
		"ref":       {"login_id", "identity", "credential_revision", "nonce"},
		"selection": {"desired_credential_revision", "auth_owner_mode", "provider", "active_id", "epoch"},
		"worker":    {"pid", "start_time"}, "native": {"pid", "start_time"},
	} {
		if _, err := claudeAuthObject(doc[key], fields...); err != nil {
			return intent, err
		}
	}
	if scope, ok := doc["scope"]; ok {
		fields, err := claudeAuthObject(scope, "host_mount_namespace", "host_user_namespace", "profile", "unit", "boot_id", "bus_id", "manager_owner", "manager", "mount_id", "device", "inode", "cgroup", "invocation_id", "worker")
		if err != nil {
			return intent, err
		}
		for _, name := range []string{"worker", "manager"} {
			if _, err := claudeAuthObject(fields[name], "pid", "start_time"); err != nil {
				return intent, err
			}
		}
	}
	if json.Unmarshal(raw, &intent) != nil || (intent.Version != 1 && intent.Version != 2) || !validClaudeAuthRef(intent.Ref) || intent.Ref.LoginID != id || intent.Deadline.IsZero() || intent.Attempt < 1 || intent.Attempt > 2 {
		return intent, ErrIneligible
	}
	if intent.Selection.Provider != "" && intent.Selection.Provider != ProviderClaude || intent.Selection.Provider == "" && intent.Selection != (ProviderState{}) || !validAuthOwnerMode(Registry{SchemaVersion: claudeAuthSchemaVersion}, ProviderClaude, intent.Selection.AuthOwnerMode) {
		return intent, ErrIneligible
	}
	if intent.Version == 1 && intent.Scope != (ClaudeAuthScope{}) || intent.Version == 2 && !validClaudeAuthScope(intent.Ref, intent.Scope) {
		return intent, ErrIneligible
	}
	if intent.Version == 2 {
		registry, err := s.load()
		if err != nil || registry.SchemaVersion < claudeAuthScopeSchemaVersion {
			return intent, ErrIneligible
		}
		if intent.Worker != (processcontain.Identity{}) && intent.Worker != intent.Scope.Worker {
			return intent, ErrIneligible
		}
	}
	if intent.UnitRetired && (intent.Scope == (ClaudeAuthScope{}) || intent.Phase == "prepared" || intent.Phase == "running" || intent.StopProof == "") {
		return intent, ErrIneligible
	}
	now := time.Now()
	if intent.Deadline.After(now.Add(claudeAuthDeadline+time.Second)) || intent.StartedAt.After(now.Add(time.Second)) || !claudeAuthProcessTuple(intent.Worker) || !claudeAuthProcessTuple(intent.Native) || intent.Selection.DesiredCredentialRevision != 0 || intent.Selection.ActiveID != "" && (!validID(intent.Selection.ActiveID) || intent.Selection.Epoch == 0) {
		return intent, ErrIneligible
	}
	if intent.Phase != "ready" && !intent.ExpiresAt.IsZero() {
		return intent, ErrIneligible
	}
	switch intent.Phase {
	case "prepared":
		if intent.Attempt != 1 || intent.Worker != (processcontain.Identity{}) || intent.Native != (processcontain.Identity{}) || intent.StopProof != "" || !intent.StartedAt.IsZero() || intent.Generation != "" {
			return intent, ErrIneligible
		}
	case "running", "retry", "ready", "failed", "retiring":
		initialNoStart := intent.StopProof == claudeAuthUnadmitted && intent.Worker == (processcontain.Identity{})
		if initialNoStart {
			if intent.Attempt != 1 || !intent.StartedAt.IsZero() || intent.Generation != "" {
				return intent, ErrIneligible
			}
		} else if intent.Worker.PID <= 0 || intent.StartedAt.IsZero() || !intent.StartedAt.Before(intent.Deadline) || intent.Deadline.After(intent.StartedAt.Add(claudeAuthDeadline)) || !validDigest(intent.Generation) {
			return intent, ErrIneligible
		}
		if intent.Native != (processcontain.Identity{}) && intent.Native == intent.Worker {
			return intent, ErrIneligible
		}
		if intent.StopProof == claudeAuthUnadmitted {
			if intent.Phase != "retiring" || intent.Native != (processcontain.Identity{}) || !initialNoStart {
				return intent, ErrIneligible
			}
		} else if intent.StopProof == claudeAuthNotStarted {
			if intent.Phase != "failed" && intent.Phase != "retiring" || intent.Native != (processcontain.Identity{}) {
				return intent, ErrIneligible
			}
		} else if intent.StopProof == claudeAuthScopeStopped {
			if !validClaudeAuthScope(intent.Ref, intent.Scope) || intent.Worker != intent.Scope.Worker {
				return intent, ErrIneligible
			}
		} else if intent.StopProof == claudeAuthTreeStopped {
			if intent.Native == (processcontain.Identity{}) {
				return intent, ErrIneligible
			}
		} else if intent.StopProof != "" || intent.Phase != "running" {
			return intent, ErrIneligible
		}
		if intent.Phase == "retry" && intent.Attempt != 1 || intent.Phase == "ready" && ((intent.StopProof != claudeAuthTreeStopped && intent.StopProof != claudeAuthScopeStopped) || !intent.ExpiresAt.After(intent.StartedAt.Add(5*time.Minute))) {
			return intent, ErrIneligible
		}
	default:
		return intent, ErrIneligible
	}
	return intent, nil
}

func claudeAuthObject(raw []byte, fields ...string) (map[string]json.RawMessage, error) {
	doc, err := jsonObject(raw)
	if err != nil {
		return nil, ErrIneligible
	}
	for key := range doc {
		if !slices.Contains(fields, key) {
			return nil, ErrIneligible
		}
	}
	return doc, nil
}

func claudeAuthProcessTuple(p processcontain.Identity) bool {
	return p == (processcontain.Identity{}) || p.PID > 0 && p.StartTime > 0
}

func (s *Store) writeClaudeAuthIntent(intent claudeAuthIntent) error {
	raw, err := json.Marshal(intent)
	if err == nil && intent.Scope == (ClaudeAuthScope{}) {
		var doc map[string]json.RawMessage
		if err = json.Unmarshal(raw, &doc); err == nil {
			delete(doc, "scope")
			raw, err = json.Marshal(doc)
		}
	}
	if err != nil || len(raw) > 8192 {
		return ErrCapacity
	}
	_, err = durableReplace(s.root, claudeAuthPath(intent.Ref.LoginID), raw, s.writeOps)
	if errors.Is(err, ErrDurabilityUncertain) {
		s.uncertain = true
	}
	return err
}

func (s *Store) claudeAuthMutationAllowed(id string) error {
	intent, err := s.readClaudeAuthIntent(id)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if intent.Phase != "ready" && intent.Phase != "failed" {
		return ErrInUse
	}
	return nil
}

func (s *Store) claudeAuthNamespaceAbsent(id string) error {
	_, err := s.readClaudeAuthIntent(id)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return ErrInUse
}

func (s *Store) retireClaudeAuthForRemoval(expected uint64, id string) error {
	s.mu.Lock()
	var ref ClaudeAuthRef
	err := s.locked(func() error {
		r, err := s.load()
		if err != nil {
			return err
		}
		if r.Revision != expected {
			return ErrConflict
		}
		if owned(r, id) {
			return ErrInUse
		}
		l := find(&r, id)
		if l == nil {
			return ErrIneligible
		}
		if l.Provider != ProviderClaude {
			return nil
		}
		intent, err := s.readClaudeAuthIntent(id)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		ref = intent.Ref
		return nil
	})
	s.mu.Unlock()
	if err != nil || ref.Nonce == "" {
		return err
	}
	return s.RetireClaudeAuth(ref)
}

func (s *Store) claudeAuthGeneration(id string) (string, []byte, error) {
	raw, err := s.claudeAuth(id)
	if err != nil {
		return "", nil, err
	}
	if len(raw) > maxCredentialBytes || validateJSON(raw) != nil {
		return "", nil, ErrInvalidCredentials
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), raw, nil
}

func claudeAuthTarget(r Registry, ref ClaudeAuthRef) error {
	l := find(&r, ref.LoginID)
	if l == nil || l.Provider != ProviderClaude || l.Identity != ref.Identity || l.CredentialRevision != ref.CredentialRevision {
		return ErrConflict
	}
	return nil
}

func (s *Store) prepareClaudeAuthDirectory(id string) error {
	if err := mkdirPrivate(s.root, "claude-auth", s.writeOps); err != nil {
		return err
	}
	parent, err := openPrivateDir(s.root, "claude-auth")
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	dir, err := parent.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	entries, err := dir.ReadDir(2*MaxHandoverGrantRefs + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(entries) > 2*MaxHandoverGrantRefs {
		return ErrCapacity
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() {
			if validStoreTemporary(name) {
				continue
			}
			name = strings.TrimSuffix(name, ".json")
			if name == entry.Name() {
				return ErrUnsafePath
			}
		}
		if !validID(name) {
			return ErrUnsafePath
		}
		seen[name] = true
	}
	if len(seen) > MaxHandoverGrantRefs || !seen[id] && len(seen) == MaxHandoverGrantRefs {
		return ErrCapacity
	}
	return nil
}

// BeginClaudeAuth prepares one bounded job; native launch requires a separate claim.
func (s *Store) BeginClaudeAuth(expected uint64, loginID string) (ClaudeAuthRef, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ref ClaudeAuthRef
	created := false
	err := s.locked(func() error {
		r, err := s.load()
		if err != nil {
			return err
		}
		if previous, err := s.readClaudeAuthIntent(loginID); err == nil {
			ref = previous.Ref
			return claudeAuthTarget(r, ref)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if r.Revision != expected {
			return ErrConflict
		}
		if r.MigrationPending || r.Revision == ^uint64(0) {
			return ErrPending
		}
		if _, pending := r.Switches[ProviderClaude]; pending {
			return ErrPending
		}
		l := find(&r, loginID)
		if l == nil || l.Provider != ProviderClaude || l.AuthState != AuthReady {
			return ErrIneligible
		}
		if _, err := s.readRelogin(*l); err == nil {
			return ErrPending
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if _, err := s.readSaved(*l); err != nil {
			return err
		}
		nonce, err := newID()
		if err != nil {
			return err
		}
		ref = ClaudeAuthRef{LoginID: l.ID, Identity: l.Identity, CredentialRevision: l.CredentialRevision, Nonce: nonce}
		if r.SchemaVersion < claudeAuthSchemaVersion {
			r.SchemaVersion = claudeAuthSchemaVersion
			r.Revision++
			if err := s.save(r); err != nil {
				return err
			}
		}
		if err := s.prepareClaudeAuthDirectory(loginID); err != nil {
			return err
		}
		intent := claudeAuthIntent{Version: 1, Ref: ref, Selection: r.Providers[ProviderClaude], Deadline: time.Now().UTC().Add(claudeAuthDeadline), Attempt: 1, Phase: "prepared"}
		if err := s.writeClaudeAuthIntent(intent); err != nil {
			return err
		}
		created = true
		return nil
	})
	if errors.Is(err, ErrDurabilityUncertain) {
		s.uncertain = true
	}
	return ref, created, err
}

func validateClaudeAuthReceipt(profile, grant []byte, identity string, started, ended time.Time) (time.Time, error) {
	bad := func() (time.Time, error) { return time.Time{}, ErrInvalidCredentials }
	if len(profile) > maxCredentialBytes || ended.Before(started) {
		return bad()
	}
	got, err := ClaudeIdentity(profile)
	if err != nil || got != identity {
		return bad()
	}
	doc, err := jsonObject(profile)
	if err != nil {
		return bad()
	}
	account, err := jsonObject(doc["oauthAccount"])
	if err != nil {
		return bad()
	}
	var uuid, org string
	var fetched int64
	if json.Unmarshal(account["accountUuid"], &uuid) != nil || json.Unmarshal(account["organizationUuid"], &org) != nil || json.Unmarshal(account["profileFetchedAt"], &fetched) != nil || identityDigest(ProviderClaude, uuid+"\x00"+org) != identity || fetched < started.UnixMilli() || fetched > ended.UnixMilli() {
		return bad()
	}
	return usableClaudeAuthGrant(grant, ended)
}

func usableClaudeAuthGrant(grant []byte, now time.Time) (time.Time, error) {
	bad := func() (time.Time, error) { return time.Time{}, ErrInvalidCredentials }
	if len(grant) > maxCredentialBytes {
		return bad()
	}
	doc, err := jsonObject(grant)
	if err != nil {
		return bad()
	}
	oauth, err := jsonObject(doc["claudeAiOauth"])
	if err != nil {
		return bad()
	}
	var access string
	var expiry int64
	var scopes []string
	if json.Unmarshal(oauth["accessToken"], &access) != nil || json.Unmarshal(oauth["expiresAt"], &expiry) != nil || json.Unmarshal(oauth["scopes"], &scopes) != nil || strings.TrimSpace(access) == "" {
		return bad()
	}
	inference, profileScope := false, false
	for _, scope := range scopes {
		inference = inference || scope == "user:inference"
		profileScope = profileScope || scope == "user:profile"
	}
	expires := time.UnixMilli(expiry).UTC()
	if !inference || !profileScope || !expires.After(now.Add(5*time.Minute)) {
		return bad()
	}
	return expires, nil
}

func (s *Store) claudeAuthConfig(intent claudeAuthIntent, attempt int) string {
	return filepath.Join(s.path, "claude-auth", intent.Ref.LoginID, intent.Ref.Nonce, strconv.Itoa(attempt), "config")
}

// ClaimClaudeAuthAttempt durably claims the native launch before any native effect.
func (s *Store) ClaimClaudeAuthAttempt(ref ClaudeAuthRef, worker processcontain.Identity) (ClaudeAuthAttempt, *ClaudeAuthLaunch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var attempt ClaudeAuthAttempt
	var launch *ClaudeAuthLaunch
	err := s.locked(func() (claimErr error) {
		r, err := s.load()
		if err != nil {
			return err
		}
		intent, err := s.readClaudeAuthIntent(ref.LoginID)
		if err != nil {
			return err
		}
		if intent.Ref != ref || claudeAuthTarget(r, ref) != nil || intent.Selection != r.Providers[ProviderClaude] {
			return ErrConflict
		}
		if r.MigrationPending || find(&r, ref.LoginID).AuthState != AuthReady {
			return ErrIneligible
		}
		if intent.UnitRetired || !validClaudeAuthScope(ref, intent.Scope) || intent.Scope.Worker != worker {
			return ErrInUse
		}
		if worker.PID != os.Getpid() || !worker.Alive() || !time.Now().Before(intent.Deadline) {
			return ErrInUse
		}
		if intent.Phase == "retry" && intent.Worker == worker {
			if err := s.proveClaudeAuthStopped(intent); err != nil {
				return err
			}
			intent.Attempt++
		} else if intent.Phase != "prepared" {
			return ErrInUse
		}
		generation, _, err := s.claudeAuthGeneration(ref.LoginID)
		if err != nil {
			return err
		}
		config := s.claudeAuthConfig(intent, intent.Attempt)
		if _, err := os.Lstat(config); err == nil {
			return ErrConflict
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		var created []string
		published := false
		defer func() {
			if claimErr == nil || published || errors.Is(claimErr, ErrDurabilityUncertain) {
				return
			}
			for n := len(created) - 1; n >= 0; n-- {
				if err := s.root.Remove(created[n]); err != nil && !errors.Is(err, os.ErrNotExist) {
					claimErr = errors.Join(claimErr, ErrDurabilityUncertain)
					return
				}
			}
			for _, dir := range created {
				parent := filepath.Dir(dir)
				if _, err := checkRelative(s.root, parent, true); err == nil {
					if err := syncRootDir(s.root, parent); err != nil {
						claimErr = errors.Join(claimErr, ErrDurabilityUncertain)
						return
					}
				}
			}
		}()
		base := filepath.Join(claudeAuthPayload(ref.LoginID), ref.Nonce)
		for _, dir := range []string{claudeAuthPayload(ref.LoginID), base, filepath.Join(base, strconv.Itoa(intent.Attempt)), filepath.Join(base, strconv.Itoa(intent.Attempt), "config")} {
			if _, err := s.root.Lstat(dir); errors.Is(err, os.ErrNotExist) {
				created = append(created, dir)
			} else if err != nil {
				return err
			}
			if err := mkdirPrivate(s.root, dir, s.writeOps); err != nil {
				return err
			}
		}
		if !time.Now().Before(intent.Deadline) {
			return ErrPending
		}
		intent.Generation, intent.StartedAt, intent.Worker, intent.Native, intent.Phase = generation, time.Now().UTC(), worker, processcontain.Identity{}, "running"
		intent.StopProof = ""
		if err := s.writeClaudeAuthIntent(intent); err != nil {
			return err
		}
		published = true
		attempt = ClaudeAuthAttempt{Number: intent.Attempt, ConfigHome: s.claudeAuthConfig(intent, intent.Attempt), AuthorityHome: s.savedNativeHome(ref.LoginID), StartedAt: intent.StartedAt, Deadline: intent.Deadline}
		launch = &ClaudeAuthLaunch{ref: ref, attempt: intent.Attempt, issuer: worker, anchor: s.anchor}
		return nil
	})
	if errors.Is(err, ErrDurabilityUncertain) {
		s.uncertain = true
	}
	return attempt, launch, err
}

func (s *Store) RecordClaudeAuthNative(ref ClaudeAuthRef, attempt int, native processcontain.Identity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.locked(func() error {
		intent, err := s.readClaudeAuthIntent(ref.LoginID)
		if err != nil {
			return err
		}
		if intent.Ref != ref || intent.Attempt != attempt || intent.Phase != "running" || intent.StopProof != "" || intent.Native != (processcontain.Identity{}) || intent.Worker.PID != os.Getpid() || !intent.Worker.Alive() || !native.Alive() || !time.Now().Before(intent.Deadline) {
			return ErrConflict
		}
		intent.Native = native
		return s.writeClaudeAuthIntent(intent)
	})
}

func (s *Store) proveClaudeAuthStopped(intent claudeAuthIntent) error {
	if intent.StopProof == claudeAuthScopeStopped && validClaudeAuthScope(intent.Ref, intent.Scope) && intent.Worker == intent.Scope.Worker && !intent.Native.Alive() {
		return nil
	}
	if (intent.StopProof == claudeAuthNotStarted || intent.StopProof == claudeAuthUnadmitted) && intent.Native == (processcontain.Identity{}) {
		return nil
	}
	if intent.StopProof != claudeAuthTreeStopped || intent.Native.PID <= 0 || intent.Native.StartTime <= 0 || intent.Native.Alive() {
		return ErrInUse
	}
	return nil
}

// RecordClaudeAuthContained is an assertion by the exact live dedicated worker
// after qualified positive descendant containment, not a substitute for it.
// A crashed worker cannot reconstruct this receipt from an empty inventory.
func (s *Store) RecordClaudeAuthContained(ref ClaudeAuthRef, attempt int) error {
	s.mu.Lock()
	err := s.safeAnchor()
	var intent claudeAuthIntent
	if err == nil {
		intent, err = s.readClaudeAuthIntent(ref.LoginID)
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if intent.Ref != ref || intent.Attempt != attempt || intent.Phase != "running" || intent.Worker.PID != os.Getpid() || !intent.Worker.Alive() {
		return ErrConflict
	}
	if intent.Native.PID <= 0 || intent.Native.StartTime <= 0 || intent.Native.Alive() {
		return ErrInUse
	}
	scan := s.grantWriters
	if scan == nil {
		scan = processcontain.EnvironmentWriters
	}
	homes := make([]string, 0, intent.Attempt)
	for n := 1; n <= intent.Attempt; n++ {
		homes = append(homes, s.claudeAuthConfig(intent, n))
	}
	writers, err := scan(homes, nil)
	if err != nil || len(writers) != 0 {
		return ErrInUse
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.locked(func() error {
		current, err := s.readClaudeAuthIntent(ref.LoginID)
		if err != nil {
			return err
		}
		if current != intent || !intent.Worker.Alive() || intent.Native.Alive() {
			return ErrConflict
		}
		intent.StopProof = claudeAuthTreeStopped
		return s.writeClaudeAuthIntent(intent)
	})
}

func claudeAuthResult(intent claudeAuthIntent) ClaudeAuthResult {
	return ClaudeAuthResult{Ref: intent.Ref, Phase: intent.Phase, Attempt: intent.Attempt, Deadline: intent.Deadline, ExpiresAt: intent.ExpiresAt, Scope: intent.Scope, UnitRetired: intent.UnitRetired}
}

// FinishClaudeAuthAttempt observes native output only after helper-only containment.
func (s *Store) FinishClaudeAuthAttempt(ref ClaudeAuthRef, attempt int) (ClaudeAuthResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result ClaudeAuthResult
	var receiptErr error
	err := s.locked(func() error {
		r, err := s.load()
		if err != nil {
			return err
		}
		intent, err := s.readClaudeAuthIntent(ref.LoginID)
		if err != nil {
			return err
		}
		if intent.Ref != ref || intent.Attempt != attempt {
			return ErrConflict
		}
		if intent.Phase == "ready" || intent.Phase == "failed" || intent.Phase == "retry" {
			result = claudeAuthResult(intent)
			switch intent.Phase {
			case "failed":
				receiptErr = ErrInvalidCredentials
			case "retry":
				receiptErr = ErrPending
			}
			return nil
		}
		if intent.Phase != "running" || intent.Worker.PID != os.Getpid() && intent.Worker.Alive() {
			return ErrInUse
		}
		if err := s.proveClaudeAuthStopped(intent); err != nil {
			return err
		}
		intent.Phase = "failed"
		now := time.Now().UTC()
		if intent.Scope != (ClaudeAuthScope{}) && s.checkClaudeAuthScratch(intent) != nil {
			receiptErr = ErrCapacity
		} else if claudeAuthTarget(r, ref) != nil || intent.Selection != r.Providers[ProviderClaude] {
			receiptErr = ErrConflict
		} else if !now.Before(intent.Deadline) {
			receiptErr = ErrPending
		} else {
			generation, grant, err := s.claudeAuthGeneration(ref.LoginID)
			profile, profileErr := readPrivate(s.root, filepath.Join("claude-auth", ref.LoginID, ref.Nonce, strconv.Itoa(attempt), "config", ".claude.json"), maxCredentialBytes)
			if err != nil || profileErr != nil {
				receiptErr = ErrInvalidCredentials
			} else if expires, err := validateClaudeAuthReceipt(profile, grant, ref.Identity, intent.StartedAt, now); err != nil {
				receiptErr = err
			} else if generation != intent.Generation {
				receiptErr = ErrPending
				if intent.Attempt == 1 {
					intent.Phase = "retry"
				}
			} else {
				intent.Phase, intent.ExpiresAt = "ready", expires
			}
		}
		if err := s.writeClaudeAuthIntent(intent); err != nil {
			return err
		}
		if intent.Phase == "ready" {
			generation, _, generationErr := s.claudeAuthGeneration(ref.LoginID)
			if generationErr != nil || generation != intent.Generation || !time.Now().Before(intent.Deadline) {
				intent.Phase, intent.ExpiresAt, receiptErr = "failed", time.Time{}, ErrPending
				if generationErr == nil && intent.Attempt == 1 && time.Now().Before(intent.Deadline) {
					intent.Phase = "retry"
				}
				if err := s.writeClaudeAuthIntent(intent); err != nil {
					return err
				}
			}
		}
		result = claudeAuthResult(intent)
		return nil
	})
	if err != nil {
		return result, err
	}
	return result, receiptErr
}

func (s *Store) ReadClaudeAuth(ref ClaudeAuthRef) (ClaudeAuthResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.safeAnchor(); err != nil {
		return ClaudeAuthResult{}, err
	}
	intent, err := s.readClaudeAuthIntent(ref.LoginID)
	if err != nil {
		return ClaudeAuthResult{}, err
	}
	if intent.Ref != ref {
		return ClaudeAuthResult{}, ErrConflict
	}
	return claudeAuthResult(intent), nil
}

// ValidateClaudeAuthReceipt consumes a warm receipt under the actual selection.
func (s *Store) ValidateClaudeAuthReceipt(ref ClaudeAuthRef, epoch uint64) error {
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
		selected := r.Providers[ProviderClaude]
		if r.MigrationPending {
			return ErrPending
		}
		if intent.Ref != ref || intent.Phase != "ready" || claudeAuthTarget(r, ref) != nil || selected.ActiveID != ref.LoginID || selected.Epoch != epoch || epoch == 0 {
			return ErrConflict
		}
		if find(&r, ref.LoginID).AuthState != AuthReady {
			return ErrIneligible
		}
		if _, pending := r.Switches[ProviderClaude]; pending {
			return ErrPending
		}
		generation, grant, err := s.claudeAuthGeneration(ref.LoginID)
		if err != nil || generation != intent.Generation {
			return ErrInvalidCredentials
		}
		expires, err := usableClaudeAuthGrant(grant, time.Now())
		if err != nil || !expires.Equal(intent.ExpiresAt) {
			return ErrInvalidCredentials
		}
		return nil
	})
}

// RetireClaudeAuth revokes an unadmitted job or retires positively resolved work.
// A running native claim cannot be revoked from missing PID or timeout evidence.
func (s *Store) RetireClaudeAuth(ref ClaudeAuthRef) error {
	s.mu.Lock()
	absent := false
	err := s.locked(func() error {
		intent, err := s.readClaudeAuthIntent(ref.LoginID)
		if errors.Is(err, os.ErrNotExist) {
			absent = true
			return nil
		}
		if err != nil {
			return err
		}
		if intent.Ref != ref {
			return ErrConflict
		}
		if intent.Phase == "prepared" {
			intent.StopProof = claudeAuthUnadmitted
		} else if intent.Phase != "ready" && intent.Phase != "failed" && intent.Phase != "retry" && intent.Phase != "retiring" {
			return ErrInUse
		}
		if err := s.proveClaudeAuthStopped(intent); err != nil {
			return err
		}
		intent.Phase, intent.ExpiresAt = "retiring", time.Time{}
		if err := s.writeClaudeAuthIntent(intent); err != nil {
			return err
		}
		if intent.Scope != (ClaudeAuthScope{}) && !intent.UnitRetired {
			return ErrPending
		}
		return nil
	})
	s.mu.Unlock()
	if err != nil || absent {
		return err
	}
	if err := s.cleanupClaudeAuthPayload(ref); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.locked(func() error {
		intent, err := s.readClaudeAuthIntent(ref.LoginID)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if intent.Ref != ref || intent.Phase != "retiring" {
			return ErrConflict
		}
		if err := s.proveClaudeAuthStopped(intent); err != nil {
			return err
		}
		payload := claudeAuthPayload(ref.LoginID)
		if _, err := s.root.Lstat(payload); err == nil {
			root, err := openPrivateDir(s.root, payload)
			if err != nil {
				return err
			}
			file, err := root.Open(".")
			if err != nil {
				_ = root.Close()
				return err
			}
			entries, readErr := file.ReadDir(1)
			_ = file.Close()
			_ = root.Close()
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return readErr
			}
			if len(entries) != 0 {
				return ErrPending
			}
			if err := s.root.Remove(payload); err != nil {
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := syncRootDir(s.root, "claude-auth"); err != nil {
			s.uncertain = true
			return ErrDurabilityUncertain
		}
		remove := s.root.Remove
		if s.writeOps.remove != nil {
			remove = s.writeOps.remove
		}
		if err := remove(claudeAuthPath(ref.LoginID)); err != nil {
			return err
		}
		if err := syncRootDir(s.root, "claude-auth"); err != nil {
			s.uncertain = true
			return ErrDurabilityUncertain
		}
		return nil
	})
}

func (s *Store) cleanupClaudeAuthPayload(ref ClaudeAuthRef) error {
	payload := claudeAuthPayload(ref.LoginID)
	if _, err := s.root.Lstat(payload); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	root, err := openPrivateDir(s.root, payload)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	count, bytes := 0, int64(0)
	if err := boundClaudeAuthPayload(root, ref.Nonce, 0, &count, &bytes); err != nil {
		return err
	}
	remove := func() error { return root.RemoveAll(ref.Nonce) }
	if s.writeOps.remove != nil {
		remove = func() error { return s.writeOps.remove(filepath.Join(payload, ref.Nonce)) }
	}
	if err := remove(); err != nil {
		return err
	}
	return syncRootDir(root, ".")
}

func boundClaudeAuthPayload(root *os.Root, name string, depth int, count *int, bytes *int64) error {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	*count++
	if !info.IsDir() {
		*bytes += info.Size()
	}
	if depth > 32 || *count > 4096 || *bytes > 32<<20 {
		return ErrCapacity
	}
	if !privateInfo(info, info.IsDir()) {
		return ErrUnsafePath
	}
	if !info.IsDir() {
		return nil
	}
	dir, err := openPrivateDir(root, name)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	file, err := dir.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	for {
		entries, err := file.ReadDir(128)
		for _, entry := range entries {
			if err := boundClaudeAuthPayload(dir, entry.Name(), depth+1, count, bytes); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
