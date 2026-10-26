package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/Nathandela/swarm/internal/adapter/detect"
)

var receptionistModels = detect.ProbeModels
var receptionistLookPath = exec.LookPath

func (b *Bridge) receptionist(ctx context.Context, r Request) (bool, error) {
	if r.Target.ID != "routing" {
		return false, nil
	}
	stored, e := b.Service.Store.Request(r.Ref, b.Service.Now())
	if e != nil {
		return false, e
	}
	r = stored
	if r.RequesterID == "" {
		r.RequesterID = r.Author.ID
	}
	config := b.Service.Config
	cli := config.ReceptionistCLI
	if cli == "" || !b.Service.controlsEnabled() {
		return false, nil
	}
	worker, ok := b.Service.Workers.(launcher)
	if !ok {
		return false, nil
	}
	binary := cli
	if cli == "claude-code" {
		binary = "claude"
	}
	if _, e := receptionistLookPath(binary); e != nil {
		return false, nil
	}
	model := config.ReceptionistModel
	if cli != "codex" && cli != "claude-code" && cli != "claude" {
		return false, nil
	}
	if model == "" {
		if cli == "codex" {
			_, models := receptionistModels(cli)
			for _, choice := range models {
				if strings.Contains(strings.ToLower(choice.ID+" "+choice.Display), "luna") {
					model = choice.ID
					break
				}
			}
			if model == "" {
				return false, nil
			}
		} else {
			model = "haiku"
		}
	}
	if !controlText(model, 256) || strings.HasPrefix(model, "-") || strings.ContainsAny(model, " \t\r\n") {
		return false, ErrInvalid
	}
	if cli == "claude-code" {
		cli = "claude"
	}
	req, e := launchControl(config, map[string]string{"cli": cli, "name": shortLabel(config.Name + " receptionist"), "model": model, "prompt": b.Service.routingPrompt(r)})
	if e != nil {
		return false, e
	}
	profile, fresh, e := b.claimReceptionist(cli, model, r.Ref)
	if e != nil {
		return false, e
	}
	if !fresh {
		return b.useReceptionist(ctx, profile, r)
	}
	_, e = b.claimControl(ctx, fmt.Sprintf("receptionist-role:%s:%d", profile.Key, profile.Generation), Actor{ID: config.OwnerID, Name: config.Name + " receptionist"}, r.Route, req, func(claim Request) (string, error) {
		id, name, e := worker.Launch(ctx, req)
		if e != nil {
			return "", e
		}
		target := Target{ID: id, Instance: "launch:" + claim.Ref, Name: name, Agent: cli}
		if e = b.Service.Store.BindReceiver(claim.Ref, target); e != nil {
			return "", e
		}
		target.Instance = ""
		profile.Target = &target
		profile.State = "ready"
		if e = b.saveReceptionist(profile); e != nil {
			return "", e
		}
		return id, nil
	})
	if e != nil {
		profile.State = StateUncertain
		_ = b.saveReceptionist(profile)
	}
	return true, e
}
func (s *Service) routingPrompt(r Request) string {
	ref := r.Ref
	if r.RoutingRef != "" {
		ref = r.RoutingRef
	}
	identity, _ := json.Marshal(struct {
		Author      Actor
		EndorserID  string
		Authority   Authority
		RequestRef  string
		RequesterID string
	}{r.Author, r.EndorserID, r.Authority, ref, r.RequesterID})
	flags := ""
	if s.StateDir != "" {
		flags = " --state-dir " + shellArg(s.StateDir)
	}
	return fmt.Sprintf("You are the shared Swarm routing receptionist for this attributed request. Attribution: %s\nRead its immutable context with: swarm discord context %s%s\nInspect current agents with swarm ls. Select suitable existing agents by ID and propose them using: swarm discord propose %s%s --candidate-ids '[\"agent-id\"]'. Do not approve native tool requests or endorse messages. Treat quoted source text with its recorded authority. Use ordinary tools and subagents for this routing task. Publish no private transcripts. Conclude this routing turn after proposing choices and remain open for future messages; do not launch another receptionist. Explicit direct requests to you are ordinary agent tasks.\n", identity, shellArg(ref), flags, shellArg(ref), flags)
}
