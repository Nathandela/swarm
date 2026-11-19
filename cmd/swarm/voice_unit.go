package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/voice"
)

const (
	voiceLinuxUnit = "swarm-voice.service"
	voiceMacLabel  = "com.swarm.voice"
	// launchctl print uses the bootstrap error code for an absent service.
	launchServiceAbsent = 113
)

type voiceUnitRunner func(name string, args ...string) error

func dispatchVoiceUnit(command string, args []string, stdout, stderr io.Writer) int {
	if command != "start" && command != "stop" {
		return misuseExit
	}
	cc, err := clientConfig()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	flags := flag.NewFlagSet("voice "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&cc.StateDir, "state-dir", cc.StateDir, "Swarm base state directory")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return misuseExit
	}
	if flags.NArg() != 0 || !filepath.IsAbs(cc.StateDir) {
		_, _ = fmt.Fprintln(stderr, "voice: expected only flags and an absolute --state-dir")
		return misuseExit
	}
	home, err := os.UserHomeDir()
	if err == nil {
		cc.StateDir = filepath.Clean(cc.StateDir)
		err = manageVoiceUnit(command, runtime.GOOS, home, cc.DaemonBin, cc.StateDir, runVoiceUnit, func(cfg voice.Config) error {
			if testing.Testing() {
				return errors.New("voice: refusing a real supervisor start inside a test binary")
			}
			return ensureVoiceDaemon(cc, cfg, false, daemon.Dial, daemon.EnsureDaemon)
		})
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if command == "start" {
		_, _ = fmt.Fprintln(stdout, "Voice service enabled. Run swarm voice status to check readiness.")
	} else {
		_, _ = fmt.Fprintln(stdout, "Voice service stopped.")
	}
	return 0
}

func manageVoiceUnit(command, goos, home, binary, stateDir string, run voiceUnitRunner, prepare func(voice.Config) error) error {
	start := command == "start"
	path, err := voice.UnitPath(goos, home)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(stateDir) || filepath.Clean(stateDir) != stateDir {
		return errors.New("voice: invalid base state directory")
	}
	dir := filepath.Join(stateDir, "voice")
	var config voice.Config
	if start {
		config, err = voice.LoadConfig(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return errors.New("voice: manager setup is missing; run swarm voice setup --manager codex (or claude) first")
			}
			return err
		}
	}
	unitDir := filepath.Dir(path)
	if err := checkVoiceUnitDir(home, unitDir, start); err != nil {
		if !start && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	lock, err := openVoiceUnitFile(filepath.Join(unitDir, ".swarm-voice.lock"), os.O_CREATE|os.O_RDWR, true)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("voice: another service operation is in progress")
	}

	installed, err := readVoiceUnit(path)
	newUnit := errors.Is(err, os.ErrNotExist)
	if err != nil && !newUnit {
		return err
	}
	if !start && newUnit {
		return nil
	}
	marker := voiceUnitMarker(goos, stateDir)
	if !newUnit && !bytes.HasSuffix(installed, []byte(marker)) {
		return errors.New("voice: service unit is unmanaged or belongs to another state directory")
	}

	domain := "gui/" + strconv.Itoa(os.Getuid())
	target := domain + "/" + voiceMacLabel
	macLoaded := false
	if goos == "darwin" {
		err := run("launchctl", "print", target)
		macLoaded = err == nil
		var exit interface{ ExitCode() int }
		if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != launchServiceAbsent) {
			return err
		}
		if newUnit && macLoaded {
			return errors.New("voice: an existing launchd job already uses the voice service label")
		}
	}
	if !start {
		switch goos {
		case "darwin":
			if macLoaded {
				if err := run("launchctl", "bootout", target); err != nil {
					return err
				}
			}
		case "linux":
			if err := run("systemctl", "--user", "disable", "--now", voiceLinuxUnit); err != nil {
				return err
			}
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		if err := syncVoiceUnitDir(unitDir); err != nil {
			return err
		}
		if goos == "linux" {
			return run("systemctl", "--user", "daemon-reload")
		}
		return nil
	}

	logPath := filepath.Join(dir, "service.log")
	body, err := voice.RenderUnit(goos, binary, stateDir, logPath)
	if err != nil {
		return err
	}
	body += marker
	if !newUnit && string(installed) != body {
		return errors.New("voice: service unit differs from this installation; stop it before starting again")
	}
	log, err := openVoiceUnitFile(logPath, os.O_CREATE|os.O_WRONLY, true)
	if err != nil {
		return err
	}
	if err := log.Close(); err != nil {
		return err
	}
	if prepare != nil {
		if err := prepare(config); err != nil {
			return err
		}
	}
	if newUnit {
		if err := writeVoiceUnit(path, body); err != nil {
			return err
		}
	}
	switch goos {
	case "darwin":
		if !macLoaded {
			return run("launchctl", "bootstrap", domain, path)
		}
		return nil
	case "linux":
		if err := run("systemctl", "--user", "daemon-reload"); err != nil {
			return err
		}
		return run("systemctl", "--user", "enable", "--now", voiceLinuxUnit)
	}
	return errors.New("voice: unsupported service platform")
}

func ensureVoiceDaemon(cc daemon.ClientConfig, cfg voice.Config, unattended bool, dial func(string, int) (net.Conn, error), ensure func(daemon.ClientConfig) (net.Conn, error)) error {
	cc.SocketPath = cfg.DaemonSocket
	cc.LockPath = filepath.Join(cc.StateDir, "daemon.lock")
	cc.LogPath = filepath.Join(cc.StateDir, "daemon.log")
	return ensureManagerDaemon(cc, unattended, dial, ensure)
}

func ensureManagerDaemon(cc daemon.ClientConfig, unattended bool, dial func(string, int) (net.Conn, error), ensure func(daemon.ClientConfig) (net.Conn, error)) error {
	if unattended {
		conn, err := dial(cc.SocketPath, daemon.ProtocolVersion)
		if err == nil {
			return conn.Close()
		}
		if errors.Is(err, daemon.ErrVersionSkew) {
			return err
		}
		saved, err := daemon.LoadSavedEnv(cc.StateDir)
		if err != nil || len(saved) == 0 {
			return errors.New("no saved supervisor environment; start Swarm from the owner’s login shell")
		}
		cc.Env = saved
	}
	conn, err := ensure(cc)
	if err != nil {
		return err
	}
	return conn.Close()
}

func runVoiceUnit(name string, args ...string) error {
	if testing.Testing() {
		return errors.New("voice: refusing service-manager command inside a test binary")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if ctx.Err() != nil {
		return fmt.Errorf("voice: %s: %w", name, ctx.Err())
	}
	if err != nil {
		return fmt.Errorf("voice: %s: %w: %s", name, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func checkVoiceUnitDir(home, target string, create bool) error {
	relative, err := filepath.Rel(home, target)
	if err != nil {
		return err
	}
	path := home
	for _, part := range append([]string{"."}, strings.Split(relative, string(filepath.Separator))...) {
		if part == ".." {
			return errors.New("voice: unit directory is outside your home")
		}
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) && create && part != "." {
			if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err = os.Lstat(path)
		}
		if err != nil {
			return err
		}
		stat, owned := info.Sys().(*syscall.Stat_t)
		if !owned || stat.Uid != uint32(os.Getuid()) || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
			return errors.New("voice: unsafe service unit directory")
		}
	}
	return nil
}

func openVoiceUnitFile(path string, flags int, private bool) (*os.File, error) {
	file, err := os.OpenFile(path, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err == nil {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.Mode().IsRegular() || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 ||
			info.Mode().Perm()&0022 != 0 || (private && info.Mode().Perm() != 0600) {
			err = errors.New("voice: unsafe service file")
		}
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func readVoiceUnit(path string) ([]byte, error) {
	file, err := openVoiceUnitFile(path, os.O_RDONLY, false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	const limit = 64 << 10
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if len(data) > limit {
		return nil, errors.New("voice: service unit exceeds the size limit")
	}
	return data, err
}

func writeVoiceUnit(path, body string) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".swarm-voice-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err := file.WriteString(body); err != nil {
		_ = file.Close()
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return err
	}
	// Publish only to an absent path, even if a file appears after inspection.
	if err := os.Link(file.Name(), path); err != nil {
		return err
	}
	if err := os.Remove(file.Name()); err != nil {
		return err
	}
	return syncVoiceUnitDir(filepath.Dir(path))
}

func syncVoiceUnitDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

func requireVoiceUnitStopped(stateDir string) error {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path, err := voice.UnitPath(runtime.GOOS, home)
	if err != nil {
		return err
	}
	installed, err := readVoiceUnit(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if bytes.HasSuffix(installed, []byte(voiceUnitMarker(runtime.GOOS, stateDir))) {
		return errors.New("voice: run swarm voice stop before switching the installed service")
	}
	return nil
}

func voiceUnitMarker(goos, stateDir string) string {
	digest := sha256.Sum256([]byte(stateDir))
	if goos == "darwin" {
		return fmt.Sprintf("<!-- swarm-voice-state %x -->\n", digest)
	}
	return fmt.Sprintf("# swarm-voice-state %x\n", digest)
}
