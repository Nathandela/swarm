package upgrade

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Owner classifies who manages the binary at a path. The update transaction
// self-replaces ONLY OwnerSelf: every package manager keeps its own books, and
// a binary rewritten behind those books is the recorded 2026-08-27 brew
// incident and the dpkg/rpm corruption the committee named (C2/C5) -- the exact
// argument that produced brew deference produces deference for every owner.
type Owner string

const (
	OwnerSelf Owner = "self"    // tarball / install-script: ours to replace
	OwnerBrew Owner = "brew"    // Homebrew cask: `brew upgrade --cask swarm` owns it
	OwnerDpkg Owner = "dpkg"    // apt/dpkg package: apt owns it
	OwnerRpm  Owner = "rpm"     // rpm/dnf package: dnf owns it
	OwnerGo   Owner = "go"      // `go install` under GOPATH/bin: the user's toolchain owns it
	OwnerNone Owner = "unknown" // classification failed; treated as NOT self (refuse)
)

// ownerProbeTimeout bounds the dpkg/rpm ownership queries.
const ownerProbeTimeout = 5 * time.Second

// ClassifyOwner answers for the RESOLVED path of the binary actually being
// upgraded -- never for what a package database merely remembers. The recorded
// brew incident is precisely both-present-and-diverged: a hand-copied
// /usr/local/bin/swarm beside a brew record, where `brew list --cask` succeeds
// and says nothing about the binary the daemon runs (committee H4, Sonnet #2).
func ClassifyOwner(binPath string) Owner {
	resolved, err := filepath.EvalSymlinks(binPath)
	if err != nil {
		return OwnerNone
	}

	if runtime.GOOS == "darwin" {
		// The cask links from the Caskroom; the resolved path is inside it (or a
		// Cellar for a formula install). Prefix checks over `brew list`: the path
		// answers for THIS binary.
		for _, marker := range []string{"/Caskroom/", "/Cellar/", "/homebrew/"} {
			if strings.Contains(resolved, marker) {
				return OwnerBrew
			}
		}
	}

	if gobin := goInstallDir(); gobin != "" && filepath.Dir(resolved) == gobin {
		return OwnerGo
	}

	if runtime.GOOS == "linux" {
		if pkgOwned(resolved, "dpkg", "-S") {
			return OwnerDpkg
		}
		if pkgOwned(resolved, "rpm", "-qf") {
			return OwnerRpm
		}
	}

	return OwnerSelf
}

// pkgOwned asks the package database whether it owns path. The tool being
// absent, erroring, or answering "not owned" all mean no; only a clean zero
// exit is a claim of ownership.
func pkgOwned(path, tool, flag string) bool {
	if _, err := exec.LookPath(tool); err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), ownerProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, tool, flag, path)
	cmd.Stdout, cmd.Stderr = nil, nil
	return cmd.Run() == nil
}

// goInstallDir is where `go install` puts binaries: $GOBIN, else $GOPATH/bin,
// else ~/go/bin. Empty when no home resolves.
func goInstallDir() string {
	if b := os.Getenv("GOBIN"); b != "" {
		return filepath.Clean(b)
	}
	if p := os.Getenv("GOPATH"); p != "" {
		return filepath.Join(p, "bin")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "go", "bin")
}
