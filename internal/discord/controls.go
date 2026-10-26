package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/adapter/registry"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
	"github.com/bwmarrin/discordgo"
	bolt "go.etcd.io/bbolt"
)

var ErrControlUnsupported = errors.New("discord: this agent does not support structured native controls; use its local session")

type launcher interface {
	Launch(context.Context, protocol.LaunchReq) (string, string, error)
}
type resumer interface {
	Resume(context.Context, string) (string, string, error)
}
type stopper interface {
	Kill(context.Context, string) error
}
type nativeController interface {
	Requests(context.Context, protocol.NativeRequestsReq) (protocol.NativeRequestsReply, error)
	Decide(context.Context, string, protocol.NativeDecisionReq) (protocol.NativeDecisionReceipt, error)
}

func ControlOptions() []*discordgo.ApplicationCommandOption {
	stringOption := func(name, description string, required bool) *discordgo.ApplicationCommandOption {
		return &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionString, Name: name, Description: description, Required: required}
	}
	var options []*discordgo.ApplicationCommandOption
	options = append(options, &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionSubCommand, Name: "new", Description: "Create an agent (owner only)", Options: []*discordgo.ApplicationCommandOption{stringOption("cli", "claude, codex, agy, hermes or opencode", true), stringOption("name", "Agent name", false), stringOption("model", "Model ID", false), stringOption("dir", "Absolute working directory", false), stringOption("prompt", "Initial message", false)}})
	for _, name := range []string{"resume", "stop", "exclude", "include", "requests"} {
		options = append(options, &discordgo.ApplicationCommandOption{Type: discordgo.ApplicationCommandOptionSubCommand, Name: name, Description: name + " an exact agent ID (owner only)", Options: []*discordgo.ApplicationCommandOption{stringOption("agent", "Exact agent ID", true)}})
	}
	return options
}
func MessageCommands() []*discordgo.ApplicationCommand {
	return []*discordgo.ApplicationCommand{{Name: "Ask Swarm", Type: discordgo.MessageApplicationCommand}, {Name: "Treat as my instruction", Type: discordgo.MessageApplicationCommand}}
}
func (b *Bridge) owner(i *discordgo.InteractionCreate, actor Actor) error {
	if i == nil || i.Interaction == nil || i.AppID != b.Service.Config.ApplicationID || i.ID == "" || actor.ID != interactionActor(i).ID || actor.ID != b.Service.Config.OwnerID {
		return ErrNotAuthorized
	}
	return nil
}
func (s *Service) controlsEnabled() bool {
	return s.Config.Enabled && (s.StateDir == "" || NativeInputEnabled(s.StateDir))
}
func optionStrings(option *discordgo.ApplicationCommandInteractionDataOption, allowed ...string) (map[string]string, error) {
	values := make(map[string]string)
	for _, value := range option.Options {
		if !containsID(allowed, value.Name) {
			return nil, ErrInvalid
		}
		text, ok := value.Value.(string)
		if !ok || !utf8.ValidString(text) || strings.ContainsRune(text, 0) || len(text) > 16384 {
			return nil, ErrInvalid
		}
		if _, exists := values[value.Name]; exists {
			return nil, ErrInvalid
		}
		values[value.Name] = text
	}
	return values, nil
}
func controlText(value string, max int) bool {
	return len(value) <= max && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}
func launchControl(config Config, values map[string]string) (protocol.LaunchReq, error) {
	cli := values["cli"]
	if cli == "claude-code" {
		cli = "claude"
	}
	if !registry.IsProduction(cli) {
		return protocol.LaunchReq{}, ErrInvalid
	}
	dir := values["dir"]
	if dir == "" {
		dir = config.WorkDir
	}
	info, e := os.Stat(dir)
	if !filepath.IsAbs(dir) || e != nil || !info.IsDir() || !controlText(values["name"], 256) {
		return protocol.LaunchReq{}, ErrInvalid
	}
	model := values["model"]
	if !controlText(model, 256) || strings.HasPrefix(model, "-") || strings.IndexFunc(model, unicode.IsSpace) >= 0 {
		return protocol.LaunchReq{}, ErrInvalid
	}
	req := protocol.LaunchReq{Agent: cli, Name: values["name"], Cwd: dir, InitialPrompt: values["prompt"], Cols: 80, Rows: 24}
	if model != "" {
		req.Options = map[string]string{"model": model}
	}
	return req, nil
}

// claimControl persists the exact operation before any controller effect.
func (b *Bridge) claimControl(ctx context.Context, eventID string, actor Actor, route Route, payload any, effect func(Request) (string, error)) (string, error) {
	return b.claimControlTarget(ctx, eventID, actor, route, Target{ID: "control", Instance: b.Service.Config.InstallationID}, payload, effect)
}
func (b *Bridge) claimControlTarget(ctx context.Context, eventID string, actor Actor, route Route, target Target, payload any, effect func(Request) (string, error)) (string, error) {
	raw, e := json.Marshal(payload)
	if e != nil {
		return "", e
	}
	r, _, e := b.Service.Store.Admit(Request{EventID: "control:" + eventID, EventAt: b.Service.Now(), Author: actor, RequesterID: actor.ID, Authority: AuthorityOwnerDirect, Route: route, Target: target, Text: string(raw), Context: []ContextMessage{{ID: route.MessageID, Author: actor, Text: string(raw)}}, Snapshot: true}, b.Service.Now())
	if e != nil {
		return "", e
	}
	r, fresh, e := b.Service.Store.BeginDelivery(r.Ref, b.Service.Now())
	if e != nil {
		return "", e
	}
	if !fresh {
		if r.DeliveryState == StateSent {
			return r.NativeEvidence, nil
		}
		return "", ErrDeliveryUncertain
	}
	if !b.Service.controlsEnabled() {
		_ = b.Service.Store.FinishDelivery(r.Ref, StateFailed, "connector disabled")
		return "", ErrNotAuthorized
	}
	result, e := effect(r)
	state := StateSent
	evidence := result
	if e != nil {
		state = StateUncertain
		evidence = "controller outcome unconfirmed"
	}
	if saveErr := b.Service.Store.FinishDelivery(r.Ref, state, evidence); saveErr != nil {
		return "", saveErr
	}
	return result, e
}
func (b *Bridge) exactWorker(ctx context.Context, id string) (Worker, error) {
	rows, e := b.Service.Workers.Roster(ctx)
	if e != nil {
		return Worker{}, e
	}
	if e = followWorkerTargets(b.Service.Store, rows); e != nil {
		return Worker{}, e
	}
	for _, row := range rows {
		if row.ID == id {
			return row, nil
		}
	}
	return Worker{}, ErrNotFound
}
func (b *Bridge) controlCommand(ctx context.Context, i *discordgo.InteractionCreate, actor Actor, route Route, option *discordgo.ApplicationCommandInteractionDataOption) (string, error) {
	if e := b.owner(i, actor); e != nil {
		return "", e
	}
	if option == nil {
		return "", ErrInvalid
	}
	if option.Name == "new" {
		values, e := optionStrings(option, "cli", "name", "model", "dir", "prompt")
		if e != nil {
			return "", e
		}
		req, e := launchControl(b.Service.Config, values)
		if e != nil {
			return "", e
		}
		worker, ok := b.Service.Workers.(launcher)
		if !ok {
			return "", ErrControlUnsupported
		}
		return b.claimControl(ctx, i.ID, actor, route, req, func(r Request) (string, error) {
			launch := req
			launch.InitialPrompt = b.Service.envelope(r) + "Read the stored launch request's initial_prompt as your exact first task.\n"
			id, name, e := worker.Launch(ctx, launch)
			if e != nil {
				return "", e
			}
			if e = b.Service.Store.BindReceiver(r.Ref, Target{ID: id, Instance: "launch:" + r.Ref, Name: name, Agent: req.Agent}); e != nil {
				return "", e
			}
			return "Created " + name + " · " + id, e
		})
	}
	values, e := optionStrings(option, "agent")
	if e != nil {
		return "", e
	}
	if option.Name == "resume" || option.Name == "stop" {
		if previous, e := b.Service.Store.ExistingEvent("control:"+i.ID, b.Service.Now()); e == nil {
			var saved struct {
				Command string
				Target  Target
			}
			if json.Unmarshal([]byte(previous.Text), &saved) != nil || saved.Command != option.Name || saved.Target.ID != values["agent"] || previous.Author.ID != actor.ID || previous.Route != route {
				return "", ErrConflict
			}
			if previous.DeliveryState == StateSent {
				return previous.NativeEvidence, nil
			}
			return "", ErrDeliveryUncertain
		} else if !errors.Is(e, ErrNotFound) {
			return "", e
		}
	}
	row, e := b.exactWorker(ctx, values["agent"])
	if e != nil {
		return "", e
	}
	switch option.Name {
	case "exclude", "include":
		if e = b.Service.Store.SetOptOut(row.Target, option.Name == "exclude"); e != nil {
			return "", e
		}
		return option.Name + " · " + row.ID, nil
	case "requests":
		return b.ownerInbox(ctx, row)
	case "resume":
		worker, ok := b.Service.Workers.(resumer)
		if !ok {
			return "", ErrControlUnsupported
		}
		return b.claimControl(ctx, i.ID, actor, route, struct {
			Command string
			Target  Target
		}{option.Name, row.Target}, func(Request) (string, error) {
			id, name, e := worker.Resume(ctx, row.ID)
			return "Resumed " + name + " · " + id, e
		})
	case "stop":
		worker, ok := b.Service.Workers.(stopper)
		if !ok {
			return "", ErrControlUnsupported
		}
		return b.claimControl(ctx, i.ID, actor, route, struct {
			Command string
			Target  Target
		}{option.Name, row.Target}, func(Request) (string, error) { return "Stopped · " + row.ID, worker.Kill(ctx, row.ID) })
	}
	return "", ErrInvalid
}

func (b *Bridge) contextCommand(ctx context.Context, i *discordgo.InteractionCreate, actor Actor, route Route) (string, error) {
	if i == nil || i.Interaction == nil || i.AppID != b.Service.Config.ApplicationID || i.Type != discordgo.InteractionApplicationCommand || actor.ID == "" || actor.ID != interactionActor(i).ID || !b.Service.Allowed(actor, route) {
		return "", ErrNotAuthorized
	}
	data := i.ApplicationCommandData()
	endorse := data.Name == "Treat as my instruction"
	if endorse {
		if e := b.owner(i, actor); e != nil {
			return "", e
		}
	} else if data.Name != "Ask Swarm" {
		return "", ErrInvalid
	}
	if data.Resolved == nil {
		return "", ErrInvalid
	}
	source := data.Resolved.Messages[data.TargetID]
	if source == nil || source.Author == nil || source.ID != data.TargetID || (source.ChannelID != "" && source.ChannelID != route.ChannelID) {
		return "", ErrInvalid
	}
	route.MessageID = source.ID
	authority := AuthorityExternal
	endorser := ""
	if endorse {
		authority = AuthorityOwnerEndorsed
		endorser = actor.ID
	}
	eventID := "context:" + i.ID
	if endorse {
		eventID = "context-endorsement:" + digest([]string{route.GuildID, route.ChannelID, source.ID, source.Author.ID, source.Content, actor.ID})[:32]
	}
	r, _, e := b.Service.Store.Admit(Request{EventID: eventID, EventAt: b.Service.Now(), Author: Actor{ID: source.Author.ID, Name: source.Author.Username}, RequesterID: actor.ID, Authority: authority, EndorserID: endorser, Route: route, Target: Target{ID: "routing", Instance: b.Service.Config.InstallationID}, Text: source.Content, Snapshot: true, MessageRevision: digest([]string{source.ID, source.Author.ID, source.Content}), Context: []ContextMessage{{ID: source.ID, Author: Actor{ID: source.Author.ID, Name: source.Author.Username}, Text: source.Content, CreatedAt: source.Timestamp}}}, b.Service.Now())
	if e != nil {
		return "", e
	}
	rows, e := b.Service.Roster(ctx)
	if e != nil {
		return "", e
	}
	if len(rows) > 1 {
		if handled, e := b.receptionist(ctx, r); handled || e != nil {
			return "Swarm is finding suitable agents.", e
		}
	}
	var ids []string
	for _, row := range rows {
		if len(ids) == 25 {
			break
		}
		ids = append(ids, row.ID)
	}
	if len(ids) == 0 {
		return "There are no available agents.", nil
	}
	return "Choose an agent in the source discussion.", b.Service.Propose(ctx, r.Ref, ids)
}
func (b *Bridge) selectRequest(ctx context.Context, actor Actor, r Request, targetID, eventID string) (Request, error) {
	if actor.ID != r.Author.ID && actor.ID != r.RequesterID && actor.ID != b.Service.Config.OwnerID {
		return Request{}, ErrNotAuthorized
	}
	row, e := b.Service.target(ctx, targetID)
	if e != nil {
		return Request{}, e
	}
	r.Ref = ""
	r.OperationID = ""
	r.EventID = "selection:" + digest([]string{r.EventID, row.ID, row.Instance})[:32]
	r.EventAt = b.Service.Now()
	r.Target = row.Target
	return b.Service.dispatch(ctx, r)
}

type nativeCard struct {
	Ref       string                     `json:"ref"`
	OwnerID   string                     `json:"owner_id"`
	View      protocol.NativeRequestView `json:"view"`
	ExpiresAt time.Time                  `json:"expires_at"`
	State     string                     `json:"state"`
	ChannelID string                     `json:"channel_id,omitempty"`
}

func (b *Bridge) saveNativeCard(view protocol.NativeRequestView) (nativeCard, error) {
	now := b.Service.Now()
	if view.ExpiresAt == nil || !now.Before(*view.ExpiresAt) {
		return nativeCard{}, ErrExpired
	}
	raw, e := json.Marshal(view)
	if e != nil || len(raw) > schema.MaxNativeRequestViewBytes {
		return nativeCard{}, ErrCapacity
	}
	card := nativeCard{Ref: digest([]any{b.Service.Config.OwnerID, view})[:32], OwnerID: b.Service.Config.OwnerID, View: view, ExpiresAt: *view.ExpiresAt, State: StatePrepared}
	e = b.Service.Store.db.Update(func(tx *bolt.Tx) error {
		bucket, e := tx.CreateBucketIfNotExists([]byte("native_controls"))
		if e != nil {
			return e
		}
		var stale [][]byte
		if e = bucket.ForEach(func(k, v []byte) error {
			var old nativeCard
			if e := json.Unmarshal(v, &old); e != nil {
				return e
			}
			if now.After(old.ExpiresAt.Add(GuardTTL)) {
				stale = append(stale, append([]byte(nil), k...))
			}
			return nil
		}); e != nil {
			return e
		}
		for _, k := range stale {
			if e = bucket.Delete(k); e != nil {
				return e
			}
		}
		if old := bucket.Get([]byte(card.Ref)); old != nil {
			return json.Unmarshal(old, &card)
		}
		if bucket.Stats().KeyN >= 256 {
			return ErrCapacity
		}
		body, e := json.Marshal(card)
		if e != nil {
			return e
		}
		return bucket.Put([]byte(card.Ref), body)
	})
	return card, e
}
func (b *Bridge) loadNativeCard(ref string) (card nativeCard, err error) {
	err = b.Service.Store.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("native_controls"))
		if bucket == nil {
			return ErrNotFound
		}
		raw := bucket.Get([]byte(ref))
		if raw == nil {
			return ErrNotFound
		}
		return json.Unmarshal(raw, &card)
	})
	if err == nil && !b.Service.Now().Before(card.ExpiresAt) {
		return nativeCard{}, ErrExpired
	}
	return
}
func (b *Bridge) markNativeCard(card nativeCard, state string) error {
	card.State = state
	return b.Service.Store.db.Update(func(tx *bolt.Tx) error {
		raw, e := json.Marshal(card)
		if e != nil {
			return e
		}
		return tx.Bucket([]byte("native_controls")).Put([]byte(card.Ref), raw)
	})
}
func (b *Bridge) claimNativeCard(card nativeCard) (out nativeCard, fresh bool, err error) {
	err = b.Service.Store.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte("native_controls"))
		raw := bucket.Get([]byte(card.Ref))
		if raw == nil {
			return ErrNotFound
		}
		if e := json.Unmarshal(raw, &out); e != nil {
			return e
		}
		if !b.Service.Now().Before(out.ExpiresAt) {
			return ErrExpired
		}
		if out.State != StatePrepared {
			return nil
		}
		out.State = StateSending
		raw, e := json.Marshal(out)
		if e != nil {
			return e
		}
		if e = bucket.Put([]byte(card.Ref), raw); e != nil {
			return e
		}
		fresh = true
		return nil
	})
	return
}
func (b *Bridge) ownerInbox(ctx context.Context, row Worker) (string, error) {
	if excluded, e := b.Service.Store.IsOptedOut(row.Target); e != nil {
		return "", e
	} else if excluded {
		return "", ErrOptedOut
	}
	controller, ok := b.Service.Workers.(nativeController)
	if !ok {
		return "", ErrControlUnsupported
	}
	pending, e := controller.Requests(ctx, protocol.NativeRequestsReq{Session: row.ID})
	if e != nil {
		return "", ErrControlUnsupported
	}
	if pending.Reason != "" && len(pending.Requests) == 0 && pending.Request == nil {
		return "", ErrControlUnsupported
	}
	var views []protocol.NativeRequestView
	if pending.Request != nil {
		views = append(views, *pending.Request)
	}
	if len(pending.Requests) > 64 {
		return "", ErrCapacity
	}
	for _, summary := range pending.Requests {
		detail, e := controller.Requests(ctx, protocol.NativeRequestsReq{Session: row.ID, RequestID: summary.RequestID, NativeSessionID: summary.NativeSessionID})
		if e != nil {
			return "", e
		}
		if detail.Request != nil {
			views = append(views, *detail.Request)
		}
	}
	if len(views) == 0 {
		return "There are no pending structured native requests.", nil
	}
	for _, view := range views {
		if view.Session != row.ID || view.SessionInstance != row.Instance {
			return "", ErrConflict
		}
		card, e := b.saveNativeCard(view)
		if e != nil {
			return "", e
		}
		card, fresh, e := b.claimNativeCard(card)
		if e != nil {
			return "", e
		}
		if !fresh {
			if card.State != StateSent {
				return "Waiting for owner; native requests remain available locally.", nil
			}
			continue
		}
		var dm struct {
			ID string `json:"id"`
		}
		if e = b.Client.REST(ctx, http.MethodPost, "/users/@me/channels", map[string]string{"recipient_id": b.Service.Config.OwnerID}, &dm); e != nil {
			return "Waiting for owner; native requests remain available locally.", nil
		}
		if dm.ID == "" {
			return "Waiting for owner; native requests remain available locally.", nil
		}
		card.ChannelID = dm.ID
		if e = b.markNativeCard(card, StateSending); e != nil {
			return "", e
		}
		var actions []Action
		for _, decision := range view.Decisions {
			if decision.ID != "" && len(actions) < 5 {
				action := Action{ID: "native:" + card.Ref + ":" + decision.ID, Label: nativeLabel(decision.Label)}
				if decision.ID == "question_reply" {
					// shortcut: multi-question/free-form answers stay local; add modal capture when needed.
					if len(view.Questions) != 1 || view.Questions[0].Multiple || len(view.Questions[0].Options) == 0 || len(view.Questions[0].Options) > 25 {
						continue
					}
					for n, option := range view.Questions[0].Options {
						action.Choices = append(action.Choices, Choice{ID: strconv.Itoa(n), Label: shortLabel(option.Label)})
					}
				}
				actions = append(actions, action)
			}
		}
		details, _ := json.Marshal(view)
		text := "Native request for " + row.Name + " (separate from message endorsement):\n" + string(details)
		parts := splitUTF16(text, 1800)
		for n, part := range parts {
			out := Outbound{Route: Route{ChannelID: dm.ID}, Text: part, Nonce: digest([]any{card.Ref, n})[:24]}
			if n == len(parts)-1 {
				out.Actions = actions
			}
			if _, e = b.Service.Messenger.Post(ctx, out); e != nil {
				return "Waiting for owner; native requests remain available locally.", nil
			}
		}
		if e = b.markNativeCard(card, StateSent); e != nil {
			return "", e
		}
	}
	return "Pending native requests were sent to the owner's private inbox.", nil
}
func (b *Bridge) nativeAction(ctx context.Context, i *discordgo.InteractionCreate, actor Actor, route Route) (string, error) {
	if e := b.owner(i, actor); e != nil {
		return "", e
	}
	parts := strings.Split(i.MessageComponentData().CustomID, ":")
	if len(parts) != 3 || parts[0] != "native" {
		return "", ErrInvalid
	}
	card, e := b.loadNativeCard(parts[1])
	if e != nil {
		return "", e
	}
	if card.OwnerID != actor.ID {
		return "", ErrNotAuthorized
	}
	if route.GuildID != "" || route.ChannelID != card.ChannelID {
		return "", ErrNotAuthorized
	}
	controller, ok := b.Service.Workers.(nativeController)
	if !ok {
		return "", ErrControlUnsupported
	}
	current, e := controller.Requests(ctx, protocol.NativeRequestsReq{Session: card.View.Session, RequestID: card.View.RequestID, NativeSessionID: card.View.NativeSessionID})
	if e != nil || current.Request == nil {
		return "", ErrExpired
	}
	if digest(*current.Request) != digest(card.View) {
		return "", ErrConflict
	}
	allowed := false
	for _, decision := range card.View.Decisions {
		if decision.ID == parts[2] {
			allowed = true
		}
	}
	if !allowed {
		return "", ErrInvalid
	}
	view := card.View
	req := protocol.NativeDecisionReq{Session: view.Session, SessionInstance: view.SessionInstance, RootID: view.RootID, Generation: view.Generation, NativeSessionID: view.NativeSessionID, RequestID: view.RequestID, RequestHash: view.RequestHash, ExpiresAt: view.ExpiresAt, Kind: view.Kind, Reply: parts[2]}
	if view.Kind == "permission" {
		req.Kind = "permission_reply"
	}
	if view.Kind == "question" && parts[2] == "reject" {
		req.Kind = "question_reject"
		req.Reply = ""
	}
	if parts[2] == "question_reject" {
		req.Kind = "question_reject"
		req.Reply = ""
	}
	if parts[2] == "question_reply" {
		data := i.MessageComponentData()
		if len(data.Values) != 1 || len(view.Questions) != 1 || view.Questions[0].Multiple {
			return "", ErrControlUnsupported
		}
		index, e := strconv.Atoi(data.Values[0])
		if e != nil || index < 0 || index >= len(view.Questions[0].Options) {
			return "", ErrInvalid
		}
		req.Kind = "question_reply"
		req.Reply = ""
		req.Answers = [][]string{{view.Questions[0].Options[index].Label}}
	}
	if e = protocol.ValidateNativeDecision(req); e != nil {
		return "", ErrControlUnsupported
	}
	route.MessageID = card.Ref
	return b.claimControlTarget(ctx, "native:"+card.Ref, actor, route, Target{ID: view.Session, Instance: view.SessionInstance}, req, func(r Request) (string, error) {
		receipt, e := controller.Decide(ctx, r.OperationID, req)
		if e != nil {
			return "", e
		}
		if receipt.State != "admitted" {
			return "", ErrDeliveryUncertain
		}
		return "Native decision admitted; message endorsement was not changed.", nil
	})
}
func nativeLabel(label string) string {
	runes := []rune(label)
	if len(runes) > 80 {
		return string(runes[:80])
	}
	return label
}
func (b *Bridge) Attention(ctx context.Context) error {
	rows, e := b.Service.Roster(ctx)
	if e != nil {
		return e
	}
	for _, row := range rows {
		if row.Attention {
			if _, e = b.ownerInbox(ctx, row); e != nil && !errors.Is(e, ErrControlUnsupported) {
				return fmt.Errorf("discord: owner inbox unavailable")
			}
		}
	}
	return nil
}
