package accounts

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// NativeInventorySchemaVersion describes only the supported owner-local Linux
// writers below. It is not a promise about a future native version or ambient
// keyrings, remote handoff credentials, user tools, or transcript contents.
const NativeInventorySchemaVersion = 1

func nativeErasureContract(provider string, g Generation, version string) (string, error) {
	if runtime.GOOS != "linux" || g.Kind != KindNative || g.Source != SourceNativeLogin || !validVerification(provider, g.Kind, g.Identity, g.Verification) {
		return "", ErrIneligible
	}
	if (provider == ProviderCodex && version == "0.160.0") || (provider == ProviderClaude && (version == "2.1.288" || version == "2.1.289")) {
		return provider + ":" + version + ":linux-file-v" + strconv.Itoa(NativeInventorySchemaVersion), nil
	}
	return "", ErrIneligible
}

type nativeInventory struct {
	remove        []string
	config        []byte
	configPresent bool
}

// eraseNativeInventory never accepts a caller-supplied filename. Codex's pinned
// file auth writer truncates auth.json directly; its MCP .credentials.json
// fallback and shared keyring routes have no supported managed routing proof,
// so their presence refuses erasure. Claude's Linux plaintext credential writer
// uses .credentials.json.tmp.<8 lower hex>; whole-config backups can also contain
// primaryApiKey. Only that key is removed from the retained live configuration.
func (s *Store) eraseNativeInventory(provider string, g Generation) error {
	version := "0.160.0"
	if provider == ProviderClaude {
		version = "2.1.288"
		if strings.Contains(g.ErasureInventory, ":2.1.289:") {
			version = "2.1.289"
		}
	}
	contract, err := nativeErasureContract(provider, g, version)
	if err != nil || contract != g.ErasureInventory {
		return ErrIneligible
	}
	base := filepath.Join("profiles", g.ProfileGeneration)
	profile, err := openPrivateDir(s.root, base)
	if err != nil {
		return err
	}
	defer func() { _ = profile.Close() }()
	inventory, err := inspectNativeInventory(profile, provider)
	if err != nil {
		return err
	}
	// Validate the entire inventory before the first deletion. Crash retries also
	// include our own private config replacement staging inode.
	if inventory.configPresent {
		if _, err := durableReplace(s.root, filepath.Join(base, ".claude.json"), inventory.config, s.writeOps); err != nil {
			return err
		}
	}
	for _, path := range inventory.remove {
		if err := profile.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if _, err := profile.Lstat("backups"); err == nil {
		backups, err := openNativeBackups(profile)
		if err != nil {
			return err
		}
		err = syncNativeDirectory(backups)
		_ = backups.Close()
		if err != nil {
			return ErrDurabilityUncertain
		}
	}
	if err := syncNativeDirectory(profile); err != nil {
		return ErrDurabilityUncertain
	}
	return nil
}

func nativeEntries(root *os.Root, limit int) ([]os.DirEntry, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, ErrUnsafePath
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(limit + 1)
	if (err != nil && !errors.Is(err, io.EOF)) || len(entries) > limit {
		return nil, ErrIneligible
	}
	return entries, nil
}

func inspectNativeInventory(profile *os.Root, provider string) (nativeInventory, error) {
	var result nativeInventory
	if nativeContextUpdatePending(profile, provider) {
		return result, ErrIneligible
	}
	if provider == ProviderCodex && !validCodexStockInventory(profile) {
		return result, ErrIneligible
	}
	entries, err := nativeEntries(profile, 256)
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		name := entry.Name()
		info, err := profile.Lstat(name)
		if err != nil {
			return result, ErrUnsafePath
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if provider == ProviderCodex && name == "sessions" && validCodexHistoryInventory(profile) {
				continue
			}
			if validNativeContextAssetAlias(profile, provider, name) {
				continue
			}
			return result, ErrUnsafePath
		}
		if provider == ProviderCodex {
			switch name {
			case "auth.json":
				if !privateInfo(info, false) || info.Size() > maxCredentialBytes {
					return result, ErrUnsafePath
				}
				result.remove = append(result.remove, name)
			case ".tmp":
				if !validCodexMarketplaceInventory(profile) {
					return result, ErrUnsafePath
				}
			case ".credentials.json", "secrets", "keyring", "auth.json.bak":
				return result, ErrIneligible
			case "config.toml":
				raw, err := readPrivate(profile, name, maxCredentialBytes)
				if err != nil || (!codexInventoryPolicy(raw) && !validCodexContextConfiguration(profile, raw)) {
					return result, ErrIneligible
				}
			default:
				if strings.Contains(strings.ToLower(name), "credential") || strings.HasPrefix(name, "auth.json.") {
					return result, ErrIneligible
				}
			}
			continue
		}
		switch {
		case name == ".credentials.json", nativeStaging(name, ".credentials.json"), nativeStaging(name, ".claude.json"), nativeConfigStaging(name), accountStaging(name), (strings.HasPrefix(name, ".claude.json.corrupted.") && nativeConfigCopy(name)):
			if !privateInfo(info, false) || info.Mode().Perm()&0o100 != 0 || info.Size() > maxCredentialBytes {
				return result, ErrUnsafePath
			}
			result.remove = append(result.remove, name)
		case name == "projects":
			if !info.IsDir() || !validClaudeHistoryAliases(profile) {
				return result, ErrUnsafePath
			}
		case name == ".config.json":
			return result, ErrIneligible
		case name == ".claude.json":
			raw, err := readPrivate(profile, name, maxCredentialBytes)
			if err != nil {
				return result, err
			}
			result.config, err = sanitizedClaudeConfig(raw)
			if err != nil {
				return result, err
			}
			result.configPresent = true
		case name == "backups":
			backups, err := openNativeBackups(profile)
			if err != nil {
				return result, err
			}
			copied, err := nativeEntries(backups, 64)
			if err != nil {
				_ = backups.Close()
				return result, err
			}
			for _, copy := range copied {
				if !nativeConfigCopy(copy.Name()) {
					_ = backups.Close()
					return result, ErrIneligible
				}
				info, err := backups.Lstat(copy.Name())
				if err != nil || !privateInfo(info, false) || info.Mode().Perm()&0o100 != 0 || info.Size() > maxCredentialBytes {
					_ = backups.Close()
					return result, ErrUnsafePath
				}
				result.remove = append(result.remove, filepath.Join("backups", copy.Name()))
			}
			_ = backups.Close()
		default:
			lower := strings.ToLower(name)
			if strings.Contains(lower, "credential") || strings.Contains(lower, "keyring") || strings.Contains(lower, "oauth") || strings.Contains(lower, "auth") || strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.HasPrefix(name, ".claude.json.") || strings.HasPrefix(name, ".account-tmp-") {
				return result, ErrIneligible
			}
		}
	}
	return result, nil
}

func nativeStaging(name, base string) bool {
	suffix, ok := strings.CutPrefix(name, base+".tmp.")
	if !ok || len(suffix) != 8 || suffix != strings.ToLower(suffix) {
		return false
	}
	_, err := hex.DecodeString(suffix)
	return err == nil
}

// The pinned global-config legacy async ck and sync io helpers use the same
// process.pid + randomBytes(6) stage. Credential ee/v5 writes use the 8hex helper.
func nativeConfigStaging(name string) bool {
	suffix, ok := strings.CutPrefix(name, ".claude.json.tmp.")
	if !ok {
		return false
	}
	pid, random, ok := strings.Cut(suffix, ".")
	if !ok || len(pid) > 10 || len(random) != 12 || random != strings.ToLower(random) {
		return false
	}
	number, err := strconv.ParseInt(pid, 10, 32)
	if err != nil || number <= 0 || strconv.FormatInt(number, 10) != pid {
		return false
	}
	_, err = hex.DecodeString(random)
	return err == nil
}

func accountStaging(name string) bool {
	id, ok := strings.CutPrefix(name, ".account-tmp-")
	return ok && validID(id)
}

func nativeConfigCopy(name string) bool {
	suffix, ok := strings.CutPrefix(name, ".claude.json.backup.")
	if !ok {
		suffix, ok = strings.CutPrefix(name, ".claude.json.corrupted.")
	}
	return ok && len(suffix) >= 10 && len(suffix) <= 16 && strings.Trim(suffix, "0123456789") == ""
}

// The private parent confines native's 0775 backup directory. Refuse aliases,
// another owner, or an other-writable directory without rewriting its metadata.
func openNativeBackups(profile *os.Root) (*os.Root, error) {
	before, err := profile.Lstat("backups")
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 || before.Mode().Perm()&0o002 != 0 {
		return nil, ErrUnsafePath
	}
	owner, ok := before.Sys().(*syscall.Stat_t)
	if !ok || owner.Uid != uint32(os.Getuid()) {
		return nil, ErrUnsafePath
	}
	root, err := profile.OpenRoot("backups")
	if err != nil {
		return nil, ErrUnsafePath
	}
	after, err := root.Lstat(".")
	current, currentErr := profile.Lstat("backups")
	if err != nil || currentErr != nil || !os.SameFile(before, after) || !os.SameFile(current, after) {
		_ = root.Close()
		return nil, ErrUnsafePath
	}
	return root, nil
}

func syncNativeDirectory(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}

// Only scalar settings admitted by the current managed compiler are accepted.
// No tables, MCP policies, keyring routing or alternate providers are inferred
// from an absent credential file. Native receives the explicit file override.
func codexInventoryPolicy(raw []byte) bool {
	seen := make(map[string]bool)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || seen[key] {
			return false
		}
		seen[key] = true
		switch key {
		case "model", "model_reasoning_effort", "model_reasoning_summary", "approval_policy", "sandbox_mode", "personality", "cli_auth_credentials_store":
			text, err := strconv.Unquote(value)
			if err != nil || (key == "cli_auth_credentials_store" && text != "file") {
				return false
			}
		case "sandbox_workspace_write.network_access", "sandbox_workspace_write.exclude_tmpdir_env_var", "sandbox_workspace_write.exclude_slash_tmp", "check_for_update_on_startup":
			if value != "true" && value != "false" {
				return false
			}
		case "model_context_window", "model_auto_compact_token_limit":
			if _, err := strconv.ParseUint(value, 10, 64); err != nil {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func sanitizedClaudeConfig(raw []byte) ([]byte, error) {
	// A malformed or duplicate-key config cannot be safely rewritten while
	// preserving project trust. Whole known corrupted backups may be deleted.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	config := make(map[string]any)
	if dec.Decode(&config) != nil || config == nil || dec.Decode(new(any)) != io.EOF || validateJSON(raw) != nil {
		return nil, ErrIneligible
	}
	delete(config, "primaryApiKey")
	// Pinned customApiKeyResponses stores use(key)=trim(key).slice(-20), not
	// hashes. Remove these credential suffix approvals while preserving trust.
	delete(config, "customApiKeyResponses")
	if !safeClaudeCredentialKeys(config) {
		return nil, ErrIneligible
	}
	out, err := json.Marshal(config)
	if err != nil {
		return nil, ErrIneligible
	}
	return append(out, '\n'), nil
}

func safeClaudeCredentialKeys(value any) bool {
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			lower := strings.ToLower(key)
			switch key {
			case "lastTotalInputTokens", "lastTotalOutputTokens", "lastTotalCacheCreationInputTokens", "lastTotalCacheReadInputTokens", "inputTokens", "outputTokens", "cacheCreationInputTokens", "cacheReadInputTokens":
				number, ok := child.(json.Number)
				if !ok {
					return false
				}
				value, err := strconv.ParseFloat(string(number), 64)
				if err != nil || value < 0 {
					return false
				}
				continue
			}
			if strings.Contains(lower, "token") || strings.Contains(lower, "apikey") || strings.Contains(lower, "api_key") || strings.Contains(lower, "credential") || strings.Contains(lower, "secret") {
				return false
			}
			if key == "mcpServers" {
				if servers, ok := child.(map[string]any); !ok || len(servers) != 0 {
					return false
				}
			}
			if !safeClaudeCredentialKeys(child) {
				return false
			}
		}
	case []any:
		for _, child := range item {
			if !safeClaudeCredentialKeys(child) {
				return false
			}
		}
	}
	return true
}
