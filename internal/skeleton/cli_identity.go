package skeleton

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/registry"
	"github.com/Nathandela/swarm/internal/persist"
)

const cliProbeTimeout = 3 * time.Second

type cliVersionProber struct {
	path, cwd string
	env       []string
	output    *string
}

func (p cliVersionProber) LookPath(string) (string, error) { return p.path, nil }

// cappedCLIOutput bounds output even from a broken launcher.
type cappedCLIOutput struct{ data []byte }

func (b *cappedCLIOutput) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := 16*1024 - len(b.data); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}
func (p cliVersionProber) Run(path string, args []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cliProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env, cmd.Dir = p.env, p.cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 100 * time.Millisecond
	var out cappedCLIOutput
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if p.output != nil {
		*p.output = string(out.data)
	}
	return string(out.data), err
}

// probeCLIIdentity observes a stable installation using the session's environment.
// An empty executable resolves the provider again on that environment's PATH.
func probeCLIIdentity(agentType, executable string, env []string, cwd string) (*persist.CLIIdentity, error) {
	ad, ok := registry.New(agentType)
	if !ok {
		return nil, fmt.Errorf("unknown CLI provider %q", agentType)
	}
	if executable == "" {
		executable = ad.Binary()
	}
	path, err := lookPathIn(executable, env)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("CLI path must be absolute")
	}
	before, err := persist.CLIFingerprint(path)
	if err != nil {
		return nil, err
	}
	var output string
	detection := adapter.Detect(ad, cliVersionProber{path: path, cwd: cwd, env: env, output: &output})
	if !detection.Found || detection.Version == "" || !detection.InRange {
		return nil, fmt.Errorf("CLI version unavailable")
	}
	// Adapter parsers accept a dotted substring. Automation requires the entire
	// version token: never mistake a prerelease/build suffix for a stable release.
	foundToken := false
	for _, token := range strings.Fields(output) {
		if strings.Contains(token, detection.Version) {
			if token != detection.Version {
				return nil, fmt.Errorf("ambiguous CLI release version")
			}
			foundToken = true
		}
	}
	if !foundToken {
		return nil, fmt.Errorf("CLI release version unavailable")
	}
	after, err := persist.CLIFingerprint(path)
	if err != nil || before != after {
		return nil, fmt.Errorf("CLI installation changed during version probe")
	}
	return &persist.CLIIdentity{Path: path, Version: detection.Version, Fingerprint: after}, nil
}

// cliIdentityNewer accepts only unambiguous numeric releases. A rebuild, downgrade,
// prerelease or unparseable version must not cause an automatic replacement.
func cliIdentityNewer(candidate, baseline *persist.CLIIdentity) bool {
	if candidate == nil || baseline == nil {
		return false
	}
	parse := func(v string) ([]uint64, bool) {
		parts := strings.Split(v, ".")
		if len(parts) != 3 {
			return nil, false
		}
		numbers := make([]uint64, 3)
		for i, s := range parts {
			if s == "" {
				return nil, false
			}
			for _, r := range s {
				if r < '0' || r > '9' {
					return nil, false
				}
			}
			n, err := strconv.ParseUint(s, 10, 64)
			if err != nil {
				return nil, false
			}
			numbers[i] = n
		}
		return numbers, true
	}
	next, ok := parse(candidate.Version)
	if !ok {
		return false
	}
	prev, ok := parse(baseline.Version)
	if !ok {
		return false
	}
	for i := range next {
		if next[i] != prev[i] {
			return next[i] > prev[i]
		}
	}
	return false
}
