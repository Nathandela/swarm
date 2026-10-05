package accountconfig

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/Nathandela/swarm/internal/adapter/claude"
)

// CompatibilityVersion is the current reader capability. Existing projections
// retain their original requirements; native Codex stock custody requires 4.
const CompatibilityVersion = 4

type projectBoundary struct {
	Home, Root, MainRoot string          `json:",omitempty"`
	Evidence             []projectMarker `json:",omitempty"`
}

type projectMarker struct {
	Path, Kind string
	SHA256     string `json:",omitempty"`
}

// ProjectionCompatibility validates intrinsic, nonsecret projection metadata.
// The caller must separately anchor the file and verify its reference hash.
// It never reads mutable user settings or native credentials.
func ProjectionCompatibility(raw []byte) (int, error) {
	var fields map[string]json.RawMessage
	var m manifest
	if len(raw) > maxSourceBytes || json.Unmarshal(raw, &fields) != nil || json.Unmarshal(raw, &m) != nil || (m.SchemaVersion != 1 && m.SchemaVersion != 3) ||
		(m.Provider != "codex" && m.Provider != "claude") || !cleanAbsolute(m.Cwd) {
		return 0, Conflict("projection-corrupt")
	}
	if m.SchemaVersion == 3 {
		if m.Codex != nil || m.Claude != nil || len(m.Cohort) != 0 || len(m.Sources) != 0 || len(m.SourceAliases) != 0 ||
			(m.Provider == "codex") != (m.CodexContext != nil) || (m.Provider == "claude") != (m.ClaudeContext != nil) {
			return 0, Conflict("projection-corrupt")
		}
		if m.ClaudeContext != nil && !validClaudeContext(*m.ClaudeContext) {
			return 0, Conflict("projection-corrupt")
		}
		if m.Provider == "claude" && (m.CodexHistoryAlias != nil || len(m.CodexProjectSources) != 0 || m.ClaudeHistoryAlias == nil || m.ClaudeHistoryAlias.ProjectKey != claude.New().ProjectDirName(m.Cwd) || !ValidClaudeHistoryAlias(*m.ClaudeContext, *m.ClaudeHistoryAlias)) {
			return 0, Conflict("projection-corrupt")
		}
		if m.Provider == "codex" && (m.ClaudeHistoryAlias != nil || len(m.ClaudeProjectSources) != 0 || len(m.ClaudeInvocationSources) != 0 || m.CodexHistoryAlias == nil || !ValidCodexHistoryAlias(*m.CodexContext, *m.CodexHistoryAlias)) {
			return 0, Conflict("projection-corrupt")
		}
		if m.CodexContext != nil && !validCodexContext(*m.CodexContext) {
			return 0, Conflict("projection-corrupt")
		}

	} else if m.CodexContext != nil || m.ClaudeContext != nil || m.CodexHistoryAlias != nil || m.ClaudeHistoryAlias != nil || len(m.ClaudeProjectSources) != 0 || len(m.CodexProjectSources) != 0 || len(m.ClaudeInvocationSources) != 0 {
		return 0, Conflict("projection-corrupt")
	}
	for key := range fields {
		if strings.EqualFold(key, "ProjectBoundary") && key != "ProjectBoundary" {
			return 0, Conflict("projection-corrupt")
		}
	}
	boundaryRaw, exists := fields["ProjectBoundary"]
	if !exists {
		if m.SchemaVersion == 3 {
			if m.Provider == "codex" {
				return 0, Conflict("projection-corrupt")
			}
			if err := validNativeProjectProvenance(m); err != nil {
				return 0, err
			}
			return 3, nil
		}
		return 1, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(boundaryRaw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&m.ProjectBoundary) != nil {
		return 0, Conflict("projection-corrupt")
	}
	b := m.ProjectBoundary
	if b == nil || len(b.Evidence) > 1024 {
		return 0, Conflict("projection-corrupt")
	}
	if m.Provider == "claude" {
		if !cleanAbsolute(b.Home) || b.Home == m.Cwd || !withinDirectory(b.Home, m.Cwd) || b.Root != "" || b.MainRoot != "" || len(b.Evidence) != 0 {
			return 0, Conflict("projection-corrupt")
		}
	} else {
		if b.Home != "" || !cleanAbsolute(b.Root) || !withinDirectory(b.Root, m.Cwd) || len(b.Evidence) == 0 ||
			(b.MainRoot != "" && (!cleanAbsolute(b.MainRoot) || b.MainRoot == b.Root)) {
			return 0, Conflict("projection-corrupt")
		}
		seen := map[string]bool{}
		for _, e := range b.Evidence {
			if !cleanAbsolute(e.Path) || seen[e.Path] {
				return 0, Conflict("projection-corrupt")
			}
			seen[e.Path] = true
			switch e.Kind {
			case "absent", "directory", "empty-directory", "present":
				if e.SHA256 != "" {
					return 0, Conflict("projection-corrupt")
				}
			case "file":
				digest, err := hex.DecodeString(e.SHA256)
				if err != nil || len(digest) != sha256.Size {
					return 0, Conflict("projection-corrupt")
				}
			default:
				return 0, Conflict("projection-corrupt")
			}
		}
	}
	if m.SchemaVersion == 3 {
		if err := validNativeProjectProvenance(m); err != nil {
			return 0, err
		}
		return 3, nil
	}
	return 2, nil
}

func cleanAbsolute(path string) bool { return filepath.IsAbs(path) && filepath.Clean(path) == path }

func withinDirectory(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func launchHome(env []string) (string, error) {
	home := ""
	seen := false
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "HOME="); ok {
			if seen {
				return "", Conflict("invalid-environment")
			}
			home, seen = value, true
		}
	}
	return home, nil
}

func discoverProjectBoundary(provider, cwd string, env []string) (*projectBoundary, error) {
	if provider == "codex" {
		return discoverCodexBoundary(cwd)
	}
	home, err := launchHome(env)
	if err != nil {
		return nil, err
	}
	if home == "" || home == cwd || !withinDirectory(home, cwd) {
		return nil, nil
	}
	if safePath(home, true) != nil {
		return nil, Conflict("unsafe-project-home")
	}
	return &projectBoundary{Home: home}, nil
}

func validateProjectBoundary(m manifest, env []string) error {
	if m.ProjectBoundary == nil {
		return nil
	}
	if safePath(m.Cwd, true) != nil {
		return Conflict("unsafe-project-directory")
	}
	var current *projectBoundary
	var err error
	if m.Provider == "claude" {
		home := m.ProjectBoundary.Home
		if env != nil {
			if actual, e := launchHome(env); e != nil || actual != home {
				return Conflict("project-home-changed")
			}
		}
		current, err = discoverProjectBoundary("claude", m.Cwd, []string{"HOME=" + home})
	} else {
		current, err = discoverCodexBoundary(m.Cwd)
	}
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(m.ProjectBoundary, current) {
		return Conflict("project-boundary-changed")
	}
	return nil
}

// Codex 0.160.0's default marker is the nearest .git file or directory
// containing HEAD. No marker means cwd, rather than an unbounded ancestor walk.
func discoverCodexBoundary(cwd string) (*projectBoundary, error) {
	b := &projectBoundary{Root: cwd}
	for dir := cwd; ; dir = filepath.Dir(dir) {
		path := filepath.Join(dir, ".git")
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			b.Evidence = append(b.Evidence, projectMarker{Path: path, Kind: "absent"})
		} else if err != nil || safePath(path, false) != nil {
			return nil, Conflict("unsafe-project-marker")
		} else if info.IsDir() {
			head := filepath.Join(path, "HEAD")
			if _, err := os.Lstat(head); errors.Is(err, os.ErrNotExist) {
				b.Evidence = append(b.Evidence, projectMarker{Path: path, Kind: "empty-directory"})
			} else if err != nil || safePath(head, false) != nil {
				return nil, Conflict("unsafe-project-marker")
			} else {
				b.Evidence = append(b.Evidence, projectMarker{Path: path, Kind: "directory"}, projectMarker{Path: head, Kind: "present"})
				b.Root = dir
				break
			}
		} else if info.Mode().IsRegular() {
			raw, err := markerFile(b, path)
			if err != nil {
				return nil, err
			}
			b.Root = dir
			main, err := worktreeMainRoot(b, dir, raw)
			if err != nil {
				return nil, err
			}
			b.MainRoot = main
			break
		} else {
			return nil, Conflict("unsafe-project-marker")
		}
		if len(b.Evidence) > 1000 {
			return nil, Conflict("project-marker-inventory-limit")
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	return b, nil
}

func markerFile(b *projectBoundary, path string) ([]byte, error) {
	// Git's shared-repository files may be group writable. They remain bounded,
	// nonalias, anchored and hash-frozen; user settings keep the stricter reader.
	raw, err := readBoundedRegular(path, 64<<10, 0o002)
	if err != nil {
		return nil, Conflict("unsafe-project-marker")
	}
	digest := sha256.Sum256(raw)
	b.Evidence = append(b.Evidence, projectMarker{Path: path, Kind: "file", SHA256: hex.EncodeToString(digest[:])})
	return raw, nil
}

func pointerPath(base, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(base, value))
}

// A linked worktree may load main-checkout hooks outside its project range.
// Prove ordinary Git's reciprocal pointers before admitting that extra root;
// unfamiliar layouts refuse instead of inferring authority from a pathname.
func worktreeMainRoot(b *projectBoundary, checkout string, raw []byte) (string, error) {
	value, ok := strings.CutPrefix(strings.TrimSpace(string(raw)), "gitdir:")
	if !ok || strings.TrimSpace(value) == "" {
		return "", Conflict("unsupported-git-layout")
	}
	gitDir := pointerPath(checkout, strings.TrimSpace(value))
	if safePath(gitDir, true) != nil || filepath.Base(filepath.Dir(gitDir)) != "worktrees" {
		return "", Conflict("unsupported-git-layout")
	}
	common := filepath.Dir(filepath.Dir(gitDir))
	back, err := markerFile(b, filepath.Join(gitDir, "gitdir"))
	if err != nil || pointerPath(gitDir, strings.TrimSpace(string(back))) != filepath.Join(checkout, ".git") {
		return "", Conflict("unsupported-git-layout")
	}
	commondir, err := markerFile(b, filepath.Join(gitDir, "commondir"))
	if err != nil || pointerPath(gitDir, strings.TrimSpace(string(commondir))) != common {
		return "", Conflict("unsupported-git-layout")
	}
	main := filepath.Dir(common)
	if filepath.Join(main, ".git") != common || safePath(common, true) != nil {
		return "", Conflict("unsupported-git-layout")
	}
	if _, err := os.Lstat(filepath.Join(common, "HEAD")); err != nil || safePath(filepath.Join(common, "HEAD"), false) != nil {
		return "", Conflict("unsupported-git-layout")
	}
	b.Evidence = append(b.Evidence, projectMarker{Path: common, Kind: "directory"}, projectMarker{Path: filepath.Join(common, "HEAD"), Kind: "present"})
	return main, nil
}
