package discord

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
	if !safePath(home) {
		return "", errors.New("discord: invalid home path")
	}
	switch goos {
	case "darwin":
		return filepath.Join(home, "Library/LaunchAgents/com.swarm.discord.plist"), nil
	case "linux":
		return filepath.Join(home, ".config/systemd/user/swarm-discord.service"), nil
	default:
		return "", errors.New("discord: unsupported service platform")
	}
}
func safePath(s string) bool {
	if !filepath.IsAbs(s) || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func RenderUnit(goos, binary, stateDir, logPath string) (string, error) {
	for _, p := range []string{binary, stateDir, logPath} {
		if !safePath(p) {
			return "", errors.New("discord: invalid service path")
		}
	}
	switch goos {
	case "darwin":
		escape := func(s string) string { var b bytes.Buffer; _ = xml.EscapeText(&b, []byte(s)); return b.String() }
		return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>com.swarm.discord</string>
<key>ProgramArguments</key><array><string>%s</string><string>discord</string><string>run</string><string>--state-dir</string><string>%s</string></array>
<key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>ThrottleInterval</key><integer>10</integer>
<key>StandardOutPath</key><string>%s</string><key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, escape(binary), escape(stateDir), escape(logPath), escape(logPath)), nil
	case "linux":
		if strings.TrimRightFunc(logPath, unicode.IsSpace) != logPath || strings.HasSuffix(logPath, `\`) {
			return "", errors.New("discord: invalid log path")
		}
		quote := func(s string) string {
			s = strings.ReplaceAll(s, "%", "%%")
			s = strings.ReplaceAll(s, "\\", "\\\\")
			s = strings.ReplaceAll(s, `"`, `\"`)
			return `"` + s + `"`
		}
		return fmt.Sprintf("[Unit]\nDescription=Swarm Discord\nStartLimitIntervalSec=0\n[Service]\nExecStart=:%s discord run --state-dir %s\nRestart=always\nRestartSec=10\nStandardOutput=append:%s\nStandardError=append:%s\n[Install]\nWantedBy=default.target\n", quote(binary), quote(stateDir), strings.ReplaceAll(logPath, "%", "%%"), strings.ReplaceAll(logPath, "%", "%%")), nil
	default:
		return "", errors.New("discord: unsupported service platform")
	}
}
