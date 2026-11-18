package skeleton

import (
	"errors"
	"path/filepath"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
)

func (*coreAPI) ManagerLaunchSupported() bool { return true }

func managerAdapterSpec(m *daemon.ManagerLaunchSpec) *adapter.ManagerSpec {
	if m == nil {
		return nil
	}
	return &adapter.ManagerSpec{Command: m.Command, Args: []string{"voice", "mcp", "--socket", m.BridgeSocket, "--auth-file", m.AuthFile, "--conversation", m.ConversationID}}
}

func validateManagerSource(spec daemon.LaunchSpec, endpoint string, get func(string) (persist.Meta, bool)) error {
	if spec.Manager != nil && (spec.AgentType != "codex" && spec.AgentType != "claude" || !filepath.IsAbs(spec.Manager.Command)) {
		return errors.New("manager requires a native Codex/Claude runtime and trusted Swarm executable")
	}
	if from := spec.Options[protocol.OptionResumeFrom]; from != "" {
		_, source, err := validateResumeSource(from, spec.AgentType, endpoint, get)
		if err != nil {
			return err
		}
		if source.Role == "manager" && (spec.Manager == nil || source.ManagerConversationID != spec.Manager.ConversationID) {
			return errors.New("manager resume requires current manager launch configuration for the same conversation")
		}
		if source.Role == "manager" && source.AgentType == "claude" {
			selected := ""
			if spec.Manager.ClaudeConfigDir != nil {
				selected = *spec.Manager.ClaudeConfigDir
			}
			if selected != source.ManagerClaudeConfigDir {
				return errors.New("manager resume requires its selected native Claude configuration")
			}
		}
	}
	return nil
}

func validateManagerSelection(spec daemon.LaunchSpec, selected persist.Meta) error {
	if spec.Manager == nil {
		if selected.Role == "manager" {
			return errors.New("manager selection requires current manager launch configuration")
		}
		return nil
	}
	m := spec.Manager
	if selected.Role != "manager" || selected.ManagerConversationID != m.ConversationID || selected.ManagerBridgeSocket != m.BridgeSocket || selected.ManagerAuthFile != m.AuthFile {
		return errors.New("existing native conversation has a different manager bridge binding; reconcile it before relaunch")
	}
	if spec.AgentType == "claude" && (m.ClaudeConfigDir == nil && selected.ManagerClaudeConfigDir != "" || m.ClaudeConfigDir != nil && selected.ManagerClaudeConfigDir != *m.ClaudeConfigDir) {
		return errors.New("existing manager has a different native Claude configuration; reconcile its account before relaunch")
	}
	return nil
}
