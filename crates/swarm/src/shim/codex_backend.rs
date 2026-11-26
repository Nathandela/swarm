mod context_guard;
use super::pty;
use crate::ipc::field;
use crate::providers::{codex_proof, codex_receipt};
use crate::schedule::worker;
use crate::supervisor::runtime;
use serde_json::{Value, json};
use std::io;
use std::net::Shutdown;
use std::os::unix::{net::UnixStream, process::CommandExt};
use std::path::Path;
use std::process::{Child, Command, Stdio};
use std::sync::mpsc::{Receiver, SyncSender};
use std::thread;
use std::time::{Duration, Instant};
use tungstenite::{
    Error, Message, WebSocket, client::client_with_config, protocol::WebSocketConfig,
};

pub(super) struct Backend {
    child: Option<Child>,
}

#[derive(Debug)]
pub(super) struct ContainmentUnknown(std::io::Error);

impl std::fmt::Display for ContainmentUnknown {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "Codex App Server stop is unconfirmed: {}", self.0)
    }
}
impl std::error::Error for ContainmentUnknown {}

impl Backend {
    pub(super) fn shutdown(&mut self) -> io::Result<()> {
        if let Some(child) = self.child.as_ref() {
            pty::signal_group(child, libc::SIGKILL)
                .map_err(|e| io::Error::other(ContainmentUnknown(e)))?;
            self.child
                .take()
                .expect("owned app-server")
                .wait()
                .map_err(|e| io::Error::other(ContainmentUnknown(e)))?;
        }
        Ok(())
    }
}

impl Drop for Backend {
    fn drop(&mut self) {
        let _ = self.shutdown();
    }
}

pub(super) struct Channel {
    pub endpoint: String,
    connection: WebSocket<UnixStream>,
    pid: u32,
    birth: u64,
    user_agent: String,
    restored: Option<Observation>,
}

impl Channel {
    pub(super) fn prove_saved_thread(&mut self, thread: &str, cwd: &Path) -> io::Result<()> {
        send(
            &mut self.connection,
            json!({"id":2,"method":"thread/read","params":{
                "threadId":thread,"includeTurns":false,
            }}),
        )?;
        let deadline = Instant::now() + Duration::from_secs(5);
        while Instant::now() < deadline {
            let Some(reply) = read(&mut self.connection)? else {
                continue;
            };
            if reply["id"] == 2 && reply["method"].is_null() {
                let saved = &reply["result"]["thread"];
                if reply.get("error").is_none()
                    && saved["id"] == thread
                    && Path::new(field(saved, "cwd"))
                        .canonicalize()
                        .is_ok_and(|path| path == cwd)
                {
                    return Ok(());
                }
                return Err(io::Error::other(
                    "current Codex login cannot read the exact original thread",
                ));
            }
        }
        Err(io::Error::other("Codex external identity proof timed out"))
    }

    pub(super) fn bind_resume(
        &mut self,
        thread: &str,
        cwd: &Path,
        path: &Path,
        relocated: bool,
    ) -> io::Result<()> {
        let mut params = json!({"threadId":thread,"excludeTurns":true});
        if relocated {
            params["cwd"] = json!(cwd);
        }
        send(
            &mut self.connection,
            json!({"id":2,"method":"thread/resume","params":params}),
        )?;
        let mut observation = Observation {
            thread: thread.to_owned(),
            idle: true,
            joined: true,
            ..Observation::default()
        };
        let deadline = Instant::now() + Duration::from_secs(8);
        while Instant::now() < deadline {
            let Some(frame) = read(&mut self.connection)? else {
                continue;
            };
            if frame["id"] != 2 || !frame["method"].is_null() {
                observation.apply(&frame, cwd);
                continue;
            }
            let restored = &frame["result"]["thread"];
            let live = field(restored, "cwd");
            if restored["id"] != thread
                || relocated && restored["path"].as_str().map(Path::new) != Some(path)
                || live.is_empty()
                || Path::new(live).canonicalize()?.as_path() != cwd
                || restored["status"]["type"] != "idle"
                || !observation.schedule_ready()
            {
                return Err(io::Error::other(
                    "Codex native resume did not prove the exact idle source thread",
                ));
            }
            self.restored = Some(observation);
            return Ok(());
        }
        Err(io::Error::other("Codex native resume proof timed out"))
    }
}

pub(super) fn start(
    executable: &Path,
    cwd: &Path,
    dir: &Path,
    env: &[(String, String)],
    policy: &[String],
) -> io::Result<(Backend, Channel)> {
    start_bounded(executable, cwd, dir, env, policy, Duration::from_secs(60))
}

pub(super) fn start_bounded(
    executable: &Path,
    cwd: &Path,
    dir: &Path,
    env: &[(String, String)],
    policy: &[String],
    timeout: Duration,
) -> io::Result<(Backend, Channel)> {
    let socket = dir.join("b");
    let endpoint = format!("unix://{}", socket.display());
    let child = Command::new(executable)
        .args(["app-server", "--listen", &endpoint])
        .args(policy)
        .current_dir(cwd)
        .env_clear()
        .envs(env.iter().map(|(key, value)| (key, value)))
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .process_group(0)
        .spawn()?;
    let mut backend = Backend { child: Some(child) };
    let channel = connect(
        backend.child.as_ref().expect("owned app-server"),
        &socket,
        endpoint,
        timeout,
    );
    match channel {
        Ok(channel) => Ok((backend, channel)),
        Err(e) => {
            backend.shutdown()?;
            Err(e)
        }
    }
}

fn connect(
    child: &Child,
    socket: &Path,
    endpoint: String,
    timeout: Duration,
) -> io::Result<Channel> {
    // The reference allows cold native startup to take a full minute under host I/O pressure.
    let deadline = Instant::now() + timeout;
    let stream = loop {
        match UnixStream::connect(socket) {
            Ok(stream) => {
                if runtime::peer_pid(&stream)? != child.id() {
                    return Err(io::Error::other(
                        "Codex app-server peer does not match owned child",
                    ));
                }
                break stream;
            }
            Err(error)
                if matches!(
                    error.kind(),
                    io::ErrorKind::NotFound | io::ErrorKind::ConnectionRefused
                ) => {}
            Err(error) => return Err(error),
        }
        if pty::child_exited(child)? {
            return Err(io::Error::other(
                "Codex app-server exited before binding its private socket",
            ));
        }
        if Instant::now() >= deadline {
            return Err(io::Error::other(
                "Codex app-server private socket startup timed out",
            ));
        }
        thread::sleep(Duration::from_millis(20));
    };
    stream.set_read_timeout(Some(Duration::from_secs(10)))?;
    stream.set_write_timeout(Some(Duration::from_secs(10)))?;
    let bounded = WebSocketConfig::default()
        .max_message_size(Some(1024 * 1024))
        .max_frame_size(Some(1024 * 1024));
    let (mut connection, _) = client_with_config("ws://localhost/rpc", stream, Some(bounded))
        .map_err(|_| io::Error::other("Codex app-server WebSocket upgrade failed"))?;
    send(
        &mut connection,
        json!({
            "id":1,"method":"initialize","params":{"clientInfo":{"name":"swarm","title":"swarm","version":"1"},
            "capabilities":{"experimentalApi":true,"requestAttestation":false}}
        }),
    )?;
    let deadline = Instant::now() + Duration::from_secs(10);
    let user_agent = loop {
        if let Some(message) = read(&mut connection)?
            && message["id"] == 1
        {
            if !message["result"].is_object() {
                return Err(io::Error::other("Codex app-server refused to initialize"));
            }
            break field(&message["result"], "userAgent").to_owned();
        }
        if Instant::now() >= deadline {
            return Err(io::Error::other(
                "Codex app-server initialization timed out",
            ));
        }
    };
    send(&mut connection, json!({"method":"initialized","params":{}}))?;
    connection
        .get_mut()
        .set_read_timeout(Some(Duration::from_millis(400)))?;
    connection
        .get_mut()
        .set_write_timeout(Some(Duration::from_millis(400)))?;
    let pid = child.id();
    let birth = runtime::birth(pid)?;
    Ok(Channel {
        endpoint,
        connection,
        pid,
        birth,
        user_agent,
        restored: None,
    })
}

fn send(ws: &mut WebSocket<UnixStream>, message: Value) -> io::Result<()> {
    ws.send(Message::Text(message.to_string().into()))
        .map_err(|_| io::Error::other("Codex app-server write outcome is unknown"))
}

fn read(ws: &mut WebSocket<UnixStream>) -> io::Result<Option<Value>> {
    match ws.read() {
        Ok(Message::Text(message)) => {
            let mut frame: Value = serde_json::from_str(message.as_str())?;
            let guard_evidence = matches!(
                field(&frame, "method"),
                "thread/tokenUsage/updated" | "thread/compacted"
            ) || frame["params"]["item"]["type"] == "contextCompaction";
            if guard_evidence {
                if message.len() > 64 << 10
                    || !crate::providers::codex_context_guard::unique_keys(message.as_str())
                {
                    return Ok(Some(json!({
                        "method":"swarm/guard_evidence_gap","params":{
                            "threadId":frame["params"]["threadId"],"item":frame["params"]["item"]
                        }
                    })));
                }
                if frame["jsonrpc"] == "2.0" {
                    frame.as_object_mut().unwrap().shift_remove("jsonrpc");
                }
            }
            Ok(Some(frame))
        }
        Ok(Message::Close(_)) => Err(io::Error::other("Codex app-server closed the stream")),
        Ok(_) => Ok(None),
        Err(Error::Io(error))
            if matches!(
                error.kind(),
                io::ErrorKind::WouldBlock | io::ErrorKind::TimedOut
            ) =>
        {
            Ok(None)
        }
        Err(_) => Err(io::Error::other("Codex app-server stream closed")),
    }
}

#[derive(Default)]
struct Observation {
    guard: Option<context_guard::Guard>,
    legacy_system_helper: bool,
    proof: Option<codex_proof::Ledger>,
    thread: String,
    rollout: Value,
    turn: Option<String>,
    requests: std::collections::BTreeSet<String>,
    joined: bool,
    missing_rollout: bool,
    scheduled_turn: Option<ScheduledTurn>,
    fresh_no_rollout: bool,
    requests_unverified: bool,
    pilot_final: Option<Value>,
    pilot_user: Option<Value>,
    idle: bool,
    ambiguous: bool,
}

impl Observation {
    fn schedule_ready(&self) -> bool {
        (self.joined || self.missing_rollout)
            && !self.ambiguous
            && !self.requests_unverified
            && self.idle
            && self.turn.is_none()
            && self.requests.is_empty()
            && self.scheduled_turn.is_none()
    }

    fn can_enter_joined_thread(&self, managed: bool) -> bool {
        self.joined
            || managed
                && self.fresh_no_rollout
                && self
                    .proof
                    .as_ref()
                    .is_some_and(codex_proof::Ledger::is_virgin)
    }

    fn apply(&mut self, message: &Value, cwd: &Path) -> bool {
        let payload = &message["params"];
        let method = field(message, "method");
        if method == "thread/started" {
            let thread = &payload["thread"];
            let id = field(thread, "id");
            // The installed viewer starts a proven ephemeral title generator
            // after a reply. It never owns the native rollout or the PTY.
            if thread["ephemeral"] == true
                && thread["source"] == "vscode"
                && (thread["threadSource"] == "thread_title"
                    || self.legacy_system_helper
                        && thread["threadSource"] == "system"
                        && thread["path"].is_null()
                        && thread["parentThreadId"].is_null())
            {
                return false;
            }
            if !self.thread.is_empty() && codex_receipt::canonical_thread(id) && self.thread != id {
                self.ambiguous = true;
                self.idle = false;
                return true;
            }
            let root = field(thread, "cwd");
            if self.thread.is_empty()
                && codex_receipt::canonical_thread(id)
                && !root.is_empty()
                && std::fs::canonicalize(root).ok().as_deref() == Some(cwd)
            {
                self.thread = id.to_owned();
                self.idle = field(&thread["status"], "type") == "idle";
                return true;
            }
            return false;
        }
        if self.thread.is_empty() || field(payload, "threadId") != self.thread {
            return false;
        }
        let turn = field(&payload["turn"], "id");
        let parent = field(&payload["turn"], "rootTurnId");
        let revoked = matches!(method, "turn/started" | "turn/completed")
            && !turn.is_empty()
            && (parent.is_empty() || parent == turn)
            && std::mem::take(&mut self.fresh_no_rollout);
        match method {
            "turn/started" => {
                let id = field(&payload["turn"], "id");
                if id.is_empty() || (!parent.is_empty() && parent != id) {
                    return false;
                }
                self.turn = Some(id.to_owned());
                self.idle = false;
            }
            "turn/completed" => {
                if self.turn.as_deref() != Some(turn) {
                    return revoked;
                }
                self.turn = None;
                self.idle = true;
            }
            "thread/status/changed" => {
                self.idle = self.turn.is_none() && field(&payload["status"], "type") == "idle";
            }
            "item/commandExecution/requestApproval"
            | "item/fileChange/requestApproval"
            | "item/tool/requestUserInput" => {
                if self.requests.len() >= 128
                    || !(message["id"].is_number() || message["id"].is_string())
                {
                    self.requests_unverified = true;
                    return true;
                }
                self.requests.insert(message["id"].to_string());
            }
            "serverRequest/resolved" => {
                self.requests.remove(&payload["requestId"].to_string());
            }
            _ if !method.is_empty() && !message["id"].is_null() => {
                self.requests_unverified = true;
            }
            _ => return false,
        }
        true
    }

    fn status(&self) -> Value {
        let (turn, interaction) = if !self.requests.is_empty() {
            (
                if self.turn.is_some() {
                    "active"
                } else {
                    "idle"
                },
                "permission",
            )
        } else if self.turn.is_some() {
            ("active", "none")
        } else if self.idle {
            ("idle", "none")
        } else {
            ("unknown", "none")
        };
        let mut status = json!({"Process":"running","Turn":turn,"Interaction":interaction});
        if turn == "active" && interaction == "permission" {
            status["Attention"] = json!(true);
        }
        status
    }
}

pub(super) struct SemanticAction {
    pub operation: String,
    pub session: String,
    pub text: String,
    pub schedule_attempt: Option<String>,
    pub composer: Value,
    pub enrolled: Value,
    pub not_after: Instant,
    pub answer: SyncSender<io::Result<Value>>,
}

struct SemanticTarget<'a> {
    dir: &'a Path,
    schedule: Option<&'a worker::Binding>,
    token: &'a str,
    manager: &'a Value,
    cwd: &'a Path,
}

fn persist_observation(observation: &mut Observation, target: &SemanticTarget<'_>) {
    if observation.ambiguous {
        let _ = runtime::write_json(
            &target.dir.join("codex-thread-changed.json"),
            &json!({"token":target.token}),
        );
    }
    if observation.rollout.is_null()
        && observation.joined
        && !observation.ambiguous
        && target.schedule.is_none()
        && target.manager.is_null()
        && let Ok(profile) = runtime::read_json(&target.dir.join("codex-profile.json"))
        && profile["token"] == target.token
        && let Ok(meta) = runtime::read_json(&target.dir.join("session.json"))
        && meta["token"] == target.token
        && let Ok(rollout) = crate::providers::codex_continuity::retained_rollout(
            Path::new(field(&profile["profile"], "native_home")),
            target.cwd,
            &observation.thread,
            &meta["codex_resume"],
        )
    {
        observation.rollout = rollout;
    }
    let mut view = json!({
        "token":target.token, "conversation_id":observation.thread, "status":observation.status(),
        "rollout":observation.rollout,
        "backend_available":!observation.ambiguous, "requests_available":observation.joined && !observation.ambiguous && !observation.requests_unverified,
        "schedule_ready":target.schedule.is_some() && observation.schedule_ready(),
        "manager_ingress_ready": observation.can_enter_joined_thread(true) && observation.idle
            && !observation.ambiguous && !observation.requests_unverified && observation.requests.is_empty(),
    });
    if let Some(guard) = &observation.guard {
        view["context_guard"] = guard.view();
    }
    let _ = runtime::write_json(&target.dir.join("observation.json"), &view);
}

fn capture_history(observation: &mut Observation, message: &Value, target: &SemanticTarget<'_>) {
    if observation.ambiguous || !target.manager.is_null() || target.schedule.is_some() {
        return;
    }
    let params = &message["params"];
    if observation.thread.is_empty() || params["threadId"] != observation.thread {
        return;
    }
    let item = &params["item"];
    let text_item = |kind, text: &str| {
        (!text.is_empty() && text.len() <= 4096).then(|| {
            json!({
                "kind":kind,"status":"completed","text":text,"turn_id":observation.turn,
            })
        })
    };
    let turn = field(&params["turn"], "id");
    let root = field(&params["turn"], "rootTurnId");
    let active = |candidate: &Value| {
        observation
            .turn
            .as_deref()
            .is_some_and(|id| candidate == id)
    };
    match field(message, "method") {
        "turn/started"
            if !turn.is_empty()
                && observation.turn.as_deref() != Some(turn)
                && (root.is_empty() || root == turn) =>
        {
            observation.pilot_final = None;
            observation.pilot_user = None;
        }
        "item/completed" if active(&params["turnId"]) => match field(item, "type") {
            "userMessage" => {
                let Some(parts) = item["content"].as_array() else {
                    return;
                };
                if parts.len() == 1 && parts[0]["type"] == "text" {
                    observation.pilot_user = text_item("user_message", field(&parts[0], "text"));
                }
            }
            "agentMessage" if item["phase"].is_null() || item["phase"] == "final_answer" => {
                observation.pilot_final = text_item("agent_message", field(item, "text"));
            }
            _ => {}
        },
        "turn/completed" if active(&params["turn"]["id"]) => {
            let user = observation.pilot_user.take();
            let reply = observation
                .pilot_final
                .take()
                .filter(|_| params["turn"]["status"] == "completed");
            for item in user.into_iter().chain(reply) {
                let _ = super::capture_pilot_item(target.dir, target.token, item);
            }
        }
        _ => {}
    }
}

fn backfill_initial_history(observation: &Observation, target: &SemanticTarget<'_>) {
    if observation.ambiguous
        || !target.manager.is_null()
        || target.schedule.is_some()
        || observation.rollout.is_null()
        || target
            .dir
            .join("pilot-replies.json")
            .symlink_metadata()
            .is_ok()
    {
        return;
    }
    if let Ok(items) = crate::providers::codex_continuity::initial_history(
        &observation.rollout,
        &observation.thread,
    ) {
        for item in items {
            if super::capture_pilot_item(target.dir, target.token, item).is_err() {
                break;
            }
        }
    }
}

fn observe_frame(observation: &mut Observation, message: &Value, target: &SemanticTarget<'_>) {
    capture_history(observation, message, target);
    let changed = observation.apply(message, target.cwd);
    let updated = observation.guard.as_mut().is_some_and(|guard| {
        let before = guard.view();
        guard.frame(message);
        if observation.ambiguous {
            guard.disconnected();
        }
        before != guard.view()
    });
    if changed || updated {
        persist_observation(observation, target);
    }
}

fn submit(
    ws: &mut WebSocket<UnixStream>,
    observation: &mut Observation,
    rpc: u64,
    action: &SemanticAction,
    target: &SemanticTarget<'_>,
) -> io::Result<Value> {
    let hash = if action.enrolled.is_null() {
        codex_receipt::hash(&action.text)
    } else {
        crate::discord::enrolled_worker::send_intent(&action.enrolled["action"])?
    };
    let root = target
        .dir
        .parent()
        .ok_or_else(|| io::Error::other("missing runtime root"))?;
    codex_receipt::verify_binding(
        root,
        &action.operation,
        &action.session,
        &codex_receipt::instance(target.token),
        &hash,
    )?;
    let admission = &action.composer["manager_admission"];
    let manager_nonce = (!admission.is_null()).then(|| field(admission, "nonce"));
    if let Some(record) = codex_receipt::load(
        target.dir,
        &action.operation,
        &action.session,
        target.token,
        &observation.thread,
    )? {
        if record["intent"] != hash {
            return Err(io::Error::other(
                "semantic operation is already bound to different text",
            ));
        }
        return Ok(record["receipt"].clone());
    }
    let launch = target
        .schedule
        .filter(|binding| action.schedule_attempt.as_deref() == Some(&binding.attempt_id));
    if action.schedule_attempt.is_some() && (launch.is_none() || !observation.schedule_ready()) {
        return Err(io::Error::other(
            "scheduled Codex has no exact opening authority",
        ));
    }
    if observation.thread.is_empty()
        || observation.ambiguous
        || !(observation.can_enter_joined_thread(manager_nonce.is_some())
            || launch.is_some() && observation.missing_rollout && manager_nonce.is_none())
        || observation.requests_unverified
        || !observation.idle
        || observation.turn.is_some()
        || !observation.requests.is_empty()
    {
        return Err(io::Error::other(
            "Codex has no confirmed idle native thread",
        ));
    }
    if Instant::now() >= action.not_after {
        return Err(io::Error::other(
            "semantic authority expired before dispatch",
        ));
    }
    let _manager_effect = manager_nonce
        .map(|_| crate::discord::lock_manager_mail_effect(target.manager))
        .transpose()?;
    if let Some(nonce) = manager_nonce {
        let proof = observation
            .proof
            .as_mut()
            .ok_or_else(|| io::Error::other("manager observer custody is unavailable"))?;
        if admission["binding"] != *proof.binding() {
            return Err(io::Error::other(
                "manager admission has the wrong observation generation",
            ));
        }
        let external = admission["expected_external_input"]
            .as_u64()
            .ok_or_else(|| io::Error::other("manager admission lacks external input evidence"))?;
        crate::discord::verify_connector_input(
            target.manager,
            &action.operation,
            &action.composer,
            root.parent(),
        )?;
        proof.prepare(&action.operation, nonce, &action.text, external)?;
    }
    let _enrolled = if action.enrolled.is_null() {
        None
    } else {
        if manager_nonce.is_some() || launch.is_some() {
            return Err(io::Error::other(
                "unrelated native scopes cannot be combined",
            ));
        }
        let lock = crate::discord::enrolled_worker::authority::guard(
            target.dir,
            target.cwd,
            target.token,
            &action.operation,
            &action.composer,
            &action.enrolled,
        )?;
        if Instant::now() >= action.not_after
            || action.composer["text"] != action.text
            || action.composer["session"] != action.session
        {
            return Err(io::Error::other(
                "enrolled worker send expired before dispatch",
            ));
        }
        Some(lock)
    };
    let enrolled_input = _enrolled
        .as_ref()
        .map(|_| codex_receipt::hash(&action.operation));
    let mut receipt = json!({
        "session":action.session,"session_instance":codex_receipt::instance(target.token),
        "operation_id":action.operation,"native_session_id":observation.thread,
        "agent":"codex","state":"outcome_unknown",
    });
    if enrolled_input.is_some() {
        receipt["origin"] =
            action.enrolled["action"]["source_anchor"]["composer"]["origin"].clone();
    }
    codex_receipt::fence(
        target.dir,
        &action.operation,
        &action.session,
        target.token,
        &observation.thread,
    )?;
    codex_receipt::save(target.dir, &action.operation, &hash, &receipt)?;
    if Instant::now() >= action.not_after {
        return Ok(receipt);
    }
    if manager_nonce.is_some() {
        crate::discord::verify_connector_input(
            target.manager,
            &action.operation,
            &action.composer,
            root.parent(),
        )?;
        if action.composer["text"] != action.text || action.composer["session"] != action.session {
            return Err(io::Error::other(
                "manager shim input differs from the authorized composer",
            ));
        }
    }
    if let Some(guard) = observation.guard.as_mut() {
        guard.owner_input();
    }
    observation.fresh_no_rollout = false;
    observation.idle = false;
    let mut params = json!({"threadId":observation.thread,
        "input":[{"type":"text","text":action.text,"text_elements":[]}]});
    if let Some(binding) = launch {
        worker::codex_turn(&binding.authority, target.cwd, &mut params);
    }
    if let Some(nonce) = manager_nonce.or(enrolled_input.as_deref()) {
        params["clientUserMessageId"] = json!(nonce);
    }
    if send(ws, json!({"id":rpc,"method":"turn/start","params":params})).is_err() {
        // Prevent a prepared frame from flushing on a later read after the caller's authority ends.
        let _ = ws.get_ref().shutdown(Shutdown::Both);
        return Ok(receipt);
    }
    let deadline = Instant::now() + Duration::from_secs(4);
    while Instant::now() < deadline {
        let Ok(message) = read(ws) else { break };
        let Some(message) = message else { continue };
        if message["id"] != rpc || !message["method"].is_null() {
            if let Some(proof) = observation.proof.as_mut() {
                proof.observe(&message)?;
            }
            observe_frame(observation, &message, target);
            continue;
        }
        if message["result"].is_null() && message["error"]["code"].is_i64() {
            receipt["state"] = json!("rejected");
            observation.idle = false;
        } else if message["error"].is_null() {
            let turn = field(&message["result"]["turn"], "id");
            if !turn.is_empty() && turn.len() <= 128 && !turn.chars().any(char::is_control) {
                receipt["state"] = json!("native_accepted");
                receipt["native_turn_id"] = json!(turn);
                if let Some(input) = enrolled_input.as_deref() {
                    receipt["native_input_id"] = json!(input);
                }
                observation.turn = Some(turn.into());
                if let Some(binding) = launch {
                    observation.scheduled_turn = Some(ScheduledTurn {
                        attempt: binding.attempt_id.clone(),
                        operation: action.operation.clone(),
                        turn: turn.into(),
                    });
                }
            }
        }
        codex_receipt::save(target.dir, &action.operation, &hash, &receipt)?;
        return Ok(receipt);
    }
    Ok(receipt)
}

struct ScheduledTurn {
    attempt: String,
    operation: String,
    turn: String,
}

impl ScheduledTurn {
    fn proof(&self, thread: &Value, id: &str, target: &SemanticTarget<'_>) -> Option<Value> {
        if field(thread, "id") != id
            || !codex_receipt::canonical_thread(id)
            || std::fs::canonicalize(field(thread, "cwd")).ok().as_deref() != Some(target.cwd)
        {
            return None;
        }
        let turn = thread["turns"]
            .as_array()?
            .iter()
            .find(|turn| turn["id"] == self.turn)?;
        if !field(turn, "rootTurnId").is_empty() && turn["rootTurnId"] != self.turn {
            return None;
        }
        let mut proof = json!({"token":target.token,"thread":id,"turn":self.turn,
            "attempt":self.attempt,"operation":self.operation});
        match field(turn, "status") {
            "failed" if turn["error"]["codexErrorInfo"] == "usageLimitExceeded" => {
                proof["outcome"] = json!("usageLimitExceeded");
            }
            "completed" if turn["itemsView"].is_null() || turn["itemsView"] == "full" => {
                let mut messages = turn["items"]
                    .as_array()?
                    .iter()
                    .rev()
                    .filter(|item| item["type"] == "agentMessage");
                let latest = messages.next()?;
                let last = if latest["phase"].is_null() || latest["phase"] == "final_answer" {
                    latest
                } else {
                    messages.find(|item| item["phase"] == "final_answer")?
                };
                let text = field(last, "text");
                let id = field(last, "id");
                if id.is_empty() || id.len() > 128 || text.is_empty() || text.len() > 64 * 1024 {
                    return None;
                }
                proof["outcome"] = json!("completed");
                proof["item"] = json!(id);
                proof["text"] = json!(text);
            }
            _ => return None,
        }
        Some(proof)
    }
}

pub(super) fn observe(
    mut channel: Channel,
    state: std::sync::Arc<super::State>,
    actions: Receiver<SemanticAction>,
    manager: Value,
    guard_version: Option<String>,
) {
    let dir = &state.dir;
    let token = &state.token;
    let cwd = &state.working_dir;
    let resumed = channel.restored.is_some();
    let mut observation = channel.restored.take().unwrap_or_default();
    let target = SemanticTarget {
        cwd,
        dir,
        token,
        schedule: state.schedule.as_ref(),
        manager: &manager,
    };
    if resumed {
        persist_observation(&mut observation, &target);
    }
    let mut serial = 10;
    let mut join = None;
    let mut history = None;
    let mut next_history = Instant::now();
    let mut next_join = Some(Instant::now());
    let mut quiet = false;
    let guard_version = guard_version.filter(|version| {
        channel
            .user_agent
            .split(['/', ' ', '(', ')', ';', '\t'])
            .any(|part| part == version)
            && crate::providers::codex_context_guard::support(version)
                != crate::providers::codex_context_guard::Support::Unsupported
    });
    // 0.151.0's hidden title turn has the native ephemeral system tag;
    // current builds name it thread_title explicitly.
    observation.legacy_system_helper = guard_version.as_deref() == Some("0.151.0");
    loop {
        if observation.guard.is_none()
            && let Some(version) = &guard_version
            && !observation.thread.is_empty()
        {
            observation.guard =
                context_guard::Guard::bind(state.clone(), &observation.thread, version, &channel);
            persist_observation(&mut observation, &target);
        }

        if !observation.thread.is_empty()
            && !observation.ambiguous
            && !observation.joined
            && !observation.fresh_no_rollout
            && join.is_none()
            && next_join.is_some_and(|at| Instant::now() >= at)
        {
            serial += 1;
            if send(&mut channel.connection,json!({
                "id":serial,"method":"thread/resume","params":{"threadId":observation.thread,"excludeTurns":true}
            })).is_err() { break; }
            observation.fresh_no_rollout = false;
            join = Some((serial, Instant::now() + Duration::from_secs(3)));
        }
        if join.is_some_and(|(_, deadline)| Instant::now() >= deadline) {
            join = None;
            next_join = None;
        }
        if history.is_some_and(|(_, deadline)| Instant::now() >= deadline) {
            history = None;
        }
        if observation.scheduled_turn.is_some()
            && !observation.ambiguous
            && history.is_none()
            && Instant::now() >= next_history
        {
            serial += 1;
            if send(&mut channel.connection, json!({
                "id":serial,"method":"thread/read","params":{"threadId":observation.thread,"includeTurns":true},
            })).is_err() { break; }
            history = Some((serial, Instant::now() + Duration::from_secs(3)));
            next_history = Instant::now() + Duration::from_secs(1);
        }
        if quiet && let Ok(action) = actions.try_recv() {
            serial += 1;
            let result = submit(
                &mut channel.connection,
                &mut observation,
                serial,
                &action,
                &target,
            );
            let _ = action.answer.send(result);
        }
        if quiet && let Some(mut guard) = observation.guard.take() {
            let before = guard.view();
            let result = guard.tick(&mut channel, &observation, &mut serial);
            let changed = before != guard.view();
            observation.guard = Some(guard);
            if changed {
                persist_observation(&mut observation, &target);
            }
            if result.is_err() {
                break;
            }
        }
        let reading = Instant::now();
        match read(&mut channel.connection) {
            Ok(Some(message)) => {
                quiet = false;
                if history.is_some_and(|(id, _)| message["id"] == id) && message["method"].is_null()
                {
                    history = None;
                    if let Some(turn) = &observation.scheduled_turn
                        && let Some(result) =
                            turn.proof(&message["result"]["thread"], &observation.thread, &target)
                        && runtime::write_json(&target.dir.join("schedule-finish.json"), &result)
                            .is_ok()
                    {
                        if result["outcome"] == "completed" {
                            observation.turn = None;
                            observation.idle = true;
                        }
                        observation.scheduled_turn = None;
                        persist_observation(&mut observation, &target);
                    }
                } else if join.is_some_and(|(id, _)| message["id"] == id)
                    && message["method"].is_null()
                {
                    join = None;
                    if message["error"].is_null()
                        && field(&message["result"]["thread"], "id") == observation.thread
                    {
                        observation.joined = true;
                        observation.missing_rollout = false;
                        observation.idle = observation.turn.is_none()
                            && field(&message["result"]["thread"]["status"], "type") == "idle";
                        persist_observation(&mut observation, &target);
                        backfill_initial_history(&observation, &target);
                    } else if message["result"].is_null()
                        && message["error"]["code"] == -32600
                        && field(&message["error"], "message")
                            == format!("no rollout found for thread id {}", observation.thread)
                    {
                        observation.missing_rollout = true;
                        observation.fresh_no_rollout = manager["resume_request"].is_null()
                            && observation
                                .proof
                                .as_ref()
                                .is_some_and(|proof| proof.is_virgin());
                        persist_observation(&mut observation, &target);
                        next_join = Some(Instant::now() + Duration::from_secs(1));
                        persist_observation(&mut observation, &target);
                    } else {
                        next_join = None;
                    }
                } else {
                    observe_frame(&mut observation, &message, &target);
                    if !manager.is_null() && !observation.thread.is_empty() {
                        if manager["resume_request"]["binding"]["native_session_id"]
                            .as_str()
                            .is_some_and(|expected| expected != observation.thread)
                        {
                            break;
                        }
                        if observation.proof.is_none() {
                            let binding = json!({
                                "session":format!("{}/{}", field(&manager,"endpoint_id"),field(&manager,"session_id")),
                                "session_instance":codex_receipt::instance(token),
                                "conversation_id":manager["launch"]["manager"]["conversation_id"],
                                "provider":"codex","native_session_id":observation.thread,
                                "native_generation":format!("{}:{}",channel.pid,channel.birth),
                                "observation_generation":codex_receipt::hash(&format!("{}:{}:{}",token,channel.pid,channel.birth)),
                                "login_epoch":manager["account"]
                            });
                            let Ok(proof) = codex_proof::Ledger::open(dir, &binding) else {
                                break;
                            };
                            if runtime::write_json(&dir.join("manager-binding.json"),&json!({
                                "binding":binding,"pid":channel.pid,"birth":channel.birth,"token":token
                            })).is_err() { break; }
                            observation.proof = Some(proof);
                        }
                        if let Some(proof) = observation.proof.as_mut()
                            && proof.observe(&message).is_err()
                        {
                            break;
                        }
                    }
                }
            }
            Ok(None) => {
                quiet = reading.elapsed() >= Duration::from_millis(350);
            }
            Err(_) => break,
        }
    }
    let mut terminal = json!({
        "token":token,"conversation_id":observation.thread,"rollout":observation.rollout,
        "status":{"Process":"running","Turn":"unknown","Interaction":"none"},
        "backend_available":false,
    });
    if let Some(guard) = observation.guard.as_mut() {
        guard.disconnected();
        terminal["context_guard"] = guard.view();
    }
    let _ = runtime::write_json(&dir.join("observation.json"), &terminal);
    let _ = channel.connection.get_ref().shutdown(Shutdown::Both);
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn scheduled_finish_is_bound_to_native_root_turn_and_last_assistant_item() {
        let cwd = std::env::current_dir().unwrap().canonicalize().unwrap();
        let id = "019dc0a0-6bd5-71e1-9bd7-629de956bfe4";
        let target = SemanticTarget {
            cwd: &cwd,
            dir: &cwd,
            token: "fixture",
            schedule: None,
            manager: &Value::Null,
        };
        let turn = ScheduledTurn {
            attempt: "attempt".into(),
            operation: "operation".into(),
            turn: "owned".into(),
        };
        let mut thread = json!({"id":id,"cwd":cwd,"turns":[{
            "id":"owned","rootTurnId":"different","status":"completed","itemsView":"full",
            "items":[{"type":"agentMessage","id":"earlier","text":"First result"},
                {"type":"agentMessage","id":"latest","text":"Corrected result"},
                {"type":"mcpToolCall","id":"receipt","text":"First result"}],
        }]});
        assert!(turn.proof(&thread, id, &target).is_none());
        thread["turns"][0]["rootTurnId"] = json!("owned");
        assert_eq!(
            turn.proof(&thread, id, &target).unwrap()["text"],
            "Corrected result"
        );
        thread["turns"][0]["items"][1]["phase"] = json!("commentary");
        assert!(turn.proof(&thread, id, &target).is_none());
        thread["turns"][0]["items"][0]["phase"] = json!("partial_answer");
        assert!(turn.proof(&thread, id, &target).is_none());
        thread["turns"][0]["items"][1]["phase"] = json!("final_answer");
        assert_eq!(
            turn.proof(&thread, id, &target).unwrap()["text"],
            "Corrected result"
        );
        thread["turns"][0]["itemsView"] = json!("summary");
        assert!(turn.proof(&thread, id, &target).is_none());
        thread["turns"][0]["status"] = json!("failed");
        thread["turns"][0]["error"] = json!({"codexErrorInfo":"usageLimitExceeded"});
        assert_eq!(
            turn.proof(&thread, id, &target).unwrap()["outcome"],
            "usageLimitExceeded"
        );
        thread["id"] = json!("019dc0a0-6bd5-71e1-9bd7-629de956bfe5");
        assert!(turn.proof(&thread, id, &target).is_none());
    }

    #[test]
    fn manager_nonce_does_not_bypass_failed_or_unknown_join() {
        let mut observation = Observation::default();
        assert!(!observation.can_enter_joined_thread(true));
        observation.fresh_no_rollout = true;
        assert!(
            !observation.can_enter_joined_thread(true),
            "volatile exception requires authoritative proof"
        );
    }

    #[test]
    fn observed_turn_permanently_revokes_unjoined_manager_exception() {
        let root =
            crate::test_temp::root().join(format!("virgin-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&root).unwrap();
        let binding = json!({"native_session_id":"native-thread"});
        let frame = |method, thread| {
            json!({"method":method,"params":{"threadId":thread,
            "turn":{"id":"exact-turn","rootTurnId":"exact-turn","status":"completed"}}})
        };
        for methods in [
            &["turn/started", "turn/completed"][..],
            &["turn/completed"][..],
        ] {
            let mut observation = Observation {
                thread: "native-thread".into(),
                idle: true,
                fresh_no_rollout: true,
                proof: Some(codex_proof::Ledger::open(&root, &binding).unwrap()),
                ..Observation::default()
            };
            assert!(observation.can_enter_joined_thread(true));
            let foreign = frame("turn/completed", "other-thread");
            assert!(!observation.apply(&foreign, &root));
            observation
                .proof
                .as_mut()
                .unwrap()
                .observe(&foreign)
                .unwrap();
            assert!(observation.can_enter_joined_thread(true));

            for method in methods {
                let event = frame(*method, "native-thread");
                assert!(observation.apply(&event, &root));
                observation.proof.as_mut().unwrap().observe(&event).unwrap();
                assert!(!observation.fresh_no_rollout);
                assert!(!observation.can_enter_joined_thread(true));
            }
            assert!(observation.idle);
            observation.proof = Some(codex_proof::Ledger::open(&root, &binding).unwrap());
            observation.fresh_no_rollout = true;
            assert!(
                !observation.can_enter_joined_thread(true),
                "reopened ledger must veto a stale exception"
            );
            observation.joined = true;
            assert!(observation.can_enter_joined_thread(true));
            std::fs::remove_file(root.join("manager-proof.json")).unwrap();
        }
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn accepts_only_bound_thread_and_turn() {
        let cwd = std::env::current_dir().unwrap().canonicalize().unwrap();
        let id = "019dc0a0-6bd5-71e1-9bd7-629de956bfe4";
        let mut native = Observation::default();
        assert!(!native.apply(
            &json!({"method":"thread/started","params":{"thread":{"id":id,"cwd":"/wrong"}}}),
            &cwd
        ));
        assert!(native.apply(
            &json!({"method":"thread/started","params":{"thread":{"id":id,"cwd":cwd}}}),
            &cwd
        ));
        assert!(native.apply(
            &json!({"method":"turn/started","params":{"threadId":id,"turn":{"id":"turn"}}}),
            &cwd
        ));
        assert!(!native.apply(
            &json!({"method":"turn/completed","params":{"threadId":id,"turn":{"id":"stale"}}}),
            &cwd
        ));
        assert_eq!(native.status()["Turn"], "active");
        assert!(native.apply(&json!({"id":20,"method":"item/commandExecution/requestApproval","params":{"threadId":id}}), &cwd));
        assert_eq!(native.status()["Attention"], true);
        assert!(native.apply(
            &json!({"method":"serverRequest/resolved","params":{"threadId":id,"requestId":20}}),
            &cwd
        ));
        assert!(native.apply(
            &json!({"method":"turn/completed","params":{"threadId":id,"turn":{"id":"turn"}}}),
            &cwd
        ));
        assert_eq!(native.status()["Turn"], "idle");
        assert!(native.apply(&json!({"method":"thread/status/changed","params":{"threadId":id,"status":{"type":"active"}}}), &cwd));
        assert!(!native.idle);
    }
}
