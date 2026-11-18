package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/voice"
)

func dispatchManagerInterrupt(command string, args []string, in io.Reader, out, errout io.Writer) int {
	if len(args) != 0 {
		if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
			_, _ = fmt.Fprintf(out, "swarm voice %s < saved-manager-status.json\n", command)
			return 0
		}
		return misuseExit
	}
	raw, err := io.ReadAll(io.LimitReader(in, voice.MaxControlFrameBytes+1))
	var target voice.ManagerStatus
	if err != nil || len(raw) > voice.MaxControlFrameBytes || json.Unmarshal(raw, &target) != nil || target.Binding.Generation == 0 || target.NativeInputID == "" || target.NativeTurnID == "" {
		_, _ = fmt.Fprintln(errout, "voice: provide a saved voice status containing one active manager input and turn")
		return misuseExit
	}
	client := voice.NewManagerClient(filepath.Join(resolveStateDir(), "voice"))
	defer client.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	var receipt protocol.ManagerInterruptReceipt
	if command == "interrupt-manager" {
		receipt, err = client.InterruptManager(ctx, target.Binding, target.NativeInputID, target.NativeTurnID)
	} else {
		receipt, err = client.ManagerInterruptStatus(ctx, target.Binding, target.NativeInputID, target.NativeTurnID)
	}
	stop()
	if err == nil {
		err = json.NewEncoder(out).Encode(receipt)
	}
	if err != nil {
		_, _ = fmt.Fprintln(errout, err)
		return 1
	}
	return 0
}
