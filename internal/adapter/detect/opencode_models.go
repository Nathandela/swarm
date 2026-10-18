package detect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"sort"
	"strings"
	"unicode"

	"github.com/Nathandela/swarm/internal/adapter"
)

// shortcut: catalogues over 4 MiB fall back to editable input; raise this cap if a real profile exceeds it.
const maxOpenCodeProbeOutput = 4 << 20

func probeOpenCodeModels() (string, []adapter.ModelChoice) {
	path, err := exec.LookPath("opencode")
	if err != nil {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	var models []adapter.ModelChoice
	// shortcut: project-local providers/defaults reflect Swarm's cwd; re-probe by form cwd if target-project settings are needed.
	if data, ok := runOpenCodeProbe(ctx, cancel, path, "models", "--verbose"); ok {
		models = parseOpenCodeModels(data)
	}
	if data, ok := runOpenCodeProbe(ctx, cancel, path, "debug", "config"); ok {
		return parseOpenCodeDefault(data), models
	}
	return "", models
}

// runOpenCodeProbe captures only bounded stdout; OpenCode owns provider filtering
// and excludes deprecated catalogue entries without any credential reads here.
func runOpenCodeProbe(ctx context.Context, cancel context.CancelFunc, path string, args ...string) ([]byte, bool) {
	if ctx.Err() != nil {
		return nil, false
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stderr = io.Discard
	cmd.WaitDelay = probeTimeout / 5
	out := &openCodeProbeOutput{limit: maxOpenCodeProbeOutput, cancel: cancel}
	cmd.Stdout = out
	if err := cmd.Run(); err != nil {
		return nil, false
	}
	return out.buf.Bytes(), true
}

type openCodeProbeOutput struct {
	buf    bytes.Buffer
	limit  int
	cancel context.CancelFunc
}

func (o *openCodeProbeOutput) Write(p []byte) (int, error) {
	if len(p) > o.limit-o.buf.Len() {
		o.cancel()
		return 0, errors.New("opencode model probe output limit")
	}
	return o.buf.Write(p)
}

// parseOpenCodeModels reads the CLI's repeated `provider/model\n{metadata}`
// records. A malformed/truncated stream yields no choices instead of a partial list.
func parseOpenCodeModels(data []byte) []adapter.ModelChoice {
	var out []adapter.ModelChoice
	seen := make(map[string]bool)
	for len(data) > 0 {
		end := bytes.IndexByte(data, '\n')
		if end < 0 {
			return nil
		}
		id := strings.TrimSpace(string(data[:end]))
		data = data[end+1:]
		var detail struct {
			ID         string `json:"id"`
			ProviderID string `json:"providerID"`
			Name       string `json:"name"`
			Status     string `json:"status"`
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		if err := dec.Decode(&detail); err != nil {
			return nil
		}
		data = bytes.TrimSpace(data[dec.InputOffset():])
		provider, model, _ := strings.Cut(id, "/")
		if !validOpenCodeModel(id) || detail.ID != model || detail.ProviderID != provider || detail.Status == "deprecated" || seen[id] {
			continue
		}
		seen[id] = true
		if detail.Name == "" || strings.IndexFunc(detail.Name, unicode.IsControl) >= 0 {
			detail.Name = id
		}
		out = append(out, adapter.ModelChoice{ID: id, Display: detail.Name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func parseOpenCodeDefault(data []byte) string {
	var config struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(data, &config) == nil && validOpenCodeModel(config.Model) {
		return config.Model
	}
	return ""
}

func validOpenCodeModel(id string) bool {
	provider, model, ok := strings.Cut(id, "/")
	return ok && provider != "" && model != "" && len(id) <= 512 &&
		strings.IndexFunc(id, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) < 0
}
