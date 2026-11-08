package daemon

// ManagerLaunchSpec carries owner-validated references. Command comes only from
// this daemon's configured executable, never the wire or the session PATH.
type ManagerLaunchSpec struct {
	// Nil selects the daemon's native origin; an empty value explicitly selects HOME.
	ClaudeConfigDir   *string
	UnsupportedOrigin bool
	ConversationID    string
	BridgeSocket      string
	AuthFile          string
	Command           string
}

func (d *Daemon) ManagerBridgeCommand() string { return d.cfg.ShimBinary }
