package main

// `swarm remote presets` -- the SETUP UX by which the MACHINE authors launch presets
// (Wave R5 deliverable 1, playbook 4.3: presets are machine-authored; the phone only
// ever selects and confirms). Authoring stores the root as its CANONICAL
// (symlink-resolved) real path -- the same fully-resolved path the policy checks and
// the shim receives (ADR-007 D8) -- so what the operator sees listed is what runs.

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/Nathandela/swarm/internal/adapter/registry"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
)

const presetsUsage = `usage: swarm remote presets <command>

  swarm remote presets add --name <display> --agent <provider> --root <dir>
        [--worktree] [--option key=value ...]
        author a launch preset (mints a stable opaque id; the root is stored
        as its canonical, symlink-resolved real path; each --option authors one
        allowlisted launch option the confirm sheet and the launch carry)
  swarm remote presets list
        list the authored presets (id, name, provider, canonical root, revision)
  swarm remote presets edit <id> [--name <display>] [--agent <provider>]
        [--root <dir>] [--worktree=<true|false>] [--option key=value ...]
        re-author a preset IN PLACE: same id, new revision -- a phone that
        confirmed the old revision then refuses stale_preset. Passing any
        --option REPLACES the authored option set
  swarm remote presets remove <id>
        withdraw a preset: the id stops being authored, and a phone that still
        names it refuses unknown_preset
`

// runRemotePresets dispatches the presets authoring verbs.
func runRemotePresets(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, presetsUsage)
		return 2
	}
	switch args[0] {
	case "add":
		return runRemotePresetsAdd(args[1:], stdout, stderr)
	case "list":
		return runRemotePresetsList(stdout, stderr)
	case "edit":
		return runRemotePresetsEdit(args[1:], stdout, stderr)
	case "remove":
		return runRemotePresetsRemove(args[1:], stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "remote presets: unknown command %q\n", args[0])
		_, _ = fmt.Fprint(stderr, presetsUsage)
		return 2
	}
}

// requireRegisteredAgent enforces presets.go's own stated principle at the agent
// field too (round-2 fix-pack, MAJOR 3): a preset naming a provider this build
// cannot launch is a setup error surfaced at setup time -- naming the typo and the
// registered choices -- not at the phone's confirm as a generic policy code.
func requireRegisteredAgent(agent string, stderr io.Writer) bool {
	if registry.IsProduction(agent) {
		return true
	}
	production := make([]string, 0)
	for _, name := range registry.Names() {
		if registry.IsProduction(name) {
			production = append(production, name)
		}
	}
	_, _ = fmt.Fprintf(stderr, "remote presets: %q is not a registered provider; choose one of: %s\n",
		agent, strings.Join(production, ", "))
	return false
}

// presetOptions collects the repeatable `--option key=value` entries (round 3,
// review MEDIUM: without an authoring flag, LaunchPreset.Options was ALWAYS nil in
// production custody -- a documented wire fact nothing could author, leaving the
// preset-path R-POL.4 denylist unreachable). Each entry must be key=value with a
// non-empty key; `worktree` refuses pointing at the dedicated flag, so one policy
// bit has exactly one authoring spelling. A malformed entry refuses at authoring
// time via flag.Parse, naming it -- the file's own setup-time principle.
type presetOptions struct {
	set map[string]string
}

func (o *presetOptions) String() string { return "" }

func (o *presetOptions) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok || k == "" {
		return fmt.Errorf("an option is authored as key=value")
	}
	if k == "worktree" {
		return fmt.Errorf("worktree isolation is authored by the dedicated --worktree flag")
	}
	if o.set == nil {
		o.set = make(map[string]string)
	}
	o.set[k] = val
	return nil
}

// presetsStateDir resolves the daemon state dir the way every other remote verb does
// (SWARM_DAEMON_STATE env, falling back to persist.DefaultDir).
func presetsStateDir() (string, error) {
	if dir := os.Getenv(daemon.EnvStateDir); dir != "" {
		return dir, nil
	}
	return persist.DefaultDir()
}

// newPresetID mints the stable opaque id the phone selects by.
func newPresetID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate preset id: %w", err)
	}
	return "preset-" + hex.EncodeToString(b[:]), nil
}

// runRemotePresetsAdd authors one preset. A nonexistent root refuses at authoring
// time, naming the path, and authors nothing: a preset nobody can launch is a setup
// error surfaced at setup time, not at the phone's confirm.
func runRemotePresetsAdd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("remote presets add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "display name the phone's preset list renders")
	agent := fs.String("agent", "", "provider (adapter name, e.g. claude)")
	root := fs.String("root", "", "allowed workspace/worktree root directory")
	worktree := fs.Bool("worktree", false, "launch into an isolated worktree by default")
	var options presetOptions
	fs.Var(&options, "option", "allowlisted launch option as key=value (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *name == "" || *agent == "" || *root == "" {
		_, _ = fmt.Fprintln(stderr, "remote presets add: --name, --agent and --root are all required")
		return 2
	}
	if !requireRegisteredAgent(*agent, stderr) {
		return 1
	}
	resolved, err := filepath.EvalSymlinks(*root)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "remote presets add: root %s does not resolve to an existing directory: %v\n", *root, err)
		return 1
	}
	if fi, err := os.Stat(resolved); err != nil || !fi.IsDir() {
		_, _ = fmt.Fprintf(stderr, "remote presets add: root %s is not a directory\n", *root)
		return 1
	}

	stateDir, err := presetsStateDir()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "remote presets add: %v\n", err)
		return 1
	}
	presets, err := daemon.LoadLaunchPresets(stateDir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "remote presets add: %v\n", err)
		return 1
	}
	id, err := newPresetID()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "remote presets add: %v\n", err)
		return 1
	}
	p := daemon.LaunchPreset{
		ID:          id,
		DisplayName: *name,
		Agent:       *agent,
		Root:        resolved,
		Options:     options.set,
		Worktree:    *worktree,
	}
	if err := daemon.SaveLaunchPresets(stateDir, append(presets, p)); err != nil {
		_, _ = fmt.Fprintf(stderr, "remote presets add: %v\n", err)
		return 1
	}
	// The revision is the staleness coordinate the phone will echo; surfacing it lets
	// an operator correlate a stale_preset refusal with the edit that caused it.
	_, _ = fmt.Fprintf(stdout, "authored %s (%s)\n  agent:    %s\n  root:     %s\n  revision: %s\n",
		id, *name, *agent, resolved, daemon.PresetRevision(p))
	return 0
}

// runRemotePresetsList prints every authored preset, with an EXPLICIT empty state:
// zero presets says so (exit 0) -- an answer, not an error and not silence.
func runRemotePresetsList(stdout, stderr io.Writer) int {
	stateDir, err := presetsStateDir()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "remote presets list: %v\n", err)
		return 1
	}
	presets, err := daemon.LoadLaunchPresets(stateDir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "remote presets list: %v\n", err)
		return 1
	}
	if len(presets) == 0 {
		_, _ = fmt.Fprintln(stdout, "no presets authored on this machine; add one with `swarm remote presets add`")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 2, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tNAME\tAGENT\tROOT\tWORKTREE\tREVISION")
	for _, p := range presets {
		wt := "no"
		if p.Worktree {
			wt = "yes"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			p.ID, p.DisplayName, p.Agent, p.Root, wt, daemon.PresetRevision(p))
	}
	_ = tw.Flush()
	return 0
}

// runRemotePresetsEdit re-authors one preset IN PLACE (round-2 fix-pack, MAJOR 3):
// same id -- the phone selects by it -- new content, and therefore a new revision.
// This is the operator path behind stale_preset: a phone that confirmed the OLD
// revision refuses instead of silently launching the new policy. Only the flags the
// operator passed change; a flag left off keeps the authored value.
func runRemotePresetsEdit(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		_, _ = fmt.Fprintln(stderr, "remote presets edit: the preset id comes first (see `swarm remote presets list`)")
		return 2
	}
	id := args[0]
	fs := flag.NewFlagSet("remote presets edit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "new display name")
	agent := fs.String("agent", "", "new provider (adapter name)")
	root := fs.String("root", "", "new workspace/worktree root directory")
	worktree := fs.String("worktree", "", "worktree isolation default: true or false")
	var options presetOptions
	fs.Var(&options, "option", "allowlisted launch option as key=value (repeatable; REPLACES the authored set)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	stateDir, err := presetsStateDir()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "remote presets edit: %v\n", err)
		return 1
	}
	presets, err := daemon.LoadLaunchPresets(stateDir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "remote presets edit: %v\n", err)
		return 1
	}
	idx := -1
	for i, p := range presets {
		if p.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		_, _ = fmt.Fprintf(stderr, "remote presets edit: preset %s is not authored on this machine\n", id)
		return 1
	}
	p := presets[idx]
	if *name != "" {
		p.DisplayName = *name
	}
	if *agent != "" {
		if !requireRegisteredAgent(*agent, stderr) {
			return 1
		}
		p.Agent = *agent
	}
	if *root != "" {
		resolved, err := filepath.EvalSymlinks(*root)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "remote presets edit: root %s does not resolve to an existing directory: %v\n", *root, err)
			return 1
		}
		if fi, err := os.Stat(resolved); err != nil || !fi.IsDir() {
			_, _ = fmt.Fprintf(stderr, "remote presets edit: root %s is not a directory\n", *root)
			return 1
		}
		p.Root = resolved
	}
	switch *worktree {
	case "":
	case "true":
		p.Worktree = true
	case "false":
		p.Worktree = false
	default:
		_, _ = fmt.Fprintf(stderr, "remote presets edit: --worktree takes true or false, got %q\n", *worktree)
		return 2
	}
	if options.set != nil {
		// REPLACE, never merge: a merge could never remove an authored entry, so a
		// policy option once authored would be un-withdrawable.
		p.Options = options.set
	}
	presets[idx] = p
	if err := daemon.SaveLaunchPresets(stateDir, presets); err != nil {
		_, _ = fmt.Fprintf(stderr, "remote presets edit: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "re-authored %s (%s)\n  agent:    %s\n  root:     %s\n  revision: %s\n",
		p.ID, p.DisplayName, p.Agent, p.Root, daemon.PresetRevision(p))
	return 0
}

// runRemotePresetsRemove withdraws one preset (round-2 fix-pack, MAJOR 3): the id
// stops being authored, so a phone that still names it refuses unknown_preset --
// the operator path behind that stable code. An id this machine never authored
// refuses, naming it: a remove that "succeeds" on nothing would tell the operator a
// live preset was withdrawn.
func runRemotePresetsRemove(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 || strings.HasPrefix(args[0], "-") {
		_, _ = fmt.Fprintln(stderr, "remote presets remove: exactly one preset id (see `swarm remote presets list`)")
		return 2
	}
	id := args[0]
	stateDir, err := presetsStateDir()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "remote presets remove: %v\n", err)
		return 1
	}
	presets, err := daemon.LoadLaunchPresets(stateDir)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "remote presets remove: %v\n", err)
		return 1
	}
	kept := presets[:0]
	found := false
	for _, p := range presets {
		if p.ID == id {
			found = true
			continue
		}
		kept = append(kept, p)
	}
	if !found {
		_, _ = fmt.Fprintf(stderr, "remote presets remove: preset %s is not authored on this machine\n", id)
		return 1
	}
	if err := daemon.SaveLaunchPresets(stateDir, kept); err != nil {
		_, _ = fmt.Fprintf(stderr, "remote presets remove: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "removed %s; a phone that still names it will be refused unknown_preset\n", id)
	return 0
}
