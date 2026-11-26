package worktree

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Resolve Git identity from cwd, even when Swarm itself was started in a Git hook.
func gitCommand(cwd string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", append([]string{"-C", cwd}, args...)...)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "GIT_DIR", "GIT_COMMON_DIR", "GIT_WORK_TREE", "GIT_IMPLICIT_WORK_TREE",
			"GIT_INDEX_FILE", "GIT_PREFIX", "GIT_INTERNAL_SUPER_PREFIX",
			"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_GRAFT_FILE",
			"GIT_SHALLOW_FILE", "GIT_CONFIG", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT",
			"GIT_NO_REPLACE_OBJECTS", "GIT_REPLACE_REF_BASE":
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	return cmd
}

func gitValue(cwd string, args ...string) (string, error) {
	out, err := gitCommand(cwd, args...).Output()
	return strings.TrimSuffix(string(out), "\n"), err
}

func commonDir(cwd string) (string, error) {
	dir, err := gitValue(cwd, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(dir)
}

func registeredPaths(cwd string) ([]string, error) {
	out, err := gitCommand(cwd, "worktree", "list", "--porcelain", "-z").CombinedOutput()
	delimiter := byte(0)
	if err != nil && bytes.Contains(out, []byte("unknown switch `z'")) {
		// Git 2.34 supports porcelain with quoted paths but no NUL delimiter.
		out, err = gitCommand(cwd, "worktree", "list", "--porcelain").CombinedOutput()
		delimiter = '\n'
	}
	if err != nil {
		return nil, fmt.Errorf("list Git worktrees: %w\n%s", err, out)
	}
	var paths []string
	for _, field := range bytes.Split(out, []byte{delimiter}) {
		path, ok := strings.CutPrefix(string(field), "worktree ")
		if !ok {
			continue
		}
		if delimiter == '\n' && strings.HasPrefix(path, "\"") {
			path, err = strconv.Unquote(path)
			if err != nil {
				return nil, err
			}
		}
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("invalid Git worktree path %q", path)
		}
		paths = append(paths, path)
	}
	if len(paths) == 0 {
		return nil, errors.New("missing Git worktree registrations")
	}
	return paths, nil
}

// resolveRoot resolves either existing component of the managed path, without
// treating a dangling symlink as a missing directory Git should create.
func resolveRoot(anchor string) (string, error) {
	root, err := filepath.EvalSymlinks(anchor)
	if err != nil {
		return "", err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", err
	}
	for _, segment := range []string{".swarm", "worktrees"} {
		root = filepath.Join(root, segment)
		info, err := os.Lstat(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			root, err = filepath.EvalSymlinks(root)
			if err != nil {
				return "", err
			}
			info, err = os.Stat(root)
			if err != nil {
				return "", err
			}
		}
		if !info.IsDir() {
			return "", fmt.Errorf("worktree root component %q is not a directory", root)
		}
	}
	return root, nil
}

// DestinationRoot uses the physical primary checkout for the source's Git
// repository. Bare and separate metadata repositories use their common directory
// when Git lists that directory as the primary.
func DestinationRoot(cwd string) (string, error) {
	if !IsRepo(cwd) {
		return "", fmt.Errorf("worktree: %q is not a Git checkout", cwd)
	}
	common, err := commonDir(cwd)
	if err != nil {
		return "", err
	}
	paths, err := registeredPaths(cwd)
	if err != nil {
		return "", err
	}
	anchor, err := filepath.EvalSymlinks(paths[0])
	if err != nil {
		return "", err
	}
	if anchor != common {
		primaryCommon, err := commonDir(anchor)
		top, topErr := gitValue(anchor, "rev-parse", "--show-toplevel")
		if err != nil || topErr != nil || primaryCommon != common || top != anchor {
			return "", fmt.Errorf("worktree: cannot verify primary checkout %q", anchor)
		}
	}
	root, err := resolveRoot(anchor)
	if err != nil {
		return "", fmt.Errorf("worktree: resolve destination: %w", err)
	}
	// An independent repository can itself live inside a linked checkout. Also
	// check resolved aliases, since a repository can redirect its worktree root.
	aliased := root != worktreeRoot(anchor)
	if aliased && (PathsOverlap(common, root) || isWithin(root, anchor) || sameAncestor(root, anchor)) {
		return "", fmt.Errorf("worktree: unsafe destination alias %q", root)
	}
	for ancestor := root; ; ancestor = filepath.Dir(ancestor) {
		if info, err := os.Lstat(filepath.Join(ancestor, ".git")); err == nil {
			if aliased || !info.IsDir() {
				owner, err := commonDir(ancestor)
				gitDir, gitErr := gitValue(ancestor, "rev-parse", "--absolute-git-dir")
				if err != nil || gitErr != nil || gitDir != owner || (aliased && owner != common) {
					return "", fmt.Errorf("worktree: destination %q lies inside another checkout at %q", root, ancestor)
				}
			}
		} else if !os.IsNotExist(err) {
			return "", err
		}
		if parent := filepath.Dir(ancestor); parent == ancestor {
			break
		}
	}
	return root, nil
}
