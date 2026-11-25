package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Nathandela/swarm/internal/appserver"
	"os"
	"time"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	done := make(chan json.RawMessage, 1)
	client, err := appserver.Dial(ctx, os.Args[1], appserver.Options{OnNotify: func(method string, event json.RawMessage) {
		if method == "turn/completed" {
			done <- event
		}
		if method == "item/completed" {
			fmt.Println(string(event))
		}
	}})
	if err != nil {
		panic(err)
	}
	defer client.Close()
	call := func(method string, params any, target any) {
		if err := client.Call(ctx, method, params, target); err != nil {
			panic(err)
		}
	}
	var init map[string]any
	call("initialize", map[string]any{"clientInfo": map[string]string{"name": "swarm-fixture", "version": "1"}, "capabilities": map[string]bool{"experimentalApi": true}}, &init)
	if err := client.Notify(ctx, "initialized", map[string]any{}); err != nil {
		panic(err)
	}
	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	call("thread/start", map[string]any{"cwd": os.Args[2], "sandbox": "danger-full-access", "approvalPolicy": "never"}, &started)
	call("turn/start", map[string]any{"threadId": started.Thread.ID, "input": []map[string]string{{"type": "text", "text": "Use the provided synthetic native tool."}}}, nil)
	select {
	case event := <-done:
		fmt.Println(string(event))
	case <-ctx.Done():
		panic(ctx.Err())
	}
}
