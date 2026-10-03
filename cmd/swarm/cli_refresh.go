package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/Nathandela/swarm/internal/skeleton"
)

// This command is deliberately local: inspecting or changing refresh policy must
// not start a daemon with the environment of a cron job or an unrelated shell.
func dispatchCLIRefresh(args []string, stdout, stderr io.Writer) int {
	cc, err := clientConfig()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "refresh: %v\n", err)
		return 1
	}
	return runCLIRefresh(args, cc.StateDir, stdout, stderr)
}

func runCLIRefresh(args []string, stateDir string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("refresh", flag.ContinueOnError)
	fs.SetOutput(stderr)
	auto := fs.String("auto", "", "on|off: enable or disable new automatic CLI refreshes")
	asJSON := fs.Bool("json", false, "print refresh configuration and progress as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "refresh: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if *auto != "" && *auto != "on" && *auto != "off" {
		_, _ = fmt.Fprintln(stderr, "refresh: --auto takes on or off")
		return 2
	}
	if *auto != "" {
		if err := skeleton.SetCLIRefreshDisabled(stateDir, *auto == "off"); err != nil {
			_, _ = fmt.Fprintf(stderr, "refresh: %v\n", err)
			return 1
		}
	}
	report, err := skeleton.CLIRefreshStatus(stateDir)
	exitCode := 0
	if err != nil {
		exitCode = 1
		_, _ = fmt.Fprintf(stderr, "refresh: %v\n", err)
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			_, _ = fmt.Fprintf(stderr, "refresh: %v\n", err)
			return 1
		}
		return exitCode
	}
	mode := "on"
	if report.Disabled {
		mode = "off"
	}
	_, _ = fmt.Fprintf(stdout, "Automatic CLI refresh: %s\n", mode)
	if report.Frozen {
		_, _ = fmt.Fprintln(stdout, "Refresh state needs attention; automatic actions are held.")
	}
	if len(report.Records) == 0 {
		_, _ = fmt.Fprintln(stdout, "No recorded refresh requests. Legacy sessions without launch evidence remain untouched.")
		return exitCode
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "SOURCE\tAGENT\tSTATE\tTARGET\tREPLACEMENT\tDETAIL")
	for _, r := range report.Records {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.SourceID, r.AgentType, r.State, r.TargetVersion, r.ReplacementID, r.LastError)
	}
	_ = tw.Flush()
	return exitCode
}
