package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/status"
)

type semanticClient interface {
	SemanticSend(string, protocol.ComposerSendReq) error
}

type semanticReceiptClient interface {
	SemanticOperation(session, operationID string) (protocol.SemanticMessageReceipt, error)
}

func semanticRequest(c agentClient, id, text string) (semanticClient, protocol.ComposerSendReq, error) {
	sender, ok := c.(semanticClient)
	if !ok {
		return nil, protocol.ComposerSendReq{}, errors.New("semantic messages require an updated daemon")
	}
	row, err := pilotTarget(c, id)
	if err != nil {
		return nil, protocol.ComposerSendReq{}, err
	}
	if row.Status.Process != status.ProcessRunning {
		return nil, protocol.ComposerSendReq{}, errors.New("discussion is not running; resume it explicitly")
	}
	if row.Capabilities == nil || row.Capabilities.SessionInstance == "" {
		return nil, protocol.ComposerSendReq{}, errors.New("discussion has no current session incarnation; refresh the roster and daemon")
	}
	return sender, protocol.ComposerSendReq{Session: id, SessionInstance: row.Capabilities.SessionInstance, Text: text}, nil
}

func runMessage(args []string, c agentClient, stdout, stderr io.Writer) int {
	id, rest, ok := takeSessionID(args)
	if !ok {
		_, _ = fmt.Fprintln(stderr, "message: swarm message <session> --text <exact message> [--operation-id <id>]")
		return misuseExit
	}
	fs := flag.NewFlagSet("message", flag.ContinueOnError)
	fs.SetOutput(stderr)
	text := fs.String("text", "", "exact semantic message")
	statusOnly := fs.Bool("status", false, "read the existing operation without submitting")
	operation := fs.String("operation-id", "", "reuse this operation ID only for an exact retry within one hour")
	if fs.Parse(rest) != nil || fs.NArg() != 0 || (*text != "") == *statusOnly || *statusOnly && *operation == "" {
		_, _ = fmt.Fprintln(stderr, "message: requires --text <exact message> or --status --operation-id <id>")
		return misuseExit
	}
	if *statusOnly {
		reader, ok := c.(semanticReceiptClient)
		if !ok {
			_, _ = fmt.Fprintln(stderr, "message: semantic receipt reads are unavailable")
			return 1
		}
		receipt, err := reader.SemanticOperation(id, *operation)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "message: operation_id=%s: %v\n", *operation, err)
			return 1
		}
		return pilotJSON(stdout, stderr, receipt)
	}
	sender, req, err := semanticRequest(c, id, *text)
	if err == nil && *operation == "" {
		*operation, err = protocol.NewSemanticOperationID()
	}
	if err == nil {
		err = sender.SemanticSend(*operation, req)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "message: operation_id=%s: %v\n", *operation, err)
		return 1
	}
	receipt := protocol.SemanticMessageReceipt{Session: id, OperationID: *operation, State: "submitted"}
	if reader, ok := c.(semanticReceiptClient); ok {
		if observed, readErr := reader.SemanticOperation(id, *operation); readErr == nil {
			receipt = observed
		}
	}
	if err := writeJSON(stdout, receipt); err != nil {
		_, _ = fmt.Fprintf(stderr, "message: operation_id=%s: write receipt: %v\n", *operation, err)
		return 1
	}
	return 0
}
