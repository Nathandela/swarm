//go:build !linux

package persist

import "github.com/Nathandela/swarm/internal/procstart"

// Other platforms retain the existing strict installation proof until their
// loaded-image inspection has independent native qualification.
func MatchCLIProcessImage(pid int, start int64, path, expected string) bool {
	if pid <= 0 || start <= 0 {
		return false
	}
	before, err := procstart.StartTime(pid)
	if err != nil || before != start || !MatchCLIFingerprint(path, expected) {
		return false
	}
	after, err := procstart.StartTime(pid)
	return err == nil && after == before
}
