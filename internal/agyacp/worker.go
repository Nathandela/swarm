package agyacp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Nathandela/swarm/internal/adapter"
)

type Env struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Server struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Env     []Env    `json:"env"`
}

type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

type relay struct {
	decoder *bufio.Scanner
	encoder *json.Encoder
	stdin   *bufio.Scanner
	output  io.Writer
}

func Run(ctx context.Context, path, cwd, dir, attempt string, env []string, server Server, input io.Reader, output io.Writer) error {
	cmd := exec.CommandContext(ctx, path, "acp", "--stdio")
	cmd.Dir = cwd
	cmd.Env = env
	toProvider, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	fromProvider, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return errors.New("could not start native agy ACP")
	}
	defer func() {
		_ = toProvider.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	r := relay{bufio.NewScanner(fromProvider), json.NewEncoder(toProvider), bufio.NewScanner(input), output}
	r.decoder.Buffer(make([]byte, 65536), 2<<20)
	r.stdin.Buffer(make([]byte, 64), 1024)
	startupTimeout := time.AfterFunc(30*time.Second, func() { _ = cmd.Process.Kill() })
	defer startupTimeout.Stop()
	return r.run(ctx, cwd, dir, attempt, server, func() { startupTimeout.Stop() })
}

func (r relay) send(id int, method string, params any) error {
	return r.encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
}

func (r relay) result(id json.RawMessage, result any) error {
	return r.encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (r relay) read(id string, notify func(message) error) (json.RawMessage, error) {
	for r.decoder.Scan() {
		var msg message
		if err := json.Unmarshal(r.decoder.Bytes(), &msg); err != nil {
			return nil, errors.New("invalid native agy ACP response")
		}
		if msg.Method == "" {
			if string(msg.ID) != id {
				continue
			}
			if len(msg.Error) != 0 || len(msg.Result) == 0 {
				return nil, errors.New("native agy rejected an ACP request")
			}
			return msg.Result, nil
		}
		if msg.Method == "session/update" && len(msg.ID) == 0 || msg.Method == "session/request_permission" && len(msg.ID) != 0 {
			if err := notify(msg); err != nil {
				return nil, err
			}
		} else if len(msg.ID) != 0 {
			if err := r.encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": -32601, "message": "Unsupported ACP method"}}); err != nil {
				return nil, err
			}
		}
	}
	return nil, errors.New("native agy ACP closed before completing the request")
}

func (r relay) run(ctx context.Context, cwd, dir, attempt string, server Server, startupDone func()) (runErr error) {
	started := false
	defer func() {
		if runErr != nil && !started {
			// No private prompt could have been delivered. The daemon still
			// requires a shim exit receipt before trying another provider.
			_ = write(dir, "schedule-agy-evidence.json", Evidence{Attempt: attempt, StartupRejected: true}, false)
		}
	}()
	startup := func(msg message) error {
		if len(msg.ID) != 0 {
			return r.permission(msg.ID, "")
		}
		return nil
	}
	if err := r.send(1, "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}}); err != nil {
		return err
	}
	result, err := r.read("1", startup)
	var initialized struct{ ProtocolVersion int }
	if err != nil || json.Unmarshal(result, &initialized) != nil || initialized.ProtocolVersion != 1 {
		return errors.New("native agy did not initialize ACP v1")
	}
	if err := r.send(2, "session/new", map[string]any{"cwd": cwd, "mcpServers": []Server{server}}); err != nil {
		return err
	}
	result, err = r.read("2", startup)
	var session struct{ SessionID string }
	if err != nil || json.Unmarshal(result, &session) != nil || !adapter.IsCanonicalConversationID(session.SessionID) {
		return errors.New("native agy did not start a session; check its login and project configuration")
	}
	startupDone()
	started = true
	evidence := Evidence{Attempt: attempt, Conversation: session.SessionID}
	save := func() error { return write(dir, "schedule-agy-evidence.json", evidence, false) }
	if err := save(); err != nil {
		return err
	}
	prompt, err := awaitPrompt(ctx, dir, attempt)
	if err != nil {
		return err
	}
	if err := r.send(3, "session/prompt", map[string]any{"sessionId": session.SessionID, "prompt": []map[string]string{{"type": "text", "text": prompt}}}); err != nil {
		return err
	}
	var closing strings.Builder
	overflow := false
	result, err = r.read("3", func(msg message) error {
		var event struct {
			SessionID string
			Update    struct {
				Kind    string `json:"sessionUpdate"`
				Content struct{ Type, Text string }
			}
			ToolCall struct{ Title string }
			Options  []struct{ OptionID, Name, Kind string }
		}
		if err := json.Unmarshal(msg.Params, &event); err != nil || event.SessionID != session.SessionID {
			if len(msg.ID) != 0 {
				return r.permission(msg.ID, "")
			}
			return nil
		}
		if msg.Method == "session/request_permission" {
			if len(event.Options) == 0 || len(event.Options) > 12 {
				return r.permission(msg.ID, "")
			}
			evidence.NeedsInput = true
			if err := save(); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(r.output, "\r\nAntigravity requests permission for %s\r\n", display(event.ToolCall.Title)); err != nil {
				return err
			}
			for index, option := range event.Options {
				if _, err := fmt.Fprintf(r.output, "%d. %s\r\n", index+1, display(option.Name)); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprint(r.output, "Enter an option number, or 0 to cancel.\r\n"); err != nil {
				return err
			}
			selection := ""
			if r.stdin.Scan() {
				index, err := strconv.Atoi(strings.TrimSpace(r.stdin.Text()))
				if err == nil && index > 0 && index <= len(event.Options) {
					selection = event.Options[index-1].OptionID
				}
			}
			if err := r.permission(msg.ID, selection); err != nil {
				return err
			}
			evidence.NeedsInput = false
			return save()
		}
		switch event.Update.Kind {
		case "agent_message_chunk":
			if event.Update.Content.Type == "text" {
				if !overflow && closing.Len()+len(event.Update.Content.Text) <= 64<<10 {
					closing.WriteString(event.Update.Content.Text)
				} else {
					overflow = true
				}
			}
		case "tool_call":
			closing.Reset()
			overflow = false
		}
		return nil
	})
	var finished struct{ StopReason string }
	if err != nil {
		return err
	}
	if json.Unmarshal(result, &finished) != nil || finished.StopReason != "end_turn" || overflow {
		return errors.New("native agy did not produce a bounded, completed assistant response")
	}
	evidence.EndTurn = true
	evidence.Text = closing.String()
	if err := save(); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(r.output, display(evidence.Text))
	return nil
}

func (r relay) permission(id json.RawMessage, option string) error {
	outcome := map[string]string{"outcome": "cancelled"}
	if option != "" {
		outcome = map[string]string{"outcome": "selected", "optionId": option}
	}
	return r.result(id, map[string]any{"outcome": outcome})
}

func display(text string) string {
	return strings.Map(func(ch rune) rune {
		if unicode.IsControl(ch) && ch != '\n' && ch != '\t' {
			return -1
		}
		return ch
	}, text)
}
