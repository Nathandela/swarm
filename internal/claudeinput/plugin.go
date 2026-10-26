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

// Install writes only session-owned, per-invocation plugin artifacts.
func Install(dir string) (string, error) {
	config, _ := json.Marshal(filepath.Join(dir, DescriptorFile))
	for name, source := range map[string]string{"claude-input-plugin": pluginSource, "claude-input-guard-plugin": guardSource} {
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
		files := map[string]string{
			".claude-plugin/plugin.json": string(manifest),
			"hooks/hooks.json":           `{"modules":["./input.js"]}`,
			"types/index.d.ts":           "export {};\n",
			"hooks/input.js":             strings.ReplaceAll(source, "__CONFIG_PATH__", string(config)),
		}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
				return "", err
			}
		}
	}
	return filepath.Join(dir, "claude-input-plugin"), nil
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
	return desc, nil
}
