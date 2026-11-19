package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/codex"
	"github.com/Nathandela/swarm/internal/adapter/detect"
	"github.com/Nathandela/swarm/internal/adapter/registry"
	"github.com/Nathandela/swarm/internal/claudeinput"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/discord"
	"github.com/Nathandela/swarm/internal/logins"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/bwmarrin/discordgo"
)

func runDiscordDoctor(ctx context.Context, cc daemon.ClientConfig, dir, channel string, out, errout io.Writer) int {
	if _, err := os.Stat(discord.ConfigPath(dir)); os.IsNotExist(err) {
		return discordCLIError(errout, errors.New("configure the place with swarm discord connect"))
	}
	cfg, err := discord.LoadConfig(dir)
	if err != nil {
		return discordCLIError(errout, err)
	}
	if cfg.Mode != "teammate" {
		return discordCLIError(errout, errors.New("this place uses the legacy connector; teammate checks require --mode teammate"))
	}
	var findings []doctorFinding
	check := func(name, detail string, err error) {
		f := doctorFinding{Check: name, Status: "ok", Detail: detail}
		if err != nil {
			f.Status = "fail"
			f.Detail = err.Error()
		}
		findings = append(findings, f)
	}
	checkRoot := func(name, root string) {
		info, e := os.Stat(root)
		if e == nil && !info.IsDir() {
			e = errors.New("workspace must be a directory")
		}
		check(name, root, e)
	}
	checkRoot("default workspace", cfg.WorkDir)
	for _, name := range sortedProjectNames(cfg.Projects) {
		checkRoot("project "+name, cfg.Projects[name])
	}
	token, err := discord.LoadToken(cfg.TokenFile)
	if err == nil {
		var client *discord.DiscordClient
		client, err = discord.NewDiscordClient(token)
		if err == nil {
			findings = append(findings, discordNetworkChecks(ctx, client, cfg, channel)...)
		}
	}
	if err != nil {
		check("bot credential", "", err)
	}
	caps := []string{protocol.CapManagerLaunch, protocol.CapManagerProvenance, protocol.CapManagerRetire, protocol.CapOwnerLaunch, protocol.CapMessageOrigin, protocol.CapJournalSubscribeFrom, protocol.CapLoginsManage}
	c, err := protocol.DialContext(ctx, cfg.DaemonSocket, caps)
	managed := false
	if err == nil {
		defer func() { _ = c.Close() }()
		for _, capability := range caps {
			if !slices.Contains(c.Capabilities(), capability) {
				err = fmt.Errorf("daemon lacks %s; upgrade the supervisor", capability)
				break
			}
		}
		if slices.Contains(c.Capabilities(), protocol.CapLoginsManage) {
			accounts, e := c.Logins(protocol.LoginsReq{Action: "list", Provider: cfg.ManagerCLI})
			if e != nil {
				check("managed provider login", "", e)
			} else {
				for _, login := range accounts.Logins {
					if login.Provider == cfg.ManagerCLI && login.Active {
						managed = true
						var loginErr error
						if login.State != logins.AuthReady {
							loginErr = fmt.Errorf("selected login is %s", login.State)
						}
						check("managed provider login", login.State, loginErr)
						break
					}
				}
			}
		}
	}
	check("home daemon", "manager capabilities negotiated", err)
	a, _ := registry.New(cfg.ManagerCLI)
	probe := adapter.Detect(a, detect.Host{})
	qualified := codex.SupportsNativeTurns(probe.Version)
	if cfg.ManagerCLI == "claude" {
		qualified = claudeinput.SupportsNativeTurns(probe.Version)
	}
	var problem error
	if !qualified {
		problem = fmt.Errorf("%s version %q is missing or unqualified for manager turns", cfg.ManagerCLI, probe.Version)
	}
	if saved, e := daemon.LoadSavedEnv(cc.StateDir); e == nil {
		resolved, e := (detect.EnvHost{Env: saved}).LookPath(a.Binary())
		first, _ := filepath.EvalSymlinks(resolved)
		second, _ := filepath.EvalSymlinks(probe.Path)
		if e != nil || first != second {
			problem = errors.New("daemon and invoking shell resolve different provider executables; inspect swarm doctor")
		}
	}
	check("native manager CLI", cfg.ManagerCLI+" "+probe.Version, problem)
	if !managed && probe.Found && qualified {
		bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
		args := []string{"login", "status"}
		if cfg.ManagerCLI == "claude" {
			args = []string{"auth", "status"}
		}
		e := exec.CommandContext(bounded, probe.Path, args...).Run()
		cancel()
		if e != nil {
			e = errors.New("native CLI could not confirm its local login; check the provider account")
		}
		check("native provider login", "local CLI reports logged in", e)
	}
	if err := writeJSON(out, findings); err != nil {
		return discordCLIError(errout, err)
	}
	for _, f := range findings {
		if f.Status == "fail" {
			return 1
		}
	}
	return 0
}

func sortedProjectNames(projects map[string]string) []string {
	var names []string
	for name := range projects {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func discordNetworkChecks(ctx context.Context, c *discord.DiscordClient, cfg discord.Config, channel string) []doctorFinding {
	var me discordgo.User
	if err := c.REST(ctx, http.MethodGet, "/users/@me", nil, &me); err != nil || !me.Bot || me.ID != cfg.ApplicationID {
		return []doctorFinding{{Check: "Discord app", Status: "fail", Detail: "token cannot verify the enrolled bot identity"}}
	}
	findings := []doctorFinding{{Check: "Discord app", Status: "ok", Detail: me.Username}}
	if err := c.Open(ctx); err != nil {
		findings = append(findings, doctorFinding{Check: "Gateway", Status: "fail", Detail: err.Error()})
	} else {
		_ = c.Close()
		findings = append(findings, doctorFinding{Check: "Gateway", Status: "ok", Detail: "message intents accepted"})
	}
	channels := cfg.ChannelIDs
	if channel != "" {
		channels = []string{channel}
	}
	if len(channels) == 0 {
		return append(findings, doctorFinding{Check: "discussion access", Status: "warn", Detail: "access varies by location", Fix: "run swarm discord doctor --channel CHANNEL_ID"})
	}
	for _, id := range channels {
		var actual discordgo.Channel
		f := doctorFinding{Check: "channel " + id, Status: "fail", Detail: "channel unavailable or outside configured scope"}
		if !discordSnowflake(id) {
			findings = append(findings, f)
			continue
		}
		if e := c.REST(ctx, http.MethodGet, "/channels/"+id, nil, &actual); e == nil && actual.ID == id {
			bits, e := discord.TeammatePermissions(ctx, c, cfg, discord.Route{ChannelID: id, GuildID: actual.GuildID}, me.ID)
			required := int64(discordgo.PermissionViewChannel | discordgo.PermissionReadMessageHistory | discordgo.PermissionSendMessagesInThreads)
			if actual.Type == discordgo.ChannelTypeGuildText {
				required |= discordgo.PermissionCreatePublicThreads | discordgo.PermissionSendMessages
			}
			switch {
			case e != nil:
				f.Detail = e.Error()
			case actual.Type != discordgo.ChannelTypeGuildText && !actual.IsThread():
				f.Detail = "select a regular text channel or a discussion thread"
			case bits&required != required:
				f.Detail = "missing channel history, thread creation or send permission"
			case actual.IsThread() && actual.ThreadMetadata != nil && actual.ThreadMetadata.Locked:
				f.Detail = "thread is locked"
			default:
				f.Status = "ok"
				f.Detail = "discussion permissions verified"
			}
		}
		findings = append(findings, f)
	}
	return findings
}

func discordSnowflake(value string) bool {
	if len(value) == 0 || len(value) > 20 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
