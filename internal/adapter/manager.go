package adapter

// ManagerSpec describes the trusted per-invocation stdio bridge. It contains
// references to private service custody, never speech or native credentials.
type ManagerSpec struct {
	Command string
	Args    []string
}
