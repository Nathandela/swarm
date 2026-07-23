package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/remote/machineid"
)

const remoteUsage = `usage: swarm remote <command>

  swarm remote init      provision this machine's pairing identity
  swarm remote devices   list paired devices
  swarm remote revoke    revoke a paired device
  swarm remote pair      pair a new device
  swarm remote off       disable remote control
  swarm remote on        enable remote control
  swarm remote status    show remote control status
`

// runRemote is the `swarm remote` role: it dispatches to a remote-control verb.
// With no verb it prints usage (nonzero exit); an unrecognized verb is an error
// (nonzero exit). Only `init` is wired in this slice — the rest are later work.
func runRemote(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, remoteUsage)
		return 2
	}
	switch args[0] {
	case "init":
		return runRemoteInit(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "remote: unknown remote command %q\n", args[0])
		return 2
	}
}

// remoteIdentityFile is the machine identity `runRemoteInit` persists, at
// <stateDir>/remote/machine.key. internal/skeleton.loadPairingConfig reads it
// back from the same path (see internal/skeleton/pairing_config.go) — the CLI
// and the daemon assembly must agree on it.
const remoteIdentityFile = "machine.key"

// runRemoteInit is the `swarm remote init` verb (machine key custody, A4-1b). It
// resolves the state dir the same way dialClient does (SWARM_DAEMON_STATE env,
// falling back to persist.DefaultDir), then either loads the existing machine
// identity at <stateDir>/remote/machine.key (IDEMPOTENT: a second run never
// rotates keys) or generates and saves a fresh one at 0600. It prints only the
// identity's redacted, public fingerprint (identity.String()) to stdout — never
// any private material.
func runRemoteInit(_ []string, stdout, stderr io.Writer) int {
	stateDir := os.Getenv(daemon.EnvStateDir)
	if stateDir == "" {
		var err error
		if stateDir, err = persist.DefaultDir(); err != nil {
			fmt.Fprintf(stderr, "remote init: %v\n", err)
			return 1
		}
	}

	remoteDir := filepath.Join(stateDir, "remote")
	if err := os.MkdirAll(remoteDir, 0o700); err != nil {
		fmt.Fprintf(stderr, "remote init: %v\n", err)
		return 1
	}
	path := filepath.Join(remoteDir, remoteIdentityFile)

	var id *machineid.Identity
	if _, err := os.Stat(path); err == nil {
		// Identity already provisioned: load it rather than rotating (idempotent).
		id, err = machineid.Load(path)
		if err != nil {
			fmt.Fprintf(stderr, "remote init: %v\n", err)
			return 1
		}
	} else if os.IsNotExist(err) {
		hostname, hErr := os.Hostname()
		if hErr != nil {
			hostname = "unknown"
		}
		id, err = machineid.Generate(hostname)
		if err != nil {
			fmt.Fprintf(stderr, "remote init: %v\n", err)
			return 1
		}
		if err := id.Save(path); err != nil {
			fmt.Fprintf(stderr, "remote init: %v\n", err)
			return 1
		}
	} else {
		fmt.Fprintf(stderr, "remote init: %v\n", err)
		return 1
	}

	fmt.Fprintln(stdout, id.String())
	return 0
}
