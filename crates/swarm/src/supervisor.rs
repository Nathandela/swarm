mod claude_takeover;
pub(crate) mod context_guard;
mod handoff;
pub(crate) use handoff::verify_handoff_artifact;
pub(crate) mod mail;
pub(crate) mod mail_files;
pub(crate) mod mail_store;
mod maintenance;
pub(crate) mod manager;
mod pilot_journal;
mod reliable;
mod removal;
pub(crate) mod runtime;
mod scheduling;
pub(crate) mod session;
mod stopped_go;
mod supervision;

use crate::ipc::{self, VERSION, field, wire};
use crate::shim::manager_terminal;
use crate::socket::DeadlineReader;
use runtime::{DATA_IN, DATA_OUT, IO_TIMEOUT, SNAPSHOT};
use serde_json::{Value, json};
use session::Session;
use sha2::{Digest, Sha256};
use std::collections::BTreeMap;
use std::fs;
use std::io::{self, Read, Write};
use std::net::Shutdown;
use std::os::unix::ffi::OsStrExt;
use std::os::unix::net::UnixStream;
use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::mpsc::{self, SyncSender};
use std::sync::{Arc, Mutex, RwLock, Weak};
use std::thread;
use std::time::{Duration, Instant};

const DISPATCHER_INPUT_ROUTE: &str = "use Discord or a bound worker note (swarm teammate message) to contact this dispatcher, or attach for manual input";

const SESSION_LIMIT: usize = 64;

pub(crate) fn run(args: &[String]) -> u8 {
    if args == ["export-stopped-go"] {
        return match stopped_go::export::run() {
            Ok(()) => 0,
            Err(e) => {
                eprintln!("daemon export-stopped-go: {e}");
                1
            }
        };
    }
    if args == ["restart", "--unattended"] {
        return crate::bootstrap::unattended();
    }
    if args.first().is_some_and(|arg| arg == "restart") && args.len() > 1 {
        eprintln!("usage: swarm daemon restart [--unattended]");
        return 2;
    }
    if args == ["restart"] {
        return match crate::bootstrap::restart() {
            Ok(()) => 0,
            Err(e) => {
                eprintln!("daemon restart: {e}");
                1
            }
        };
    }
    if let [role, pid, birth] = args
        && role == "--replace-idle"
    {
        let (Ok(pid), Ok(birth)) = (pid.parse::<u32>(), birth.parse::<u64>()) else {
            return 2;
        };
        match crate::bootstrap::replace_idle(pid, birth) {
            Ok(true) => {}
            Ok(false) => return 2,
            Err(error) => {
                eprintln!("daemon replacement: {error}");
                return 1;
            }
        }
    } else if !args.is_empty() {
        eprintln!("usage: swarm daemon [restart|export-stopped-go]");
        return 2;
    }
    let Some(configured) = ipc::env_path("SWARM_DAEMON_STATE") else {
        eprintln!("daemon: not implemented");
        return 1;
    };
    match serve(configured) {
        Ok(()) => 0,
        Err(err) => {
            eprintln!("daemon: serve: {err}");
            1
        }
    }
}

struct State {
    lock_identity: [u64; 2],
    stopped_go: Option<stopped_go::Store>,
    root: PathBuf,
    endpoint: String,
    socket: PathBuf,
    sessions: RwLock<BTreeMap<String, Arc<Session>>>,
    subscribers: Mutex<Vec<Weak<Subscriber>>>,
    pilot_journal: Option<pilot_journal::Journal>,
    context_guard: context_guard::Store,
    launch_gate: Mutex<()>,
    maintenance_gate: Mutex<String>,
    operation_locks: [Mutex<()>; 64],
    transactions: RwLock<()>,
    deletion_gate: Mutex<()>,
    handoff_wake: SyncSender<()>,
    generation: AtomicU64,
    schedule_socket: Option<PathBuf>,
}

impl State {
    fn verify_idle(&self) -> io::Result<()> {
        let sessions = self.sessions.read().unwrap();
        for entry in fs::read_dir(&self.root)? {
            let entry = entry?;
            if let Some(id) = entry
                .file_name()
                .to_str()
                .filter(|id| runtime::valid_id(id))
                && (!entry.file_type()?.is_dir() || !sessions.contains_key(id))
            {
                return Err(io::Error::other(format!(
                    "cannot establish custody of session {id}"
                )));
            }
        }
        for session in sessions.values() {
            session.reconcile();
            let view = session.view(&self.endpoint);
            let group = field(&view, "group");
            if !matches!(group, "completed" | "ready_for_review")
                || group == "completed" && session.workspace_unconfirmed()
                || group == "ready_for_review" && !session.confirm_idle_observation()?
            {
                return Err(io::Error::other(format!(
                    "session {} is {group} or has unconfirmed custody",
                    session.id
                )));
            }
        }
        Ok(())
    }

    fn rows(&self) -> Vec<Value> {
        let sessions = self.sessions.read().unwrap();
        let mut rows: Vec<_> = sessions.values().map(|s| s.view(&self.endpoint)).collect();
        if let Some(store) = &self.stopped_go {
            rows.extend(store.rows(&self.endpoint));
            rows.sort_by_key(|row| std::cmp::Reverse(stopped_go::timestamp(&row["created_at"])));
        } else {
            rows.sort_by(|a, b| field(b, "created_at").cmp(field(a, "created_at")));
        }
        rows
    }

    fn live_go(&self, request: &Value) -> Option<Arc<stopped_go::live::Owner>> {
        self.stopped_go
            .as_ref()?
            .live(&self.endpoint, field(request, "session_id"))
    }

    fn session(&self, request: &Value) -> io::Result<Arc<Session>> {
        let id = field(request, "session_id");
        let Some((endpoint, local)) = id.split_once('/') else {
            return Err(io::Error::other("invalid session id"));
        };
        if endpoint != self.endpoint || !runtime::valid_id(local) {
            return Err(io::Error::other("invalid session id"));
        }
        self.sessions
            .read()
            .unwrap()
            .get(local)
            .cloned()
            .ok_or_else(|| io::Error::other(format!("unknown session {id:?}")))
    }

    fn publish(&self, session: &Session) {
        self.pilot_refresh(session);
        let _ = self.handoff_wake.try_send(());
        self.publish_row(session.view(&self.endpoint));
    }

    fn publish_row(&self, row: Value) {
        let event = json!({
            "op": "event", "endpoint_id": self.endpoint, "session": row
        });
        self.subscribers.lock().unwrap().retain(|subscriber| {
            let Some(subscriber) = subscriber.upgrade() else {
                return false;
            };
            if subscriber.events.try_send(event.clone()).is_ok() {
                return true;
            }
            let _ = subscriber.conn.shutdown(Shutdown::Both);
            false
        });
    }

    fn journal(&self) -> io::Result<&pilot_journal::Journal> {
        self.pilot_journal
            .as_ref()
            .ok_or_else(|| io::Error::other("Pilot history requires native Rust state"))
    }

    fn pilot_refresh(&self, session: &Session) {
        let rows = self.sessions.read().unwrap();
        if let Some(journal) = &self.pilot_journal
            && rows
                .get(&session.id)
                .is_some_and(|row| std::ptr::eq(row.as_ref(), session))
        {
            pilot_journal::refresh(journal, session, &self.endpoint);
        }
    }

    fn reserve(
        &self,
        request: &Value,
        caps: &[&str],
        managed: Option<Value>,
        mailbox: Option<Value>,
    ) -> io::Result<Arc<Session>> {
        self.reserve_with_recovery(request, caps, managed, mailbox, None)
    }

    fn reserve_with_recovery(
        &self,
        request: &Value,
        caps: &[&str],
        managed: Option<Value>,
        mailbox: Option<Value>,
        expected_nonce: Option<&str>,
    ) -> io::Result<Arc<Session>> {
        if let Some(store) = &self.stopped_go {
            store.verify_rollback_scope()?;
        }
        if self.stopped_go.is_some()
            && (managed.is_some()
                || mailbox.is_some()
                || !matches!(
                    field(&request["launch"], "agent"),
                    "claude" | "claude-code" | "agy" | "codex"
                )
                || request["launch"]["worktree"] == true
                || [
                    "resume_from",
                    "resume_conversation_id",
                    "resume_relocate",
                    "handoff_from",
                ]
                .iter()
                .any(|key| request["launch"]["options"].get(*key).is_some())
                || ["spawned_from", "spawn_intent", "supervision"]
                    .iter()
                    .any(|key| !field(&request["launch"], key).is_empty()))
        {
            return Err(io::Error::other(
                "stopped rollback supports fresh ordinary, non-worktree Claude, agy or Codex launches",
            ));
        }
        if managed.is_some()
            && runtime::launch_env(&request["launch"]["env"])
                .iter()
                .any(|(key, value)| key == "OPENAI_API_KEY" && !value.is_empty())
        {
            return Err(io::Error::other(
                "manager refuses API-key account substitution",
            ));
        }
        if !request["operation_id"].is_null() && request["operation_id"] != "" {
            return Err(io::Error::other(
                "launch operation requires the reliable owner launch protocol",
            ));
        }
        let mut request = request.clone();
        // The caller cannot stamp a linked launch; only resolving an owned source can.
        if let Some(fields) = request.as_object_mut() {
            fields.remove("resolved_agy_source");
        }
        let launch = &request["launch"];
        let options = &launch["options"];
        if options.get("handoff_from").is_some() {
            if !caps.contains(&"hands-off-handoff") || managed.is_some() {
                return Err(io::Error::other(
                    "hands-off-handoff capability was not negotiated for this owner route",
                ));
            }
            let _deletion = self.deletion_gate.lock().unwrap();
            let (bound, source, proof) = handoff::hands_off(self, &request)?;
            let mut prepared = Session::prepare(&bound)?;
            prepared.lineage["hands_off"] = proof;
            return self.reserve_prepared(prepared, false, Some(source), None);
        }
        for (key, cap) in [
            ("resume_conversation_id", "external-resume"),
            ("resume_relocate", "resume-relocate"),
        ] {
            if options.get(key).is_some() && !caps.contains(&cap) {
                return Err(io::Error::other(format!(
                    "{cap} capability was not negotiated"
                )));
            }
        }
        if matches!(field(launch, "agent"), "claude" | "claude-code")
            && options.get("resume_conversation_id").is_some()
        {
            if managed.is_some() {
                return Err(io::Error::other(
                    "external Claude resume requires ordinary owner scope",
                ));
            }
            let prepared = Session::prepare_claude_external(launch, true)?;
            return self.reserve_prepared(prepared, false, None, None);
        }
        if field(launch, "agent") == "codex" && options.get("resume_conversation_id").is_some() {
            if managed.is_some() {
                return Err(io::Error::other(
                    "external Codex resume requires ordinary owner scope",
                ));
            }
            let prepared = Session::prepare_codex_external(launch)?;
            return self.reserve_prepared(prepared, false, None, None);
        }
        let resume = matches!(
            field(launch, "agent"),
            "agy" | "opencode" | "hermes" | "claude" | "claude-code" | "codex"
        ) && !field(options, "resume_from").is_empty();
        let _deletion = resume.then(|| self.deletion_gate.lock().unwrap());
        if resume && let Some(root) = self.root.parent() {
            let id = field(options, "resume_from")
                .rsplit('/')
                .next()
                .unwrap_or_default();
            let journal = match crate::refresh::records(root) {
                Ok(journal) => journal,
                Err(_) if expected_nonce.is_none() => Value::Null,
                Err(error) => return Err(error),
            };
            let row = &journal["records"][id];
            if matches!(field(row, "state"), "claimed" | "waiting")
                && row["nonce"].as_str() != expected_nonce
            {
                return Err(io::Error::other(
                    "the source is held by native maintenance; inspect refresh progress",
                ));
            }
        }
        if field(launch, "agent") == "hermes" && options.get("resume_from").is_some() && !resume {
            return Err(io::Error::other("resume_from requires a saved session"));
        }
        if resume {
            let source = field(options, "resume_from").to_owned();
            if field(launch, "agent") == "hermes" {
                if managed.is_some()
                    || !["spawned_from", "spawn_intent", "supervision"]
                        .into_iter()
                        .all(|key| launch[key].is_null())
                {
                    return Err(io::Error::other("unsupported Hermes resume lineage"));
                }
                let qualified = if source.contains('/') {
                    source
                } else {
                    format!("{}/{}", self.endpoint, source)
                };
                let predecessor = self.session(&json!({"session_id":qualified}))?;
                let prepared = Session::prepare_bound(&request, Some(&predecessor))?;
                return self.reserve_prepared_bound(prepared, false, None, None, Some(predecessor));
            }
            let qualified = if source.contains('/') {
                source.clone()
            } else {
                format!("{}/{}", self.endpoint, source)
            };
            let session = self.session(&json!({"session_id":qualified}))?;
            if matches!(field(launch, "agent"), "claude" | "claude-code") {
                if managed.is_some() {
                    return Err(io::Error::other("unsupported Claude resume role"));
                }
                let prepared = session.prepare_claude_resume(launch, &qualified)?;
                return self.reserve_prepared(prepared, false, None, None);
            }
            if field(launch, "agent") == "codex" {
                if managed.is_some() {
                    return Err(io::Error::other("unsupported Codex resume role"));
                }
                let prepared = if let Some(nonce) = expected_nonce {
                    let root = self
                        .root
                        .parent()
                        .ok_or_else(|| io::Error::other("missing maintenance root"))?;
                    let document = crate::refresh::records(root)?;
                    let claim = &document["records"][&session.id];
                    if claim["nonce"] != nonce || claim["source_instance"] != session.incarnation()
                    {
                        return Err(io::Error::other(
                            "native recovery claim no longer matches the source",
                        ));
                    }
                    let proof = claim["account_proof"]
                        .as_object()
                        .map(|_| &claim["account_proof"]);
                    session.prepare_codex_resume_with_relogin(launch, &qualified, proof)?
                } else {
                    session.prepare_codex_resume(launch, &qualified)?
                };
                return self.reserve_prepared(prepared, false, None, None);
            }
            if field(launch, "agent") == "opencode" {
                if managed.is_some()
                    || launch["options"]
                        .as_object()
                        .is_none_or(|options| options.len() != 1)
                    || launch.as_object().is_none_or(|fields| {
                        fields.keys().any(|key| {
                            !matches!(
                                key.as_str(),
                                "agent" | "cwd" | "env" | "name" | "cols" | "rows" | "options"
                            )
                        })
                    })
                {
                    return Err(io::Error::other("unsupported OpenCode resume parameters"));
                }
                let prepared = session.prepare_opencode_resume(launch, &source)?;
                return self.reserve_prepared(prepared, false, None, None);
            }
            session.expand_agy_resume(&mut request, &source)?;
        }
        let (bound, source) = if mailbox.is_some() {
            (request, None)
        } else {
            handoff::bind(self, &request)?
        };
        if let Some(scope) = managed
            .as_ref()
            .filter(|scope| scope["launch"]["agent"] == "claude")
        {
            crate::providers::claude::verify_project_authority(
                std::path::Path::new(field(scope, "claude_home")),
                std::path::Path::new(field(&scope["launch"], "cwd")),
                &scope["project_authority"],
            )?;
        }
        let mut prepared = Session::prepare(&bound)?;
        if let Some(scope) = mailbox {
            prepared.bind_mail(scope);
        }
        if let Some(scope) = managed.as_ref() {
            if source.is_some() {
                return Err(io::Error::other(
                    "manager cannot inherit an ordinary handoff scope",
                ));
            }
            let mut binding = scope.clone();
            let previous = &scope["resume_request"];
            if scope["launch"]["agent"] == "claude" && !previous.is_null() {
                let predecessor =
                    self.session(&json!({"session_id":previous["binding"]["session"]}))?;
                if !predecessor.manager_parked(previous)? {
                    return Err(io::Error::other(
                        "Claude manager predecessor is not proved parked",
                    ));
                }
                binding["claude_resume"] =
                    predecessor.claude_park_resume_record(&previous["binding"])?;
            }
            prepared.bind_manager(binding)?;
        }
        if self.stopped_go.is_some() {
            prepared.bind_go_rollback(&bound)?;
            runtime::private_dir(&self.root)?;
        }
        self.reserve_prepared(prepared, false, source, managed)
    }

    fn reserve_prepared(
        &self,
        prepared: session::Prepared,
        scheduled: bool,
        source: Option<(String, String)>,
        managed: Option<Value>,
    ) -> io::Result<Arc<Session>> {
        self.reserve_prepared_bound(prepared, scheduled, source, managed, None)
    }

    fn reserve_prepared_bound(
        &self,
        prepared: session::Prepared,
        scheduled: bool,
        source: Option<(String, String)>,
        managed: Option<Value>,
        predecessor: Option<Arc<Session>>,
    ) -> io::Result<Arc<Session>> {
        let reservation = self.launch_gate.lock().unwrap();
        self.reserve_locked(
            prepared,
            scheduled,
            source,
            managed,
            predecessor,
            &reservation,
        )
    }

    fn reserve_locked(
        &self,
        mut prepared: session::Prepared,
        scheduled: bool,
        source: Option<(String, String)>,
        managed: Option<Value>,
        predecessor: Option<Arc<Session>>,
        _reservation: &std::sync::MutexGuard<'_, ()>,
    ) -> io::Result<Arc<Session>> {
        if let Some((local, instance)) = source {
            if !self.sessions.read().unwrap().get(&local).is_some_and(|s| {
                s.incarnation() == instance
                    && if prepared.lineage["hands_off"].is_object() {
                        if prepared.lineage["hands_off"]["kind"] == "opencode" {
                            s.verify_opencode_handoff(&prepared.lineage["hands_off"])
                                .is_ok()
                        } else {
                            s.hands_off_source().is_ok_and(|(_, _, _, proof)| {
                                proof == prepared.lineage["hands_off"]
                            })
                        }
                    } else {
                        s.is_live() && s.verify_effective().is_ok()
                    }
            }) {
                return Err(io::Error::other(
                    "handoff: source changed during provider admission",
                ));
            }
            prepared.lineage["source_instance"] = json!(instance);
        }
        if self
            .sessions
            .read()
            .unwrap()
            .values()
            .filter(|s| s.is_live())
            .count()
            + self
                .stopped_go
                .as_ref()
                .map_or(0, |store| store.live_ids().len())
            >= SESSION_LIMIT
        {
            return Err(io::Error::other("session limit reached"));
        }
        if let Some(predecessor) = &predecessor {
            if !self
                .sessions
                .read()
                .unwrap()
                .get(&predecessor.id)
                .is_some_and(|stored| Arc::ptr_eq(stored, predecessor))
            {
                return Err(io::Error::other(
                    "Hermes resume source is no longer retained",
                ));
            }
            let claim = &prepared.lineage["classic_hermes_resume"];
            if claim.is_object() {
                predecessor.check_classic_hermes_predecessor(claim)?;
                let conversation = field(claim, "conversation");
                if self
                    .sessions
                    .read()
                    .unwrap()
                    .values()
                    .any(|row| row.could_own_classic_hermes(conversation))
                {
                    return Err(io::Error::other(
                        "Hermes conversation already has a live or unconfirmed writer",
                    ));
                }
            } else {
                predecessor.verify_effective()?;
                predecessor.hermes_check_successor()?;
            }
        }
        if let Some(directory) = scheduling::conflict(self, &prepared.cwd, scheduled) {
            return Err(io::Error::new(
                io::ErrorKind::WouldBlock,
                format!("workspace overlaps active Swarm session in {directory}"),
            ));
        }
        if let Some(id) = prepared.claude_resume["conversation_id"].as_str()
            && self
                .sessions
                .read()
                .unwrap()
                .values()
                .any(|row| row.owns_claude_conversation(id))
        {
            return Err(io::Error::other(
                "Claude conversation already has a live or unconfirmed writer",
            ));
        }
        if let Some(id) = prepared.codex_resume["thread"].as_str()
            && self
                .sessions
                .read()
                .unwrap()
                .values()
                .any(|row| row.owns_codex_thread(id))
        {
            return Err(io::Error::other(
                "Codex thread already has a live or unconfirmed writer",
            ));
        }
        if !prepared.resume.is_null()
            && self
                .sessions
                .read()
                .unwrap()
                .values()
                .any(|session| session.opencode_owns(&prepared.resume["rootID"]))
        {
            return Err(io::Error::other("OpenCode root already has a worker"));
        }
        let manager_guard = if let Some(scope) = managed.as_ref() {
            let conversation = field(&scope["launch"]["manager"], "conversation_id");
            let guard_root = self.root.join("manager-launches");
            runtime::private_dir(&guard_root)?;
            let key = format!("{:x}", Sha256::digest(conversation.as_bytes()));
            let marker = guard_root.join(format!("{key}.json"));
            let sessions = self.sessions.read().unwrap();
            let existing = sessions
                .values()
                .find(|session| {
                    session.owns_manager_launch(conversation, &scope["launch_operation"])
                })
                .cloned();
            let recorded = match runtime::read_json(&marker) {
                Ok(record) => Some(record),
                Err(err) if err.kind() == io::ErrorKind::NotFound => None,
                Err(err) => return Err(err),
            };
            let mut previous_id = None;
            if !scope["resume_request"].is_null() {
                let previous = field(&scope["resume_request"]["binding"], "session")
                    .strip_prefix(&format!("{}/", self.endpoint))
                    .and_then(|id| sessions.get(id))
                    .ok_or_else(|| io::Error::other("manager predecessor is not retained"))?;
                if !previous.owns_manager(conversation)
                    || !previous.manager_parked(&scope["resume_request"])?
                {
                    return Err(io::Error::other(
                        "manager predecessor is not proved retired",
                    ));
                }
                previous_id = Some(previous.id.clone());
            } else if existing.is_none()
                && sessions
                    .values()
                    .any(|session| session.owns_manager(conversation))
            {
                return Err(io::Error::other(
                    "discussion already has a durable manager reservation",
                ));
            }
            drop(sessions);
            if let Some(session) = existing {
                let matches = recorded.as_ref().is_some_and(|record| {
                    record["session_id"] == session.id
                        && record["operation_id"] == scope["launch_operation"]
                        && record["request_hash"] == scope["request_hash"]
                });
                if !matches
                    && recorded.as_ref().is_some_and(|record| {
                        record["session_id"].as_str() != previous_id.as_deref()
                    })
                {
                    return Err(io::Error::other(
                        "discussion already has a durable manager reservation",
                    ));
                }
                session.resume_manager_reservation(prepared)?;
                if !matches {
                    runtime::write_json(
                        &marker,
                        &json!({"session_id":session.id,"operation_id":scope["launch_operation"],"request_hash":scope["request_hash"]}),
                    )?;
                }
                return Ok(session);
            }
            if recorded
                .as_ref()
                .and_then(|record| record["session_id"].as_str())
                != previous_id.as_deref()
            {
                return Err(io::Error::other(
                    "discussion already has a durable manager reservation",
                ));
            }
            Some(marker)
        } else {
            None
        };
        let session = Arc::new(Session::reserve(&self.root, prepared)?);
        if let Some(predecessor) = predecessor {
            predecessor.hermes_mark_successor(&session)?;
        }
        self.sessions
            .write()
            .unwrap()
            .insert(session.id.clone(), session.clone());
        if let Some(marker) = manager_guard {
            let scope = managed.as_ref().unwrap();
            runtime::write_json(
                &marker,
                &json!({"session_id":session.id,"operation_id":scope["launch_operation"],"request_hash":scope["request_hash"]}),
            )?;
        }
        Ok(session)
    }

    fn start(&self, session: &Session) -> io::Result<Value> {
        let root = self
            .root
            .parent()
            .ok_or_else(|| io::Error::other("session root has no owner directory"))?;
        let log = if self.stopped_go.is_some() {
            self.root.join("daemon.log")
        } else {
            crate::clean::DiagnosticLog::path(root)
        };
        session.start(&log)?;
        session.await_ready()?;
        self.publish(session);
        Ok(session.view(&self.endpoint))
    }
}

pub(crate) fn endpoint(root: &std::path::Path) -> String {
    let digest = Sha256::digest(root.as_os_str().as_bytes());
    format!(
        "ep-{}",
        digest[..4]
            .iter()
            .map(|b| format!("{b:02x}"))
            .collect::<String>()
    )
}

fn serve(configured: PathBuf) -> io::Result<()> {
    runtime::private_dir(&configured)?;
    let root = fs::canonicalize(configured)?;
    let canonical_lock = root.join("daemon.lock");
    let lock = ipc::env_path("SWARM_DAEMON_LOCK").unwrap_or_else(|| canonical_lock.clone());
    let opted_in = std::env::var(stopped_go::OPT_IN).ok().as_deref() == Some("1");
    let adopt_live = std::env::var(stopped_go::live::OPT_IN).ok().as_deref() == Some("1");
    if adopt_live && !opted_in {
        return Err(io::Error::other(
            "live Go compatibility requires the stopped Go compatibility opt-in",
        ));
    }
    if opted_in && lock != canonical_lock {
        return Err(io::Error::other(
            "stopped Go compatibility requires the canonical daemon lock",
        ));
    }
    // Alternate sockets and lock overrides cannot create two Rust writers for one state root.
    let _lock = runtime::lock(&lock)?;
    use std::os::unix::fs::MetadataExt;
    let lock_meta = _lock.metadata()?;
    let socket = ipc::socket_path()?;
    if opted_in {
        stopped_go::verify_daemon_stopped(&root, &socket)?;
    }
    let stopped_go = stopped_go::Store::open(&root, opted_in, adopt_live)?;
    let ownership = root.join(".rust-state-writer.lock");
    let _state_lock = if lock == ownership || opted_in {
        None
    } else {
        Some(runtime::lock(&ownership)?)
    };
    let endpoint = endpoint(&root);
    let runtime_root = root.join("rust-v1");
    let mut sessions = BTreeMap::new();
    if let Some(store) = &stopped_go {
        for path in stopped_go::export::collect(&root)? {
            let id = path
                .file_name()
                .and_then(|x| x.to_str())
                .expect("validated Rust ID");
            if store.exported(id)? || stopped_go::export::was_published(&path)? {
                continue;
            }
            let session = Session::load(path)?;
            session.reconcile();
            sessions.insert(session.id.clone(), Arc::new(session));
        }
    } else {
        runtime::private_dir(&runtime_root)?;
        runtime::private_dir(&runtime_root.join("operations"))?;
        removal::recover(&runtime_root)?;
        for entry in fs::read_dir(&runtime_root)? {
            let entry = entry?;
            if entry.file_type()?.is_dir()
                && entry.file_name().to_str().is_some_and(runtime::valid_id)
            {
                match Session::load(entry.path()) {
                    Ok(session) => {
                        session.reconcile();
                        sessions.insert(session.id.clone(), Arc::new(session));
                    }
                    Err(err) => eprintln!(
                        "daemon: session {}: {err}",
                        entry.file_name().to_string_lossy()
                    ),
                }
            }
        }
    }
    if let Some(parent) = socket.parent().filter(|p| !p.as_os_str().is_empty()) {
        runtime::private_dir(parent)?;
    }
    // The daemon allocates files and sockets only within private state directories.
    // Set the process mask before starting workers so there is no bind/chmod exposure.
    unsafe { libc::umask(0o077) };
    runtime::reclaim_socket(&socket)?;
    // Imported Go sessions keep their original log and writer custody.
    let mut log = stopped_go
        .is_none()
        .then(|| crate::clean::DiagnosticLog::open_owner(&crate::clean::DiagnosticLog::path(&root)))
        .transpose()?;
    let listener = runtime::bind(&socket)?;
    if stopped_go.is_none() {
        crate::environment::save(&root)?;
    }
    let (handoff_wake, handoff_events) = mpsc::sync_channel(1);
    let receipt = if stopped_go.is_none() {
        scheduling::socket(&socket)
            .map_err(|e| {
                eprintln!("daemon: scheduled completion listener unavailable: {e}");
                e
            })
            .ok()
    } else {
        None
    };
    let pilot_journal = stopped_go.is_none().then(|| {
        pilot_journal::Journal::load(
            &runtime_root,
            sessions.values().map(|session| session.view(&endpoint)),
        )
    });
    let state = Arc::new(State {
        lock_identity: [lock_meta.dev(), lock_meta.ino()],
        context_guard: context_guard::Store::open(&root),
        stopped_go,
        handoff_wake,
        pilot_journal,
        root: runtime_root,
        endpoint,
        socket,
        sessions: RwLock::new(sessions),
        subscribers: Mutex::new(Vec::new()),
        launch_gate: Mutex::new(()),
        maintenance_gate: Mutex::new(String::new()),
        operation_locks: std::array::from_fn(|_| Mutex::new(())),
        transactions: RwLock::new(()),
        deletion_gate: Mutex::new(()),
        generation: AtomicU64::new(1),
        schedule_socket: receipt.as_ref().map(|(path, _)| path.clone()),
    });
    if state.stopped_go.is_none() {
        let watcher = state.clone();
        thread::spawn(move || maintenance::run(watcher));
        let handoffs = state.clone();
        let _ = handoffs.handoff_wake.try_send(());
        thread::spawn(move || supervision::run(handoffs, handoff_events));
        if let Some((_, listener)) = receipt {
            scheduling::receipts(listener, state.clone());
        }
    }
    let monitor = state.clone();
    thread::spawn(move || {
        let mut adopted = monitor
            .stopped_go
            .as_ref()
            .map(stopped_go::Store::live_ids)
            .unwrap_or_default();
        loop {
            if let Some(log) = &mut log {
                log.tick();
            }
            thread::sleep(Duration::from_millis(150));
            if let Some(store) = &monitor.stopped_go {
                adopted.retain(|id| match store.observe_exit(id, &monitor.endpoint) {
                    Ok(Some(row)) => {
                        monitor.publish_row(row);
                        false
                    }
                    _ => true,
                });
            }
            let sessions: Vec<_> = monitor.sessions.read().unwrap().values().cloned().collect();
            for session in sessions {
                if session.reconcile() {
                    monitor.publish(&session);
                } else {
                    monitor.pilot_refresh(&session);
                }
            }
        }
    });
    for incoming in listener.incoming() {
        let stream = incoming?;
        let state = state.clone();
        thread::spawn(move || {
            let _ = connection(stream, &state);
        });
    }
    Ok(())
}

type Writer = Arc<Mutex<UnixStream>>;

fn reply(destination: &Writer, state: &State, mut message: Value) -> io::Result<()> {
    message["endpoint_id"] = json!(state.endpoint);
    runtime::write_control(&mut *destination.lock().unwrap(), &message)
}

fn connection(mut stream: UnixStream, state: &State) -> io::Result<()> {
    let _cleanup = Close(stream.try_clone()?);
    stream.set_read_timeout(Some(IO_TIMEOUT))?;
    stream.set_write_timeout(Some(IO_TIMEOUT))?;
    let mut first = [0];
    stream.read_exact(&mut first)?;
    if first == *b"V" {
        stream.read_exact(&mut [0; 4])?;
        return stream.write_all(&2u32.to_be_bytes());
    }
    let hello = runtime::read_control(&mut first.as_slice().chain(&mut stream))?;
    let writer = Arc::new(Mutex::new(stream.try_clone()?));
    if field(&hello, "op") != "hello" || hello["protocol_version"] != VERSION {
        return reply(
            &writer,
            state,
            json!({"op": "error", "error": "incompatible protocol version"}),
        );
    }
    let manager_available = state.stopped_go.is_none()
        && hello["capabilities"].as_array().is_some_and(|caps| {
            caps.iter()
                .any(|cap| cap.as_str().is_some_and(|cap| cap.starts_with("manager-")))
        })
        && crate::discord::native_manager_available(state.socket.to_string_lossy().as_ref());
    let worker_origin_available = state.stopped_go.is_none()
        && state.root.parent().is_some_and(|root| {
            crate::discord::worker_input_available(root, state.socket.to_string_lossy().as_ref())
        });
    let guild_worker_available = state.stopped_go.is_none()
        && state.root.parent().is_some_and(|root| {
            crate::discord::worker_input_guild_available(
                root,
                state.socket.to_string_lossy().as_ref(),
            )
        });
    let caps: Vec<_> = hello["capabilities"]
        .as_array()
        .into_iter()
        .flatten()
        .filter_map(Value::as_str)
        .filter(|cap| {
            if *cap == crate::refresh::CAP {
                return state.stopped_go.is_none();
            }
            if let Some(legacy) = &state.stopped_go {
                return matches!(*cap, "subscribe" | "attach")
                    || *cap == "opencode-history-v1" && legacy.has_live_opencode();
            }
            matches!(*cap, crate::teammate::MAIL_CAP | crate::teammate::FILE_CAP)
                || worker_origin_available
                    && matches!(*cap, "message-origin" | "worker-discord-origin-v1")
                || guild_worker_available && *cap == "worker-discord-guild-origin-v1"
                || manager_available
                    && matches!(
                        *cap,
                        "manager-launch"
                            | "manager-provenance"
                            | "message-origin"
                            | "manager-retire"
                            | "manager-retire-resume-v1"
                            | manager::QUOTA_RETIRE_CAP
                            | manager::NATIVE_CLI_CAP
                            | crate::discord::enrolled_worker::CAP
                    )
                || matches!(
                    *cap,
                    "attach"
                        | "agy-permission-v1"
                        | "agy-permission-cli-v1"
                        | "subscribe"
                        | "owner-launch"
                        | "hands-off-handoff"
                        | "claude-batch-takeover-v1"
                        | context_guard::CAPABILITY
                        | "external-resume"
                        | "resume-relocate"
                        | "semantic-send"
                        | "journal"
                        | "journal-subscribe-from"
                        | "semantic-target"
                        | "opencode-history-v1"
                        | "native-requests-v1"
                        | crate::bootstrap::UNATTENDED
                        | crate::bootstrap::RESTART
                )
                || (state.schedule_socket.is_some() && scheduling::CAPS.contains(cap)
                    || *cap == crate::schedule::catalog::CAP
                    || *cap == crate::schedule::catalog::CONTROL)
        })
        .collect();
    let mut welcome = json!({
        "op": "hello", "protocol_version": VERSION,
        "build_version": option_env!("SWARM_BUILD_VERSION").unwrap_or("dev"),
    });
    if !caps.is_empty() {
        welcome["capabilities"] = json!(caps);
    }
    reply(&writer, state, welcome)?;
    stream.set_read_timeout(None)?;
    let mut subscribed = None;
    let mut peek_offer = None;
    loop {
        let (tag, payload) = wire::read_frame(&mut stream)?;
        if tag != wire::CONTROL {
            continue;
        }
        let request: Value = serde_json::from_slice(&payload)?;
        if request["endpoint_id"] != state.endpoint {
            reply(
                &writer,
                state,
                json!({"op": "error", "error": "invalid endpoint id"}),
            )?;
            continue;
        }
        let op = field(&request, "op");
        if state.stopped_go.is_some()
            && !matches!(
                op,
                "list"
                    | "subscribe"
                    | "rename"
                    | "set_tag"
                    | "launch"
                    | "attach"
                    | "agy-permission-v1"
                    | "resize"
                    | "terminal_subscribe"
                    | "send_input"
                    | "kill"
                    | "opencode_history"
            )
        {
            reply(
                &writer,
                state,
                json!({"op":"error","error_code":"op_not_implemented","error":"operation is outside stopped Go rollback qualification"}),
            )?;
            continue;
        }
        if op == "owner_idle_restart" && caps.contains(&crate::bootstrap::UNATTENDED) {
            let requested = std::path::Path::new(field(&request, "state_dir"));
            if Some(requested) != state.root.parent() || !request["commit"].is_boolean() {
                reply(
                    &writer,
                    state,
                    json!({"op":"error","error":"idle restart requires the bound supervisor state directory and commit flag"}),
                )?;
                continue;
            }
            if request["lock_identity"] != json!(state.lock_identity) {
                reply(
                    &writer,
                    state,
                    json!({"op":op,"result":"deferred","reason":"configured lock is not the running supervisor lock"}),
                )?;
                continue;
            }
            let _drained = state.transactions.write().unwrap();
            let _reserve = state.launch_gate.lock().unwrap();
            if let Err(reason) = state.verify_idle() {
                reply(
                    &writer,
                    state,
                    json!({"op":op,"result":"deferred","reason":reason.to_string()}),
                )?;
                continue;
            }
            let commit = request["commit"] == true;
            reply(
                &writer,
                state,
                json!({"op":op,"result":if commit {"stopping"} else {"ready"}}),
            )?;
            if commit {
                std::process::exit(0);
            }
            continue;
        }
        if op == "owner_daemon_restart" {
            let requested = std::path::Path::new(field(&request, "state_dir"));
            if !caps.contains(&crate::bootstrap::RESTART) || Some(requested) != state.root.parent()
            {
                reply(
                    &writer,
                    state,
                    json!({"op":"error","error":"restart requires the bound supervisor state directory"}),
                )?;
                continue;
            }
            let _drained = state.transactions.write().unwrap();
            reply(&writer, state, json!({"op":"ok"}))?;
            std::process::exit(0);
        }
        // Attach can stay open for hours. Its live lease is scoped to the socket.
        let _transaction = (op != "attach").then(|| state.transactions.read().unwrap());
        let result = match op {
            "context_guard_get" | "context_guard_set" => reply(&writer,state,state.context_guard.request(&request,caps.contains(&context_guard::CAPABILITY))),
            "native_relogin" if caps.contains(&crate::refresh::CAP) => maintenance::manual(state, &request)
                .and_then(|value| reply(&writer, state, value)),
            "list" => reply(
                &writer,
                state,
                json!({"op": "list", "sessions": state.rows()}),
            ),
            "subscribe" => {
                if subscribed.is_none() {
                    let (sink, events) = mpsc::sync_channel(64);
                    let subscriber = Arc::new(Subscriber {
                        conn: stream.try_clone()?,
                        events: sink,
                    });
                    {
                        let mut subscribers = state.subscribers.lock().unwrap();
                        subscribers.retain(|sink| sink.strong_count() != 0);
                        subscribers.push(Arc::downgrade(&subscriber));
                    }
                    let destination = writer.clone();
                    subscribed = Some(subscriber);
                    thread::spawn(move || {
                        for event in events {
                            let mut conn = destination.lock().unwrap();
                            if runtime::write_control(&mut *conn, &event).is_err() {
                                let _ = conn.shutdown(Shutdown::Both);
                                break;
                            }
                        }
                    });
                }
                reply(&writer, state, json!({"op": "ok"}))
            }
            "schedule_catalog" => {
                let message = if caps.contains(&crate::schedule::catalog::CAP) {
                    crate::schedule::catalog::serve(state.root.parent().ok_or_else(|| io::Error::other("missing owner state root"))?, &request)
                        .unwrap_or_else(|error| json!({"op":"error","error_code":"invalid_field","error":error.to_string()}))
                } else {
                    json!({"op":"error","error_code":"capability_refused","error":"schedule catalog requires the owner catalog capability"})
                };
                reply(&writer, state, message)
            }            "schedule_catalog_change" => {
                let message = if caps.contains(&crate::schedule::catalog::CONTROL) {
                    crate::schedule::catalog::change(state.root.parent().ok_or_else(|| io::Error::other("missing owner state root"))?, &request)
                        .unwrap_or_else(|error| json!({"op":"error","error_code":"invalid_field","error":error.to_string()}))
                } else {
                    json!({"op":"error","error_code":"capability_refused","error":"schedule change requires the owner control capability"})
                };
                reply(&writer, state, message)
            }

            crate::schedule::catalog::MANUAL_OP | "schedule_tick" | "schedule_run" | "schedule_workspace_check" | "schedule_route_check" | "schedule_native_status" => {
                let message = match scheduling::handle(state, &request, &caps) {
                    Ok(message) => message,
                    Err(e) => json!({"op":"error","error_code":"invalid_field","error":e.to_string()}),
                };
                reply(&writer, state, message)
            }
            "owner_launch" | "owner_launch_status" => {
                reliable::handle(state, &request, &caps)
                    .and_then(|v| reply(&writer, state, v))
            }
            "launch" => {
                let message = match state.reserve(&request,&caps,None,None) {
                    Err(e) => {
                        json!({"op":"error","error_code":"invalid_field","error":e.to_string()})
                    }
                    Ok(s) => match state.start(&s) {
                        Ok(view) => json!({"op":"launch","session":view}),
                        Err(e) => {
                            json!({"op":"error","error_code":"outcome_unknown","error":e.to_string()})
                        }
                    },
                };
                reply(&writer, state, message)
            }
            "rename" | "set_tag" => (|| {
                let key = if op == "rename" { "name" } else { "tag" };
                let value = match request.get(key) {
                    None | Some(Value::Null) => "",
                    Some(Value::String(s)) => s,
                    _ => return Err(io::Error::other("invalid session label type")),
                };
                if let Some(store) = &state.stopped_go
                    && field(&request, "session_id").split_once('/').is_some_and(|(_, id)| store.contains(id))
                {
                    let mut value = session::sanitize_name(value);
                    if key == "tag" {
                        value = value.trim().to_owned();
                    }
                    if let Some(row) = store.set_label(&state.endpoint, field(&request, "session_id"), key, value)? {
                        state.publish_row(row);
                    }
                } else {
                    let session = state.session(&request)?;
                    if session.set_label(key, value)? {
                        state.publish(&session);
                    }
                }
                reply(
                    &writer,
                    state,
                    json!({"op":"ok","session_id":request["session_id"]}),
                )
            })(),
            "delete" => removal::delete(state, &request).and_then(|()| {
                if let Some((_, local)) = field(&request, "session_id").split_once('/')
                    && let Some(journal) = &state.pilot_journal
                {
                    journal.deleted(local);
                }
                reply(
                    &writer,
                    state,
                    json!({"op":"ok","session_id":request["session_id"]}),
                )
            }),
            "kill" => state.session(&request).and_then(|session| {
                if let Err(err) = session.kill() {
                    return if session.is_shared_hermes() {
                        reply(&writer,state,json!({"op":"error","error_code":"outcome_unknown","error":err.to_string()}))
                    } else {
                        Err(err)
                    };
                }
                reply(
                    &writer,
                    state,
                    json!({"op": "ok", "session_id": request["session_id"]}),
                )
            }),
            "manager_runtime" | "manager_input_status" | "manager_resolve_call"
                if caps.contains(&"manager-provenance") => state.session(&request).and_then(|session| {
                    let result = (|| {
                        if op == "manager_runtime" && session.dispatcher_runtime_has_draft(&request)? {
                            return Ok(json!({"op":"error","error_code":manager_terminal::CODE,"error":manager_terminal::UNRESOLVED}));
                        }
                        session.manager_operation(&request)
                    })();
                    let body = match result {
                        Err(error) if op == "manager_runtime" && matches!(error.kind(),io::ErrorKind::WouldBlock | io::ErrorKind::TimedOut) =>
                            json!({"op":"error","error_code":"temporarily_unavailable","error":"manager runtime snapshot is busy"}),
                        result => result?,
                    };
                    reply(&writer,state,body)
                }),
            "manager_worker_read" if caps.contains(&crate::discord::enrolled_worker::CAP) =>
                state.session(&request).and_then(|manager| {
                    let call=&request["manager_worker_read"];
                    let context=manager.enrolled_tool_context(&request,call,"swarm_read")?;
                    let args=&call["arguments"];
                    let target=state.session(&json!({"session_id":args["session"]}))?;
                    let result=target.enrolled_worker_result(&context,args)?;
                    reply(&writer,state,json!({"op":"manager_worker_read","session_id":request["session_id"],
                        "worker_result":result}))
                }),
            "manager_worker_send" if caps.contains(&crate::discord::enrolled_worker::CAP) =>
                state.session(&request).and_then(|manager| {
                    let action=&request["manager_worker_send"];
                    let grant=manager.enrolled_worker_send_context(&request)?;
                    let target=state.session(&json!({"session_id":action["arguments"]["session"]}))?;
                    let result=target.enrolled_worker_send(state,&grant,action,request["submit"]==true)?;
                    reply(&writer,state,json!({"op":"manager_worker_send","session_id":request["session_id"],
                        "operation_id":action["operation_id"],"worker_send":result}))
                }),
            "manager_worker_action" if caps.contains(&crate::discord::enrolled_worker::CAP) =>
                state.session(&request).and_then(|session| {
                    let (launch, association) = session.enrolled_worker_action(&request)?;
                    let operation = field(&request,"operation_id");
                    let body = reliable::handle_structured(state,&json!({
                        "op":"owner_launch","operation_id":operation,"launch":launch
                    }),association,&|| session.enrolled_worker_action(&request).map(drop))?;
                    reply(&writer,state,body)
                }),
            "manager_retire" | "manager_retire_status" if caps.contains(&"manager-retire") =>
                state.session(&request).and_then(|session| {
                    if request["manager_retire_request"]["replacement"] == manager::QUOTA_REPLACEMENT
                        && !caps.contains(&manager::QUOTA_RETIRE_CAP)
                    {
                        return Err(io::Error::other("quota retirement capability was not negotiated"));
                    }
                    let body = match session.manager_retire(&request) {
                        Err(error) if error.to_string() == manager_terminal::UNRESOLVED =>
                            json!({"op":"error","error_code":manager_terminal::CODE,"error":"unsent terminal draft prevents dispatcher replacement"}),
                        result => result?,
                    };
                    reply(&writer,state,body)
                }),
            "worker_mail" if caps.contains(&crate::teammate::MAIL_CAP) => mail::request(state,&request).and_then(|body| reply(&writer,state,body)),
            "claude_batch_takeover" if caps.contains(&"claude-batch-takeover-v1") => {
                claude_takeover::acquire(state,&request).and_then(|results|{
                    reply(&writer,state,json!({"op":"claude_batch_takeover","results":results}))
                })
            },
            "native_requests" if caps.contains(&"native-requests-v1") => state.session(&request).and_then(|session| {
                let body = &request["native_requests"];
                let id = field(&request,"session_id");
                if body["session"] != id
                    || !["request_id","native_session_id"].into_iter().all(|key|body[key].is_null() || body[key].is_string())
                    || field(body,"request_id").is_empty() != field(body,"native_session_id").is_empty()
                {
                    return Err(io::Error::other("invalid native request read"));
                }
                let result=session.native_requests(id,body)?;
                reply(&writer,state,json!({"op":"native_requests","session_id":id,"native_requests_result":result}))
            }),
            "interaction_history" if caps.contains(&"journal") => state.session(&request).and_then(|session| {
                let history = &request["history"];
                let limit = history["limit"].as_u64().filter(|limit| *limit > 0)
                    .ok_or_else(||io::Error::other("history requires a positive page size"))?;
                let before = field(history,"before_item");
                if history["session"] != request["session_id"] || history["native"] == true
                    || !history["worker"].is_null() || !field(history,"native_cursor").is_empty()
                    || (!history["before_item"].is_null() && !history["before_item"].is_string())
                    || before.len() > 128
                {
                    return Err(io::Error::other("invalid interaction history request"));
                }
                state.pilot_refresh(&session);
                let (result,floor)=state.journal()?.history(&session.id,before,limit)?;
                reply(&writer,state,json!({"op":"interaction_history","session_id":request["session_id"],"journal":result,"history_floor":floor}))
            }),
            "journal_subscribe_from" if caps.contains(&"journal") && caps.contains(&"journal-subscribe-from") => {
                let rows:Vec<_>=state.sessions.read().unwrap().values().cloned().collect();
                for session in rows{state.pilot_refresh(&session)}
                let from=request["cursor"].as_u64().unwrap_or_default();
                let bytes=request["journal_max_bytes"].as_u64().unwrap_or_default();
                return state.journal()?.stream(stream,&state.endpoint,from,bytes);
            },
            "opencode_history" if caps.contains(&"opencode-history-v1") => (|| {
                let limit = request["limit"].as_u64().unwrap_or(50);
                let before = field(&request, "before");
                let page = if let Some(owner) = state.live_go(&request) {
                    owner.opencode_history(limit, before)?
                } else {
                    state.session(&request)?.opencode_history(limit, before)?
                };
                reply(&writer,state,json!({"op":"opencode_history","session_id":request["session_id"],"history_result":page}))
            })(),
            "native_decide" if caps.contains(&"native-requests-v1") => state.session(&request).and_then(|session| {
                let id = field(&request,"session_id");
                let operation = field(&request,"operation_id");
                let body = &request["native_decision"];
                if request["body_version"] != 1 || !body.is_object() {
                    return Err(io::Error::other("invalid native decision envelope"));
                }
                let result = session.opencode_action(id,operation,"decision",body,state)?;
                reply(&writer,state,json!({"op":"native_decide","session_id":id,"operation_id":operation,"native_decision_receipt":result}))
            }),
            "native_decision_status" if caps.contains(&"native-requests-v1") => state.session(&request).and_then(|session| {
                let id = field(&request,"session_id");
                let operation = field(&request,"operation_id");
                let result = session.opencode_operation_status(id,operation,"decision",&state.root)?
                    .ok_or_else(||io::Error::other("native decision operation is unknown"))?;
                reply(&writer,state,json!({"op":"native_decision_status","session_id":id,"operation_id":operation,"native_decision_receipt":result}))
            }),
            "semantic_target" if caps.contains(&"semantic-target") => state.session(&request).and_then(|session| {
                let instance = session.semantic_target()?;
                reply(&writer,state,json!({"op":"semantic_target","session_id":request["session_id"],"semantic_target_instance":instance}))
            }),
            "semantic_status" if caps.contains(&"semantic-send") => state.session(&request).and_then(|session| {
                let operation = field(&request,"operation_id");
                let receipt = session.semantic_status(operation,field(&request,"session_id"),&state.root)?.unwrap_or_else(|| json!({
                    "state":"unknown_operation", "session":request["session_id"],"operation_id":operation,
                }));
                reply(&writer,state,json!({"op":"semantic_status","operation_id":operation,"semantic_receipt":receipt}))
            }),
            "semantic_send" if caps.contains(&"semantic-send") => state.session(&request).and_then(|session| {
                let operation = field(&request,"operation_id");
                if session.requires_worker_origin_cap()
                    && !request["composer_send"]["origin"].is_null()
                    && !caps.contains(&"worker-discord-origin-v1") {
                    return reply(&writer,state,json!({"op":"error","operation_id":operation,"error_code":"capability_refused","error":"worker Discord origin was not negotiated"}));
                }
                if session.requires_worker_origin_cap()
                    && !field(&request["composer_send"]["origin"], "guild_id").is_empty()
                    && !caps.contains(&"worker-discord-guild-origin-v1") {
                    return reply(&writer,state,json!({"op":"error","operation_id":operation,"error_code":"capability_refused","error":"guild worker Discord origin was not negotiated"}));
                }
                if !request["composer_send"]["origin"].is_null() && !caps.contains(&"message-origin") {
                    return reply(&writer,state,json!({"op":"error","operation_id":operation,"error_code":"capability_refused","error":"message origin was not negotiated"}));
                }
                if !request["composer_send"]["manager_admission"].is_null()
                    && !(caps.contains(&"manager-provenance") && caps.contains(&"message-origin")) {
                    return reply(&writer,state,json!({"op":"error","error_code":"op_not_implemented","error":"requires manager provenance and message origin"}));
                }
                let body = &request["composer_send"];
                if session.is_discord_manager() && body["manager_admission"].is_null() {
                    let id = field(&request,"session_id");
                    let instance = field(body,"session_instance");
                    let text = field(body,"text");
                    if request["body_version"] == 1 && body["session"] == id
                        && !instance.is_empty() && !text.is_empty() && text.len() <= 4096 {
                        let digest = Sha256::digest(operation);
                        let _operation = state.operation_locks
                            [usize::from(digest[0]) % state.operation_locks.len()]
                            .lock().unwrap();
                        let current = session.semantic_target()?;
                        match crate::providers::codex_receipt::reject_unadmitted(
                            &state.root, operation, id, &current, body,
                        ) {
                            Err(error) if error.kind() == io::ErrorKind::InvalidInput => {
                                return reply(&writer,state,json!({"op":"error","error_code":"stale_instance","error":error.to_string()}));
                            }
                            result => result?,
                        }

                        return reply(&writer,state,json!({"op":"error","error_code":"structured_unsupported","error":format!("composer_send: {DISPATCHER_INPUT_ROUTE}")}));
                    }
                }
                let receipt = match session.semantic_send(&request,field(&request,"session_id"),state) {
                    Ok(receipt) => receipt,
                    Err(err) if err.to_string() == manager_terminal::UNRESOLVED => {
                        return reply(&writer,state,json!({"op":"error","error_code":manager_terminal::CODE,"error":format!("composer_send: {}", manager_terminal::UNRESOLVED)}));
                    }
                    Err(err) if session.requires_worker_origin_cap() && err.kind() == io::ErrorKind::Interrupted => {
                        return reply(&writer,state,json!({"op":"error","operation_id":operation,"error_code":"outcome_unknown","error":"Current connector authorization is unconfirmed; inspect --status"}));
                    }
                    Err(err) => return Err(err),
                };
                let response = match field(&receipt,"state") {
                    "native_accepted" | "submitted" | "completed" | "failed" | "cancelled" | "declined" => json!({"op":"ok","operation_id":operation}),
                    "rejected" => json!({"op":"error","operation_id":operation,"error_code":"structured_unsupported","error":"Native provider rejected semantic input"}),
                    _ => json!({"op":"error","operation_id":operation,"error_code":"outcome_unknown","error":"Native semantic submission outcome is unknown; inspect --status"}),
                };
                reply(&writer,state,response)
            }),
            "terminal_subscribe" => (|| {
                peek_offer = None;
                let terminal = if let Some(owner) = state.live_go(&request) {
                    owner.snapshot()?
                } else {
                    let session = state.session(&request)?;
                    let route = field(&request,"permission_route");
                    let context = (session.is_scheduled_agy() && caps.contains(&"agy-permission-cli-v1")).then(|| runtime::terminal_peer(&stream,route,true).ok()).flatten();
                    let mut terminal = session.status_exchange(&json!({
                        "type":"snapshot_req","permission_snapshot":context.is_some(),
                    }))?;
                    terminal["session"] = json!(session.id);
                    if let Some(context) = context
                        && let Some(offer) = terminal["agy_permission"]["request"].as_str() {
                        peek_offer = Some((field(&request,"session_id").to_owned(),offer.to_owned(),context,route.to_owned()));
                    }
                    terminal
                };
                reply(&writer, state, json!({"op": "ok"}))?;
                reply(&writer,state,json!({"op":"terminal_snapshot","terminal":terminal}))
            })(),
            "agy_permission_peeked" if caps.contains(&"agy-permission-cli-v1") => (|| {
                let (session, offered, context, route) = peek_offer.take().ok_or_else(|| io::Error::other("no pending Antigravity peek proof"))?;
                if request["session_id"] != session || request["request"] != offered
                    || runtime::terminal_peer(&stream,&route,true)? != context
                {
                    return Err(io::Error::other("Antigravity peek proof does not match this foreground client"));
                }
                let result = state.session(&request)?.status_exchange(&json!({
                    "type":"agy_permission_peeked","request":offered,"context":context,
                }))?;
                if result["type"] != "ok" { return Err(io::Error::other("Antigravity permission display changed")); }
                // A subsequent foreground or pipe child must have a strictly newer birth stamp.
                let cutoff = runtime::birth_clock()?;
                let deadline = Instant::now() + Duration::from_millis(50);
                while runtime::birth_clock()? <= cutoff && Instant::now() < deadline {
                    thread::sleep(Duration::from_millis(1));
                }
                reply(&writer,state,json!({"op":"ok"}))
            })(),
            "send_input" => state.session(&request).and_then(|session| {
                let data = &request["send_input"];
                if !data.is_object()
                    || !["key", "text"]
                        .into_iter()
                        .all(|key| data[key].is_null() || data[key].is_string())
                    || !(data["submit"].is_null() || data["submit"].is_boolean())
                {
                    return Err(io::Error::other("invalid send_input field type"));
                }
                let key = field(data, "key");
                let text = field(data, "text");
                let submit = key.is_empty() && data["submit"] == true;
                let (bytes, paste) = if key.is_empty() && !text.is_empty() && text.len() <= 4096 {
                    (text, true)
                } else if !key.is_empty() && text.is_empty() {
                    let bytes = match key {
                        "enter" => "\r",
                        "esc" => "\x1b",
                        "ctrl-c" => "\x03",
                        "tab" => "\t",
                        "up" => "\x1b[A",
                        "down" => "\x1b[B",
                        _ => return Err(io::Error::other("unknown key")),
                    };
                    (bytes, false)
                } else {
                    return Err(io::Error::other("need exactly one of text or key"));
                };
                if !session.is_live() {
                    return Err(io::Error::other(format!("session {:?} is not running", session.id)));
                }
                if paste && session.is_discord_manager() {
                    return Err(io::Error::other(DISPATCHER_INPUT_ROUTE));
                }
                let mut command = json!({"type":"input","data":bytes,"submit":submit,"paste":paste});
                if submit && session.is_scheduled_agy() && caps.contains(&"agy-permission-cli-v1") {
                    command["permission_contexts"] = json!(["tty","pipe"].iter().filter_map(|route| runtime::terminal_peer(&stream,route,false).ok()).collect::<Vec<_>>());
                }
                let result = session.status_exchange(&command)?;
                if field(&result, "type") != "ok" {
                    return Err(io::Error::other(field(&result, "error").to_owned()));
                }
                reply(
                    &writer,
                    state,
                    json!({"op": "ok", "session_id": request["session_id"]}),
                )
            }),
            "attach" => (|| {
                let owner = state.live_go(&request);
                let shim = if let Some(owner) = &owner {
                    owner.dial()?
                } else {
                    state.session(&request)?.dial()?
                };
                attachment(&mut stream, &writer, state, &request, shim, owner.as_deref(), owner.is_none() && caps.contains(&"agy-permission-v1"))
            })(),
            "resize" => Ok(()),
            _ => Err(io::Error::other(format!("unknown op {op:?}"))),
        };
        if let Err(err) = result {
            let message = if err.to_string() == "invalid session id" || op.is_empty() {
                err.to_string()
            } else {
                format!("{op}: {err}")
            };
            reply(&writer, state, json!({"op": "error", "error": message}))?;
        }
    }
}

struct Close(UnixStream);

impl Drop for Close {
    fn drop(&mut self) {
        let _ = self.0.shutdown(Shutdown::Both);
    }
}

struct Subscriber {
    conn: UnixStream,
    events: SyncSender<Value>,
}

fn read_attach_snapshot(shim: &mut UnixStream) -> io::Result<Vec<u8>> {
    let mut reader = DeadlineReader(shim, Instant::now() + IO_TIMEOUT);
    let info = runtime::read_control(&mut reader)?;
    let length = info["snapshot_len"]
        .as_u64()
        .filter(|n| *n <= 32 * 1024 * 1024)
        .ok_or_else(|| io::Error::other("invalid shim snapshot length"))? as usize;
    if field(&info, "type") != "snapshot_info" {
        return Err(io::Error::other("expected shim snapshot envelope"));
    }
    let mut snapshot = Vec::with_capacity(length);
    while snapshot.len() < length {
        let (tag, bytes) = wire::read_frame(&mut reader)?;
        if tag != SNAPSHOT || bytes.is_empty() || bytes.len() > length - snapshot.len() {
            return Err(io::Error::other("invalid shim snapshot chunk"));
        }
        snapshot.extend(bytes);
    }
    Ok(snapshot)
}

fn with_live_lease<T>(
    lease: Option<&stopped_go::live::Lease<'_>>,
    send: impl FnOnce() -> io::Result<T>,
) -> io::Result<T> {
    match lease {
        Some(lease) => lease.current(send),
        None => send(),
    }
}

fn attachment(
    client: &mut UnixStream,
    writer: &Writer,
    state: &State,
    request: &Value,
    mut shim: UnixStream,
    owner: Option<&stopped_go::live::Owner>,
    agy_permission: bool,
) -> io::Result<()> {
    let generation = state.generation.fetch_add(1, Ordering::Relaxed);
    let lease = owner
        .map(|owner| owner.begin(generation, &shim, client))
        .transpose()?;
    let snapshot = with_live_lease(lease.as_ref(), || {
        let mut request = json!({"type":"attach"});
        if agy_permission {
            request["agy_permission"] = json!(true);
        }
        runtime::write_control(&mut shim, &request)?;
        read_attach_snapshot(&mut shim)
    })?;
    let id = field(request, "session_id");
    {
        let mut destination = writer.lock().unwrap();
        runtime::write_control(
            &mut *destination,
            &json!({
                "op": "lease", "endpoint_id": state.endpoint, "session_id": id,
                "generation": generation, "snapshot_len": snapshot.len(),
            }),
        )?;
        for chunk in snapshot.chunks(64 * 1024) {
            wire::write_frame(&mut *destination, SNAPSHOT, chunk)?;
        }
    }
    shim.set_read_timeout(None)?;
    let mut source = shim.try_clone()?;
    let pump_writer = writer.clone();
    let closing = Arc::new(AtomicBool::new(false));
    let pump_closing = closing.clone();
    let endpoint = state.endpoint.clone();
    let session = id.to_owned();
    let pump = thread::spawn(move || {
        while let Ok((tag, bytes)) = wire::read_frame(&mut source) {
            let result = if tag == DATA_OUT {
                wire::write_frame(&mut *pump_writer.lock().unwrap(), DATA_OUT, &bytes)
            } else if agy_permission
                && tag == wire::CONTROL
                && let Ok(mut message) = serde_json::from_slice::<Value>(&bytes)
                && message["op"] == "agy_permission"
            {
                message["endpoint_id"] = json!(endpoint);
                message["session_id"] = json!(session);
                message["generation"] = json!(generation);
                runtime::write_control(&mut *pump_writer.lock().unwrap(), &message)
            } else {
                break;
            };
            if result.is_err() {
                break;
            }
        }
        if !pump_closing.load(Ordering::Acquire) {
            let _ = pump_writer.lock().unwrap().shutdown(Shutdown::Read);
        }
    });
    let read_only = owner.is_some_and(stopped_go::live::Owner::read_only);
    let handoff = (|| {
        loop {
            let (tag, bytes) = wire::read_frame(client)?;
            if tag == DATA_IN {
                if read_only {
                    continue;
                }
                with_live_lease(lease.as_ref(), || {
                    wire::write_frame(&mut shim, DATA_IN, &bytes)
                })?;
            } else if tag == wire::CONTROL {
                let control: Value = serde_json::from_slice(&bytes)?;
                if control["endpoint_id"] != state.endpoint
                    || control["session_id"] != id
                    || control["generation"] != generation
                {
                    continue;
                }
                match field(&control, "op") {
                    "resize" if !read_only => with_live_lease(lease.as_ref(), || {
                        runtime::write_control(
                            &mut shim,
                            &json!({
                                "type": "resize", "cols": control["cols"], "rows": control["rows"]
                            }),
                        )
                    })?,
                    op @ ("agy_permission_displayed" | "agy_permission_reply")
                        if agy_permission =>
                    {
                        runtime::write_control(
                            &mut shim,
                            &json!({
                                "type":op,"request":control["request"],
                                "display":control["display"],"choice":control["choice"],
                            }),
                        )?;
                    }
                    "detach" => break,
                    _ => {}
                }
            }
        }
        Ok::<(), io::Error>(())
    })();
    closing.store(true, Ordering::Release);
    let _ = shim.shutdown(Shutdown::Both);
    let _ = pump.join();
    reply(
        writer,
        state,
        json!({"op": "detach", "session_id": id, "generation": generation}),
    )?;
    match handoff {
        Err(err) if err.kind() == io::ErrorKind::UnexpectedEof => Ok(()),
        outcome => outcome,
    }
}

#[cfg(test)]
mod manager_reservation_tests {
    use super::*;
    use std::os::unix::fs::PermissionsExt;

    fn fixture(root: &std::path::Path, rows: BTreeMap<String, Arc<Session>>) -> State {
        State {
            lock_identity: [0, 0],
            context_guard: context_guard::Store::open(root),
            stopped_go: None,
            handoff_wake: mpsc::sync_channel(1).0,
            schedule_socket: None,
            pilot_journal: Some(pilot_journal::Journal::load(
                root,
                rows.values().map(|session| session.view("fixture")),
            )),
            root: root.to_owned(),
            endpoint: "fixture".into(),
            socket: root.join("socket"),
            sessions: RwLock::new(rows),
            subscribers: Mutex::new(Vec::new()),
            launch_gate: Mutex::new(()),
            maintenance_gate: Mutex::new(String::new()),
            operation_locks: std::array::from_fn(|_| Mutex::new(())),
            transactions: RwLock::new(()),
            deletion_gate: Mutex::new(()),
            generation: AtomicU64::new(1),
        }
    }

    #[test]
    fn same_manager_operation_recovers_pre_start_without_replaying_a_claimed_session() {
        let root = crate::test_temp::root().join(format!("mgr-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&root).unwrap();
        let home = root.join("home");
        let codex_home = home.join(".codex");
        runtime::private_dir(&codex_home).unwrap();
        runtime::write_json(
            &codex_home.join("auth.json"),
            &json!({"auth_mode":"chatgpt","tokens":{"account_id":"synthetic"}}),
        )
        .unwrap();
        let provider = root.join("codex");
        fs::write(&provider, b"#!/bin/sh\necho codex-cli 0.167.0\n").unwrap();
        fs::set_permissions(&provider, fs::Permissions::from_mode(0o700)).unwrap();
        let request = json!({"launch":{"agent":"codex","cwd":root,
            "env":[format!("PATH={}", root.display()),format!("HOME={}",home.display())]}});
        let scope = json!({"launch":{"manager":{"conversation_id":"conversation"}},
            "launch_operation":"launch-original","request_hash":"original-hash"});
        let isolated = fixture(&root, BTreeMap::new());
        let mut wrong_account = request.clone();
        wrong_account["launch"]["env"]
            .as_array_mut()
            .unwrap()
            .push(json!("OPENAI_API_KEY=fixture-secret"));
        let err = isolated
            .reserve(&wrong_account, &[], Some(scope.clone()), None)
            .err()
            .unwrap();
        assert!(err.to_string().contains("API-key account substitution"));
        assert!(!err.to_string().contains("fixture-secret"));
        let reserved = isolated
            .reserve(&request, &[], Some(scope.clone()), None)
            .unwrap();
        let id = reserved.id.clone();
        drop(isolated);
        drop(reserved);

        let recovered = || {
            fixture(
                &root,
                BTreeMap::from([(id.clone(), Arc::new(Session::load(root.join(&id)).unwrap()))]),
            )
        };
        let mut distinct = scope.clone();
        distinct["launch_operation"] = json!("another-operation");
        assert!(
            recovered()
                .reserve(&request, &[], Some(distinct), None)
                .is_err()
        );
        assert_eq!(
            recovered()
                .reserve(&request, &[], Some(scope.clone()), None)
                .unwrap()
                .id,
            id
        );

        let marker = root
            .join("manager-launches")
            .join(format!("{:x}.json", Sha256::digest(b"conversation")));
        fs::remove_file(marker).unwrap();
        assert_eq!(
            recovered()
                .reserve(&request, &[], Some(scope.clone()), None)
                .unwrap()
                .id,
            id
        );

        fs::write(root.join(&id).join("claimed"), b"").unwrap();
        assert!(
            recovered()
                .reserve(&request, &[], Some(scope), None)
                .is_err()
        );
        fs::remove_dir_all(root).unwrap();
    }
}
