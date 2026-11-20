package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Nathandela/swarm/internal/schedule"
	"github.com/Nathandela/swarm/internal/scheduletimer"
)

const scheduleUsage = `usage: swarm schedule <command>
  create --name NAME --dir PATH --agents ORDERED_LIST --authority MODE
         --workspace worktree|directory --prompt-file FILE [--timezone ZONE]
         (--once YYYY-MM-DD | --daily | --weekdays | --weekly Monday | --cron EXPR)
         [--time HH:MM] [--catch-up 1h] [--allow-overlap=false]
  Supported agents: codex, claude, agy (in fallback order).
  create --spec-file FILE         create from a JSON job specification
  edit JOB --spec-file FILE [--revision ID]    replace a job specification
  list [--json] | show JOB | history JOB
  pause JOB | resume JOB | archive JOB | run JOB [--operation-id ULID]
  templates list | show ID | create NAME FILE | edit ID NAME FILE | delete ID
  timer install | timer status | timer uninstall
  tick | complete

Authority: observe, workspace-write, agent-default. Agents are tried in order.
Use '-' for a prompt file on stdin. Run now leaves the calendar unchanged.
If a run reply is lost, retry the same job with the printed operation ID.
`

func dispatchSchedule(args []string, out, errOut io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		if _, err := fmt.Fprint(out, scheduleUsage); err != nil {
			return 1
		}
		return 0
	}
	switch args[0] {
	case "complete", "tick", "run":
		return dispatchScheduleWork(args[0], args[1:], out, errOut)
	}
	if args[0] == "status" && len(args) == 1 {
		return scheduleTimerCLI([]string{"status"}, out, errOut)
	}
	if args[0] == "timer" {
		return scheduleTimerCLI(args[1:], out, errOut)
	}
	cfg, err := clientConfig()
	if err != nil {
		_, _ = fmt.Fprintln(errOut, err)
		return 1
	}
	store, err := schedule.Open(cfg.StateDir)
	if err != nil {
		_, _ = fmt.Fprintln(errOut, err)
		return 1
	}
	value, err := scheduleCatalog(store, args, out)
	if err != nil {
		_, _ = fmt.Fprintln(errOut, "schedule:", err)
		return 1
	}
	if value != nil {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(value); err != nil {
			_, _ = fmt.Fprintln(errOut, err)
			return 1
		}
	}
	return 0
}

func scheduleTimerCLI(args []string, out, errOut io.Writer) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintln(errOut, "usage: swarm schedule timer install|status|uninstall")
		return 2
	}
	var err error
	switch args[0] {
	case "install":
		err = installScheduleTimer()
	case "uninstall":
		err = scheduletimer.Uninstall()
	case "status":
		var line string
		line, err = scheduletimer.Status()
		if err == nil {
			if _, err := fmt.Fprintln(out, line); err != nil {
				return 1
			}
		}
	default:
		_, _ = fmt.Fprintln(errOut, "unknown timer command:", args[0])
		return 2
	}
	if err != nil {
		_, _ = fmt.Fprintln(errOut, err)
		return 1
	}
	return 0
}

func scheduleCatalog(store *schedule.Store, args []string, out io.Writer) (any, error) {
	now := time.Now()
	switch args[0] {
	case "list":
		jobs, err := store.Jobs()
		if err != nil {
			return nil, err
		}
		if len(args) == 2 && args[1] == "--json" {
			return jobs, nil
		}
		if len(args) != 1 {
			return nil, errors.New("usage: swarm schedule list [--json]")
		}
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		if _, err := fmt.Fprintln(w, "ID\tSTATE\tNAME\tNEXT LOCAL TIME\tAGENTS"); err != nil {
			return nil, err
		}
		for _, job := range jobs {
			next := "—"
			if job.State == schedule.JobActive {
				due, err := job.Spec.Calendar.Next(now)
				if err != nil {
					return nil, err
				}
				if !due.IsZero() {
					zone, err := job.Spec.Calendar.Location()
					if err != nil {
						return nil, err
					}
					next = due.In(zone).Format("2006-01-02 15:04 MST")
				}
			}
			if _, err := fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", job.ID, job.State, job.Spec.Name, next, strings.Join(job.Spec.Agents, " → ")); err != nil {
				return nil, err
			}
		}
		return nil, w.Flush()
	case "create":
		spec, err := readScheduleSpec(args[1:])
		if err != nil {
			return nil, err
		}
		if err := validateScheduleSpec(spec); err != nil {
			return nil, err
		}
		if err := checkScheduleCalendar(spec.Calendar); err != nil {
			return nil, err
		}
		return store.CreateJob(spec, now)
	case "templates":
		return scheduleTemplates(store, args[1:], now)
	}
	if len(args) < 2 {
		return nil, fmt.Errorf("expected a job ID for %s", args[0])
	}
	id := args[1]
	if args[0] == "edit" {
		if (len(args) != 4 && len(args) != 6) || args[2] != "--spec-file" || (len(args) == 6 && args[4] != "--revision") {
			return nil, errors.New("usage: swarm schedule edit JOB --spec-file FILE")
		}
		spec, err := readScheduleSpec(args[2:4])
		if err != nil {
			return nil, err
		}
		if err := validateScheduleSpec(spec); err != nil {
			return nil, err
		}
		if err := checkScheduleCalendar(spec.Calendar); err != nil {
			return nil, err
		}
		if len(args) == 6 {
			return store.EditJobRevision(id, args[5], spec, now)
		}
		return store.EditJob(id, spec, now)
	}
	if len(args) != 2 {
		return nil, errors.New("unexpected arguments")
	}
	switch args[0] {
	case "show":
		return store.Job(id)
	case "history":
		return store.History(id)
	case "pause":
		return store.SetJobState(id, schedule.JobPaused, now)
	case "resume":
		job, err := store.Job(id)
		if err != nil {
			return nil, err
		}
		if err := checkScheduleCalendar(job.Spec.Calendar); err != nil {
			return nil, err
		}
		return store.SetJobState(id, schedule.JobActive, now)
	case "archive":
		return store.SetJobState(id, schedule.JobArchived, now)
	}
	return nil, fmt.Errorf("unknown command %q; see swarm schedule help", args[0])
}

func scheduleTemplates(s *schedule.Store, args []string, now time.Time) (any, error) {
	if len(args) == 1 && args[0] == "list" {
		return s.Templates()
	}
	if len(args) == 2 {
		switch args[0] {
		case "show":
			return s.Template(args[1])
		case "delete":
			return nil, s.DeleteTemplate(args[1])
		}
	}
	if len(args) == 3 && args[0] == "create" || len(args) == 4 && args[0] == "edit" {
		body, err := scheduleFile(args[len(args)-1])
		if err != nil {
			return nil, err
		}
		if args[0] == "create" {
			return s.CreateTemplate(args[1], string(body), now)
		}
		return s.EditTemplate(args[1], args[2], string(body), now)
	}
	return nil, errors.New("usage: swarm schedule templates list|show ID|create NAME FILE|edit ID NAME FILE|delete ID")
}

func scheduleFile(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

func readScheduleSpec(args []string) (schedule.JobSpec, error) {
	var spec schedule.JobSpec
	if len(args) == 2 && args[0] == "--spec-file" {
		body, err := scheduleFile(args[1])
		if err != nil {
			return spec, err
		}
		dec := json.NewDecoder(strings.NewReader(string(body)))
		dec.DisallowUnknownFields()
		if err = dec.Decode(&spec); err != nil {
			return spec, err
		}
		if err = dec.Decode(new(any)); err != io.EOF {
			return spec, errors.New("expected one JSON specification")
		}
		return spec, nil
	}
	f := flag.NewFlagSet("schedule create", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&spec.Name, "name", "", "job name")
	f.StringVar(&spec.Directory, "dir", "", "working directory")
	f.StringVar(&spec.Workspace, "workspace", "", "workspace policy")
	f.StringVar(&spec.Authority, "authority", "", "permission policy")
	agents := f.String("agents", "", "ordered agent IDs")
	prompt := f.String("prompt-file", "", "prompt file")
	f.BoolVar(&spec.AllowOverlap, "allow-overlap", true, "allow concurrent runs in separate worktrees")
	f.DurationVar(&spec.Grace, "catch-up", 0, "catch-up grace period")
	c := &spec.Calendar
	f.StringVar(&c.Date, "once", "", "one-time local date")
	daily := f.Bool("daily", false, "daily")
	weekdays := f.Bool("weekdays", false, "weekdays")
	f.StringVar(&c.Weekday, "weekly", "", "weekday")
	f.StringVar(&c.Cron, "cron", "", "five-field cron expression")
	f.StringVar(&c.Time, "time", "", "local time")
	f.StringVar(&c.TimeZone, "timezone", schedule.HostTimeZone, "follow host time or specify an IANA time zone")
	if err := f.Parse(args); err != nil {
		return spec, err
	}
	if f.NArg() != 0 {
		return spec, errors.New("unexpected positional argument")
	}
	kinds := []string{}
	if c.Date != "" {
		kinds = append(kinds, "once")
	}
	if *daily {
		kinds = append(kinds, "daily")
	}
	if *weekdays {
		kinds = append(kinds, "weekdays")
	}
	if c.Weekday != "" {
		kinds = append(kinds, "weekly")
	}
	if c.Cron != "" {
		kinds = append(kinds, "cron")
	}
	if len(kinds) != 1 {
		return spec, errors.New("choose one of --once, --daily, --weekdays, --weekly, --cron")
	}
	c.Kind = kinds[0]
	for _, agent := range strings.Split(*agents, ",") {
		spec.Agents = append(spec.Agents, strings.TrimSpace(agent))
	}
	if *prompt == "" {
		return spec, errors.New("--prompt-file is required")
	}
	body, err := scheduleFile(*prompt)
	if err != nil {
		return spec, err
	}
	spec.Prompt = string(body)
	return spec, nil
}
