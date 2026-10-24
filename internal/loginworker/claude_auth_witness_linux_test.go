//go:build linux

package loginworker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/procstart"
)

// These files coordinate only a disposable test witness; they are not auth
// authority or a product worker protocol.
type claudeAuthWitnessConfig struct {
	Native          ClaudeAuthConfig
	Authority, Root string
}
type claudeAuthWitnessReceipt struct {
	Native             processcontain.Identity
	RequestID, Version string
}

func init() {
	claudeAuthWitnessDispatch = func(path string) (bool, int) {
		if filepath.Base(path) != "witness.json" {
			return false, 0
		}
		if runClaudeAuthWitness(path) != nil {
			return true, 1
		}
		return true, 0
	}
}
func witnessJSON(path string, value any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(raw) > 16384 || json.Unmarshal(raw, value) != nil {
		return ErrInvalid
	}
	canonical, _ := json.Marshal(value)
	if !bytes.Equal(raw, canonical) {
		return ErrInvalid
	}
	return nil
}
func witnessWrite(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err = os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
func witnessWait(ctx context.Context, path string, value any) error {
	for {
		if err := witnessJSON(path, value); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func witnessVersion(raw []byte, id string) bool {
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		fields, ok := claudeAuthEnvelope(line)
		if !ok {
			continue
		}
		var kind string
		if json.Unmarshal(fields["type"], &kind) != nil || kind != "control_response" {
			continue
		}
		response, ok := claudeAuthEnvelope(fields["response"])
		if !ok {
			continue
		}
		var requestID, subtype string
		if json.Unmarshal(response["request_id"], &requestID) != nil || requestID != id || json.Unmarshal(response["subtype"], &subtype) != nil || subtype != "success" {
			continue
		}
		p, ok := claudeAuthEnvelope(response["response"])
		if !ok {
			return false
		}
		var version string
		if len(p) < 1 || len(p) > 2 || json.Unmarshal(p["version"], &version) != nil || version != "2.1.310" {
			return false
		}
		for key, v := range p {
			if key == "buildTime" {
				var stamp string
				if bytes.Equal(bytes.TrimSpace(v), []byte("null")) || json.Unmarshal(v, &stamp) != nil {
					return false
				}
			} else if key != "version" {
				return false
			}
		}
		return true
	}
	return false
}
func runClaudeAuthWitness(path string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var c claudeAuthWitnessConfig
	if err := witnessJSON(path, &c); err != nil {
		return err
	}
	if filepath.Dir(path) != c.Root || c.Root != filepath.Join(c.Native.StateRoot, "native-witness") {
		return ErrUnsafe
	}
	ctx, cancel := context.WithTimeout(context.Background(), 43*time.Second)
	defer cancel()
	var scope logins.ClaudeAuthScope
	if err := witnessWait(ctx, filepath.Join(c.Root, "scope.json"), &scope); err != nil {
		return err
	}
	if err := checkClaudeAuthConfinement(scope); err != nil {
		return err
	}
	if err := enableSubreaper(); err != nil {
		return err
	}
	if err := claudeAuthFixtureTransport(c.Native.StateRoot); err != nil {
		return err
	}
	image, err := openClaudeAuthImage(c.Native)
	if err != nil {
		return err
	}
	defer func() { _ = image.Close() }()
	home, tmp, config := filepath.Join(c.Root, "home"), filepath.Join(c.Root, "tmp"), filepath.Join(c.Root, "config")
	for _, p := range []string{home, tmp, config} {
		if err := os.Mkdir(p, 0700); err != nil {
			return err
		}
	}
	cmd := claudeAuthImageCommand(image, c.Native.NativePath, claudeAuthArguments()...)
	cmd.Dir = home
	cmd.Env = claudeAuthNativeEnvironment(logins.ClaudeAuthAttempt{ConfigHome: config, AuthorityHome: c.Authority}, home, tmp)
	cmd.SysProcAttr = nativeAttrs()
	output, stderr := &boundedBuffer{limit: 2 << 20}, &boundedBuffer{limit: 64 << 10}
	cmd.Stdout = output
	cmd.Stderr = stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	if err := cmd.Start(); err != nil {
		return err
	}
	child, startErr := observeClaudeAuthChild(cmd, procstart.StartTime)
	defer func() {
		_ = input.Close()
		_ = containClaudeAuthScope(scope, cmd.Process.Pid)
		_ = waitClaudeAuthOutput(child)
		_ = reapClaudeAuthScope()
	}()
	if startErr != nil {
		return startErr
	}
	if _, err = io.WriteString(input, "{\"type\":\"control_request\",\"request_id\":\"swarm-auth-init\",\"request\":{\"subtype\":\"initialize\"}}\n"); err != nil {
		return err
	}
	for {
		raw, overflow := output.Bytes()
		if overflow {
			return ErrInvalid
		}
		if claudeAuthInitialized(raw) {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-child.done:
			return ErrUnavailable
		case <-time.After(10 * time.Millisecond):
		}
	}
	for _, phase := range []string{"before", "after"} {
		if phase == "after" {
			var goSignal string
			if err := witnessWait(ctx, filepath.Join(c.Root, "after-gate.json"), &goSignal); err != nil || goSignal != "after" {
				return ErrInvalid
			}
		}
		id := "witness-" + phase + "-" + c.Native.Ref.Nonce
		request := map[string]any{"type": "control_request", "request_id": id, "request": map[string]string{"subtype": "get_binary_version"}}
		raw, _ := json.Marshal(request)
		if _, err := input.Write(append(raw, '\n')); err != nil {
			return err
		}
		for {
			raw, overflow := output.Bytes()
			_, errOverflow := stderr.Bytes()
			if overflow || errOverflow {
				return ErrInvalid
			}
			if witnessVersion(raw, id) {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-child.done:
				return ErrUnavailable
			case <-time.After(10 * time.Millisecond):
			}
		}
		if err := witnessWrite(filepath.Join(c.Root, phase+".json"), claudeAuthWitnessReceipt{Native: child.identity, RequestID: id, Version: "2.1.310"}); err != nil {
			return err
		}
	}
	var stop string
	if err := witnessWait(ctx, filepath.Join(c.Root, "stop-gate.json"), &stop); err != nil || stop != "stop" {
		return ErrInvalid
	}
	return nil
}

func startClaudeAuthWitness(ctx context.Context, executable string, c ClaudeAuthConfig, authority string) (logins.ClaudeAuthScope, claudeAuthWitnessReceipt, error) {
	root := filepath.Join(c.StateRoot, "native-witness")
	if err := os.Mkdir(root, 0700); err != nil {
		return logins.ClaudeAuthScope{}, claudeAuthWitnessReceipt{}, err
	}
	path := filepath.Join(root, "witness.json")
	if err := witnessWrite(path, claudeAuthWitnessConfig{Native: c, Authority: authority, Root: root}); err != nil {
		return logins.ClaudeAuthScope{}, claudeAuthWitnessReceipt{}, err
	}
	ref := c.Ref
	ref.Nonce = "f" + ref.Nonce[1:]
	if ref.Nonce == c.Ref.Nonce {
		ref.Nonce = "e" + ref.Nonce[1:]
	}
	scope, err := startClaudeAuthScope(ctx, executable, path, ref, time.Now().Add(44*time.Second))
	if err != nil {
		return scope, claudeAuthWitnessReceipt{}, err
	}
	if err := witnessWrite(filepath.Join(root, "scope.json"), scope); err != nil {
		return scope, claudeAuthWitnessReceipt{}, err
	}
	var receipt claudeAuthWitnessReceipt
	err = witnessWait(ctx, filepath.Join(root, "before.json"), &receipt)
	if err == nil {
		if receipt.RequestID != "witness-before-"+c.Ref.Nonce {
			err = ErrInvalid
		} else {
			err = verifyClaudeAuthWitness(scope, receipt)
		}
	}
	return scope, receipt, err
}
func verifyClaudeAuthWitness(scope logins.ClaudeAuthScope, r claudeAuthWitnessReceipt) error {
	if !r.Native.Alive() || r.Version != "2.1.310" {
		return ErrUnavailable
	}
	group, err := claudeAuthGroup(r.Native.PID)
	if err != nil || group != scope.Cgroup {
		return ErrStale
	}
	dev, ino, err := claudeAuthGroupIdentity("/sys/fs/cgroup" + group)
	if err != nil || dev != scope.Device || ino != scope.Inode {
		return ErrStale
	}
	return nil
}
func stopClaudeAuthWitness(ctx context.Context, scope logins.ClaudeAuthScope) error {
	if scope.Worker.Alive() {
		if err := processcontain.SignalIdentity(scope.Worker, syscall.SIGKILL); err != nil {
			return err
		}
	}
	for {
		if err := verifyClaudeAuthScopeRetired(ctx, scope); err == nil {
			return collectClaudeAuthScope(ctx, scope)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("witness retirement pending: %w", ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func TestClaudeAuthWitnessVersionRequiresExactControlReply(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{`{"type":"control_response","response":{"request_id":"expected","subtype":"success","response":{"version":"2.1.310"}}}`, true},
		{`{"type":"control_response","response":{"request_id":"expected","subtype":"success","response":{"version":"2.1.310","buildTime":"fixture"}}}`, true},
		{`{"type":"control_response","response":{"request_id":"old","subtype":"success","response":{"version":"2.1.310"}}}`, false},
		{`{"type":"control_response","response":{"request_id":"expected","subtype":"error","response":{"version":"2.1.310"}}}`, false},
		{`{"type":"control_response","response":{"request_id":"expected","subtype":"success","response":{"version":"2.1.311"}}}`, false},
		{`{"type":"control_response","response":{"request_id":"expected","subtype":"success","response":{"version":"2.1.310","extra":true}}}`, false},
		{`{"type":"control_response","response":{"request_id":"expected","subtype":"success","response":{"version":"2.1.310","buildTime":2}}}`, false},
		{`{"Type":"control_response","response":{"request_id":"expected","subtype":"success","response":{"version":"2.1.310"}}}`, false},
		{`{"type":"wrong","type":"control_response","response":{"request_id":"expected","subtype":"success","response":{"version":"2.1.310"}}}`, false},
		{`{"type":"control_response","response":{"Request_ID":"expected","subtype":"success","response":{"version":"2.1.310"}}}`, false},
		{`{"type":"control_response","response":{"request_id":"old","request_id":"expected","subtype":"success","response":{"version":"2.1.310"}}}`, false},
		{`{"type":"control_response","response":{"request_id":"expected","Subtype":"success","response":{"version":"2.1.310"}}}`, false},
		{`{"type":"control_response","response":{"request_id":"expected","subtype":"error","subtype":"success","response":{"version":"2.1.310"}}}`, false},
		{`{"type":"control_response","response":{"request_id":"expected","subtype":"success","response":{"version":"wrong","version":"2.1.310"}}}`, false},
		{`{"type":"control_response","response":{"request_id":"expected","subtype":"success","response":{"version":"2.1.310","buildTime":null}}}`, false},
	} {
		if got := witnessVersion([]byte(tc.raw), "expected"); got != tc.want {
			t.Fatalf("reply qualified=%v want %v", got, tc.want)
		}
	}
}
