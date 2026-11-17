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

	"github.com/Nathandela/swarm/internal/voice"
)

func dispatchVoiceCapture(args []string, out, errout io.Writer) int {
	fs := flag.NewFlagSet("voice capture", flag.ContinueOnError)
	fs.SetOutput(errout)
	end := fs.String("end", "", "end the exact current lease ID")
	take := fs.String("take", "", "replace the exact current lease with local push-to-talk")
	id := fs.String("id", "", "new local push-to-talk utterance ID for --take")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return misuseExit
	}
	if fs.NArg() != 0 || (*take == "") != (*id == "") || (*take != "" && *end != "") {
		_, _ = fmt.Fprintln(errout, "voice capture [--end EXPECTED_LEASE | --take EXPECTED_LEASE --id NEW_UTTERANCE]")
		return misuseExit
	}
	c := voice.NewManagerClient(filepath.Join(resolveStateDir(), "voice"))
	defer c.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	var reply any
	var err error
	switch {
	case *take != "":
		reply, err = c.TakeCapture(ctx, *take, *id)
	case *end != "":
		err = c.EndCapture(ctx, *end)
	default:
		reply, err = c.Capture(ctx)
	}
	stop()
	if err == nil && reply != nil {
		err = json.NewEncoder(out).Encode(reply)
	}
	if err != nil {
		_, _ = fmt.Fprintln(errout, err)
		return 1
	}
	return 0
}
