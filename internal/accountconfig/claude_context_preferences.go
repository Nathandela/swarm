package accountconfig

var claudeMutableTrustKeys = []string{"hasTrustDialogAccepted", "hasClaudeMdExternalIncludesApproved", "hasClaudeMdExternalIncludesWarningShown"}
var claudeFrozenProjectPreferenceKeys = []string{"allowedTools", "mcpServers", "enabledMcpjsonServers", "disabledMcpjsonServers", "ignorePatterns"}

// Native project trust acknowledgements are mutable private state. They do not
// change MCP routing or permission grants. Empty native default collections do
// not make an otherwise empty newly visited project part of the global cohort.
func claudeStablePreferences(preferences map[string]any) map[string]any {
	out := map[string]any{}
	if servers, ok := preferences["mcpServers"]; ok {
		out["mcpServers"] = servers
	}
	projects := map[string]any{}
	if original, ok := preferences["projects"].(map[string]any); ok {
		for path, raw := range original {
			project, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			stable := map[string]any{}
			for _, key := range claudeFrozenProjectPreferenceKeys {
				value, exists := project[key]
				if !exists {
					continue
				}
				switch collection := value.(type) {
				case []any:
					if len(collection) == 0 {
						continue
					}
				case map[string]any:
					if len(collection) == 0 {
						continue
					}
				}
				stable[key] = value
			}
			if len(stable) > 0 {
				projects[path] = stable
			}
		}
	}
	if len(projects) > 0 {
		out["projects"] = projects
	}
	return out
}

func mergeClaudePreferences(current, owner map[string]any) {
	delete(current, "mcpServers")
	if value, ok := owner["mcpServers"]; ok {
		current["mcpServers"] = value
	}
	projects, _ := current["projects"].(map[string]any)
	if projects == nil {
		projects = map[string]any{}
	}
	for _, raw := range projects {
		if project, ok := raw.(map[string]any); ok {
			for _, key := range claudeFrozenProjectPreferenceKeys {
				delete(project, key)
			}
		}
	}
	if original, ok := owner["projects"].(map[string]any); ok {
		for path, raw := range original {
			ordinary, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			project, _ := projects[path].(map[string]any)
			if project == nil {
				project = map[string]any{}
			}
			for key, value := range ordinary {
				mutable := false
				for _, trust := range claudeMutableTrustKeys {
					if key == trust {
						mutable = true
					}
				}
				if _, exists := project[key]; !mutable || !exists {
					project[key] = value
				}
			}
			projects[path] = project
		}
	}
	if len(projects) > 0 {
		current["projects"] = projects
	} else {
		delete(current, "projects")
	}
}
