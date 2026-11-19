package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
)

type scheduleBridgeRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func runScheduleReceiptBridge(input io.Reader, output, errOut io.Writer) int {
	if err := serveScheduleReceiptBridge(input, output, submitScheduleCompletion); err != nil {
		_, _ = fmt.Fprintln(errOut, err)
		return 1
	}
	return 0
}

// The stdio server is launched directly by Codex. It exposes one receipt tool
// outside the shell sandbox and never returns a capability or session ID.
func serveScheduleReceiptBridge(input io.Reader, output io.Writer, submit func(string, []string) error) error {
	reader := bufio.NewScanner(input)
	reader.Buffer(make([]byte, 4096), 48<<10)
	encoder := json.NewEncoder(output)
	for reader.Scan() {
		var req scheduleBridgeRequest
		if err := json.Unmarshal(reader.Bytes(), &req); err != nil {
			return fmt.Errorf("invalid automation receipt bridge request: %w", err)
		}
		if len(req.ID) == 0 {
			continue
		}
		reply := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "initialize":
			var params struct {
				Version string `json:"protocolVersion"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				return fmt.Errorf("invalid automation receipt bridge initialization: %w", err)
			}
			switch params.Version {
			case "2024-11-05", "2025-03-26", "2025-06-18", "2025-11-25":
			default:
				params.Version = "2025-11-25"
			}
			reply["result"] = map[string]any{
				"protocolVersion": params.Version,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]string{"name": "swarm_schedule", "version": "1"},
			}
		case "ping":
			reply["result"] = map[string]any{}
		case "tools/list":
			reply["result"] = scheduleBridgeToolList
		case "tools/call":
			var call struct {
				Name      string `json:"name"`
				Arguments struct {
					Summary string   `json:"summary"`
					Links   []string `json:"links"`
				} `json:"arguments"`
			}
			err := json.Unmarshal(req.Params, &call)
			text := "Unknown completion tool or invalid input."
			isError := true
			if err == nil && call.Name == "complete" {
				err = submit(call.Arguments.Summary, call.Arguments.Links)
				if err == nil {
					text, isError = "Receipt recorded. Include its summary verbatim in your closing chat message.", false
				} else {
					text = err.Error()
				}
			}
			reply["result"] = map[string]any{
				"content": []map[string]string{{"type": "text", "text": text}},
				"isError": isError,
			}
		default:
			reply["error"] = map[string]any{"code": -32601, "message": "Method not found"}
		}
		if err := encoder.Encode(reply); err != nil {
			return err
		}
	}
	return reader.Err()
}

var scheduleBridgeToolList = json.RawMessage(`{"tools":[{
	"name":"complete",
	"description":"Record that your scheduled automation finished. Post the closing result in chat before calling this tool.",
	"inputSchema":{"type":"object","additionalProperties":false,"required":["summary"],"properties":{
		"summary":{"type":"string","maxLength":4096},
		"links":{"type":"array","maxItems":16,"items":{"type":"string","maxLength":1024}}
	}}
}]}`)
