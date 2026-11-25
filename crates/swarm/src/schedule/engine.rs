use super::{
    Result, id,
    store::{Attempt, Completion, Occurrence, Store, digest, zero_time},
    workspace,
};
use jiff::Timestamp;
use std::{
    fs::{self, File},
    io::Read,
    path::{Path, PathBuf},
};
use subtle::ConstantTimeEq;

const STARTUP_REJECTED: &str = "Worker rejected startup before the opening prompt. Worker stop verified; check the provider login and installation.";
const QUOTA: &str = "Provider usage limit reached; worker stop verified.";

pub(crate) struct Launch<'a> {
    pub run_id: &'a str,
    pub attempt_id: &'a str,
    pub operation_id: &'a str,
    pub token: &'a str,
    pub agent: &'a str,
    pub authority: &'a str,
    pub name: &'a str,
    pub workspace: &'a str,
}

pub(crate) enum LaunchOutcome {
    Started(String),
    Refused(String),
    Busy(String),
    Unknown(String),
}

#[derive(Clone, Copy)]
pub(crate) enum WorkerState {
    Running,
    NeedsInput,
    Quota,
    QuotaStopped,
    Stopped,
    StartupRefused,
    Unknown,
}

pub(crate) trait Backend {
    /// The route must bind the supplied occurrence, attempt and exact authority before releasing a worker.
    fn launch(&self, launch: Launch<'_>) -> LaunchOutcome;
    /// Lookup the accepted operation without launching. Absence is not proof of refusal.
    fn reconcile(&self, operation: &str) -> Option<String>;
    /// Return evidence for this exact worker and attempt. A vanished PID is not verified stop.
    fn session(&self, session: &str, attempt: &str) -> Option<WorkerState>;
    fn stop(&self, session: &str, attempt: &str) -> Result<()>;
    fn workspace_available(&self, path: &str) -> Result<bool>;
    /// The backend invokes begin only at its final authorized native-write boundary.
    fn send_prompt(
        &self,
        session: &str,
        attempt: &str,
        text: &str,
        begin: &mut dyn FnMut() -> Result<()>,
    ) -> Result<()>;
    /// Evidence must come from a completed assistant message in the same bound native chat and run.
    fn closing_matches(&self, session: &str, attempt: &str, summary: &str) -> bool;
    /// Projection is idempotent, verified against the bound session, including after a lost response.
    fn project(&self, session: &str, attempt: &str) -> Result<()>;
    fn handoff(&self, session: &str, attempt: &str) -> String;
}

pub(crate) struct Engine<B> {
    store: Store,
    state: PathBuf,
    backend: B,
}

#[derive(Debug)]
struct Changed;
impl std::fmt::Display for Changed {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("scheduled occurrence changed")
    }
}
impl std::error::Error for Changed {}

impl<B: Backend> Engine<B> {
    pub(crate) fn open(state: &Path, host: &Path, backend: B) -> Result<Self> {
        let store = Store::open(state, host)?;
        Ok(Self {
            store,
            state: fs::canonicalize(state)?,
            backend,
        })
    }

    pub(crate) fn tick(&self, now: Timestamp) -> Result<()> {
        let _dispatch = self.store.dispatch_lock()?;
        self.store.claim_due(now)?;
        let mut errors = Vec::new();
        for run in self.store.active()? {
            if let Err(err) = self.advance(&run, now)
                && !err.is::<Changed>()
            {
                errors.push(format!("run {}: {err}", run.id));
            }
        }
        if !errors.is_empty() {
            return Err(errors.join("\n").into());
        }
        Ok(())
    }

    pub(crate) fn run(&self, job: &str, invocation: &str, now: Timestamp) -> Result<String> {
        let _dispatch = self.store.dispatch_lock()?;
        let run = self.store.claim_manual(job, invocation, now)?;
        if run.state == "queued"
            && run.attempts.is_empty()
            && let Err(error) = self.dispatch(&run, now)
            && !error.is::<Changed>()
        {
            return Err(error);
        }
        Ok(run.id)
    }

    fn advance(&self, run: &Occurrence, now: Timestamp) -> Result<()> {
        match run.state.as_str() {
            "ready" => self.project(run, now),
            "attention" => self.recover(run, now),
            "working" | "needs-input" => self.observe(run, now),
            "queued" | "blocked" => {
                let due = run.due_at.parse::<Timestamp>()?;
                let age = now.as_nanosecond() - due.as_nanosecond();
                let grace = i128::from(run.spec.grace);
                if !run.manual
                    && used_routes(run) == 0
                    && ((grace == 0 && age >= 60_000_000_000) || (grace > 0 && age > grace))
                {
                    return self.mark(&run.id, "missed", "Trigger expired before safe dispatch.");
                }
                self.dispatch(run, now)
            }
            _ => Ok(()),
        }
    }

    fn observe(&self, run: &Occurrence, now: Timestamp) -> Result<()> {
        let Some(attempt) = run.attempts.last() else {
            return self.mark(
                &run.id,
                "attention",
                "Worker state is missing its execution attempt.",
            );
        };
        if attempt.state == "dispatching" {
            return if let Some(session) = self.backend.reconcile(&attempt.operation_id) {
                self.transition(run, "working", "running", Some(&session), "", now)
            } else {
                self.transition(
                    run,
                    "attention",
                    "unknown",
                    None,
                    "Launch outcome unknown. Inspect the daemon session before re-running.",
                    now,
                )
            };
        }
        let Some(worker) = self.backend.session(&attempt.session_id, &attempt.id) else {
            return self.transition(
                run,
                "attention",
                "unknown",
                None,
                "Worker session is unavailable. Inspect the saved workspace and session history.",
                now,
            );
        };
        if attempt.state == "stopping" {
            return self.stop(run, worker, now);
        }
        if let Some(report) = &run.completion {
            if self
                .backend
                .closing_matches(&attempt.session_id, &attempt.id, &report.summary)
            {
                return self.finalize(run, None, now);
            }
            if stopped(worker) {
                return self.transition(run, "incomplete", "stopped", None,
                    "Completion was received, but the worker exited before its closing chat message could be verified.", now);
            }
            if run.state != "needs-input" && elapsed(now, attempt.updated_at, 5 * 60) {
                return self.transition(run, "needs-input", &attempt.state, None,
                    "Open the worker chat and ask it to include the saved completion summary in its closing message.", now);
            }
            return Ok(());
        }
        match worker {
            WorkerState::Quota | WorkerState::QuotaStopped => {
                let next = self.change(run, |current, attempt| {
                    attempt.state = "stopping".into();
                    attempt.updated_at = now;
                    current.state = "working".into();
                    current.detail =
                        "Provider usage limit reached. Verifying that the worker stopped.".into();
                    Ok(())
                })?;
                self.stop(&next, worker, now)
            }
            WorkerState::StartupRefused if !attempt.prompt_started => {
                self.fallback(run, STARTUP_REJECTED, now)
            }
            WorkerState::Stopped | WorkerState::StartupRefused => self.transition(
                run,
                "incomplete",
                "stopped",
                None,
                "Worker exited without a completion receipt; the workspace was kept.",
                now,
            ),
            WorkerState::Unknown => self.transition(
                run,
                "attention",
                "unknown",
                None,
                "Worker disappeared without a verified stop or completion receipt.",
                now,
            ),
            WorkerState::NeedsInput => self.transition(
                run,
                "needs-input",
                &attempt.state,
                None,
                "Open the worker chat to continue.",
                now,
            ),
            WorkerState::Running if !attempt.prompt_delivered => self.deliver(run, now),
            WorkerState::Running if run.state != "working" => {
                self.transition(run, "working", &attempt.state, None, "", now)
            }
            WorkerState::Running => Ok(()),
        }
    }

    fn deliver(&self, run: &Occurrence, now: Timestamp) -> Result<()> {
        let attempt = run.attempts.last().ok_or(Changed)?;
        let unknown = |current: &Occurrence| {
            self.transition(current, "attention", "unknown", None,
            "Opening prompt delivery outcome unknown. Inspect the worker chat before sending anything again.", now)
        };
        if attempt.prompt_started {
            return unknown(run);
        }
        let prompt = worker_prompt(run, &self.backend);
        let mut begun = None;
        let send = self
            .backend
            .send_prompt(&attempt.session_id, &attempt.id, &prompt, &mut || {
                if begun.is_some() {
                    return Err(Changed.into());
                }
                begun = Some(self.change(run, |_, attempt| {
                    attempt.prompt_started = true;
                    Ok(())
                })?);
                Ok(())
            });
        if let Some(begun) = begun {
            if send.is_err() {
                return unknown(&begun);
            }
            self.store.update(&begun.id, |current| {
                let live = current.attempts.last_mut().ok_or(Changed)?;
                if live.id != attempt.id || !live.prompt_started {
                    return Err(Changed.into());
                }
                live.prompt_delivered = true;
                if current.completion.is_none() {
                    current.state = "working".into();
                    current.detail.clear();
                }
                Ok(())
            })?;
        } else if elapsed(now, attempt.started_at, 2 * 60) {
            self.transition(run, "needs-input", &attempt.state, None,
                "The private composer is not available. Open the worker chat and check its login and workspace trust.", now)?;
        }
        Ok(())
    }

    fn recover(&self, run: &Occurrence, now: Timestamp) -> Result<()> {
        let Some(a) = run.attempts.last().filter(|a| a.state == "unknown") else {
            return Ok(());
        };
        let recovered;
        let session = if a.session_id.is_empty() {
            let Some(identity) = self.backend.reconcile(&a.operation_id) else {
                return Ok(());
            };
            recovered = identity;
            recovered.as_str()
        } else {
            a.session_id.as_str()
        };
        let Some(worker) = self.backend.session(session, &a.id) else {
            return Ok(());
        };
        if let Some(receipt) = &run.completion
            && self
                .backend
                .closing_matches(session, &a.id, &receipt.summary)
        {
            return self.finalize(run, Some(session), now);
        }
        if stopped(worker) {
            return self.transition(run, "incomplete", "stopped", Some(session),
                "Worker stop was verified after an uncertain outcome. The workspace and history were kept.", now);
        }
        if a.session_id.is_empty()
            && matches!(
                worker,
                WorkerState::Running | WorkerState::NeedsInput | WorkerState::Quota
            )
        {
            self.transition(run, "working", "running", Some(session), "", now)?;
        }
        Ok(())
    }

    fn stop(&self, run: &Occurrence, state: WorkerState, now: Timestamp) -> Result<()> {
        let a = run.attempts.last().ok_or(Changed)?;
        if stopped(state) {
            return self.fallback(run, QUOTA, now);
        }
        if now.as_nanosecond() - a.updated_at.as_nanosecond() > 120_000_000_000 {
            return self.transition(
                run,
                "attention",
                "unknown",
                None,
                "Could not verify worker stop. Inspect the previous session before continuing.",
                now,
            );
        }
        if !matches!(state, WorkerState::Unknown) {
            let _ = self.backend.stop(&a.session_id, &a.id);
        }
        Ok(())
    }

    fn fallback(&self, run: &Occurrence, reason: &str, now: Timestamp) -> Result<()> {
        self.transition(run, "queued", "stopped", None, reason, now)?;
        self.dispatch(&self.store.occurrence(&run.id)?, now)
    }

    fn dispatch(&self, initial: &Occurrence, now: Timestamp) -> Result<()> {
        let mut run = initial.clone();
        if run.workspace.is_empty() {
            let Some(reserved) = self.reserve_workspace(&run)? else {
                return Ok(());
            };
            run = reserved;
        }
        if run.state == "blocked" && !self.workspace_available(&run)? {
            return Ok(());
        }
        if let Err(e) = workspace::ensure(&self.state, &run) {
            return self.mark(&run.id, "attention", &e.to_string());
        }
        for i in used_routes(&run)..run.spec.agents.len() {
            let mut entropy = [0u8; 32];
            File::open("/dev/urandom")?.read_exact(&mut entropy)?;
            let token: String = entropy.iter().map(|b| format!("{b:02x}")).collect();
            let agent = run.spec.agents[i].clone();
            let attempt = Attempt {
                id: id(now)?,
                operation_id: format!("schedule:{}", id(now)?),
                completion_token_hash: digest(&token),
                dispatch_started: true,
                state: "dispatching".into(),
                agent,
                workspace: run.workspace.clone(),
                started_at: now,
                updated_at: now,
                ..Attempt::default()
            };
            run = self.store.update(&run.id, |current| {
                if current.attempts.len() != run.attempts.len()
                    || !matches!(current.state.as_str(), "queued" | "blocked")
                {
                    return Err(Changed.into());
                }
                current.state = "working".into();
                current.detail.clear();
                current.attempts.push(attempt.clone());
                Ok(())
            })?;
            let outcome = self.backend.launch(Launch {
                run_id: &run.id,
                attempt_id: &attempt.id,
                operation_id: &attempt.operation_id,
                token: &token,
                agent: &attempt.agent,
                authority: &run.spec.authority,
                name: &run.spec.name,
                workspace: &run.workspace,
            });
            let redact = |error: String| scrub(error, &[&token, &run.spec.prompt]);
            match outcome {
                LaunchOutcome::Started(session) if !session.is_empty() => {
                    return self.transition(&run, "working", "running", Some(&session), "", now);
                }
                LaunchOutcome::Refused(reason) => {
                    self.transition(&run, "queued", "rejected", None, &redact(reason), now)?;
                    run = self.store.occurrence(&run.id)?;
                }
                LaunchOutcome::Busy(reason) => {
                    return self.transition(
                        &run,
                        "blocked",
                        "workspace_busy",
                        None,
                        &redact(reason),
                        now,
                    );
                }
                unknown => {
                    if let Some(session) = self.backend.reconcile(&attempt.operation_id) {
                        return self.transition(
                            &run,
                            "working",
                            "running",
                            Some(&session),
                            "",
                            now,
                        );
                    }
                    let reason = match unknown {
                        LaunchOutcome::Unknown(reason) => redact(reason),
                        _ => String::new(),
                    };
                    return self.transition(&run, "attention", "unknown", None,
                        &format!("Launch outcome unknown. Inspect the daemon session before re-running. {reason}"), now);
                }
            }
        }
        self.mark(
            &run.id,
            "exhausted",
            "All configured agents were refused or reached a verified usage limit.",
        )
    }

    fn reserve_workspace(&self, original: &Occurrence) -> Result<Option<Occurrence>> {
        let isolate = original.spec.workspace == "worktree";
        let (source, work) = match workspace::describe(&self.state, original, isolate) {
            Ok(pair) => pair,
            Err(e) => {
                self.mark(&original.id, "attention", &e.to_string())?;
                return Ok(None);
            }
        };
        let mut run = original.clone();
        run.source_directory = source;
        run.workspace = work;
        let mut available = self.workspace_available(&run)?;
        if !available
            && !isolate
            && run.spec.allow_overlap
            && let Ok((_, isolated)) = workspace::describe(&self.state, &run, true)
        {
            run.workspace = isolated;
            available = self.workspace_available(&run)?;
        }
        if !available {
            self.mark(&run.id, "blocked", &format!(
                "Workspace {} overlaps another Swarm session or occurrence. Choose a dedicated checkout for isolation, or wait until it is free.",
                run.source_directory
            ))?;
            return Ok(None);
        }
        Ok(Some(self.store.update(&run.id, |current| {
            current.workspace = run.workspace;
            current.source_directory = run.source_directory;
            Ok(())
        })?))
    }

    fn workspace_available(&self, run: &Occurrence) -> Result<bool> {
        if !self.backend.workspace_available(&run.workspace)? {
            return Ok(false);
        }
        for other in self.store.active()? {
            if other.id == run.id || other.workspace.is_empty() || !other.reserves() {
                continue;
            }
            let exclusive = other.job_id == run.job_id
                && other.state != "ready"
                && (!run.spec.allow_overlap || !other.spec.allow_overlap);
            if exclusive
                || workspace::overlaps(Path::new(&run.workspace), Path::new(&other.workspace))
            {
                return Ok(false);
            }
        }
        Ok(true)
    }

    pub(crate) fn complete(
        &self,
        run_id: &str,
        attempt_id: &str,
        session: &str,
        token: &str,
        mut report: Completion,
        now: Timestamp,
    ) -> Result<()> {
        report.summary = report.summary.trim().to_owned();
        if report.summary.is_empty()
            || report.summary.len() > 4096
            || report.links.len() > 16
            || token.len() != 64
        {
            return Err("invalid completion summary, links, or attempt credential".into());
        }
        if report
            .links
            .iter()
            .any(|link| link.trim().is_empty() || link.len() > 1024)
        {
            return Err("invalid completion artifact link".into());
        }
        let token_hash = digest(token);
        let saved = self.store.update(run_id, |run| {
            let a = run
                .attempts
                .last_mut()
                .ok_or("no active automation attempt")?;
            if a.id != attempt_id
                || !bool::from(
                    a.completion_token_hash
                        .as_bytes()
                        .ct_eq(token_hash.as_bytes()),
                )
            {
                return Err(
                    "completion credential did not match the active automation attempt".into(),
                );
            }
            if a.session_id != session || session.is_empty() {
                return Err("completion came from a different worker session".into());
            }
            if let Some(previous) = &run.completion {
                if previous == &report {
                    return Ok(());
                }
                return Err("a different completion receipt is already recorded".into());
            }
            if a.state != "running" || !a.prompt_started {
                return Err("this automation attempt is no longer eligible to complete".into());
            }
            if self.backend.session(session, attempt_id).is_none() {
                return Err("completion came from an unbound worker session".into());
            }
            a.updated_at = now;
            run.state = "working".into();
            run.completion = Some(report.clone());
            run.detail = "Completion received. Waiting for the closing chat message.".into();
            Ok(())
        })?;
        if saved.state != "ready"
            && self
                .backend
                .closing_matches(session, attempt_id, &report.summary)
            && let Err(err) = self.finalize(&saved, Some(session), now)
            && !err.is::<Changed>()
        {
            return Err(err);
        }
        Ok(())
    }

    fn finalize(&self, run: &Occurrence, session: Option<&str>, now: Timestamp) -> Result<()> {
        let saved = self.change(run, |current, a| {
            if current.completion.is_none() {
                return Err(Changed.into());
            }
            if current.state != "ready" {
                if let Some(session) = session {
                    a.session_id = session.into();
                }
                a.state = "complete".into();
                a.updated_at = now;
                current.state = "ready".into();
                current.completed_at = now;
                current.detail.clear();
            }
            Ok(())
        })?;
        let _ = self.project(&saved, now);
        Ok(())
    }

    fn project(&self, run: &Occurrence, now: Timestamp) -> Result<()> {
        let a = run
            .attempts
            .last()
            .ok_or("completion has no worker attempt")?;
        let Some(worker) = self.backend.session(&a.session_id, &a.id) else {
            return Ok(());
        };
        let projected = run.projected_at == zero_time();
        let released = run.released_at == zero_time() && stopped(worker);
        if projected {
            self.backend.project(&a.session_id, &a.id)?;
        }
        if projected || released {
            self.change(run, |current, _| {
                if projected && current.projected_at == zero_time() {
                    current.projected_at = now;
                }
                if released && current.released_at == zero_time() {
                    current.released_at = now;
                }
                Ok(())
            })?;
        }
        Ok(())
    }

    fn mark(&self, id: &str, state: &str, detail: &str) -> Result<()> {
        self.store.update(id, |current| {
            current.state = state.into();
            current.detail = detail.into();
            Ok(())
        })?;
        Ok(())
    }

    fn change(
        &self,
        run: &Occurrence,
        f: impl FnOnce(&mut Occurrence, &mut Attempt) -> Result<()>,
    ) -> Result<Occurrence> {
        let old = run.attempts.last().ok_or(Changed)?;
        self.store.update(&run.id, |current| {
            let mut a = current.attempts.pop().ok_or(Changed)?;
            if old.id != a.id
                || old.state != a.state
                || (run.completion.is_none() && current.completion.is_some())
            {
                return Err(Changed.into());
            }
            f(current, &mut a)?;
            current.attempts.push(a);
            Ok(())
        })
    }

    fn transition(
        &self,
        run: &Occurrence,
        state: &str,
        attempt_state: &str,
        session: Option<&str>,
        detail: &str,
        now: Timestamp,
    ) -> Result<()> {
        self.change(run, |current, a| {
            a.state = attempt_state.into();
            a.detail = detail.into();
            a.updated_at = now;
            if let Some(session) = session {
                a.session_id = session.into();
            }
            current.state = state.into();
            current.detail = detail.into();
            Ok(())
        })?;
        Ok(())
    }
}

fn stopped(state: WorkerState) -> bool {
    matches!(
        state,
        WorkerState::Stopped | WorkerState::StartupRefused | WorkerState::QuotaStopped
    )
}

fn elapsed(now: Timestamp, since: Timestamp, seconds: i128) -> bool {
    now.as_nanosecond() - since.as_nanosecond() >= seconds * 1_000_000_000
}

fn used_routes(run: &Occurrence) -> usize {
    run.attempts
        .iter()
        .filter(|a| a.state != "workspace_busy")
        .count()
}

fn scrub(mut text: String, secrets: &[&str]) -> String {
    for secret in secrets.iter().filter(|s| !s.is_empty()) {
        text = text.replace(secret, "[redacted]");
    }
    let mut chars = text.chars().map(|c| if c.is_control() { ' ' } else { c });
    let mut output: String = chars.by_ref().take(400).collect();
    if chars.next().is_some() {
        output.push('…');
    }
    output
}

fn worker_prompt(run: &Occurrence, backend: &impl Backend) -> String {
    let mut prompt = format!(
        "Scheduled automation from Swarm\n\n{}\n\nOnce the requested work is complete, record completion with the scheduled completion tool if it is available. Otherwise, call the local completion command from this session: \"$SWARM_SCHEDULE_BIN\" schedule complete --summary 'your summary'. Use --link once per artifact if needed. After the receipt succeeds, send a concise final assistant message with the result, current state, and any artifact links. Include the receipt summary verbatim in that closing message.",
        run.spec.prompt
    );
    if let Some(a) = run
        .attempts
        .iter()
        .rev()
        .skip(1)
        .find(|a| a.state == "stopped" && a.detail != STARTUP_REJECTED)
    {
        prompt.push_str(&format!("\n\nA previous {} worker reached a provider usage limit and was verified stopped. It used this same workspace. Inspect the changes and verify any prior external actions before continuing. Preserve existing work. Previous Swarm session: {}.{}",
            a.agent, a.session_id, backend.handoff(&a.session_id, &a.id)));
    }
    prompt
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::schedule::{Calendar, Spec, Temp};
    use std::{
        cell::{Cell, RefCell},
        collections::BTreeMap,
        panic::{AssertUnwindSafe, catch_unwind},
        process::Command,
    };

    struct Started {
        agent: String,
        attempt: String,
        operation: String,
        session: String,
        token: String,
        cwd: String,
        state: WorkerState,
    }

    #[derive(Default)]
    struct Fixture {
        launches: RefCell<Vec<Started>>,
        prompts: RefCell<Vec<String>>,
        refused: RefCell<Vec<String>>,
        stops: Cell<usize>,
        crash_after_start: Cell<bool>,
        crash_after_prompt: Cell<bool>,
        fail_project: Cell<bool>,
        project_calls: Cell<usize>,
        projected: RefCell<BTreeMap<String, String>>,
        closing: RefCell<BTreeMap<String, (String, String)>>,
    }

    impl Backend for &Fixture {
        fn launch(&self, launch: Launch<'_>) -> LaunchOutcome {
            assert!(!launch.run_id.is_empty() && !launch.name.is_empty());
            assert_eq!(launch.authority, "agent-default");
            let mut starts = self.launches.borrow_mut();
            let session = format!("fixture-{}", starts.len());
            starts.push(Started {
                agent: launch.agent.into(),
                attempt: launch.attempt_id.into(),
                operation: launch.operation_id.into(),
                session: session.clone(),
                token: launch.token.into(),
                cwd: launch.workspace.into(),
                state: WorkerState::Running,
            });
            if self.crash_after_start.replace(false) {
                panic!("injected crash after worker start and before catalog reply");
            }
            if self
                .refused
                .borrow()
                .iter()
                .any(|agent| agent == launch.agent)
            {
                starts.pop();
                return LaunchOutcome::Refused(format!(
                    "fixture refused {}\n{}",
                    launch.agent, launch.token
                ));
            }
            LaunchOutcome::Started(session)
        }

        fn reconcile(&self, operation: &str) -> Option<String> {
            self.launches
                .borrow()
                .iter()
                .find(|a| a.operation == operation)
                .map(|a| a.session.clone())
        }

        fn session(&self, session: &str, attempt: &str) -> Option<WorkerState> {
            self.launches
                .borrow()
                .iter()
                .find(|a| a.session == session && a.attempt == attempt)
                .map(|a| a.state)
        }

        fn stop(&self, session: &str, attempt: &str) -> Result<()> {
            assert!(self.session(session, attempt).is_some());
            self.stops.set(self.stops.get() + 1);
            Ok(())
        }

        fn workspace_available(&self, _: &str) -> Result<bool> {
            Ok(true)
        }

        fn send_prompt(
            &self,
            session: &str,
            attempt: &str,
            text: &str,
            begin: &mut dyn FnMut() -> Result<()>,
        ) -> Result<()> {
            assert!(self.session(session, attempt).is_some());
            begin()?;
            self.prompts.borrow_mut().push(text.into());
            if self.crash_after_prompt.replace(false) {
                panic!(
                    "injected crash after native prompt and before orchestration acknowledgement"
                );
            }
            Ok(())
        }

        fn closing_matches(&self, session: &str, attempt: &str, summary: &str) -> bool {
            self.closing
                .borrow()
                .get(session)
                .is_some_and(|(a, body)| a == attempt && body.contains(summary))
        }

        fn project(&self, session: &str, attempt: &str) -> Result<()> {
            self.project_calls.set(self.project_calls.get() + 1);
            self.projected
                .borrow_mut()
                .insert(session.into(), attempt.into());
            if self.fail_project.replace(false) {
                return Err("fixture lost projection reply".into());
            }
            Ok(())
        }

        fn handoff(&self, _: &str, _: &str) -> String {
            " Keep fixture.txt.".into()
        }
    }

    fn scenario(
        dir: &Temp,
        engine: &Engine<&Fixture>,
        now: Timestamp,
        workspace: &str,
    ) -> JobAndId {
        let checkout = dir.0.join("repo");
        fs::create_dir_all(&checkout).unwrap();
        if workspace == "worktree" {
            for args in [
                vec!["init", "--quiet"],
                vec![
                    "-c",
                    "user.name=Fixture",
                    "-c",
                    "user.email=fixture@invalid.example",
                    "-c",
                    "commit.gpgsign=false",
                    "-c",
                    "core.hooksPath=/dev/null",
                    "commit",
                    "--allow-empty",
                    "-qm",
                    "fixture",
                ],
            ] {
                let status = Command::new("git")
                    .args(args)
                    .current_dir(&checkout)
                    .status()
                    .unwrap();
                assert!(status.success());
            }
        }
        let job = engine
            .store
            .create(
                Spec {
                    name: "Fixture".into(),
                    prompt: "Retain fixture evidence".into(),
                    directory: checkout.to_str().unwrap().into(),
                    authority: "agent-default".into(),
                    workspace: workspace.into(),
                    agents: vec!["codex".into(), "claude".into()],
                    calendar: Calendar {
                        kind: "cron".into(),
                        time_zone: "UTC".into(),
                        cron: "* * * * *".into(),
                        ..Default::default()
                    },
                    allow_overlap: true,
                    ..Default::default()
                },
                now,
                true,
            )
            .unwrap();
        JobAndId {
            job: job.id,
            invocation: id(now).unwrap(),
        }
    }

    struct JobAndId {
        job: String,
        invocation: String,
    }

    #[test]
    fn quota_fallback_requires_stop_and_first_receipt_requires_final_proof() {
        let dir = Temp::new();
        let state = dir.0.join("state");
        let host = Path::new("/etc/localtime");
        let backend = Fixture::default();
        let now: Timestamp = "2026-01-01T12:00:00Z".parse().unwrap();
        let engine = Engine::open(&state, host, &backend).unwrap();
        let ids = scenario(&dir, &engine, now, "worktree");
        let run = engine.run(&ids.job, &ids.invocation, now).unwrap();
        assert_eq!(engine.run(&ids.job, &ids.invocation, now).unwrap(), run);
        engine.tick(now).unwrap();
        {
            let mut calls = backend.launches.borrow_mut();
            assert_eq!(calls.len(), 1);
            assert_eq!(calls[0].agent, "codex");
            fs::write(Path::new(&calls[0].cwd).join("fixture.txt"), "untouched").unwrap();
            calls[0].state = WorkerState::Quota;
        }
        engine.tick(now).unwrap();
        assert_eq!(backend.stops.get(), 1);
        assert_eq!(backend.launches.borrow().len(), 1);
        backend.launches.borrow_mut()[0].state = WorkerState::Stopped;
        engine.tick(now).unwrap();
        engine.tick(now).unwrap();
        let calls = backend.launches.borrow();
        assert_eq!(calls.len(), 2);
        let (first, next) = (&calls[0], &calls[1]);
        assert_eq!(next.agent, "claude");
        assert_eq!(first.cwd, next.cwd);
        assert_eq!(
            fs::read_to_string(Path::new(&next.cwd).join("fixture.txt")).unwrap(),
            "untouched"
        );
        assert!(backend.prompts.borrow()[1].contains("Previous Swarm session: fixture-0."));
        let report = Completion {
            summary: "Artifact ready".into(),
            links: vec!["file:fixture.txt".into()],
        };
        assert!(
            engine
                .complete(
                    &run,
                    &first.attempt,
                    &first.session,
                    &first.token,
                    report.clone(),
                    now
                )
                .is_err()
        );
        assert!(
            engine
                .complete(
                    &run,
                    &next.attempt,
                    &first.session,
                    &next.token,
                    report.clone(),
                    now
                )
                .is_err()
        );
        engine
            .complete(
                &run,
                &next.attempt,
                &next.session,
                &next.token,
                report.clone(),
                now,
            )
            .unwrap();
        assert!(
            engine
                .complete(
                    &run,
                    &next.attempt,
                    &next.session,
                    &next.token,
                    Completion {
                        summary: "Changed".into(),
                        links: vec![]
                    },
                    now
                )
                .is_err()
        );
        assert_eq!(engine.store.occurrence(&run).unwrap().state, "working");
        backend.closing.borrow_mut().insert(
            next.session.clone(),
            (first.attempt.clone(), report.summary.clone()),
        );
        engine.tick(now).unwrap();
        assert_eq!(engine.store.occurrence(&run).unwrap().state, "working");
        backend.closing.borrow_mut().insert(
            next.session.clone(),
            (next.attempt.clone(), report.summary.clone()),
        );
        backend.fail_project.set(true);
        engine.tick(now).unwrap();
        assert_eq!(engine.store.occurrence(&run).unwrap().state, "ready");
        assert_eq!(
            engine.store.occurrence(&run).unwrap().projected_at,
            zero_time()
        );
        assert_eq!(backend.project_calls.get(), 1);
        assert!(engine.store.active().unwrap()[0].reserves());
        drop(calls);
        drop(engine);

        let engine = Engine::open(&state, host, &backend).unwrap();
        backend.launches.borrow_mut()[1].state = WorkerState::Stopped;
        engine.tick(now).unwrap();
        let ready = engine.store.occurrence(&run).unwrap();
        assert_eq!(ready.released_at, now);
        assert_eq!(ready.projected_at, now);
        assert_eq!(backend.project_calls.get(), 2);
        assert_eq!(backend.projected.borrow().len(), 1);
        assert!(engine.store.active().unwrap().is_empty());
        assert_eq!(backend.launches.borrow().len(), 2);
    }

    #[test]
    fn restart_reconciles_started_worker_and_never_replays_uncertain_prompt() {
        let dir = Temp::new();
        let state = dir.0.join("state");
        let host = Path::new("/etc/localtime");
        let backend = Fixture::default();
        let now: Timestamp = "2026-01-01T12:00:00Z".parse().unwrap();
        let engine = Engine::open(&state, host, &backend).unwrap();
        let ids = scenario(&dir, &engine, now, "directory");
        backend.crash_after_start.set(true);
        assert!(
            catch_unwind(AssertUnwindSafe(|| engine.run(
                &ids.job,
                &ids.invocation,
                now
            )))
            .is_err()
        );
        let before = engine.store.occurrence(&ids.invocation).unwrap();
        assert_eq!(before.attempts[0].state, "dispatching");
        assert!(before.attempts[0].session_id.is_empty());
        drop(engine);

        let engine = Engine::open(&state, host, &backend).unwrap();
        engine.tick(now).unwrap();
        assert_eq!(
            engine.store.occurrence(&ids.invocation).unwrap().attempts[0].session_id,
            "fixture-0"
        );
        backend.crash_after_prompt.set(true);
        assert!(catch_unwind(AssertUnwindSafe(|| engine.tick(now))).is_err());
        assert!(engine.store.occurrence(&ids.invocation).unwrap().attempts[0].prompt_started);
        drop(engine);

        let engine = Engine::open(&state, host, &backend).unwrap();
        for _ in 0..3 {
            engine.tick(now).unwrap();
        }
        let uncertain = engine.store.occurrence(&ids.invocation).unwrap();
        assert_eq!(uncertain.state, "attention");
        assert!(uncertain.reserves());
        assert_eq!(uncertain.attempts[0].id, before.attempts[0].id);
        assert_eq!(backend.prompts.borrow().len(), 1);
        assert_eq!(backend.launches.borrow().len(), 1);
    }

    #[test]
    fn refusal_redacts_token_and_launches_next_route_in_separate_overlap_workspace() {
        let dir = Temp::new();
        let state = dir.0.join("state");
        let backend = Fixture::default();
        backend.refused.borrow_mut().push("codex".into());
        let now: Timestamp = "2026-01-01T12:00:00Z".parse().unwrap();
        let engine = Engine::open(&state, Path::new("/etc/localtime"), &backend).unwrap();
        let ids = scenario(&dir, &engine, now, "worktree");
        let run = engine.run(&ids.job, &ids.invocation, now).unwrap();
        let first = engine.store.occurrence(&run).unwrap();
        assert_eq!(first.attempts[0].detail, "fixture refused codex [redacted]");
        let second = engine.run(&ids.job, &id(now).unwrap(), now).unwrap();
        let second = engine.store.occurrence(&second).unwrap();
        assert_ne!(first.workspace, second.workspace);
        fs::write(Path::new(&first.workspace).join("fixture.txt"), "first").unwrap();
        assert!(!Path::new(&second.workspace).join("fixture.txt").exists());
    }
}
