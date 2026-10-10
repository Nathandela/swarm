package adapter

// TypedStatusSource interprets event bodies without adding I/O to the frozen Adapter.
// A claimed event with no dimensions is ignored, including its static mapping.
type TypedStatusSource interface {
	EventStatus(HookPayload, string) (TypedStatus, bool)
}

// TypedStatus preserves concurrent normalized waits alongside the displayed dimensions.
type TypedStatus struct {
	Dimensions map[string]string
	Waiting    []string
}
