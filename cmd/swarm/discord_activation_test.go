package main

import (
	"github.com/Nathandela/swarm/internal/discord"
	"testing"
)

func TestDiscordConnectAndDisconnectControlNativeInputActivation(t *testing.T) {
	dir, token := discordCLIRig(t)
	previous := discordUnitRun
	discordUnitRun = func(string, ...string) error { return nil }
	t.Cleanup(func() { discordUnitRun = previous })
	if code, out, err := cliDiscord(t, "connect", "--name", "Any chosen name", "--application-id", "100", "--owner-id", "1", "--token-file", token); code != 0 {
		t.Fatal(code, out, err)
	}
	if !discord.NativeInputEnabled(dir) {
		t.Fatal("connect did not enable native input for newly launched agents")
	}
	if code, out, err := cliDiscord(t, "disconnect"); code != 0 {
		t.Fatal(code, out, err)
	}
	if discord.NativeInputEnabled(dir) {
		t.Fatal("disconnect left new-agent native integration enabled")
	}
}
