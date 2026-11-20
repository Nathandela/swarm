package adapter

// ManagerSpec describes the trusted per-invocation stdio bridge. It contains
// references to private service custody, never speech or native credentials.
type ManagerSpec struct {
	NativeCLI bool
	Command   string
	Args      []string
}

// DiscordTransportTools names the operations authorized by installation of the
// discussion bridge. Provider approval for shell commands remains independent.
func DiscordTransportTools() []string {
	return []string{"publish", "ask", "swarm_discussion_history", "swarm_discussion_answer"}
}
