// Command swarm-fake-agent is a scripted stand-in for a real agent CLI,
// driven by internal/fakeagent. It is a dev/test binary only, never
// shipped (E1.9).
package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Nathandela/swarm/internal/fakeagent"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: swarm-fake-agent <script-path|->")
		os.Exit(2)
	}

	var script io.Reader
	fromStdin := os.Args[1] == "-"
	if fromStdin {
		script = os.Stdin
	} else {
		f, err := os.Open(os.Args[1])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		defer f.Close()
		script = f
	}

	steps, err := fakeagent.Parse(script)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	// When the script itself came from stdin, stdin is already consumed, so an
	// ask step has no channel left to read its answer from. Reject before running.
	if fromStdin {
		for _, s := range steps {
			if s.Kind == fakeagent.KindAsk {
				fmt.Fprintln(os.Stderr, "ask requires a script file (stdin is consumed by the script)")
				os.Exit(2)
			}
		}
	}

	code, err := fakeagent.Run(steps, os.Stdin, os.Stdout, time.Sleep)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(3)
	}
	os.Exit(code)
}
