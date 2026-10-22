package logins

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"strings"
	"time"

	"golang.org/x/text/unicode/norm"
)

type credentialKeychain interface {
	Read(string) ([]byte, error)
	Write(string, []byte) error
	Delete(string) error
}

func (nativeKeychain) Delete(home string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "delete-generic-password", "-s", keychainService(home), "-a", NativeUsername())
	if e := cmd.Run(); e != nil {
		var exit *exec.ExitError
		if errors.As(e, &exit) && exit.ExitCode() == 44 {
			return nil
		}
		return errors.New("native credential Keychain removal failed")
	}
	return nil
}

type nativeKeychain struct{}

var safeUser = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// NativeUsername matches Claude's Keychain account selector for this OS user.
func NativeUsername() string {
	u, e := user.Current()
	if e == nil && safeUser.MatchString(u.Username) {
		return u.Username
	}
	return "claude-code-user"
}
func keychainService(home string) string {
	sum := sha256.Sum256([]byte(norm.NFC.String(home)))
	return "Claude Code-credentials-" + hex.EncodeToString(sum[:])[:8]
}
func (nativeKeychain) Read(home string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "find-generic-password", "-s", keychainService(home), "-a", NativeUsername(), "-w")
	b, e := cmd.Output()
	if e != nil {
		var exit *exec.ExitError
		if errors.As(e, &exit) && exit.ExitCode() == 44 {
			return nil, os.ErrNotExist
		}
		return nil, errors.New("native credential Keychain read failed")
	}
	return bytes.TrimSpace(b), nil
}
func securityQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`).Replace(s) + `"`
}
func (nativeKeychain) Write(home string, raw []byte) error {
	if len(raw) > maxCredentialBytes || validateJSON(raw) != nil {
		return ErrInvalidCredentials
	}
	// Interactive security reads the password from stdin, never process argv.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "-i")
	cmd.Stdin = strings.NewReader("add-generic-password -U -s " + securityQuote(keychainService(home)) + " -a " + securityQuote(NativeUsername()) + " -w " + securityQuote(string(raw)) + "\n")
	if e := cmd.Run(); e != nil {
		return errors.New("native credential Keychain write failed")
	}
	actual, e := (nativeKeychain{}).Read(home)
	if e != nil || !bytes.Equal(actual, raw) {
		return errors.New("native credential Keychain readback failed")
	}
	return nil
}
