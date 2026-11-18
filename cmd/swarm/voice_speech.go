package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"unicode"

	"github.com/Nathandela/swarm/internal/voice"
	"github.com/Nathandela/swarm/internal/voice/openailive"
	"github.com/charmbracelet/x/term"
)

const voiceUsage = `usage: swarm voice setup | switch | start | stop | serve | status | text | ptt | receipt | publications | live | transcribe
  --host ALIAS   open an enrolled remote manager through OpenSSH (--enroll for first use)
  panel       open the local browser panel (default); --print-url for manual opening
  setup       use the global OpenAI speech endpoint; enter the key privately or pipe it to stdin
              existing Gradium settings switch to the marin voice
  setup --manager codex|claude --dir PATH   configure the native manager workspace
  start / stop   install or stop the per-user voice service
  serve       run the foreground manager service
  text        submit text from stdin (use --id to retry the same request)
  ptt         submit one finalized PCM utterance to the native manager (accepts --id)
  read        read aloud one selected publication (--id and --publication), PCM to stdout
  status      show the manager binding and attention state
  receipt     read an admission by --id
  publications  read the manager's canonical reply phrases by --id
  enroll / devices / revoke / device-endpoint   manage scoped device grants over SSH
  interrupt-manager / interrupt-status   act on one saved active manager status from stdin
  question / answer   inspect or answer an exact manager clarification
  resume / ack        reconcile manual input or an uncertain admission
  replace / replacement   replace a quarantined manager or inspect its receipt
  live        stream mono 24 kHz PCM16LE from stdin and write reply audio to stdout
  transcribe  read one utterance of the same format from stdin and print its final text
`

func dispatchSpeech(args []string, stdin *os.File, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, _ = io.WriteString(stdout, voiceUsage)
		return 0
	}
	if len(args) != 1 || (args[0] != "setup" && args[0] != "live" && args[0] != "transcribe") {
		_, _ = io.WriteString(stderr, voiceUsage)
		return misuseExit
	}
	root := resolveStateDir()
	if root == "" {
		_, _ = fmt.Fprintln(stderr, "voice: cannot locate the Swarm state directory")
		return 1
	}
	if err := runSpeech(args[0], filepath.Join(root, "voice"), stdin, stdout, stderr); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func runSpeech(command, dir string, in *os.File, out, errout io.Writer) error {
	if command == "setup" {
		var key io.Reader = in
		if term.IsTerminal(in.Fd()) {
			_, _ = fmt.Fprint(errout, "OpenAI API key (hidden): ")
			secret, err := term.ReadPassword(in.Fd())
			_, _ = fmt.Fprintln(errout)
			if err != nil {
				return errors.New("voice: could not read key")
			}
			defer clear(secret)
			key = bytes.NewReader(secret)
		}
		if err := voice.SetupOpenAISpeech(dir, key); err != nil {
			return err
		}
		_, err := fmt.Fprintln(out, "OpenAI speech credential saved for api.openai.com.")
		return err
	}
	if term.IsTerminal(in.Fd()) {
		return errors.New("voice: pipe mono 24 kHz PCM16LE audio to stdin")
	}
	if stdoutFile, ok := out.(*os.File); ok && command == "live" && term.IsTerminal(stdoutFile.Fd()) {
		return errors.New("voice: pipe stdout to a PCM audio player or a file")
	}
	config, err := voice.LoadConfig(dir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	key, err := voice.LoadOpenAIKey(dir)
	if os.IsNotExist(err) {
		return errors.New("voice: run swarm voice setup to save your OpenAI key")
	}
	if err != nil {
		return err
	}
	if command == "transcribe" {
		pcm, err := io.ReadAll(io.LimitReader(in, openailive.MaxUtteranceBytes+1))
		if err != nil {
			return err
		}
		if len(pcm) > openailive.MaxUtteranceBytes {
			return errors.New("voice: utterance exceeds two minutes")
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		text, err := openailive.Transcribe(ctx, key, pcm, config.Language)
		stop()
		if err != nil {
			return err
		}
		if f, ok := out.(*os.File); ok && term.IsTerminal(f.Fd()) {
			text = terminalTranscript(text)
		}
		_, err = fmt.Fprintln(out, text)
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return streamLiveVoice(ctx, key, config.VoiceID, in, out)
}

func terminalTranscript(text string) string {
	return strings.Map(func(r rune) rune {
		if r != '\n' && r != '\t' && unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
}
