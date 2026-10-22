package loginmigration

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Ordinary sessions and other state stores do not opt a machine into the old
// account conversion. Their own loaders remain responsible for their schemas.
func legacyPresent(state string) (bool, error) {
	var registry oldRegistry
	if err := readJSON(filepath.Join(state, "accounts", "registry.json"), &registry); err == nil {
		if registry.Schema != 1 {
			return false, ErrUnsupported
		}
		if len(registry.Accounts) > 0 {
			return true, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	for _, name := range []string{"profiles", "jobs", "checks", "configurations", "recovery-inbox", "history-transfer"} {
		entries, err := os.ReadDir(filepath.Join(state, "accounts", name))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		if len(entries) > 0 {
			return true, nil
		}
	}
	var jobs struct {
		Jobs map[string]json.RawMessage `json:"jobs"`
	}
	if err := readJSON(filepath.Join(state, "accounts", "enrollment.json"), &jobs); err == nil {
		if len(jobs.Jobs) > 0 {
			return true, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	raw, err := readFile(filepath.Join(state, "auth-watch-state.json"), maxJSON, true)
	if err == nil {
		var document map[string]json.RawMessage
		if json.Unmarshal(raw, &document) == nil {
			for key, value := range document {
				if strings.HasPrefix(key, "account_") {
					return true, nil
				}
				if key == "cli_refresh" {
					var records map[string]map[string]json.RawMessage
					if json.Unmarshal(value, &records) == nil {
						for _, record := range records {
							if _, ok := record["account_enrollment"]; ok {
								return true, nil
							}
						}
					}
				}
			}
		} else if bytes.Contains(raw, []byte(`"account_`)) {
			return false, ErrUnsupported
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	entries, err := os.ReadDir(state)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		raw, err = readFile(filepath.Join(state, entry.Name(), "meta.json"), maxJSON, true)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		var meta struct {
			Agent      string          `json:"agent_type"`
			Schema     int             `json:"schema_version"`
			Binding    json.RawMessage `json:"account_binding"`
			Projection string          `json:"account_projection_ref"`
			Fallback   json.RawMessage `json:"account_claude_fallback"`
		}
		if json.Unmarshal(raw, &meta) != nil {
			if bytes.Contains(raw, []byte(`"account_binding"`)) {
				return false, ErrUnsupported
			}
			continue
		}
		if meta.Agent != "codex" && meta.Agent != "claude" {
			continue
		}
		if meta.Schema == 2 || len(meta.Binding) > 0 && string(meta.Binding) != "null" || meta.Projection != "" || len(meta.Fallback) > 0 && string(meta.Fallback) != "null" {
			return true, nil
		}
		var native struct {
			Schema int `json:"schema_version"`
		}
		if err = readJSON(filepath.Join(state, entry.Name(), "native-process.json"), &native); err == nil && native.Schema == 2 {
			return true, nil
		}
	}
	return false, nil
}
