package voice

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

func UnitPath(goos, home string) (string, error) {
	if !safeUnitPath(home) {
		return "", errors.New("voice: invalid home path")
	}
	switch goos {
	case "darwin":
		return filepath.Join(home, "Library/LaunchAgents/com.swarm.voice.plist"), nil
	case "linux":
		return filepath.Join(home, ".config/systemd/user/swarm-voice.service"), nil
	default:
		return "", errors.New("voice: unsupported service platform")
	}
}

func RenderUnit(goos, binary, stateDir, logPath string) (string, error) {
	for _, path := range []string{binary, stateDir, logPath} {
		if !safeUnitPath(path) {
			return "", errors.New("voice: invalid service path")
		}
	}
	switch goos {
	case "darwin":
		for _, path := range []string{binary, stateDir, logPath} {
			if !validPlistText(path) {
				return "", errors.New("voice: path cannot be represented in launchd plist")
			}
		}
		escape := func(s string) string {
			var b bytes.Buffer
			_ = xml.EscapeText(&b, []byte(s))
			return b.String()
		}
		return fmt.Sprintf("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n"+
			"<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n"+
			"<plist version=\"1.0\"><dict>\n"+
			"<key>Label</key><string>com.swarm.voice</string>\n"+
			"<key>ProgramArguments</key><array><string>%s</string><string>voice</string><string>serve</string><string>--state-dir</string><string>%s</string></array>\n"+
			"<key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>ThrottleInterval</key><integer>10</integer>\n"+
			"<key>StandardOutPath</key><string>%s</string><key>StandardErrorPath</key><string>%s</string>\n"+
			"</dict></plist>\n", escape(binary), escape(stateDir), escape(logPath), escape(logPath)), nil
	case "linux":
		if !safeSystemdExecutablePath(binary) {
			return "", errors.New("voice: invalid systemd executable path")
		}
		if strings.TrimRightFunc(logPath, unicode.IsSpace) != logPath || strings.HasSuffix(logPath, "\\") {
			return "", errors.New("voice: invalid log path")
		}
		quote := func(s string) string {
			s = strings.ReplaceAll(s, "%", "%%")
			s = strings.ReplaceAll(s, "\\", "\\\\")
			s = strings.ReplaceAll(s, "\"", "\\\"")
			return "\"" + s + "\""
		}
		log := strings.ReplaceAll(logPath, "%", "%%")
		// The leading colon disables systemd's $ expansion for caller-provided paths.
		return fmt.Sprintf("[Unit]\nDescription=Swarm Voice\nStartLimitIntervalSec=0\n[Service]\nExecStart=:%s voice serve --state-dir %s\nRestart=always\nRestartSec=10\nStandardOutput=append:%s\nStandardError=append:%s\n[Install]\nWantedBy=default.target\n", quote(binary), quote(stateDir), log, log), nil
	default:
		return "", errors.New("voice: unsupported service platform")
	}
}

func safeUnitPath(path string) bool {
	if !filepath.IsAbs(path) || !utf8.ValidString(path) {
		return false
	}
	for _, r := range path {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func safeSystemdExecutablePath(path string) bool {
	if !safeUnitPath(path) {
		return false
	}
	for _, r := range path {
		switch r {
		case '\\', '"', '\'', '*', '?', '[':
			return false
		}
	}
	return true
}

func validPlistText(path string) bool {
	for _, r := range path {
		if r == 0xfffe || r == 0xffff {
			return false
		}
	}
	return true
}
