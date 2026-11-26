// Package worktree implements Epic 12's launch-time isolation (S-3/R-3): Create
// prepares an isolated git worktree + branch for a session and Remove tears it
// down. Session ids are validated against path traversal (ADR-004) before
// anything ever touches git or disk.
package worktree

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/persist"
)

// maxSlugBytes keeps the generated path component and branch suffix comfortably
// below common filesystem and git-ref limits. The session id is always retained
// in full; only the cosmetic name prefix is truncated.
const maxSlugBytes = 128

// workspaceMu serializes creation with teardown in this supervisor.
var workspaceMu sync.Mutex

// validID reports whether id is safe to use as a path component and as a git
// branch-name suffix (ADR-004); it delegates to persist.ValidID, the single
// source of truth for the path-safe session-id pattern.
func validID(id string) bool {
	return persist.ValidID(id)
}

// slugifyName turns a cosmetic session name into a safe, readable path/ref
// component. Letter and number runs are preserved (including Unicode), all other
// runs become one hyphen, and leading/trailing separators disappear.
func slugifyName(name string) string {
	var b strings.Builder
	separator := false
	for _, r := range strings.TrimSpace(name) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			if separator && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(unicode.ToLower(r))
			separator = false
			continue
		}
		separator = b.Len() > 0
	}
	return b.String()
}

// truncateUTF8 returns the longest rune-aligned prefix of s that occupies no
// more than max bytes.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

// slugs returns the preferred readable directory component and the unique git
// branch component. The common named path is just the name slug; the branch
// retains the full session id. Empty or punctuation-only names keep the
// established id-only form.
func slugs(id, name string) (dirSlug, branchSlug string) {
	prefix := slugifyName(name)
	prefix = strings.TrimRight(truncateUTF8(prefix, maxSlugBytes), "-")
	if prefix == "" {
		return id, id
	}

	budget := maxSlugBytes - len(id) - 1
	if budget <= 0 {
		return prefix, id
	}
	branchPrefix := strings.TrimRight(truncateUTF8(prefix, budget), "-")
	if branchPrefix == "" {
		return prefix, id
	}
	return prefix, branchPrefix + "-" + id
}

func worktreeRoot(repoDir string) string {
	return filepath.Join(repoDir, ".swarm", "worktrees")
}

// worktreeDir returns the on-disk worktree path for a path-safe slug.
func worktreeDir(repoDir, slug string) string {
	return filepath.Join(worktreeRoot(repoDir), slug)
}

// IsRepo reports whether dir is inside a git work tree, the condition under which
// Create can isolate a session there. A missing directory or a failing git is false.
func IsRepo(dir string) bool {
	out, err := gitValue(dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && out == "true"
}

// ReservedCheckout recovers the exact registered checkout when a supervisor
// crashed after git worktree add and before the reserved session metadata save.
func ReservedCheckout(repoDir, id, name string) (string, error) {
	if !validID(id) {
		return "", fmt.Errorf("invalid worktree session ID %q", id)
	}
	friendly, unique := slugs(id, name)
	ref := "refs/heads/swarm/" + unique
	// for-each-ref prints the registered checkout, including paths containing newlines.
	raw, err := gitValue(repoDir, "for-each-ref", "--format=%(refname)%00%(worktreepath)", ref)
	if err != nil {
		return "", err
	}
	if raw == "" {
		return "", nil
	}
	refName, path, ok := strings.Cut(raw, "\x00")
	if !ok || refName != ref {
		return "", fmt.Errorf("unexpected registered branch for %q", id)
	}
	legacy, _ := resolveRoot(repoDir)
	if !matchesCandidate(legacy, path, friendly, unique) {
		root, err := DestinationRoot(repoDir)
		if err != nil {
			return "", err
		}
		if !matchesCandidate(root, path, friendly, unique) {
			return "", fmt.Errorf("reserved worker branch has moved to %q", path)
		}
	}
	actual, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if actual != path {
		return "", fmt.Errorf("reserved checkout %q is redirected", path)
	}
	// A stale Git registration can survive a plain directory move and path reuse.
	head, err := gitValue(path, "symbolic-ref", "-q", "HEAD")
	if err != nil || head != ref {
		return "", fmt.Errorf("reserved checkout HEAD does not match %s", ref)
	}
	top, err := gitValue(path, "rev-parse", "--show-toplevel")
	if err != nil || top != path {
		return "", fmt.Errorf("registered worker checkout %q is unavailable", path)
	}
	sourceCommon, err := commonDir(repoDir)
	if err != nil {
		return "", err
	}
	common, err := commonDir(path)
	if err != nil || common != sourceCommon {
		return "", fmt.Errorf("reserved checkout %q has another owning repository", path)
	}
	if owner, err := recordedOwner(path); err != nil && !os.IsNotExist(err) {
		return "", err
	} else if err == nil && owner != id {
		return "", fmt.Errorf("reserved checkout %q is owned by another session", path)
	}
	return path, nil
}

func matchesCandidate(root, path, friendly, unique string) bool {
	return root != "" && filepath.Dir(path) == root &&
		(filepath.Base(path) == friendly || filepath.Base(path) == unique)
}

// Create makes an isolated git worktree for session id. When name has usable
// letters or numbers, its preferred directory is <name-slug> and its branch is
// swarm/<name-slug>-<id>. If that friendly directory is occupied by another
// same-named session, this session falls back to <name-slug>-<id>. An unnamed
// session retains the established id-only form. id is validated before anything
// touches git or disk, so a rejected id never creates so much as the .swarm
// directory.
func Create(repoDir, id, name string) (string, error) {
	return CreateChecked(repoDir, id, name, nil)
}

// CreateChecked applies the caller's policy to the physical destination before
// claiming it or updating Git.
func CreateChecked(repoDir, id, name string, allow func(string) error) (string, error) {
	if !validID(id) {
		return "", fmt.Errorf("worktree: invalid session id %q", id)
	}
	source, err := filepath.EvalSymlinks(repoDir)
	if err != nil {
		return "", err
	}
	workspaceMu.Lock()
	defer workspaceMu.Unlock()
	root, err := DestinationRoot(source)
	if err != nil {
		return "", err
	}
	friendly, unique := slugs(id, name)
	dir := filepath.Join(root, friendly)
	if allow != nil {
		if err := allow(dir); err != nil {
			return "", err
		}
		if current, err := DestinationRoot(source); err != nil || current != root {
			return "", fmt.Errorf("worktree: destination changed during policy validation: %q", root)
		}
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	// An atomic directory claim also coordinates independent supervisors.
	err = os.Mkdir(dir, 0o755)
	if os.IsExist(err) && friendly != unique {
		dir = filepath.Join(root, unique)
		if allow != nil {
			if err := allow(dir); err != nil {
				return "", err
			}
		}
		err = os.Mkdir(dir, 0o755)
	}
	if err != nil {
		return "", fmt.Errorf("worktree: claim %q: %w", dir, err)
	}
	claim, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	// A checkout can rename or redirect its workspace while we are claiming it.
	removeClaim := func() {
		if info, err := os.Lstat(dir); err == nil && os.SameFile(claim, info) {
			_ = os.Remove(dir)
		}
	}
	if physical, err := filepath.EvalSymlinks(dir); err != nil || physical != dir {
		removeClaim()
		return "", fmt.Errorf("worktree: claimed destination %q was redirected", dir)
	}
	out, err := gitCommand(source, "worktree", "add", "-b", "swarm/"+unique, dir, "HEAD").CombinedOutput()
	if err != nil {
		removeClaim()
		return "", fmt.Errorf("worktree: git worktree add %s: %w\n%s", dir, err, out)
	}
	marker, err := ownerMarker(dir)
	if err == nil {
		err = os.WriteFile(marker, []byte(id), 0o600)
	}
	if err != nil {
		return "", fmt.Errorf("worktree: cannot record owner of %q; checkout retained: %w", dir, err)
	}
	return dir, nil
}

func ownerMarker(cwd string) (string, error) {
	admin, err := gitValue(cwd, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	common, err := commonDir(cwd)
	if err != nil {
		return "", err
	}
	if filepath.Dir(admin) != filepath.Join(common, "worktrees") {
		return "", fmt.Errorf("worktree: %q has an unexpected Git administrative directory %q", cwd, admin)
	}
	return filepath.Join(admin, "swarm-session"), nil
}

func recordedOwner(cwd string) (string, error) {
	marker, err := ownerMarker(cwd)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(marker)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > maxSlugBytes {
		return "", fmt.Errorf("worktree: invalid owner record in %q", marker)
	}
	data, err := os.ReadFile(marker)
	return string(data), err
}

// Remove tears down an older worktree recorded only by session id.
func Remove(repoDir, id string) error {
	if !validID(id) {
		return fmt.Errorf("worktree: invalid session id %q", id)
	}

	return RemoveAt(repoDir, worktreeDir(repoDir, id), id)
}

// RemoveAt tears down the exact worktree path persisted in Meta.AgentCwd. It
// accepts the canonical placement and the original cwd-relative layout.
func RemoveAt(repoDir, dir, id string) error {
	if !validID(id) {
		return fmt.Errorf("worktree: invalid session id %q", id)
	}
	workspaceMu.Lock()
	defer workspaceMu.Unlock()
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("worktree: %q is not a physical checkout directory", dir)
	}
	path, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	top, err := gitValue(path, "rev-parse", "--show-toplevel")
	if err != nil || top != path {
		return fmt.Errorf("worktree: %q is not the root of a Git checkout", path)
	}
	paths, err := registeredPaths(path)
	if err != nil {
		return err
	}
	registrations := 0
	for _, candidate := range paths[1:] {
		if candidate == path {
			registrations++
		} else if isWithin(path, candidate) || sameAncestor(path, candidate) {
			return fmt.Errorf("worktree: %q contains a registered Git worktree %q; checkout retained", path, candidate)
		}
	}
	if registrations != 1 {
		return fmt.Errorf("worktree: %q does not have exactly one linked checkout registration", path)
	}
	if owner, err := recordedOwner(path); err == nil {
		if owner != id {
			return fmt.Errorf("worktree: %q is owned by another session", path)
		}
	} else if os.IsNotExist(err) {
		branch, err := gitValue(path, "symbolic-ref", "--quiet", "HEAD")
		name, swarmBranch := strings.CutPrefix(branch, "refs/heads/swarm/")
		if err != nil || !swarmBranch || (name != id && !strings.HasSuffix(name, "-"+id)) {
			return fmt.Errorf("worktree: cannot verify ownership of legacy checkout %q", path)
		}
	} else {
		return err
	}

	if _, err := os.Stat(repoDir); os.IsNotExist(err) {
		// A flattened child can outlast its source.
		root, err := DestinationRoot(path)
		if err != nil {
			return err
		}
		if filepath.Dir(path) != root {
			return fmt.Errorf("worktree: %q is outside the verified destination %q", path, root)
		}
	} else {
		sourceCommon, sourceErr := commonDir(repoDir)
		targetCommon, targetErr := commonDir(path)
		if sourceErr != nil || targetErr != nil || sourceCommon != targetCommon {
			return fmt.Errorf("worktree: %q does not belong to the source Git repository", path)
		}
		legacy, _ := resolveRoot(repoDir)
		if filepath.Dir(path) != legacy {
			root, err := DestinationRoot(repoDir)
			if err != nil {
				return err
			}
			if filepath.Dir(path) != root {
				return fmt.Errorf("worktree: %q is outside the verified destination %q", path, root)
			}
		}
	}
	if err := filepath.WalkDir(path, func(child string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == ".git" && filepath.Dir(child) != path {
			return fmt.Errorf("worktree: %q contains another Git checkout at %q; checkout retained", path, filepath.Dir(child))
		}
		return nil
	}); err != nil {
		return err
	}
	if out, err := gitCommand(path, "worktree", "remove", "--force", path).CombinedOutput(); err != nil {
		return fmt.Errorf("worktree: git worktree remove %s: %w\n%s", path, err, out)
	}
	return nil
}
