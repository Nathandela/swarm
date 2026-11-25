use super::pty;
use crate::ipc::field;
use crate::supervisor::runtime;
use serde_json::{Value, json};
use std::io;
use std::net::Shutdown;
use std::os::unix::{net::UnixStream, process::CommandExt};
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
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
    requests: std::collections::BTreeSet<String>,
    idle: bool,
}

impl Observation {
    fn apply(&mut self, message: &Value, cwd: &Path) -> bool {
        let payload = &message["params"];
        let method = field(message, "method");
        if method == "thread/started" {
            let thread = &payload["thread"];
            let id = field(thread, "id");
            let root = field(thread, "cwd");
            if self.thread.is_empty()
                && canonical_id(id)
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
            "thread/status/changed" if field(&payload["status"], "type") == "idle" => {
                if self.turn.is_some() {
                    return false;
                }
                self.idle = true;
            }
            "item/commandExecution/requestApproval"
            | "item/fileChange/requestApproval"
            | "item/tool/requestUserInput" => {
                if self.requests.len() >= 128
                    || !(message["id"].is_number() || message["id"].is_string())
                {
                    return false;
                }
                self.requests.insert(message["id"].to_string());
            }
            "serverRequest/resolved" => {
                self.requests.remove(&payload["requestId"].to_string());
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

fn canonical_id(id: &str) -> bool {
    id.len() == 36
        && id.bytes().enumerate().all(|(i, b)| {
            if matches!(i, 8 | 13 | 18 | 23) {
                b == b'-'
            } else {
                b.is_ascii_digit() || (b'a'..=b'f').contains(&b)
            }
        })
}

pub(super) fn observe(mut channel: Channel, cwd: PathBuf, dir: PathBuf, token: String) {
    let mut observation = Observation::default();
    while let Ok(message) = read(&mut channel.connection) {
        if let Some(message) = message
            && observation.apply(&message, &cwd)
        {
            let _ = runtime::write_json(
                &dir.join("observation.json"),
                &json!({
                    "token":token, "conversation_id":observation.thread, "status":observation.status()
                }),
            );
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
    }
}
