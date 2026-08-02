// Command e2e2session starts ONE session on a running daemon and exits, so that
// scripts/pbe2e2-emulator-smoke.sh has something for the phone to observe, take control of
// and type into.
//
// IT IS NOT PART OF THE PRODUCT AND MUST NEVER BECOME PART OF IT.
//
// It lives under scripts/ rather than cmd/ deliberately. .goreleaser.yaml builds swarm,
// swarm-remote and swarm-relay from ./cmd; a fourth directory there would read as a fourth
// binary to a reader scanning the tree, and this one is a test fixture. `go build ./...`
// still compiles it, which is the point of keeping it in the module: a helper that stops
// compiling between demonstrations is a demonstration that cannot be repeated.
//
// WHY IT EXISTS AT ALL, and why it is not a new verb. PB-E2E-2's five in-app actions all
// need a session to already exist on the machine, and this repository has no non-interactive
// way to create one: `swarm` is daemon|shim|hook|remote|version plus a TUI that refuses a
// non-terminal ("the TUI needs an interactive terminal"), and `swarm remote` has no launch.
// Adding one was ruled out at closure and the reasoning stands -- product surface added so a
// demonstration can be automated is how a demonstration stops being about the product.
//
// So this speaks the daemon's EXISTING owner protocol, over the same UDS the TUI uses, and
// calls the same protocol.Client.Launch the TUI calls. The exit demonstration
// (internal/skeleton, TestPBE2E1) already drives that API from Go for the same reason. A
// session created here is indistinguishable to the daemon from one the TUI created: same
// LaunchSpec, same shim, same PTY, same registry row. Nothing about the phone's half is
// simulated, substituted or bypassed -- the smoke still drives the installed APK through its
// own controls, which is what PB-E2E-2 is about.
//
// The smoke's own contract already expected this: SWARM_E2E2_SESSION_CMD is documented there
// as "the operator's" command. This is that command, checked in so the run is reproducible
// rather than depending on what a particular operator happened to type.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/protocol"
)

// defaultScript keeps the session alive and prints one recognisable line, so the roster the
// phone observes has a row whose rendered output a human can identify in a screenshot. The
// idle is long enough to outlast the whole smoke, because a session that exits mid-run turns
// "observes" into a race.
const defaultScript = "print PBE2E2_SESSION_READY\nidle 900s\n"

func main() {
	fs := flag.NewFlagSet("e2e2session", flag.ContinueOnError)
	socket := fs.String("socket", "", "daemon UDS (default: $SWARM_DAEMON_SOCK, else <state>/daemon.sock)")
	cwd := fs.String("cwd", "", "working directory for the session (required: R-POL.7 confines remote launches to configured roots)")
	agent := fs.String("agent", "fake", "agent to launch; `fake` is the reserved dev/test agent and needs no API key")
	script := fs.String("script", "", "path to a fake-agent script (default: a built-in keep-alive script written beside --cwd)")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	if err := run(*socket, *cwd, *agent, *script); err != nil {
		fmt.Fprintf(os.Stderr, "e2e2session: %v\n", err)
		os.Exit(1)
	}
}

func run(socket, cwd, agent, script string) error {
	if cwd == "" {
		return fmt.Errorf("--cwd is required; remote launches are confined to the roots in " +
			"remote-policy.json and fail closed with none (R-POL.7)")
	}
	if socket == "" {
		socket = os.Getenv(daemon.EnvSocket)
	}
	if socket == "" {
		stateDir := os.Getenv(daemon.EnvStateDir)
		if stateDir == "" {
			return fmt.Errorf("no daemon socket: pass --socket, or set %s / %s",
				daemon.EnvSocket, daemon.EnvStateDir)
		}
		socket = filepath.Join(stateDir, "daemon.sock")
	}

	if script == "" {
		script = filepath.Join(cwd, "e2e2-session-script.txt")
		if err := os.WriteFile(script, []byte(defaultScript), 0o600); err != nil {
			return fmt.Errorf("write the fake-agent script: %w", err)
		}
	}

	// The OWNER tier, which is what the TUI dials. The phone reaches the daemon over a
	// different socket entirely (ADR-007 D4), so nothing here stands in for the phone's half.
	client, err := protocol.Dial(socket, nil)
	if err != nil {
		return fmt.Errorf("dial the daemon at %s: %w", socket, err)
	}
	defer func() { _ = client.Close() }()

	id, _, err := client.Launch(protocol.LaunchReq{
		Agent:   agent,
		Cwd:     cwd,
		Options: map[string]string{"script": script},
		Cols:    80,
		Rows:    24,
	})
	if err != nil {
		return fmt.Errorf("launch: %w", err)
	}
	fmt.Println(id)
	return nil
}
