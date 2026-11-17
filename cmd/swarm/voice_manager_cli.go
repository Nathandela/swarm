package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/skeleton"
	"github.com/Nathandela/swarm/internal/voice"
	"github.com/charmbracelet/x/term"
)

func dispatchVoiceManager(args []string, in io.Reader, out, errout io.Writer) int {
	fs := flag.NewFlagSet("voice "+args[0], flag.ContinueOnError)
	fs.SetOutput(errout)
	cc, err := clientConfig()
	if err != nil {
		_, _ = fmt.Fprintln(errout, err)
		return 1
	}
	var manager, workspace, model, id, deviceListen, publication, from string
	var unattended bool
	switch args[0] {
	case "setup":
		fs.StringVar(&deviceListen, "device-listen", "", "opt-in literal loopback host:port for enrolled devices (port 0 selects an available port)")
		fs.StringVar(&manager, "manager", "", "native manager (codex or claude)")
		fs.StringVar(&workspace, "dir", ".", "manager workspace")
		fs.StringVar(&model, "model", "", "native manager model")
	case "switch":
		fs.StringVar(&from, "from", "", "exact currently configured manager conversation ID")
		fs.StringVar(&manager, "manager", "", "next native manager (codex or claude)")
		fs.StringVar(&workspace, "dir", "", "manager workspace (defaults to the configured workspace)")
	case "serve":
		fs.BoolVar(&unattended, "unattended", false, "spawn the supervisor only from its saved environment")
		fs.StringVar(&cc.StateDir, "state-dir", cc.StateDir, "Swarm state directory")
	case "text", "ptt", "receipt", "publications", "read":
		if args[0] == "read" {
			fs.StringVar(&publication, "publication", "", "exact publication ID from voice publications")
		}
		fs.StringVar(&id, "id", "", "stable utterance ID")
	case "status":
	default:
		return misuseExit
	}
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return misuseExit
	}
	if args[0] == "switch" && (from == "" || (manager != "codex" && manager != "claude")) {
		_, _ = fmt.Fprintln(errout, "voice: switch requires --from <conversation-id> and --manager codex or claude")
		return misuseExit
	}
	if fs.NArg() != 0 || (args[0] == "read" && (id == "" || publication == "")) || ((args[0] == "receipt" || args[0] == "publications") && id == "") {
		_, _ = fmt.Fprintln(errout, "voice: expected command flags; receipt and publications require --id")
		return misuseExit
	}
	if !filepath.IsAbs(cc.StateDir) {
		_, _ = fmt.Fprintln(errout, "voice: state directory must be absolute")
		return misuseExit
	}
	dir := filepath.Join(cc.StateDir, "voice")
	var result any
	switch args[0] {
	case "setup":
		if manager != "claude" && manager != "codex" {
			_, _ = fmt.Fprintln(errout, "voice: choose --manager claude or codex")
			return misuseExit
		}
		workspace, err = resolveVoiceWorkspace(workspace)
		if err == nil {
			var addr *string
			fs.Visit(func(f *flag.Flag) {
				if f.Name == "device-listen" {
					addr = &deviceListen
				}
			})
			result, err = voice.ConfigureManager(dir, skeleton.EndpointIDForStateDir(cc.StateDir), manager, workspace, cc.SocketPath, model, addr)
		}
	case "switch":
		if workspace != "" {
			workspace, err = resolveVoiceWorkspace(workspace)
		}
		if err == nil {
			err = requireVoiceUnitStopped(filepath.Clean(cc.StateDir))
		}
		if err == nil {
			result, err = voice.SwitchManager(dir, from, manager, workspace)
		}
	case "read":
		if f, ok := out.(*os.File); ok && term.IsTerminal(f.Fd()) {
			_, _ = fmt.Fprintln(errout, "voice: pipe the returned 24 kHz PCM16LE to an audio player or file")
			return misuseExit
		}
		client := voice.NewManagerClient(dir)
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		var pcm []byte
		pcm, err = client.PreviewPublication(ctx, id, publication)
		stop()
		client.Close()
		if err == nil {
			var n int
			n, err = out.Write(pcm)
			if err == nil && n != len(pcm) {
				err = io.ErrShortWrite
			}
		}
	case "serve":
		var cfg voice.Config
		cfg, err = voice.LoadConfig(dir)
		if err == nil {
			err = ensureVoiceDaemon(cc, cfg, unattended, daemon.Dial, daemon.EnsureDaemon)
		}
		if err == nil {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			err = voice.ServeManager(ctx, dir, skeleton.EndpointIDForStateDir(cc.StateDir))
		}
	default:
		result, err = localManagerCall(args[0], id, dir, in, errout)
	}
	if err == nil && result != nil {
		err = json.NewEncoder(out).Encode(result)
	}
	if err != nil {
		_, _ = fmt.Fprintln(errout, err)
		return 1
	}
	return 0
}

func resolveVoiceWorkspace(workspace string) (string, error) {
	path, err := filepath.Abs(workspace)
	if err == nil {
		path, err = filepath.EvalSymlinks(path)
	}
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("voice: manager workspace must be a directory")
	}
	return path, nil
}

func localManagerCall(command, id, dir string, in io.Reader, notices io.Writer) (any, error) {
	client := voice.NewManagerClient(dir)
	defer client.Close()
	var body []byte
	if command == "text" || command == "ptt" {
		if id == "" {
			id = rand.Text()
		}
		if _, err := fmt.Fprintf(notices, "Utterance ID %q\n", id); err != nil {
			return nil, err
		}
		limit := voice.MaxUtteranceBytes
		if command == "ptt" {
			if f, ok := in.(*os.File); ok && term.IsTerminal(f.Fd()) {
				return nil, errors.New("voice: pipe mono 24 kHz PCM16LE to the push-to-talk command")
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			_, err := client.BeginPTT(ctx, id)
			stop()
			if err != nil {
				return nil, err
			}
			limit = voice.MaxPTTBytes
		}
		var err error
		body, err = io.ReadAll(io.LimitReader(in, int64(limit)+1))
		if err != nil {
			return nil, err
		}
		if len(body) > limit {
			return nil, voice.ErrCapacity
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch command {
	case "status":
		return client.Status(ctx)
	case "receipt":
		return client.Receipt(ctx, id)
	case "publications":
		return client.Publications(ctx, id)
	case "ptt":
		return client.FinalizePTT(ctx, id, body)
	default:
		return client.SubmitText(ctx, id, string(body))
	}
}
