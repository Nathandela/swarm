package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/opencodebridge"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/shim"
)

func (d *Daemon) prepareOpenCodeLaunch(spec *LaunchSpec, m *persist.Meta, dir string) error {
	if spec.RequiredRuntime == "" && spec.OpenCodeBinding == nil {
		return nil
	}
	if spec.RequiredRuntime != persist.RequiredRuntimeOpenCode || spec.AgentType != "opencode" || spec.AccountBinding != nil || spec.Backend != nil {
		return errors.New("OpenCode runtime: incompatible launch contract")
	}
	physicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return errors.New("OpenCode runtime: private session directory unavailable")
	}
	dir = physicalDir
	physicalCwd, err := filepath.EvalSymlinks(spec.Cwd)
	if err != nil {
		return errors.New("OpenCode runtime: native working directory unavailable")
	}
	if physicalCwd != spec.Cwd {
		spec.Cwd = physicalCwd
		m.AgentCwd = physicalCwd
	}
	nativeEnv, err := d.OpenCodeEnvironment(spec.OpenCodeEnv)
	if err != nil {
		return err
	}
	spec.OpenCodeEnv = nativeEnv
	var profile []string
	for _, entry := range nativeEnv {
		key, _, _ := strings.Cut(entry, "=")
		if persist.OpenCodeProfileKey(key) {
			profile = append(profile, entry)
		}
	}
	key, err := newHookToken()
	if err != nil {
		return err
	}
	b := &persist.OpenCodeBinding{Revision: 1, Mode: persist.OpenCodeModeCreate, Phase: persist.OpenCodePhasePlanned, Generation: key[:32], ProfileEnv: profile}
	if spec.ConversationID == "" {
		root, err := newHookToken()
		if err != nil {
			return err
		}
		spec.ConversationID = "ses_" + root[:32]
	} else {
		prior := spec.OpenCodeBinding
		if !adapter.IsOpenCodeConversationID(spec.ConversationID) || prior == nil || prior.NativeCreatedAtMS <= 0 || !slices.Equal(profile, prior.ProfileEnv) {
			return errors.New("OpenCode runtime: resume requires the exact durable native profile and creation identity")
		}
		b.Mode, b.NativeCreatedAtMS = persist.OpenCodeModeResume, prior.NativeCreatedAtMS
	}
	if len(spec.InitialPrompt) > 16<<10 || !utf8.ValidString(spec.InitialPrompt) || strings.ContainsRune(spec.InitialPrompt, 0) {
		return errors.New("OpenCode runtime: initial input exceeds the qualified text boundary")
	}
	if spec.InitialPrompt != "" {
		op, err := newHookToken()
		if err != nil {
			return err
		}
		hash := sha256.Sum256([]byte(spec.InitialPrompt))
		b.Initial = &persist.OpenCodeInitial{OperationID: op[:32], RequestHash: hex.EncodeToString(hash[:]), Phase: persist.OpenCodeInitialPrepared}
		body, _ := json.Marshal(opencodebridge.InitialRequest{Version: 1, SessionInstance: b.Generation, BackendGeneration: b.Generation, RootID: spec.ConversationID, OperationID: b.Initial.OperationID, RequestHash: b.Initial.RequestHash, Text: spec.InitialPrompt, ExpectedModel: spec.Options["model"], ExpectedAgent: spec.Options["agent"]})
		if len(body) > 64<<10 {
			return errors.New("OpenCode runtime: encoded initial input exceeds the bridge boundary")
		}
	}
	args := make([]string, 0, len(spec.Argv))
	for i := 0; i < len(spec.Argv); i++ {
		arg := spec.Argv[i]
		if i > 0 && (arg == "--prompt" || arg == "--session" || arg == "-s") {
			if i+1 >= len(spec.Argv) {
				return errors.New("OpenCode runtime: incomplete native startup argument")
			}
			value := spec.Argv[i+1]
			i++
			if arg == "--prompt" && value != spec.InitialPrompt || arg != "--prompt" && value != spec.ConversationID {
				return errors.New("OpenCode runtime: native startup identity or input mismatch")
			}
			continue
		}
		args = append(args, arg)
	}
	spec.Argv = args
	token, err := newHookToken()
	if err != nil {
		return err
	}
	embargo, err := newHookDrainToken()
	if err != nil {
		return err
	}
	bindingPath := filepath.Join(dir, shim.OpenCodeBindingFile)
	spec.OpenCodeLaunch = &shim.OpenCodeConfig{RootID: spec.ConversationID, Generation: b.Generation, BindingFile: bindingPath, EmbargoToken: embargo}
	if err := shim.ValidateOpenCodeConfig(shim.Config{SessionDir: dir, Argv: args, OpenCode: spec.OpenCodeLaunch}); err != nil {
		return err
	}
	if !persist.ValidOpenCodeBinding(*b) {
		return errors.New("OpenCode runtime: invalid planned identity")
	}
	binding := opencodebridge.Binding{Version: 1, WorkerID: m.ID, SessionInstance: b.Generation, RootID: spec.ConversationID, Directory: spec.Cwd, BackendGeneration: b.Generation,
		SocketPath: filepath.Join(dir, "oc.sock"), Token: token, Mode: b.Mode, NativeCreatedAtMS: b.NativeCreatedAtMS, InitialPrompt: b.Initial != nil}
	if len(binding.SocketPath) > 100 {
		return errors.New("OpenCode runtime: private socket path exceeds native limit")
	}
	pluginPath := filepath.Join(dir, "opencode-tui.mjs")
	pluginURL := (&url.URL{Scheme: "file", Path: pluginPath}).String()
	config := map[string]any{"plugin": []any{[]any{pluginURL, map[string]string{"bindingFile": bindingPath}}}}
	bindingJSON, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	configJSON, err := json.Marshal(config)
	if err != nil {
		return err
	}
	for _, file := range []struct {
		name string
		data []byte
	}{{shim.OpenCodeBindingFile, bindingJSON}, {"opencode-tui.mjs", opencodebridge.PluginSource}, {"opencode-tui.json", configJSON}} {
		if err := writeOpenCodeArtifact(filepath.Join(dir, file.name), file.data); err != nil {
			return errors.New("OpenCode runtime: could not create private launch artifacts")
		}
	}
	spec.OpenCodeBinding = persist.CloneOpenCodeBinding(b)
	m.ConversationID, m.RequiredRuntime, m.SchemaVersion = spec.ConversationID, persist.RequiredRuntimeOpenCode, persist.OpenCodeSchemaVersion
	m.OpenCodeBinding = persist.CloneOpenCodeBinding(b)
	return nil
}

func writeOpenCodeArtifact(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func mergeOpenCodeEnv(base, native []string) []string {
	keys := map[string]bool{}
	for _, entry := range native {
		key, _, _ := strings.Cut(entry, "=")
		keys[key] = true
	}
	out := make([]string, 0, len(base)+len(native))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if !keys[key] {
			out = append(out, entry)
		}
	}
	return append(out, native...)
}
