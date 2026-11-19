package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/schedule"
)

func dispatchScheduleWork(verb string, args []string, out, errOut io.Writer) int {
	if verb == "complete" {
		if err := completeSchedule(args, errOut); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			_, _ = fmt.Fprintln(errOut, err)
			return 1
		}
		if _, err := fmt.Fprintln(out, "Completion recorded."); err != nil {
			return 1
		}
		return 0
	}
	if verb == "tick" && len(args) != 0 {
		_, _ = fmt.Fprintln(errOut, "usage: swarm schedule tick")
		return 2
	}
	var jobID, operationID string
	if verb == "run" {
		var err error
		jobID, operationID, err = parseScheduleRun(args, errOut)
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		if err != nil {
			return 2
		}
	}

	client, err := dialScheduleClient(verb == "tick")
	if err != nil {
		_, _ = fmt.Fprintln(errOut, err)
		return 1
	}
	defer func() { _ = client.Close() }()
	switch verb {
	case "tick":
		err = client.ScheduleTick()
	case "run":
		// Publish the key before submission so it survives an interrupted CLI.
		_, _ = fmt.Fprintf(errOut, "Operation ID: %q\n", operationID)
		var id string
		id, err = client.ScheduleRun(jobID, operationID)
		if err == nil {
			_, err = fmt.Fprintln(out, id)
		}
	default:
		err = fmt.Errorf("unknown schedule execution command %q", verb)
	}
	if err != nil {
		_, _ = fmt.Fprintln(errOut, err)
		if verb == "run" {
			_, _ = fmt.Fprintf(errOut, "To retry the same run, repeat the command for the same job with --operation-id %q.\n", operationID)
		}
		return 1
	}
	return 0
}

func parseScheduleRun(args []string, errOut io.Writer) (jobID, operationID string, err error) {
	flags := flag.NewFlagSet("swarm schedule run", flag.ContinueOnError)
	flags.SetOutput(errOut)
	flags.Func("operation-id", "reuse this operation ULID to retry the same manual run", func(id string) error {
		if id == "" {
			return errors.New("expected a non-empty operation ID")
		}
		operationID = id
		return nil
	})
	flags.Usage = func() {
		_, _ = fmt.Fprintln(errOut, "usage: swarm schedule run JOB [--operation-id ULID]")
		flags.PrintDefaults()
	}
	// Accept the documented job-first syntax as well as flags before the job.
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		jobID, args = args[0], args[1:]
	}
	if err := flags.Parse(args); err != nil {
		return "", "", err
	}
	if jobID == "" && flags.NArg() == 1 {
		jobID = flags.Arg(0)
	} else if flags.NArg() != 0 {
		jobID = ""
	}
	if jobID == "" {
		flags.Usage()
		return "", "", errors.New("expected one job ID")
	}
	if operationID == "" {
		operationID = schedule.NewID(time.Now())
	}
	return jobID, operationID, nil
}

func completeSchedule(args []string, errOut io.Writer) error {
	flags := flag.NewFlagSet("swarm schedule complete", flag.ContinueOnError)
	flags.SetOutput(errOut)
	summary := flags.String("summary", "", "brief summary already posted in the worker chat")
	var links []string
	flags.Func("link", "artifact link (repeatable)", func(link string) error {
		links = append(links, link)
		return nil
	})
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || strings.TrimSpace(*summary) == "" {
		return errors.New("usage: swarm schedule complete --summary TEXT [--link LINK]")
	}

	return submitScheduleCompletion(*summary, links)
}

func submitScheduleCompletion(summary string, links []string) error {
	runID := os.Getenv(daemon.ScheduleEnvRun)
	attempt := os.Getenv(daemon.ScheduleEnvAttempt)
	token := os.Getenv(daemon.ScheduleEnvToken)
	socket := os.Getenv(daemon.ScheduleEnvSocket)
	session := os.Getenv(hookclient.EnvSessionID)
	if runID == "" || attempt == "" || token == "" || socket == "" || session == "" {
		return errors.New("complete must run inside the active scheduled worker session")
	}
	// Reuse the already-running owner socket, without spawning a daemon or
	// reading/writing the private schedule catalog in the worker sandbox.
	client, err := protocol.Dial(socket, []string{protocol.CapSchedule})
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	return client.ScheduleComplete(session, schema.ScheduleCompletion{
		RunID: runID, AttemptID: attempt, Token: token, Summary: summary, Links: links,
	})
}
