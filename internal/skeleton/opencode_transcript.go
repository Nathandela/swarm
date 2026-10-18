package skeleton

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/opencode"
	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/persist"
)

const (
	maxNativeProjectionItems        = 256
	maxNativeProjectionFingerprints = 512
)

type nativeProjectionSeen struct {
	hash       [32]byte
	provenance string
	at         int64
	status     string
}

// The observer owns these bounded caches. Stable IDs do not require a lifetime ref map.
type openCodeTranscript struct {
	ready            atomic.Bool
	sinkQualified    atomic.Bool
	owner            *openCodeObservation
	d                *Daemon
	local, scope     string
	identity         opencodebridge.Identity
	seen             map[string]nativeProjectionSeen
	order            []string
	priorDescendants []string
	descendants      map[string]bool
}

func nativeProjectionScope(meta persist.Meta) (string, error) {
	if meta.ConversationID == "" || meta.OpenCodeBinding == nil || meta.OpenCodeBinding.NativeCreatedAtMS <= 0 {
		return "", errors.New("native projection has no durable root identity")
	}
	paths := make([]string, 0, len(meta.OpenCodeBinding.ProfileEnv)+1)
	for _, entry := range append(append([]string(nil), meta.OpenCodeBinding.ProfileEnv...), "directory="+meta.ProviderCwd()) {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !filepath.IsAbs(value) {
			return "", errors.New("native projection profile path is not absolute")
		}
		resolved, err := nativeProjectionPath(value)
		if err != nil {
			return "", err
		}
		paths = append(paths, key+"="+resolved)
	}
	sort.Strings(paths)
	raw, _ := json.Marshal(struct {
		Paths   []string
		Root    string
		Created int64
	}{paths, meta.ConversationID, meta.OpenCodeBinding.NativeCreatedAtMS})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Resolve existing ancestors too: an as-yet absent native config directory has a stable home.
func nativeProjectionPath(path string) (string, error) {
	path = filepath.Clean(path)
	tail := []string{}
	for {
		resolved, err := filepath.EvalSymlinks(path)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) || filepath.Dir(path) == path {
			return "", err
		}
		tail = append(tail, filepath.Base(path))
		path = filepath.Dir(path)
	}
}

func nativeProjectionID(scope, kind, ref string) string {
	sum := sha256.Sum256([]byte(scope + "\x00" + kind + "\x00" + ref))
	var id [16]byte
	copy(id[:], sum[:16])
	return encodeItemID(id)
}

func newOpenCodeTranscript(d *Daemon, meta persist.Meta, identity opencodebridge.Identity) (*openCodeTranscript, error) {
	if !openCodeMetaMatches(meta, identity) {
		return nil, errors.New("native transcript binding changed")
	}
	scope, err := nativeProjectionScope(meta)
	if err != nil {
		return nil, err
	}
	d.initInteractions()
	return &openCodeTranscript{d: d, local: meta.ID, scope: scope, identity: identity, seen: map[string]nativeProjectionSeen{}, descendants: map[string]bool{}}, nil
}

func (p *openCodeTranscript) current() bool {
	meta, ok := p.d.core.Get(p.local)
	return ok && openCodeMetaMatches(meta, p.identity)
}
func (p *openCodeTranscript) inScope(id string) bool {
	return id == p.identity.RootID || p.descendants[id] || slices.Contains(p.priorDescendants, id)
}

func (p *openCodeTranscript) history(rows []json.RawMessage, provenance string) error {
	type timedRow struct {
		at  int64
		id  string
		raw json.RawMessage
	}
	ordered := make([]timedRow, 0, len(rows))
	for _, raw := range rows {
		info, _, err := opencode.DecodeMessageRow(raw)
		if err != nil {
			return err
		}
		if !p.inScope(info.SessionID) {
			return errors.New("native history contains a foreign row")
		}
		ordered = append(ordered, timedRow{info.Time.Created, info.ID, raw})
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].at != ordered[j].at {
			return ordered[i].at < ordered[j].at
		}
		return ordered[i].id < ordered[j].id
	})
	for _, row := range ordered {
		if err := p.row(row.raw, provenance); err != nil {
			return err
		}
	}
	return nil
}

func (p *openCodeTranscript) row(row json.RawMessage, provenance string) error {
	info, _, err := opencode.DecodeMessageRow(row)
	if err != nil {
		return err
	}
	if info.Time.Created <= 0 {
		return errors.New("native transcript row has no creation time")
	}
	if !p.inScope(info.SessionID) {
		return errors.New("native transcript row is outside its verified scope")
	}
	for _, item := range opencode.NativeInteractions().Interactions(adapter.HookPayload{Event: opencode.NativeMessageRowEvent, Raw: row}) {
		if err := p.interaction(info, item, provenance, row); err != nil {
			return err
		}
	}
	return nil
}

func (p *openCodeTranscript) interaction(info opencode.Message, item adapter.Interaction, provenance string, row json.RawMessage) error {
	if err := item.Validate(); err != nil {
		return err
	}
	kind, fields := nativeSnapshotFields(p.identity.RootID, info, item)
	if fields == nil {
		return nil
	}
	if info.SessionID == p.identity.RootID && item.MessageRole == "user" && item.Source == adapter.SourceNative {
		input := OpenCodeInput{MessageID: info.ID}
		owned := p.d.ownedOpenCodeInputs(p.local, OpenCodeView{Root: p.identity.RootID, Generation: p.identity.BackendGeneration, Inputs: []OpenCodeInput{input}})
		if own, ok := owned[info.ID]; ok {
			if _, err := qualifyOpenCodeAdmission(own.operation, own.intent, row); err == nil {
				fields["source"] = adapter.SourceOwner
				if own.intent.Source == "phone" {
					fields["source"] = adapter.SourcePhone
				}
				if own.intent.Source == "supervisor" {
					fields["source"] = adapter.SourceDerived
				}
				fields["operation_id"] = own.operation
				fields["input_sha256"] = own.intent.TextHash
				if provenance == "imported" {
					provenance = "reconciled"
				}
			}
		}
	}
	return p.offer(kind, info.SessionID+":"+item.Ref, item.TurnRef, item.Status, time.UnixMilli(info.Time.Created), provenance, fields, false)
}

func (p *openCodeTranscript) offer(kind, ref, turn, state string, at time.Time, provenance string, fields map[string]any, authority bool) error {
	if p.owner != nil {
		p.d.openCode.mu.Lock()
		defer p.d.openCode.mu.Unlock()
		if p.d.openCode.closed || p.d.openCode.sessions[p.local] != p.owner {
			return errors.New("native projection observer was replaced")
		}
	}
	if !p.current() {
		return errors.New("native transcript binding changed")
	}
	id := nativeProjectionID(p.scope, kind, ref)
	old, exists := p.seen[id]
	if exists && (at.UnixMilli() < old.at || old.status != "" && old.status != adapter.StatusInProgress && state == adapter.StatusInProgress) {
		return nil
	}
	if exists {
		operation, _ := fields["operation_id"].(string)
		hash, _ := fields["input_sha256"].(string)
		reconciledUser := kind == "message_snapshot" && fields["role"] == "user" && operation != "" && hash != "" && provenance == "reconciled" && old.provenance == "imported"
		if !reconciledUser {
			provenance = old.provenance
		}
	}
	fields["provenance"] = provenance
	body, err := json.Marshal(struct {
		State  string
		Fields map[string]any
	}{state, fields})
	if err != nil {
		return err
	}
	hash := sha256.Sum256(body)
	if exists && hash == old.hash {
		p.touch(id)
		return nil
	}
	if at.IsZero() {
		at = time.Now()
	}
	envelope := daemon.InteractionItem{V: 2, ItemID: id, TS: at.UTC(), Kind: kind, Status: state}
	if turn != "" {
		envelope.TurnID = nativeProjectionID(p.scope, "input", turn)
	}
	var raw, full json.RawMessage
	if authority {
		raw, err = serializeItem(&envelope, fields)
		if len(raw) > daemon.MaxItemBytes {
			return errors.New("native request card exceeds journal bound")
		}
	} else {
		raw, full, err = fitItem(envelope, fields)
	}
	if err != nil {
		return err
	}
	if full != nil {
		p.d.itemMu.Lock()
		p.d.retainDetailLocked(p.local, id, full)
		p.d.itemMu.Unlock()
	}
	if err = p.d.items.OfferBounded(p.local, raw, maxNativeProjectionItems); err != nil {
		return err
	}
	p.touch(id)
	p.seen[id] = nativeProjectionSeen{hash: hash, provenance: provenance, at: at.UnixMilli(), status: state}
	for len(p.order) > maxNativeProjectionFingerprints {
		delete(p.seen, p.order[0])
		p.order = p.order[1:]
	}
	return nil
}

// Frequently reconciled outcomes must not be evicted by a stream of new prose.
func (p *openCodeTranscript) touch(id string) {
	for i, old := range p.order {
		if old == id {
			copy(p.order[i:], p.order[i+1:])
			p.order = p.order[:len(p.order)-1]
			break
		}
	}
	p.order = append(p.order, id)
}

func (p *openCodeTranscript) event(ctx context.Context, reader openCodeReader, event opencode.Event) (json.RawMessage, error) {
	var target, id string
	if event.MessageUpdated != nil {
		target, id = event.MessageUpdated.Info.SessionID, event.MessageUpdated.Info.ID
	}
	if event.MessagePartUpdated != nil {
		target, id = event.MessagePartUpdated.Part.SessionID, event.MessagePartUpdated.Part.MessageID
	}
	if !p.inScope(target) {
		return nil, nil
	}
	if target == p.identity.RootID {
		return p.readMessage(ctx, reader, id)
	}
	scoped, ok := reader.(interface {
		MessageForSession(context.Context, string, string) (json.RawMessage, error)
	})
	if !ok {
		return nil, errors.New("native descendant message reader is unavailable")
	}
	// Retained IDs select candidates only. Every read proves current ancestry in the bridge.
	row, err := scoped.MessageForSession(ctx, target, id)
	if err != nil {
		return nil, err
	}
	info, _, err := opencode.DecodeMessageRow(row)
	if err != nil || info.SessionID != target || info.ID != id {
		return nil, errors.New("native descendant message read returned another identity")
	}
	return row, nil
}

func (p *openCodeTranscript) view(view OpenCodeView) error {
	p.descendants = map[string]bool{}
	if view.CurrentScopeKnown {
		for _, child := range view.Descendants {
			p.descendants[child.SessionID] = true
			if i := slices.Index(p.priorDescendants, child.SessionID); i >= 0 {
				p.priorDescendants = slices.Delete(p.priorDescendants, i, i+1)
			}
			p.priorDescendants = append(p.priorDescendants, child.SessionID)
			if len(p.priorDescendants) > 64 {
				p.priorDescendants = p.priorDescendants[1:]
			}
		}
	}
	if err := p.requests(view); err != nil {
		return err
	}
	for _, input := range view.Inputs {
		if input.Outcome == OpenCodeOutcomeNone || input.Synthetic || !input.Classified {
			continue
		}
		fields := map[string]any{"outcome": input.Outcome, "native_input_id": input.MessageID}
		if input.ErrorName != "" {
			fields["error_name"] = input.ErrorName
		}
		if err := p.offer("input_outcome", input.MessageID, input.MessageID, string(input.Outcome), time.UnixMilli(input.OutcomeAt), input.OutcomeSource, fields, false); err != nil {
			return err
		}
	}
	return nil
}

func (p *openCodeTranscript) readMessage(ctx context.Context, reader openCodeReader, id string) (json.RawMessage, error) {
	row, err := reader.Message(ctx, id)
	if err != nil {
		return nil, err
	}
	info, _, err := opencode.DecodeMessageRow(row)
	if err != nil || info.ID != id || info.SessionID != p.identity.RootID {
		return nil, errors.New("native message read returned another identity")
	}
	return row, nil
}

func (p *openCodeTranscript) requests(view OpenCodeView) error {
	instance, ok := p.d.sessionInstance(p.local)
	if !ok {
		return errors.New("native transcript has no current session instance")
	}
	cards, err := p.d.openCodeRequestCards(p.local, view)
	complete := p.ready.Load() && view.CurrentScopeKnown && err == nil
	current := map[string]string{}
	if complete {
		for _, pending := range view.Pending {
			current[nativeProjectionID(p.scope, pending.Kind+"_request", pending.SessionID+":"+pending.ID)] = ""
		}
		for _, card := range cards {
			kind := card.Kind + "_request"
			id := nativeProjectionID(p.scope, kind, card.NativeSessionID+":"+card.RequestID)
			current[id] = card.RequestHash
		}
	}
	fields := map[string]any{"complete": complete, "session_instance": instance, "root_id": p.identity.RootID, "generation": p.identity.BackendGeneration, "requests": current}
	if offerErr := p.offer("request_set", p.identity.RootID, "", "", time.Now(), "reconciled", fields, true); offerErr != nil {
		return offerErr
	}
	if !complete {
		return nil
	}
	for _, card := range cards {
		fields := map[string]any{"authority_valid": true, "request": card}
		if err := p.offer(card.Kind+"_request", card.NativeSessionID+":"+card.RequestID, "", adapter.StatusInProgress, time.Now(), "reconciled", fields, true); err != nil {
			return err
		}
	}
	return nil
}

func (p *openCodeTranscript) qualifySink(ctx context.Context, reader openCodeReader) error {
	readyReader, ok := reader.(interface {
		Ready(context.Context) (opencodebridge.Ready, error)
	})
	if !ok {
		p.sinkQualified.Store(false)
		return errors.New("native bridge health reader unavailable")
	}
	readyCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ready, err := readyReader.Ready(readyCtx)
	healthy := err == nil && ready.Identity == p.identity && ready.NativeVersion == "1.19.3" && ready.Bound
	p.sinkQualified.Store(healthy && ready.Acknowledged && ready.Ready && ready.DraftGuardSupported)
	if !healthy {
		return errors.New("native bridge health is unconfirmed")
	}
	return nil
}

func nativeSnapshotFields(root string, info opencode.Message, item adapter.Interaction) (string, map[string]any) {
	kind := item.Kind
	fields := interactionFields(item)
	switch kind {
	case adapter.KindMessageSnapshot:
		fields["text"] = item.Text
		fields["role"] = item.MessageRole
		fields["source"] = item.Source
	case adapter.KindToolRun:
		kind = "tool_snapshot"
	case adapter.KindFileChange:
		kind = "file_snapshot"
	case adapter.KindPlanUpdate:
		kind = "plan_snapshot"
	default:
		return "", nil
	}
	fields["native_session_id"] = info.SessionID
	fields["native_root_id"] = root
	fields["native_message_id"] = info.ID
	return kind, fields
}
