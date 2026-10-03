package accountconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// ValidateAvailabilityCheck gates the fixed Claude 2.1.288 availability command:
// safe-mode, empty setting-sources/settings/tools/MCP, disabled slash commands,
// no session persistence, one turn, and an exact model without fallback. The
// caller must verify personal native OAuth identity/version and run that exact
// command in this private empty scratch directory. No credential file is read.
// Safe mode retains managed hooks; any managed or uncharacterized source refuses.
func ValidateAvailabilityCheck(stateRoot, profile, cwd string, env []string) error {
	if runtime.GOOS != "linux" {
		return Conflict("availability-managed-platform-not-characterized")
	}
	return validateAvailabilityCheck(stateRoot, profile, cwd, env, "/etc/claude-code")
}

func validateAvailabilityCheck(stateRoot, profile, cwd string, env []string, policyDirectory string) error {
	for _, path := range []string{stateRoot, profile, cwd} {
		if err := privateDir(path); err != nil {
			return Conflict("availability-unsafe-private-context")
		}
	}
	rel, err := filepath.Rel(stateRoot, profile)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return Conflict("availability-profile-outside-account-store")
	}
	entries, err := os.ReadDir(cwd)
	if err != nil || len(entries) != 0 {
		return Conflict("availability-scratch-directory-not-empty")
	}
	selected := false
	seen := map[string]bool{}
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || seen[key] {
			return Conflict("availability-invalid-environment")
		}
		seen[key] = true
		if value == "" {
			continue
		}
		switch key {
		case "CLAUDE_CONFIG_DIR":
			if value != profile {
				return Conflict("availability-alternate-profile")
			}
			selected = true
		case "CLAUDE_SECURESTORAGE_CONFIG_DIR":
			if value != profile {
				return Conflict("availability-alternate-profile")
			}
		case "CLAUDE_CODE_MAX_RETRIES", "CLAUDE_CODE_NONSTREAMING_TIMEOUT_RETRIES":
			if value != "0" {
				return Conflict("availability-unbounded-retry")
			}
		case "CLAUDE_CODE_MAX_OUTPUT_TOKENS":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > 16 {
				return Conflict("availability-unbounded-output")
			}
		case "NODE_OPTIONS", "NODE_PATH", "BUN_OPTIONS", "LD_PRELOAD", "LD_LIBRARY_PATH":
			return Conflict("availability-runtime-injection")
		default:
			if strings.HasPrefix(key, "CLAUDE_") || strings.HasPrefix(key, "ANTHROPIC_") {
				return Conflict("availability-alternate-native-control")
			}
		}
	}
	if !selected {
		return Conflict("availability-missing-selected-profile")
	}
	var inventory manifest
	if err := collectClaudeProfileSources(&inventory, env); err != nil {
		return err
	}
	if err := collectClaudePolicies(&inventory, policyDirectory, "availability-managed-policy-not-characterized"); err != nil {
		return err
	}
	if err := collectProjectSources(&inventory, "claude", cwd); err != nil {
		return err
	}
	if _, err := readClaudeSettings(&inventory, filepath.Join(profile, "settings.json"), false); err != nil {
		return err
	}
	if err := collectClaudeProfilePolicies(&inventory, profile, "availability-native-customization-not-characterized"); err != nil {
		return err
	}
	for _, name := range append(cohortNames("claude"), "settings.local.json") {
		if err := absentSource(&inventory, filepath.Join(profile, name), "availability-native-customization-not-characterized"); err != nil {
			return err
		}
	}
	entries, err = os.ReadDir(profile)
	if err != nil {
		return Conflict("availability-unsafe-private-context")
	}
	for _, entry := range entries {
		switch entry.Name() {
		case ".credentials.json", ".claude.json", ".swarm-candidate.json", "settings.json":
		case "projects":
			if err := validateClaudeRuntimeHistory(filepath.Join(profile, entry.Name())); err != nil {
				return err
			}
		case "backups":
			if err := validateClaudeRuntimeBackups(profile); err != nil {
				return err
			}
		default:
			return Conflict("availability-unknown-profile-source")
		}
	}
	digest, err := claudeTrustAndMCPAt(profile, cwd, "")
	empty := sha256.Sum256([]byte("{}"))
	if err != nil || digest != hex.EncodeToString(empty[:]) {
		return Conflict("availability-native-mcp-or-trust-not-characterized")
	}
	return validateSources(inventory.Sources)
}

// Claude 2.1.288 creates up to five timestamped .claude.json backups when
// saving global metadata. Its Kc/Qc readers only suggest manual restoration;
// they never load backup content. Native mkdir may produce a 0775 backups
// directory, which remains confined by the already private 0700 profile.
func validateClaudeRuntimeBackups(profile string) error {
	if err := safePath(profile, true); err != nil {
		return Conflict("availability-unsafe-runtime-artifact")
	}
	if err := privateDir(profile); err != nil {
		return Conflict("availability-unsafe-runtime-artifact")
	}
	anchor, err := os.OpenRoot(profile)
	if err != nil {
		return Conflict("availability-unsafe-runtime-artifact")
	}
	defer func() { _ = anchor.Close() }()
	before, err := anchor.Lstat("backups")
	if err != nil || !before.IsDir() || before.Mode().Perm()&0o002 != 0 {
		return Conflict("availability-unsafe-runtime-artifact")
	}
	owner, ok := before.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Getuid()) {
		return Conflict("availability-unsafe-runtime-artifact")
	}
	backups, err := anchor.OpenRoot("backups")
	if err != nil {
		return Conflict("availability-unsafe-runtime-artifact")
	}
	defer func() { _ = backups.Close() }()
	dir, err := backups.Open(".")
	if err != nil {
		return Conflict("availability-unsafe-runtime-artifact")
	}
	defer func() { _ = dir.Close() }()
	after, err := dir.Stat()
	if err != nil || !os.SameFile(before, after) {
		return Conflict("availability-unsafe-runtime-artifact")
	}
	entries, err := dir.ReadDir(6)
	if err != nil && !errors.Is(err, io.EOF) {
		return Conflict("availability-unsafe-runtime-artifact")
	}
	if len(entries) > 5 {
		return Conflict("availability-runtime-inventory-too-large")
	}
	for _, entry := range entries {
		stamp, recognized := strings.CutPrefix(entry.Name(), ".claude.json.backup.")
		if !recognized || len(stamp) < 10 || len(stamp) > 16 || strings.Trim(stamp, "0123456789") != "" {
			return Conflict("availability-unknown-runtime-artifact")
		}
		info, err := backups.Lstat(entry.Name())
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o177 != 0 {
			return Conflict("availability-unsafe-runtime-artifact")
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != uint32(os.Getuid()) || owner.Nlink != 1 {
			return Conflict("availability-unsafe-runtime-artifact")
		}
		if info.Size() > maxSourceBytes {
			return Conflict("availability-runtime-inventory-too-large")
		}
	}
	return privateDir(profile)
}

// The fixed fresh --print check never resumes or discovers instructions from
// native projects history. Permit its characterized transcript/index data,
// without opening content or copying a rotating credential into another profile.
func validateClaudeRuntimeHistory(path string) error {
	if err := safePath(path, true); err != nil {
		return Conflict("availability-unsafe-runtime-artifact")
	}
	count := 0
	var total int64
	return filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.Type()&os.ModeSymlink != 0 {
			return Conflict("availability-unsafe-runtime-artifact")
		}
		count++
		rel, err := filepath.Rel(path, current)
		if err != nil || count > 4096 || strings.Count(rel, string(filepath.Separator)) > 8 {
			return Conflict("availability-runtime-inventory-too-large")
		}
		info, err := entry.Info()
		if err != nil || info.Mode().Perm()&0o022 != 0 {
			return Conflict("availability-unsafe-runtime-artifact")
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != uint32(os.Getuid()) {
			return Conflict("availability-unsafe-runtime-artifact")
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 != 0 || filepath.Ext(entry.Name()) != ".jsonl" && entry.Name() != "sessions-index.json" {
			return Conflict("availability-unknown-runtime-artifact")
		}
		total += info.Size()
		if info.Size() > 64<<20 || total > 512<<20 {
			return Conflict("availability-runtime-inventory-too-large")
		}
		return nil
	})
}
