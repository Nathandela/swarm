package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nathandela/swarm/internal/agyacp"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/hookclient"
)

func runScheduleAgyWorker(args []string, out, errOut io.Writer) int {
	attempt, dir := os.Getenv(daemon.ScheduleEnvAttempt), os.Getenv(agyacp.StateEnv)
	bridge := os.Getenv(daemon.ScheduleEnvBinary)
	if len(args) != 1 || attempt == "" || !filepath.IsAbs(dir) || !filepath.IsAbs(bridge) || !filepath.IsAbs(args[0]) {
		_, _ = fmt.Fprintln(errOut, "Scheduled Antigravity worker has no executor binding.")
		return 1
	}
	cwd, err := os.Getwd()
	if err != nil {
		_, _ = fmt.Fprintln(errOut, "Scheduled Antigravity worker cannot open its workspace.")
		return 1
	}
	server := agyacp.Server{Name: "swarm_schedule", Command: bridge, Args: []string{"schedule-receipt-bridge"}}
	for _, name := range []string{daemon.ScheduleEnvRun, daemon.ScheduleEnvAttempt, daemon.ScheduleEnvToken, daemon.ScheduleEnvSocket, hookclient.EnvSessionID} {
		value := os.Getenv(name)
		if value == "" {
			_, _ = fmt.Fprintln(errOut, "Scheduled Antigravity worker has an incomplete receipt binding.")
			return 1
		}
		server.Env = append(server.Env, agyacp.Env{Name: name, Value: value})
	}
	env := []string{}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(name, "SWARM_") {
			env = append(env, entry)
		}
	}
	if err := agyacp.Run(context.Background(), args[0], cwd, dir, attempt, env, server, os.Stdin, out); err != nil {
		_, _ = fmt.Fprintln(errOut, "Scheduled Antigravity worker:", err)
		return 1
	}
	return 0
}
