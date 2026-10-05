package accountconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
)

// Ordinary configuration is owner-controlled rather than secret storage. This
// admission does not apply to credentials or the account profile destination.
func claudeOwnedInfo(info os.FileInfo, dir bool) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid()) && info.Mode().Perm()&0002 == 0 && ((dir && info.IsDir()) || (!dir && info.Mode().IsRegular() && stat.Nlink == 1))
}
func readClaudeOwnedSource(path string) ([]byte, error) {
	if err := safePath(path, false); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !claudeOwnedInfo(info, false) {
		return nil, Conflict("unsafe-owner-configuration")
	}
	return readBoundedRegular(path, maxSourceBytes, 0002)
}
func snapshotClaudeSourceAlias(path string) (sourceProfileAlias, error) {
	alias := sourceProfileAlias{Path: path}
	if !cleanAbsolute(path) {
		return alias, Conflict("unsafe-source-profile")
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		alias.Canonical = path
		alias.Absent = true
		return alias, nil
	} else if err != nil {
		return alias, Conflict("unsafe-source-profile")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || safePath(canonical, true) != nil {
		return alias, Conflict("unsafe-source-profile")
	}
	info, err := os.Lstat(canonical)
	if err != nil || !claudeOwnedInfo(info, true) {
		return alias, Conflict("unsafe-source-profile")
	}
	alias.Canonical = canonical
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(path, current), current) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return alias, Conflict("unsafe-source-profile")
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Getuid()) {
			return alias, Conflict("unsafe-source-profile")
		}
		target, err := os.Readlink(current)
		if err != nil {
			return alias, Conflict("unsafe-source-profile")
		}
		alias.Links = append(alias.Links, sourceProfileLink{Path: current, Target: target, Device: uint64(stat.Dev), Inode: stat.Ino})
	}
	return alias, nil
}
func claudeSourceDigest(path string) (string, error) {
	raw, err := readClaudeOwnedSource(path)
	if errors.Is(err, os.ErrNotExist) {
		return "absent", nil
	}
	if err != nil {
		return "", Conflict("unsafe-owner-configuration")
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Preserve owner-native asset directory behavior, including native plugin cache
// updates. Freeze custody of the root rather than recursively snapshotting a
// potentially enormous mutable cache. Ordinary standalone CLAUDE.md is frozen.
func claudeAssetDigest(path string) (string, error) {
	if err := safePath(path, false); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", Conflict("unsafe-native-customization")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "absent", nil
	}
	if err != nil || !claudeOwnedInfo(info, info.IsDir()) {
		return "", Conflict("unsafe-native-customization")
	}
	if !info.IsDir() {
		return claudeSourceDigest(path)
	}
	stat := info.Sys().(*syscall.Stat_t)
	return claudeHash(struct {
		Path          string
		Device, Inode uint64
		Owner         uint32
	}{path, uint64(stat.Dev), stat.Ino, stat.Uid}), nil
}

// ValidateClaudeProjectSettings retains native project precedence while holding
// sources capable of selecting another account/provider. Returned provenance
// must be revalidated immediately before native spawn.
func ValidateClaudeProjectSettings(cwd string, env []string) ([]source, error) {
	boundary, err := discoverProjectBoundary("claude", cwd, env)
	if err != nil {
		return nil, err
	}
	var sources []source
	for dir := cwd; ; dir = filepath.Dir(dir) {
		if boundary == nil || dir != boundary.Home {
			for _, name := range []string{"settings.json", "settings.local.json"} {
				path := filepath.Join(dir, ".claude", name)
				raw, err := readClaudeOwnedSource(path)
				digest := "absent"
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					return nil, Conflict("unsafe-project-settings")
				}
				if err == nil {
					var settings map[string]any
					if json.Unmarshal(raw, &settings) != nil {
						return nil, Conflict("invalid-claude-settings")
					}
					if err := validateClaudeContextSettings(settings); err != nil {
						return nil, err
					}
					digest = claudeHashBytes(raw)
				}
				sources = append(sources, source{Path: path, SHA256: digest})
			}
		}
		if filepath.Dir(dir) == dir || boundary != nil && dir == boundary.Home {
			break
		}
	}
	return sources, nil
}
func RevalidateClaudeProjectSettings(sources []source) error {
	for _, expected := range sources {
		actual, err := claudeSourceDigest(expected.Path)
		if err != nil || actual != expected.SHA256 {
			return Conflict("configuration-source-changed")
		}
	}
	return nil
}
func validateClaudeSourceAlias(alias sourceProfileAlias) error {
	actual, err := snapshotClaudeSourceAlias(alias.Path)
	if err != nil || !reflect.DeepEqual(alias, actual) {
		return Conflict("source-profile-alias-changed")
	}
	return nil
}

func validateClaudeProjectSources(sources []source) error {
	return RevalidateClaudeProjectSettings(sources)
}

// Invocation settings retain native highest-priority semantics, but cannot
// replace the bound account's authentication route.
func ValidateClaudeInvocationSettings(argv []string) ([]source, error) {
	var sources []source
	for _, value := range flagValues(argv, "--settings") {
		raw := []byte(value)
		if !strings.HasPrefix(strings.TrimSpace(value), "{") {
			var err error
			raw, err = readClaudeOwnedSource(value)
			if err != nil {
				return nil, Conflict("unsafe-launch-settings")
			}
			sources = append(sources, source{Path: value, SHA256: claudeHashBytes(raw)})
		}
		if len(raw) > maxSourceBytes {
			return nil, Conflict("invalid-launch-settings")
		}
		var settings map[string]any
		if json.Unmarshal(raw, &settings) != nil {
			return nil, Conflict("invalid-launch-settings")
		}
		if err := validateClaudeContextSettings(settings); err != nil {
			return nil, err
		}
	}
	return sources, nil
}
