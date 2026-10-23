package logins

import "encoding/json"

// ReadClaudeStatusLine reads native configuration without retaining unrelated settings.
func ReadClaudeStatusLine(path string) (map[string]json.RawMessage, bool, error) {
	raw, err := readAuthConfig(path)
	if err != nil || len(raw) == 0 {
		return nil, false, err
	}
	var settings map[string]json.RawMessage
	if validateJSON(raw) != nil || json.Unmarshal(raw, &settings) != nil || settings == nil {
		return nil, false, ErrIneligible
	}
	value, present := settings["statusLine"]
	if !present {
		return nil, false, nil
	}
	var line map[string]json.RawMessage
	if json.Unmarshal(value, &line) != nil {
		return nil, true, ErrIneligible
	}
	return line, true, nil
}
