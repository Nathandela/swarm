package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/discord"
)

func discordCLIRig(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv(daemon.EnvStateDir, root)
	token := filepath.Join(root, "token")
	if e := os.WriteFile(token, []byte("test-token-never-print\n"), 0600); e != nil {
		t.Fatal(e)
	}
	return filepath.Join(root, "discord"), token
}
func cliDiscord(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, err bytes.Buffer
	code := dispatchDiscord(args, &out, &err)
	return code, out.String(), err.String()
}
func TestDiscordConnectKeepsIdentityAcrossUnicodeRename(t *testing.T) {
	dir, token := discordCLIRig(t)
	args := []string{"connect", "--name", "夜の群れ 🙂", "--application-id", "100", "--owner-id", "1", "--token-file", token, "--guild", "10", "--channel", "20", "--allow-user", "2"}
	if code, out, err := cliDiscord(t, args...); code != 0 || strings.Contains(out+err, "test-token") {
		t.Fatal(code, out, err)
	}
	first, e := discord.LoadConfig(dir)
	if e != nil {
		t.Fatal(e)
	}
	if first.Name != "夜の群れ 🙂" || first.InstallationID == "" {
		t.Fatal(first)
	}
	if code, out, err := cliDiscord(t, "connect", "--name", "renamed 🦋"); code != 0 {
		t.Fatal(code, out, err)
	}
	second, e := discord.LoadConfig(dir)
	if e != nil {
		t.Fatal(e)
	}
	if first.InstallationID != second.InstallationID || second.Name != "renamed 🦋" || !reflect.DeepEqual(first.GuildIDs, second.GuildIDs) || second.TokenFile != token {
		t.Fatal(first, second)
	}
}
func TestDiscordReplyReadsExactBodyAndRejectsFlagsBeforeEffects(t *testing.T) {
	dir, _ := discordCLIRig(t)
	body := "quoted ' \" $(not a shell) 🙂\nlast line\n"
	path := filepath.Join(t.TempDir(), "body file")
	if e := os.WriteFile(path, []byte(body), 0600); e != nil {
		t.Fatal(e)
	}
	previous := discordLocalCall
	t.Cleanup(func() { discordLocalCall = previous })
	calls := 0
	discordLocalCall = func(_ context.Context, stateDir, operation string, request, response any) error {
		calls++
		if stateDir != dir || operation != "reply" {
			t.Fatal(stateDir, operation)
		}
		r, ok := request.(discord.LocalReply)
		if !ok || r.Ref != "ref" || r.Body != body || r.ID != "final" {
			t.Fatal(request)
		}
		raw, _ := json.Marshal(discord.Publication{State: discord.StateSent})
		return json.Unmarshal(raw, response)
	}
	if code, out, err := cliDiscord(t, "reply", "ref", "--body-file", path); code != 0 {
		t.Fatal(code, out, err)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	for _, args := range [][]string{{"reply", "--body-file", path}, {"reply", "ref", "--text", "no"}, {"reply", "ref", "--body-file", path, "extra"}, {"context", "ref", "--body-file", path}, {"include", "--state-dir", dir}} {
		if code, out, err := cliDiscord(t, args...); code != misuseExit {
			t.Fatal(args, code, out, err)
		}
	}
	if calls != 1 {
		t.Fatal("bad arguments caused effects", calls)
	}
	if e := os.WriteFile(path, []byte(strings.Repeat("x", discord.MaxPublicationBytes+1)), 0600); e != nil {
		t.Fatal(e)
	}
	if code, _, _ := cliDiscord(t, "reply", "ref", "--body-file", path); code == 0 || calls != 1 {
		t.Fatal("oversized body published", code, calls)
	}
	if e := os.WriteFile(path, []byte{0xff}, 0600); e != nil {
		t.Fatal(e)
	}
	if code, _, _ := cliDiscord(t, "reply", "ref", "--body-file", path); code == 0 || calls != 1 {
		t.Fatal("invalid UTF-8 silently rewritten", code, calls)
	}
}
func TestDiscordStatusDisconnectedAndNoInlineSecret(t *testing.T) {
	_, token := discordCLIRig(t)
	if code, out, err := cliDiscord(t, "connect", "--name", "Atlas", "--application-id", "100", "--owner-id", "1", "--token-file", token); code != 0 {
		t.Fatal(code, out, err)
	}
	previous := discordLocalCall
	t.Cleanup(func() { discordLocalCall = previous })
	discordLocalCall = func(context.Context, string, string, any, any) error { return errors.New("not running") }
	_, out, err := cliDiscord(t, "status")
	var status discord.LocalStatus
	if json.Unmarshal([]byte(out), &status) != nil || status.Connected || status.Name != "Atlas" || strings.Contains(out+err, "test-token-never-print") {
		t.Fatal(out, err)
	}
	if code, out, err := cliDiscord(t, "connect", "--token", "inline-secret"); code != misuseExit || strings.Contains(out+err, "inline-secret") {
		t.Fatal(code, out, err)
	}
}
func TestDiscordInstallCommandsAreShellFreeAndRealRunnerRefusesTests(t *testing.T) {
	dir, _ := discordCLIRig(t)
	previous := discordUnitRun
	t.Cleanup(func() { discordUnitRun = previous })
	var commands [][]string
	discordUnitRun = func(name string, args ...string) error {
		commands = append(commands, append([]string{name}, args...))
		return nil
	}
	home := t.TempDir()
	binary := "/opt/a directory/swarm'bin"
	if e := installDiscordUnit("linux", home, binary, dir); e != nil {
		t.Fatal(e)
	}
	want := [][]string{{"systemctl", "--user", "daemon-reload"}, {"systemctl", "--user", "enable", "--now", "swarm-discord.service"}}
	if !reflect.DeepEqual(commands, want) {
		t.Fatal(commands)
	}
	path, e := discord.UnitPath("linux", home)
	if e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile(path)
	if e != nil || !strings.Contains(string(data), binary) {
		t.Fatal(string(data), e)
	}
	if e = runDiscordUnit("systemctl", "--user", "stop", "swarm-discord.service"); e == nil {
		t.Fatal("production init runner permitted in test")
	}
}

func TestDiscordRunAndProposalDelegateOnlyExplicitInputs(t *testing.T) {
	dir, _ := discordCLIRig(t)
	previousRun := discordServiceRun
	previousCall := discordLocalCall
	t.Cleanup(func() { discordServiceRun = previousRun; discordLocalCall = previousCall })
	ran := false
	discordServiceRun = func(ctx context.Context, path string) error {
		if path != dir || ctx.Err() != nil {
			t.Fatal(path, ctx.Err())
		}
		ran = true
		return nil
	}
	if code, out, err := cliDiscord(t, "run"); code != 0 || !ran {
		t.Fatal(code, out, err)
	}
	calls := 0
	discordLocalCall = func(_ context.Context, path, operation string, request, response any) error {
		calls++
		if path != dir || operation != "propose" {
			t.Fatal(path, operation)
		}
		r, ok := request.(discord.LocalProposal)
		if !ok || r.Ref != "ref" || !reflect.DeepEqual(r.CandidateIDs, []string{"local/one", "local/two"}) {
			t.Fatal(request)
		}
		return nil
	}
	if code, out, err := cliDiscord(t, "propose", "ref", "--candidate-ids", `["local/one","local/two"]`); code != 0 {
		t.Fatal(code, out, err)
	}
	for _, value := range []string{"null", "[]", `{"id":"one"}`, `[""]`} {
		if code, out, err := cliDiscord(t, "propose", "ref", "--candidate-ids", value); code != misuseExit {
			t.Fatal(code, out, err)
		}
	}
	if calls != 1 {
		t.Fatal("invalid proposal admitted", calls)
	}
}
