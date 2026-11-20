package adapter

// ManagerSpec describes the trusted per-invocation stdio bridge. It contains
// references to private service custody, never speech or native credentials.
type ManagerSpec struct {
	NativeCLI bool
	Command   string
	Args      []string
}

// DiscordTransportTools names the VM discussion bridge's provider-approved tools.
// The controller still verifies exact call and worker grants for create and read.
func DiscordTransportTools() []string {
	return []string{"publish", "ask", "swarm_discussion_history", "swarm_discussion_answer", "swarm_reply", "swarm_read_note", "swarm_create", "swarm_read"}
}
