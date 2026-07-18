package detect

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Nathandela/swarm/internal/adapter"
)

// maxConfigSize caps every config read. A CLI config is kilobytes; the cap keeps
// a pathological or corrupt file from being slurped whole. A file over the cap is
// read truncated (best-effort): a truncated JSON simply fails to parse and yields
// empty, never an error that blocks detection.
const maxConfigSize = 1 << 20 // 1 MiB

// ProbeModels discovers, best-effort, the model the named CLI is configured to
// default to and the model choices it exposes, by reading the CLI's on-disk
// config. name is the registry adapter name ("codex" / "claude"). Any missing or
// corrupt file yields empty results, never an error: model discovery piggybacks
// the async, generation-stamped detection and must never block or fail it.
func ProbeModels(name string) (configured string, models []adapter.ModelChoice) {
	switch name {
	case "codex":
		// $CODEX_HOME overrides the default ~/.codex, mirroring the CLI itself.
		home := os.Getenv("CODEX_HOME")
		if home == "" {
			home = filepath.Join(os.Getenv("HOME"), ".codex")
		}
		if data, ok := readCapped(filepath.Join(home, "config.toml")); ok {
			configured = tomlTopLevelString(data, "model")
		}
		if data, ok := readCapped(filepath.Join(home, "models_cache.json")); ok {
			models = parseCodexModelsCache(data)
		}
	case "claude", "claude-code":
		// Claude Code exposes no model enumeration; only the configured default
		// is discoverable (user settings.json). The registry name is
		// "claude-code"; the short form is accepted for symmetry.
		if data, ok := readCapped(filepath.Join(os.Getenv("HOME"), ".claude", "settings.json")); ok {
			configured = claudeModelFromJSON(data)
		}
	}
	return configured, models
}

// readCapped reads path up to maxConfigSize bytes. ok is false when the file is
// absent, unreadable, or not a regular file (a FIFO or device planted at a config
// path would block the detection goroutine forever - stat-and-reject before open);
// a file over the cap is returned truncated to the cap.
func readCapped(path string) (data []byte, ok bool) {
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return nil, false // Stat follows symlinks: a linked regular config passes, a FIFO/device target is rejected
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	data, err = io.ReadAll(io.LimitReader(f, maxConfigSize))
	if err != nil {
		return nil, false
	}
	return data, true
}

// tomlTopLevelString returns the value of a bare top-level TOML string key (one
// appearing before the first [table] header), or "" when absent. It is a minimal
// hand parser for the one key the codex config needs — no dependency.
func tomlTopLevelString(data []byte, key string) string {
	for _, line := range strings.Split(string(data), "\n") {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "[") {
			// The first table header ends the top-level section; a key below it
			// (e.g. [profiles.x] model = ...) is NOT the top-level key.
			return ""
		}
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		rest, found := strings.CutPrefix(s, key)
		if !found {
			continue
		}
		rest = strings.TrimSpace(rest)
		rest, found = strings.CutPrefix(rest, "=")
		if !found {
			continue // a longer key sharing the prefix (e.g. "modelx")
		}
		rest = strings.TrimSpace(rest)
		if len(rest) >= 2 {
			if q := rest[0]; q == '"' || q == '\'' {
				if i := strings.IndexByte(rest[1:], q); i >= 0 {
					return rest[1 : 1+i]
				}
			}
		}
		return "" // key present but not a quoted string
	}
	return ""
}

// parseCodexModelsCache parses codex's models_cache.json into the choices the
// launch form cycles: models with visibility=="list" only, sorted ascending by
// priority, ID=slug, Display=display_name. Corrupt/empty input yields nil.
func parseCodexModelsCache(data []byte) []adapter.ModelChoice {
	var cache struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Visibility  string `json:"visibility"`
			Priority    int    `json:"priority"`
		} `json:"models"`
	}
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil
	}
	type entry struct {
		choice adapter.ModelChoice
		pri    int
	}
	var listed []entry
	for _, m := range cache.Models {
		if m.Visibility == "list" && m.Slug != "" {
			listed = append(listed, entry{adapter.ModelChoice{ID: m.Slug, Display: m.DisplayName}, m.Priority})
		}
	}
	if len(listed) == 0 {
		return nil
	}
	sort.SliceStable(listed, func(i, j int) bool { return listed[i].pri < listed[j].pri })
	out := make([]adapter.ModelChoice, len(listed))
	for i, e := range listed {
		out[i] = e.choice
	}
	return out
}

// claudeModelFromJSON returns the top-level "model" string from claude's
// settings.json, or "" when absent or the JSON is corrupt.
func claudeModelFromJSON(data []byte) string {
	var s struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return ""
	}
	return s.Model
}
