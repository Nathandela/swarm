package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"testing"

	"github.com/Nathandela/swarm/internal/voice"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type voiceWriter struct{ io.Writer }

func (voiceWriter) Close() error { return nil }

var voiceMCPRun = func(ctx context.Context, socket, auth, conversation, session string, stdout io.Writer) error {
	if testing.Testing() {
		return errors.New("voice: refusing a live manager bridge inside a test binary")
	}
	client, err := voice.NewManagerBridgeClient(socket, auth, conversation, session)
	if err != nil {
		return err
	}
	defer client.Close()
	return voice.NewManagerMCP(client.Call).Run(ctx, &mcp.IOTransport{Reader: os.Stdin, Writer: voiceWriter{stdout}, MaxLineLength: voice.MaxControlFrameBytes})
}

func dispatchVoice(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "mcp" {
		_, _ = fmt.Fprintln(stderr, "swarm voice mcp --socket <private-socket> --auth-file <private-file> --conversation <id>")
		return misuseExit
	}
	fs := flag.NewFlagSet("voice mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", "", "private controller socket")
	auth := fs.String("auth-file", "", "private manager credential file")
	conversation := fs.String("conversation", "", "manager conversation")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return misuseExit
	}
	session := os.Getenv("SWARM_SESSION_ID")
	if fs.NArg() != 0 || *socket == "" || *auth == "" || *conversation == "" || session == "" {
		_, _ = fmt.Fprintln(stderr, "voice: MCP requires the configured bridge and a Swarm-owned manager session")
		return misuseExit
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := voiceMCPRun(ctx, *socket, *auth, *conversation, session, stdout); err != nil && ctx.Err() == nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
