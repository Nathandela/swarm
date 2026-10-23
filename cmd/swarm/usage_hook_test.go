package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nathandela/swarm/internal/hookclient"
)

const usageConversation = "10000000-0000-4000-8000-000000000001"

func usageBody(now time.Time) string {
	return fmt.Sprintf(`{"session_id":%q,"transcript_path":"private transcript","secret":"private","context_window":{"remaining_percentage":9},"rate_limits":{"five_hour":{"used_percentage":23.5,"resets_at":%d},"seven_day":{"used_percentage":90,"resets_at":%d},"spend_limit":{"used_percentage":8}}}`, usageConversation, now.Add(time.Hour).Unix(), now.Add(24*time.Hour).Unix())
}

func TestUsageHookPostsOnlyUsageAndNeverRaw(t *testing.T) {
	now := time.Now()
	sock, next := hookSink(t)
	hookEnv(t, sock, []string{"SwarmUsage"})
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	var output, diagnostics bytes.Buffer
	if code := runUsageHook(nil, strings.NewReader(usageBody(now)), &output, &diagnostics); code != 0 {
		t.Fatalf("exit = %d", code)
	}
	cb := next()
	if cb.Event != "SwarmUsage" || cb.SessionID != "sid-1" || cb.Token != "tok-abc" || cb.Sequence != 0 || len(cb.Raw) != 0 || len(cb.Payload) != 6 || cb.Payload["five_hour_used_percentage"] != "23.5" || cb.Payload["seven_day_used_percentage"] != "90" {
		t.Fatalf("unexpected sanitized callback: %#v", cb)
	}
	if cb.Payload["session_id"] != usageConversation || strings.Contains(fmt.Sprint(cb.Payload), "private") || strings.Contains(fmt.Sprint(cb.Payload), "context_window") {
		t.Fatalf("private native input escaped: %#v", cb.Payload)
	}
	if _, err := time.Parse(time.RFC3339Nano, cb.Payload["observed_at"]); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 || diagnostics.Len() != 0 {
		t.Fatalf("quiet observer output = %q, diagnostics = %q", output.String(), diagnostics.String())
	}
}

func TestUsageHookDoesNotWaitForHookSequenceLock(t *testing.T) {
	sock, next := hookSink(t)
	hookEnv(t, sock, nil)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	file, err := os.OpenFile(os.Getenv(hookclient.EnvSequenceFile), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }()
	finished := make(chan int, 1)
	go func() {
		finished <- runUsageHook(nil, strings.NewReader(usageBody(time.Now())), io.Discard, io.Discard)
	}()
	select {
	case code := <-finished:
		if code != 0 {
			t.Fatalf("exit=%d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("advisory usage waited on hook sequence lock")
	}
	if cb := next(); cb.Sequence != 0 {
		t.Fatalf("advisory usage borrowed hook sequence: %d", cb.Sequence)
	}
}

func TestClaudeUsagePayloadRejectsUnavailableAndInvalidWindows(t *testing.T) {
	now := time.Now()
	for _, body := range []string{
		`{}`, `{"rate_limits":null}`, `{"session_id":"bad","rate_limits":{"five_hour":{"used_percentage":20,"resets_at":9999999999}}}`,
		fmt.Sprintf(`{"session_id":%q,"agent_id":"child","rate_limits":{"five_hour":{"used_percentage":20,"resets_at":%d}}}`, usageConversation, now.Add(time.Hour).Unix()),
		fmt.Sprintf(`{"session_id":%q,"rate_limits":{"five_hour":{"used_percentage":101,"resets_at":%d}}}`, usageConversation, now.Add(time.Hour).Unix()),
		fmt.Sprintf(`{"session_id":%q,"rate_limits":{"five_hour":{"used_percentage":20,"resets_at":%d}}}`, usageConversation, now.Unix()),
		fmt.Sprintf(`{"session_id":%q,"rate_limits":{"five_hour":{"used_percentage":null,"resets_at":%d}}}`, usageConversation, now.Add(time.Hour).Unix()),
		usageBody(now) + `{}`,
	} {
		if got := claudeUsagePayload([]byte(body), now); got != nil {
			t.Fatalf("invalid input %s produced %#v", body, got)
		}
	}
	body := fmt.Sprintf(`{"session_id":%q,"rate_limits":{"five_hour":{"used_percentage":-1,"resets_at":%d},"seven_day":{"used_percentage":0,"resets_at":%d}}}`, usageConversation, now.Add(time.Hour).Unix(), now.Add(time.Hour).Unix())
	got := claudeUsagePayload([]byte(body), now)
	if len(got) != 4 || got["seven_day_used_percentage"] != "0" {
		t.Fatalf("independent valid zero window lost: %#v", got)
	}
}

func setUsageStatusLine(t *testing.T, home, command string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"statusLine": map[string]any{"type": "command", "command": command}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "settings.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUsageHookPreservesNativeCommandAndReloadsIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	t.Setenv("SWARM_USAGE_TEST", "value with 'quotes' $ and ;")
	t.Setenv(hookclient.EnvToken, "")
	command := `printf '<%s>|%s|%s|%s|' "$0" "$SWARM_USAGE_TEST" "$PWD" "$#"; cat; printf '\nerr' >&2; exit 7`
	setUsageStatusLine(t, home, command)
	body := "native JSON\nwith trailing newline\n"
	var expectedOut, expectedErr bytes.Buffer
	native := exec.Command("/bin/sh", "-c", command)
	native.Stdin, native.Stdout, native.Stderr = strings.NewReader(body), &expectedOut, &expectedErr
	_ = native.Run()
	var output, diagnostics bytes.Buffer
	if code := runUsageHook([]string{"outdated command"}, strings.NewReader(body), &output, &diagnostics); code != 7 || output.String() != expectedOut.String() || diagnostics.String() != expectedErr.String() {
		t.Fatalf("native command changed: exit=%d out=%q err=%q; want out=%q err=%q", code, output.String(), diagnostics.String(), expectedOut.String(), expectedErr.String())
	}
	setUsageStatusLine(t, home, "printf updated")
	output.Reset()
	if code := runUsageHook([]string{command}, strings.NewReader(body), &output, io.Discard); code != 0 || output.String() != "updated" {
		t.Fatalf("native command edit not honored: exit=%d out=%q", code, output.String())
	}
	if err := os.WriteFile(filepath.Join(home, "settings.json"), []byte("invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if code := runUsageHook([]string{"printf fallback"}, strings.NewReader(body), &output, io.Discard); code != 0 || output.String() != "fallback" {
		t.Fatalf("loaded native fallback lost: exit=%d out=%q", code, output.String())
	}
}

func TestUsageHookDoesNotTruncateNativeCommandInput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	setUsageStatusLine(t, home, "cat")
	body := strings.Repeat("x", hookStdinLimit+123)
	var output bytes.Buffer
	if code := runUsageHook(nil, strings.NewReader(body), &output, io.Discard); code != 0 || output.String() != body {
		t.Fatalf("native input truncated: exit=%d length=%d", code, output.Len())
	}
}

func TestUsageHookDaemonFailureDoesNotHoldNativeStatusLine(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "usage-sock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	// A socket that neither accepts nor reads must not keep the custom line waiting.
	hookEnv(t, socket, []string{"SwarmUsage"})
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	setUsageStatusLine(t, home, "printf retained")
	for _, path := range []string{socket, filepath.Join(dir, "absent.sock")} {
		t.Setenv(hookclient.EnvSocket, path)
		var output, diagnostics bytes.Buffer
		start := time.Now()
		if code := runUsageHook(nil, strings.NewReader(usageBody(start)), &output, &diagnostics); code != 0 || output.String() != "retained" || diagnostics.Len() != 0 || time.Since(start) > time.Second {
			t.Fatalf("advisory post held native line: exit=%d out=%q err=%q elapsed=%s", code, output.String(), diagnostics.String(), time.Since(start))
		}
	}
}

func TestUsageHookCancellationKeepsNativeProcessGroup(t *testing.T) {
	if os.Getenv("SWARM_USAGE_CANCEL_CHILD") == "1" {
		os.Exit(runUsageHook(nil, strings.NewReader("{}"), os.Stdout, os.Stderr))
	}
	home := t.TempDir()
	pidfile := filepath.Join(home, "pid")
	setUsageStatusLine(t, home, `printf '%s' "$$" > '`+pidfile+`'; exec sleep 30`)
	cmd := exec.Command(os.Args[0], "-test.run=^TestUsageHookCancellationKeepsNativeProcessGroup$")
	cmd.Env = append(os.Environ(), "SWARM_USAGE_CANCEL_CHILD=1", "CLAUDE_CONFIG_DIR="+home)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) })
	var child int
	deadline := time.Now().Add(5 * time.Second)
	for child == 0 && time.Now().Before(deadline) {
		raw, _ := os.ReadFile(pidfile)
		_, _ = fmt.Sscan(string(raw), &child)
		if child == 0 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if child == 0 {
		t.Fatal("native command never started")
	}
	group, err := syscall.Getpgid(child)
	if err != nil || group != cmd.Process.Pid {
		t.Fatalf("native command escaped cancellation group: group=%d wrapper=%d err=%v", group, cmd.Process.Pid, err)
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Wait()
}
