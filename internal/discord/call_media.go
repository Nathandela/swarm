package discord

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sync/atomic"
	"time"

	"github.com/Nathandela/swarm/internal/jsontext"
)

const (
	callMediaLineLimit = 8192
	callMediaPCMBytes  = 960
	callMediaQueueSize = 8
	callMediaStopGrace = 500 * time.Millisecond
	callMediaWriteWait = 250 * time.Millisecond
	callMediaPath      = "/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin"
)

var errCallMediaProtocol = errors.New("discord call media: invalid pipe message")
var callMediaFaultCode = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type callMediaEvent struct {
	Type   string          `json:"type"`
	Data   json.RawMessage `json:"data,omitempty"`
	UserID string          `json:"userId,omitempty"`
	PCM    []byte          `json:"pcm,omitempty"`
	Code   string          `json:"code,omitempty"`
}

// handle is called serially. It must return promptly and honor the call context:
// shutdown can reap the helper even if a callback is still running.
func runCallMedia(ctx context.Context, helperPath, guildID, roomID, botID string, speakers []string, input <-chan any, handle func(callMediaEvent) error) error {
	if !filepath.IsAbs(helperPath) {
		return errors.New("discord call media: helper path must be absolute")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	start, err := json.Marshal(map[string]any{
		"type": "start", "guildId": guildID, "channelId": roomID, "botId": botID,
		"speakers": append([]string{}, speakers...),
	})
	if err != nil || len(start) > callMediaLineLimit {
		return errCallMediaProtocol
	}
	allowed := slices.Clone(speakers)

	// Own both pipes so Wait can reap independently of buffered stdout and blocked stdin.
	childIn, stdin, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() {
		_ = childIn.Close()
		_ = stdin.Close()
	}()
	stdout, childOut, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() {
		_ = stdout.Close()
		_ = childOut.Close()
	}()
	cmd := exec.Command(helperPath)
	cmd.Env = []string{"PATH=" + callMediaPath}
	cmd.Stdin, cmd.Stdout = childIn, childOut
	if err := cmd.Start(); err != nil {
		return errors.New("discord call media: could not start helper")
	}
	_ = childIn.Close()
	_ = childOut.Close()

	ctx, cancel := context.WithCancel(ctx)
	exited := make(chan struct{})
	var exitErr error
	go func() {
		exitErr = cmd.Wait()
		close(exited)
	}()
	defer func() {
		cancel()
		_ = stdin.Close()
		_ = stdout.Close()
		_ = cmd.Process.Kill()
		<-exited
	}()

	writes := make(chan []byte, callMediaQueueSize)
	writes <- start
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- writeCallMedia(ctx, stdin, writes)
	}()
	events := make(chan callMediaEvent, callMediaQueueSize)
	readDone := make(chan error, 1)
	var ready, stopped atomic.Bool
	go func() {
		defer close(events)
		readDone <- readCallMedia(stdout, allowed, botID, &ready, events)
	}()
	handled := make(chan error, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-events:
				if !ok {
					handled <- nil
					return
				}
				if ctx.Err() != nil {
					return
				}
				if event.Type == "audio" && stopped.Load() {
					continue
				}
				if err := handle(event); err != nil {
					handled <- err
					return
				}
				if event.Type == "fault" {
					handled <- errors.New("discord call media: helper reported a fault")
					return
				}
			}
		}
	}()

	var shutdown *time.Timer
	var deadline <-chan time.Time
	defer func() {
		if shutdown != nil {
			shutdown.Stop()
		}
	}()
	beginShutdown := func() {
		input = nil
		if writes != nil {
			close(writes)
			writes = nil
		}
		if shutdown == nil {
			shutdown = time.NewTimer(callMediaStopGrace)
			deadline = shutdown.C
		}
	}
	processDone := (<-chan struct{})(exited)
	for processDone != nil || handled != nil || readDone != nil || writeDone != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return errors.New("discord call media: helper shutdown timed out")
		case <-processDone:
			processDone = nil
			beginShutdown()
		case err := <-readDone:
			if err != nil {
				return err
			}
			readDone = nil
			beginShutdown()
		case err := <-handled:
			if err != nil {
				return err
			}
			handled = nil
		case err := <-writeDone:
			if err != nil {
				return err
			}
			writeDone = nil
		case message, ok := <-input:
			if !ok {
				beginShutdown()
				continue
			}
			line, err := json.Marshal(message)
			if err != nil {
				return errCallMediaProtocol
			}
			event, err := decodeCallMedia(line, false)
			if err != nil {
				return err
			}
			if event.Type == "play" && !ready.Load() {
				continue
			}
			if event.Type == "stop" {
				stopped.Store(true)
			}
			select {
			case writes <- line:
			default:
				return errors.New("discord call media: helper input backlog")
			}
			if event.Type == "stop" {
				beginShutdown()
			}
		}
	}
	if exitErr != nil {
		return errors.New("discord call media: helper exited unsuccessfully")
	}
	return ctx.Err()
}

func writeCallMedia(ctx context.Context, stdin *os.File, pending <-chan []byte) error {
	defer func() { _ = stdin.Close() }()
	for {
		select {
		case <-ctx.Done():
			return nil
		case line, ok := <-pending:
			if !ok || ctx.Err() != nil {
				return nil
			}
			if err := stdin.SetWriteDeadline(time.Now().Add(callMediaWriteWait)); err != nil {
				return errors.New("discord call media: could not bound helper write")
			}
			if _, err := stdin.Write(append(line, '\n')); err != nil {
				return errors.New("discord call media: helper write failed")
			}
		}
	}
}

func readCallMedia(stdout io.Reader, speakers []string, botID string, ready *atomic.Bool, events chan<- callMediaEvent) error {
	reader := bufio.NewReaderSize(stdout, callMediaLineLimit+1)
	joined := false
	for {
		line, err := reader.ReadSlice('\n')
		if err == io.EOF && len(line) == 0 {
			return nil
		}
		if err != nil {
			return errCallMediaProtocol
		}
		event, err := decodeCallMedia(line[:len(line)-1], true)
		if err != nil {
			return err
		}
		switch event.Type {
		case "gateway":
			if joined || ready.Load() {
				return errCallMediaProtocol
			}
			joined = true
		case "ready":
			if ready.Swap(true) {
				return errCallMediaProtocol
			}
		case "audio":
			if !ready.Load() || event.UserID == botID || !slices.Contains(speakers, event.UserID) {
				return errCallMediaProtocol
			}
		}
		select {
		case events <- event:
		default:
			return errors.New("discord call media: helper output backlog")
		}
		if event.Type == "fault" {
			return nil
		}
	}
}

func decodeCallMedia(line []byte, fromHelper bool) (callMediaEvent, error) {
	var event callMediaEvent
	if len(line) > callMediaLineLimit || jsontext.ValidateUnicode(line) != nil {
		return event, errCallMediaProtocol
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.UseNumber()
	if !uniqueCallMediaJSON(decoder, 0) {
		return event, errCallMediaProtocol
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(line, &fields) != nil || json.Unmarshal(fields["type"], &event.Type) != nil {
		return event, errCallMediaProtocol
	}

	valid := false
	switch event.Type {
	case "ready", "clear", "stop":
		valid = len(fields) == 1 && fromHelper == (event.Type == "ready")
	case "gateway", "voice_state", "voice_server":
		event.Data = bytes.TrimSpace(fields["data"])
		valid = len(fields) == 2 && len(event.Data) > 0 && event.Data[0] == '{' &&
			fromHelper == (event.Type == "gateway")
	case "audio", "play":
		var encoded string
		if json.Unmarshal(fields["pcm"], &encoded) != nil || len(encoded) != base64.StdEncoding.EncodedLen(callMediaPCMBytes) {
			return callMediaEvent{}, errCallMediaProtocol
		}
		var err error
		event.PCM, err = base64.StdEncoding.Strict().DecodeString(encoded)
		valid = err == nil && len(event.PCM) == callMediaPCMBytes
		if event.Type == "audio" {
			valid = valid && fromHelper && len(fields) == 3 &&
				json.Unmarshal(fields["userId"], &event.UserID) == nil && event.UserID != ""
		} else {
			valid = valid && !fromHelper && len(fields) == 2
		}
	case "fault":
		valid = fromHelper && len(fields) == 2 && json.Unmarshal(fields["code"], &event.Code) == nil &&
			callMediaFaultCode.MatchString(event.Code)
	}
	if !valid {
		return callMediaEvent{}, errCallMediaProtocol
	}
	return event, nil
}

func uniqueCallMediaJSON(decoder *json.Decoder, depth int) bool {
	if depth > 32 {
		return false
	}
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	if token != json.Delim('{') && token != json.Delim('[') {
		return true
	}
	keys := make(map[string]bool)
	for decoder.More() {
		if token == json.Delim('{') {
			key, err := decoder.Token()
			name, ok := key.(string)
			if err != nil || !ok || keys[name] {
				return false
			}
			keys[name] = true
		}
		if !uniqueCallMediaJSON(decoder, depth+1) {
			return false
		}
	}
	_, err = decoder.Token()
	return err == nil
}
