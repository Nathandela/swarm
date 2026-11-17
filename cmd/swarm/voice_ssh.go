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
	"github.com/Nathandela/swarm/internal/voicessh"
)

func dispatchVoiceSSH(args []string, out, errout io.Writer) int {
	fs := flag.NewFlagSet("voice --host", flag.ContinueOnError)
	fs.SetOutput(errout)
	host := fs.String("host", "", "OpenSSH alias of the manager machine")
	enroll := fs.Bool("enroll", false, "explicitly enroll this desktop if needed")
	printURL := fs.Bool("print-url", false, "print the private local browser URL")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return misuseExit
	}
	if fs.NArg() != 0 || !voicessh.ValidAlias(*host) {
		_, _ = fmt.Fprintln(errout, voicessh.ErrInvalidAlias)
		return misuseExit
	}
	if !*printURL && os.Getenv("SSH_CONNECTION") != "" {
		_, _ = fmt.Fprintln(errout, "voice: run the SSH panel on the machine with your microphone")
		return 1
	}
	root := resolveStateDir()
	if !filepath.IsAbs(root) {
		_, _ = fmt.Fprintln(errout, "voice: cannot locate the Swarm state directory")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	connection, err := voicessh.Open(ctx, filepath.Join(root, "voice", "ssh"), *host, *enroll)
	if err != nil {
		_, _ = fmt.Fprintln(errout, err)
		return 1
	}
	defer connection.Close()
	err = voicepanel.ServeDevice(ctx, connection.Client, connection.Credential, *host, func(url string) error {
		if *printURL {
			_, err := fmt.Fprintln(out, url)
			return err
		}
		opener := "xdg-open"
		if runtime.GOOS == "darwin" {
			opener = "open"
		}
		if err := exec.CommandContext(ctx, opener, url).Run(); err != nil {
			return errors.New("voice: could not open the local browser; add --print-url")
		}
		return nil
	})
	if err != nil {
		_, _ = fmt.Fprintln(errout, err)
		return 1
	}
	return 0
}
