// Command worker_mail_oracle drives the pinned Go mailbox in isolated parity fixtures.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/Nathandela/swarm/internal/workermail"
)

func main() {
	if len(os.Args) != 3 {
		os.Exit(2)
	}
	var input struct {
		Binding        workermail.Binding
		ID, Kind, Text string
		FileID         string
	}
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var out any
	var err error
	switch os.Args[1] {
	case "submit":
		out, err = workermail.Submit(os.Args[2], input.Binding, input.ID, input.Kind, input.Text, true)
	case "lookup":
		out, err = workermail.Lookup(os.Args[2], input.Binding, input.ID)
	case "read-file":
		out, err = workermail.ReadFile(os.Args[2], input.Binding, input.ID, input.FileID)
	case "pending":
		out, err = workermail.Pending(os.Args[2], input.Binding.Discussion)
	default:
		os.Exit(2)
	}
	if err == nil {
		err = json.NewEncoder(os.Stdout).Encode(out)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
