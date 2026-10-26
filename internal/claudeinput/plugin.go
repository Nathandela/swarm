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

// Install writes only session-owned, per-invocation plugin artifacts.
func Install(dir string) (string, error) {
	root := filepath.Join(dir, "claude-input-plugin")
	for _, path := range []string{filepath.Join(root, ".claude-plugin"), filepath.Join(root, "hooks"), filepath.Join(root, "types")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return "", err
		}
	}
	config, _ := json.Marshal(filepath.Join(dir, DescriptorFile))
	files := map[string]string{
		".claude-plugin/plugin.json": `{"name":"swarm-native-input","version":"1.0.0","description":"Swarm native message ingress","types":"./types/index.d.ts"}`,
		"hooks/hooks.json":           `{"modules":["./input.js"]}`,
		"types/index.d.ts":           "export {};\n",
		"hooks/input.js":             strings.ReplaceAll(pluginSource, "__CONFIG_PATH__", string(config)),
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			return "", err
		}
	}
	return root, nil
}

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
	if !SupportedVersion(desc.ProviderVersion) || len(desc.Token) != 64 || len(desc.Generation) != 32 || desc.ShimPID <= 0 || desc.ShimStartTime <= 0 || desc.SocketPath != filepath.Join(dir, "claude-input.sock") {
		return desc, os.ErrInvalid
	}
	return desc, nil
}
