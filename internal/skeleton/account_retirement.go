package skeleton

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Nathandela/swarm/internal/accountcheck"
	"github.com/Nathandela/swarm/internal/accounts"
	"github.com/Nathandela/swarm/internal/enrollment"
	"github.com/Nathandela/swarm/internal/persist"
	"github.com/Nathandela/swarm/internal/processcontain"
	"github.com/Nathandela/swarm/internal/shim"
	"github.com/Nathandela/swarm/internal/status"
)

// Retirement uses the existing watcher writer and enrollment authority. The
// first scan never fences a healthy, pending, or uncertain writer. Only after a
// durable generation embargo does the second scan authorize credential deletion.
// Ended discussion metadata and native history are retained throughout.
func (m *accountRotationManager) stepRetirements() {
	if m == nil || m.d == nil || m.d.accounts == nil || m.w == nil || m.w.stateErr != nil || m.store == nil || m.w.stopping() {
		return
	}
	manager := m.d.accounts
	// Accounts may hold its mutex while waiting for a watcher owner operation.
	// Skip this sweep rather than reverse that lock order.
	if !manager.mu.TryLock() {
		return
	}
	defer manager.mu.Unlock()
	if manager.unavailable != nil || manager.store != m.store {
		return
	}
	registry, err := m.store.Snapshot()
	if err != nil {
		return
	}
	for _, account := range registry.Accounts {
		for _, generation := range account.Generations {
			if account.Lifecycle == accounts.LifecycleRetiring && generation.CredentialErasing {
				// Confirm a predecessor's exact visible fence, then renew both complete
				// scans below. No credential deletion follows an uncertain registry write.
				registry, err = m.store.Reconcile()
				if err != nil {
					return
				}
				break
			}
		}
	}
	ids := make([]string, 0, len(registry.Accounts))
	for id, account := range registry.Accounts {
		if account.Lifecycle == accounts.LifecycleRetiring {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		account := registry.Accounts[id]
		generations := make([]uint64, 0, len(account.Generations))
		for number, generation := range account.Generations {
			if !generation.CredentialErased {
				generations = append(generations, number)
			}
		}
		sort.Slice(generations, func(i, j int) bool { return generations[i] < generations[j] })
		for _, number := range generations {
			if m.w.stopping() {
				return
			}
			generation := account.Generations[number]
			binding := accounts.Binding{SchemaVersion: accounts.SchemaVersion, Provider: account.Provider, AccountID: id, CredentialGeneration: number, Identity: generation.Identity, ConfigurationGeneration: 1}
			version, err := m.retirementReferences(manager, binding, generation)
			if err != nil {
				continue
			}
			if !generation.CredentialErasing {
				registry, err = m.store.BeginCredentialErasure(registry.Revision, id, number, version)
				if err != nil {
					return
				}
				generation = registry.Accounts[id].Generations[number]
			}
			if _, err = m.retirementReferences(manager, binding, generation); err != nil {
				continue
			}
			registry, err = m.store.EraseCredentials(registry.Revision, id, number, accounts.ErasureProof{WritersStopped: true})
			if err != nil {
				registry, err = m.store.Snapshot()
				if err != nil {
					return
				}
				continue
			}
		}
	}
	manager.reconcileRetiredEnrollment()
	manager.consumeTerminalEnrollment()
}

func sameCredentialGeneration(a, b accounts.Binding) bool {
	return a.Provider == b.Provider && a.AccountID == b.AccountID && a.CredentialGeneration == b.CredentialGeneration
}

// No corrupt session or orphan custody record is silently skipped. Persist's
// ordinary roster Scan isolates corrupt rows for availability; erasure needs a
// complete inventory and therefore reads each possible custody row explicitly.
func (m *accountRotationManager) retirementMetas() ([]persist.Meta, error) {
	root, err := openAccountRecoveryRoot(m.w.stateDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	entries, err := dir.ReadDir(8193)
	if (err != nil && !errors.Is(err, io.EOF)) || len(entries) > 8192 {
		return nil, accounts.ErrInUse
	}
	metas := append([]persist.Meta(nil), m.w.list()...)
	for _, entry := range entries {
		if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		hasMeta, hasNative := false, false
		for _, name := range []string{"meta.json", shim.NativeProcessFile} {
			_, err := root.Lstat(filepath.Join(entry.Name(), name))
			if err == nil {
				if name == "meta.json" {
					hasMeta = true
				} else {
					hasNative = true
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, accounts.ErrInUse
			}
		}
		if !hasMeta && !hasNative {
			continue
		}
		if !hasMeta || !persist.ValidID(entry.Name()) || entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("retirement orphan custody: %w", accounts.ErrInUse)
		}
		raw, err := readAccountRecoveryFile(m.w.stateDir, filepath.Join(entry.Name(), "meta.json"))
		if err != nil || rejectDuplicateJSONKeys(raw) != nil {
			return nil, fmt.Errorf("retirement private meta read: %w (%v)", accounts.ErrInUse, err)
		}
		var meta persist.Meta
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&meta); err != nil {
			return nil, fmt.Errorf("retirement meta decode: %w (%v)", accounts.ErrInUse, err)
		}
		if dec.Decode(new(any)) != io.EOF || meta.ID != entry.Name() || meta.SchemaVersion > persist.ManagedSchemaVersion {
			return nil, fmt.Errorf("retirement meta version/identity: %w", accounts.ErrInUse)
		}
		if (meta.AccountBinding == nil && meta.SchemaVersion == persist.ManagedSchemaVersion) || (meta.AccountBinding != nil && (meta.SchemaVersion != persist.ManagedSchemaVersion || !validManagedBinding(*meta.AccountBinding) || meta.AccountBinding.Provider != meta.AgentType || !accountHex(meta.AccountProjectionRef, 64))) {
			return nil, accounts.ErrInUse
		}
		// A writer record with a removed managed binding must not disappear behind
		// apparently unmanaged metadata.
		if hasNative && meta.AccountBinding == nil {
			return nil, accounts.ErrInUse
		}
		metas = append(metas, meta)
	}
	return metas, nil
}

func (m *accountRotationManager) retirementReferences(manager *accountManager, binding accounts.Binding, generation accounts.Generation) (string, error) {
	// Retrying uses retained generation metadata, never ProfilePath or a cached
	// environment which would bypass the CredentialErasing execution embargo.
	if _, err := m.store.HistoryProfilePath(binding); err != nil {
		return "", err
	}
	metas, err := m.retirementMetas()
	if err != nil {
		return "", err
	}
	version := ""
	if generation.CredentialErasing {
		parts := strings.Split(generation.ErasureInventory, ":")
		if len(parts) != 3 || parts[0] != binding.Provider {
			return "", accounts.ErrIneligible
		}
		version = parts[1]
	}
	byID := make(map[string][]persist.Meta)
	for _, meta := range metas {
		byID[meta.ID] = append(byID[meta.ID], meta)
		if meta.AccountBinding == nil || !sameCredentialGeneration(*meta.AccountBinding, binding) {
			continue
		}
		if meta.Status.Process == status.ProcessRunning || verifyAccountWritersStopped(m.w.stateDir, meta) != nil {
			return "", accounts.ErrInUse
		}
		nativeVersion, ok := historyNativeVersion(meta)
		if !ok || (version != "" && version != nativeVersion) {
			return "", accounts.ErrIneligible
		}
		version = nativeVersion
	}
	durable, err := loadAuthWatchStateChecked(m.w.stateDir)
	if err != nil {
		return "", err
	}
	if err := syncAuthWatchStateDir(m.w.stateDir); err != nil {
		return "", err
	}
	for _, state := range []authWatchState{m.w.state, durable} {
		for _, record := range state.AccountRotations {
			if record.State == accountComplete || record.State == accountOwnerCanceled {
				continue
			}
			if sameCredentialGeneration(record.SourceBinding, binding) || (record.Destination != nil && sameCredentialGeneration(*record.Destination, binding)) {
				return "", accounts.ErrInUse
			}
		}
		for _, permit := range state.AccountHalfOpen {
			if !sameCredentialGeneration(permit.Stamp.Binding, binding) {
				continue
			}
			if permit.WorkerPID == 0 || !accountcheck.CustodyStopped(m.w.stateDir, processcontain.Identity{PID: permit.WorkerPID, StartTime: permit.WorkerStartTime}, permit.Stamp.Binding) {
				return "", accounts.ErrInUse
			}
		}
		// Legacy auth/CLI refresh obligations may still create a replacement from a
		// retained source. Unknown IDs are an incomplete reservation inventory.
		checkIDs := make(map[string]bool)
		for id, killed := range state.Killed {
			if killed {
				checkIDs[id] = true
			}
		}
		for _, ids := range state.Pending {
			for _, id := range ids {
				checkIDs[id] = true
			}
		}
		for id, child := range state.Candidates {
			checkIDs[id], checkIDs[child] = true, true
		}
		for id, rec := range state.CLI {
			if rec.State != cliRefreshComplete {
				checkIDs[id] = true
				if rec.ReplacementID != "" {
					checkIDs[rec.ReplacementID] = true
				}
			}
		}
		for id := range checkIDs {
			rows := byID[id]
			if len(rows) == 0 {
				return "", accounts.ErrInUse
			}
			for _, row := range rows {
				if row.AccountBinding != nil && sameCredentialGeneration(*row.AccountBinding, binding) {
					return "", accounts.ErrInUse
				}
			}
		}
	}
	if !accountcheck.WritersStoppedForBinding(m.w.stateDir, binding) {
		return "", accounts.ErrInUse
	}
	enrollmentVersion, err := manager.retirementEnrollment(binding, generation)
	if err != nil {
		return "", err
	}
	if enrollmentVersion != "" {
		if version != "" && version != enrollmentVersion {
			return "", accounts.ErrIneligible
		}
		version = enrollmentVersion
	}
	if version == "" {
		return "", accounts.ErrIneligible
	}
	return version, nil
}

// The enrollment intent is reread while accountManager.mu excludes new login,
// reauthentication, cancellation and admission. Physical job directories are
// inventoried as well, including jobs forgotten from the bounded UI journal.
func (m *accountManager) retirementEnrollment(binding accounts.Binding, generation accounts.Generation) (string, error) {
	jobs := m.jobs
	if raw, err := m.readJobs(); err == nil {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if rejectDuplicateJSONKeys(raw) != nil || dec.Decode(&jobs) != nil || dec.Decode(new(any)) != io.EOF || jobs.SchemaVersion != accountJobsSchema || jobs.Jobs == nil || len(jobs.Jobs) > 256 {
			return "", accounts.ErrInUse
		}
	} else if !errors.Is(err, os.ErrNotExist) || len(m.jobs.Jobs) != 0 {
		return "", accounts.ErrInUse
	}
	for _, inventory := range []accountJobs{m.jobs, jobs} {
		for _, job := range inventory.Jobs {
			if job.TargetAccountID == binding.AccountID && job.State != "admitted" && job.State != "cancelled" {
				return "", accounts.ErrInUse
			}
		}
	}
	jobsPath := filepath.Join(m.stateRoot, "accounts", "jobs")
	root, err := openAccountRecoveryRoot(jobsPath)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	dir, err := root.Open(".")
	if err != nil {
		return "", err
	}
	defer func() { _ = dir.Close() }()
	entries, err := dir.ReadDir(1025)
	if (err != nil && !errors.Is(err, io.EOF)) || len(entries) > 1024 {
		return "", accounts.ErrInUse
	}
	version := ""
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !persist.ValidID(entry.Name()) {
			return "", accounts.ErrInUse
		}
		if job, ok := m.jobs.Jobs[entry.Name()]; ok && job.CustodyConsumed {
			disk, found := jobs.Jobs[entry.Name()]
			if !found || !disk.CustodyConsumed || disk.Generation != job.Generation || (disk.State != "admitted" && disk.State != "cancelled") {
				return "", accounts.ErrInUse
			}
			continue
		}
		var cfg enrollment.Config
		if err := readAccountWriterProof(root, entry.Name(), enrollment.ConfigFile, &cfg); err != nil {
			if job, ok := m.jobs.Jobs[entry.Name()]; ok && job.Worker.PID == 0 && enrollment.NeverStarted(m.stateRoot, job.ID) {
				continue
			}
			return "", accounts.ErrInUse
		}
		if cfg.SchemaVersion != enrollment.SchemaVersion || cfg.JobID != entry.Name() || cfg.StateRoot != m.stateRoot || cfg.Generation == 0 {
			return "", accounts.ErrInUse
		}
		if cfg.CandidateProfileGeneration != generation.ProfileGeneration {
			continue
		}
		if cfg.Provider != binding.Provider {
			return "", accounts.ErrInUse
		}
		progress, err := enrollment.ReadProgress(m.stateRoot, cfg.JobID)
		if err != nil || progress.SchemaVersion != enrollment.SchemaVersion || progress.JobID != cfg.JobID || progress.Generation != cfg.Generation || !progress.WritersStopped || !progress.NativeWritersStopped || progress.Worker.PID <= 0 || progress.Worker.StartTime <= 0 || progress.Worker.Alive() || progress.Runner.Alive() {
			return "", accounts.ErrInUse
		}
		for _, child := range progress.Children {
			if child.Alive() {
				return "", accounts.ErrInUse
			}
		}
		for _, inventory := range []accountJobs{m.jobs, jobs} {
			if job, ok := inventory.Jobs[cfg.JobID]; ok && (job.State != "admitted" || job.AdmittedAccountID != binding.AccountID || job.Candidate.ProfileGeneration != generation.ProfileGeneration || job.Generation != cfg.Generation || job.Worker != progress.Worker) {
				return "", accounts.ErrInUse
			}
		}
		if version != "" && version != cfg.NativeVersion {
			return "", accounts.ErrIneligible
		}
		version = cfg.NativeVersion
	}
	return version, nil
}

// A terminal enrollment proof is consumed only behind its durable cancellation
// fence, and only after its candidate was deleted or every admitted credential
// generation owning that profile was erased. The consumed bit is written before
// removing the metadata directory, so a crash cannot manufacture unknown custody.
func (m *accountManager) consumeTerminalEnrollment() {
	registry, err := m.store.Snapshot()
	if err != nil {
		return
	}
	ids := make([]string, 0, len(m.jobs.Jobs))
	for id := range m.jobs.Jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		job := m.jobs.Jobs[id]
		if !persist.ValidID(job.ID) || job.ID != job.Candidate.ID || !accountHex(job.Candidate.ProfileGeneration, 32) || job.Generation == 0 {
			continue
		}
		if job.State != "admitted" && job.State != "cancelled" {
			continue
		}
		if !job.CustodyConsumed {
			if job.State == "admitted" {
				account, found := registry.Accounts[job.AdmittedAccountID]
				if !found || account.Lifecycle != accounts.LifecycleRetiring {
					continue
				}
				erased := false
				for _, generation := range account.Generations {
					if generation.ProfileGeneration == job.Candidate.ProfileGeneration {
						erased = generation.CredentialErased
					}
				}
				if !erased {
					continue
				}
			} else {
				// Cancellation's existing DiscardCandidate durably removed this stable
				// profile before writing the terminal journal state.
				if _, err := m.jobsRoot.Lstat(filepath.Join("profiles", job.Candidate.ProfileGeneration)); !errors.Is(err, os.ErrNotExist) {
					continue
				}
			}
			neverStarted := job.Worker.PID == 0 && enrollment.NeverStarted(m.stateRoot, job.ID)
			if !neverStarted {
				before, err := enrollment.ReadProgress(m.stateRoot, job.ID)
				if err != nil || before.Worker != job.Worker || !retirementEnrollmentStopped(before, job.ID, job.Generation) {
					continue
				}
				if enrollment.FenceCancel(m.stateRoot, job.ID, job.Generation) != nil {
					continue
				}
				progress, err := enrollment.ReadProgress(m.stateRoot, job.ID)
				if err != nil || progress.SchemaVersion != enrollment.SchemaVersion || progress.JobID != job.ID || progress.Generation != job.Generation || progress.Worker != job.Worker || progress.Worker.PID <= 0 || progress.Worker.StartTime <= 0 || !progress.WritersStopped || !progress.NativeWritersStopped || progress.Worker.Alive() || progress.Runner.Alive() {
					continue
				}
				live := false
				for _, child := range progress.Children {
					live = live || child.Alive()
				}
				if live {
					continue
				}
			}
			job.CustodyConsumed = true
			if m.saveJob(job) != nil {
				return
			}
		}
		root, err := openAccountRecoveryRoot(filepath.Join(m.stateRoot, "accounts", "jobs"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			continue
		}
		// Remove only the anchored exact terminal job; no profile or history path is
		// accepted here. A replaced alias remains an unresolved inventory entry.
		info, err := root.Lstat(job.ID)
		if err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			if err := root.RemoveAll(job.ID); err == nil {
				if err := syncNativeRetirementDir(root); err != nil {
					m.unavailable = accounts.ErrDurabilityUncertain
				}
			}
		}
		_ = root.Close()
		if m.unavailable != nil {
			return
		}
	}
}

func syncNativeRetirementDir(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}

// Never drop the journal's cleanup intent while a partially removed job directory
// still exists. Older zero-worker cancellations with no directory/profile are
// independently safe under the same singleton Start/cancel ordering.
func (m *accountManager) terminalEnrollmentAbsent(job accountJob) bool {
	if !persist.ValidID(job.ID) || !accountHex(job.Candidate.ProfileGeneration, 32) {
		return false
	}
	if _, err := m.jobsRoot.Lstat(filepath.Join("jobs", job.ID)); !errors.Is(err, os.ErrNotExist) {
		return false
	}
	if !job.CustodyConsumed {
		if job.State != "cancelled" || job.Worker.PID != 0 || !enrollment.NeverStarted(m.stateRoot, job.ID) {
			return false
		}
		if _, err := m.jobsRoot.Lstat(filepath.Join("profiles", job.Candidate.ProfileGeneration)); !errors.Is(err, os.ErrNotExist) {
			return false
		}
	}
	root, err := openAccountRecoveryRoot(filepath.Join(m.stateRoot, "accounts", "jobs"))
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	return syncNativeRetirementDir(root) == nil
}

// Older builds pruned terminal journal rows while leaving their exact native
// proof directories. Reconstruct only an erased admitted profile (or a fenced,
// stopped cancellation with no profile); unknown or partial custody stays held.
func (m *accountManager) reconcileRetiredEnrollment() {
	registry, err := m.store.Snapshot()
	if err != nil {
		return
	}
	root, err := openAccountRecoveryRoot(filepath.Join(m.stateRoot, "accounts", "jobs"))
	if err != nil {
		return
	}
	defer func() { _ = root.Close() }()
	dir, err := root.Open(".")
	if err != nil {
		return
	}
	defer func() { _ = dir.Close() }()
	entries, err := dir.ReadDir(1025)
	if (err != nil && !errors.Is(err, io.EOF)) || len(entries) > 1024 {
		return
	}
	for _, entry := range entries {
		if _, found := m.jobs.Jobs[entry.Name()]; found {
			continue
		}
		if len(m.jobs.Jobs) >= 256 || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !persist.ValidID(entry.Name()) {
			continue
		}
		var cfg enrollment.Config
		if readAccountWriterProof(root, entry.Name(), enrollment.ConfigFile, &cfg) != nil || cfg.SchemaVersion != enrollment.SchemaVersion || cfg.StateRoot != m.stateRoot || cfg.JobID != entry.Name() || cfg.CandidateID != cfg.JobID || cfg.Generation == 0 || !accountHex(cfg.CandidateProfileGeneration, 32) {
			continue
		}
		progress, err := enrollment.ReadProgress(m.stateRoot, cfg.JobID)
		if err != nil || progress.SchemaVersion != enrollment.SchemaVersion || progress.JobID != cfg.JobID || progress.Generation != cfg.Generation || !progress.WritersStopped || !progress.NativeWritersStopped || progress.Worker.PID <= 0 || progress.Worker.StartTime <= 0 || progress.Worker.Alive() || progress.Runner.Alive() {
			continue
		}
		live := false
		for _, child := range progress.Children {
			live = live || child.Alive()
		}
		if live {
			continue
		}
		job := accountJob{ID: cfg.JobID, Generation: cfg.Generation, Provider: cfg.Provider, Method: cfg.Method, Deadline: cfg.Deadline, Worker: progress.Worker, Candidate: accounts.Candidate{ID: cfg.CandidateID, Provider: cfg.Provider, Kind: accounts.KindNative, Source: accounts.SourceNativeLogin, ProfileGeneration: cfg.CandidateProfileGeneration}}
		for id, account := range registry.Accounts {
			for _, generation := range account.Generations {
				if generation.ProfileGeneration == cfg.CandidateProfileGeneration && generation.CredentialErased && account.Lifecycle == accounts.LifecycleRetiring && account.Provider == cfg.Provider {
					job.State, job.AdmittedAccountID = "admitted", id
					job.Candidate.Identity, job.Candidate.Verification = generation.Identity, generation.Verification
				}
			}
		}
		if job.State == "" {
			if progress.Phase != enrollment.PhaseCanceled {
				continue
			}
			if _, err := m.jobsRoot.Lstat(filepath.Join("profiles", cfg.CandidateProfileGeneration)); !errors.Is(err, os.ErrNotExist) {
				continue
			}
			job.State = "cancelled"
		}
		if enrollment.FenceCancel(m.stateRoot, cfg.JobID, cfg.Generation) != nil {
			continue
		}
		confirmed, err := enrollment.ReadProgress(m.stateRoot, cfg.JobID)
		if err != nil || confirmed.Worker != progress.Worker || !retirementEnrollmentStopped(confirmed, cfg.JobID, cfg.Generation) {
			continue
		}
		// The exact cancellation fence above disallows a stale config re-exec. The
		// durable terminal intent is consumed before any proof file is removed.
		job.CustodyConsumed = true
		if m.saveJob(job) != nil {
			return
		}
	}
}

func retirementEnrollmentStopped(progress enrollment.Progress, id string, generation uint64) bool {
	if progress.SchemaVersion != enrollment.SchemaVersion || progress.JobID != id || progress.Generation != generation || !progress.WritersStopped || !progress.NativeWritersStopped || progress.Worker.PID <= 0 || progress.Worker.StartTime <= 0 || progress.Worker.Alive() || progress.Runner.Alive() {
		return false
	}
	for _, child := range progress.Children {
		if child.Alive() {
			return false
		}
	}
	return true
}
