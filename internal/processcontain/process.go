package processcontain

import "errors"

var (
	ErrUnsupported = errors.New("native descendant containment unsupported")
	ErrUnavailable = errors.New("native descendants could not be contained")
)

type Identity struct {
	PID       int   `json:"pid"`
	StartTime int64 `json:"start_time"`
}

func (p Identity) Alive() bool {
	if p.PID <= 0 || p.StartTime <= 0 {
		return false
	}
	return processAlive(p.PID, p.StartTime)
}
