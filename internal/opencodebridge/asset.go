// Package opencodebridge connects the supervisor to its native OpenCode TUI plugin.
package opencodebridge

import _ "embed"

// PluginSource is written into the managed worker's private launch directory.
//
//go:embed tui.mjs
var PluginSource []byte
