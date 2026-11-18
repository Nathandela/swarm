package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
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
	if len(args) == 0 {
		return dispatchVoicePanel(nil, stdout, stderr)
	}
	if args[0] == "--host" || strings.HasPrefix(args[0], "--host=") {
		return dispatchVoiceSSH(args, stdout, stderr)
	}
	switch args[0] {
	case "interrupt-manager", "interrupt-status":
		return dispatchManagerInterrupt(args[0], args[1:], os.Stdin, stdout, stderr)
	case "capture":
		return dispatchVoiceCapture(args[1:], stdout, stderr)
	case "enroll", "revoke", "device-endpoint", "devices":
		return dispatchVoiceDevice(args[0], args[1:], stdout, stderr)
	case "start", "stop":
		return dispatchVoiceUnit(args[0], args[1:], stdout, stderr)
	case "panel":
		return dispatchVoicePanel(args[1:], stdout, stderr)
	case "question", "answer", "resume", "ack", "replace", "replacement":
		return dispatchVoiceRecovery(args[0], args[1:], os.Stdin, stdout, stderr)
	case "serve", "status", "text", "ptt", "read", "receipt", "publications", "switch":
		return dispatchVoiceManager(args, os.Stdin, stdout, stderr)
	case "setup":
		if len(args) > 1 {
			return dispatchVoiceManager(args, os.Stdin, stdout, stderr)
		}
	}
	if args[0] != "mcp" {
		return dispatchSpeech(args, os.Stdin, stdout, stderr)
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
