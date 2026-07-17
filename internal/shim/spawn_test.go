package shim

// E4.1 — the shim setsids the agent, execs it directly (never via a shell) from
// an argv array + captured env in the requested cwd, in its own process group.
// Invariants S4 (injection-free spawn) and S-6 (env differential) live here.
//
// All four facets are read from the "info" helper, which prints its argv, cwd,
// pid, pgid, and full environ over the PTY; each test drives it with a tailored
// Config and asserts its own facet from the transcript.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// parseInfo pulls the TAB-delimited "info" helper output out of a transcript
// (PTY output arrives with CR-LF line endings). It returns argv (in order), a
// map of the other single-valued fields, and the environ entries.
func parseInfo(t *testing.T, transcript string) (argv []string, fields map[string]string, env []string) {
	t.Helper()
	fields = map[string]string{}
	if !strings.Contains(transcript, "INFO_DONE") {
		t.Fatalf("info helper did not run to completion; transcript:\n%s", transcript)
	}
	for _, raw := range strings.Split(transcript, "\n") {
		line := strings.TrimRight(raw, "\r")
		key, val, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		switch key {
		case "ARGV":
			argv = append(argv, val)
		case "ENV":
			env = append(env, val)
		default:
			fields[key] = val
		}
	}
	return argv, fields, env
}

func runInfo(t *testing.T, cfg Config) (argv []string, fields map[string]string, env []string) {
	t.Helper()
	ch := runShimAsync(cfg)
	r := waitRun(t, ch, 20*time.Second)
	if r.err != nil {
		t.Fatalf("Run: %v", r.err)
	}
	return parseInfo(t, readTranscript(t, cfg.SessionDir))
}

// E4.1a / S4 — metacharacter-laden argv elements arrive as single, verbatim
// argv entries, and NO shell interprets them (a $(...) substitution would have
// created the sentinel file; it must not exist).
func TestSpawn_ArgvInjectionFree(t *testing.T) {
	pwned := filepath.Join(t.TempDir(), "pwned")
	// The command substitution names an absolute path so a shell, if one were
	// involved, would create it regardless of cwd.
	injections := []string{
		"; rm -rf /tmp/does-not-matter",
		"$(touch " + pwned + ")",
		"`touch " + pwned + "`",
		"&& touch " + pwned,
		"| cat",
		"metacharacters: * ? ~ > < \\ ' \" spaces and\ttabs",
	}
	cfg := helperConfig(t, modeInfo, injections, nil)
	argv, _, _ := runInfo(t, cfg)

	if _, err := os.Stat(pwned); err == nil {
		t.Fatalf("sentinel %s was created — a shell interpreted a spawn field (S4 violated)", pwned)
	}
	// argv[0] is the program; the injections follow verbatim, one element each.
	if len(argv) != 1+len(injections) {
		t.Fatalf("argv has %d elements, want %d:\n%q", len(argv), 1+len(injections), argv)
	}
	for i, want := range injections {
		if got := argv[i+1]; got != want {
			t.Errorf("argv[%d] = %q, want %q (metacharacters must arrive intact, not split or expanded)", i+1, got, want)
		}
	}
}

// E4.1b / S-6 — the agent's environment is exactly cfg.Env (plus a TERM the
// shim injects when absent), and never the shim/test process's inherited env.
// The differential: a marker set in the test process must be absent downstream.
func TestSpawn_EnvIsCapturedNotInherited(t *testing.T) {
	const markerKey = "SWARM_ENV_DIFFERENTIAL_MARKER"
	const markerVal = "must-not-leak"
	t.Setenv(markerKey, markerVal) // present in the TEST process env only

	const sentinelKV = "SWARM_ENV_SENTINEL=carried-through"
	cfg := helperConfig(t, modeInfo, nil, []string{sentinelKV})
	_, _, env := runInfo(t, cfg)

	envSet := map[string]bool{}
	for _, kv := range env {
		envSet[kv] = true
	}
	// Positive: the caller-provided entries pass through verbatim.
	if !envSet[sentinelKV] {
		t.Errorf("agent env missing caller-supplied %q; got:\n%s", sentinelKV, strings.Join(env, "\n"))
	}
	if !envSet[helperEnvVar+"="+modeInfo] {
		t.Errorf("agent env missing the helper gate; got:\n%s", strings.Join(env, "\n"))
	}
	// Differential: the test process's marker must NOT reach the agent.
	for _, kv := range env {
		if strings.HasPrefix(kv, markerKey+"=") {
			t.Errorf("agent env leaked the inherited marker %q — spawn used the inherited env, not cfg.Env (S-6 violated)", kv)
		}
	}
	// TERM injection: absent from cfg.Env, so the shim supplies a sane default.
	if envSet["TERM=xterm-256color"] {
		return
	}
	for _, kv := range env {
		if strings.HasPrefix(kv, "TERM=") {
			t.Errorf("TERM = %q, want xterm-256color injected when cfg.Env carries no TERM", kv)
			return
		}
	}
	t.Errorf("no TERM in agent env; the shim must inject TERM=xterm-256color when absent")
}

// E4.1b (companion) — when cfg.Env DOES carry a TERM, the shim preserves it and
// does not override with its default.
func TestSpawn_EnvTermPreservedWhenProvided(t *testing.T) {
	cfg := helperConfig(t, modeInfo, nil, []string{"TERM=screen-256color"})
	_, _, env := runInfo(t, cfg)
	for _, kv := range env {
		if kv == "TERM=xterm-256color" {
			t.Fatalf("shim overrode a caller-provided TERM with its default")
		}
	}
	found := false
	for _, kv := range env {
		if kv == "TERM=screen-256color" {
			found = true
		}
	}
	if !found {
		t.Errorf("caller-provided TERM=screen-256color not preserved; env:\n%s", strings.Join(env, "\n"))
	}
}

// E4.1c — the agent runs in the requested working directory.
func TestSpawn_Cwd(t *testing.T) {
	cwd := t.TempDir()
	cfg := helperConfig(t, modeInfo, nil, nil)
	cfg.Cwd = cwd
	_, fields, _ := runInfo(t, cfg)

	// Resolve symlinks on both sides: on macOS os.MkdirTemp lives under
	// /var -> /private/var, and the child's Getwd reports the resolved path.
	wantResolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", cwd, err)
	}
	gotResolved, err := filepath.EvalSymlinks(fields["CWD"])
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", fields["CWD"], err)
	}
	if gotResolved != wantResolved {
		t.Errorf("agent cwd = %q, want %q", gotResolved, wantResolved)
	}
}

// E4.1d — the agent is spawned in its OWN process group: its pgid equals its own
// pid (it leads its group, via the shim's setsid/setpgid) and differs from the
// shim/test process's group. This is the precondition for group-kill (S5).
func TestSpawn_OwnProcessGroup(t *testing.T) {
	cfg := helperConfig(t, modeInfo, nil, nil)
	_, fields, _ := runInfo(t, cfg)

	pid := fields["PID"]
	pgid := fields["PGID"]
	if pid == "" || pgid == "" {
		t.Fatalf("missing PID/PGID in info output: %+v", fields)
	}
	if pgid != pid {
		t.Errorf("agent pgid %s != pid %s — agent is not the leader of its own group", pgid, pid)
	}
	if testPgid := strconv.Itoa(syscall.Getpgrp()); pgid == testPgid {
		t.Errorf("agent pgid %s equals the test process group %s — agent was not placed in its own group", pgid, testPgid)
	}
}
