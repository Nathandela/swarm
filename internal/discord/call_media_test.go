package discord

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func mediaTestHelper(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-voice")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func mediaTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestCallMediaRoundTrip(t *testing.T) {
	for _, key := range []string{"DISCORD_TOKEN", "OPENAI_API_KEY", "NODE_OPTIONS", "SWARM_MANAGER_TOKEN", "HOME", "PATH"} {
		t.Setenv(key, "parent-secret-"+key)
	}
	pcm := bytes.Repeat([]byte{42}, callMediaPCMBytes)
	encoded := base64.StdEncoding.EncodeToString(pcm)
	path := mediaTestHelper(t, fmt.Sprintf(`
[ "$PATH" = %q ]
[ "${DISCORD_TOKEN+x}${OPENAI_API_KEY+x}${NODE_OPTIONS+x}${SWARM_MANAGER_TOKEN+x}${HOME+x}" = "" ]
printf 'child-secret-diagnostic\n' >&2
IFS= read -r line
[ "$line" = '{"botId":"3","channelId":"2","guildId":"1","speakers":["4"],"type":"start"}' ]
IFS= read -r line
[ "$line" = '{"data":{"token":"selected-voice-token"},"type":"voice_server"}' ]
printf '%%s\n' '{"type":"gateway","data":{"op":4}}'
IFS= read -r line
[ "$line" = '{"data":{"session_id":"selected-session"},"type":"voice_state"}' ]
printf '%%s\n' '{"type":"ready"}' '{"type":"audio","userId":"4","pcm":"%s"}'
IFS= read -r line
[ "$line" = '{"pcm":"%s","type":"play"}' ]
IFS= read -r line
[ "$line" = '{"type":"clear"}' ]
IFS= read -r line
[ "$line" = '{"type":"stop"}' ]
! IFS= read -r line
`, callMediaPath, encoded, encoded))

	stderr, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stderr.Close() }()
	previous := os.Stderr
	os.Stderr = stderr
	defer func() { os.Stderr = previous }()

	input := make(chan any, 5)
	input <- map[string]any{"type": "play", "pcm": pcm}
	input <- map[string]any{"type": "voice_server", "data": json.RawMessage(`{"token":"selected-voice-token"}`)}
	var received []string
	err = runCallMedia(mediaTestContext(t), path, "1", "2", "3", []string{"4"}, input, func() {}, func(event callMediaEvent) error {
		received = append(received, event.Type)
		switch event.Type {
		case "gateway":
			if string(event.Data) != `{"op":4}` {
				return errors.New("unexpected gateway data")
			}
			input <- map[string]any{"type": "voice_state", "data": json.RawMessage(`{"session_id":"selected-session"}`)}
		case "audio":
			if event.UserID != "4" || !bytes.Equal(pcm, event.PCM) {
				return errors.New("unexpected audio")
			}
			input <- map[string]any{"type": "play", "pcm": pcm}
			input <- map[string]any{"type": "clear"}
			input <- map[string]any{"type": "stop"}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(received, []string{"gateway", "ready", "audio"}) {
		t.Fatalf("unexpected event order %v", received)
	}
	diagnostics, err := os.ReadFile(stderr.Name())
	if err != nil || bytes.Contains(diagnostics, []byte("child-secret-diagnostic")) {
		t.Fatalf("helper stderr escaped isolation (read error %v)", err)
	}
}

func TestCallMediaRejectsInvalidOutput(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(make([]byte, callMediaPCMBytes))
	audio := fmt.Sprintf(`{"type":"audio","userId":"4","pcm":"%s"}`, encoded)
	ready := `{"type":"ready"}` + "\n"
	for _, test := range []struct{ name, output string }{
		{"unframed", strings.TrimSpace(ready)},
		{"oversize", strings.Repeat(" ", callMediaLineLimit+1)},
		{"multiple objects", strings.TrimSpace(ready) + ready},
		{"duplicate field", `{"type":"ready","t\u0079pe":"ready"}` + "\n"},
		{"nested duplicate", `{"type":"gateway","data":{"d":[{"id":1,"id":2}]}}` + "\n"},
		{"duplicate ready", ready + ready},
		{"stale gateway", ready + `{"type":"gateway","data":{"op":4}}` + "\n"},
		{"duplicate gateway", strings.Repeat(`{"type":"gateway","data":{"op":4}}`+"\n", 2)},
		{"unknown", `{"type":"voice_state","data":{}}` + "\n"},
		{"extra field", `{"type":"ready","code":null}` + "\n"},
		{"early audio", audio + "\n"},
		{"unapproved speaker", ready + strings.Replace(audio, `"4"`, `"5"`, 1) + "\n"},
		{"bot audio", ready + strings.Replace(audio, `"4"`, `"3"`, 1) + "\n"},
		{"short PCM", ready + strings.Replace(audio, encoded, "AA==", 1) + "\n"},
		{"non-base64 PCM", ready + strings.Replace(audio, encoded, strings.Repeat("_", len(encoded)), 1) + "\n"},
		{"unsafe fault", `{"type":"fault","code":"secret\nmessage"}` + "\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := mediaTestHelper(t, "IFS= read -r line\nprintf '%s' '"+test.output+"'\n")
			lost := make(chan struct{})
			err := runCallMedia(mediaTestContext(t), path, "1", "2", "3", []string{"3", "4"}, nil, func() { close(lost) }, func(event callMediaEvent) error {
				if event.Type == "audio" {
					t.Error("invalid audio reached handler")
				}
				return nil
			})
			if !errors.Is(err, errCallMediaProtocol) {
				t.Fatalf("expected protocol failure, got %v", err)
			}
			select {
			case <-lost:
			default:
				t.Fatal("invalid output did not revoke authority")
			}
		})
	}
}

func TestCallMediaShutdown(t *testing.T) {
	for _, test := range []struct {
		name, afterPID, want string
	}{
		{"cancel blocked handler", "exec /bin/sleep 60", "context canceled"},
		{"EOF while child hangs", "IFS= read -r line; exec 1>&-; exec /bin/sleep 60", "shutdown timed out"},
		{"closed input while child hangs", "exec /bin/sleep 60", "shutdown timed out"},
		{"output backlog", "IFS= read -r line; printf '%s\\n' '{\"type\":\"ready\"}'; while :; do printf '%s\\n' '${audio}'; done", "output backlog"},
		{"blocked writer", "exec /bin/sleep 60", "helper input backlog"},
		{"single blocked write", "exec /bin/sleep 60", "helper write failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(mediaTestContext(t))
			defer cancel()
			encoded := base64.StdEncoding.EncodeToString(make([]byte, callMediaPCMBytes))
			after := strings.ReplaceAll(test.afterPID, "${audio}", fmt.Sprintf(`{"type":"audio","userId":"4","pcm":"%s"}`, encoded))
			path := mediaTestHelper(t, `IFS= read -r line
printf '{"type":"gateway","data":{"pid":%s}}\n' "$$"
`+after+"\n")
			input := make(chan any, 32)
			entered, result := make(chan int, 1), make(chan error, 1)
			lost := make(chan struct{})
			release := make(chan struct{})
			defer close(release)
			go func() {
				result <- runCallMedia(ctx, path, "1", "2", "3", []string{"4"}, input, func() { close(lost) }, func(event callMediaEvent) error {
					if event.Type != "gateway" {
						return nil
					}
					var marker struct{ PID int }
					if err := json.Unmarshal(event.Data, &marker); err != nil {
						return err
					}
					entered <- marker.PID
					if test.name == "cancel blocked handler" || test.name == "output backlog" {
						<-release
					}
					return nil
				})
			}()
			var pid int
			select {
			case pid = <-entered:
			case err := <-result:
				t.Fatalf("helper exited before the PID event: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if pid <= 0 {
				t.Fatal("invalid helper PID")
			}
			switch test.name {
			case "cancel blocked handler":
				cancel()
			case "closed input while child hangs":
				close(input)
			case "output backlog", "EOF while child hangs":
				input <- map[string]any{"type": "clear"}
			case "blocked writer", "single blocked write":
				message := map[string]any{"type": "voice_server", "data": map[string]string{"token": strings.Repeat("x", 8000)}}
				input <- message
				if test.name == "blocked writer" {
					for i := 0; i < cap(input)-1; i++ {
						input <- message
					}
				} else {
					// Fill the pipe slowly so the deadline, rather than the queue, stops the writer.
					pacer := time.NewTicker(50 * time.Millisecond)
					defer pacer.Stop()
				fill:
					for {
						select {
						case <-lost:
							break fill
						case <-pacer.C:
							input <- message
						case <-ctx.Done():
							t.Fatal(ctx.Err())
						}
					}
				}
			}
			if test.name == "EOF while child hangs" {
				callFixtureReceive(t, ctx, lost, "stdout EOF revoking authority")
				if err := syscall.Kill(pid, 0); err != nil {
					t.Fatalf("EOF notification arrived after child cleanup: %v", err)
				}
			}
			select {
			case err := <-result:
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("expected %q, got %v", test.want, err)
				}
				select {
				case <-lost:
				default:
					t.Fatal("runner returned before notifying authority loss")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("helper shutdown did not return")
			}
			if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
				t.Fatalf("helper %d still exists: %v", pid, err)
			}
		})
	}
}

func TestCallMediaFaultAndInputValidation(t *testing.T) {
	path := mediaTestHelper(t, `IFS= read -r line
printf '%s\n' '{"type":"fault","code":"dave_required"}'
exit 1
`)
	fault := make(chan string, 1)
	err := runCallMedia(mediaTestContext(t), path, "1", "2", "3", []string{"4"}, nil, func() {}, func(event callMediaEvent) error {
		fault <- event.Code
		return nil
	})
	select {
	case code := <-fault:
		if code != "dave_required" || err == nil || !strings.Contains(err.Error(), "reported a fault") {
			t.Fatalf("unexpected fault %q and error %v", code, err)
		}
	default:
		t.Fatalf("fault was not delivered: %v", err)
	}
	for _, command := range []any{
		map[string]any{"type": "start"},
		map[string]any{"type": "voice_state", "data": nil},
		map[string]any{"type": "clear", "pcm": "secret"},
		map[string]any{"type": "play", "pcm": []byte{1}},
		map[string]any{"type": "voice_server", "data": map[string]string{"token": strings.Repeat("secret", callMediaLineLimit)}},
	} {
		if line, err := json.Marshal(command); err != nil {
			t.Fatal(err)
		} else if _, err := decodeCallMedia(line, false); !errors.Is(err, errCallMediaProtocol) {
			t.Fatalf("invalid input got %v", err)
		}
	}
	if err := runCallMedia(mediaTestContext(t), "./fake", "1", "2", "3", nil, nil, func() {}, nil); err == nil {
		t.Fatal("accepted a relative helper path")
	}
}

func TestHelperInputClosePreservesFinalFault(t *testing.T) {
	path := mediaTestHelper(t, `IFS= read -r line
exec 0<&-
printf '%s\n' '{"type":"ready"}'
sleep 0.1
printf '%s\n' '{"type":"fault","code":"dave_changed"}'
`)
	commands := make(chan any, 1)
	observed := make(chan string, 1)
	err := runCallMedia(mediaTestContext(t), path, "1", "2", "3", []string{"4"}, commands, func() {}, func(e callMediaEvent) error {
		if e.Type == "ready" {
			commands <- map[string]any{"type": "clear"}
		}
		if e.Type == "fault" {
			observed <- e.Code
		}
		return nil
	})
	if err == nil {
		t.Fatal("accepted interrupted media helper")
	}
	select {
	case code := <-observed:
		if code != "dave_changed" {
			t.Fatal(code)
		}
	default:
		t.Fatal("stdin failure hid the DAVE fault")
	}
}
