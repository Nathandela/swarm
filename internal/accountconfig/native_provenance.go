package accountconfig

import (
	"encoding/hex"
	"path/filepath"
)

// Intrinsic provenance must enumerate every characterized native project
// layer, including absent files. Revalidation of a truncated list is unsafe.
func validNativeProjectProvenance(m manifest) error {
	var paths []string
	var sources []source
	if m.Provider == "codex" {
		if m.ProjectBoundary == nil {
			return Conflict("projection-corrupt")
		}
		for dir := m.Cwd; ; dir = filepath.Dir(dir) {
			for _, name := range []string{"config.toml", "hooks.json"} {
				paths = append(paths, filepath.Join(dir, ".codex", name))
			}
			if dir == m.ProjectBoundary.Root {
				break
			}
			if filepath.Dir(dir) == dir {
				return Conflict("projection-corrupt")
			}
		}
		if m.ProjectBoundary.MainRoot != "" {
			paths = append(paths, filepath.Join(m.ProjectBoundary.MainRoot, ".codex", "hooks.json"))
		}
		sources = m.CodexProjectSources
	} else {
		for dir := m.Cwd; ; dir = filepath.Dir(dir) {
			if m.ProjectBoundary == nil || dir != m.ProjectBoundary.Home {
				for _, name := range []string{"settings.json", "settings.local.json"} {
					paths = append(paths, filepath.Join(dir, ".claude", name))
				}
			}
			if filepath.Dir(dir) == dir || m.ProjectBoundary != nil && dir == m.ProjectBoundary.Home {
				break
			}
		}
		sources = m.ClaudeProjectSources
	}
	if len(paths) != len(sources) || len(paths) > 1024 {
		return Conflict("projection-corrupt")
	}
	for i, path := range paths {
		if sources[i].Path != path || !validNativeSourceDigest(sources[i].SHA256, true) {
			return Conflict("projection-corrupt")
		}
	}
	seen := map[string]bool{}
	if len(m.ClaudeInvocationSources) > 1024 {
		return Conflict("projection-corrupt")
	}
	for _, s := range m.ClaudeInvocationSources {
		if !cleanAbsolute(s.Path) || seen[s.Path] || !validNativeSourceDigest(s.SHA256, false) {
			return Conflict("projection-corrupt")
		}
		seen[s.Path] = true
	}
	return nil
}

func validNativeSourceDigest(value string, absent bool) bool {
	if absent && value == "absent" {
		return true
	}
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 32 && hex.EncodeToString(raw) == value
}
