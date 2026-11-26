use super::{Process, read_at};
use crate::{ipc::field, providers::codex_receipt::canonical_thread, supervisor::runtime};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{
    fs::{self, File},
    io,
    os::unix::{
        ffi::OsStrExt,
        fs::{FileTypeExt, MetadataExt},
        net::UnixStream,
    },
    path::{Path, PathBuf},
    time::Instant,
};
use tungstenite::{Message, WebSocket, client::client_with_config, protocol::WebSocketConfig};

pub(super) fn owned_backend(
    path: &Path,
    directory: &File,
    launch: &Value,
    native: &Value,
) -> io::Result<Process> {
    let backend = read_at(directory, c"backend.json")?;
    let expected = path.join("codex.sock");
    if Path::new(field(launch, "backend_socket_path")) != expected
        || Path::new(field(&backend, "socket_path")) != expected
        || backend["pid"] != backend["pgid"]
        || backend["pid"] != native["backend_pid"]
        || backend["started_at_ms"] != native["backend_start_time"]
        || backend["pid"] == native["pid"]
        || backend["pid"] == native["shim_pid"]
        || native["runtime"] != json!({"provider":"","epoch":0,"identity":"","home":""})
    {
        return Err(io::Error::other("Go Codex backend custody disagrees"));
    }
    Process::new(&backend["pid"], &backend["started_at_ms"])
}

pub(super) fn verify_thread(dir: &Path, backend: &Process, meta: &Value) -> io::Result<()> {
    let thread_id = field(meta, "conversation_id");
    if !canonical_thread(thread_id) {
        return Err(io::Error::other("Go Codex conversation is unproved"));
    }
    let endpoint = dir.join("codex.sock");
    let uid = dir.metadata()?.uid();
    let version = field(&meta["cli_identity"], "version");
    let (path, socket) = socket_target(&endpoint, uid, version)?;
    backend.matches()?;
    let stream = UnixStream::connect(&path)?;
    let (confirmed_path, confirmed_socket) = socket_target(&endpoint, uid, version)?;
    if confirmed_path != path
        || (confirmed_socket.dev(), confirmed_socket.ino()) != (socket.dev(), socket.ino())
    {
        return Err(io::Error::other(
            "Go Codex backend socket changed during verification",
        ));
    }
    stream.set_read_timeout(Some(runtime::IO_TIMEOUT))?;
    stream.set_write_timeout(Some(runtime::IO_TIMEOUT))?;
    if runtime::peer_pid(&stream)? != backend.pid {
        return Err(io::Error::other("Go Codex backend peer identity mismatch"));
    }
    backend.matches()?;
    let config = WebSocketConfig::default()
        .max_message_size(Some(1024 * 1024))
        .max_frame_size(Some(1024 * 1024));
    let (mut wire, _) = client_with_config("ws://localhost/rpc", stream, Some(config))
        .map_err(|_| io::Error::other("Go Codex backend WebSocket handshake failed"))?;
    let initialized = request(
        &mut wire,
        1,
        "initialize",
        json!({"clientInfo":{"name":"swarm","title":"Swarm","version":"1"},
            "capabilities":{"experimentalApi":true,"requestAttestation":false}}),
    )?;
    if !initialized.is_object() {
        return Err(io::Error::other(
            "Go Codex backend initialization was invalid",
        ));
    }
    send(&mut wire, json!({"method":"initialized","params":{}}))?;
    let loaded = request(&mut wire, 2, "thread/loaded/list", json!({}))?;
    // The app-server can also have guardian and subagent threads. Match the
    // persisted root exactly once, like Go's same-instance rejoin.
    if !loaded["data"].as_array().is_some_and(|ids| {
        ids.iter().filter(|id| *id == thread_id).count() == 1
            && ids
                .iter()
                .all(|id| id.as_str().is_some_and(canonical_thread))
    }) {
        return Err(io::Error::other("Go Codex loaded conversation disagrees"));
    }
    let read = request(
        &mut wire,
        3,
        "thread/read",
        json!({"threadId":thread_id,"includeTurns":false}),
    )?;
    if read["thread"]["id"] != thread_id || read["thread"]["cwd"] != meta["cwd"] {
        return Err(io::Error::other("Go Codex conversation checkout disagrees"));
    }
    backend.matches()
}

fn socket_target(endpoint: &Path, uid: u32, version: &str) -> io::Result<(PathBuf, fs::Metadata)> {
    let invalid = || io::Error::other("unproven Go Codex backend socket");
    let original = endpoint.symlink_metadata()?;
    if original.uid() != uid {
        return Err(invalid());
    }
    if original.file_type().is_socket() {
        // The stock direct listener is already inside the validated private session directory.
        return Ok((endpoint.to_owned(), original));
    }
    if !original.is_symlink() || version != "0.172.0" {
        return Err(invalid());
    }
    // Codex 0.172.0 hashes the exact requested pathname and puts a mode-0600
    // listener into its per-UID private directory. Never connect via a mutable alias.
    let root = PathBuf::from(format!("/tmp/codex-daemon-{uid}"));
    let name = format!("{:x}", Sha256::digest(endpoint.as_os_str().as_bytes()));
    let expected = root.join(name);
    let directory = root.symlink_metadata()?;
    if fs::read_link(endpoint)? != expected
        || !directory.is_dir()
        || directory.uid() != uid
        || directory.mode() & 0o777 != 0o700
    {
        return Err(invalid());
    }
    let socket = expected.symlink_metadata()?;
    if !socket.file_type().is_socket() || socket.uid() != uid || socket.mode() & 0o777 != 0o600 {
        return Err(invalid());
    }
    Ok((expected, socket))
}

fn send(wire: &mut WebSocket<UnixStream>, request: Value) -> io::Result<()> {
    wire.send(Message::Text(request.to_string().into()))
        .map_err(|_| io::Error::other("Go Codex backend probe write failed"))
}

fn request(
    wire: &mut WebSocket<UnixStream>,
    id: u64,
    method: &str,
    params: Value,
) -> io::Result<Value> {
    send(wire, json!({"id":id,"method":method,"params":params}))?;
    let deadline = Instant::now() + runtime::IO_TIMEOUT;
    for _ in 0..256 {
        if Instant::now() >= deadline {
            break;
        }
        let frame = wire
            .read()
            .map_err(|_| io::Error::other("Go Codex backend probe read failed"))?;
        if let Message::Text(text) = frame {
            let reply: Value = serde_json::from_str(&text)?;
            if reply["id"] == id && reply.get("method").is_none() {
                return reply
                    .get("result")
                    .filter(|_| reply.get("error").is_none())
                    .cloned()
                    .ok_or_else(|| io::Error::other("Go Codex backend probe was refused"));
            }
        }
    }
    Err(io::Error::other("Go Codex backend probe timed out"))
}
