package main

import (
	"context"
	"os"

	"github.com/Nathandela/swarm/internal/accountcheck"
	"github.com/Nathandela/swarm/internal/attach"
	"github.com/Nathandela/swarm/internal/enrollment"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/tui"
)

func runAccountEnrollment(args []string) int {
	if len(args) == 2 && args[0] == "account-check" {
		if accountcheck.RunWorker(context.Background(), args[1], os.Stdin, os.Stdout) != nil {
			return 1
		}
		return 0
	}
	if len(args) != 2 || args[0] != "account-enroll" {
		return 2
	}
	if err := enrollment.RunConfig(context.Background(), args[1]); err != nil {
		return 1
	}
	return 0
}

func accountLoginRunner(hand tui.TerminalHandoff) tui.AccountLoginRunner {
	return func(job protocol.AccountEnrollmentView) error {
		session, err := enrollment.AttachSocket(context.Background(), job.LoginSocket)
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
