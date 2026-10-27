// Command swarm-fake-agent is a scripted stand-in for a real agent CLI,
// driven by internal/fakeagent. It is a dev/test binary only, never
// shipped (E1.9).
package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/shimwire"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/fakeagent"
	"github.com/charmbracelet/x/term"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run is main's testable body. --stdin-log is deliberately an explicit dev/test
// flag rather than a launch environment variable: supervised agent environments
// are security-allowlisted, and a fixture must not widen that production contract.
// When enabled, every byte the scripted agent consumes from stdin is copied to a
// private file before Run interprets it.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "--claude-primary-fixture" {
		return runClaudePrimaryFixture(args, stdin, stdout, stderr)
	}
	scriptPath, stdinLog, raw, ok := parseArgs(args)
	if !ok {
		_, _ = fmt.Fprintln(stderr, "usage: swarm-fake-agent [--stdin-log PATH|--raw-stdin-log PATH] <script-path|->")
		return 2
	}

	var script io.Reader
	fromStdin := scriptPath == "-"
	if fromStdin {
		script = stdin
	} else {
		f, err := os.Open(scriptPath)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 2
		}
		defer func() { _ = f.Close() }()
		script = f
	}
	if raw {
		state, rawErr := term.MakeRaw(os.Stdin.Fd())
		if rawErr != nil {
			_, _ = fmt.Fprintln(stderr, rawErr)
			return 2
		}
		defer func() { _ = term.Restore(os.Stdin.Fd(), state) }()
	}

	steps, err := fakeagent.Parse(script)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}

	// When the script itself came from stdin, stdin is already consumed, so an
	// ask step has no channel left to read its answer from. Reject before running.
	if fromStdin {
		for _, s := range steps {
			if s.Kind == fakeagent.KindAsk {
				_, _ = fmt.Fprintln(stderr, "ask requires a script file (stdin is consumed by the script)")
				return 2
			}
		}
	}

	if stdinLog != "" {
		logFile, openErr := os.OpenFile(stdinLog, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if openErr != nil {
			_, _ = fmt.Fprintln(stderr, openErr)
			return 2
		}
		defer func() { _ = logFile.Close() }()
		// OpenFile's mode only governs creation. Pin an existing opt-in log back to
		// the same private mode too, so the side channel never leaves readable input.
		if chmodErr := logFile.Chmod(0o600); chmodErr != nil {
			_, _ = fmt.Fprintln(stderr, chmodErr)
			return 2
		}
		stdin = io.TeeReader(stdin, logFile)
	}

	code, err := fakeagent.Run(steps, stdin, stdout, time.Sleep)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 3
	}
	return code
}

// parseArgs preserves the original one-positional-argument interface and adds
// one opt-in prefix. As before, trailing arguments are ignored by the fixture.
func parseArgs(args []string) (scriptPath, stdinLog string, raw, ok bool) {
	if len(args) == 0 {
		return "", "", false, false
	}
	if args[0] != "--stdin-log" && args[0] != "--raw-stdin-log" {
		return args[0], "", false, true
	}
	if len(args) < 3 || args[1] == "" || args[2] == "" {
		return "", "", false, false
	}
	return args[2], args[1], args[0] == "--raw-stdin-log", true
}

// A finite dev-only stdio primary; it sends no automatic user message and
// accepts no chat commands. Process termination owns its blocked input reader.
const claudeFixtureTranscriptFile = "claude-fixture-transcript.jsonl"

func runClaudePrimaryFixture(args []string, stdin io.Reader, stdout, stderr io.Writer) (code int) {
	if len(args) != 2 || !strings.HasPrefix(args[1], "--session-id=") {
		return 2
	}
	id := strings.TrimPrefix(args[1], "--session-id=")
	if !adapter.IsCanonicalConversationID(id) {
		return 2
	}
	var transcript *os.File
	if dir := os.Getenv("SWARM_CLAUDE_PRIMARY_FIXTURE_DIR"); dir != "" {
		file, err := openClaudeFixtureTranscript(dir)
		if err != nil {
			return 3
		}
		transcript = file
		defer func() {
			if file.Close() != nil {
				code = 3
			}
		}()
	}
	tty := func(v any) bool { f, ok := v.(*os.File); return ok && term.IsTerminal(f.Fd()) }
	type fixtureStdio struct {
		StdinTTY  bool `json:"stdin_tty"`
		StdoutTTY bool `json:"stdout_tty"`
		StderrTTY bool `json:"stderr_tty"`
	}
	pgid, err := syscall.Getpgid(0)
	if err != nil {
		return 3
	}
	init := struct {
		Type    string       `json:"type"`
		Subtype string       `json:"subtype"`
		Session string       `json:"session_id"`
		PID     int          `json:"fixture_pid"`
		PGID    int          `json:"fixture_pgid"`
		Stdio   fixtureStdio `json:"fixture_stdio"`
	}{"system", "init", id, os.Getpid(), pgid, fixtureStdio{tty(stdin), tty(stdout), tty(stderr)}}
	if json.NewEncoder(stdout).Encode(init) != nil {
		return 3
	}
	telemetry := struct {
		Fixture   string `json:"fixture"`
		StdinTTY  bool   `json:"stdin_tty"`
		StdoutTTY bool   `json:"stdout_tty"`
		StderrTTY bool   `json:"stderr_tty"`
		PID       int    `json:"fixture_pid"`
		PGID      int    `json:"fixture_pgid"`
	}{"claude-primary", tty(stdin), tty(stdout), tty(stderr), os.Getpid(), pgid}
	if json.NewEncoder(stderr).Encode(telemetry) != nil {
		return 3
	}
	done := make(chan int, 1)
	go func() { done <- runClaudeFixtureTurn(id, stdin, stdout, transcript) }()
	timer := time.NewTimer(12 * time.Second)
	defer timer.Stop()
	select {
	case code := <-done:
		return code
	case <-timer.C:
		return 0
	}
}

func openClaudeFixtureTranscript(dir string) (*os.File, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return nil, fmt.Errorf("invalid fixture directory")
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm()&0077 != 0 || owner.Uid != uint32(os.Getuid()) {
		return nil, fmt.Errorf("unsafe fixture directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	current, err := root.Stat(".")
	if err != nil || !os.SameFile(info, current) {
		return nil, fmt.Errorf("fixture directory changed")
	}
	f, err := root.OpenFile(claudeFixtureTranscriptFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Sync(); err == nil {
		var d *os.File
		d, err = root.Open(".")
		if err == nil {
			err = d.Sync()
			_ = d.Close()
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func claudeFixtureLine(rd *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		part, err := rd.ReadSlice('\n')
		if len(part) > (1<<20)+1-len(line) {
			return nil, fmt.Errorf("fixture input exceeds bound")
		}
		line = append(line, part...)
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			if err == io.EOF && len(line) == 0 {
				return nil, io.EOF
			}
			return nil, io.ErrUnexpectedEOF
		}
		return line, nil
	}
}

func claudeFixtureUUID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	id[6], id[8] = id[6]&15|64, id[8]&63|128
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]), nil
}

func runClaudeFixtureTurn(session string, input io.Reader, output io.Writer, transcript *os.File) int {
	reader := bufio.NewReaderSize(input, 32<<10)
	line, err := claudeFixtureLine(reader)
	if err == io.EOF {
		return 0
	}
	if err != nil || transcript == nil {
		return 3
	}
	var user struct {
		Type    string          `json:"type"`
		UUID    string          `json:"uuid"`
		Session string          `json:"session_id"`
		Parent  json.RawMessage `json:"parent_tool_use_id"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&user) != nil || user.Type != "user" || !adapter.IsCanonicalConversationID(user.UUID) || user.Session != session || !bytes.Equal(bytes.TrimSpace(user.Parent), []byte("null")) || user.Message.Role != "user" || len(user.Message.Content) != 1 || user.Message.Content[0].Type != "text" {
		return 3
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return 3
	}
	text := user.Message.Content[0].Text
	if len(text) == 0 || len(text) > shimwire.MaxClaudeFixtureTextBytes || !utf8.ValidString(text) {
		return 3
	}
	assistantID, err := claudeFixtureUUID()
	if err != nil {
		return 3
	}
	resultID, err := claudeFixtureUUID()
	if err != nil {
		return 3
	}
	if assistantID == user.UUID || resultID == assistantID || resultID == user.UUID {
		return 3
	}
	reply := "fixture reply: " + text
	type message struct {
		ID      string `json:"id,omitempty"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	assistantMessage := message{ID: assistantID, Role: "assistant", Content: []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{{"text", reply}}}
	assistant := struct {
		Type    string  `json:"type"`
		UUID    string  `json:"uuid"`
		Session string  `json:"session_id"`
		Parent  any     `json:"parent_tool_use_id"`
		Message message `json:"message"`
	}{"assistant", assistantID, session, nil, assistantMessage}
	result := struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		UUID    string `json:"uuid"`
		Session string `json:"session_id"`
		IsError bool   `json:"is_error"`
		Result  string `json:"result"`
	}{"result", "success", resultID, session, false, reply}
	type row struct {
		Version int     `json:"v"`
		Type    string  `json:"type"`
		UUID    string  `json:"uuid"`
		Parent  *string `json:"parentUuid"`
		Session string  `json:"sessionId"`
		Message any     `json:"message,omitempty"`
		Subtype string  `json:"subtype,omitempty"`
		Result  string  `json:"result,omitempty"`
	}
	rows := []row{{1, "user", user.UUID, nil, session, user.Message, "", ""}, {1, "assistant", assistantID, &user.UUID, session, assistantMessage, "", ""}, {1, "result", resultID, &assistantID, session, nil, "success", reply}}
	encoded := make([][]byte, 3)
	native := make([][]byte, 3)
	native[0] = line
	native[1], err = json.Marshal(assistant)
	if err != nil {
		return 3
	}
	native[1] = append(native[1], '\n')
	native[2], err = json.Marshal(result)
	if err != nil {
		return 3
	}
	native[2] = append(native[2], '\n')
	total := 0
	for i, r := range rows {
		body, err := json.Marshal(r)
		if err != nil {
			return 3
		}
		encoded[i] = append(body, '\n')
		total += len(encoded[i])
	}
	if total > 512<<10 {
		return 3
	}
	for i, body := range encoded {
		n, err := transcript.Write(body)
		if err != nil || n != len(body) || transcript.Sync() != nil {
			return 3
		}
		n, err = output.Write(native[i])
		if err != nil || n != len(native[i]) {
			return 3
		}
	}
	// Only one main turn is implemented. A second line is not a new inference.
	if _, err := claudeFixtureLine(reader); err == io.EOF {
		return 0
	}
	return 3
}
