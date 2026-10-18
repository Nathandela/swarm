package skeleton

import (
	"crypto/sha256"
	"encoding/json"
	"strings"
	"unicode"

	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/protocol"
	"github.com/Nathandela/swarm/internal/protocol/schema"
)

const maxOpenCodeProjectionBytes = 16 << 10

type openCodeObservationFunc func(string) (protocol.OpenCodeObservationView, bool)

func (a *coreAPI) OpenCodeObservation(local string) (protocol.OpenCodeObservationView, bool) {
	if source := a.openCodeObservationFn.Load(); source != nil {
		return (*source)(local)
	}
	return protocol.OpenCodeObservationView{}, false
}

func (a *coreAPI) openCodeObservationHash(local string) [32]byte {
	view, ok := a.OpenCodeObservation(local)
	if !ok {
		return [32]byte{}
	}
	raw, _ := json.Marshal(view)
	return sha256.Sum256(raw)
}

func (d *Daemon) openCodeObservation(local string) (protocol.OpenCodeObservationView, bool) {
	if d.core == nil {
		return protocol.OpenCodeObservationView{}, false
	}
	meta, ok := d.core.Get(local)
	if !ok || meta.RequiredRuntime != persist.RequiredRuntimeOpenCode || meta.OpenCodeBinding == nil {
		return protocol.OpenCodeObservationView{}, false
	}
	d.openCode.mu.Lock()
	defer d.openCode.mu.Unlock()
	observation := d.openCode.sessions[local]
	if d.openCode.closed || observation == nil || observation.view.Epoch == 0 || !openCodeMetaMatches(meta, observation.identity) {
		return openCodeProjection(meta, OpenCodeView{}, false), true
	}
	return openCodeProjection(meta, observation.view, true), true
}

func openCodeProjection(meta persist.Meta, view OpenCodeView, observed bool) protocol.OpenCodeObservationView {
	out := protocol.OpenCodeObservationView{Version: 1, RootID: meta.ConversationID, Generation: meta.OpenCodeBinding.Generation, Phase: meta.OpenCodeBinding.Phase}
	if !observed {
		out.Degraded = true
		out.Reason = "native observation unavailable"
		return out
	}
	text := func(value string, limit int) string {
		clean := ocPublicText(value, limit)
		if clean != value {
			out.Truncated = true
		}
		return clean
	}
	out.Epoch = view.Epoch
	out.Busy, out.BusyKnown, out.RootBusy, out.RootBusyKnown = view.Busy, view.BusyKnown, view.RootBusy, view.RootBusyKnown
	out.CurrentScopeKnown, out.Attention, out.Degraded = view.CurrentScopeKnown, view.Attention, view.Degraded
	out.Reason = text(view.DegradedReason, 256)
	if view.Error != nil {
		out.Error = text(view.Error.Name, 128)
	}
	if view.Retry != nil {
		out.RetryAttempt = view.Retry.Attempt
		out.RetryAtMS = view.Retry.Next
	}
	if view.Current != nil {
		out.CurrentInputID = text(view.Current.MessageID, 128)
	}
	out.InputCount, out.PendingCount, out.DescendantCount = len(view.Inputs), len(view.Pending), len(view.Descendants)
	for _, input := range view.Inputs[max(0, len(view.Inputs)-4):] {
		item := schema.OpenCodeInputView{MessageID: text(input.MessageID, 128), AtMS: input.At, Text: text(input.Text, 256), Synthetic: input.Synthetic, Classified: input.Classified, Outcome: string(input.Outcome), OutcomeSource: input.OutcomeSource, Error: text(input.ErrorName, 128), Agent: text(input.Agent, 128), Variant: text(input.Variant, 128)}
		if input.ProviderID != "" && input.ModelID != "" {
			item.Model = text(input.ProviderID+"/"+input.ModelID, 256)
		}
		out.Inputs = append(out.Inputs, item)
	}
	for _, request := range view.Pending[:min(8, len(view.Pending))] {
		summary := request.Permission
		if request.Kind == "question" && len(request.Questions) > 0 {
			summary = request.Questions[0].Header + ": " + request.Questions[0].Question
		}
		out.Pending = append(out.Pending, schema.OpenCodeRequestView{Kind: request.Kind, RequestID: text(request.ID, 128), SessionID: text(request.SessionID, 128), Summary: text(summary, 256), QuestionCount: len(request.Questions)})
	}
	for _, child := range view.Descendants[:min(8, len(view.Descendants))] {
		state := child.Status.Type
		if state != "busy" && state != "idle" && state != "retry" {
			state = "unknown"
		}
		item := schema.OpenCodeDescendantView{SessionID: text(child.SessionID, 128), ParentID: text(child.ParentID, 128), Title: text(child.Title, 128), Status: state}
		if child.Error != nil {
			item.Error = text(child.Error.Name, 128)
		}
		out.Descendants = append(out.Descendants, item)
	}
	for {
		out.Truncated = out.Truncated || len(out.Inputs) < out.InputCount || len(out.Pending) < out.PendingCount || len(out.Descendants) < out.DescendantCount
		raw, _ := json.Marshal(out)
		if len(raw) <= maxOpenCodeProjectionBytes {
			return out
		}
		switch {
		case len(out.Descendants) > 0:
			out.Descendants = out.Descendants[:len(out.Descendants)-1]
		case len(out.Inputs) > 0:
			out.Inputs = out.Inputs[1:]
		case len(out.Pending) > 0:
			out.Pending = out.Pending[:len(out.Pending)-1]
		default:
			return out
		}
	}
}

func ocPublicText(value string, limit int) string {
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, value)
	return clampBytes(value, limit)
}
