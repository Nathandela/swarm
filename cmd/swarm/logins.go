package main

import (
	"context"
	"os"

	"github.com/Nathandela/swarm/internal/attach"
	"github.com/Nathandela/swarm/internal/loginworker"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/tui"
)

func runLoginWorker(args []string) int {
	if len(args) != 2 {
		return 2
	}
	var err error
	switch args[0] {
	case "login-enroll":
		err = loginworker.RunConfig(context.Background(), args[1])
	case "login-refresh":
		err = loginworker.RunCodexRefreshConfig(context.Background(), args[1])
	default:
		return 2
	}
	if err != nil {
		return 1
	}
	return 0
}

func loginRunner(hand tui.TerminalHandoff) tui.LoginRunner {
	return func(job protocol.LoginJobView) error {
		session, err := loginworker.AttachSocket(context.Background(), job.LoginSocket)
		if err != nil {
			return err
		}
		defer func() { _ = session.Detach() }()
		if hand.Release != nil {
			if err := hand.Release(); err != nil {
				return err
			}
		}
		if hand.Restore != nil {
			defer func() { _ = hand.Restore() }()
		}
		term, err := attach.NewTermControl(os.Stdin, os.Stdout)
		if err != nil {
			return err
		}
		_, err = attach.Run(attach.Config{Term: term, Session: session, Chrome: true, Name: "Account sign-in"})
		return err
	}
}
