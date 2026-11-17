package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Nathandela/swarm/internal/voice/openailive"
)

const liveVoiceIOTimeout = 5 * time.Second

type liveVoiceSession interface {
	SendAudio(context.Context, []byte) error
	Next(context.Context) (openailive.Event, error)
	Finish(context.Context) error
}

func streamLiveVoice(ctx context.Context, key, voice string, in *os.File, out io.Writer) error {
	session, err := openailive.Open(ctx, key, voice)
	if err != nil {
		return err
	}
	defer func() { _ = session.Close() }()
	return pumpLiveVoice(ctx, session, in, out, liveVoiceIOTimeout)
}

func pumpLiveVoice(ctx context.Context, session liveVoiceSession, in *os.File, out io.Writer, timeout time.Duration) error {
	readFrom, pollInput, closeInput, err := pilotWatchOutput(in)
	if err != nil {
		return fmt.Errorf("prepare voice input: %w", err)
	}
	writeTo, pollOutput, closeOutput, err := pilotWatchOutput(out)
	if err != nil {
		closeInput()
		return fmt.Errorf("prepare voice output: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	inputDone, outputDone := make(chan error, 1), make(chan error, 1)
	readerDone, writerDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(readerDone)
		inputDone <- sendLiveVoiceAudio(runCtx, session, readFrom.(*os.File))
	}()
	go func() {
		defer close(writerDone)
		if pollOutput {
			outputDone <- receiveLiveVoiceAudio(runCtx, session, writeTo, writeTo.(*os.File), timeout)
		} else {
			outputDone <- receiveLiveVoiceAudio(runCtx, session, writeTo, nil, timeout)
		}
	}()
	defer func() {
		cancel()
		// Close the owned poll descriptors before waiting for their workers.
		// Plain writers must return promptly, as io.Writer cannot be interrupted.
		closeOutput()
		closeInput()
		if pollInput {
			<-readerDone
		}
		if pollOutput {
			<-writerDone
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-outputDone:
		return err
	case err := <-inputDone:
		if err != nil {
			return err
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-outputDone:
		return err
	case <-timer.C:
		return errors.New("OpenAI Live did not confirm session closure")
	}
}

func sendLiveVoiceAudio(ctx context.Context, session liveVoiceSession, in io.Reader) error {
	var buf [4096]byte
	var carry int
	var nextSend time.Time
	for ctx.Err() == nil {
		n, err := in.Read(buf[carry:])
		n += carry
		complete := n &^ 1
		if complete != 0 {
			if err := waitLiveVoicePCM(ctx, nextSend); err != nil {
				return err
			}
			// Anchor each chunk to its actual send time so a stalled source cannot catch up in a burst.
			const pcmBytesPerSecond = 24000 * 2
			nextSend = time.Now().Add(time.Duration(complete) * time.Second / pcmBytesPerSecond)
			if sendErr := session.SendAudio(ctx, buf[:complete]); sendErr != nil {
				return sendErr
			}
		}
		carry = n - complete
		if carry != 0 {
			buf[0] = buf[complete]
		}
		if errors.Is(err, io.EOF) {
			if carry != 0 {
				return errors.New("voice input ends with a partial PCM16 sample")
			}
			if err := waitLiveVoicePCM(ctx, nextSend); err != nil {
				return err
			}
			return session.Finish(ctx)
		}
		if err != nil {
			return fmt.Errorf("read voice input: %w", err)
		}
	}
	return ctx.Err()
}

func waitLiveVoicePCM(ctx context.Context, until time.Time) error {
	wait := time.Until(until)
	if wait <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func receiveLiveVoiceAudio(ctx context.Context, session liveVoiceSession, out io.Writer, pollFile *os.File, timeout time.Duration) error {
	for {
		event, err := session.Next(ctx)
		if err != nil {
			return err
		}
		switch event.Type {
		case "session.closed":
			return nil
		case "session.delegation.created":
			return errors.New("native manager connection required for OpenAI Live delegation")
		}
		if len(event.Audio) == 0 {
			continue
		}
		if pollFile != nil {
			if err := pollFile.SetWriteDeadline(time.Now().Add(timeout)); err != nil && !errors.Is(err, os.ErrNoDeadline) {
				return fmt.Errorf("bound voice output: %w", err)
			}
		}
		n, err := out.Write(event.Audio)
		if err != nil {
			return fmt.Errorf("write voice output: %w", err)
		}
		if n != len(event.Audio) {
			return io.ErrShortWrite
		}
	}
}
