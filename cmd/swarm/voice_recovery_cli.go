package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/voice"
)

func dispatchVoiceRecovery(command string, args []string, in io.Reader, out, errout io.Writer) int {
	fs := flag.NewFlagSet("voice "+command, flag.ContinueOnError)
	fs.SetOutput(errout)
	var instance, id, hash, answerID string
	var generation, external uint64
	var ack voice.InputRecoveryAck
	mutating := command != "question" && command != "replacement"
	if mutating {
		fs.StringVar(&instance, "instance", "", "session_instance from voice status")
		fs.Uint64Var(&generation, "generation", 0, "binding generation from voice status")
	}
	switch command {
	case "resume", "ack":
		fs.Uint64Var(&external, "external", 0, "observed external input sequence")
		if command == "ack" {
			fs.Uint64Var(&ack.Admission, "admission", 0, "uncertain admission sequence")
			fs.StringVar(&ack.OperationID, "operation", "", "original operation ID")
			fs.StringVar(&ack.NativeTurnID, "turn", "", "original native turn ID, if assigned")
		}
	case "answer":
		fs.StringVar(&id, "request", "", "manager clarification ID")
		fs.StringVar(&hash, "hash", "", "clarification hash")
		fs.StringVar(&answerID, "id", "", "stable answer ID")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return misuseExit
	}
	if fs.NArg() != 0 || (mutating && (generation == 0 || instance == "")) || (command == "ack" && (ack.Admission == 0 || ack.OperationID == "")) || (command == "answer" && (id == "" || hash == "" || answerID == "")) {
		_, _ = fmt.Fprintln(errout, "voice: missing expected binding or operation coordinates; use --help")
		return misuseExit
	}
	var answer json.RawMessage
	if command == "answer" {
		body, err := io.ReadAll(io.LimitReader(in, voice.MaxUtteranceBytes+1))
		if err != nil || len(body) == 0 || len(body) > voice.MaxUtteranceBytes || !utf8.Valid(body) {
			_, _ = fmt.Fprintln(errout, "voice: expected a bounded UTF-8 answer on stdin")
			return misuseExit
		}
		answer, _ = json.Marshal(string(body))
	}
	client := voice.NewManagerClient(filepath.Join(resolveStateDir(), "voice"))
	defer client.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	var result any
	var err error
	var status voice.ManagerStatus
	if mutating {
		status, err = client.Status(ctx)
		if err == nil && (status.Binding.Generation != generation || status.Binding.SessionInstance != instance) {
			err = voice.ErrStale
		}
	}
	if err == nil {
		switch command {
		case "question":
			result, err = client.Clarification(ctx)
		case "answer":
			result, err = client.AnswerQuestion(ctx, status.Binding, id, hash, answerID, answer)
		case "resume":
			err = client.ResumeManual(ctx, status.Binding, external)
		case "ack":
			ack.ExternalInput = external
			err = client.AcknowledgeUncertainty(ctx, status.Binding, ack)
		case "replace":
			result, err = client.ReplaceManager(ctx, status.Binding)
		case "replacement":
			result, err = client.Replacement(ctx)
		}
	}
	stop()
	if err == nil && result != nil {
		err = json.NewEncoder(out).Encode(result)
	}
	if err != nil {
		_, _ = fmt.Fprintln(errout, err)
		return 1
	}
	return 0
}
