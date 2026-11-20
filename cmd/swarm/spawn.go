package main

// `swarm spawn` — the agent-facing WRITE verb of ADR-010 Phase 2 (D1/D2, Amendment
// A4): launch a NEW session with continuity of context, either from inline
// instructions (--prompt) or from an agent-authored document (--handoff /
// --delegate) that is copied into a private temporary directory of its own and
// pointed at by a one-line initial prompt, so instructions never travel as argv.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/hookclient"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/Nathandela/swarm/internal/workermail"
	"github.com/Nathandela/swarm/internal/worktree"
)

// The grid a spawned session starts on: the mobile default, the one size every
// client can render without reflowing.
const (
	spawnCols = 80
	spawnRows = 24
)

// runSpawn is `swarm spawn --cli <agent> [--dir d] [--model m] [--worktree]
// [--name n] (--prompt <text> | --handoff <file> | --delegate <file>)`. On success
// the new session id goes to stdout ALONE, so an agent can pipe it straight into
// `swarm watch`; the human-facing name goes to stderr. Argument refusals exit 2 (the
// runLS convention) before any file I/O; everything else exits 1.
func runSpawn(args []string, c agentClient, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("spawn", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cli := fs.String("cli", "", "agent CLI to launch (required)")
	dir := fs.String("dir", "", "working directory (default: the caller's cwd)")
	model := fs.String("model", "", "model to launch the agent with")
	isolate := fs.Bool("worktree", false, "run the session in an isolated git worktree (default for --handoff and --delegate in a git checkout)")
	name := fs.String("name", "", "session label (default: the daemon's)")
	prompt := fs.String("prompt", "", "inline instructions for the new session")
	handoff := fs.String("handoff", "", "handoff document to hand the new session")
	delegate := fs.String("delegate", "", "delegation document to hand the new session")
	operationID := fs.String("operation-id", "", "durable operation ID from swarm spawn --new-operation-id; reuse only for an exact retry")
	generateID := fs.Bool("new-operation-id", false, "print a new durable operation ID for a later launch")
	statusID := fs.String("status", "", "read the durable launch outcome for this operation ID as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "spawn: unexpected positional arguments")
		return 2
	}
	if *generateID {
		if fs.NFlag() != 1 {
			_, _ = fmt.Fprintln(stderr, "spawn: --new-operation-id takes no other arguments")
			return 2
		}
		id, err := protocol.NewLaunchOperationID()
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		_, _ = fmt.Fprintln(stdout, id)
		return 0
	}
	var emptyID bool
	fs.Visit(func(f *flag.Flag) {
		if (f.Name == "operation-id" || f.Name == "status") && f.Value.String() == "" {
			emptyID = true
		}
	})
	if emptyID {
		_, _ = fmt.Fprintln(stderr, "spawn: operation ID must not be empty")
		return 2
	}
	if *statusID != "" {
		if fs.NFlag() != 1 {
			_, _ = fmt.Fprintln(stderr, "spawn: --status takes only an operation ID")
			return 2
		}
		reader, ok := c.(interface {
			LaunchOperationStatus(string) (schema.OperationOutcomeView, error)
		})
		if !ok {
			_, _ = fmt.Fprintln(stderr, "spawn: launch status requires an updated daemon")
			return 1
		}
		outcome, err := reader.LaunchOperationStatus(*statusID)
		if err == nil {
			err = json.NewEncoder(stdout).Encode(outcome)
		}
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "spawn: %v\n", err)
			return 1
		}
		return 0
	}
	if *operationID != "" && !protocol.LaunchOperationReplayable(*operationID, time.Now()) {
		_, _ = fmt.Fprintln(stderr, "spawn: invalid or expired operation ID; check an earlier attempt with --status")
		return misuseExit
	}
	if *cli == "" {
		_, _ = fmt.Fprintln(stderr, "spawn: --cli is required: swarm spawn --cli <agent> (--prompt t | --handoff f | --delegate f)")
		return 2
	}
	sources := 0
	for _, src := range []string{*prompt, *handoff, *delegate} {
		if src != "" {
			sources++
		}
	}
	if sources != 1 {
		_, _ = fmt.Fprintln(stderr, "spawn: need exactly one of --prompt, --handoff or --delegate")
		return 2
	}

	cwd := *dir
	if cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "spawn: %v\n", err)
			return 1
		}
		cwd = wd
	}
	// The daemon is long-lived and resolves nothing against the CALLER's cwd: a
	// relative --dir sent verbatim would be stat'd against the daemon's own cwd,
	// launching the child in the wrong place or refusing a real directory.
	cwd, err := filepath.Abs(cwd)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "spawn: %v\n", err)
		return 1
	}

	// --handoff and --delegate share their mechanics and differ only in recorded
	// intent (D2); --prompt is the lightweight inline alternative (A4), recorded as a
	// delegation since the child is being handed work, not a conversation.
	initial, intent := *prompt, protocol.SpawnIntentDelegate
	doc := *delegate
	if *handoff != "" {
		doc, intent = *handoff, protocol.SpawnIntentHandoff
	}
	// A handoff or delegation document hands work to another agent, which gets a git
	// worktree of its own unless the caller says otherwise (ADR-010 Amendment 10):
	// sharing the caller's checkout, or improvising one under /tmp, is how work is lost.
	explicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "worktree" {
			explicit = true
		}
	})
	if doc != "" && !explicit && worktree.IsRepo(cwd) {
		*isolate = true
	}
	parent := os.Getenv(hookclient.EnvSessionID)
	dispatcher := os.Getenv(schema.EnvManagerVMCLI) == "1"
	var dispatcherProof string
	if dispatcher && intent == protocol.SpawnIntentDelegate {
		dispatcherProof = os.Getenv(workermail.ProofEnv)
		if dispatcherProof == "" {
			dispatcherProof = workermail.Proof(os.Getenv(hookclient.EnvToken), "spawn")
		}
		if parent == "" || dispatcherProof == "" {
			_, _ = fmt.Fprintln(stderr, "spawn: this retained dispatcher lacks its worker mailbox credential; allow this manager to park and resume the same discussion on the upgraded supervisor")
			return 1
		}
	}
	if *operationID == "" && parent != "" && dispatcher {
		*operationID, err = protocol.NewLaunchOperationID()
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		// Publish the recovery coordinate before sending anything to the daemon.
		if _, err := fmt.Fprintf(stderr, "operation_id=%s\n", *operationID); err != nil {
			return 1
		}
	}
	var dest string
	if doc != "" {
		if *operationID == "" {
			dest, err = copyHandoff(doc)
		} else {
			dest, err = operationHandoff(*operationID, doc)
		}
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "spawn: %v\n", err)
			return 1
		}
		// The prompt is a POINTER at the copied document, never its body (A4).
		initial = "Read and follow the instructions in " + dest + "."
	}

	req := protocol.LaunchReq{
		Agent:         *cli,
		Name:          *name, // empty lets the daemon default it
		Cwd:           cwd,
		Env:           os.Environ(), // the daemon allowlist-filters it (S-6)
		Cols:          spawnCols,
		Rows:          spawnRows,
		InitialPrompt: initial,
		Worktree:      *isolate,
	}
	if *model != "" {
		req.Options = map[string]string{"model": *model}
	}
	// The daemon injects SWARM_SESSION_ID into every agent it launches, so it is set
	// exactly when the caller IS a session. A human at a plain terminal has no parent,
	// and the daemon refuses an intent without one, so neither field is sent there.
	if parent != "" {
		req.SpawnedFrom, req.SpawnIntent = parent, intent
		req.DispatcherProof = dispatcherProof
	}

	var id, canonical string
	if *operationID == "" {
		id, canonical, err = c.Launch(req)
	} else {
		launcher, ok := c.(ownerLaunchClient)
		if !ok {
			_, _ = fmt.Fprintln(stderr, "spawn: reliable launch requires an updated daemon")
			return 1
		}
		req.Env = daemon.OwnerLaunchEnvironment(req.Agent, req.Env)
		slices.Sort(req.Env)
		id, canonical, err = launcher.LaunchOperation(*operationID, req)
	}
	if err != nil {
		// A refused launch must not strand its handoff copy: retries against a
		// full daemon would otherwise accumulate documents no session references.
		if dest != "" && *operationID == "" {
			_ = os.RemoveAll(filepath.Dir(dest)) // the private directory is the copy's
		}
		_, _ = fmt.Fprintf(stderr, "spawn: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, id)
	_, _ = fmt.Fprintf(stderr, "spawned %s (%s)\n", canonical, id)
	return 0
}

// copyHandoff snapshots the agent-authored document into a private temporary
// directory of its own and returns the copy's absolute path. The copy is the point
// (D2): the child reads a stable path of swarm's while the source's own file may go
// away. It is NOT under the swarm state dir (ADR-010 Amendment 5 F3): a codex source
// runs this command inside its workspace-write sandbox, where the state dir is
// read-only and the temp dir is the one writable place outside the checkout, and a
// 0700 directory of its own protects the copy exactly as the state dir did.
func copyHandoff(src string) (string, error) {
	body, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "swarm-handoff-") // 0700, one per handoff, no squatting
	if err != nil {
		return "", err
	}
	// The pointer prompt must name a path the CHILD can resolve from its own cwd,
	// so the destination is forced absolute even under a relative TMPDIR.
	dest, err := filepath.Abs(filepath.Join(dir, "handoff.md"))
	if err == nil {
		err = os.WriteFile(dest, body, 0o600)
	}
	if err != nil {
		_ = os.RemoveAll(dir) // never strand a directory the caller will not hear about
		return "", err
	}
	return dest, nil
}
