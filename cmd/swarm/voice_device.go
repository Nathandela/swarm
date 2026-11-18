package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
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

func dispatchVoiceDevice(command string, args []string, out, errout io.Writer) int {
	fs := flag.NewFlagSet("voice "+command, flag.ContinueOnError)
	fs.SetOutput(errout)
	var public, id string
	if command == "enroll" {
		fs.StringVar(&public, "device-key", "", "base64url Ed25519 public device key")
	}
	if command == "revoke" {
		fs.StringVar(&id, "id", "", "enrolled voice device ID")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return misuseExit
	}
	var key []byte
	if command == "enroll" {
		key, _ = base64.RawURLEncoding.Strict().DecodeString(public)
	}
	if fs.NArg() != 0 || (command == "enroll" && (len(key) != ed25519.PublicKeySize || public != base64.RawURLEncoding.EncodeToString(key))) || (command == "revoke" && id == "") {
		_, _ = fmt.Fprintln(errout, "voice: missing or invalid device coordinates; use --help")
		return misuseExit
	}
	client := voice.NewManagerClient(filepath.Join(resolveStateDir(), "voice"))
	defer client.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	var result any
	var err error
	switch command {
	case "devices":
		result, err = client.ListDevices(ctx)
	case "device-endpoint":
		result, err = client.DeviceEndpoint(ctx)
	case "enroll":
		result, err = client.IssueDeviceEnrollment(ctx, key)
	case "revoke":
		err = client.RevokeDevice(ctx, id)
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
