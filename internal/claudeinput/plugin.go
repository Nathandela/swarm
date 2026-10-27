package claudeinput

import (
	_ "embed"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

//go:embed plugin.js
var pluginSource string

//go:embed guard.js
var guardSource string

//go:embed control_guard.js
var controlGuardSource string

// InstallNativeControl installs the independent observer, with optional sender.
func InstallNativeControl(dir string, sender bool) (string, error) {
	return install(dir, sender, true)
}

// Install writes only session-owned, per-invocation plugin artifacts.
func Install(dir string) (string, error) {
	return install(dir, true, false)
}

func install(dir string, sender, control bool) (string, error) {
	config, _ := json.Marshal(filepath.Join(dir, DescriptorFile))
	guard := guardSource
	if control {
		guard = controlGuardSource
	}
	sources := map[string]string{"claude-input-guard-plugin": guard}
	if sender {
		sources["claude-input-plugin"] = pluginSource
	}
	for name, source := range sources {
		root := filepath.Join(dir, name)
		for _, path := range []string{filepath.Join(root, ".claude-plugin"), filepath.Join(root, "hooks"), filepath.Join(root, "types")} {
			if err := os.MkdirAll(path, 0o700); err != nil {
				return "", err
			}
		}
		pluginName := "swarm-native-input"
		if name == "claude-input-guard-plugin" {
			pluginName += "-guard"
		}
		manifest, _ := json.Marshal(map[string]string{"name": pluginName, "version": "1.1.0", "description": "Swarm native message ingress", "types": "./types/index.d.ts"})
		types := "export {};\n"
		if control && name == "claude-input-guard-plugin" {
			types = "export type NativeControl = never;\n"
		}
		files := map[string]string{
			".claude-plugin/plugin.json": string(manifest),
			"hooks/hooks.json":           `{"modules":["./input.js"]}`,
			"types/index.d.ts":           types,
			"hooks/input.js":             strings.ReplaceAll(source, "__CONFIG_PATH__", string(config)),
		}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
				return "", err
			}
		}
	}
	if sender {
		return filepath.Join(dir, "claude-input-plugin"), nil
	}
	return GuardPath(dir), nil
}

// GuardPath is the mandatory companion mod installed beside the sender.
func GuardPath(dir string) string { return filepath.Join(dir, "claude-input-guard-plugin") }

func ReadDescriptor(dir string) (Descriptor, error) {
	var desc Descriptor
	raw, err := os.ReadFile(filepath.Join(dir, DescriptorFile))
	if err != nil {
		return desc, err
	}
	if len(raw) > 16<<10 {
		return desc, os.ErrInvalid
	}
	if err = json.Unmarshal(raw, &desc); err != nil {
		return desc, err
	}
	if !desc.LifecycleFence || !SupportedVersion(desc.ProviderVersion) || len(desc.Token) != 64 || len(desc.Generation) != 32 || desc.ShimPID <= 0 || desc.ShimStartTime <= 0 || desc.SocketPath != filepath.Join(dir, "claude-input.sock") {
		return desc, os.ErrInvalid
	}
	if desc.RequiredRuntime != "" && (desc.RequiredRuntime != NativeControlRuntime || desc.ProviderVersion != "2.1.310") || desc.RequiredRuntime == "" && desc.SenderEnabled {
		return desc, os.ErrInvalid
	}
	return desc, nil
}
