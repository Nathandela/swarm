package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
)

const claudeChatLaunchConfigMaxBytes = 1 << 20

func readShimLaunchConfigBytes(path, requiredRuntime string) ([]byte, error) {
	if requiredRuntime != persist.RequiredRuntimeClaudeChat && requiredRuntime != persist.RequiredRuntimeClaudeNativeControl {
		return os.ReadFile(path) // Preserve legacy configuration read policy.
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > claudeChatLaunchConfigMaxBytes {
		return nil, fmt.Errorf("claude chat config exceeds read limit or is not regular")
	}
	raw, err := io.ReadAll(io.LimitReader(file, claudeChatLaunchConfigMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > claudeChatLaunchConfigMaxBytes {
		return nil, fmt.Errorf("claude chat config exceeds read limit")
	}
	return raw, nil
}

func decodeShimLaunchConfig(data []byte, requiredRuntime string) (shimLaunchConfig, error) {
	var lc shimLaunchConfig
	if (requiredRuntime == persist.RequiredRuntimeClaudeChat || requiredRuntime == persist.RequiredRuntimeClaudeNativeControl) && len(data) > claudeChatLaunchConfigMaxBytes {
		return lc, fmt.Errorf("claude chat config exceeds read limit")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return lc, err
	}
	chatSelector := false
	for key := range fields {
		if strings.EqualFold(key, "required_runtime") || strings.EqualFold(key, "claude_chat_fixture") {
			chatSelector = true
		}
	}
	if err := json.Unmarshal(data, &lc); err != nil {
		return lc, err
	}
	if requiredRuntime == persist.RequiredRuntimeClaudeNativeControl || lc.RequiredRuntime == persist.RequiredRuntimeClaudeNativeControl {
		if requiredRuntime != persist.RequiredRuntimeClaudeNativeControl || lc.RequiredRuntime != requiredRuntime || len(data) > claudeChatLaunchConfigMaxBytes {
			return lc, fmt.Errorf("claude native control requires matching marker and runtime configuration")
		}
		if err := validateClaudeChatLaunchJSON(data); err != nil {
			return lc, err
		}
		for _, key := range []string{"backend_agent_command_args", "opencode_env", "opencode_initial_prompt"} {
			if value := fields[key]; len(value) != 0 && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return lc, fmt.Errorf("claude native control cannot carry %s", key)
			}
		}
		if err := shim.ValidateClaudeNativeControlConfig(shimConfigFromLaunch(lc)); err != nil {
			return lc, err
		}
		return lc, validateShimRuntime(lc, false)
	}
	if requiredRuntime == persist.RequiredRuntimeClaudeChat || chatSelector {
		if requiredRuntime != persist.RequiredRuntimeClaudeChat || lc.RequiredRuntime != persist.RequiredRuntimeClaudeChat {
			return lc, fmt.Errorf("claude chat requires matching marker and runtime configuration")
		}
		if len(data) > claudeChatLaunchConfigMaxBytes {
			return lc, fmt.Errorf("claude chat config exceeds read limit")
		}
		if err := validateClaudeChatLaunchJSON(data); err != nil {
			return lc, err
		}
		if lc.BackendProgram != "" || lc.BackendSocketPath != "" || lc.BackendCwd != "" || len(lc.BackendArgs) != 0 || len(lc.BackendEnv) != 0 || len(lc.BackendAgentArgs) != 0 {
			return lc, fmt.Errorf("claude chat cannot carry backend configuration")
		}
		// The daemon launch writer has this selector; the legacy cmd projection does not.
		if raw := fields["backend_agent_command_args"]; len(raw) != 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return lc, fmt.Errorf("claude chat cannot carry backend command arguments")
		}
		if raw := fields["opencode_env"]; len(raw) != 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return lc, fmt.Errorf("claude chat cannot carry OpenCode configuration")
		}
		if raw := fields["opencode_initial_prompt"]; len(raw) != 0 {
			return lc, fmt.Errorf("claude chat cannot carry OpenCode prompt")
		}
		if err := shim.ValidateClaudeChatConfig(shimConfigFromLaunch(lc)); err != nil {
			return lc, err
		}
	}
	if requiredRuntime != "" && requiredRuntime != persist.RequiredRuntimeClaudeChat && requiredRuntime != persist.RequiredRuntimeOpenCode {
		return lc, fmt.Errorf("unknown required shim runtime")
	}
	return lc, validateShimRuntime(lc, requiredRuntime == persist.RequiredRuntimeOpenCode)
}

// Same bounded token walk used for private authority JSON; only chat configs use it.
func validateClaudeChatLaunchJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 32 {
			return fmt.Errorf("claude chat config is too deeply nested")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, composite := token.(json.Delim)
		if !composite {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				name, ok := key.(string)
				if err != nil || !ok || seen[name] {
					return fmt.Errorf("ambiguous Claude chat config key")
				}
				seen[name] = true
				canonical := []string{"provider", "epoch", "identity", "home", "external_auth", "path", "version", "fingerprint"}
				if depth == 0 {
					canonical = []string{"required_runtime", "conversation_id", "claude_chat_fixture", "session_id", "argv", "cwd", "env", "session_dir", "socket_path", "hook_socket_path", "hook_drain_token", "login_runtime", "login_state_root", "login_switch_id", "login_switch_source", "native_provider", "cli_identity", "input_embargo", "input_embargo_token", "opencode", "opencode_env", "opencode_initial_prompt", "backend_program", "backend_socket_path", "backend_cwd", "backend_args", "backend_env", "backend_agent_args", "backend_agent_command_args", "hermes_input", "agy_input", "claude_input"}
				}
				for _, exact := range canonical {
					if name != exact && strings.EqualFold(name, exact) {
						return fmt.Errorf("noncanonical Claude chat custody config key")
					}
				}
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return fmt.Errorf("invalid Claude chat config object")
			}
		case '[':
			for decoder.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return fmt.Errorf("invalid Claude chat config array")
			}
		default:
			return fmt.Errorf("invalid Claude chat config delimiter")
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("trailing Claude chat config data")
	}
	return nil
}
