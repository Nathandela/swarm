// Command hookprobe is a TEST-ONLY stand-in for a real agent CLI's hook. It
// dials the characterization hook sink named by $SWARM_CHAR_HOOK_SINK, posts a
// couple of newline-delimited JSON payloads (as a real CLI's hook command
// would), prints a marker to stdout so it also produces a PTY capture, and
// exits. swarm-char's char_test.go drives it to prove the hook-collection sink
// records payloads into Fixture.HookPayloads end to end, without a real CLI.
// Never shipped.
package main

import (
	"fmt"
	"net"
	"os"
)

func main() {
	if sink := os.Getenv("SWARM_CHAR_HOOK_SINK"); sink != "" {
		if conn, err := net.Dial("unix", sink); err == nil {
			fmt.Fprintln(conn, `{"event":"SessionStart","cwd":"/work"}`)
			fmt.Fprintln(conn, `{"hook_event_name":"Stop","reason":"end_turn"}`)
			_ = conn.Close()
		} else {
			fmt.Fprintln(os.Stderr, "hookprobe: dial sink:", err)
		}
	}
	fmt.Println("hookprobe-marker done")
}
