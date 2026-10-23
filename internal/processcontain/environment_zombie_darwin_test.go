//go:build darwin

package processcontain

import (
	"testing"

	"golang.org/x/sys/unix"
)

func TestDarwinProcessRecheckUsesCurrentLiveness(t *testing.T) {
	var before unix.KinfoProc
	before.Eproc.Ucred.Uid = 42
	before.Proc.P_starttime.Sec = 100
	before.Proc.P_starttime.Usec = 23
	before.Proc.P_stat = 2

	for _, test := range []struct {
		name   string
		change func(*unix.KinfoProc)
		live   bool
	}{
		{name: "same-live-process", live: true},
		{name: "exited-after-snapshot", change: func(p *unix.KinfoProc) { p.Proc.P_stat = 5 }},
		{name: "reused-pid", change: func(p *unix.KinfoProc) { p.Proc.P_starttime.Usec++ }},
		{name: "changed-owner", change: func(p *unix.KinfoProc) { p.Eproc.Ucred.Uid++ }},
		{name: "unknown-state-still-needs-inspection", change: func(p *unix.KinfoProc) { p.Proc.P_stat = 0 }, live: true},
		{name: "stopped-process-still-needs-inspection", change: func(p *unix.KinfoProc) { p.Proc.P_stat = 4 }, live: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			current := before
			if test.change != nil {
				test.change(&current)
			}
			if got := sameLiveDarwinProcess(before, current); got != test.live {
				t.Fatalf("current process may still write: got %v, want %v", got, test.live)
			}
		})
	}
}
