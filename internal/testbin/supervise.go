package testbin

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const runRootEnv = "SWARM_TEST_RUN_ROOT"
const executableManifest = "executables"

// Supervise runs a private copy outside m.Run so panic and test timeout cannot
// bypass cleanup. Call after fake-agent dispatch, before builds and m.Run.
// The copied test executable and registered binaries are covered; arbitrary
// system executables and privileged/nondumpable processes still require recorded
// PID/start custody with stop/wait in their fixtures.
func Supervise() (int, bool) {
	if supervisedChild(os.Getenv(runRootEnv)) {
		return 0, false
	}
	root, err := os.MkdirTemp("/tmp", "s")
	if err != nil {
		fmt.Fprintln(os.Stderr, "test supervisor:", err)
		return 1, true
	}
	code := 1
	err = runSupervised(root, &code)
	if err != nil {
		fmt.Fprintf(os.Stderr, "test supervisor: %v; retained scratch: %s\n", err, root)
		if code == 0 {
			code = 1
		}
	}
	return code, true
}

func runSupervised(root string, code *int) error {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	src, err := os.Open(self)
	if err != nil {
		return err
	}
	defer src.Close()
	exe := filepath.Join(root, "test")
	dst, err := os.OpenFile(exe, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(dst, src)
	if err := errors.Join(copyErr, dst.Close()); err != nil {
		return err
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), runRootEnv+"="+root, "TMPDIR="+root)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupts)
	runErr := runChild(cmd, interrupts)
	if runErr == nil {
		*code = 0
	} else if exit, ok := runErr.(*exec.ExitError); ok {
		*code = exit.ExitCode()
		if *code < 0 {
			*code = 1
		}
	} else {
		fmt.Fprintln(os.Stderr, "test supervisor child:", runErr)
	}
	currentRoot, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !os.SameFile(rootInfo, currentRoot) || !currentRoot.IsDir() {
		return errors.New("test run root was replaced")
	}
	exes, err := registeredExecutables(root)
	if err != nil {
		return err
	}
	ownedTest, err := ownedExecutable(root, exe)
	if err != nil {
		return err
	}
	exes = append(exes, ownedTest)
	return cleanupRun(root, exes, killCanonicalProcesses, os.RemoveAll)
}

// RegisterExecutable gives the parent ownership of a run-built binary. Register
// before building, so even a test panic during its first invocation is covered.
func RegisterExecutable(path string) error {
	root := os.Getenv(runRootEnv)
	if root == "" {
		return nil
	}
	path, err := ownedExecutable(root, path)
	if err != nil {
		return err
	}
	file, err := openManifest(root, os.O_CREATE|os.O_APPEND|os.O_WRONLY)
	if err != nil {
		return err
	}
	_, err = file.WriteString(path + "\n")
	return errors.Join(err, file.Close())
}

func registeredExecutables(root string) ([]string, error) {
	file, err := openManifest(root, os.O_RDONLY)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var exes []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		path, err := ownedExecutable(root, scanner.Text())
		if err != nil {
			return nil, err
		}
		exes = append(exes, path)
	}
	return exes, scanner.Err()
}

func openManifest(root string, flags int) (*os.File, error) {
	file, err := os.OpenFile(filepath.Join(root, executableManifest), flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 || info.Size() > 1<<20 {
		_ = file.Close()
		return nil, errors.New("unsafe test executable manifest")
	}
	return file, nil
}

// Keep scratch as evidence whenever stopping or removing owned resources fails.
func cleanupRun(root string, exes []string, stop func(...string) error, remove func(string) error) error {
	if err := stop(exes...); err != nil {
		return err
	}
	return remove(root)
}

func ownedExecutable(root, path string) (string, error) {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if os.IsNotExist(err) {
		parent, parentErr := filepath.EvalSymlinks(filepath.Dir(path))
		if parentErr != nil {
			// Test cleanup may already have removed an entire registered build directory.
			// The manifest stores canonical absolute paths; preserve those missing paths.
			if !os.IsNotExist(parentErr) {
				return "", parentErr
			}
			resolved = filepath.Clean(path)
		} else {
			resolved = filepath.Join(parent, filepath.Base(path))
		}
	} else if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(realRoot, resolved)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || strings.ContainsAny(resolved, "\r\n") {
		return "", fmt.Errorf("test executable outside run root: %q", path)
	}
	return resolved, nil
}

func supervisedChild(root string) bool {
	if root == "" {
		return false
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return false
	}
	self, err := os.Executable()
	if err != nil {
		return false
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return false
	}
	expected, err := ownedExecutable(root, filepath.Join(root, "test"))
	if err != nil {
		return false
	}
	if self == expected {
		return true
	}
	paths, err := registeredExecutables(root)
	if err != nil {
		return false
	}
	for _, path := range paths {
		if path == self {
			return true
		}
	}
	return false
}

func runChild(cmd *exec.Cmd, interrupts <-chan os.Signal) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err := <-exited:
		return err
	case sig := <-interrupts:
		if err := cmd.Process.Signal(sig); err != nil && !errors.Is(err, os.ErrProcessDone) {
			fmt.Fprintln(os.Stderr, "test supervisor interrupt:", err)
		}
		select {
		case err := <-exited:
			return err
		case <-time.After(time.Second):
			if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				fmt.Fprintln(os.Stderr, "test supervisor kill child:", err)
			}
			return <-exited
		}
	}
}
