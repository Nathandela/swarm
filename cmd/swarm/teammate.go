package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/voice"
)

func dispatchTeammate(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		_, _ = fmt.Fprintln(stdout, "swarm teammate message --text TEXT [--id MESSAGE_ID]")
		return 0
	}
	if args[0] != "message" {
		_, _ = fmt.Fprintln(stderr, "teammate: expected message --text TEXT")
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
