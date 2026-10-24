package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/diagnosticlog"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/skeleton"
)

const cleanPreserved = "Histories, credentials, projects, handoff inputs, session metadata, replay/idempotency state, recovery receipts, unknown files and shared caches are preserved. Hook cleanup requires the configured daemon to be stopped. Allocated-byte changes are observations, not guaranteed free space."

var errCleanUnsafe = errors.New("ownership or identity is unproven")

type cleanSize struct {
	Logical   int64 `json:"logical_bytes"`
	Allocated int64 `json:"allocated_bytes"`
}
type cleanItem struct {
	Path          string     `json:"path"`
	Kind          string     `json:"kind"`
	Status        string     `json:"status"`
	Reason        string     `json:"reason"`
	Before        *cleanSize `json:"before,omitempty"`
	After         *cleanSize `json:"after,omitempty"`
	EligibleBytes int64      `json:"eligible_logical_bytes"`
	Delta         *int64     `json:"observed_allocated_reduction_bytes,omitempty"`
}
type cleanReport struct {
	Mode      string      `json:"mode"`
	Complete  bool        `json:"complete"`
	Items     []cleanItem `json:"items"`
	Preserved string      `json:"preserved"`
}

func runClean(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("clean", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apply := fs.Bool("apply", false, "apply proven cleanup (default is preview)")
	asJSON := fs.Bool("json", false, "print results as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "clean: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	cc, err := clientConfig()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "clean: %v\n", err)
		return 1
	}
	report := collectClean(cc, *apply, 10000, time.Now().Add(2*time.Second))
	// Collection releases all locks before a slow output consumer can block.
	if *asJSON {
		err = writeJSON(stdout, report)
	} else {
		_, err = fmt.Fprintf(stdout, "Storage cleanup %s (complete=%t)\n", report.Mode, report.Complete)
		for _, item := range report.Items {
			if err != nil {
				break
			}
			_, err = fmt.Fprintf(stdout, "%s %s %q: %q; eligible logical bytes=%d; before %s; after %s", item.Status, item.Kind, item.Path, item.Reason, item.EligibleBytes, cleanSizeText(item.Before), cleanSizeText(item.After))
			if err == nil && item.Delta != nil {
				_, err = fmt.Fprintf(stdout, "; observed allocated reduction=%d bytes", *item.Delta)
			}
			if err == nil {
				_, err = fmt.Fprintln(stdout)
			}
		}
		if err == nil {
			_, err = fmt.Fprintln(stdout, report.Preserved)
		}
		if err == nil && !*apply {
			_, err = fmt.Fprintln(stdout, "Use swarm clean --apply to revalidate and apply eligible cleanup.")
		}
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "clean: write output: %v\n", err)
		return 1
	}
	if !report.Complete {
		return 1
	}
	for _, item := range report.Items {
		if item.Status == "error" {
			return 1
		}
	}
	return 0
}

func cleanSizeText(size *cleanSize) string {
	if size == nil {
		return "unknown"
	}
	return fmt.Sprintf("allocated=%d logical=%d bytes", size.Allocated, size.Logical)
}

func cleanBytes(info os.FileInfo) *cleanSize {
	if info == nil {
		return nil
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Blocks < 0 {
		return nil
	}
	return &cleanSize{Logical: info.Size(), Allocated: st.Blocks * 512}
}

func cleanOutcome(path, kind string, before, after os.FileInfo, eligible int64, changed, apply bool, err error) cleanItem {
	item := cleanItem{Path: path, Kind: kind, Status: "unchanged", Reason: "no eligible bytes", Before: cleanBytes(before), After: cleanBytes(after), EligibleBytes: eligible}
	if eligible > 0 {
		item.Status = "eligible"
		item.Reason = "proven cleanup candidate"
	}
	if apply && changed {
		item.Status = "cleaned"
		item.Reason = "retention applied"
	}
	if err != nil {
		item.Status = "error"
		item.Reason = err.Error()
		switch {
		case changed:
			item.Reason = "cleanup changed bytes before failing: " + err.Error()
		case errors.Is(err, os.ErrNotExist):
			item.Status = "retained"
			item.Reason = "target or required proof is absent"
		case errors.Is(err, errCleanUnsafe), errors.Is(err, diagnosticlog.ErrUnsafe), errors.Is(err, shim.ErrHookProducerBusy), errors.Is(err, shim.ErrHookRetirementUnproven), errors.Is(err, shim.ErrHookCleanupTooLarge):
			item.Status = "retained"
		}
		item.EligibleBytes = 0
	}
	if item.Before != nil && item.After != nil {
		delta := item.Before.Allocated - item.After.Allocated
		item.Delta = &delta
	}
	return item
}

func collectClean(cc daemon.ClientConfig, apply bool, maxEntries int, deadline time.Time) (report cleanReport) {
	report = cleanReport{Mode: "preview", Complete: true, Items: []cleanItem{}, Preserved: cleanPreserved}
	defer func() {
		for _, item := range report.Items {
			if item.Status == "error" {
				report.Complete = false
			}
		}
	}()
	if apply {
		report.Mode = "apply"
	}
	logResult, logErr := diagnosticlog.CleanExisting(cc.LogPath, apply)
	report.Items = append(report.Items, cleanOutcome(cc.LogPath, "diagnostic-log", logResult.Before, logResult.After, logResult.EligibleBytes, logResult.Changed, apply, logErr))
	rootInfo, err := cleanDirectory(cc.StateDir, nil)
	if errors.Is(err, os.ErrNotExist) {
		return report
	}
	if err != nil {
		report.Complete = false
		report.Items = append(report.Items, cleanOutcome(cc.StateDir, "inventory", nil, nil, 0, false, apply, err))
		return report
	}
	root, err := os.OpenFile(cc.StateDir, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		report.Complete = false
		report.Items = append(report.Items, cleanOutcome(cc.StateDir, "inventory", nil, nil, 0, false, apply, err))
		return report
	}
	defer func() { _ = root.Close() }()
	opened, err := root.Stat()
	if err != nil || !os.SameFile(rootInfo, opened) {
		report.Complete = false
		report.Items = append(report.Items, cleanOutcome(cc.StateDir, "inventory", nil, nil, 0, false, apply, errCleanUnsafe))
		return report
	}
	lock, lockErr := cleanSingleton(cc.LockPath)
	if lock != nil {
		defer func() { _ = lock.Close() }()
	}
	count := 0
	for {
		if count >= maxEntries || time.Now().After(deadline) {
			report.Complete = false
			report.Items = append(report.Items, cleanItem{Path: cc.StateDir, Kind: "inventory", Status: "error", Reason: "discovery limit reached; inventory is incomplete"})
			break
		}
		entries, readErr := root.ReadDir(min(128, maxEntries-count))
		for _, entry := range entries {
			count++
			if time.Now().After(deadline) {
				report.Complete = false
				report.Items = append(report.Items, cleanItem{Path: cc.StateDir, Kind: "inventory", Status: "error", Reason: "discovery deadline reached; inventory is incomplete"})
				break
			}
			dir := filepath.Join(cc.StateDir, entry.Name())
			if entry.Type()&os.ModeSymlink != 0 {
				report.Items = append(report.Items, cleanOutcome(dir, "directory", nil, nil, 0, false, apply, errCleanUnsafe))
				continue
			}
			if !entry.IsDir() {
				continue
			}
			path := filepath.Join(dir, shim.HookSpoolFile)
			if _, err := cleanDirectory(cc.StateDir, rootInfo); err != nil {
				report.Complete = false
				report.Items = append(report.Items, cleanOutcome(cc.StateDir, "inventory", nil, nil, 0, false, apply, err))
				break
			}
			dirInfo, err := cleanDirectory(dir, nil)
			if err != nil {
				report.Items = append(report.Items, cleanOutcome(dir, "directory", nil, nil, 0, false, apply, err))
				continue
			}
			info, err := os.Lstat(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				report.Items = append(report.Items, cleanOutcome(path, "hook-prefix", nil, nil, 0, false, apply, err))
				continue
			}
			if lockErr != nil {
				report.Items = append(report.Items, cleanOutcome(path, "hook-prefix", info, nil, 0, false, apply, lockErr))
				continue
			}
			// A compaction must never replace the pathname carrying singleton custody.
			lockParent, lockParentErr := os.Lstat(filepath.Dir(cc.LockPath))
			lockReal, lockResolveErr := filepath.EvalSymlinks(cc.LockPath)
			dirReal, dirResolveErr := filepath.EvalSymlinks(dir)
			lockAbs, _ := filepath.Abs(lockReal)
			dirAbs, _ := filepath.Abs(dirReal)
			rel, err := filepath.Rel(dirAbs, lockAbs)
			if lockParentErr != nil || os.SameFile(lockParent, dirInfo) || lockResolveErr != nil || dirResolveErr != nil || err != nil || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
				report.Items = append(report.Items, cleanOutcome(path, "hook-prefix", info, nil, 0, false, apply, fmt.Errorf("%w: singleton is inside session directory", errCleanUnsafe)))
				continue
			}
			fence := func() error {
				if _, err := cleanDirectory(cc.StateDir, rootInfo); err != nil {
					return err
				}
				if _, err := cleanDirectory(dir, dirInfo); err != nil {
					return err
				}
				return cleanFileIdentity(cc.LockPath, lock)
			}
			item := cleanHook(path, entry.Name(), fence, apply)
			if item.Before == nil && info.Mode().IsRegular() {
				item.Before = cleanBytes(info)
			}
			report.Items = append(report.Items, item)
		}
		if !report.Complete {
			break
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			report.Complete = false
			report.Items = append(report.Items, cleanOutcome(cc.StateDir, "inventory", nil, nil, 0, false, apply, readErr))
			break
		}
	}
	return report
}

func cleanHook(path, id string, fence func() error, apply bool) cleanItem {
	fail := func(err error) cleanItem { return cleanOutcome(path, "hook-prefix", nil, nil, 0, false, apply, err) }
	if err := fence(); err != nil {
		return fail(err)
	}
	data, err := cleanRead(filepath.Join(filepath.Dir(path), "meta.json"), 1<<20)
	if err != nil {
		return fail(err)
	}
	var meta persist.Meta
	if err := json.Unmarshal(data, &meta); err != nil || meta.ID != id || meta.ID == "" {
		return fail(fmt.Errorf("%w: invalid session metadata", errCleanUnsafe))
	}
	switch meta.SchemaVersion {
	case 0, persist.SchemaVersion, persist.OpenCodeSchemaVersion, persist.LoginSchemaVersion:
	default:
		return fail(fmt.Errorf("%w: unsupported session metadata schema", errCleanUnsafe))
	}
	incarnation, producer, seq, err := skeleton.ReadHookRetirementCertificate(filepath.Join(filepath.Dir(path), "hook.fold"))
	if err != nil {
		return fail(err)
	}
	result, err := shim.CleanStoppedHookSpool(path, incarnation, producer, seq, meta.ShimPID, meta.ShimStartTime, apply, fence)
	return cleanOutcome(path, "hook-prefix", result.Before, result.After, result.EligibleBytes, result.Changed, apply, err)
}

func cleanDirectory(path string, before os.FileInfo) (os.FileInfo, error) {
	if filepath.Clean(path) != path {
		return nil, fmt.Errorf("%w: noncanonical directory path", errCleanUnsafe)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || st.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0022 != 0 || (before != nil && !os.SameFile(before, info)) {
		return nil, fmt.Errorf("%w: unsafe or changed directory %q", errCleanUnsafe, path)
	}
	return info, nil
}

func cleanFileIdentity(path string, f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
		return errCleanUnsafe
	}
	current, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !current.Mode().IsRegular() || !os.SameFile(info, current) {
		return errCleanUnsafe
	}
	return nil
}

func cleanOpen(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENODEV) {
			return nil, errCleanUnsafe
		}
		return nil, err
	}
	if err := cleanFileIdentity(path, f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func cleanRead(path string, limit int64) ([]byte, error) {
	f, err := cleanOpen(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: metadata exceeds read limit", errCleanUnsafe)
	}
	return data, err
}

func cleanSingleton(path string) (*os.File, error) {
	if filepath.Clean(path) != path {
		return nil, fmt.Errorf("%w: noncanonical singleton path", errCleanUnsafe)
	}
	parent, err := cleanDirectory(filepath.Dir(path), nil)
	if err != nil {
		return nil, err
	}
	f, err := cleanOpen(path)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: daemon singleton is busy", errCleanUnsafe)
		}
		return nil, err
	}
	if _, err = cleanDirectory(filepath.Dir(path), parent); err == nil {
		err = cleanFileIdentity(path, f)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
