package main

// `swarm agents install` — the agent-initiated half of ADR-010's "two triggers, one code
// path" (D3): it writes the `/swarm-handoff` and `/swarm-delegate` slash-command documents
// into each target CLI's user-global convention from ONE embedded template source, so the
// prompt content is maintained once instead of per CLI. The two documents differ only in
// intent wording and the spawn flag they name (D2).
//
// The verb touches no daemon socket — it writes local files — so main.go dispatches it
// directly rather than through dispatchAgentVerb.

import (
	_ "embed"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

//go:embed templates/swarm-command.md.tmpl
var slashCommandTemplate string

// agentsInstallHome resolves the user's home directory the command files are written
// under. It is a package-level indirection (the spawnStateDir precedent) so a test can
// point the install at a temp dir and never touch the real $HOME.
var agentsInstallHome = func() (string, error) { return os.UserHomeDir() }

const agentsUsage = `usage: swarm agents install [--dry-run]

  install   write the /swarm-handoff and /swarm-delegate command files into every
            target CLI's user-global convention
            (--dry-run: print the paths it would write and touch nothing)
`

// commandVariant is one rendered document. The two variants differ ONLY in intent wording
// and the spawn command line they name (D2/D3); everything else comes from the shared
// template.
type commandVariant struct {
	Slug       string // the file's basename stem
	Invocation string // how THIS CLI invokes the command; filled per target at render
	Intent     string // the recorded intent, in prose
	SpawnLine  string
	SpawnNote  string
}

// commandVariants is the frozen pair of documents every known CLI receives.
func commandVariants() []commandVariant {
	return []commandVariant{
		{
			Slug:      "handoff",
			Intent:    "handoff document",
			SpawnLine: "swarm spawn --cli <target-cli> --handoff <handoff-file>",
			SpawnNote: "The new session starts in this session's working directory.",
		},
		{
			Slug:      "delegate",
			Intent:    "delegation document",
			SpawnLine: "swarm spawn --cli <target-cli> --delegate <handoff-file> --worktree",
			SpawnNote: "`--worktree` gives the new session its own git worktree — the delegate default,\nso two sessions never contend for one checkout.",
		},
	}
}

// installTarget is one CLI's user-global command directory, relative to home. A CLI with
// no relDir has no documented command/prompt-file convention (checked against
// docs/research/inter-session-orchestration-landscape.md and the adapter packages) and is
// SKIPPED and reported — never guessed at, which would litter $HOME with files no CLI reads.
type installTarget struct {
	// invoke renders the CLI's invocation form for a slug: claude command files
	// are /swarm-<slug>, codex prompt files /prompts:swarm-<slug>
	// (docs/research/inter-session-orchestration-landscape.md).
	invoke func(slug string) string
	cli    string
	relDir string // "" = no known convention
}

func installTargets() []installTarget {
	return []installTarget{
		{cli: "claude", relDir: filepath.Join(".claude", "commands"), invoke: func(slug string) string { return "/swarm-" + slug }},
		{cli: "codex", relDir: filepath.Join(".codex", "prompts"), invoke: func(slug string) string { return "/prompts:swarm-" + slug }},
		{cli: "agy"},
		{cli: "opencode"},
	}
}

// runAgents dispatches `swarm agents <verb>`. Only "install" exists; anything else prints
// the agents-specific usage and is misuse (exit 2).
func runAgents(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "install" {
		_, _ = fmt.Fprint(stderr, agentsUsage)
		return misuseExit
	}
	return runAgentsInstall(args[1:], stdout, stderr)
}

// runAgentsInstall is `swarm agents install [--dry-run]`. It always REGENERATES the two
// documents per known CLI (they are generated content) and never touches anything else in
// the same directory. --dry-run decides before it touches the filesystem at all — not even
// the target directories — so an agent can inspect the plan before it lands in $HOME.
// Argument refusals exit 2; a home-resolution or write failure exits 1, naming the failure.
func runAgentsInstall(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agents install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dryRun := fs.Bool("dry-run", false, "print the files that would be written and touch nothing")
	if err := fs.Parse(args); err != nil {
		return misuseExit
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintf(stderr, "agents install: unexpected argument %q\n", fs.Arg(0))
		return misuseExit
	}

	home, err := agentsInstallHome()
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "agents install: %v\n", err)
		return 1
	}
	for _, t := range installTargets() {
		if t.relDir == "" {
			_, _ = fmt.Fprintf(stdout, "%s: skipped: no known command convention\n", t.cli)
			continue
		}
		dir := filepath.Join(home, t.relDir)
		docs, err := renderCommandDocs(t)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "agents install: %v\n", err)
			return 1
		}
		for _, v := range commandVariants() {
			path := filepath.Join(dir, "swarm-"+v.Slug+".md")
			if *dryRun {
				_, _ = fmt.Fprintf(stdout, "would write %s\n", path)
				continue
			}
			if err := writeCommandFile(dir, path, docs[v.Slug]); err != nil {
				_, _ = fmt.Fprintf(stderr, "agents install: %v\n", err)
				return 1
			}
			_, _ = fmt.Fprintf(stdout, "wrote %s\n", path)
		}
	}
	return 0
}

// renderCommandDocs renders the one embedded template once per variant for one CLI,
// keyed by slug — per-CLI because the invocation form in the title differs.
func renderCommandDocs(t installTarget) (map[string]string, error) {
	tmpl, err := template.New("swarm-command").Parse(slashCommandTemplate)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(commandVariants()))
	for _, v := range commandVariants() {
		v.Invocation = t.invoke(v.Slug)
		var buf strings.Builder
		if err := tmpl.Execute(&buf, v); err != nil {
			return nil, err
		}
		out[v.Slug] = buf.String()
	}
	return out, nil
}

// writeCommandFile creates the target directory if needed and writes (replacing) exactly
// the one generated file. MkdirAll leaves an existing directory's mode untouched, so a
// CLI's own command directory keeps whatever permissions it already had.
func writeCommandFile(dir, path, body string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}
