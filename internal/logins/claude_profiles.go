package logins

import (
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"unicode"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/processcontain"
)

const (
	ClaudeNativeProfileMode    = "claude-native-profile-v1"
	MaxClaudeProfiles          = MaxHandoverGrantRefs
	claudeProfileOwner         = ".swarm-profile-owner"
	claudeProfileSchemaVersion = 7
	maxClaudeProfileEntries    = 32
)

// ClaudeProfile retains the stable identity-derived aliases independently of a
// saved grant's lifetime. Its private native config may contain MCP secrets.
type ClaudeProfile struct {
	Provisioned bool     `json:"provisioned,omitempty"`
	CWDs        []string `json:"cwds,omitempty"`
}

var claudeProfileAssets = []string{"projects", "settings.json", "CLAUDE.md", "commands", "skills", "agents", "rules", "output-styles", "plugins"}

func validClaudeCWDs(cwds []string) bool {
	if len(cwds) > MaxHandoverMembers {
		return false
	}
	size := 0
	for i, cwd := range cwds {
		if !utf8.ValidString(cwd) || len(cwd) > 4096 || !filepath.IsAbs(cwd) || filepath.Clean(cwd) != cwd || (i > 0 && cwds[i-1] >= cwd) {
			return false
		}
		for _, r := range cwd {
			if unicode.IsControl(r) {
				return false
			}
		}
		size += len(cwd)
	}
	return size <= maxHandoverBytes
}

func validClaudeProfiles(r Registry) bool {
	if len(r.ClaudeProfiles) > MaxClaudeProfiles || (len(r.ClaudeProfiles) > 0 && r.SchemaVersion < claudeProfileSchemaVersion) {
		return false
	}
	for identity, p := range r.ClaudeProfiles {
		if !validDigest(identity) || !validClaudeCWDs(p.CWDs) {
			return false
		}
	}
	return true
}

func reserveClaudeProfileSpace(r Registry, identity string, cwds []string) error {
	r.ClaudeProfiles = maps.Clone(r.ClaudeProfiles)
	if r.ClaudeProfiles == nil {
		r.ClaudeProfiles = map[string]ClaudeProfile{}
	}
	r.ClaudeProfiles[identity] = ClaudeProfile{Provisioned: true, CWDs: cwds}
	r.Revision += 2
	if r.SchemaVersion < claudeProfileSchemaVersion {
		r.SchemaVersion = claudeProfileSchemaVersion
	}
	if err := reserveHandoverSpace(r); err != nil {
		return err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(raw) > maxRegistryBytes {
		return ErrCapacity
	}
	return nil
}

// ClaudeProfileHome is an address, not proof of provisioning or native custody.
func (s *Store) ClaudeProfileHome(identity string) (string, error) {
	if !validDigest(identity) {
		return "", ErrIneligible
	}
	return filepath.Join(s.path, "provider", "claude", "profiles", identity), nil
}

// ProvisionClaudeProfile prepares an unborn native profile. Runtime selection is
// deliberately separate: this foundation does not enable managed profile births.
// New local scopes on established profiles require positive writer exclusion.
func (s *Store) ProvisionClaudeProfile(expected uint64, loginID string, cwds []string) (Registry, error) {
	if len(cwds) > MaxHandoverMembers {
		return Registry{}, ErrCapacity
	}
	cwds = slices.Clone(cwds)
	slices.Sort(cwds)
	cwds = slices.Compact(cwds)
	if !validClaudeCWDs(cwds) {
		return Registry{}, ErrIneligible
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var r Registry
	err := s.locked(func() error {
		var err error
		r, err = s.load()
		if err != nil {
			return err
		}
		if r.Revision != expected {
			return ErrConflict
		}
		if mode := r.Providers[ProviderClaude].AuthOwnerMode; r.MigrationPending || mode != "" && (mode != ClaudeNativeProfileMode || r.SchemaVersion < ClaudeProfileCustodySchemaVersion) {
			return ErrPending
		}
		if _, ok := r.Switches[ProviderClaude]; ok {
			return ErrPending
		}
		l := find(&r, loginID)
		if l == nil || l.Provider != ProviderClaude {
			return ErrIneligible
		}
		identity := l.Identity
		home, _ := s.ClaudeProfileHome(identity)
		dir := filepath.Join("provider", "claude", "profiles", identity)
		record, known := r.ClaudeProfiles[identity]
		if known && record.Provisioned {
			if err := s.verifyClaudeAliases(dir); err != nil {
				return err
			}
			all := slices.Concat(record.CWDs, cwds)
			slices.Sort(all)
			all = slices.Compact(all)
			if slices.Equal(all, record.CWDs) {
				return nil
			}
			if !validClaudeCWDs(all) {
				return ErrCapacity
			}
			if r.Revision >= ^uint64(0)-1 {
				return ErrIneligible
			}
			if err := reserveClaudeProfileSpace(r, identity, all); err != nil {
				return err
			}
			if err := s.claudeAuthorityMutationAllowed(r, *l); err != nil {
				return err
			}
			scan := s.grantWriters
			if scan == nil {
				scan = processcontain.EnvironmentWriters
			}
			writers, err := scan([]string{home}, nil)
			if err != nil || len(writers) != 0 {
				return ErrInUse
			}
			definitions, err := s.claudeMCPDefinitions(all)
			if err != nil {
				return err
			}
			if err := s.writeClaudeMCPDefinitions(dir, definitions); err != nil {
				return err
			}
			// Also settle a visible prior config rename before publishing its CWD.
			syncDir := s.writeOps.syncDir
			if syncDir == nil {
				syncDir = func(dir string) error { return syncRootDir(s.root, dir) }
			}
			if err := syncDir(dir); err != nil {
				return ErrDurabilityUncertain
			}
			r.ClaudeProfiles[identity] = ClaudeProfile{Provisioned: true, CWDs: all}
			r.Revision++
			return s.save(r)
		}
		if known && !slices.Equal(record.CWDs, cwds) {
			return ErrPending
		}
		if !known && len(r.ClaudeProfiles) >= MaxClaudeProfiles {
			return ErrCapacity
		}
		if r.Revision >= ^uint64(0)-1 {
			return ErrIneligible
		}
		if err = reserveClaudeProfileSpace(r, identity, cwds); err != nil {
			return err
		}
		if _, err = checkRelative(s.root, "provider/claude/native", true); err != nil {
			return err
		}
		if err = s.validateClaudeAliasSources(); err != nil {
			return err
		}
		// Read only explicit definition maps; native account/trust/OAuth fields stay put.
		definitions, err := s.claudeMCPDefinitions(cwds)
		if err != nil {
			return err
		}
		if _, err = checkRelative(s.root, "provider/claude/profiles", true); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		_, pathErr := s.root.Lstat(dir)
		if !known {
			if pathErr == nil {
				return ErrConflict
			}
			if !errors.Is(pathErr, os.ErrNotExist) {
				return pathErr
			}
			if r.ClaudeProfiles == nil {
				r.ClaudeProfiles = map[string]ClaudeProfile{}
			}
			r.ClaudeProfiles[identity] = ClaudeProfile{CWDs: cwds}
			if r.SchemaVersion < claudeProfileSchemaVersion {
				r.SchemaVersion = claudeProfileSchemaVersion
			}
			r.Revision++
			if err = s.save(r); err != nil {
				return err
			}
		} else if pathErr == nil {
			// A crashed provisioner cannot infer writer absence from its pending row.
			scan := s.grantWriters
			if scan == nil {
				scan = processcontain.EnvironmentWriters
			}
			writers, scanErr := scan([]string{home}, nil)
			if scanErr != nil || len(writers) != 0 {
				return ErrInUse
			}
			owner, ownerErr := readPrivate(s.root, filepath.Join(dir, claudeProfileOwner), 128)
			if ownerErr != nil || string(owner) != identity {
				return ErrPending
			}
			if err = s.checkClaudePendingContents(dir); err != nil {
				return err
			}
		} else if !errors.Is(pathErr, os.ErrNotExist) {
			return pathErr
		}
		if err = mkdirPrivate(s.root, "provider/claude/profiles", s.writeOps); err != nil {
			return err
		}
		if errors.Is(pathErr, os.ErrNotExist) {
			if err = s.root.Mkdir(dir, 0700); err != nil {
				return err
			}
			if err = syncRootDir(s.root, filepath.Dir(dir)); err != nil {
				return ErrDurabilityUncertain
			}
			if _, err = durableReplace(s.root, filepath.Join(dir, claudeProfileOwner), []byte(identity), s.writeOps); err != nil {
				return err
			}
		} else if _, err = checkRelative(s.root, dir, true); err != nil {
			return err
		}
		if _, err = s.root.Lstat("provider/claude/native/projects"); errors.Is(err, os.ErrNotExist) {
			if err = mkdirPrivate(s.root, "provider/claude/native/projects", s.writeOps); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if err = s.provisionClaudeAliases(dir); err != nil {
			return err
		}
		if err = s.writeClaudeMCPDefinitions(dir, definitions); err != nil {
			return err
		}
		record = ClaudeProfile{Provisioned: true, CWDs: cwds}
		r.ClaudeProfiles[identity] = record
		r.Revision++
		return s.save(r)
	})
	if errors.Is(err, ErrDurabilityUncertain) {
		s.uncertain = true
	}
	return r, err
}

func (s *Store) validateClaudeAliasSources() error {
	for _, name := range claudeProfileAssets {
		info, err := s.root.Lstat(filepath.Join("provider/claude/native", name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		directory := name != "settings.json" && name != "CLAUDE.md"
		if err != nil || !nativeOwned(info, directory) {
			return ErrUnsafePath
		}
	}
	return nil
}

func (s *Store) verifyClaudeAliases(dir string) error {
	if _, err := checkRelative(s.root, dir, true); err != nil {
		return err
	}
	owner, err := readPrivate(s.root, filepath.Join(dir, claudeProfileOwner), 128)
	if err != nil || string(owner) != filepath.Base(dir) {
		return ErrUnsafePath
	}
	if err := s.validateClaudeAliasSources(); err != nil {
		return err
	}
	parent, err := openPrivateDir(s.root, dir)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	for _, name := range claudeProfileAssets {
		info, err := parent.Lstat(name)
		if err != nil || !nativeLinkOwned(info) {
			return ErrUnsafePath
		}
		target, err := parent.Readlink(name)
		if err != nil || target != filepath.Join(s.Home(ProviderClaude), name) {
			return ErrUnsafePath
		}
	}
	return nil
}

func (s *Store) provisionClaudeAliases(dir string) error {
	parent, err := openPrivateDir(s.root, dir)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	for _, name := range claudeProfileAssets {
		target := filepath.Join(s.Home(ProviderClaude), name)
		info, err := parent.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			if err = parent.Symlink(target, name); err != nil {
				return err
			}
		} else {
			if err != nil || !nativeLinkOwned(info) {
				return ErrUnsafePath
			}
			actual, err := parent.Readlink(name)
			if err != nil || actual != target {
				return ErrUnsafePath
			}
		}
	}
	if err = syncRootDir(parent, "."); err != nil {
		return ErrDurabilityUncertain
	}
	return s.verifyClaudeAliases(dir)
}

func (s *Store) checkClaudePendingContents(dir string) error {
	parent, err := openPrivateDir(s.root, dir)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	f, err := parent.Open(".")
	if err != nil {
		return err
	}
	entries, err := f.ReadDir(maxClaudeProfileEntries + 1)
	_ = f.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(entries) > maxClaudeProfileEntries {
		return ErrCapacity
	}
	for _, entry := range entries {
		name := entry.Name()
		if slices.Contains(claudeProfileAssets, name) {
			continue
		}
		if name != ".claude.json" && name != claudeProfileOwner && !validStoreTemporary(name) {
			return ErrUnsafePath
		}
		if _, err = readPrivate(parent, name, maxCredentialBytes); err != nil {
			return err
		}
	}
	return nil
}

func jsonObject(raw []byte) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if validateJSON(raw) != nil || json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, ErrIneligible
	}
	return object, nil
}

func explicitMCPMap(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	m, err := jsonObject(raw)
	if err != nil || len(m) > MaxHandoverMembers {
		return nil, ErrIneligible
	}
	for name, definition := range m {
		if !boundedNativeID(name, false) {
			return nil, ErrIneligible
		}
		if _, err = jsonObject(definition); err != nil {
			return nil, err
		}
	}
	b, err := json.Marshal(m)
	return b, err
}

func (s *Store) claudeMCPDefinitions(cwds []string) (map[string]json.RawMessage, error) {
	raw, err := s.readOptional("provider/claude/native/.claude.json")
	if err != nil {
		return nil, err
	}
	source := map[string]json.RawMessage{}
	if len(raw) > 0 {
		source, err = jsonObject(raw)
		if err != nil {
			return nil, err
		}
	}
	out := map[string]json.RawMessage{}
	user, err := explicitMCPMap(source["mcpServers"])
	if err != nil {
		return nil, err
	}
	if len(user) > 0 {
		out["mcpServers"] = user
	}
	if len(source["projects"]) > 0 {
		projects, err := jsonObject(source["projects"])
		if err != nil {
			return nil, err
		}
		selected := map[string]json.RawMessage{}
		for _, cwd := range cwds {
			if len(projects[cwd]) == 0 {
				continue
			}
			project, err := jsonObject(projects[cwd])
			if err != nil {
				return nil, err
			}
			local, err := explicitMCPMap(project["mcpServers"])
			if err != nil {
				return nil, err
			}
			if len(local) > 0 {
				selected[cwd], err = json.Marshal(map[string]json.RawMessage{"mcpServers": local})
				if err != nil {
					return nil, err
				}
			}
		}
		if len(selected) > 0 {
			out["projects"], err = json.Marshal(selected)
			if err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func mergeClaudeMCPMap(destination map[string]json.RawMessage, key string, source json.RawMessage) error {
	if len(source) == 0 {
		return nil
	}
	if old := destination[key]; len(old) > 0 {
		var a, b any
		if json.Unmarshal(old, &a) != nil || json.Unmarshal(source, &b) != nil {
			return ErrIneligible
		}
		if !reflect.DeepEqual(a, b) {
			return ErrConflict
		}
		return nil
	}
	destination[key] = source
	return nil
}

func (s *Store) writeClaudeMCPDefinitions(dir string, definitions map[string]json.RawMessage) error {
	name := filepath.Join(dir, ".claude.json")
	raw, err := s.readOptional(name)
	if err != nil {
		return err
	}
	destination := map[string]json.RawMessage{}
	if len(raw) > 0 {
		destination, err = jsonObject(raw)
		if err != nil {
			return err
		}
	}
	if err = mergeClaudeMCPMap(destination, "mcpServers", definitions["mcpServers"]); err != nil {
		return err
	}
	if source := definitions["projects"]; len(source) > 0 {
		incoming, _ := jsonObject(source)
		projects := map[string]json.RawMessage{}
		if old := destination["projects"]; len(old) > 0 {
			projects, err = jsonObject(old)
			if err != nil {
				return err
			}
		}
		for cwd, raw := range incoming {
			scope := map[string]json.RawMessage{}
			if old := projects[cwd]; len(old) > 0 {
				scope, err = jsonObject(old)
				if err != nil {
					return err
				}
			}
			fields, _ := jsonObject(raw)
			if err = mergeClaudeMCPMap(scope, "mcpServers", fields["mcpServers"]); err != nil {
				return err
			}
			projects[cwd], err = json.Marshal(scope)
			if err != nil {
				return err
			}
		}
		destination["projects"], err = json.Marshal(projects)
		if err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(destination)
	if err != nil {
		return err
	}
	if len(encoded) > maxCredentialBytes {
		return ErrCapacity
	}
	var before, after any
	if len(raw) > 0 && json.Unmarshal(raw, &before) == nil && json.Unmarshal(encoded, &after) == nil && reflect.DeepEqual(before, after) {
		return nil
	}
	_, err = durableReplace(s.root, name, encoded, s.writeOps)
	return err
}

func claudeProfilesOwned(r Registry, provider string) bool {
	return provider == ProviderClaude && r.Providers[provider].AuthOwnerMode == ClaudeNativeProfileMode
}

func validAuthOwnerMode(r Registry, provider, mode string) bool {
	if mode == "" {
		return true
	}
	if provider == ProviderCodex {
		return mode == CodexNativeGrantMode && r.SchemaVersion >= 2
	}
	return provider == ProviderClaude && mode == ClaudeNativeProfileMode && r.SchemaVersion >= claudeProfileSchemaVersion
}
