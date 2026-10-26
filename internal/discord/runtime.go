package discord

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// Run owns the connector lifetime, leaving the local daemon and agents independent.
func Run(parent context.Context, stateDir string) error {
	cfg, err := LoadConfig(stateDir)
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		cfg.Enabled = true
		if err = SaveConfig(stateDir, cfg); err != nil {
			return err
		}
	}
	token, err := LoadToken(cfg.TokenFile)
	if err != nil {
		return err
	}
	store, err := OpenStore(stateDir)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	if err = store.Prune(time.Now()); err != nil {
		return err
	}
	client, err := NewDiscordClient(token)
	if err != nil {
		return err
	}
	s := NewService(cfg, store, SwarmClient{Socket: cfg.DaemonSocket}, ScopedMessenger{Client: client, Config: cfg})
	s.StateDir = stateDir
	return runConnected(parent, s, client)
}

func runConnected(parent context.Context, s *Service, client *DiscordClient) error {
	cfg, store, stateDir := s.Config, s.Store, s.StateDir
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	s.Stop = cancel
	bridge := NewBridge(s, client)
	defer bridge.Wait()
	client.OnMessage = func(m *discordgo.MessageCreate) { bridge.start(ctx, func() { bridge.Message(ctx, m) }) }
	client.OnMessageUpdate = func(m *discordgo.MessageUpdate) { bridge.start(ctx, func() { bridge.MessageUpdate(ctx, m) }) }
	client.OnMessageDelete = bridge.MessageDelete
	client.OnInteraction = func(i *discordgo.InteractionCreate) { bridge.start(ctx, func() { bridge.Interaction(ctx, i) }) }
	networkErr := make(chan error, 1)
	client.OnError = func(err error) {
		select {
		case networkErr <- err:
		default:
		}
	}
	client.OnReady = func(r *discordgo.Ready) {
		if r.Application == nil || r.Application.ID != cfg.ApplicationID {
			select {
			case networkErr <- errors.New("discord: bot token does not match configured application"):
			default:
			}
		}
	}
	localErr := make(chan error, 1)
	localDone := make(chan struct{})
	go func() { defer close(localDone); localErr <- s.ServeLocal(ctx, stateDir) }()
	defer func() { cancel(); _ = client.Close(); <-localDone }()
	if err := waitLocal(ctx, stateDir, localErr); err != nil {
		return err
	}
	if err := client.Open(ctx); err != nil {
		return err
	}
	select {
	case err := <-networkErr:
		return err
	default:
	}
	if err := bridge.Register(ctx); err != nil {
		return err
	}
	bridge.start(ctx, func() {
		refresh := time.NewTicker(5 * time.Second)
		defer refresh.Stop()
		for {
			bounded, stop := context.WithTimeout(ctx, 2*time.Second)
			_ = s.ReconcilePending(bounded)
			stop()
			bounded, stop = context.WithTimeout(ctx, 2*time.Second)
			_ = bridge.Attention(bounded)
			stop()
			select {
			case <-ctx.Done():
				return
			case <-refresh.C:
			}
		}
	})
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-localErr:
			return err
		case err := <-networkErr:
			return err
		case <-ticker.C:
			if err := store.Prune(time.Now()); err != nil {
				return err
			}
		}
	}
}
func waitLocal(ctx context.Context, dir string, failures <-chan error) error {
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-failures:
			return err
		case <-deadline.C:
			return errors.New("discord: local service did not start")
		case <-ticker.C:
			conn, err := net.DialTimeout("unix", filepath.Join(dir, "bridge.sock"), 100*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				return nil
			}
		}
	}
}

type Bridge struct {
	Service  *Service
	Client   *DiscordClient
	mu       sync.Mutex
	work     sync.WaitGroup
	stopping bool
	active   chan struct{}
}

func NewBridge(s *Service, c *DiscordClient) *Bridge {
	return &Bridge{Service: s, Client: c, active: make(chan struct{}, 64)}
}
func (b *Bridge) start(ctx context.Context, fn func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopping || ctx.Err() != nil {
		return
	}
	select {
	case b.active <- struct{}{}:
	default:
		return
	}
	b.work.Add(1)
	go func() { defer b.work.Done(); defer func() { <-b.active }(); fn() }()
}
func (b *Bridge) Wait() { b.mu.Lock(); b.stopping = true; b.mu.Unlock(); b.work.Wait() }
func (b *Bridge) Register(ctx context.Context) error {
	commands := []*discordgo.ApplicationCommand{{Name: "swarm", Description: "Talk to this Swarm", Options: []*discordgo.ApplicationCommandOption{
		{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "agents", Description: "Find agents"},
		{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "ask", Description: "Ask an agent", Options: []*discordgo.ApplicationCommandOption{
			{Type: discordgo.ApplicationCommandOptionString, Name: "agent", Description: "Agent ID or name", Required: true},
			{Type: discordgo.ApplicationCommandOptionString, Name: "text", Description: "Message", Required: true}}},
		{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "join", Description: "Follow this discussion", Options: []*discordgo.ApplicationCommandOption{{Type: discordgo.ApplicationCommandOptionString, Name: "agent", Description: "Agent ID or name", Required: true}}},
		{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "leave", Description: "Stop following this discussion"},
		{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "status", Description: "Show connection and agents"},
	}}}
	commands[0].Options = append(commands[0].Options, ControlOptions()...)
	for _, option := range commands[0].Options {
		for _, field := range option.Options {
			if field.Name == "agent" {
				field.Autocomplete = true
			}
		}
	}
	commands = append(commands, MessageCommands()...)
	for _, cmd := range commands {
		var result discordgo.ApplicationCommand
		if err := b.Client.REST(ctx, http.MethodPost, "/applications/"+b.Service.Config.ApplicationID+"/commands", cmd, &result); err != nil {
			return err
		}
	}
	return nil
}
func (b *Bridge) notify(ctx context.Context, route Route, text string, actions []Action) {
	_, _ = b.Service.Messenger.Post(ctx, Outbound{Route: route, Text: text, Actions: actions})
}
func (b *Bridge) Message(ctx context.Context, msg *discordgo.MessageCreate) {
	if msg == nil || msg.Message == nil || msg.Author == nil {
		return
	}
	in := Input{EventID: msg.ID, Actor: Actor{ID: msg.Author.ID, Name: msg.Author.Username}, Route: Route{GuildID: msg.GuildID, ChannelID: msg.ChannelID, MessageID: msg.ID}, Text: msg.Content, Bot: msg.Author.Bot || msg.WebhookID != "", At: msg.Timestamp}
	if !b.Service.AllowedIdentity(in.Actor, in.Route.GuildID) {
		return
	}
	checked, scopeErr := b.resolveRoute(ctx, in.Route)
	if scopeErr != nil {
		return
	}
	in.Route = checked
	if !b.Service.Allowed(in.Actor, in.Route) {
		return
	}
	in.Addressed = msg.GuildID == ""
	for _, u := range msg.Mentions {
		if u.ID == b.Service.Config.ApplicationID {
			in.Addressed = true
		}
	}
	if in.Bot {
		_, _ = b.Service.Handle(ctx, in)
		return
	}
	if !in.Addressed {
		_, _ = b.Service.Handle(ctx, in)
		return
	}
	// A replay keeps its original route even if the roster changed since reception.
	if prior, e := b.Service.Store.ExistingEvent(in.EventID, b.Service.Now()); e == nil {
		in.TargetID = prior.Target.ID
		r, err := b.Service.Handle(ctx, in)
		if err == nil {
			b.accepted(ctx, r)
		}
		return
	} else if !errors.Is(e, ErrNotFound) {
		return
	}
	if _, e := b.Service.Store.ExistingEvent("routing:"+in.EventID, b.Service.Now()); e == nil || !errors.Is(e, ErrNotFound) {
		return
	}
	rows, err := b.Service.Roster(ctx)
	if err != nil {
		b.notify(ctx, in.Route, "Swarm is unavailable: "+err.Error(), nil)
		return
	}
	text := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(in.Text, "<@"+b.Service.Config.ApplicationID+">", ""), "<@!"+b.Service.Config.ApplicationID+">", ""))
	for _, row := range rows {
		if text == row.ID || strings.HasPrefix(text, row.ID+" ") {
			in.TargetID = row.ID
			break
		}
	}
	if in.TargetID == "" {
		in.TargetID = explicitAgent(text, rows)
	}
	if in.TargetID == "" {
		if id, e := b.Service.boundTarget(in.Route.ChannelID); e == nil {
			in.TargetID = id
		}
	}
	if in.TargetID == "" && len(rows) == 1 {
		in.TargetID = rows[0].ID
	}
	if in.TargetID == "" {
		b.pick(ctx, in, rows)
		return
	}
	r, err := b.Service.Handle(ctx, in)
	if err != nil {
		b.notify(ctx, in.Route, err.Error(), nil)
		return
	}
	b.accepted(ctx, r)
}
func (b *Bridge) accepted(ctx context.Context, r Request) {
	var actions []Action
	if r.Authority == AuthorityExternal {
		actions = []Action{{ID: "endorse:" + r.Ref, Label: "Treat as my instruction"}}
	}
	_, _ = b.Service.Control(ctx, r.Ref, "accepted", Outbound{Route: r.Route, Text: "Submitted to " + r.Target.Name + " · " + b.Service.Config.Name + ".", Actions: actions})
}
func (b *Bridge) pick(ctx context.Context, in Input, rows []Worker) {
	authority := AuthorityExternal
	if in.Actor.ID == b.Service.Config.OwnerID {
		authority = AuthorityOwnerDirect
	}
	r, _, err := b.Service.Store.Admit(Request{EventID: "routing:" + in.EventID, MessageRevision: revision(in), EventAt: in.At, Author: in.Actor, RequesterID: in.Actor.ID, Authority: authority, Route: in.Route, Snapshot: in.Snapshot,
		Target: Target{ID: "routing", Instance: b.Service.Config.InstallationID, Name: "Agent selection"}, Text: in.Text, Context: []ContextMessage{{ID: in.Route.MessageID, Author: in.Actor, Text: in.Text, CreatedAt: in.At}}}, b.Service.Now())
	if err != nil {
		b.notify(ctx, in.Route, err.Error(), nil)
		return
	}
	var ids []string
	for i, row := range rows {
		if i == 25 {
			break
		}
		ids = append(ids, row.ID)
	}
	if len(ids) == 0 {
		b.notify(ctx, in.Route, "There are no agents in this Swarm yet.", nil)
		return
	}
	if err = b.Service.Propose(ctx, r.Ref, ids); err != nil {
		b.notify(ctx, in.Route, err.Error(), nil)
	}
	b.start(ctx, func() { _, _ = b.receptionist(ctx, r) })
}
func interactionActor(i *discordgo.InteractionCreate) Actor {
	var u *discordgo.User
	if i.Member != nil {
		u = i.Member.User
	} else {
		u = i.User
	}
	if u == nil {
		return Actor{}
	}
	return Actor{ID: u.ID, Name: u.Username}
}
func (b *Bridge) Interaction(ctx context.Context, i *discordgo.InteractionCreate) {
	if i == nil || i.Interaction == nil || i.AppID != b.Service.Config.ApplicationID {
		return
	}
	actor := interactionActor(i)
	route := Route{GuildID: i.GuildID, ChannelID: i.ChannelID, MessageID: i.ID}
	if !b.Service.AllowedIdentity(actor, route.GuildID) {
		return
	}
	if i.Type == discordgo.InteractionApplicationCommandAutocomplete {
		bounded, stop := context.WithTimeout(ctx, 2500*time.Millisecond)
		defer stop()
		if checked, err := b.resolveRoute(bounded, route); err == nil {
			_ = b.autocomplete(bounded, i, actor, checked)
		}
		return
	}
	ack := discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredChannelMessageWithSource, Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral}}
	ackCtx, stop := context.WithTimeout(ctx, 2500*time.Millisecond)
	err := b.Client.REST(ackCtx, http.MethodPost, "/interactions/"+i.ID+"/"+i.Token+"/callback", ack, nil)
	stop()
	if err != nil {
		return
	}
	ctx, stop = context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	checked, err := b.resolveRoute(ctx, route)
	message := ""
	if err == nil && !b.Service.Allowed(actor, checked) {
		err = ErrNotAuthorized
	}
	if err == nil {
		message, err = b.interactionAction(ctx, i, actor, checked)
	}
	if err != nil {
		message = err.Error()
	}
	payload := map[string]any{"content": message, "allowed_mentions": map[string]any{"parse": []string{}}}
	_ = b.Client.REST(ctx, http.MethodPatch, "/webhooks/"+i.AppID+"/"+i.Token+"/messages/@original", payload, nil)
}
func (b *Bridge) interactionAction(ctx context.Context, i *discordgo.InteractionCreate, actor Actor, route Route) (string, error) {
	if i.Type == discordgo.InteractionMessageComponent {
		data := i.MessageComponentData()
		kind, ref, _ := strings.Cut(data.CustomID, ":")
		switch kind {
		case "native":
			return b.nativeAction(ctx, i, actor, route)
		case "endorse":
			r, err := b.Service.Endorse(ctx, actor, ref, i.ID, b.Service.Now())
			if err != nil {
				return "", err
			}
			return "Sent as your instruction to " + r.Target.Name + ".", nil
		case "choose":
			if len(data.Values) != 1 {
				return "", ErrInvalid
			}
			r, err := b.Service.Store.Request(ref, b.Service.Now())
			if err != nil {
				return "", err
			}
			result, err := b.selectRequest(ctx, actor, r, data.Values[0], i.ID)
			if err != nil {
				return "", err
			}
			b.accepted(ctx, result)
			return "Message submitted.", nil
		}
		return "", ErrInvalid
	}
	if i.Type != discordgo.InteractionApplicationCommand {
		return "", ErrInvalid
	}
	d := i.ApplicationCommandData()
	if d.Name != "swarm" {
		return b.contextCommand(ctx, i, actor, route)
	}
	if len(d.Options) != 1 {
		return "", ErrInvalid
	}
	command := d.Options[0]
	get := func(name string) string {
		for _, o := range command.Options {
			if o.Name == name {
				v, _ := o.Value.(string)
				return v
			}
		}
		return ""
	}
	switch command.Name {
	case "agents", "status":
		rows, err := b.Service.Roster(ctx)
		if err != nil {
			return "", err
		}
		var text strings.Builder
		fmt.Fprintf(&text, "%s · %d agents\n", b.Service.Config.Name, len(rows))
		for n, row := range rows {
			if n == 20 {
				fmt.Fprintln(&text, "Use an agent ID to choose from the whole roster.")
				break
			}
			fmt.Fprintf(&text, "%s · %s · %s · %s\n", row.Name, row.Agent, row.State, row.ID)
		}
		return shortMessage(text.String()), nil
	case "leave":
		return "Stopped following this discussion.", b.Service.Leave(route.ChannelID)
	case "join":
		row, err := b.Service.target(ctx, get("agent"))
		if err != nil {
			return "", err
		}
		if err = b.Service.Join(route, row.Target); err != nil {
			return "", err
		}
		return row.Name + " is following this discussion and answers when addressed.", nil
	case "ask":
		in := Input{EventID: i.ID, Actor: actor, Route: route, Text: get("text"), TargetID: get("agent"), Addressed: true, At: b.Service.Now(), Snapshot: true}
		r, err := b.Service.Handle(ctx, in)
		if err != nil {
			return "", err
		}
		b.accepted(ctx, r)
		return "Message submitted.", nil
	}
	return b.controlCommand(ctx, i, actor, route, command)
}

func explicitAgent(text string, rows []Worker) string {
	best := 0
	id := ""
	ambiguous := false
	for _, row := range rows {
		name := row.Name
		if name == "" || len(text) < len(name) || !strings.EqualFold(text[:len(name)], name) {
			continue
		}
		if len(text) > len(name) && !strings.ContainsRune(" :\t\r\n", rune(text[len(name)])) {
			continue
		}
		if len(name) > best {
			best = len(name)
			id = row.ID
			ambiguous = false
		} else if len(name) == best && row.ID != id {
			ambiguous = true
		}
	}
	if ambiguous {
		return ""
	}
	return id
}
func shortMessage(s string) string {
	if prefix := clipUTF16(s, 1800); prefix != s {
		return prefix + "…"
	}
	return s
}
