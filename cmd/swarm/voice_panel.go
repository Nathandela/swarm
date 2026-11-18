package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/Nathandela/swarm/internal/voicepanel"
)

func dispatchVoicePanel(args []string, out, errout io.Writer) int {
	fs := flag.NewFlagSet("voice panel", flag.ContinueOnError)
	fs.SetOutput(errout)
	printURL := fs.Bool("print-url", false, "print the private local browser URL instead of opening it")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return misuseExit
	}
	if fs.NArg() != 0 {
		return misuseExit
	}
	root := resolveStateDir()
	if !filepath.IsAbs(root) {
		_, _ = fmt.Fprintln(errout, "voice: cannot locate the Swarm state directory")
		return 1
	}
	if !*printURL && os.Getenv("SSH_CONNECTION") != "" {
		_, _ = fmt.Fprintln(errout, "voice: run the browser panel on the machine with your microphone")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := voicepanel.Serve(ctx, filepath.Join(root, "voice"), func(url string) error {
		if *printURL {
			_, err := fmt.Fprintln(out, url)
			return err
		}
		opener := "xdg-open"
		if runtime.GOOS == "darwin" {
			opener = "open"
		}
		if err := exec.CommandContext(ctx, opener, url).Run(); err != nil {
			return errors.New("voice: could not open the local browser; run swarm voice panel --print-url")
		}
		return nil
	})
	if err != nil {
		_, _ = fmt.Fprintln(errout, err)
		return 1
	}
	return 0
}
