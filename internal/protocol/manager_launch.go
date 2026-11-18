package protocol

import (
	"errors"
	"github.com/Nathandela/swarm/internal/daemon"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"
)

const CapManagerLaunch = "manager-launch"

type ManagerLaunchSource interface{ ManagerLaunchSupported() bool }

func validateManagerLaunch(req LaunchReq) error {
	m := req.Manager
	if m == nil {
		return nil
	}
	if req.Agent != "codex" && req.Agent != "claude" {
		return errors.New("manager requires Codex or Claude")
	}
	if !daemon.ManagerNativeEnvironmentSupported(req.Agent, req.Env) {
		return errors.New("manager native account configuration is unsupported; select the native CLI account without credential or backend overrides and reconcile before launching")
	}
	if req.Agent == "claude" {
		seen := map[string]bool{}
		for _, entry := range req.Env {
			key, value, _ := strings.Cut(entry, "=")
			if key != "HOME" && key != "CLAUDE_CONFIG_DIR" {
				continue
			}
			if seen[key] {
				return errors.New("ambiguous manager native configuration")
			}
			seen[key] = true
			if value != "" && (!filepath.IsAbs(value) || filepath.Clean(value) != value || strings.ContainsFunc(value, unicode.IsControl)) {
				return errors.New("manager native configuration requires canonical absolute paths")
			}
		}
	}
	if m.ConversationID == "" || len(m.ConversationID) > 128 || strings.TrimSpace(m.ConversationID) != m.ConversationID || strings.ContainsFunc(m.ConversationID, unicode.IsControl) {
		return errors.New("invalid manager conversation identity")
	}
	if mode := req.Options["sandbox"]; mode != "" && mode != "danger-full-access" {
		return errors.New("manager requires full native tool access")
	}
	if mode := req.Options["dangerously-skip-permissions"]; mode != "" && mode != "true" {
		return errors.New("manager requires full native tool access")
	}
	for _, item := range []struct {
		path   string
		socket bool
	}{{m.BridgeSocket, true}, {m.AuthFile, false}} {
		path := item.path
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(path) > 4096 || strings.ContainsFunc(path, unicode.IsControl) {
			return errors.New("manager bridge requires canonical private paths")
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || resolved != path {
			return errors.New("manager bridge path unavailable or aliased")
		}
		parent, err := os.Lstat(filepath.Dir(path))
		if err != nil || !parent.IsDir() || parent.Mode().Perm() != 0700 {
			return errors.New("manager bridge directory must be private")
		}
		parentOwner, ok := parent.Sys().(*syscall.Stat_t)
		if !ok || parentOwner.Uid != uint32(os.Getuid()) {
			return errors.New("manager bridge directory has another owner")
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			return errors.New("manager bridge custody must have mode 0600")
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != uint32(os.Getuid()) || owner.Nlink != 1 {
			return errors.New("manager bridge custody has invalid ownership")
		}
		if item.socket {
			if info.Mode()&os.ModeSocket == 0 {
				return errors.New("manager bridge endpoint must be a socket")
			}
		} else if !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 4096 {
			return errors.New("manager bridge auth file must be a bounded private file")
		}
	}
	return nil
}
