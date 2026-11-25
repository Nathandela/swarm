package main

import (
	"context"
	"fmt"
	"github.com/Nathandela/swarm/internal/protocol"
	"os"
	"time"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := protocol.DialContext(ctx, os.Args[1], []string{protocol.CapOwnerLaunch, protocol.CapManagerLaunch, protocol.CapTeammateWorkerBridge})
	if err != nil {
		panic(err)
	}
	defer c.Close()
	op, err := protocol.NewLaunchOperationID()
	if err != nil {
		panic(err)
	}
	req := protocol.LaunchReq{Agent: "codex", Cwd: os.Args[2], Name: "ticket fixture", Cols: 120, Rows: 32, Options: map[string]string{"model": "gpt-5"}}
	if os.Args[3] == "dispatcher" {
		req.Manager = &protocol.ManagerLaunchReq{ConversationID: os.Args[4], BridgeSocket: os.Args[5], AuthFile: os.Args[6]}
	} else {
		req.SpawnedFrom = os.Args[4]
		req.SpawnIntent = "delegate"
		req.WorkerBridge = os.Args[5] + ":" + os.Args[6]
		req.InitialPrompt = "Perform the staged ticket check."
	}
	id, _, err := c.LaunchOperation(op, req)
	if err != nil {
		panic(err)
	}
	fmt.Println(id)
}
