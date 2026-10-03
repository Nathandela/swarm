package skeleton

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/adapter"
	"github.com/Nathandela/swarm/internal/adapter/registry"
	"github.com/Nathandela/swarm/internal/persist"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

var errAccountHistoryUnsupported = errors.New("account rotation: native history layout or dependency is unsupported; use native recovery")
var errAccountHistoryConflict = errors.New("account rotation: destination history diverged or has another writer; source retained")

const accountHistoryMaxBytes = 512 << 20
const accountHistoryMaxFiles = 512

type accountHistoryFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type accountHistoryManifest struct {
	SchemaVersion  int                  `json:"schema_version"`
	Provider       string               `json:"provider"`
	NativeVersion  string               `json:"native_version"`
	ConversationID string               `json:"conversation_id"`
	Cwd            string               `json:"cwd"`
	Source         accounts.Binding     `json:"source"`
	Destination    accounts.Binding     `json:"destination"`
	Epoch          uint64               `json:"epoch"`
	PreviousHash   string               `json:"previous_hash,omitempty"`
	Transaction    string               `json:"transaction"`
	Files          []accountHistoryFile `json:"files"`
	PreviousFiles  []accountHistoryFile `json:"previous_files,omitempty"`
	SHA256         string               `json:"sha256"`
	Published      bool                 `json:"published"`
}

// Ownership epochs and previously published artifact hashes establish ancestry.
// Byte-prefix comparisons cannot establish ancestry after native compaction.
type accountHistoryOwnership struct {
	Epoch             uint64                          `json:"epoch"`
	CurrentProfile    string                          `json:"current_profile"`
	CommittedManifest string                          `json:"committed_manifest"`
	ProfileFiles      map[string][]accountHistoryFile `json:"profile_files"`
}

func validateAccountHistoryFiles(files []accountHistoryFile) error {
	if len(files) > accountHistoryMaxFiles {
		return errAccountHistoryUnsupported
	}
	seen := make(map[string]bool, len(files))
	var total int64
	for _, file := range files {
		if !filepath.IsLocal(file.Path) || filepath.Clean(file.Path) != file.Path || file.Path == "." || len(file.Path) > 4096 || !accountHex(file.SHA256, 64) || file.Bytes < 0 || file.Bytes > accountHistoryMaxBytes || seen[file.Path] {
			return errAccountHistoryUnsupported
		}
		seen[file.Path] = true
		total += file.Bytes
		if total > accountHistoryMaxBytes {
			return errAccountHistoryUnsupported
		}
	}
	return nil
}

func validateAccountManifest(manifest accountHistoryManifest) error {
	if manifest.SchemaVersion != 1 || !validManagedBinding(manifest.Source) || !validManagedBinding(manifest.Destination) || manifest.Source == manifest.Destination || manifest.Provider != manifest.Source.Provider || manifest.Provider != manifest.Destination.Provider || !adapter.IsCanonicalConversationID(manifest.ConversationID) || !filepath.IsAbs(manifest.Cwd) || manifest.Epoch == 0 || !accountHex(manifest.Transaction, 64) || !accountHex(manifest.SHA256, 64) || len(manifest.Files) == 0 || validateAccountHistoryFiles(manifest.Files) != nil || validateAccountHistoryFiles(manifest.PreviousFiles) != nil {
		return errAccountHistoryUnsupported
	}
	if manifest.Provider == accounts.ProviderCodex {
		if manifest.NativeVersion != "0.160.0" || len(manifest.Files) != 1 {
			return errAccountHistoryUnsupported
		}
		for _, file := range append(append([]accountHistoryFile(nil), manifest.Files...), manifest.PreviousFiles...) {
			if !strings.HasPrefix(file.Path, "sessions/") || !strings.HasSuffix(file.Path, ".jsonl") || !strings.Contains(filepath.Base(file.Path), manifest.ConversationID) {
				return errAccountHistoryUnsupported
			}
		}
	} else {
		if manifest.NativeVersion != "2.1.288" {
			return errAccountHistoryUnsupported
		}
		ad, found := registry.New(accounts.ProviderClaude)
		if !found {
			return errAccountHistoryUnsupported
		}
		layout, ok := adapter.AsTranscriptLayout(ad)
		if !ok {
			return errAccountHistoryUnsupported
		}
		primary := filepath.Join("projects", layout.ProjectDirName(manifest.Cwd), manifest.ConversationID)
		for _, file := range append(append([]accountHistoryFile(nil), manifest.Files...), manifest.PreviousFiles...) {
			if file.Path != primary+".jsonl" && !strings.HasPrefix(file.Path, primary+string(filepath.Separator)) {
				return errAccountHistoryUnsupported
			}
		}
	}
	return nil
}

func historyProfileKey(binding accounts.Binding) string {
	return binding.AccountID + ":" + fmtUint(binding.CredentialGeneration)
}

func historyNativeVersion(source persist.Meta) (string, bool) {
	if source.CLIIdentity == nil {
		return "", false
	}
	version := strings.TrimPrefix(source.CLIIdentity.Version, "v")
	return version, (source.AgentType == accounts.ProviderCodex && version == "0.160.0") || (source.AgentType == accounts.ProviderClaude && version == "2.1.288")
}

func historyOpenRoot(path string) (*os.Root, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errAccountHistoryUnsupported
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, errAccountHistoryUnsupported
	}
	after, err := root.Lstat(".")
	if err != nil || !os.SameFile(before, after) {
		_ = root.Close()
		return nil, errAccountHistoryUnsupported
	}
	return root, nil
}

func historyOpenDir(root *os.Root, path string, create bool) (*os.Root, error) {
	if !filepath.IsLocal(path) || filepath.Clean(path) != path {
		return nil, errAccountHistoryUnsupported
	}
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	if path == "." {
		return current, nil
	}
	for _, component := range strings.Split(path, string(filepath.Separator)) {
		if create {
			mkdirErr := current.Mkdir(component, 0o700)
			if mkdirErr == nil {
				if err := historySync(current); err != nil {
					_ = current.Close()
					return nil, accounts.ErrDurabilityUncertain
				}
			}
			if err := mkdirErr; err != nil && !errors.Is(err, os.ErrExist) {
				_ = current.Close()
				return nil, err
			}
		}
		before, err := current.Lstat(component)
		if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
			_ = current.Close()
			return nil, errAccountHistoryUnsupported
		}
		child, err := current.OpenRoot(component)
		if err != nil {
			_ = current.Close()
			return nil, err
		}
		after, err := child.Lstat(".")
		_ = current.Close()
		if err != nil || !os.SameFile(before, after) {
			_ = child.Close()
			return nil, errAccountHistoryUnsupported
		}
		current = child
	}
	return current, nil
}

func historyOpenFile(root *os.Root, path string) (*os.File, error) {
	parent, err := historyOpenDir(root, filepath.Dir(path), false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = parent.Close() }()
	before, err := parent.Lstat(filepath.Base(path))
	if err != nil || !before.Mode().IsRegular() {
		return nil, errAccountHistoryUnsupported
	}
	stat, ok := before.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) || stat.Nlink != 1 {
		return nil, errAccountHistoryUnsupported
	}
	f, err := parent.OpenFile(filepath.Base(path), os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, errAccountHistoryUnsupported
	}
	after, err := f.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		_ = f.Close()
		return nil, errAccountHistoryUnsupported
	}
	return f, nil
}

func historyHashFile(root *os.Root, path string) (accountHistoryFile, error) {
	f, err := historyOpenFile(root, path)
	if err != nil {
		return accountHistoryFile{}, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || info.Size() > accountHistoryMaxBytes {
		return accountHistoryFile{}, errAccountHistoryUnsupported
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, accountHistoryMaxBytes+1))
	if err != nil || n > accountHistoryMaxBytes || n != info.Size() {
		return accountHistoryFile{}, errAccountHistoryUnsupported
	}
	return accountHistoryFile{Path: path, SHA256: hex.EncodeToString(hash.Sum(nil)), Bytes: n}, nil
}

func historyInventory(root *os.Root, primary, provider string) ([]accountHistoryFile, error) {
	paths := []string{primary}
	// Claude 2.1.288 files attachments, tool-results and subagent rollouts under
	// the conversation UUID directory beside its JSONL. Never copy a project or
	// another conversation wholesale. Codex's characterized rollout is opaque.
	if provider == accounts.ProviderClaude {
		subtree := strings.TrimSuffix(primary, ".jsonl")
		info, err := root.Lstat(subtree)
		if err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return nil, errAccountHistoryUnsupported
			}
			var walk func(string, int) error
			walk = func(path string, depth int) error {
				if depth > 16 || len(paths) > accountHistoryMaxFiles {
					return errAccountHistoryUnsupported
				}
				dir, err := historyOpenDir(root, path, false)
				if err != nil {
					return err
				}
				defer func() { _ = dir.Close() }()
				f, err := dir.Open(".")
				if err != nil {
					return err
				}
				entries, err := f.ReadDir(accountHistoryMaxFiles + 1)
				_ = f.Close()
				if err != nil && err != io.EOF {
					return err
				}
				if len(entries) > accountHistoryMaxFiles {
					return errAccountHistoryUnsupported
				}
				for _, entry := range entries {
					child := filepath.Join(path, entry.Name())
					info, err := dir.Lstat(entry.Name())
					if err != nil {
						return err
					}
					if info.IsDir() {
						if err := walk(child, depth+1); err != nil {
							return err
						}
					} else if info.Mode().IsRegular() {
						paths = append(paths, child)
					} else {
						return errAccountHistoryUnsupported
					}
					if len(paths) > accountHistoryMaxFiles {
						return errAccountHistoryUnsupported
					}
				}
				return nil
			}
			if err := walk(subtree, 0); err != nil {
				return nil, err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, errAccountHistoryUnsupported
		}
	}
	sort.Strings(paths)
	files := make([]accountHistoryFile, 0, len(paths))
	total := int64(0)
	for _, path := range paths {
		file, err := historyHashFile(root, path)
		if err != nil {
			return nil, err
		}
		total += file.Bytes
		if total > accountHistoryMaxBytes {
			return nil, errAccountHistoryUnsupported
		}
		files = append(files, file)
	}
	return files, nil
}

func preflightAccountHistory(store *accounts.Store, resolver resumeHistoryResolver, source persist.Meta, destination accounts.Binding, ownership accountHistoryOwnership, incidentID string) (accountHistoryManifest, error) {
	if ownership.Epoch > 0 && (source.AccountBinding == nil || ownership.CurrentProfile != historyProfileKey(*source.AccountBinding)) {
		return accountHistoryManifest{}, errAccountHistoryConflict
	}
	version, supported := historyNativeVersion(source)
	if !supported || source.AccountBinding == nil || source.AccountBinding.Provider != destination.Provider || source.AgentType != destination.Provider || resolver == nil {
		return accountHistoryManifest{}, errAccountHistoryUnsupported
	}
	sourcePath, err := store.HistoryProfilePath(*source.AccountBinding)
	if err != nil {
		return accountHistoryManifest{}, err
	}
	destinationPath, err := store.ProfilePath(destination)
	if err != nil {
		return accountHistoryManifest{}, err
	}
	primary, outcome := resolver.LocateTranscript(source, source.ConversationID)
	if outcome != resumeHistoryFound {
		return accountHistoryManifest{}, errAccountHistoryUnsupported
	}
	relative, err := filepath.Rel(sourcePath, primary)
	if err != nil || !filepath.IsLocal(relative) {
		return accountHistoryManifest{}, errAccountHistoryUnsupported
	}
	sourceRoot, err := historyOpenRoot(sourcePath)
	if err != nil {
		return accountHistoryManifest{}, err
	}
	defer func() { _ = sourceRoot.Close() }()
	files, err := historyInventory(sourceRoot, relative, source.AgentType)
	if err != nil {
		return accountHistoryManifest{}, err
	}
	destRoot, err := historyOpenRoot(destinationPath)
	if err != nil {
		return accountHistoryManifest{}, err
	}
	defer func() { _ = destRoot.Close() }()
	prior := ownership.ProfileFiles[historyProfileKey(destination)]
	priorByPath := make(map[string]accountHistoryFile, len(prior))
	for _, file := range prior {
		priorByPath[file.Path] = file
	}
	for _, file := range files {
		if _, err := destRoot.Lstat(file.Path); err == nil {
			old, err := historyHashFile(destRoot, file.Path)
			expected, recorded := priorByPath[file.Path]
			if err != nil || !recorded || old.SHA256 != expected.SHA256 || old.Bytes != expected.Bytes {
				return accountHistoryManifest{}, errAccountHistoryConflict
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return accountHistoryManifest{}, errAccountHistoryConflict
		}
	}
	transaction := sha256.Sum256([]byte(incidentID + "\x00" + historyProfileKey(destination)))
	manifest := accountHistoryManifest{SchemaVersion: 1, Provider: source.AgentType, NativeVersion: version, ConversationID: source.ConversationID, Cwd: source.ProviderCwd(), Source: *source.AccountBinding, Destination: destination, Epoch: ownership.Epoch + 1, PreviousHash: ownership.CommittedManifest, Transaction: hex.EncodeToString(transaction[:]), Files: files, PreviousFiles: append([]accountHistoryFile(nil), prior...)}
	manifest.SHA256 = accountManifestHash(manifest)
	return manifest, nil
}

func accountManifestHash(manifest accountHistoryManifest) string {
	manifest.SHA256 = ""
	manifest.Published = false
	raw, _ := json.Marshal(manifest)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func historySync(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Sync()
}

func historyWrite(root *os.Root, path string, source io.Reader, expected accountHistoryFile) error {
	parent, err := historyOpenDir(root, filepath.Dir(path), true)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	name := filepath.Base(path)
	temporary := ".swarm-history-" + expected.SHA256
	f, err := parent.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if errors.Is(err, os.ErrExist) {
		if err := parent.Remove(temporary); err != nil {
			return err
		}
		f, err = parent.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	}
	if err != nil {
		return err
	}
	defer func() { _ = parent.Remove(temporary) }()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(source, expected.Bytes+1))
	if err == nil && (n != expected.Bytes || hex.EncodeToString(hash.Sum(nil)) != expected.SHA256) {
		err = errAccountHistoryConflict
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := parent.Rename(temporary, name); err != nil {
		return err
	}
	if err := historySync(parent); err != nil {
		return accounts.ErrDurabilityUncertain
	}
	return nil
}

// prepareAccountHistory copies only after the caller proves both native writer
// groups stopped. Source survives. Staged files and backups are private; the
// published manifest is last, and the coordinator keeps its fence until every
// manifest hash is rechecked immediately before successor exec.
func prepareAccountHistory(stateRoot string, store *accounts.Store, manifest accountHistoryManifest) (accountHistoryManifest, error) {
	return prepareAccountHistoryWithHook(stateRoot, store, manifest, nil)
}

func prepareAccountHistoryWithHook(stateRoot string, store *accounts.Store, manifest accountHistoryManifest, afterPublish func(string) error) (accountHistoryManifest, error) {
	if validateAccountManifest(manifest) != nil || manifest.SHA256 != accountManifestHash(manifest) {
		return manifest, errAccountHistoryUnsupported
	}
	sourcePath, err := store.HistoryProfilePath(manifest.Source)
	if err != nil {
		return manifest, err
	}
	destinationPath, err := store.ProfilePath(manifest.Destination)
	if err != nil {
		return manifest, err
	}
	source, err := historyOpenRoot(sourcePath)
	if err != nil {
		return manifest, err
	}
	defer func() { _ = source.Close() }()
	destination, err := historyOpenRoot(destinationPath)
	if err != nil {
		return manifest, err
	}
	defer func() { _ = destination.Close() }()
	anchor, err := historyOpenRoot(filepath.Join(stateRoot, "accounts", "history-transfer"))
	if err != nil {
		return manifest, err
	}
	defer func() { _ = anchor.Close() }()
	staging, err := historyOpenDir(anchor, manifest.Transaction, true)
	if err != nil {
		return manifest, err
	}
	defer func() { _ = staging.Close() }()
	prior := make(map[string]accountHistoryFile, len(manifest.PreviousFiles))
	planned := make(map[string]accountHistoryFile, len(manifest.Files))
	for _, file := range manifest.PreviousFiles {
		prior[file.Path] = file
	}
	for _, file := range manifest.Files {
		planned[file.Path] = file
	}
	// Stage all new bytes before publishing any artifact.
	for _, file := range manifest.Files {
		current, err := historyHashFile(source, file.Path)
		if err != nil || current != file {
			return manifest, errAccountHistoryConflict
		}
		f, err := historyOpenFile(source, file.Path)
		if err != nil {
			return manifest, err
		}
		err = historyWrite(staging, filepath.Join("staged", file.Path), f, file)
		_ = f.Close()
		if err != nil {
			return manifest, err
		}
	}
	// Every old artifact has one immutable, verified original backup. A retry
	// accepts published new bytes only when this backup proves their ancestry.
	for _, old := range manifest.PreviousFiles {
		backupPath := filepath.Join("backup", old.Path)
		backup, backupErr := historyHashFile(staging, backupPath)
		if backupErr == nil {
			if backup.Path != backupPath || backup.SHA256 != old.SHA256 || backup.Bytes != old.Bytes {
				return manifest, errAccountHistoryConflict
			}
			continue
		}
		if _, err := staging.Lstat(backupPath); err == nil || !errors.Is(err, os.ErrNotExist) {
			return manifest, errAccountHistoryConflict
		}
		current, err := historyHashFile(destination, old.Path)
		if err != nil || current != old {
			return manifest, errAccountHistoryConflict
		}
		f, err := historyOpenFile(destination, old.Path)
		if err != nil {
			return manifest, err
		}
		err = historyWrite(staging, backupPath, f, old)
		_ = f.Close()
		if err != nil {
			return manifest, err
		}
	}
	for _, file := range manifest.Files {
		if _, err := destination.Lstat(file.Path); err == nil {
			current, err := historyHashFile(destination, file.Path)
			if err != nil {
				return manifest, err
			}
			old, hadOld := prior[file.Path]
			if current != file && (!hadOld || current != old) {
				return manifest, errAccountHistoryConflict
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return manifest, errAccountHistoryConflict
		}
	}
	for _, file := range manifest.Files {
		f, err := historyOpenFile(staging, filepath.Join("staged", file.Path))
		if err != nil {
			return manifest, err
		}
		err = historyWrite(destination, file.Path, f, file)
		_ = f.Close()
		if err != nil {
			return manifest, err
		}
		if afterPublish != nil {
			if err := afterPublish(file.Path); err != nil {
				return manifest, err
			}
		}
	}
	// Remove only old, manifested conversation artifacts. Their backup remains
	// in the private transaction directory; unrelated paths never enter this set.
	for _, old := range manifest.PreviousFiles {
		if _, keep := planned[old.Path]; keep {
			continue
		}
		if _, err := destination.Lstat(old.Path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		current, err := historyHashFile(destination, old.Path)
		if err != nil || current != old {
			return manifest, errAccountHistoryConflict
		}
		parent, err := historyOpenDir(destination, filepath.Dir(old.Path), false)
		if err != nil {
			return manifest, err
		}
		err = parent.Remove(filepath.Base(old.Path))
		if err == nil {
			err = historySync(parent)
		}
		_ = parent.Close()
		if err != nil {
			return manifest, err
		}
	}
	manifest.Published = true
	raw, err := json.Marshal(manifest)
	if err != nil {
		return manifest, err
	}
	sum := sha256.Sum256(raw)
	metadata := accountHistoryFile{Path: "manifest.json", SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(raw))}
	if err := historyWrite(staging, "manifest.json", strings.NewReader(string(raw)), metadata); err != nil {
		return manifest, err
	}
	if err := historySync(anchor); err != nil {
		return manifest, accounts.ErrDurabilityUncertain
	}
	if err := verifyAccountHistory(store, manifest); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func verifyAccountHistory(store *accounts.Store, manifest accountHistoryManifest) error {
	if !manifest.Published || validateAccountManifest(manifest) != nil || manifest.SHA256 != accountManifestHash(manifest) {
		return errAccountHistoryUnsupported
	}
	path, err := store.ProfilePath(manifest.Destination)
	if err != nil {
		return err
	}
	root, err := historyOpenRoot(path)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, file := range manifest.Files {
		observed, err := historyHashFile(root, file.Path)
		if err != nil || observed.SHA256 != file.SHA256 || observed.Bytes != file.Bytes {
			return errAccountHistoryConflict
		}
	}
	planned := make(map[string]bool, len(manifest.Files))
	for _, file := range manifest.Files {
		planned[file.Path] = true
	}
	for _, old := range manifest.PreviousFiles {
		if planned[old.Path] {
			continue
		}
		if _, err := root.Lstat(old.Path); !errors.Is(err, os.ErrNotExist) {
			return errAccountHistoryConflict
		}
	}
	return nil
}

func fmtUint(value uint64) string { return strconv.FormatUint(value, 10) }

// Managed history is rooted at the frozen native generation, including after
// credentials are erased. An invalid binding never falls back to global HOME.
type accountResumeHistoryResolver struct {
	stateRoot string
	unmanaged resumeHistoryResolver
}

func newAccountResumeHistoryResolver(stateRoot string, unmanaged resumeHistoryResolver) resumeHistoryResolver {
	return &accountResumeHistoryResolver{stateRoot: stateRoot, unmanaged: unmanaged}
}

func (r *accountResumeHistoryResolver) selected(m persist.Meta) (resumeHistoryResolver, bool) {
	if m.AccountBinding == nil {
		return r.unmanaged, r.unmanaged != nil
	}
	if m.AccountBinding.Provider != m.AgentType {
		return nil, false
	}
	store, err := accounts.OpenReadOnly(r.stateRoot)
	if err != nil {
		return nil, false
	}
	defer func() { _ = store.Close() }()
	profile, err := store.HistoryProfilePath(*m.AccountBinding)
	if err != nil {
		return nil, false
	}
	selected := newFilesystemResumeHistoryResolver(profile, defaultResumeHistoryLimits)
	selected.privateProvider = m.AgentType
	return selected, true
}

func (r *accountResumeHistoryResolver) Resolve(m persist.Meta) resumeHistoryResult {
	selected, ok := r.selected(m)
	if !ok {
		return resumeHistoryResult{Outcome: resumeHistoryUnsafe}
	}
	return selected.Resolve(m)
}

func (r *accountResumeHistoryResolver) LocateTranscript(m persist.Meta, conversationID string) (string, resumeHistoryOutcome) {
	selected, ok := r.selected(m)
	if !ok {
		return "", resumeHistoryUnsafe
	}
	return selected.LocateTranscript(m, conversationID)
}
