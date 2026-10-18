package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/Nathandela/swarm/internal/protocol"
)

type nativeRequestClient interface {
	NativeRequests(protocol.NativeRequestsReq) (protocol.NativeRequestsReply, error)
	NativeDecide(string, protocol.NativeDecisionReq) (protocol.NativeDecisionReceipt, error)
}

func runNativeRequests(args []string, c agentClient, stdout, stderr io.Writer) int {
	id, rest, ok := takeSessionID(args)
	if !ok {
		return nativeRequestFailure(stderr, errors.New("requests requires a session"), misuseExit)
	}
	fs := flag.NewFlagSet("requests", flag.ContinueOnError)
	fs.SetOutput(stderr)
	request := fs.String("request-id", "", "read this exact request")
	target := fs.String("native-session-id", "", "native root or child that owns the request")
	if fs.Parse(rest) != nil || fs.NArg() != 0 || (*request == "") != (*target == "") {
		return nativeRequestFailure(stderr, errors.New("requests requires both --request-id and --native-session-id, or neither"), misuseExit)
	}
	client, ok := c.(nativeRequestClient)
	if !ok {
		return nativeRequestFailure(stderr, errors.New("native request reads require an updated daemon"), 1)
	}
	result, err := client.NativeRequests(protocol.NativeRequestsReq{Session: id, RequestID: *request, NativeSessionID: *target})
	if err != nil {
		return nativeRequestFailure(stderr, err, 1)
	}
	return pilotJSON(stdout, stderr, result)
}

func runNativeDecide(args []string, c agentClient, stdout, stderr io.Writer) int {
	id, rest, ok := takeSessionID(args)
	if !ok {
		return nativeRequestFailure(stderr, errors.New("decide requires a session"), misuseExit)
	}
	fs := flag.NewFlagSet("decide", flag.ContinueOnError)
	fs.SetOutput(stderr)
	request := fs.String("request-id", "", "exact request ID from swarm requests")
	target := fs.String("native-session-id", "", "native root or child that owns the request")
	hash := fs.String("request-hash", "", "content hash of the complete request you inspected")
	reply := fs.String("reply", "", "once|always|reject for permissions; answer|reject for questions")
	answers := fs.String("answers", "", "JSON array of answer groups, in question order")
	status := fs.Bool("status", false, "read the original operation receipt without submitting")
	operationID := fs.String("operation-id", "", "original operation ID for --status")
	if fs.Parse(rest) != nil || fs.NArg() != 0 {
		return misuseExit
	}
	if *status || *operationID != "" {
		writeFlag := false
		fs.Visit(func(f *flag.Flag) {
			if f.Name != "status" && f.Name != "operation-id" {
				writeFlag = true
			}
		})
		if !*status || *operationID == "" || writeFlag {
			return nativeRequestFailure(stderr, errors.New("receipt reads require --status --operation-id ID without decision flags"), misuseExit)
		}
		reader, ok := c.(interface {
			NativeDecisionStatus(string, string) (protocol.NativeDecisionReceipt, error)
		})
		if !ok {
			return nativeRequestFailure(stderr, errors.New("native decision status requires an updated daemon"), 1)
		}
		receipt, err := reader.NativeDecisionStatus(id, *operationID)
		if err != nil {
			return nativeRequestFailure(stderr, err, 1)
		}
		return pilotJSON(stdout, stderr, receipt)
	}
	if *request == "" || *target == "" || *hash == "" || *reply == "" {
		return nativeRequestFailure(stderr, errors.New("decide requires --request-id, --native-session-id, --request-hash and --reply"), misuseExit)
	}
	client, ok := c.(nativeRequestClient)
	if !ok {
		return nativeRequestFailure(stderr, errors.New("native decisions require an updated daemon"), 1)
	}
	result, err := client.NativeRequests(protocol.NativeRequestsReq{Session: id, RequestID: *request, NativeSessionID: *target})
	if err != nil {
		return nativeRequestFailure(stderr, err, 1)
	}
	card := result.Request
	if card == nil || card.Session != id || card.RequestID != *request || card.NativeSessionID != *target || card.RequestHash != *hash {
		return nativeRequestFailure(stderr, errors.New("request content changed or is unavailable; inspect its complete current card before answering"), 1)
	}
	body := protocol.NativeDecisionReq{Session: id, SessionInstance: card.SessionInstance, RootID: card.RootID, Generation: card.Generation, NativeSessionID: *target, RequestID: *request, RequestHash: *hash, ExpiresAt: card.ExpiresAt}
	switch card.Kind {
	case "permission":
		body.Kind, body.Reply = "permission_reply", *reply
		if strings.TrimSpace(*answers) != "" {
			return nativeRequestFailure(stderr, errors.New("permission replies do not carry question answers"), misuseExit)
		}
	case "question":
		switch *reply {
		case "reject":
			body.Kind = "question_reject"
			if strings.TrimSpace(*answers) != "" {
				return nativeRequestFailure(stderr, errors.New("question rejection does not carry answers"), misuseExit)
			}
		case "answer":
			body.Kind = "question_reply"
			if len(*answers) > 32<<10 || json.Unmarshal([]byte(*answers), &body.Answers) != nil {
				return nativeRequestFailure(stderr, errors.New("answers must be a bounded JSON array of string groups"), misuseExit)
			}
		default:
			return nativeRequestFailure(stderr, errors.New("question reply must be answer or reject"), misuseExit)
		}
	default:
		return nativeRequestFailure(stderr, errors.New("unsupported native request kind"), 1)
	}
	if err := protocol.ValidateNativeDecision(body); err != nil {
		return nativeRequestFailure(stderr, err, misuseExit)
	}
	operation := "native:" + rand.Text()
	if _, err := fmt.Fprintf(stderr, "native decision operation_id=%s\n", operation); err != nil {
		return 1
	}
	receipt, err := client.NativeDecide(operation, body)
	if err != nil {
		return nativeRequestFailure(stderr, fmt.Errorf("operation_id=%s: %w", operation, err), 1)
	}
	if code := pilotJSON(stdout, stderr, receipt); code != 0 {
		return code
	}
	if receipt.State != "admitted" {
		return 1
	}
	return 0
}

func nativeRequestFailure(stderr io.Writer, err error, exitCode int) int {
	_, _ = fmt.Fprintf(stderr, "native requests: %v\n", err)
	return exitCode
}
