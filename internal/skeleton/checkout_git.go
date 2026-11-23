package skeleton

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

type checkoutGitRecord struct {
	path, kind, head, branch string
	locked, prunable         bool
}

func inventoryGitPaths(ctx context.Context, paths, unknowns []string) ([]CheckoutPath, []string) {
	items := make(map[string]CheckoutPath)
	repos := make(map[string][]checkoutGitRecord)
	probes := make(map[string]checkoutGitProbe)
	probe := func(dir string) checkoutGitProbe {
		if known, ok := probes[dir]; ok {
			return known
		}
		result := findCheckoutGit(ctx, dir)
		probes[dir] = result
		return result
	}
	const maxInventoryPaths = 1024
	for i, input := range paths {
		if i == 128 || len(items) >= maxInventoryPaths || ctx.Err() != nil {
			unknowns = append(unknowns, "checkout path discovery limit reached")
			break
		}
		path, err := canonicalCleanupPath(input)
		if err != nil {
			items[input] = CheckoutPath{Path: input, PathState: "unknown", Unknowns: []string{err.Error()}}
			continue
		}
		item := checkoutPathState(path)
		dir := path
		for {
			info, err := os.Stat(dir)
			if err == nil && info.IsDir() {
				break
			}
			if err != nil && !os.IsNotExist(err) || filepath.Dir(dir) == dir {
				dir = ""
				break
			}
			dir = filepath.Dir(dir)
		}
		if dir == "" {
			item.Unknowns = append(item.Unknowns, "cannot find an accessible directory for Git lookup")
			items[path] = item
			continue
		}
		found := probe(dir)
		item.GitKind = found.kind
		if found.err != nil {
			item.Unknowns = append(item.Unknowns, "cannot verify Git registration: "+found.err.Error())
		}
		if found.common == "" {
			items[path] = item
			continue
		}
		list, ok := repos[found.common]
		if !ok {
			if len(repos) >= 128 {
				item.Unknowns = append(item.Unknowns, "repository discovery limit reached")
				items[path] = item
				continue
			}
			list, err = checkoutGitWorktrees(ctx, dir)
			e := err
			if e != nil {
				item.GitCommonDir = found.common
				item.Unknowns = append(item.Unknowns, "cannot verify Git worktrees: "+e.Error())
				items[path] = item
				continue
			}
			repos[found.common] = list
			for _, row := range list {
				if len(items) >= maxInventoryPaths || ctx.Err() != nil {
					unknowns = append(unknowns, "Git worktree discovery limit reached")
					break
				}
				registered := checkoutPathState(row.path)
				applyCheckoutGit(&registered, found.common, list[0].path, row)
				registered.registrationUnverified = row.prunable || registered.PathState != "directory"
				if row.prunable {
					registered.Unknowns = append(registered.Unknowns, "Git marks this registration prunable")
				}
				if registered.PathState == "directory" {
					ours := probe(row.path)
					resolved, _ := canonicalCleanupPath(row.path)
					if row.path != resolved || ours.common == "" || !checkoutSamePath(ours.common, found.common) ||
						row.kind != "bare" && !checkoutRegistrationMatches(ctx, row) {
						registered.registrationUnverified = true
						registered.Unknowns = append(registered.Unknowns, "checkout does not prove it still belongs to the registered repository")
					}
				}
				if prior, exists := items[row.path]; exists {
					registered.Unknowns = append(registered.Unknowns, prior.Unknowns...)
					if prior.GitCommonDir != "" && !checkoutSamePath(prior.GitCommonDir, found.common) {
						registered.Unknowns = append(registered.Unknowns, fmt.Sprintf("conflicting Git registrations from %q and %q", prior.GitCommonDir, found.common))
						registered.GitKind = "ambiguous"
						registered.registrationUnverified = true
					}
				}
				items[row.path] = registered
			}
		}
		item.GitCommonDir, item.Repo = found.common, list[0].path
		var nearest checkoutGitRecord
		for _, row := range list {
			registered, known := items[row.path]
			if row.path == path {
				nearest = row
				break
			}
			if known && !registered.registrationUnverified && cleanupContains(row.path, path) && len(row.path) > len(nearest.path) {
				nearest = row
			}
		}
		if _, exists := items[path]; exists && nearest.path == path {
			continue
		}
		if nearest.path == "" {
			item.Unknowns = append(item.Unknowns, "path is not within a registered worktree")
		} else {
			applyCheckoutGit(&item, found.common, list[0].path, nearest)
			if registered, ok := items[nearest.path]; ok {
				item.Unknowns = append(item.Unknowns, registered.Unknowns...)
			}
		}
		items[path] = item
	}
	out := make([]CheckoutPath, 0, len(items))
	for _, item := range items {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, unknowns
}

func checkoutPathState(path string) CheckoutPath {
	item := CheckoutPath{Path: path}
	switch info, err := os.Stat(path); {
	case os.IsNotExist(err):
		item.PathState = "missing"
		item.Unknowns = append(item.Unknowns, "path is missing")
	case err != nil:
		item.PathState = "unknown"
		item.Unknowns = append(item.Unknowns, "cannot inspect path: "+err.Error())
	case info.IsDir():
		item.PathState = "directory"
	default:
		item.PathState = "file"
	}
	return item
}

func checkoutRegistrationMatches(ctx context.Context, row checkoutGitRecord) bool {
	top, err := checkoutGit(ctx, row.path, "rev-parse", "--show-toplevel")
	if err != nil || !checkoutSamePath(row.path, strings.TrimSuffix(string(top), "\n")) {
		return false
	}
	if row.kind != "linked" {
		return true
	}
	private, err := checkoutGit(ctx, row.path, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return false
	}
	gitDir := strings.TrimSuffix(string(private), "\n")
	if !filepath.IsAbs(gitDir) {
		return false
	}
	f, err := os.OpenFile(filepath.Join(gitDir, "gitdir"), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(f, 65537))
	backpointer := strings.TrimSuffix(string(data), "\n")
	if backpointer != "" && !filepath.IsAbs(backpointer) {
		backpointer = filepath.Join(gitDir, backpointer)
	}
	return err == nil && len(data) <= 65536 && checkoutSamePath(filepath.Join(row.path, ".git"), backpointer)
}

func applyCheckoutGit(item *CheckoutPath, common, repo string, row checkoutGitRecord) {
	item.Repo, item.GitCommonDir, item.Worktree, item.GitKind = repo, common, row.path, row.kind
	item.Branch, item.Head, item.Locked, item.Prunable = row.branch, row.head, row.locked, row.prunable
}

type checkoutGitProbe struct {
	common, kind string
	err          error
}

func findCheckoutGit(ctx context.Context, dir string) checkoutGitProbe {
	raw, err := checkoutGit(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		if strings.Contains(err.Error(), "not a git repository") {
			return checkoutGitProbe{kind: "outside_git"}
		}
		return checkoutGitProbe{err: err}
	}
	path := strings.TrimSuffix(string(raw), "\n")
	if !filepath.IsAbs(path) {
		return checkoutGitProbe{err: errors.New("received a non-absolute Git common directory")}
	}
	common, err := canonicalCleanupPath(path)
	return checkoutGitProbe{common: common, err: err}
}

func checkoutGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	env := make([]string, 0)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") && !strings.HasPrefix(entry, "LC_ALL=") {
			env = append(env, entry)
		}
	}
	cmd.Env = append(env, "GIT_OPTIONAL_LOCKS=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "LC_ALL=C")
	var out, stderr checkoutGitOutput
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out.Bytes(), nil
}

type checkoutGitOutput struct{ bytes.Buffer }

func (b *checkoutGitOutput) Write(data []byte) (int, error) {
	if b.Len()+len(data) > 8<<20 {
		return 0, errors.New("exceeded Git inventory output limit")
	}
	return b.Buffer.Write(data)
}

func checkoutGitWorktrees(ctx context.Context, dir string) ([]checkoutGitRecord, error) {
	raw, err := checkoutGit(ctx, dir, "worktree", "list", "--porcelain", "-z")
	if err == nil {
		return parseCheckoutGit(raw, 0)
	}
	if !strings.Contains(err.Error(), "unknown switch \x60z'") {
		return nil, err
	}
	// Git 2.34 supports porcelain without NUL delimiters. Reject ambiguous
	// multiline output from older versions rather than guessing a path.
	raw, err = checkoutGit(ctx, dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return parseCheckoutGit(raw, '\n')
}

func parseCheckoutGit(raw []byte, delimiter byte) ([]checkoutGitRecord, error) {
	var out []checkoutGitRecord
	separator := []byte{delimiter, delimiter}
	if len(raw) == 0 || !bytes.HasSuffix(raw, separator) {
		return nil, errors.New("missing or incomplete Git worktree registrations")
	}
	if bytes.Count(raw, separator) > 1024 {
		return nil, errors.New("too many registered Git worktrees")
	}
	paths := make(map[string]bool)
	for _, record := range bytes.Split(raw[:len(raw)-2], separator) {
		parts := strings.Split(string(record), string(delimiter))
		path, hasPath := strings.CutPrefix(parts[0], "worktree ")
		if delimiter == '\n' && strings.HasPrefix(path, "\"") {
			decoded, err := strconv.Unquote(path)
			if err != nil {
				return nil, errors.New("ambiguous quoted Git worktree path")
			}
			path = decoded
		}
		if !hasPath || !filepath.IsAbs(path) || paths[path] {
			return nil, errors.New("invalid Git worktree path")
		}
		paths[path] = true
		row := checkoutGitRecord{path: path, kind: "linked"}
		if len(out) == 0 {
			row.kind = "primary"
		}
		fields := make(map[string]bool)
		for _, field := range parts[1:] {
			key, value, _ := strings.Cut(field, " ")
			if fields[key] {
				return nil, fmt.Errorf("duplicate Git worktree field %q", key)
			}
			fields[key] = true
			switch key {
			case "bare":
				row.kind = "bare"
			case "HEAD":
				row.head = value
			case "branch":
				row.branch = value
			case "locked":
				row.locked = true
			case "prunable":
				row.prunable = true
			case "detached":
			default:
				return nil, fmt.Errorf("unrecognized Git worktree field %q", key)
			}
		}
		if row.kind != "bare" && (!fields["HEAD"] || fields["detached"] && fields["branch"]) {
			return nil, errors.New("incomplete Git worktree registration")
		}
		out = append(out, row)
	}
	return out, nil
}
