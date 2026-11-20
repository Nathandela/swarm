package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/voice"
)

func dispatchTeammate(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		_, _ = fmt.Fprintln(stdout, "swarm teammate message --text TEXT [--id MESSAGE_ID]\nswarm teammate reply --id MESSAGE_ID [--wait 90s]")
		return 0
	}
	if args[0] == "reply" {
		return dispatchTeammateReply(args[1:], stdout, stderr)
	}
	if args[0] != "message" {
		_, _ = fmt.Fprintln(stderr, "teammate: expected message --text TEXT or reply --id MESSAGE_ID")
		return misuseExit
	}
	fs := flag.NewFlagSet("teammate message", flag.ContinueOnError)
	fs.SetOutput(stderr)
	text := fs.String("text", "", "message to your own manager")
	id := fs.String("id", "", "message ID from the original attempt when retrying")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return misuseExit
	}
	if fs.NArg() != 0 || *text == "" {
		_, _ = fmt.Fprintln(stderr, "teammate: expected message --text TEXT")
		return misuseExit
	}
	if *id == "" {
		generated, err := protocol.NewSemanticOperationID()
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		*id = generated
	}
	if err := voice.SendDiscussionWorkerMessage(context.Background(), os.Getenv(schema.WorkerBridgeEnv), os.Getenv(hookclient.EnvSessionID), *id, *text); err != nil {
		_, _ = fmt.Fprintf(stderr, "%v (message ID %s)\n", err, *id)
		return 1
	}
	_ = json.NewEncoder(stdout).Encode(struct {
		State string `json:"state"`
		ID    string `json:"id"`
	}{"queued", *id})
	return 0
}

func dispatchTeammateReply(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("teammate reply", flag.ContinueOnError)
	fs.SetOutput(stderr)
	id := fs.String("id", "", "ID returned by teammate message")
	wait := fs.Duration("wait", 0, "time to wait for a reply, up to 2m")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return misuseExit
	}
	if *id == "" || fs.NArg() != 0 || *wait < 0 || *wait > 2*time.Minute {
		_, _ = fmt.Fprintln(stderr, "teammate: expected reply --id MESSAGE_ID [--wait 90s]")
		return misuseExit
	}
	ctx := context.Background()
	if *wait != 0 {
		waitCtx, cancel := context.WithTimeout(ctx, *wait)
		defer cancel()
		ctx = waitCtx
	}
	bridge, session := os.Getenv(schema.WorkerBridgeEnv), os.Getenv(hookclient.EnvSessionID)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	state, text := "pending", ""
	for {
		reply, err := voice.ReadDiscussionWorkerReply(ctx, bridge, session, *id)
		if errors.Is(err, context.DeadlineExceeded) {
			break
		}
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		if reply != "" {
			state, text = "available", reply
			break
		}
		if *wait == 0 {
			break
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
			continue
		}
		break
	}
	_ = json.NewEncoder(stdout).Encode(struct {
		State string `json:"state"`
		ID    string `json:"id"`
		Text  string `json:"text,omitempty"`
	}{state, *id, text})
	return 0
}
