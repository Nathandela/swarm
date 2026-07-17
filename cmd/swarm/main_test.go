package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDispatch(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantExit   int
		wantStderr string
	}{
		{
			// No args opens the TUI (F1). Under `go test` stdout is not a terminal,
			// so the interactive-terminal guard fires with a clear error and a
			// non-zero exit — never a panic or a half-drawn screen. The real TUI path
			// (live daemon + PTY) is exercised by TestTUI_OpensAndRestoresOverPTY.
			name:       "no args, no tty, reports not-a-terminal",
			args:       []string{},
			wantExit:   1,
			wantStderr: "not a terminal",
		},
		{
			name:       "daemon subcommand routes to stub",
			args:       []string{"daemon"},
			wantExit:   1,
			wantStderr: "daemon: not implemented",
		},
		{
			name:       "shim subcommand without --config prints usage",
			args:       []string{"shim"},
			wantExit:   2,
			wantStderr: "usage",
		},
		{
			name:       "hook subcommand routes to stub",
			args:       []string{"hook"},
			wantExit:   1,
			wantStderr: "hook: not implemented",
		},
		{
			name:       "unknown subcommand prints usage",
			args:       []string{"bogus"},
			wantExit:   2,
			wantStderr: "usage",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			gotExit := dispatch(tt.args, &stdout, &stderr)
			if gotExit != tt.wantExit {
				t.Errorf("dispatch(%v) exit = %d, want %d", tt.args, gotExit, tt.wantExit)
			}
			got := strings.ToLower(stderr.String())
			want := strings.ToLower(tt.wantStderr)
			if !strings.Contains(got, want) {
				t.Errorf("dispatch(%v) stderr = %q, want substring %q", tt.args, stderr.String(), tt.wantStderr)
			}
		})
	}
}
