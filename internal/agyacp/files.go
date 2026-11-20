package agyacp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

const StateEnv = "SWARM_SCHEDULE_AGY_STATE"

var versionPattern = regexp.MustCompile(`(?m)^\s*(\d+)\.(\d+)\.(\d+)\s*$`)

func Qualify(path string, env []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = env
	output, err := cmd.Output()
	if err != nil {
		return errors.New("cannot read the agy version on the executor")
	}
	parts := versionPattern.FindSubmatch(output)
	if len(parts) != 4 {
		return errors.New("cannot identify the agy version on the executor")
	}
	major, _ := strconv.Atoi(string(parts[1]))
	minor, _ := strconv.Atoi(string(parts[2]))
	patch, _ := strconv.Atoi(string(parts[3]))
	if major == 0 || (major == 1 && (minor < 5 || (minor == 5 && patch < 12))) {
		return errors.New("scheduled agy runs require Antigravity CLI 1.5.12 or newer on the executor")
	}
	return nil
}

type Evidence struct {
	StartupRejected bool
	Attempt         string
	Conversation    string
	NeedsInput      bool
	EndTurn         bool
	Text            string
}

type request struct {
	Attempt string
	Text    string
}

func ReadEvidence(dir, attempt string) (Evidence, bool) {
	var result Evidence
	err := read(dir, "schedule-agy-evidence.json", &result)
	return result, err == nil && attempt != "" && result.Attempt == attempt && (result.Conversation != "" || result.StartupRejected)
}

func QueuePrompt(dir, attempt, prompt string) error {
	return write(dir, "schedule-agy-prompt.json", request{attempt, prompt}, true)
}

func awaitPrompt(ctx context.Context, dir, attempt string) (string, error) {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		var req request
		err := read(dir, "schedule-agy-prompt.json", &req)
		switch {
		case err == nil:
			if req.Attempt != attempt {
				return "", errors.New("scheduled agy prompt has a different attempt")
			}
			if err := os.Remove(filepath.Join(dir, "schedule-agy-prompt.json")); err != nil {
				return "", err
			}
			return req.Text, nil
		case !errors.Is(err, os.ErrNotExist):
			return "", err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-tick.C:
		}
	}
}

func read(dir, name string, dest any) error {
	file, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	return json.NewDecoder(file).Decode(dest)
}

func write(dir, name string, data any, once bool) error {
	file, err := os.CreateTemp(dir, ".schedule-agy-")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	err = json.NewEncoder(file).Encode(data)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	target := filepath.Join(dir, name)
	if once {
		err = os.Link(file.Name(), target)
	} else {
		err = os.Rename(file.Name(), target)
	}
	if err != nil {
		return err
	}
	parent, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	return parent.Sync()
}
