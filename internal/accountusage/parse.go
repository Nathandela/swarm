package accountusage

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"time"
	"unicode"

	"github.com/Nathandela/swarm/internal/accounts"
)

type codexWindow struct {
	UsedPercent   *int            `json:"used_percent"`
	ResetAt       json.RawMessage `json:"reset_at"`
	WindowSeconds int64           `json:"limit_window_seconds"`
}

type codexLimit struct {
	Allowed   *bool        `json:"allowed"`
	Primary   *codexWindow `json:"primary_window"`
	Secondary *codexWindow `json:"secondary_window"`
}

// This is the pinned Codex 0.160.0 /wham/usage wire format, rather than the
// app-server's camelCase account/rateLimits/read projection.
func parseCodex(raw []byte, credential accounts.UsageCredential) (Result, error) {
	var payload struct {
		AccountID  *string     `json:"account_id"`
		RateLimit  *codexLimit `json:"rate_limit"`
		Additional []struct {
			Feature string      `json:"metered_feature"`
			Name    string      `json:"limit_name"`
			Model   string      `json:"normal_model_slug"`
			Limit   *codexLimit `json:"rate_limit"`
		} `json:"additional_rate_limits"`
	}
	if json.Unmarshal(raw, &payload) != nil || len(payload.Additional) > 32 || (payload.AccountID != nil && !credential.MatchesAccountID(*payload.AccountID)) {
		return Result{}, &Error{Class: "malformed"}
	}
	result := Result{Labels: make(map[string]string), WindowSeconds: make(map[string]int64)}
	global := accounts.ScopeObservation{Scope: accounts.ScopeGlobal, Authority: accounts.AuthorityUnknown}
	if payload.RateLimit != nil {
		global.Authority = codexAuthority(payload.RateLimit.Allowed)
	}
	result.Observations = append(result.Observations, global)
	seen := make(map[string]bool)
	appendLimit := func(id, label, model string, limit *codexLimit) error {
		if limit == nil {
			return nil
		}
		for _, item := range []struct {
			name   string
			window *codexWindow
		}{{"primary", limit.Primary}, {"secondary", limit.Secondary}} {
			if item.window == nil {
				continue
			}
			window := item.window
			if (window.UsedPercent != nil && (*window.UsedPercent < 0 || *window.UsedPercent > 100)) || window.WindowSeconds < 0 || window.WindowSeconds > 365*24*60*60 {
				return errors.New("invalid window")
			}
			scope := "bucket:" + id + ":" + item.name
			if seen[scope] {
				return errors.New("duplicate scope")
			}
			seen[scope] = true
			observation := accounts.ScopeObservation{Scope: scope, Model: model, UsedPercent: window.UsedPercent, Authority: codexAuthority(limit.Allowed), ResetPresent: len(window.ResetAt) != 0}
			if len(window.ResetAt) != 0 && !bytes.Equal(window.ResetAt, []byte("null")) {
				var seconds int64
				if json.Unmarshal(window.ResetAt, &seconds) != nil || seconds < 0 || seconds > 253402300799 {
					return errors.New("invalid reset")
				}
				reset := time.Unix(seconds, 0).UTC()
				observation.ResetAt = &reset
			}
			result.Observations = append(result.Observations, observation)
			result.Labels[scope] = label + " " + item.name + " window"
			if window.WindowSeconds > 0 {
				result.WindowSeconds[scope] = window.WindowSeconds
			}
		}
		return nil
	}
	if appendLimit("codex", "Codex", "", payload.RateLimit) != nil {
		return Result{}, &Error{Class: "malformed"}
	}
	for _, additional := range payload.Additional {
		if !safeName(additional.Feature, 128) || additional.Feature == "codex" || (additional.Model != "" && !safeName(additional.Model, 256)) {
			return Result{}, &Error{Class: "malformed"}
		}
		label := additional.Feature
		if safeLabel(additional.Name) {
			label = additional.Name
		}
		if appendLimit(additional.Feature, label, additional.Model, additional.Limit) != nil {
			return Result{}, &Error{Class: "malformed"}
		}
	}
	if len(result.Observations) == 1 && global.Authority == accounts.AuthorityUnknown {
		return Result{}, &Error{Class: "malformed"}
	}
	return result, nil
}

func codexAuthority(allowed *bool) string {
	if allowed == nil {
		return accounts.AuthorityUnknown
	}
	if *allowed {
		return accounts.AuthorityAllowed
	}
	return accounts.AuthorityDenied
}

func parseClaude(raw []byte) (Result, error) {
	var payload map[string]json.RawMessage
	if json.Unmarshal(raw, &payload) != nil || payload == nil {
		return Result{}, &Error{Class: "malformed"}
	}
	result := Result{Labels: make(map[string]string), WindowSeconds: make(map[string]int64)}
	for _, item := range []struct {
		key, label, model string
		seconds           int64
	}{{"five_hour", "Claude 5-hour usage", "", 5 * 60 * 60}, {"seven_day", "Claude 7-day usage", "", 7 * 24 * 60 * 60}, {"seven_day_opus", "Claude Opus 7-day usage", "claude-opus", 7 * 24 * 60 * 60}, {"seven_day_sonnet", "Claude Sonnet 7-day usage", "claude-sonnet", 7 * 24 * 60 * 60}} {
		value, present := payload[item.key]
		if !present || bytes.Equal(value, []byte("null")) {
			continue
		}
		var window struct {
			Utilization *float64        `json:"utilization"`
			Reset       json.RawMessage `json:"resets_at"`
		}
		if json.Unmarshal(value, &window) != nil || (window.Utilization != nil && (math.IsNaN(*window.Utilization) || math.IsInf(*window.Utilization, 0) || *window.Utilization < 0 || *window.Utilization > 100)) {
			return Result{}, &Error{Class: "malformed"}
		}
		observation := accounts.ScopeObservation{Scope: "bucket:claude:" + item.key, Model: item.model, Authority: accounts.AuthorityUnknown, ResetPresent: len(window.Reset) != 0}
		if window.Utilization != nil {
			percent := int(math.Round(*window.Utilization))
			observation.UsedPercent = &percent
		}
		if len(window.Reset) != 0 && !bytes.Equal(window.Reset, []byte("null")) {
			var timestamp string
			if json.Unmarshal(window.Reset, &timestamp) != nil {
				return Result{}, &Error{Class: "malformed"}
			}
			reset, err := time.Parse(time.RFC3339Nano, timestamp)
			if err != nil || reset.Year() < 1970 || reset.Year() > 9999 {
				return Result{}, &Error{Class: "malformed"}
			}
			reset = reset.UTC()
			observation.ResetAt = &reset
		}
		if observation.UsedPercent != nil || observation.ResetPresent {
			result.Observations = append(result.Observations, observation)
			result.Labels[observation.Scope] = item.label
			result.WindowSeconds[observation.Scope] = item.seconds
		}
	}
	if len(result.Observations) == 0 {
		return Result{}, &Error{Class: "malformed"}
	}
	return result, nil
}

func safeName(value string, maximum int) bool {
	if value == "" || len(value) > maximum {
		return false
	}
	for _, ch := range value {
		if (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') && !strings.ContainsRune("_-./", ch) {
			return false
		}
	}
	return true
}

func safeLabel(value string) bool {
	return value != "" && len(value) <= 128 && strings.IndexFunc(value, func(ch rune) bool { return unicode.IsControl(ch) || unicode.Is(unicode.Cf, ch) }) < 0
}

// Unknown provider fields may evolve, but duplicate keys anywhere in a bounded
// JSON body are ambiguous. Token decoding also bounds nesting against abuse.
func uniqueJSON(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 64 {
			return errors.New("JSON nesting")
		}
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{':
				seen := make(map[string]bool)
				for decoder.More() {
					key, err := decoder.Token()
					name, ok := key.(string)
					if err != nil || !ok || seen[name] {
						return errors.New("duplicate JSON key")
					}
					seen[name] = true
					if err := value(depth + 1); err != nil {
						return err
					}
				}
			case '[':
				for decoder.More() {
					if err := value(depth + 1); err != nil {
						return err
					}
				}
			default:
				return errors.New("unexpected delimiter")
			}
			_, err = decoder.Token()
			return err
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
