package main

import (
	"bytes"
	"github.com/Nathandela/swarm/internal/hookclient"
	"testing"
)

func TestClaudeChatSpawnExplicitOptionalPromptNeverDelegates(t *testing.T) {
	for _, prompt := range []string{"", "native owner 🐝\n--not-argv"} {
		t.Run(prompt, func(t *testing.T) {
			t.Setenv(hookclient.EnvSessionID, "")
			client := newFakeSpawnClient()
			args := []string{"--cli", "claude", "--interface", "swarm-chat", "--dir", t.TempDir(), "--model", "opus"}
			if prompt != "" {
				args = append(args, "--prompt", prompt)
			}
			var out, diagnostic bytes.Buffer
			if code := runSpawn(args, client, &out, &diagnostic); code != 0 {
				t.Fatalf("explicit chat refused: exit%d %s", code, diagnostic.String())
			}
			req := onlyLaunch(t, client)
			if req.Agent != "claude" || req.Options["claude_interface"] != "swarm-chat" || req.Options["model"] != "opus" || req.InitialPrompt != prompt || req.SpawnIntent != "" || req.SpawnedFrom != "" {
				t.Fatalf("chat acquired delegate lineage or lost prompt: %+v", req)
			}
		})
	}
}

func TestClaudeChatSpawnRefusesInvalidInterfaceAndDocumentBeforeEffects(t *testing.T) {
	for _, args := range [][]string{
		{"--cli", "claude", "--interface=", "--prompt", "x"},
		{"--cli", "claude", "--interface", "other", "--prompt", "x"},
		{"--cli", "codex", "--interface", "swarm-chat", "--prompt", "x"},
		{"--cli", "claude", "--interface", "swarm-chat", "--handoff", "/missing-document"},
		{"--cli", "claude", "--interface", "swarm-chat", "--delegate", "/missing-document"},
		{"--cli", "claude", "--interface", "swarm-chat", "--prompt", "x", "--handoff", "/missing-document"},
	} {
		t.Run(args[len(args)-1], func(t *testing.T) {
			client := newFakeSpawnClient()
			var out, diagnostic bytes.Buffer
			if code := runSpawn(args, client, &out, &diagnostic); code != 2 || len(client.reqs()) != 0 {
				t.Fatalf("invalid chat reached launch/file IO: exit%d reqs%d %s", code, len(client.reqs()), diagnostic.String())
			}
		})
	}
	client := newFakeSpawnClient()
	var out, diagnostic bytes.Buffer
	if code := runSpawn([]string{"--cli", "claude"}, client, &out, &diagnostic); code != 2 || len(client.reqs()) != 0 {
		t.Fatal("ordinary spawn lost required prompt/document")
	}
}

func TestClaudeChatSpawnRefusesInheritedAgentLineage(t *testing.T) {
	t.Setenv(hookclient.EnvSessionID, "caller-session")
	client := newFakeSpawnClient()
	var out, diagnostic bytes.Buffer
	if code := runSpawn([]string{"--cli", "claude", "--interface", "swarm-chat", "--prompt", "x"}, client, &out, &diagnostic); code != 2 || len(client.reqs()) != 0 {
		t.Fatal("agent lineage was silently cleared or new chat delegated", code, diagnostic.String())
	}
}
