# RED evidence — gateway CommandBridge command-IN loop (agents-tracker-5mm)

Failing-first (GG-5) for the reusable command-IN + reply poll loop.

`go test ./internal/remotegw/ -run TestCommandBridge`
```
internal/remotegw/command_loop_test.go:96:7: undefined: NewCommandBridge
internal/remotegw/command_loop_test.go:96:24: undefined: CommandBridgeConfig
FAIL	github.com/Nathandela/swarm/internal/remotegw [build failed]
```
Compile-fail RED: CommandBridge / NewCommandBridge / CommandBridgeConfig do not
exist. Unit tests pin open->forward->seal-reply, cursor advance, and per-item
fail-closed skip; the skeleton integration test drives a phone kill through a real
relay + real daemon and back.
