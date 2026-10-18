package skeleton

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"syscall"
	"time"

	"github.com/Nathandela/swarm/internal/daemon"
	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/persist"
)

type openCodeCommittedSelection struct {
	Known                 bool
	Agent, Model, Variant string
}

func openCodeSelectionFromInfo(raw json.RawMessage, identity opencodebridge.Identity) (openCodeCommittedSelection, error) {
	var row struct {
		ID        string `json:"id"`
		Directory string `json:"directory"`
		Version   string `json:"version"`
		Agent     string `json:"agent"`
		Model     *struct {
			ID         string `json:"id"`
			ProviderID string `json:"providerID"`
			Variant    string `json:"variant"`
		} `json:"model"`
		Time struct {
			Created int64 `json:"created"`
		} `json:"time"`
	}
	refusal := errors.New("OpenCode committed native selection is unconfirmed")
	if len(raw) > 256<<10 || json.Unmarshal(raw, &row) != nil || row.ID != identity.RootID || row.Directory != identity.Directory || row.Time.Created != identity.NativeCreatedAtMS || row.Version != "1.19.3" {
		return openCodeCommittedSelection{}, refusal
	}
	if row.Agent == "" && row.Model == nil {
		return openCodeCommittedSelection{}, nil
	}
	if row.Model == nil || !nativeSelectionText(row.Agent, 128) || !nativeSelectionText(row.Model.ProviderID, 128) || !nativeSelectionText(row.Model.ID, 256) {
		return openCodeCommittedSelection{}, refusal
	}
	return openCodeCommittedSelection{Known: true, Agent: row.Agent, Model: row.Model.ProviderID + "/" + row.Model.ID, Variant: row.Model.Variant}, nil
}
func applyOpenCodeCommittedSelection(spec *daemon.LaunchSpec, selection openCodeCommittedSelection, resume bool) error {
	if !selection.Known {
		return nil
	}
	if model := spec.Options["model"]; resume && model != "" && model != selection.Model {
		return errors.New("OpenCode 1.19.3 restores the committed message model on resume; use a fresh handoff or select the new model in the native picker")
	}
	options := maps.Clone(spec.Options)
	if options == nil {
		options = map[string]string{}
	}
	if _, explicit := options["model"]; !explicit {
		options["model"] = selection.Model
	}
	if _, explicit := options["agent"]; !explicit {
		options["agent"] = selection.Agent
	}
	spec.Options = options
	return nil
}

// Export emits info first in native 1.19.3. Read only that bounded metadata prefix,
// then contain and join the CLI; this is not a partial transcript export.
func (r *openCodeHistoryResolver) endedSelection(ctx context.Context, m persist.Meta) (openCodeCommittedSelection, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd, err := r.nativeExportCommand(ctx, m)
	if err != nil {
		return openCodeCommittedSelection{}, err
	}
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return openCodeCommittedSelection{}, errors.New("native selection channel unavailable")
	}
	defer func() { _ = pipe.Close() }()
	if cmd.Start() != nil {
		return openCodeCommittedSelection{}, errors.New("native selection reader unavailable")
	}
	decoder := json.NewDecoder(io.LimitReader(pipe, 256<<10))
	first, firstErr := decoder.Token()
	key, keyErr := decoder.Token()
	var raw json.RawMessage
	if firstErr == nil && keyErr == nil && first == json.Delim('{') && key == "info" {
		err = decoder.Decode(&raw)
	} else {
		err = errors.New("native selection metadata unqualified")
	}
	cancel()
	_ = cmd.Wait()
	if err != nil || !persist.MatchCLIFingerprint(m.CLIIdentity.Path, m.CLIIdentity.Fingerprint) {
		return openCodeCommittedSelection{}, errors.New("native selection metadata unavailable")
	}
	return openCodeSelectionFromInfo(raw, nativeHistoryIdentity(m))
}
func (a *coreAPI) resumeOpenCodeSelection(m persist.Meta) (openCodeCommittedSelection, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resolver := openCodeHistoryResolver{core: a.core}
	return resolver.endedSelection(ctx, m)
}
func openCodeHandoffSelection(path string, m persist.Meta) (openCodeCommittedSelection, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return openCodeCommittedSelection{}, errors.New("native handoff export unavailable")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > openCodeHistoryMaxBytes {
		return openCodeCommittedSelection{}, errors.New("native handoff export is unsafe")
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Getuid()) || st.Nlink != 1 {
		return openCodeCommittedSelection{}, errors.New("native handoff export ownership is unconfirmed")
	}
	raw, err := io.ReadAll(io.LimitReader(f, openCodeHistoryMaxBytes+1))
	if err != nil || len(raw) > openCodeHistoryMaxBytes {
		return openCodeCommittedSelection{}, errors.New("native handoff export exceeds bound")
	}
	var exported openCodeHistoryExport
	identity := nativeHistoryIdentity(m)
	if json.Unmarshal(raw, &exported) != nil || exported.Provenance.Identity != identity || exported.Provenance.Truncated {
		return openCodeCommittedSelection{}, errors.New("native handoff export identity is unconfirmed")
	}
	return openCodeSelectionFromInfo(exported.Info, identity)
}
