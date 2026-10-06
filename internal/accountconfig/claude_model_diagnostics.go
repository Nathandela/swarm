package accountconfig

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

type ClaudeFailureModel struct {
	Model, RequestID string
	Line             uint64
}

// ReadClaudeFailureModel pairs the pinned native main-request diagnostic with
// an independently proved current-turn terminal API-error transcript request ID.
// The caller owns child/file custody and transcript turn/parent-chain proof.
// File order or timestamps alone never identify the failed request.
func ReadClaudeFailureModel(reader io.Reader, requestID, failureClass string) (ClaudeFailureModel, error) {
	var proof ClaudeFailureModel
	if !claudeDiagnosticID(requestID) || (failureClass != "rate_limit" && failureClass != "authentication_failed") {
		return proof, Conflict("invalid-native-failure-proof")
	}
	input := bufio.NewReaderSize(reader, 64<<10)
	var total int
	var line uint64
	for {
		raw, err := input.ReadSlice('\n')
		if errors.Is(err, io.EOF) && len(raw) == 0 {
			break
		}
		total += len(raw)
		line++
		if err != nil || total > 16<<20 || !ValidClaudeNativeProofJSON(raw) {
			return ClaudeFailureModel{}, Conflict("invalid-native-diagnostics")
		}
		var record struct {
			Event string `json:"event"`
			Data  struct {
				Model        string `json:"model"`
				QuerySource  string `json:"query_source"`
				ErrorKind    string `json:"error_kind"`
				APIErrorType string `json:"api_error_type"`
				Response     struct {
					Status    int    `json:"status"`
					RequestID string `json:"request_id"`
				} `json:"response"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &record) != nil {
			return ClaudeFailureModel{}, Conflict("invalid-native-diagnostics")
		}
		if record.Event != "cli_api_error" || !claudeMainDiagnosticQuery(record.Data.QuerySource) || record.Data.Response.RequestID != requestID {
			continue
		}
		validClass := failureClass == "rate_limit" && record.Data.Response.Status == 429 && record.Data.ErrorKind == "rate_limit" && record.Data.APIErrorType == "rate_limit_error"
		validClass = validClass || failureClass == "authentication_failed" && record.Data.Response.Status == 401 && record.Data.APIErrorType == "authentication_error"
		if !validClass || !claudeDiagnosticModel(record.Data.Model) || proof.Line != 0 {
			return ClaudeFailureModel{}, Conflict("ambiguous-native-failure-model")
		}
		proof = ClaudeFailureModel{record.Data.Model, requestID, line}
	}
	if proof.Line == 0 {
		return proof, Conflict("native-failure-model-unobserved")
	}
	return proof, nil
}

func claudeMainDiagnosticQuery(source string) bool {
	switch source {
	case "repl_main_thread", "repl_main_thread:outputStyle:Concise", "repl_main_thread:outputStyle:Explanatory", "repl_main_thread:outputStyle:Learning", "repl_main_thread:outputStyle:Proactive", "repl_main_thread:outputStyle:custom":
		return true
	default:
		return false
	}
}

func claudeDiagnosticID(value string) bool {
	if value == "" || len(value) > 200 {
		return false
	}
	for _, char := range value {
		allowed := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("_-.:", char)
		if !allowed {
			return false
		}
	}
	return true
}

func claudeDiagnosticModel(value string) bool {
	if !claudeDiagnosticID(value) || len(value) > 128 {
		return false
	}
	switch strings.ToLower(value) {
	case "sonnet", "opus", "haiku", "opusplan", "auto", "default", "nonconforming", "unknown", "undefined", "null", "synthetic":
		return false
	}
	return true
}

// ValidClaudeNativeProofJSON bounds nesting and rejects both exact and folded
// duplicate keys before Go's case-insensitive struct field matching can apply.
func ValidClaudeNativeProofJSON(raw []byte) bool {
	return validClaudeJSON(raw, true)
}

func validClaudeJSON(raw []byte, foldKeys bool) bool {
	if len(raw) > maxSourceBytes {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 64 {
			return false
		}
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return true
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				name, ok := key.(string)
				if foldKeys {
					name = strings.ToLower(name)
				}
				if err != nil || !ok || seen[name] || !value(depth+1) {
					return false
				}
				seen[name] = true
			}
		case '[':
			for decoder.More() {
				if !value(depth + 1) {
					return false
				}
			}
		default:
			return false
		}
		closing, err := decoder.Token()
		return err == nil && (delimiter == '{' && closing == json.Delim('}') || delimiter == '[' && closing == json.Delim(']'))
	}
	if !value(0) {
		return false
	}
	_, err := decoder.Token()
	return errors.Is(err, io.EOF)
}
