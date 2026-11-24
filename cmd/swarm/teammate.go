package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/voice"
	"github.com/Nathandela/swarm/internal/workermail"
)

func dispatchTeammate(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		_, _ = fmt.Fprintln(stdout, "swarm teammate message --text TEXT [--kind needs-input|complete] [--id MESSAGE_ID]\nswarm teammate message --kind complete --file PATH [--file PATH ...] [--text CAPTION] [--id MESSAGE_ID]\nswarm teammate receipt --id MESSAGE_ID\nswarm teammate reply --id MESSAGE_ID [--wait 90s]\nswarm dispatcher-guide    read the dispatcher workflow")
		return 0
	}
	if args[0] == "receipt" {
		fs := flag.NewFlagSet("teammate receipt", flag.ContinueOnError)
		fs.SetOutput(stderr)
		id := fs.String("id", "", "message ID")
		if fs.Parse(args[1:]) != nil || fs.NArg() != 0 || *id == "" {
			return misuseExit
		}
		var err error
		r := schema.WorkerMailReceipt{ID: *id}
		if os.Getenv(workermail.SocketEnv) != "" {
			r, err = ordinaryWorkerMail(context.Background(), schema.WorkerMailReq{Action: "receipt", ID: *id})
		} else {
			r.State, err = voice.DiscussionWorkerReceipt(context.Background(), os.Getenv(schema.WorkerBridgeEnv), os.Getenv(hookclient.EnvSessionID), *id)
		}
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		_ = json.NewEncoder(stdout).Encode(r)
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
	kind := fs.String("kind", "", "needs-input or complete")
	text := fs.String("text", "", "message to your own manager")
	id := fs.String("id", "", "message ID from the original attempt when retrying")
	var files []string
	fs.Func("file", "attach a PNG, JPEG, PDF or CSV from this worker's checkout (repeatable, up to 8 MiB total)", func(path string) error {
		if path == "" || len(files) >= schema.MaxPublicationFiles {
			return errors.New("provide 1 to 10 file paths")
		}
		absolute, err := filepath.Abs(path)
		if err != nil || len(absolute) > 4096 {
			return errors.New("unable to resolve the file path")
		}
		files = append(files, absolute)
		return nil
	})
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return misuseExit
	}
	if fs.NArg() != 0 || *text == "" && len(files) == 0 || !workermail.ValidKind(*kind) {
		_, _ = fmt.Fprintln(stderr, "teammate: provide --text TEXT or attach a file with --kind complete --file PATH")
		return misuseExit
	}
	if len(files) > 0 && *kind != "complete" {
		_, _ = fmt.Fprintln(stderr, "teammate: files require --kind complete")
		return misuseExit
	}
	if len(files) > 0 && os.Getenv(workermail.SocketEnv) == "" {
		_, _ = fmt.Fprintln(stderr, "teammate: files require a mailbox-enabled Swarm worker")
		return 1
	}
	if *id == "" {
		generated, err := protocol.NewSemanticOperationID()
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		*id = generated
	}
	var err error
	receipt := schema.WorkerMailReceipt{ID: *id, State: "queued"}
	if os.Getenv(workermail.SocketEnv) != "" {
		receipt, err = ordinaryWorkerMail(context.Background(), schema.WorkerMailReq{Action: "message", ID: *id, Kind: *kind, Text: *text, Paths: files})
	} else {
		err = voice.SendDiscussionWorkerMessage(context.Background(), os.Getenv(schema.WorkerBridgeEnv), os.Getenv(hookclient.EnvSessionID), *id, *text, *kind)
		if errors.Is(err, voice.ErrWorkerReplySuperseded) {
			receipt.State, err = "superseded", nil
		}
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "%v (message ID %s)\n", err, *id)
		return 1
	}
	_ = json.NewEncoder(stdout).Encode(receipt)
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
		var reply string
		var err error
		if os.Getenv(workermail.SocketEnv) != "" {
			var r schema.WorkerMailReceipt
			r, err = ordinaryWorkerMail(ctx, schema.WorkerMailReq{Action: "reply", ID: *id})
			if err == nil && r.State == "superseded" {
				state = r.State
				break
			}
			reply = r.Text
		} else {
			reply, err = voice.ReadDiscussionWorkerReply(ctx, bridge, session, *id)
			if errors.Is(err, voice.ErrWorkerReplySuperseded) {
				state = "superseded"
				break
			}
		}
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

func ordinaryWorkerMail(ctx context.Context, req schema.WorkerMailReq) (schema.WorkerMailReceipt, error) {
	socket, session := os.Getenv(workermail.SocketEnv), os.Getenv(hookclient.EnvSessionID)
	req.Proof = os.Getenv(workermail.WorkerProofEnv)
	if socket == "" || session == "" || req.Proof == "" {
		return schema.WorkerMailReceipt{}, errors.New("run this command inside a mailbox-enabled worker")
	}
	c, err := protocol.DialContext(ctx, socket, []string{protocol.CapWorkerMail, protocol.CapWorkerMailFiles})
	if err != nil {
		return schema.WorkerMailReceipt{}, err
	}
	defer func() { _ = c.Close() }()
	receipt, err := c.WorkerMail(session, req)
	if ctx.Err() != nil {
		return schema.WorkerMailReceipt{}, ctx.Err()
	}
	return receipt, err
}
