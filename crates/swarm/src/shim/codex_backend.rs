use super::pty;
use crate::ipc::field;
use crate::providers::codex_receipt;
use crate::supervisor::runtime;
use serde_json::{Value, json};
use std::io;
use std::net::Shutdown;
use std::os::unix::{net::UnixStream, process::CommandExt};
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::sync::mpsc::{Receiver, SyncSender};
use std::thread;
use std::time::{Duration, Instant};
use tungstenite::{
    Error, Message, WebSocket, client::client_with_config, protocol::WebSocketConfig,
};

pub(super) struct Backend {
    child: Child,
}

impl Drop for Backend {
    fn drop(&mut self) {
        // Retain the unreaped group leader through containment.
        let _ = pty::signal_group(&self.child, libc::SIGKILL);
        let _ = self.child.wait();
    }
}

pub(super) struct Channel {
    pub endpoint: String,
    connection: WebSocket<UnixStream>,
}

pub(super) fn start(
    executable: &Path,
    cwd: &Path,
    dir: &Path,
    env: &[(String, String)],
) -> io::Result<(Backend, Channel)> {
    let socket = dir.join("b");
    let endpoint = format!("unix://{}", socket.display());
    let child = Command::new(executable)
        .args([
            "app-server",
            "--listen",
            &endpoint,
            "-c",
            "sandbox_workspace_write.network_access=true",
        ])
        .current_dir(cwd)
        .env_clear()
        .envs(env.iter().map(|(key, value)| (key, value)))
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .process_group(0)
        .spawn()?;
    let backend = Backend { child };
    let deadline = Instant::now() + Duration::from_secs(4);
    let stream = loop {
        if let Ok(stream) = UnixStream::connect(&socket) {
            if runtime::peer_pid(&stream)? != backend.child.id() {
                return Err(io::Error::other(
                    "Codex app-server peer does not match owned child",
                ));
            }
            break stream;
        }
        if Instant::now() >= deadline || pty::child_exited(&backend.child)? {
            return Err(io::Error::other(
                "Codex app-server did not bind its private socket",
            ));
        }
        thread::sleep(Duration::from_millis(20));
    };
    stream.set_read_timeout(Some(Duration::from_millis(400)))?;
    stream.set_write_timeout(Some(Duration::from_millis(400)))?;
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
    let deadline = Instant::now() + Duration::from_secs(4);
    loop {
        if let Some(message) = read(&mut connection)?
            && message["id"] == 1
        {
            if !message["result"].is_object() {
                return Err(io::Error::other("Codex app-server refused to initialize"));
            }
            break;
        }
        if Instant::now() >= deadline {
            return Err(io::Error::other(
                "Codex app-server initialization timed out",
            ));
        }
    }
    send(&mut connection, json!({"method":"initialized","params":{}}))?;
    Ok((
        backend,
        Channel {
            endpoint,
            connection,
        },
    ))
}

fn send(ws: &mut WebSocket<UnixStream>, message: Value) -> io::Result<()> {
    ws.send(Message::Text(message.to_string().into()))
        .map_err(|_| io::Error::other("Codex app-server write outcome is unknown"))
}

fn read(ws: &mut WebSocket<UnixStream>) -> io::Result<Option<Value>> {
    match ws.read() {
        Ok(Message::Text(message)) => Ok(Some(serde_json::from_str(message.as_str())?)),
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
    thread: String,
    turn: Option<String>,
    requests: std::collections::BTreeMap<String, &'static str>,
    joined: bool,
    requests_unverified: bool,
    idle: bool,
    ambiguous: bool,
}

impl Observation {
    fn apply(&mut self, message: &Value, cwd: &Path) -> bool {
        let payload = &message["params"];
        let method = field(message, "method");
        if method == "thread/started" {
            let thread = &payload["thread"];
            let id = field(thread, "id");
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
        match method {
            "turn/started" => {
                let id = field(&payload["turn"], "id");
                if id.is_empty() {
                    return false;
                }
                self.turn = Some(id.to_owned());
                self.idle = false;
            }
            "turn/completed" => {
                if self.turn.as_deref() != Some(field(&payload["turn"], "id")) {
                    return false;
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
                let kind = match method {
                    "item/commandExecution/requestApproval" => "command",
                    "item/fileChange/requestApproval" => "file_change",
                    _ => "user_input",
                };
                self.requests.insert(message["id"].to_string(), kind);
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
    pub not_after: Instant,
    pub answer: SyncSender<io::Result<Value>>,
}

struct SemanticTarget<'a> {
    dir: &'a Path,
    token: &'a str,
    cwd: &'a Path,
}

fn persist_observation(observation: &Observation, target: &SemanticTarget<'_>) {
    let requests: Vec<_> = observation.requests.iter().map(|(id, kind)| {
        let public_id = serde_json::from_str::<String>(id).unwrap_or_else(|_| id.clone());
        let (category, summary) = match *kind {
            "command" => ("permission","Command execution approval"),
            "file_change" => ("permission","File change approval"),
            _ => ("question","User input request"),
        };
        json!({"kind":category,"request_id":public_id,"native_session_id":observation.thread,"summary":summary})
    }).collect();
    let _ = runtime::write_json(
        &target.dir.join("observation.json"),
        &json!({
            "token":target.token, "conversation_id":observation.thread, "status":observation.status(),
            "backend_available":!observation.ambiguous, "requests_available":observation.joined && !observation.ambiguous && !observation.requests_unverified,
            "requests":requests,
        }),
    );
}

fn observe_frame(observation: &mut Observation, message: &Value, target: &SemanticTarget<'_>) {
    if observation.apply(message, target.cwd) {
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
    let hash = codex_receipt::hash(&action.text);
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
    if observation.thread.is_empty()
        || observation.ambiguous
        || !observation.joined
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
    let mut receipt = json!({
        "session":action.session,"session_instance":codex_receipt::instance(target.token),
        "operation_id":action.operation,"native_session_id":observation.thread,
        "agent":"codex","state":"outcome_unknown",
    });
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
    observation.idle = false;
    if send(
        ws,
        json!({
            "id":rpc,"method":"turn/start","params":{"threadId":observation.thread,
                "input":[{"type":"text","text":action.text,"text_elements":[]}]}
        }),
    )
    .is_err()
    {
        // Prevent a prepared frame from flushing on a later read after the caller's authority ends.
        let _ = ws.get_ref().shutdown(Shutdown::Both);
        return Ok(receipt);
    }
    let deadline = Instant::now() + Duration::from_secs(4);
    while Instant::now() < deadline {
        let Ok(message) = read(ws) else { break };
        let Some(message) = message else { continue };
        if message["id"] != rpc || !message["method"].is_null() {
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
                observation.turn = Some(turn.into());
            }
        }
        codex_receipt::save(target.dir, &action.operation, &hash, &receipt)?;
        return Ok(receipt);
    }
    Ok(receipt)
}

pub(super) fn observe(
    mut channel: Channel,
    cwd: PathBuf,
    dir: PathBuf,
    token: String,
    actions: Receiver<SemanticAction>,
) {
    let mut observation = Observation::default();
    let target = SemanticTarget {
        cwd: &cwd,
        dir: &dir,
        token: &token,
    };
    let mut serial = 10;
    let mut join = None;
    let mut next_join = Some(Instant::now());
    let mut quiet = false;
    loop {
        if !observation.thread.is_empty()
            && !observation.ambiguous
            && !observation.joined
            && join.is_none()
            && next_join.is_some_and(|at| Instant::now() >= at)
        {
            serial += 1;
            if send(&mut channel.connection,json!({
                "id":serial,"method":"thread/resume","params":{"threadId":observation.thread,"excludeTurns":true}
            })).is_err() { break; }
            join = Some((serial, Instant::now() + Duration::from_secs(3)));
        }
        if join.is_some_and(|(_, deadline)| Instant::now() >= deadline) {
            join = None;
            next_join = None;
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
        let reading = Instant::now();
        match read(&mut channel.connection) {
            Ok(Some(message)) => {
                quiet = false;
                if join.is_some_and(|(id, _)| message["id"] == id) && message["method"].is_null() {
                    join = None;
                    if field(&message["result"]["thread"], "id") == observation.thread {
                        observation.joined = true;
                        observation.idle = observation.turn.is_none()
                            && field(&message["result"]["thread"]["status"], "type") == "idle";
                        persist_observation(&observation, &target);
                    } else if field(&message["error"], "message")
                        .contains("no rollout found for thread id")
                    {
                        next_join = Some(Instant::now() + Duration::from_secs(1));
                    } else {
                        next_join = None;
                    }
                } else {
                    observe_frame(&mut observation, &message, &target);
                }
            }
            Ok(None) => {
                quiet = reading.elapsed() >= Duration::from_millis(350);
            }
            Err(_) => break,
        }
    }
    let _ = runtime::write_json(
        &dir.join("observation.json"),
        &json!({
            "token":token, "conversation_id":observation.thread,
            "status":{"Process":"running","Turn":"unknown","Interaction":"none"},
            "backend_available":false,
        }),
    );
    let _ = channel.connection.get_ref().shutdown(Shutdown::Both);
}

#[cfg(test)]
mod tests {
    use super::*;

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
