// Package accountcheck owns short-lived, private native checks in a dedicated
// re-exec process. The daemon never becomes a subreaper. A crash without the
// exact durable stopped proof leaves custody unknown and never grants capacity.
package accountcheck

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
)

const SchemaVersion = 2
const ModeAuthStatus = "auth-status"
const ModeAvailability = "availability"
const WorkerFile = "worker.json"
const StoppedFile = "stopped.json"

var ErrUnavailable = errors.New("account check unavailable")
var ErrCustodyUnknown = errors.New("account check writer custody is unknown")

type Config struct {
	StateRoot        string
	Binding          accounts.Binding
	CLI              persist.CLIIdentity
	Mode, Model, Cwd string
	Env              []string // anonymous admission pipe only, never written to disk
	Deadline         time.Time
}

type Ref struct {
	SchemaVersion int                     `json:"schema_version"`
	Generation    string                  `json:"generation"`
	Worker        processcontain.Identity `json:"worker"`
	Binding       accounts.Binding        `json:"binding"`
	Mode          string                  `json:"mode"`
}

type Stopped struct {
	SchemaVersion  int                     `json:"schema_version"`
	Ref            Ref                     `json:"ref"`
	Native         processcontain.Identity `json:"native"`
	WritersStopped bool                    `json:"writers_stopped"`
	ExitOK         bool                    `json:"exit_ok"`
}

type boundedOutput struct {
	data     []byte
	maximum  int
	exceeded bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	room := b.maximum - len(b.data)
	if len(p) > room {
		b.exceeded = true
		p = p[:room]
	}
	b.data = append(b.data, p...)
	return n, nil
}

// Run starts a blocked owned worker, durably records its creation identity,
// and lets admit persist/revalidate authority before any native exec is allowed.
func Run(ctx context.Context, executable string, cfg Config, admit func(Ref) error) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := openChecks(cfg.StateRoot)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	// Shared with ordinary native configuration initialization.
	lock, err := acquireGenerationLock(root, cfg.Binding)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Close() }()
	if !collectionStateSafe(cfg.StateRoot) || !writersStopped(root, cfg.Binding) {
		return nil, ErrCustodyUnknown
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	generation := hex.EncodeToString(nonce[:])
	if err := root.Mkdir(generation, 0o700); err != nil {
		return nil, err
	}
	if err := syncRoot(root); err != nil {
		_ = root.Remove(generation)
		return nil, err
	}
	files, err := openGeneration(root, generation)
	if err != nil {
		return nil, err
	}
	defer func() { _ = files.Close() }()
	path := filepath.Join(cfg.StateRoot, "accounts", "checks", generation)
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()
	cmd := exec.Command(executable, "internal", "account-check", path)
	cmd.Stdin = reader
	// The child receives selected auth only through the anonymous pipe; its own
	// process environment contains no native selectors or inherited SWARM_ keys.
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		switch key {
		case "HOME", "USER", "LOGNAME", "PATH", "LANG", "LC_ALL", "LC_CTYPE", "TERM", "TZ":
			cmd.Env = append(cmd.Env, item)
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out := &boundedOutput{maximum: 64 << 10}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		_ = root.Remove(generation)
		_ = syncRoot(root)
		return nil, ErrUnavailable
	}
	start, err := procstart.StartTime(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, ErrCustodyUnknown
	}
	ref := Ref{SchemaVersion: SchemaVersion, Generation: generation, Worker: processcontain.Identity{PID: cmd.Process.Pid, StartTime: start}, Binding: cfg.Binding, Mode: cfg.Mode}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	admissionErr := writeJSON(files, WorkerFile, ref)
	if admissionErr == nil && admit != nil {
		admissionErr = admit(ref)
	}
	admitted := false
	if admissionErr == nil {
		if ctx.Err() != nil {
			admissionErr = ctx.Err()
		} else {
			admissionErr = json.NewEncoder(writer).Encode(cfg)
			admitted = admissionErr == nil
		}
	}
	_ = writer.Close()
	_ = reader.Close()

	var waitErr error
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		if admitted {
			_ = processcontain.SignalIdentity(ref.Worker, syscall.SIGTERM)
		}
		select {
		case waitErr = <-done:
		case <-time.After(5 * time.Second):
			_ = processcontain.SignalIdentity(ref.Worker, syscall.SIGKILL)
			waitErr = <-done
		}
	}
	var proof Stopped
	if readJSON(files, StoppedFile, &proof) != nil || proof.SchemaVersion != SchemaVersion || proof.Ref != ref || !proof.WritersStopped {
		return nil, ErrCustodyUnknown
	}
	if admissionErr != nil {
		return nil, admissionErr
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if waitErr != nil || !proof.ExitOK || out.exceeded {
		return nil, ErrUnavailable
	}
	return append([]byte(nil), out.data...), nil
}

// CustodyStopped checks the exact owner incarnation from a persisted permit.
// A missing or foreign proof never becomes true through PID/PGID disappearance.
func CustodyStopped(stateRoot string, worker processcontain.Identity, binding accounts.Binding) bool {
	if !collectionStateSafe(stateRoot) {
		return false
	}
	root, err := openChecks(stateRoot)
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	directory, err := root.Open(".")
	if err != nil {
		return false
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(4097)
	if err != nil && !errors.Is(err, io.EOF) || len(entries) > 4096 {
		return false
	}
	for _, entry := range entries {
		if !validGeneration(entry.Name()) || !entry.IsDir() {
			continue
		}
		files, err := openGeneration(root, entry.Name())
		if err != nil {
			return false
		}
		var ref Ref
		var proof Stopped
		valid := readJSON(files, WorkerFile, &ref) == nil && ref.SchemaVersion == SchemaVersion && ref.Generation == entry.Name() && ref.Worker == worker && ref.Binding == binding && readJSON(files, StoppedFile, &proof) == nil && proof.SchemaVersion == SchemaVersion && proof.Ref == ref && proof.WritersStopped
		_ = files.Close()
		if valid {
			return !worker.Alive()
		}
	}
	return false
}

// WritersStoppedForBinding refuses transfer while any retained check for this
// credential generation lacks an exact stopped-writer proof. A genuinely absent
// check inventory is safe; malformed or unsafe custody fails closed.
func WritersStoppedForBinding(stateRoot string, binding accounts.Binding) bool {
	if !collectionStateSafe(stateRoot) {
		return false
	}
	root, err := openCheckRoot(stateRoot, false)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	return writersStopped(root, binding)
}

// writersStopped covers every owned check sharing the retained credential
// generation and refuses unreadable or malformed custody.
func writersStopped(root *os.Root, binding accounts.Binding) bool {
	directory, err := root.Open(".")
	if err != nil {
		return false
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(4097)
	if (err != nil && !errors.Is(err, io.EOF)) || len(entries) > 4096 {
		return false
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".lock-") {
			key, decodeErr := hex.DecodeString(strings.TrimPrefix(entry.Name(), ".lock-"))
			info, statErr := root.Lstat(entry.Name())
			if decodeErr != nil || len(key) != 32 || statErr != nil || !validLockInfo(info) {
				return false
			}
			continue
		}
		if !validGeneration(entry.Name()) || !entry.IsDir() {
			return false
		}
		files, err := openGeneration(root, entry.Name())
		if err != nil {
			return false
		}
		var ref Ref
		var proof Stopped
		if readJSON(files, WorkerFile, &ref) != nil || ref.SchemaVersion != SchemaVersion || ref.Generation != entry.Name() {
			_ = files.Close()
			return false
		}
		same := ref.Binding.Provider == binding.Provider && ref.Binding.AccountID == binding.AccountID && ref.Binding.CredentialGeneration == binding.CredentialGeneration
		if same {
			stopped := !ref.Worker.Alive() && readJSON(files, StoppedFile, &proof) == nil && proof.SchemaVersion == SchemaVersion && proof.Ref == ref && proof.WritersStopped
			_ = files.Close()
			if !stopped {
				return false
			}
		} else {
			_ = files.Close()
		}
	}
	return true
}

func openGeneration(root *os.Root, generation string) (*os.Root, error) {
	before, err := root.Lstat(generation)
	if err != nil || !before.IsDir() || before.Mode().Perm() != 0o700 {
		return nil, ErrUnavailable
	}
	owner, ok := before.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Getuid()) {
		return nil, ErrUnavailable
	}
	files, err := root.OpenRoot(generation)
	if err != nil {
		return nil, err
	}
	directory, err := files.Open(".")
	if err != nil {
		_ = files.Close()
		return nil, err
	}
	after, err := directory.Stat()
	_ = directory.Close()
	if err != nil || !os.SameFile(before, after) {
		_ = files.Close()
		return nil, ErrUnavailable
	}
	return files, nil
}

func validLockInfo(info os.FileInfo) bool {
	if info == nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != 0 {
		return false
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	return ok && owner.Uid == uint32(os.Getuid()) && owner.Nlink == 1
}

func validGeneration(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 16 && value == strings.ToLower(value)
}

func generationLockName(binding accounts.Binding) string {
	key := sha256.Sum256([]byte(binding.Provider + ":" + binding.AccountID + ":" + fmt.Sprint(binding.CredentialGeneration)))
	return ".lock-" + hex.EncodeToString(key[:])
}
func openChecks(stateRoot string) (*os.Root, error) {
	return openCheckRoot(stateRoot, true)
}

func openCheckRoot(stateRoot string, create bool) (*os.Root, error) {
	if !filepath.IsAbs(stateRoot) {
		return nil, ErrUnavailable
	}
	// The account store validates the private anchor and account directories.
	store, err := accounts.Open(stateRoot)
	if err != nil {
		return nil, ErrUnavailable
	}
	_ = store.Close()
	state, err := os.OpenRoot(stateRoot)
	if err != nil {
		return nil, err
	}
	defer func() { _ = state.Close() }()
	if create {
		if err := state.Mkdir("accounts/checks", 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, err
		}
	} else if _, err := state.Lstat("accounts/checks"); err != nil {
		return nil, err
	}
	// Sync even an existing link: an earlier process may have crashed between
	// mkdir and its parent fsync.
	accountsRoot, err := state.OpenRoot("accounts")
	if err != nil {
		return nil, err
	}
	err = syncRoot(accountsRoot)
	_ = accountsRoot.Close()
	if err != nil {
		return nil, err
	}
	info, err := state.Lstat("accounts/checks")
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return nil, ErrUnavailable
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Getuid()) {
		return nil, ErrUnavailable
	}
	root, err := state.OpenRoot("accounts/checks")
	if err != nil {
		return nil, err
	}
	after, err := root.Lstat(".")
	if err != nil || !os.SameFile(info, after) {
		_ = root.Close()
		return nil, ErrUnavailable
	}
	return root, nil
}

func readJSON(root *os.Root, name string, value any) error {
	before, err := root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0o600 {
		return ErrUnavailable
	}
	owner, ok := before.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Getuid()) || owner.Nlink != 1 || before.Size() > 16<<10 {
		return ErrUnavailable
	}
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) {
		return ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(file, 16<<10))
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, value)
}

func writeJSON(root *os.Root, name string, value any) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	temp := ".check-" + hex.EncodeToString(nonce[:])
	file, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close(); _ = root.Remove(temp) }()
	if err := json.NewEncoder(file).Encode(value); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := root.Rename(temp, name); err != nil {
		return err
	}
	return syncRoot(root)
}

func syncRoot(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
