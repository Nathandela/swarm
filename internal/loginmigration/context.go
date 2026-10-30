package loginmigration

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"github.com/Nathandela/swarm/internal/adapter/claude"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/persist"
)

const contextMarker = ".swarm-provider-context.json"

type contextSource struct {
	Home              string
	Conversation      string
	Cwd               string
	HistoryOnly       bool
	ParentPreferences bool
	Managed           bool
}
type contextFile struct {
	Source, Target, SHA256 string
	Bytes                  int64
	Filter                 string
	Mode                   os.FileMode
}
type contextPlan struct {
	Provider      string
	Files         []contextFile
	Conversations []contextSource
}

var uuidRE = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

func providerHome(state, provider string) string {
	return filepath.Join(state, "logins", "provider", provider, "native")
}
func sourceHome(provider string, env []string) (string, error) {
	values := map[string]string{}
	for _, entry := range env {
		k, v, ok := strings.Cut(entry, "=")
		if ok {
			values[k] = v
		}
	}
	key, dir := "CODEX_HOME", ".codex"
	if provider == "claude" {
		key, dir = "CLAUDE_CONFIG_DIR", ".claude"
	}
	path := values[key]
	if path == "" {
		home := values["HOME"]
		if home == "" {
			var e error
			home, e = os.UserHomeDir()
			if e != nil {
				return "", e
			}
		}
		path = filepath.Join(home, dir)
	}
	_, e := filepath.EvalSymlinks(path)
	if errors.Is(e, os.ErrNotExist) {
		return path, nil
	}
	return path, e
}
func contextSourceFromEnv(provider string, env []string) (contextSource, error) {
	home, err := sourceHome(provider, env)
	source := contextSource{Home: home, ParentPreferences: provider == "claude"}
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "CLAUDE_CONFIG_DIR="); ok && provider == "claude" {
			source.ParentPreferences = value == ""
		}
	}
	return source, err
}
func contextSources(state, provider string, sources []persist.Meta) ([]contextSource, error) {
	seenIDs := map[string]bool{}
	for _, meta := range sources {
		if meta.ID != "" {
			seenIDs[meta.ID] = true
		}
	}
	entries, err := os.ReadDir(state)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || seenIDs[entry.Name()] {
			continue
		}
		var meta persist.Meta
		if err = readJSON(filepath.Join(state, entry.Name(), "meta.json"), &meta); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		if meta.AgentType != provider || meta.ConversationID == "" {
			continue
		}
		if meta.SchemaVersion == 2 || meta.ID != entry.Name() {
			return nil, ErrUnsupported
		}
		sources = append(sources, meta)
	}
	out := []contextSource{}
	for _, m := range sources {
		if m.AgentType != provider {
			continue
		}
		source, e := contextSourceFromEnv(provider, m.Env)
		if e != nil {
			return nil, e
		}
		if m.LoginRuntime != nil {
			source = contextSource{Home: m.LoginRuntime.Home, Managed: true}
		}
		source.Conversation, source.Cwd = m.ConversationID, m.ProviderCwd()
		out = append(out, source)
	}
	if len(out) == 0 {
		source, e := contextSourceFromEnv(provider, os.Environ())
		if e != nil {
			return nil, e
		}
		out = append(out, source)
	}
	return out, nil
}

// PreflightProviderContext reads and validates the complete first-use context.
// The caller keeps admission fenced until PrepareProviderContext commits it.
func PreflightProviderContext(state, provider string, sources []persist.Meta) error {
	inputs, e := contextSources(state, provider, sources)
	if e != nil {
		return e
	}
	_, e = planContext(state, provider, inputs)
	return e
}

// PrepareProviderContext is called only after the provider's contained writers
// stop. External native clients must also exit before their context is copied.
func PrepareProviderContext(state, provider string, sources []persist.Meta) error {
	inputs, e := contextSources(state, provider, sources)
	if e != nil {
		return e
	}
	return prepareContext(state, provider, inputs)
}

// ContextImported reports whether the provider's managed home has completed
// its one-time native context import.
func ContextImported(state, provider string) (bool, error) {
	e := safePath(filepath.Join(providerHome(state, provider), contextMarker))
	if errors.Is(e, os.ErrNotExist) {
		return false, nil
	}
	return e == nil, e
}

func planContext(state, provider string, sources []contextSource) (contextPlan, error) {
	plan := contextPlan{Provider: provider, Conversations: sources}
	if provider != "codex" && provider != "claude" {
		return plan, ErrUnsupported
	}
	target := providerHome(state, provider)
	if _, e := os.Lstat(filepath.Join(target, contextMarker)); e == nil {
		// Native-bound discussions keep their history in the native home.
		var managed []contextSource
		for _, source := range sources {
			if source.Managed {
				managed = append(managed, source)
			}
		}
		return plan, verifyConversations(target, provider, managed)
	} else if !errors.Is(e, os.ErrNotExist) {
		return plan, e
	}
	paths := map[string]contextFile{}
	conversations := map[string]string{}
	wanted := map[string]bool{}
	for _, source := range sources {
		if source.Conversation != "" {
			wanted[source.Conversation] = true
		}
	}
	codexFiles := map[string]bool{}
	if provider == "codex" {
		var err error
		codexFiles, err = codexHistoryClosure(sources, wanted)
		if err != nil {
			return plan, err
		}
	}
	seen := map[string]bool{}
	var total int64
	for _, source := range sources {
		if !filepath.IsAbs(source.Home) || filepath.Clean(source.Home) != source.Home || source.ParentPreferences && (provider != "claude" || filepath.Base(source.Home) != ".claude") {
			return plan, ErrUnsupported
		}
		home, e := filepath.EvalSymlinks(source.Home)
		missingHome := errors.Is(e, os.ErrNotExist)
		if e != nil && !missingHome {
			return plan, e
		}
		if provider == "claude" && !source.HistoryOnly && home != target {
			preference, found, err := claudePreferences(source, home, missingHome)
			if err != nil {
				return plan, err
			}
			if found {
				if old, exists := paths[preference.Target]; exists && (old.SHA256 != preference.SHA256 || old.Mode != preference.Mode) {
					return plan, ErrHistoryConflict
				}
				paths[preference.Target] = preference
			}
		}
		if missingHome {
			if source.Conversation != "" {
				return plan, ErrUnsupported
			}
			continue
		}
		fullContext, visited := seen[home]
		if home == target || visited && (fullContext || source.HistoryOnly) {
			continue
		}
		seen[home] = !source.HistoryOnly
		if e = safePath(home); e != nil {
			return plan, e
		}
		i, e := os.Lstat(home)
		if e != nil || !owned(i, false) || !i.IsDir() {
			return plan, ErrUnsupported
		}
		names := []string{"config.toml", "AGENTS.md", "rules", "skills", "plugins", ".credentials.json", "sessions", "archived_sessions"}
		if provider == "claude" {
			names = []string{"settings.json", "CLAUDE.md", "rules", ".mcp.json", ".credentials.json", "projects", "agents", "commands", "skills", "hooks", "plugins", "output-styles"}
		}
		if source.HistoryOnly {
			names = []string{"sessions", "archived_sessions"}
			if provider == "claude" {
				names = []string{"projects"}
			}
		}
		for _, name := range names {
			start := filepath.Join(home, name)
			if _, e = os.Lstat(start); errors.Is(e, os.ErrNotExist) {
				continue
			} else if e != nil {
				return plan, e
			}
			e = filepath.WalkDir(start, func(path string, d fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				rel, e := filepath.Rel(home, path)
				if e != nil {
					return e
				}
				if !d.IsDir() {
					if provider == "codex" && (strings.HasPrefix(rel, "sessions/") || strings.HasPrefix(rel, "archived_sessions/")) {
						if !codexFiles[path] {
							return nil
						}
					} else if !includeHistoryFile(provider, rel, wanted) {
						return nil
					}
				}
				info, e := os.Lstat(path)
				if e != nil || !owned(info, false) {
					return ErrUnsupported
				}
				if d.IsDir() {
					return nil
				}
				if !info.Mode().IsRegular() || info.Size() > maxFile {
					return ErrUnsupported
				}
				b, e := readFile(path, maxFile, false)
				if e != nil {
					return e
				}
				filter := ""
				if provider == "claude" && rel == ".credentials.json" {
					filter = "oauth"
				}
				data, e := filterContext(b, filter)
				if e != nil {
					return e
				}
				if (provider == "codex" && rel == "config.toml") || (provider == "claude" && rel == "settings.json") {
					if e = logins.ValidateAuthSettings(provider, data); e != nil {
						return e
					}
				}
				mode := os.FileMode(0600)
				if info.Mode().Perm()&0100 != 0 {
					mode = 0700
				}
				sha := digest(data)
				if old, ok := paths[rel]; ok {
					if old.SHA256 != sha || old.Mode != mode {
						return ErrHistoryConflict
					}
					return nil
				}
				if (strings.HasSuffix(rel, ".jsonl") || provider == "codex" && strings.HasSuffix(rel, ".jsonl.zst")) && (strings.HasPrefix(rel, "sessions/") || strings.HasPrefix(rel, "archived_sessions/") || provider == "claude" && strings.Count(rel, "/") == 2 && strings.HasPrefix(rel, "projects/")) {
					id := uuidRE.FindString(filepath.Base(rel))
					if provider == "codex" {
						_, physical, parseErr := rolloutIDs(rel)
						if parseErr != nil {
							return parseErr
						}
						id = physical
					}
					if id != "" {
						if old, ok := conversations[id]; ok && old != sha {
							return ErrHistoryConflict
						}
						conversations[id] = sha
					}
				}
				total += int64(len(b))
				if total > 64<<30 || len(paths) >= 100000 {
					return ErrUnsupported
				}
				paths[rel] = contextFile{path, rel, sha, int64(len(data)), filter, mode}
				return nil
			})
			if e != nil {
				return plan, e
			}
		}
	}
	for _, f := range paths {
		plan.Files = append(plan.Files, f)
	}
	sort.Slice(plan.Files, func(i, j int) bool { return plan.Files[i].Target < plan.Files[j].Target })
	for _, source := range sources {
		if source.Conversation != "" && !contextHasConversation(plan, source) {
			return plan, fmt.Errorf("%w: retained conversation history is inaccessible", ErrUnsupported)
		}
	}
	return plan, nil
}
func claudePreferences(source contextSource, home string, missingHome bool) (contextFile, bool, error) {
	var file contextFile
	path := filepath.Join(home, ".claude.json")
	var raw []byte
	err := os.ErrNotExist
	if !missingHome {
		raw, err = readFile(path, maxJSON, false)
	}
	if errors.Is(err, os.ErrNotExist) && source.ParentPreferences {
		parent, resolveErr := filepath.EvalSymlinks(filepath.Dir(source.Home))
		if resolveErr != nil && !errors.Is(resolveErr, os.ErrNotExist) {
			return file, false, resolveErr
		}
		if resolveErr == nil {
			path = filepath.Join(parent, ".claude.json")
			raw, err = readFile(path, maxJSON, false)
		}
	}
	if errors.Is(err, os.ErrNotExist) {
		return file, false, nil
	}
	if err != nil {
		return file, false, err
	}
	data, err := filterContext(raw, "identity")
	if err != nil {
		return file, false, err
	}
	info, err := os.Lstat(path)
	if err != nil || !owned(info, false) || !info.Mode().IsRegular() {
		return file, false, ErrUnsupported
	}
	mode := os.FileMode(0600)
	if info.Mode().Perm()&0100 != 0 {
		mode = 0700
	}
	return contextFile{path, ".claude.json", digest(data), int64(len(data)), "identity", mode}, true, nil
}

func filterContext(b []byte, kind string) ([]byte, error) {
	if kind == "" {
		return b, nil
	}
	var m map[string]json.RawMessage
	if validJSON(b) != nil || json.Unmarshal(b, &m) != nil || m == nil {
		return nil, ErrUnsupported
	}
	if kind == "identity" {
		for _, k := range []string{"oauthAccount", "userID", "primaryApiKey"} {
			delete(m, k)
		}
	} else {
		delete(m, "claudeAiOauth")
	}
	return json.Marshal(m)
}
func contextHasConversation(plan contextPlan, source contextSource) bool {
	if plan.Provider == "claude" && source.Cwd != "" {
		expected := filepath.Join("projects", claude.New().ProjectDirName(source.Cwd), source.Conversation+".jsonl")
		for _, f := range plan.Files {
			if f.Target == expected {
				return true
			}
		}
		return false
	}
	for _, f := range plan.Files {
		if (strings.HasSuffix(f.Target, ".jsonl") || plan.Provider == "codex" && strings.HasSuffix(f.Target, ".jsonl.zst")) && strings.Contains(filepath.Base(f.Target), source.Conversation) {
			return true
		}
	}
	return false
}
func verifyConversations(home, provider string, sources []contextSource) error {
	for _, s := range sources {
		if s.Conversation == "" {
			continue
		}
		if provider == "claude" && s.Cwd != "" {
			if _, e := readFile(filepath.Join(home, "projects", claude.New().ProjectDirName(s.Cwd), s.Conversation+".jsonl"), maxFile, false); e != nil {
				return ErrUnsupported
			}
			continue
		}
		found := false
		dirs := []string{"sessions", "archived_sessions"}
		if provider == "claude" {
			dirs = []string{"projects"}
		}
		for _, dir := range dirs {
			e := filepath.WalkDir(filepath.Join(home, dir), func(path string, d fs.DirEntry, e error) error {
				if errors.Is(e, os.ErrNotExist) {
					return nil
				}
				if e != nil {
					return e
				}
				if !d.IsDir() && (strings.HasSuffix(d.Name(), ".jsonl") || provider == "codex" && strings.HasSuffix(d.Name(), ".jsonl.zst")) && strings.Contains(d.Name(), s.Conversation) {
					found = true
				}
				return nil
			})
			if e != nil {
				return e
			}
		}
		if !found {
			return ErrUnsupported
		}
	}
	return nil
}
func contextWriters(provider, home string, plan contextPlan) error {
	if len(plan.Files) == 0 {
		for _, source := range plan.Conversations {
			if source.Conversation != "" {
				return checkExternalWriters(provider)
			}
		}
		// An empty bootstrap imports no external authority. Only writers of
		// this private destination can race its initial publication.
		writers, err := environmentWriters([]string{home}, nil)
		if err != nil || len(writers) != 0 {
			return ErrLiveWriter
		}
		return nil
	}
	return checkExternalWriters(provider)
}

func prepareContext(state, provider string, sources []contextSource) error {
	plan, e := planContext(state, provider, sources)
	if e != nil {
		return e
	}
	home := providerHome(state, provider)
	if _, e = os.Lstat(filepath.Join(home, contextMarker)); e == nil {
		return nil
	}
	if e = contextWriters(provider, home, plan); e != nil {
		return e
	}
	parent := filepath.Dir(home)
	if e = privateDir(parent); e != nil {
		return e
	}
	stage := filepath.Join(parent, ".native-migration-stage")
	if _, e = os.Lstat(stage); e == nil {
		if e = safePath(stage); e != nil {
			return e
		}
		if e = os.RemoveAll(stage); e != nil {
			return e
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if e = privateDir(stage); e != nil {
		return e
	}
	for _, f := range plan.Files {
		if e = privateDir(filepath.Dir(filepath.Join(stage, f.Target))); e != nil {
			return e
		}
		b, e := readFile(f.Source, maxFile, false)
		if e != nil {
			return e
		}
		info, statErr := os.Lstat(f.Source)
		if statErr != nil {
			return statErr
		}
		mode := os.FileMode(0600)
		if info.Mode().Perm()&0100 != 0 {
			mode = 0700
		}
		if mode != f.Mode {
			return ErrHistoryConflict
		}
		b, e = filterContext(b, f.Filter)
		if e != nil || digest(b) != f.SHA256 {
			return ErrHistoryConflict
		}
		destination := filepath.Join(stage, f.Target)
		if e = replaceMode(destination, b, f.Mode); e != nil {
			return e
		}
	}
	if e = verifyConversations(stage, provider, sources); e != nil {
		return e
	}
	if e = writeJSON(filepath.Join(stage, contextMarker), plan); e != nil {
		return e
	}
	if e = syncDir(stage); e != nil {
		return e
	}
	if e = contextWriters(provider, home, plan); e != nil {
		return e
	}
	if entries, e := os.ReadDir(home); e == nil && len(entries) != 0 {
		return ErrHistoryConflict
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	// Darwin cannot rename over an existing empty directory. Rmdir cannot
	// remove files or populated directories, including a raced-in native home.
	if e = syscall.Rmdir(home); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if e = os.Rename(stage, home); e != nil {
		return e
	}
	return syncDir(parent)
}

// Native transcript discovery rebuilds its index from these selected rollouts;
// unrelated owner history and SQLite databases never enter the execution home.
func includeHistoryFile(provider, relative string, wanted map[string]bool) bool {
	history := strings.HasPrefix(relative, "sessions/") || strings.HasPrefix(relative, "archived_sessions/") || provider == "claude" && strings.HasPrefix(relative, "projects/")
	if !history {
		return true
	}
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		for id := range wanted {
			if strings.Contains(part, id) {
				return true
			}
		}
	}
	return false
}
