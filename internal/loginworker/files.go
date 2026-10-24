package loginworker

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/Nathandela/swarm/internal/codexnative"
	"github.com/Nathandela/swarm/internal/persist"
)

type jobFiles struct {
	root   *os.Root
	path   string
	anchor os.FileInfo
}

func validID(id string) bool {
	_, err := hex.DecodeString(id)
	return len(id) == 32 && err == nil && strings.ToLower(id) == id
}

func safeAbsolute(path string, directory bool, private bool) (os.FileInfo, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrUnsafe
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	current := "/"
	var info os.FileInfo
	for i, part := range parts {
		current = filepath.Join(current, part)
		var err error
		info, err = os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || ((i < len(parts)-1 || directory) && !info.IsDir()) {
			return nil, ErrUnsafe
		}
		if i == len(parts)-1 && private && !privateInfo(info, directory) {
			return nil, ErrUnsafe
		}
	}
	return info, nil
}

func privateInfo(info os.FileInfo, directory bool) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0o077 != 0 {
		return false
	}
	if directory {
		return info.IsDir()
	}
	return info.Mode().IsRegular() && stat.Nlink == 1
}

// ValidateNativePath checks the resolved executable using the same permissions
// required at launch. Callers must resolve installation aliases first.
func ValidateNativePath(path string) error {
	info, err := safeAbsolute(path, false, false)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return ErrUnsafe
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (stat.Uid != 0 && stat.Uid != uint32(os.Getuid())) || info.Mode().Perm()&0o022 != 0 {
		return ErrUnsafe
	}
	return nil
}

func validateStart(c Config) error {
	if c.SchemaVersion != SchemaVersion || !validID(c.JobID) || c.Generation == 0 || c.NativeVersion == "" {
		return ErrInvalid
	}
	if !validLabel(c.Label) || (c.LoginID != "" && !validID(c.LoginID)) {
		return ErrInvalid
	}
	if (c.Provider == "codex" && c.Method != MethodDeviceCode) || (c.Provider == "claude" && c.Method != MethodNativeLogin) || (c.Provider != "codex" && c.Provider != "claude") {
		return ErrUnsupported
	}
	if c.Deadline.IsZero() || !c.Deadline.After(time.Now()) || c.Deadline.After(time.Now().Add(maxDeadline+time.Second)) {
		return ErrInvalid
	}
	if _, err := safeAbsolute(c.StateRoot, true, false); err != nil {
		return err
	}
	if err := ValidateNativePath(c.NativePath); err != nil {
		return err
	}
	if c.Provider == "codex" {
		installation, err := codexnative.Resolve(c.NativePath)
		if errors.Is(err, codexnative.ErrUnsafe) {
			return err
		}
		if err == nil && (installation.Path != c.NativePath || installation.Version != "" && installation.Version != c.NativeVersion) {
			return ErrUnsafe
		}
	}
	if c.NativeFingerprint == "" || !persist.MatchCLIFingerprint(c.NativePath, c.NativeFingerprint) {
		return ErrUnsafe
	}
	return nil
}

func validateConfig(c Config) error {
	if err := validateStart(c); err != nil {
		return err
	}
	for _, p := range []string{filepath.Join(c.StateRoot, "logins"), filepath.Join(c.StateRoot, "logins", "enrollment"), filepath.Join(c.StateRoot, "logins", "enrollment", c.JobID), NativeHome(c.StateRoot, c.JobID)} {
		if _, err := safeAbsolute(p, true, true); err != nil {
			return err
		}
	}
	return nil
}

func validLabel(label string) bool {
	return strings.TrimSpace(label) != "" && len(label) <= 128 && !strings.ContainsFunc(label, unicode.IsControl)
}

// NativeHome is private native authentication state, not a discussion context.
func NativeHome(stateRoot, jobID string) string {
	return filepath.Join(stateRoot, "logins", "enrollment", jobID, "native")
}

func prepareHome(c Config) error {
	if !validID(c.JobID) {
		return ErrInvalid
	}
	if _, err := safeAbsolute(c.StateRoot, true, false); err != nil {
		return err
	}
	for _, p := range []string{filepath.Join(c.StateRoot, "logins"), filepath.Join(c.StateRoot, "logins", "enrollment"), filepath.Join(c.StateRoot, "logins", "enrollment", c.JobID), NativeHome(c.StateRoot, c.JobID)} {
		err := os.Mkdir(p, 0700)
		if err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if _, err := safeAbsolute(p, true, true); err != nil {
			return err
		}
		parent, err := os.Open(filepath.Dir(p))
		if err != nil {
			return err
		}
		err = parent.Sync()
		_ = parent.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func openJob(stateRoot, jobID string, create bool) (*jobFiles, error) {
	if !validID(jobID) {
		return nil, ErrInvalid
	}
	loginsPath := filepath.Join(stateRoot, "logins")
	if _, err := safeAbsolute(loginsPath, true, true); err != nil {
		return nil, err
	}
	parentPath := filepath.Join(loginsPath, "enrollment")
	if _, err := safeAbsolute(parentPath, true, true); err != nil {
		return nil, err
	}
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		return nil, ErrUnsafe
	}
	defer func() { _ = parent.Close() }()
	if create {
		if err := parent.Mkdir(jobID, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, ErrUnsafe
		}
		dir, err := parent.Open(".")
		if err != nil {
			return nil, err
		}
		err = dir.Sync()
		_ = dir.Close()
		if err != nil {
			return nil, err
		}
	}
	path := filepath.Join(parentPath, jobID)
	anchor, err := safeAbsolute(path, true, true)
	if err != nil {
		return nil, err
	}
	root, err := parent.OpenRoot(jobID)
	if err != nil {
		return nil, ErrUnsafe
	}
	return &jobFiles{root: root, path: path, anchor: anchor}, nil
}

func (f *jobFiles) Close() { _ = f.root.Close() }

func (f *jobFiles) safe() error {
	info, err := safeAbsolute(f.path, true, true)
	if err != nil || !os.SameFile(info, f.anchor) {
		return ErrUnsafe
	}
	return nil
}

func readPrivate(root *os.Root, name string, limit int64) ([]byte, error) {
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !privateInfo(before, false) || before.Size() > limit {
		return nil, ErrUnsafe
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrUnsafe
	}
	defer func() { _ = f.Close() }()
	after, err := f.Stat()
	if err != nil || !privateInfo(after, false) || !os.SameFile(before, after) {
		return nil, ErrUnsafe
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, ErrUnsafe
	}
	current, err := root.Lstat(name)
	if err != nil || !os.SameFile(current, after) {
		return nil, ErrUnsafe
	}
	return raw, nil
}

func (f *jobFiles) write(name string, value any) error {
	if err := f.safe(); err != nil {
		return err
	}
	if info, err := f.root.Lstat(name); err == nil && !privateInfo(info, false) {
		return ErrUnsafe
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrUnsafe
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > 32<<10 {
		return ErrInvalid
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmpName := "." + name + "." + hex.EncodeToString(nonce[:]) + ".tmp"
	tmp, err := f.root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return ErrUnsafe
	}
	defer func() { _ = tmp.Close(); _ = f.root.Remove(tmpName) }()
	if _, err := tmp.Write(raw); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := f.root.Rename(tmpName, name); err != nil {
		return err
	}
	dir, err := f.root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

func ReadProgress(stateRoot, jobID string) (Progress, error) {
	f, err := openJob(stateRoot, jobID, false)
	if err != nil {
		return Progress{}, err
	}
	defer f.Close()
	raw, err := readPrivate(f.root, ProgressFile, 32<<10)
	if err != nil {
		return Progress{}, err
	}
	var progress Progress
	if json.Unmarshal(raw, &progress) != nil || progress.SchemaVersion != SchemaVersion || progress.JobID != jobID || progress.Generation == 0 || len(progress.Children) > 64 {
		return Progress{}, ErrInvalid
	}
	switch progress.Phase {
	case PhaseStarting, PhaseAuthenticating, PhaseVerifying, PhaseReady, PhaseCanceled, PhaseFailed:
	default:
		return Progress{}, ErrInvalid
	}
	return progress, nil
}

// FenceCancel is a daemon-owned durable terminal fence. Call after recording
// canonical cancellation and before contacting or killing a worker.
func FenceCancel(stateRoot, jobID string, generation uint64) error {
	if generation == 0 {
		return ErrInvalid
	}
	f, err := openJob(stateRoot, jobID, false)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.write(CancelFile, struct {
		Generation uint64 `json:"generation"`
	}{generation})
}

func (f *jobFiles) canceled(generation uint64) bool {
	if f.safe() != nil {
		return true
	}
	raw, err := readPrivate(f.root, CancelFile, 128)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	var fence struct {
		Generation uint64 `json:"generation"`
	}
	return err != nil || json.Unmarshal(raw, &fence) != nil || fence.Generation == 0 || fence.Generation >= generation
}
