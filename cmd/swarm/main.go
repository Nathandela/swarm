// Command swarm is the single distributed binary for the swarm system.
// Role is selected by the first argument: daemon, shim, hook, or no
// argument at all (opens the TUI).
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/term"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/detect"
	"github.com/Nathandela/swarm/internal/adapter/registry"
	"github.com/Nathandela/swarm/internal/attach"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/skeleton"
	"github.com/Nathandela/swarm/internal/transcript"
	"github.com/Nathandela/swarm/internal/tui"
	"github.com/Nathandela/swarm/internal/version"
	"golang.org/x/sys/unix"
)

// defaultMaxSessions caps concurrent sessions for a production daemon.
const defaultMaxSessions = 128

// Engine tuning for the assembled daemon: a low-frequency fallback poll and the
// staleness window that bounds a stale typed signal / an active-but-silent turn.
const (
	daemonPollInterval       = time.Second
	daemonStalenessThreshold = 30 * time.Second
)

// envFakeAgentBin is the dev/test-only knob naming the swarm-fake-agent binary the
// walking-skeleton assembly execs for the reserved agent "fake". It is unset in a
// real install, so "fake" simply does not resolve there.
const envFakeAgentBin = "SWARM_FAKE_AGENT_BIN"

// shimSessionEnv guards the setsid re-exec against an infinite loop: it is set
// on the re-exec'd child so a shim that still cannot become a session leader
// fails loudly instead of re-exec'ing again.
const shimSessionEnv = "SWARM_SHIM_SESSION"

const usage = `usage: swarm [daemon|shim|hook|handoff|spawn|reattach|ls|watch|kill|send|peek|doctor|relogin|upgrade|version]

  swarm            open the TUI
  swarm daemon     run the session daemon
  swarm shim       run the PTY-owning shim process
  swarm hook       post a hook event to the daemon
  swarm handoff    launch a supervised cross-CLI handoff
                   (--cli agent [--model m] [--name n]
                    [--supervision passive|manual|none] --context-file file)
  swarm spawn      launch a new session with context
                   (--cli agent [--dir d] [--model m] [--worktree] [--name n],
                    one of --prompt text | --handoff file | --delegate file)
  swarm reattach   adopt provider-managed background sessions
                   (--cli claude [--all] [--take-over] [--dry-run])
  swarm ls         list sessions (--json for the full roster)
  swarm watch      wait for a session to reach a status
                   (--until needs_input[,ready_for_review,completed]|change, --timeout d)
  swarm kill       terminate a session
  swarm send       type into a session <session>
                   (--text s [--no-submit] | --key enter|esc|ctrl-c|tab|up|down)
  swarm peek       print a session's current screen <session> [--lines N]
  swarm doctor     diagnose this machine's swarm lifecycle [--json]
                   (never starts a daemon; safe from cron)
  swarm relogin    recycle sessions stranded by a provider re-login
                   ([--dry-run] [--force] [--auto on|off]; the daemon's
                    watcher does this automatically -- this is the manual face)
  swarm upgrade    check, --stage, --activate, --rollback, or --unattended
                   (signature + checksum verified; activation defers around
                    live sessions and hands off to the new binary's converge;
                    never starts a daemon) [--json]
  swarm version    print the build version
`

const shimUsage = `usage: swarm shim --config <path>

  --config <path>   JSON launch config: session_id, argv, cwd, env,
                    socket_path, session_dir, cols, rows, grace_ms
`

func main() {
	os.Exit(dispatch(os.Args[1:], os.Stdout, os.Stderr))
}

// dispatch routes args to the appropriate role and returns the process
// exit code. It performs no I/O beyond stdout/stderr so it is testable
// without exec.
func dispatch(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return runTUI(stdout, stderr)
	}

	switch args[0] {
	case "daemon":
		return runDaemon(args[1:], stdout, stderr)
	case "shim":
		return runShim(args[1:], stdout, stderr)
	case "hook":
		return runHook(args[1:], os.Stdin, stderr)
	case "remote":
		return runRemote(args[1:], stdout, stderr)
	case "relay":
		return runRelay(args[1:], stdout, stderr)
	case "spawn":
		return dispatchAgentVerb(runSpawn, args[1:], nil, stdout, stderr)
	case "handoff":
		return dispatchAgentVerb(runHandoff, args[1:], nil, stdout, stderr)
	case "reattach":
		return dispatchAgentVerb(runReattach, args[1:], []string{protocol.CapExternalResume}, stdout, stderr)
	case "ls":
		return dispatchAgentVerb(runLS, args[1:], nil, stdout, stderr)
	case "doctor":
		// NOT dispatchAgentVerb: that seam ensures a daemon (D-1), and doctor's
		// whole contract is that it never starts one (see doctor.go's header).
		return runDoctor(args[1:], stdout, stderr)
	case "relogin":
		// The manual face of the ADR-024 auth watcher: needs Delete (which
		// agentClient lacks) plus the state dir for its local reads, so it
		// carries its own thin dispatch.
		return dispatchRelogin(args[1:], stdout, stderr)
	case "upgrade":
		// Same posture as doctor: the transaction stages, never activates, and
		// never starts a daemon.
		return runUpgrade(args[1:], stdout, stderr)
	case "watch":
		return dispatchAgentVerb(runWatch, args[1:], []string{"subscribe"}, stdout, stderr)
	case "kill":
		return dispatchAgentVerb(runKill, args[1:], nil, stdout, stderr)
	case "send":
		return dispatchAgentVerb(runSend, args[1:], nil, stdout, stderr)
	case "peek":
		return dispatchAgentVerb(runPeek, args[1:], nil, stdout, stderr)
	case "version", "--version":
		return runVersion(stdout)
	default:
		_, _ = fmt.Fprint(stderr, usage)
		return 2
	}
}

// runVersion is the `swarm version` (and `--version`) role (E13.2): it prints
// the build-time stamped version (internal/version.Version, "dev" unless
// overridden via -ldflags at release build time — see .goreleaser.yaml) plus
// the Go toolchain version. This is also the value the D-8 hello handshake
// reports to a connecting client (internal/protocol's Control.BuildVersion),
// so a client can tell it is talking to a different-build daemon.
func runVersion(stdout io.Writer) int {
	_, _ = fmt.Fprintf(stdout, "swarm %s (%s)\n", version.Version, runtime.Version())
	return 0
}

// runTUI is the no-argument role: it opens the client TUI on the real terminal
// (F1 — the Epic 8 milestone that assembles skeleton + attach + tui into the bare
// binary). It ensures a daemon is running (auto-start, D-1), dials a protocol
// client, builds the agent-detect and attach-runner seams, and runs the Bubble Tea
// program over the controlling terminal, handing the terminal to internal/attach on
// Enter and taking it back on detach. Without an interactive terminal (a pipe / CI)
// it fails with a clear message and a non-zero exit — never a panic or a half-drawn
// screen. A user-initiated quit (Esc, or SIGINT that Bubble Tea catches and turns
// into ErrInterrupted after restoring the terminal) is a clean exit.
func runTUI(stdout, stderr io.Writer) int {
	out, ok := interactiveTTY(stdout, os.Stdin)
	if !ok {
		_, _ = fmt.Fprintln(stderr, "swarm: not a terminal; the TUI needs an interactive terminal")
		return 1
	}

	cc, err := clientConfig()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "swarm: %v\n", err)
		return 1
	}
	client, err := dialClient(tuiCaps())
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "swarm: %v\n", err)
		return 1
	}
	defer func() { _ = client.Close() }()

	// prog is captured by the attach runner's terminal handoff; it is assigned just
	// before Run, so the closures see the live program when an attach fires.
	var prog *tea.Program
	runner := tui.NewAttachRunner(attachDialer(cc), tui.TerminalHandoff{
		Release: func() error { return prog.ReleaseTerminal() },
		Restore: func() error { return prog.RestoreTerminal() },
	})
	model := tui.New(client, detectAgents(os.Getenv(envFakeAgentBin)),
		tui.WithAttachRunner(runner), tui.WithDaemonRestarter(daemonRestarter(cc)))

	prog = tea.NewProgram(model, tea.WithInput(os.Stdin), tea.WithOutput(out))
	if _, err := prog.Run(); err != nil && !errors.Is(err, tea.ErrInterrupted) {
		_, _ = fmt.Fprintf(stderr, "swarm: tui: %v\n", err)
		return 1
	}
	return 0
}

// interactiveTTY verifies BOTH stdout and stdin are interactive terminals — the TUI
// needs both: Bubble Tea renders to stdout while the attach passthrough reads
// keystrokes from stdin, so a piped/redirected either end (a non-TTY) must be
// rejected up front rather than half-drawing a screen or blocking on dead input.
// Checking only stdout would let `swarm < /dev/null` (a redirected stdin) slip past.
// It returns the stdout file to render into when both are terminals.
func interactiveTTY(stdout io.Writer, stdin *os.File) (out *os.File, ok bool) {
	f, isFile := stdout.(*os.File)
	if !isFile || !term.IsTerminal(f.Fd()) {
		return nil, false
	}
	if stdin == nil || !term.IsTerminal(stdin.Fd()) {
		return nil, false
	}
	return f, true
}

// clientConfig builds the daemon.ClientConfig the client roles share (TUI dial,
// auto-start, and the `swarm daemon restart` reuse) from the SWARM_DAEMON_*
// environment, falling back to the default state dir. The SWARM_DAEMON_* knobs (the
// same ones `swarm daemon` reads) let a test point the client at a controlled daemon.
// DaemonBin is this executable, since the client auto-starts (and restarts) the daemon
// from its own binary.
func clientConfig() (daemon.ClientConfig, error) {
	stateDir := os.Getenv(daemon.EnvStateDir)
	if stateDir == "" {
		var err error
		if stateDir, err = persist.DefaultDir(); err != nil {
			return daemon.ClientConfig{}, err
		}
	}
	exe, _ := os.Executable()
	return daemon.ClientConfig{
		StateDir:   stateDir,
		SocketPath: envOr(daemon.EnvSocket, filepath.Join(stateDir, "daemon.sock")),
		LockPath:   envOr(daemon.EnvLock, filepath.Join(stateDir, "daemon.lock")),
		LogPath:    envOr(daemon.EnvLog, filepath.Join(stateDir, "daemon.log")),
		DaemonBin:  exe,
	}, nil
}

// tuiCaps is the capability set the TUI's ROSTER client offers at hello. It has ONE
// definition because the TUI dials twice — runTUI for the long-lived client, and
// daemonRestarter for the replacement client after a daemon auto-upgrade — and a set
// that is right in one place and wrong in the other yields a feature that works until
// the daemon upgrades itself and then silently starts refusing.
//
// CapHandsOffHandoff is offered because the TUI's handoff form can submit a
// `handoff_from` launch (ADR-010 Amendment 4 E1) and the daemon refuses that option
// CodeCapabilityRefused when it was not negotiated. Unlike the one-shot `swarm
// reattach` verb, which negotiates external-resume immediately before its launch, the
// TUI dials once at startup and cannot re-hello per launch, so it offers the
// capability up front; a daemon that does not know it simply does not intersect it,
// which is exactly the signal the form's refusal is built on.
//
// attachDialer's per-attach dial deliberately does NOT use this set: it offers
// {"attach"} and never submits a launch.
func tuiCaps() []string {
	return []string{"attach", "subscribe", protocol.CapHandsOffHandoff, protocol.CapContextGuardSettings}
}

// dialClient ensures a daemon is running (auto-start, D-1) and returns a connected
// protocol client to it, offering caps. EnsureDaemon only spawns one when the socket
// does not answer. It builds its own ClientConfig so the remote subcommands can call
// it bare; runTUI additionally builds one for the attach dialer and daemon restarter.
func dialClient(caps []string) (*protocol.Client, error) {
	cc, err := clientConfig()
	if err != nil {
		return nil, err
	}
	conn, err := daemon.EnsureDaemon(cc)
	if err != nil {
		return nil, err
	}
	_ = conn.Close() // EnsureDaemon proved the daemon is live; the caller speaks the full client protocol on its own dial
	return protocol.Dial(cc.SocketPath, caps)
}

// attachDialer builds the per-attach dialer the TUI's attach runner uses: it dials a
// FRESH protocol client to the daemon socket for EACH attach and returns that client's
// Close as the cleanup. Dialing per attach — rather than multiplexing the TUI's
// long-lived client connection — keeps attach working across a daemon auto-upgrade, which
// swaps that long-lived client out from under the runner (bd agents-tracker-5jl); the old
// code closed over the original client and, after the swap, attached on its dead conn
// (item 1, the blocker). The fresh conn is closed by the returned cleanup once the
// passthrough returns; on a dial/attach failure it is closed before returning the error.
func attachDialer(cc daemon.ClientConfig) tui.AttachDialer {
	return func(id string) (attach.Session, func(), error) {
		c, err := protocol.Dial(cc.SocketPath, []string{"attach"})
		if err != nil {
			return nil, nil, err
		}
		att, err := c.Attach(id)
		if err != nil {
			_ = c.Close()
			return nil, nil, err
		}
		return att, func() { _ = c.Close() }, nil
	}
}

// daemonRestarter is the client-side reuse of `swarm daemon restart` injected into the
// TUI (bd agents-tracker-5jl): it performs the D-8 safe restart of an outdated daemon
// and reconnects to the replacement. Its shims survive the handoff (they own the PTYs)
// and are reconnected by the replacement — the same guarantee `swarm daemon restart`
// gives, now driven automatically when the client is newer than the daemon it reached.
func daemonRestarter(cc daemon.ClientConfig) tui.DaemonRestarter {
	return func() (tui.Client, error) {
		if err := daemon.Restart(cc); err != nil {
			return nil, err
		}
		c, err := protocol.Dial(cc.SocketPath, tuiCaps())
		if err != nil {
			return nil, err
		}
		return c, nil
	}
}

// agentDetectFunc probes one already-constructed adapter and reports its
// Detection. The production default runs the real exec-based detect.Host (plus
// the best-effort on-disk model discovery, v0.5); tests inject a stub so
// detectAgentsWith's concurrency can be proven with a barrier instead of a
// wall-clock assertion (R-A2).
type agentDetectFunc func(ad adapter.Adapter) adapter.Detection

// detectAgents builds the launch-form agent detector. It probes the host for
// every registered PRODUCTION adapter through the CORE adapter.Detect + the real
// exec-based detect.Host, so the picker greys an agent that is missing or
// out-of-supported-range (L-2). The reserved dev/test "fake" agent is appended when
// SWARM_FAKE_AGENT_BIN is set (unset in a real install). Detection runs the free
// `--version` probe only — never a billable agent run.
func detectAgents(fakeBin string) tui.DetectFunc {
	host := detect.Host{}
	return detectAgentsWith(fakeBin, func(ad adapter.Adapter) adapter.Detection {
		det := adapter.Detect(ad, host)
		// Piggyback the best-effort model discovery on the same probe: pre-fill
		// the form's model field with the real configured default and cycle the
		// CLI's real choices (v0.5, bead e5i). Read failures leave these empty
		// and the option renders exactly as before.
		det.ConfiguredModel, det.Models = detect.ProbeModels(ad.Name())
		return det
	})
}

// detectAgentsWith is detectAgents with the per-adapter probe INJECTED (R-A2), so
// a test can substitute a barrier stub for the real host. It probes every
// PRODUCTION adapter (registry.IsProduction — never a literal name match, which
// would leak a future fixture-only adapter into the picker) CONCURRENTLY, one
// goroutine per adapter: at probeTimeout up to 5s each, N production CLIs probed
// serially would gate the launch form at N*5s worst case. Results are joined and
// reported in registry.Names()'s deterministic sorted order regardless of which
// goroutine finishes first.
func detectAgentsWith(fakeBin string, probe agentDetectFunc) tui.DetectFunc {
	return func() []tui.AgentInfo {
		names := registry.Names()
		slots := make([]tui.AgentInfo, len(names))
		present := make([]bool, len(names))
		translated := rosettaTranslated() // probed once: swarm x86_64 under Rosetta (bead 8c0)

		var wg sync.WaitGroup
		for i, name := range names {
			if !registry.IsProduction(name) {
				continue // fixture-only adapters (e.g. "reference") are not installable CLIs
			}
			ad, ok := registry.New(name)
			if !ok {
				continue
			}
			wg.Add(1)
			go func(i int, name string, ad adapter.Adapter) {
				defer wg.Done()
				det := probe(ad)
				slots[i] = tui.AgentInfo{
					Name:      name,
					Installed: det.Found,
					InRange:   det.InRange,
					Reason:    archAugmentedReason(unavailabilityReason(det), det, translated),
					Options:   overlayModelOptions(ad.Options(), det.ConfiguredModel, det.Models),
				}
				present[i] = true
			}(i, name, ad)
		}
		wg.Wait()

		agents := make([]tui.AgentInfo, 0, len(names)+1)
		for i, ok := range present {
			if ok {
				agents = append(agents, slots[i])
			}
		}
		if fakeBin != "" {
			agents = append(agents, tui.AgentInfo{
				Name:      "fake",
				Installed: true,
				InRange:   true,
				Options:   []adapter.OptionSpec{{Key: "script", Label: "Script path", Type: "string", Required: true}},
			})
		}
		return agents
	}
}

// overlayModelOptions augments the "model" launch option with what the CLI is
// actually configured to use, discovered from its on-disk config: the real
// default pre-fills the field (Default) and the discovered choices become the
// left/right cycle values (Suggest, layered over any curated aliases). Non-model
// options, and adapters with nothing discovered, are returned untouched. The
// input specs are never mutated — a fresh slice is returned when anything changes.
func overlayModelOptions(specs []adapter.OptionSpec, configured string, models []adapter.ModelChoice) []adapter.OptionSpec {
	if configured == "" && len(models) == 0 {
		return specs
	}
	out := make([]adapter.OptionSpec, len(specs))
	copy(out, specs)
	for i, spec := range out {
		if spec.Key != "model" {
			continue
		}
		if configured != "" {
			spec.Default = configured
		}
		if len(models) > 0 {
			// The CLI's own catalog replaces any curated aliases outright.
			suggest := make([]string, len(models))
			for j, m := range models {
				suggest[j] = m.ID
			}
			spec.Suggest = suggest
		} else if configured != "" {
			// Default-only discovery (claude): the real default leads the curated
			// aliases, deduplicated.
			suggest := []string{configured}
			for _, s := range spec.Suggest {
				if s != configured {
					suggest = append(suggest, s)
				}
			}
			spec.Suggest = suggest
		}
		out[i] = spec
	}
	return out
}

// unavailabilityReason derives a short, human-readable cause an agent cannot launch
// from its Detection, so the launch picker greys the agent WITH an explanation
// instead of an indistinguishable dot (the v0.3 field-test gap: a broken codex whose
// version probe fails rendered like a usable one). A usable or plainly not-installed
// agent has no reason — the latter keeps the existing install-hint behavior.
func unavailabilityReason(det adapter.Detection) string {
	switch {
	case !det.Found:
		return "" // not installed: existing install-hint behavior
	case det.Version == "":
		// A crashed probe carries the CLI's own first error line; show that real cause
		// (e.g. codex's "Missing optional dependency ... Reinstall Codex") rather than
		// the generic hint (bead 8c0).
		if det.ProbeErr != "" {
			return det.ProbeErr
		}
		return "version probe failed - reinstall?"
	case !det.InRange:
		return "unsupported version " + det.Version
	default:
		return ""
	}
}

// rosettaRebuildHint is appended to a found-but-crashed agent's reason when swarm
// itself is an x86_64 binary under Rosetta on Apple Silicon (bead 8c0): the crash
// is almost always that codex's env-node then resolves the x64 CLI package npm
// never installs on arm64. Rebuilding swarm native arm64 fixes it.
const rosettaRebuildHint = "(swarm is x86_64 under Rosetta; rebuild native: CGO_ENABLED=0 GOARCH=arm64 go build ./cmd/swarm)"

// archAugmentedReason appends the Rosetta rebuild hint to a found-but-crashed
// agent's reason when this swarm process is running translated (bead 8c0). A
// usable agent (empty base reason), a not-installed agent, and a plainly
// out-of-range agent (which reports a version, so is not an arch symptom) are
// left untouched.
func archAugmentedReason(base string, det adapter.Detection, translated bool) string {
	if base == "" || !translated || !det.Found || det.Version != "" {
		return base
	}
	return base + " " + rosettaRebuildHint
}

// runDaemon runs the `swarm daemon` role. `swarm daemon restart` performs the
// D-8 safe restart from the CALLER's environment (the owner's shell), and
// `swarm daemon restart --unattended` performs the nightly converge instead
// (auto-upgrade plan L2, ADR-020). A plain `swarm daemon` stands up the FULL assembly
// (internal/skeleton) from its SWARM_DAEMON_* environment (set by the client's
// detached auto-start, D-1) and serves until signalled; with no such configuration
// it is a no-op stub, since the daemon is never started bare by a user — the
// client auto-starts it.
func runDaemon(args []string, _, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "restart" {
		switch {
		case len(args) == 1:
			return runDaemonRestart(stderr)
		case len(args) == 2 && args[1] == "--unattended":
			return runDaemonRestartUnattended(stderr)
		default:
			// 2 is this binary's usage-error status and also converge.ExitDeferred; the
			// plist template is tested to carry the exact flag, and this stderr line is
			// what tells the two apart in the timer's log.
			_, _ = fmt.Fprintln(stderr, daemonRestartUsage)
			return 2
		}
	}
	cfg, ok := skeletonConfigFromEnv()
	if !ok {
		_, _ = fmt.Fprintln(stderr, "daemon: not implemented")
		return 1
	}
	d, err := skeleton.Serve(cfg)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "daemon: serve: %v\n", err)
		return 1
	}
	// Serve until a termination signal, then Close cleanly (running shims are
	// independent and survive; the singleton lock is released).
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	<-sigs
	_ = d.Close()
	return 0
}

// skeletonConfigFromEnv builds the assembly Config from the SWARM_DAEMON_*
// environment (plus the dev/test-only fake-agent knob). It reports false when no
// state dir is configured (the bare-invocation stub).
func skeletonConfigFromEnv() (skeleton.Config, bool) {
	stateDir := os.Getenv(daemon.EnvStateDir)
	if stateDir == "" {
		return skeleton.Config{}, false
	}
	exe, _ := os.Executable() // the daemon spawns `swarm shim` from its own binary
	return skeleton.Config{
		StateDir:           stateDir,
		SocketPath:         os.Getenv(daemon.EnvSocket),
		LockPath:           os.Getenv(daemon.EnvLock),
		LogPath:            os.Getenv(daemon.EnvLog),
		RemoteSocketPath:   gatewaySocket(stateDir), // ADR-007 B15: the SAME definition the unit dials
		ShimBinary:         exe,
		MaxSessions:        defaultMaxSessions,
		PollInterval:       daemonPollInterval,
		StalenessThreshold: daemonStalenessThreshold,
		FakeAgentBin:       os.Getenv(envFakeAgentBin),
	}, true
}

// runDaemonRestart stops the running daemon and spawns a fresh one (D-8). Its
// shims survive the handoff and are reconnected by the replacement.
func runDaemonRestart(stderr io.Writer) int {
	cc, err := clientConfig()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "daemon restart: %v\n", err)
		return 1
	}
	if err := daemon.Restart(cc); err != nil {
		_, _ = fmt.Fprintf(stderr, "daemon restart: %v\n", err)
		return 1
	}
	return 0
}

// envOr returns the environment value for key, or fallback when it is unset.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// shimLaunchConfig is the JSON launch contract for `swarm shim --config`,
// decoded into a shim.Config.
type shimLaunchConfig struct {
	SessionID  string   `json:"session_id"`
	Argv       []string `json:"argv"`
	Cwd        string   `json:"cwd"`
	Env        []string `json:"env"`
	SocketPath string   `json:"socket_path"`
	SessionDir string   `json:"session_dir"`
	Cols       int      `json:"cols"`
	Rows       int      `json:"rows"`
	GraceMS    int      `json:"grace_ms"`
	// HookSocketPath is the per-session shim-owned hook UDS (playbook §6.1); "" (an
	// old, pre-R6 launch config with no such key) disables it entirely (requirement
	// 7's compat), leaving shim.Run's behavior exactly what it is today.
	HookSocketPath string `json:"hook_socket_path"`
	// HookDrainToken gates the hook socket's DRAIN verb (R6 review fix-pack,
	// SECURITY HIGH): a DEDICATED per-session secret, distinct from lc.Env's own
	// hookclient.EnvToken -- the POST-side token, which necessarily reaches the
	// agent's own environment to authenticate a live hook post. DRAIN is both
	// destructive (FoldSeq compacts on the caller's say-so) and read-everything, so
	// gating it with the SAME secret the agent process (and every hook script or
	// child it spawns) already holds would let the least-trusted party in the
	// system fold away or read the whole spool. "" (an old launch config with no
	// such key, or one a future wave has not yet wired to mint a value here) is the
	// shim's own "no token configured" compat default -- DRAIN's check does not run
	// at all, exactly HookSocketPath's "unset means disabled" convention.
	HookDrainToken string `json:"hook_drain_token"`
	// The SESSION BACKEND (Wave R7, Mirror M4.1; ADR-013 §R7.2b). BackendSocketPath == ""
	// -- an old launch config, or any session of any CLI that needs no side process -- is
	// the pre-R7 session and shim.Run's spawn path is then byte-for-byte what it is today.
	// BackendProgram is the RESOLVED absolute path the daemon obtained through the adapter
	// contract's LookPath discipline, so the shim never searches PATH itself.
	BackendProgram    string   `json:"backend_program"`
	BackendArgs       []string `json:"backend_args"`
	BackendAgentArgs  []string `json:"backend_agent_args"`
	BackendSocketPath string   `json:"backend_socket_path"`
	BackendEnv        []string `json:"backend_env"`
}

// shimConfigFromLaunch maps a decoded launch config onto shim.Config -- the pure
// translation runShim itself used to build inline, extracted so it is testable
// without running a real shim process (setsid, a real agent, ...).
//
// HookToken is lc.HookDrainToken VERBATIM -- deliberately NOT derived from lc.Env
// (R6 review fix-pack, SECURITY HIGH: the shimLaunchConfig field's own doc explains
// why). Minting that dedicated per-session value and threading it into the launch
// config is the daemon's own launch/spawn wiring, out of this file's owned scope;
// an old (or not-yet-updated) launch config carrying no such field yields "", the
// shim's compat default of "no token configured".
func shimConfigFromLaunch(lc shimLaunchConfig) shim.Config {
	return shim.Config{
		SessionID:      lc.SessionID,
		Argv:           lc.Argv,
		Cwd:            lc.Cwd,
		Env:            lc.Env,
		SocketPath:     lc.SocketPath,
		SessionDir:     lc.SessionDir,
		Cols:           lc.Cols,
		Rows:           lc.Rows,
		TranscriptCfg:  transcript.Config{MaxBytes: 8 << 20, MaxFiles: 3},
		GraceTimeout:   time.Duration(lc.GraceMS) * time.Millisecond,
		HookSocketPath: lc.HookSocketPath,
		HookToken:      lc.HookDrainToken,
		Backend:        backendConfigFromLaunch(lc),
	}
}

// backendConfigFromLaunch builds the shim's BackendConfig, or nil.
//
// BOTH the program and the socket must be named: a declared backend with no program starts
// nothing while the agent attaches to a socket nobody serves (the adapter contract's
// obligation 8, restated at the last place that can still refuse), and a program with no
// socket has no endpoint for the agent to be pointed at. Either alone is a malformed config
// and nil -- the pre-R7 session -- is the safe reading of it.
func backendConfigFromLaunch(lc shimLaunchConfig) *shim.BackendConfig {
	if lc.BackendSocketPath == "" || lc.BackendProgram == "" {
		return nil
	}
	env := lc.BackendEnv
	if len(env) == 0 {
		// The backend inherits the AGENT's filtered environment when the daemon named none:
		// it is the same CLI in a different mode and needs the same auth and config, and
		// lc.Env has already passed the launch-environment allowlist.
		env = lc.Env
	}
	return &shim.BackendConfig{
		Program:    lc.BackendProgram,
		Args:       lc.BackendArgs,
		Env:        env,
		SocketPath: lc.BackendSocketPath,
	}
}

// runShim parses --config, detaches from any controlling terminal, and runs the
// shim engine, exiting with the agent's exit code. A missing --config is a usage
// error (exit 2).
func runShim(args []string, _, stderr io.Writer) int {
	fs := flag.NewFlagSet("shim", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "path to the JSON launch config")
	if err := fs.Parse(args); err != nil {
		_, _ = fmt.Fprint(stderr, shimUsage)
		return 2
	}
	if *configPath == "" {
		_, _ = fmt.Fprint(stderr, shimUsage)
		return 2
	}

	data, err := os.ReadFile(*configPath)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "shim: read config: %v\n", err)
		return 2
	}
	var lc shimLaunchConfig
	if err := json.Unmarshal(data, &lc); err != nil {
		_, _ = fmt.Fprintf(stderr, "shim: parse config: %v\n", err)
		return 2
	}

	// Guarantee the shim leads its own session so it outlives the launching
	// terminal (E4.1 "Shim setsids", D-3). On success we proceed; if a re-exec
	// was needed to acquire the session, we return its child's exit code; any
	// unexpected failure is fatal.
	code, reexeced, err := ensureSession()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "shim: %v\n", err)
		return 1
	}
	if reexeced {
		return code
	}

	cfg := shimConfigFromLaunch(lc)
	exit, err := shim.Run(cfg)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "shim: %v\n", err)
		if exit == 0 {
			return 1 // a setup failure with no agent exit code to report
		}
	}
	return exit
}

// ensureSession makes the shim a session leader (E4.1). It returns:
//   - reexeced=false, err=nil: this process is now (or already was) a session
//     leader — proceed to run the shim here.
//   - reexeced=true: we could not setsid in place, so we re-exec'd ourselves
//     with SysProcAttr{Setsid:true} and ran the shim in that child; exitCode is
//     the child's exit code, which the caller must return.
//   - err!=nil: an unexpected, fatal failure — never silently proceed.
func ensureSession() (exitCode int, reexeced bool, err error) {
	if _, serr := syscall.Setsid(); serr == nil {
		return 0, false, nil // we are now a session leader
	} else if !errors.Is(serr, syscall.EPERM) {
		return 0, false, fmt.Errorf("setsid: %w", serr)
	}
	// EPERM: we are already a process-group leader. If we already lead the
	// session, that is fine; otherwise we must re-exec to acquire one.
	if sid, gerr := unix.Getsid(0); gerr == nil && sid == os.Getpid() {
		return 0, false, nil
	}
	if os.Getenv(shimSessionEnv) == "1" {
		return 0, false, errors.New("setsid: not a session leader even after re-exec")
	}
	code, rerr := reexecWithSetsid()
	if rerr != nil {
		return 0, false, rerr
	}
	return code, true, nil
}

// reexecWithSetsid re-launches this binary with the same args in a fresh session
// (SysProcAttr.Setsid), guarded by shimSessionEnv to prevent re-exec loops, and
// returns the child's exit code.
func reexecWithSetsid() (int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("locate self for setsid re-exec: %w", err)
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), shimSessionEnv+"=1")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode(), nil
		}
		return 0, fmt.Errorf("setsid re-exec: %w", err)
	}
	return 0, nil
}

// runHook runs the `swarm hook <event>` role (E10.1 / G4, Epic 11 mapping bridge):
// it composes an authenticated status callback from the per-session environment
// injected at spawn (session id, live token, daemon socket, monotonic sequence) and
// posts it to the daemon socket. The hook CLI (e.g. Claude Code) posts its JSON
// payload on STDIN, whose top-level fields are extracted into the callback payload;
// the engine then NORMALIZES {event, payload} into status dimensions via the
// session's registered SignalSources (the adapter's event->status table). Explicit
// `key=value` args still work (and override a stdin field of the same name), so
// `swarm hook Stop` and `swarm hook Notification notification_type=idle` both work.
// A bare `swarm hook` with no event has nothing to post.
//
// For an event the session's adapter declared capture=raw on (ADR-010 §6), the callback ALSO
// carries the CLI's body whole: the flattened payload keeps top-level strings only, so a tool's
// input, its response and a diff — all nested objects — exist nowhere else by the time the
// daemon sees the post. The status path is untouched by that: the same flattening, the same
// reserved-key guard, the same dimensions.
func runHook(args []string, stdin io.Reader, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "hook: not implemented")
		return 1
	}
	body := readHookBody(stdin)
	payload := parseHookStdin(bytes.NewReader(body))
	for k, v := range parseHookPayload(args[1:]) {
		payload[k] = v // explicit args override a stdin field of the same name
	}
	cb, err := hookclient.FromEnv(os.Getenv, args[0], payload)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "hook: %v\n", err)
		return 1
	}
	if hookclient.CapturesRaw(os.Getenv, args[0]) && json.Valid(body) {
		// json.Valid is the cap's teeth as much as a sanity check: a body OVER hookStdinLimit
		// arrives truncated, and a truncated object is neither the whole body §6 asks for nor
		// encodable onto the wire — json.Marshal fails on an invalid RawMessage, which would take
		// this session's STATUS down with it. Untrusted tool output never gets to do that, so an
		// unparseable body is dropped and the status post goes on exactly as before.
		cb.Raw = body
	}
	// PostSmart (requirement 7): prefers the per-session shim hook socket when the
	// daemon injected one, retrying a reachable-but-silent shim before falling back
	// to the daemon socket -- honest about which path served, and unchanged from
	// today's bare Post when EnvHookSocket is unset (every pre-R6 shim).
	hookSock := os.Getenv(hookclient.EnvHookSocket)
	path, err := hookclient.PostSmart(hookSock, os.Getenv(hookclient.EnvSocket), cb)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "hook: %v\n", err)
		return 1
	}
	// R6 review fix-pack round 1 (LOW 9): requirement 7's "honest about which path
	// served" was API-only -- PostSmart returned the path and this call discarded it,
	// so an operator debugging a partial upgrade had nothing to look at. ONE case earns
	// a line: a shim hook socket WAS configured and the daemon carried the post anyway,
	// which is precisely the mid-upgrade state (an old shim, or a shim whose socket
	// went away) and precisely the state in which the spool's survival boundary is not
	// in play. The ordinary shim-served path stays silent: this CLI runs once per hook,
	// inside the agent's own output.
	if hookSock != "" && path != hookclient.HookPathShim {
		_, _ = fmt.Fprintf(stderr, "hook: shim hook socket %s did not carry this post; served by the %s socket instead\n", hookSock, path)
	}
	return 0
}

// hookStdinLimit bounds how much of a hook's stdin payload we read. Claude posts a
// small JSON object; the cap guards against an unbounded or garbage stream.
const hookStdinLimit = 1 << 20

// readHookBody reads at most hookStdinLimit bytes of a hook's stdin. It is total: a nil or
// unreadable stream yields nothing. The bytes are read ONCE and used twice — flattened into the
// status payload, and (for a capture=raw event) carried whole on the callback — because stdin is
// a stream and the second reader would find it empty.
func readHookBody(r io.Reader) []byte {
	if r == nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(r, hookStdinLimit))
	if err != nil {
		return nil
	}
	return data
}

// parseHookStdin reads a hook's JSON payload from r (Claude Code posts it on stdin)
// and extracts its top-level STRING fields into a status payload the engine
// normalizes via the session's SignalSources. It is best-effort and total: nil,
// empty, non-JSON, or a non-object stream yields an empty (never nil) map. The
// reserved dimension keys "turn"/"interaction" are skipped, so a crafted payload
// cannot inject a status dimension directly — deriving those from the event is the
// engine's job.
func parseHookStdin(r io.Reader) map[string]string {
	out := map[string]string{}
	if r == nil {
		return out
	}
	data, err := io.ReadAll(io.LimitReader(r, hookStdinLimit))
	if err != nil || len(bytes.TrimSpace(data)) == 0 {
		return out
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil {
		return out
	}
	for k, raw := range obj {
		if k == "turn" || k == "interaction" { // engine.PayloadKey* — never client-injected
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" {
			out[k] = s
		}
	}
	return out
}

// parseHookPayload turns `key=value` args into a status-dimension payload,
// ignoring any arg without '='. Returns nil when there is nothing to carry.
func parseHookPayload(args []string) map[string]string {
	if len(args) == 0 {
		return nil
	}
	m := make(map[string]string, len(args))
	for _, a := range args {
		if k, v, ok := strings.Cut(a, "="); ok {
			m[k] = v
		}
	}
	return m
}
