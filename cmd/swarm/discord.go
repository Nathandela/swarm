package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/discord"
)

const discordUsage = "swarm discord connect|run|status|disconnect|exclude <target>|include <target>|context <ref>|reply <ref> --body-file <file>|propose <ref> --candidate-ids '[\"target\"]'"

var discordLocalCall = discord.CallLocal
var discordUnitRun = runDiscordUnit
var discordServiceRun = func(ctx context.Context, dir string) error {
	if testing.Testing() {
		return errors.New("discord: refusing live service inside a test binary")
	}
	return discord.Run(ctx, dir)
}

func dispatchDiscord(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, discordUsage)
		return misuseExit
	}
	if args[0] == "--help" || args[0] == "-h" {
		_, _ = fmt.Fprintln(stdout, discordUsage)
		return 0
	}
	command := args[0]
	rest := args[1:]
	id := ""
	switch command {
	case "context", "reply", "exclude", "include", "propose":
		var ok bool
		id, rest, ok = takeSessionID(rest)
		if !ok || id == "" {
			_, _ = fmt.Fprintln(stderr, "discord: need a reference or target ID first")
			return misuseExit
		}
	case "connect", "run", "status", "disconnect":
	default:
		_, _ = fmt.Fprintln(stderr, discordUsage)
		return misuseExit
	}
	fs := flag.NewFlagSet("discord "+command, flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", "", "connector state directory")
	var name, applicationID, ownerID, tokenFile, workdir, receptionistCLI, receptionistModel, bodyFile, publicationID, candidateJSON string
	var guilds, channels, users []string
	var install bool
	var offset int
	if command == "connect" {
		fs.StringVar(&name, "name", "", "independent bot name")
		fs.StringVar(&applicationID, "application-id", "", "Discord application ID")
		fs.StringVar(&ownerID, "owner-id", "", "Discord owner user ID")
		fs.StringVar(&tokenFile, "token-file", "", "private bot token file")
		fs.StringVar(&workdir, "workdir", "", "receptionist working directory")
		fs.StringVar(&receptionistCLI, "receptionist-cli", "", "receptionist agent CLI")
		fs.StringVar(&receptionistModel, "receptionist-model", "", "receptionist model")
		fs.BoolVar(&install, "install", false, "install and start the owner service")
		for _, option := range []struct {
			name   string
			values *[]string
		}{{"guild", &guilds}, {"channel", &channels}, {"allow-user", &users}} {
			values := option.values
			fs.Func(option.name, "allowed Discord ID (repeat for multiple)", func(value string) error { *values = append(*values, value); return nil })
		}
	}
	if command == "context" {
		fs.IntVar(&offset, "offset", 0, "context page offset")
	}
	if command == "reply" {
		fs.StringVar(&bodyFile, "body-file", "", "file containing the exact reply body")
		fs.StringVar(&publicationID, "publication-id", "final", "stable publication ID")
	}
	if command == "propose" {
		fs.StringVar(&candidateJSON, "candidate-ids", "", "JSON array of candidate target IDs")
	}
	if err := fs.Parse(rest); err != nil {
		return misuseExit
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "discord: unexpected positional argument")
		return misuseExit
	}
	if offset < 0 || (command == "reply" && (bodyFile == "" || publicationID == "")) {
		_, _ = fmt.Fprintln(stderr, "discord: invalid offset or missing body file/publication ID")
		return misuseExit
	}
	cc, err := clientConfig()
	if err != nil {
		return discordCLIError(stderr, err)
	}
	if *stateDir == "" {
		*stateDir = filepath.Join(cc.StateDir, "discord")
	}
	*stateDir, err = filepath.Abs(*stateDir)
	if err != nil {
		return discordCLIError(stderr, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	switch command {
	case "connect":
		var config discord.Config
		if _, e := os.Lstat(discord.ConfigPath(*stateDir)); e == nil {
			config, err = discord.LoadConfig(*stateDir)
			if err != nil {
				return discordCLIError(stderr, err)
			}
		} else if !os.IsNotExist(e) {
			return discordCLIError(stderr, e)
		} else {
			var random [16]byte
			if _, err = rand.Read(random[:]); err != nil {
				return discordCLIError(stderr, err)
			}
			cwd, e := os.Getwd()
			if e != nil {
				return discordCLIError(stderr, e)
			}
			socket, e := filepath.Abs(cc.SocketPath)
			if e != nil {
				return discordCLIError(stderr, e)
			}
			config = discord.Config{Version: 1, InstallationID: hex.EncodeToString(random[:]), DaemonSocket: socket, WorkDir: cwd, ReceptionistCLI: "codex"}
		}
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "name":
				config.Name = name
			case "application-id":
				config.ApplicationID = applicationID
			case "owner-id":
				config.OwnerID = ownerID
			case "token-file":
				config.TokenFile = tokenFile
			case "workdir":
				config.WorkDir = workdir
			case "receptionist-cli":
				config.ReceptionistCLI = receptionistCLI
			case "receptionist-model":
				config.ReceptionistModel = receptionistModel
			case "guild":
				config.GuildIDs = guilds
			case "channel":
				config.ChannelIDs = channels
			case "allow-user":
				config.UserIDs = users
			}
		})
		for _, path := range []*string{&config.TokenFile, &config.WorkDir} {
			if *path != "" {
				*path, err = filepath.Abs(*path)
				if err != nil {
					return discordCLIError(stderr, err)
				}
			}
		}
		config.Enabled = true
		if err = config.Validate(); err != nil {
			return discordCLIError(stderr, err)
		}
		if _, err = discord.LoadToken(config.TokenFile); err != nil {
			return discordCLIError(stderr, err)
		}
		if info, e := os.Stat(config.WorkDir); e != nil || !info.IsDir() {
			return discordCLIError(stderr, errors.New("discord: working directory does not exist"))
		}
		if err = discord.SaveConfig(*stateDir, config); err != nil {
			return discordCLIError(stderr, err)
		}
		if install {
			home, e := os.UserHomeDir()
			if e != nil {
				return discordCLIError(stderr, e)
			}
			binary, e := os.Executable()
			if e != nil {
				return discordCLIError(stderr, e)
			}
			if e = installDiscordUnit(runtime.GOOS, home, binary, *stateDir); e != nil {
				return discordCLIError(stderr, e)
			}
		}
		if err = writeJSON(stdout, struct {
			Name           string `json:"name"`
			InstallationID string `json:"installation_id"`
			StateDir       string `json:"state_dir"`
			Installed      bool   `json:"installed"`
			Next           string `json:"next"`
		}{config.Name, config.InstallationID, *stateDir, install, "swarm discord run --state-dir " + shellDiscordArg(*stateDir)}); err != nil {
			return discordCLIError(stderr, err)
		}
		return 0
	case "run":
		runCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err = discordServiceRun(runCtx, *stateDir); err != nil {
			return discordCLIError(stderr, err)
		}
		return 0
	case "status":
		config, e := discord.LoadConfig(*stateDir)
		if e != nil {
			return discordCLIError(stderr, e)
		}
		var status discord.LocalStatus
		if e = discordLocalCall(ctx, *stateDir, "status", nil, &status); e != nil {
			status = discord.LocalStatus{Name: config.Name, InstallationID: config.InstallationID, Connected: false}
		}
		if err = writeJSON(stdout, status); err != nil {
			return discordCLIError(stderr, err)
		}
		return 0
	case "disconnect":
		home, e := os.UserHomeDir()
		if e != nil {
			return discordCLIError(stderr, e)
		}
		path, e := discord.UnitPath(runtime.GOOS, home)
		if e != nil {
			return discordCLIError(stderr, e)
		}
		if _, e = os.Lstat(path); e == nil {
			if e = stopDiscordUnit(runtime.GOOS, path); e != nil {
				return discordCLIError(stderr, e)
			}
		} else if !os.IsNotExist(e) {
			return discordCLIError(stderr, e)
		}
		if _, e = os.Lstat(filepath.Join(*stateDir, "bridge.sock")); e == nil {
			if e = discordLocalCall(ctx, *stateDir, "shutdown", struct{}{}, nil); e != nil {
				return discordCLIError(stderr, e)
			}
		} else if !os.IsNotExist(e) {
			return discordCLIError(stderr, e)
		}
		if _, e = os.Lstat(discord.ConfigPath(*stateDir)); e == nil {
			config, loadErr := discord.LoadConfig(*stateDir)
			if loadErr != nil {
				return discordCLIError(stderr, loadErr)
			}
			config.Enabled = false
			if saveErr := discord.SaveConfig(*stateDir, config); saveErr != nil {
				return discordCLIError(stderr, saveErr)
			}
		} else if !os.IsNotExist(e) {
			return discordCLIError(stderr, e)
		}
		_, err = fmt.Fprintln(stdout, "Discord connector disconnected.")
		if err != nil {
			return discordCLIError(stderr, err)
		}
		return 0
	}
	var request, response any
	switch command {
	case "context":
		request = discord.LocalContextRequest{Ref: id, Offset: offset}
		response = &discord.LocalContext{}
	case "exclude", "include":
		request = discord.LocalTarget{TargetID: id}
		response = &discord.LocalStatus{}
	case "reply":
		f, e := os.Open(bodyFile)
		if e != nil {
			return discordCLIError(stderr, e)
		}
		body, e := io.ReadAll(io.LimitReader(f, discord.MaxPublicationBytes+1))
		closeErr := f.Close()
		if e != nil {
			return discordCLIError(stderr, e)
		}
		if closeErr != nil {
			return discordCLIError(stderr, closeErr)
		}
		if len(body) > discord.MaxPublicationBytes {
			return discordCLIError(stderr, discord.ErrCapacity)
		}
		if len(body) == 0 {
			return discordCLIError(stderr, errors.New("discord: empty reply body"))
		}
		if !utf8.Valid(body) {
			return discordCLIError(stderr, errors.New("discord: reply body must be valid UTF-8"))
		}
		request = discord.LocalReply{Ref: id, ID: publicationID, Body: string(body)}
		response = &discord.Publication{}
	case "propose":
		var candidates []string
		if len(candidateJSON) > 8192 || json.Unmarshal([]byte(candidateJSON), &candidates) != nil || len(candidates) < 1 || len(candidates) > 10 {
			_, _ = fmt.Fprintln(stderr, "discord: --candidate-ids must contain 1 to 10 target IDs as JSON")
			return misuseExit
		}
		for _, candidate := range candidates {
			if candidate == "" || len(candidate) > 512 {
				_, _ = fmt.Fprintln(stderr, "discord: invalid candidate target ID")
				return misuseExit
			}
		}
		request = discord.LocalProposal{Ref: id, CandidateIDs: candidates}
		response = &struct {
			Proposed bool `json:"proposed"`
		}{}
	}
	if err = discordLocalCall(ctx, *stateDir, command, request, response); err != nil {
		return discordCLIError(stderr, err)
	}
	if err = writeJSON(stdout, response); err != nil {
		return discordCLIError(stderr, err)
	}
	return 0
}

func discordNativeInputEnabled(stateDir string) bool {
	return discord.NativeInputEnabled(filepath.Join(stateDir, "discord"))
}
func discordCLIError(stderr io.Writer, err error) int {
	_, _ = fmt.Fprintf(stderr, "discord: %v\n", err)
	return 1
}
func shellDiscordArg(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func runDiscordUnit(name string, args ...string) error {
	if testing.Testing() {
		return errors.New("discord: refusing init-system command inside a test binary")
	}
	output, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("discord: %s: %w: %s", name, err, strings.TrimSpace(string(output)))
	}
	return nil
}
func installDiscordUnit(goos, home, binary, stateDir string) error {
	path, err := discord.UnitPath(goos, home)
	if err != nil {
		return err
	}
	body, err := discord.RenderUnit(goos, binary, stateDir, filepath.Join(stateDir, "connector.log"))
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err = validateDiscordUnitPath(path); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".swarm-discord-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err = file.WriteString(body); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	switch goos {
	case "darwin":
		_ = discordUnitRun("launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/com.swarm.discord")
		return discordUnitRun("launchctl", "bootstrap", "gui/"+strconv.Itoa(os.Getuid()), path)
	case "linux":
		if err = discordUnitRun("systemctl", "--user", "daemon-reload"); err != nil {
			return err
		}
		return discordUnitRun("systemctl", "--user", "enable", "--now", "swarm-discord.service")
	}
	return errors.New("discord: unsupported service platform")
}
func validateDiscordUnitPath(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Nlink != 1 || stat.Uid != uint32(os.Getuid()) {
		return errors.New("discord: unsafe service unit path")
	}
	return nil
}
func stopDiscordUnit(goos, path string) error {
	if err := validateDiscordUnitPath(path); err != nil {
		return err
	}
	var err error
	switch goos {
	case "darwin":
		err = discordUnitRun("launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/com.swarm.discord")
	case "linux":
		err = discordUnitRun("systemctl", "--user", "disable", "--now", "swarm-discord.service")
	default:
		return errors.New("discord: unsupported service platform")
	}
	if err != nil {
		return err
	}
	return os.Remove(path)
}
