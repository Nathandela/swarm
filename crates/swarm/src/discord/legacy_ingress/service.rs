//! An original personal enrollment under a single stopped-connector handoff.
use super::*;
use crate::discord::{dispatcher::read_tool_bytes, gateway, rest::Discord};
use std::{
    io::{Read, Write},
    os::unix::net::UnixListener,
    time::{Duration, Instant},
};

#[derive(serde::Deserialize, serde::Serialize)]
#[serde(deny_unknown_fields)]
struct ManagerRequest {
    version: u16,
    conversation_id: String,
    session_id: String,
    call: ManagerCall,
}

#[derive(serde::Deserialize, serde::Serialize)]
#[serde(deny_unknown_fields)]
struct ManagerCall {
    name: String,
    meta: Value,
    arguments: Final,
}

#[derive(serde::Deserialize, serde::Serialize)]
#[serde(deny_unknown_fields)]
struct Final {
    output_id: String,
    sequence: u64,
    text: String,
    #[serde(rename = "final")]
    final_reply: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    file_ids: Option<Vec<String>>,
}

struct Bridge {
    listener: UnixListener,
    socket: PathBuf,
    identity: (u64, u64),
    credential: String,
    _lock: fs::File,
}

impl Bridge {
    fn open(place: &Place, cfg: &Config, binding: &Value) -> io::Result<Self> {
        let (directory, id, _) = voice_location(place, cfg, binding)?;
        let auth = open_owner_file(&directory.join("bridge.auth"))?;
        let mut credential = String::new();
        auth.take(65).read_to_string(&mut credential)?;
        if credential.len() != 64
            || !credential
                .bytes()
                .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
        {
            return Err(invalid());
        }
        let root = fs::canonicalize("/tmp")?
            .join(format!("swarm-dm-{}", unsafe { libc::getuid() }))
            .join(&id[..32]);
        if fs::canonicalize(&root)? != root {
            return Err(invalid());
        }
        runtime::private_dir(&root)?;
        let lock = runtime::lock(&root.join("controller.lock"))?;
        let socket = root.join("bridge.sock");
        runtime::reclaim_socket(&socket)?;
        let listener = runtime::bind(&socket)?;
        let metadata = fs::symlink_metadata(&socket)?;
        listener.set_nonblocking(true)?;
        Ok(Self {
            listener,
            socket,
            identity: (metadata.dev(), metadata.ino()),
            credential,
            _lock: lock,
        })
    }

    fn accept(
        &self,
        place: &Place,
        cfg: &Config,
        lease: &mut ConnectorLease,
        c: &Candidate,
        source: impl FnOnce() -> io::Result<Value>,
    ) -> io::Result<Option<String>> {
        self.accept_request(|request| {
            if original_revision(&source()?, &c.origin)? != c.origin["revision"] {
                return Err(invalid());
            }
            manager_publication::accept(place, cfg, lease, c, &request)
        })
    }

    fn accept_request(
        &self,
        handle: impl FnOnce(Value) -> io::Result<Value>,
    ) -> io::Result<Option<String>> {
        let mut connection = match self.listener.accept() {
            Ok((connection, _)) => connection,
            Err(error) if error.kind() == io::ErrorKind::WouldBlock => return Ok(None),
            Err(error) => return Err(error),
        };
        let result = read_tool_bytes(&connection, &self.credential).and_then(|raw| {
            let request: ManagerRequest = serde_json::from_slice(&raw)?;
            let request = serde_json::to_value(request)?;
            handle(request)
        });
        let output = result
            .as_ref()
            .ok()
            .map(|value| field(value, "id").to_owned());
        let reply = match result {
            Ok(value) => json!({"data":value}),
            Err(_) => json!({"error":"unconfirmed"}),
        };
        let bytes = serde_json::to_vec(&reply)?;
        connection.set_write_timeout(Some(Duration::from_secs(2)))?;
        let _ = write!(connection,"HTTP/1.0 200 OK\r\nContent-Type: application/json\r\nCache-Control: no-store\r\nContent-Length: {}\r\n\r\n",bytes.len())
            .and_then(|_| connection.write_all(&bytes));
        Ok(output)
    }
}

impl Drop for Bridge {
    fn drop(&mut self) {
        if fs::symlink_metadata(&self.socket)
            .is_ok_and(|meta| (meta.dev(), meta.ino()) == self.identity)
        {
            let _ = fs::remove_file(&self.socket);
        }
    }
}

struct StatusSocket {
    socket: PathBuf,
    inode: (u64, u64),
    connected: std::sync::Arc<std::sync::atomic::AtomicBool>,
    requested: std::sync::Arc<std::sync::atomic::AtomicBool>,
    stop: std::sync::mpsc::Sender<()>,
    worker: Option<std::thread::JoinHandle<()>>,
}

impl StatusSocket {
    fn start(place: &Place, cfg: &Config) -> io::Result<Self> {
        let socket = place.dir.join("bridge.sock");
        match fs::symlink_metadata(&socket) {
            Err(error) if error.kind() == io::ErrorKind::NotFound => {}
            Err(error) => return Err(error),
            Ok(previous) => {
                use std::os::unix::fs::FileTypeExt;
                if !previous.file_type().is_socket()
                    || previous.nlink() != 1
                    || previous.uid() != unsafe { libc::geteuid() }
                    || previous.mode() & 0o777 != 0o600
                {
                    return Err(invalid());
                }
                match crate::discord::local::connect_available(&socket) {
                    Err(error) if error.kind() == io::ErrorKind::ConnectionRefused => {
                        let current = fs::symlink_metadata(&socket)?;
                        if (previous.dev(), previous.ino()) != (current.dev(), current.ino()) {
                            return Err(invalid());
                        }
                        fs::remove_file(&socket)?;
                    }
                    _ => return Err(invalid()),
                }
            }
        }
        let listener = runtime::bind(&socket)?;
        let meta = fs::symlink_metadata(&socket)?;
        listener.set_nonblocking(true)?;
        let (stop, exit) = std::sync::mpsc::channel();
        let connected = std::sync::Arc::new(std::sync::atomic::AtomicBool::new(false));
        let live = connected.clone();
        let requested = std::sync::Arc::new(std::sync::atomic::AtomicBool::new(false));
        let shutdown = requested.clone();
        let identity = Sha256::digest(cfg.text("installation_id").as_bytes());
        let stop_path = format!("/installation-v1/{identity:x}/shutdown");
        let mut initial = cfg.status();
        let worker = std::thread::spawn(move || {
            loop {
                use std::sync::mpsc::RecvTimeoutError;
                match exit.recv_timeout(Duration::from_millis(20)) {
                    Ok(()) | Err(RecvTimeoutError::Disconnected) => return,
                    Err(RecvTimeoutError::Timeout) => {}
                }
                let Ok((mut stream, _)) = listener.accept() else {
                    continue;
                };
                use std::io::BufRead;
                let mut line = String::new();
                let deadline = Instant::now() + Duration::from_millis(250);
                let mut reader = std::io::BufReader::new(
                    crate::socket::DeadlineReader(&stream, deadline).take(512),
                );
                let reply = if reader.read_line(&mut line).is_ok()
                    && matches!(
                        line.as_str(),
                        "GET /status HTTP/1.0\r\n" | "GET /status HTTP/1.1\r\n"
                    ) {
                    initial["connected"] = json!(live.load(std::sync::atomic::Ordering::Acquire));
                    Some(initial.clone())
                } else if line == format!("POST {stop_path} HTTP/1.0\r\n")
                    && read_shutdown(&mut reader)
                {
                    shutdown.store(true, std::sync::atomic::Ordering::Release);
                    live.store(false, std::sync::atomic::Ordering::Release);
                    Some(json!({"stopped":true}))
                } else {
                    None
                };
                let _ = stream.set_write_timeout(Some(Duration::from_millis(250)));
                let Some(reply) = reply else {
                    let _ =
                        stream.write_all(b"HTTP/1.0 404 Not Found\r\nContent-Length: 0\r\n\r\n");
                    continue;
                };
                let Ok(body) = serde_json::to_vec(&reply) else {
                    continue;
                };
                let _ = write!(stream, "HTTP/1.0 200 OK\r\nContent-Type: application/json\r\nCache-Control: no-store\r\nContent-Length: {}\r\n\r\n", body.len())
                .and_then(|_| stream.write_all(&body));
            }
        });
        Ok(Self {
            socket,
            inode: (meta.dev(), meta.ino()),
            connected,
            requested,
            stop,
            worker: Some(worker),
        })
    }

    fn stopping(&self) -> bool {
        self.requested.load(std::sync::atomic::Ordering::Acquire)
    }

    fn online(&self, value: bool) {
        self.connected.store(
            value && !self.stopping(),
            std::sync::atomic::Ordering::Release,
        );
    }
}

fn read_shutdown(input: &mut impl std::io::BufRead) -> bool {
    let mut host = false;
    let mut size = false;
    let mut content = false;
    let mut line = String::new();
    loop {
        line.clear();
        if input.read_line(&mut line).ok().is_none_or(|n| n == 0) {
            return false;
        }
        match line.as_str() {
            "\r\n" => break,
            "Host: local\r\n" if !host => host = true,
            "Content-Length: 2\r\n" if !size => size = true,
            "Content-Type: application/json\r\n" if !content => content = true,
            "Connection: close\r\n" => {}
            _ => return false,
        }
    }
    let mut body = [0; 2];
    host && size && content && input.read_exact(&mut body).is_ok() && body == *b"{}"
}

impl Drop for StatusSocket {
    fn drop(&mut self) {
        let _ = self.stop.send(());
        if let Some(worker) = self.worker.take() {
            let _ = worker.join();
        }
        if fs::symlink_metadata(&self.socket)
            .is_ok_and(|meta| (meta.dev(), meta.ino()) == self.inode)
        {
            let _ = fs::remove_file(&self.socket);
        }
    }
}

fn enrolled_dm<'a>(cfg: &Config, message: &'a Value) -> Option<&'a str> {
    let id = message["id"].as_str().filter(|id| snowflake(id))?;
    if message["author"]["id"] != cfg.text("owner_id")
        || message["channel_id"] != cfg.text("dm_channel_id")
        || !message["guild_id"].is_null()
        || !message["webhook_id"].is_null()
        || message["author"]
            .get("bot")
            .is_some_and(|flag| flag != false)
    {
        return None;
    }
    Some(id)
}

pub(super) trait Room {
    fn verify(&self, cfg: &Config) -> io::Result<()>;
    fn history(&self, cfg: &Config) -> io::Result<Vec<Value>>;
    fn message(&self, cfg: &Config, id: &str) -> io::Result<Value>;
    fn post(
        &self,
        cfg: &Config,
        nonce: &str,
        body: &str,
        files: &[crate::supervisor::mail_files::Captured],
    ) -> crate::discord::rest::Post;
}

impl Room for Discord {
    fn verify(&self, cfg: &Config) -> io::Result<()> {
        self.verify_app(cfg)?;
        self.verify_dm(cfg, None)?;
        Ok(())
    }

    fn history(&self, cfg: &Config) -> io::Result<Vec<Value>> {
        self.owner_history(cfg, "", 100)
    }

    fn message(&self, cfg: &Config, id: &str) -> io::Result<Value> {
        self.owner_message(cfg, id)?.ok_or_else(invalid)
    }

    fn post(
        &self,
        cfg: &Config,
        nonce: &str,
        body: &str,
        files: &[crate::supervisor::mail_files::Captured],
    ) -> crate::discord::rest::Post {
        self.post_publication(cfg, nonce, body, files)
    }
}

pub(in crate::discord) fn stage_vm_wake(place: &Place, deliver: bool) -> io::Result<bool> {
    let cfg = personal_config(place, true)?;
    let api = Discord::connect(&cfg)?;
    with_vm_wake(place, &api, deliver, || {})
}

pub(super) fn with_vm_wake(
    place: &Place,
    room: &impl Room,
    deliver: bool,
    opened: impl FnOnce(),
) -> io::Result<bool> {
    let cfg = personal_config(place, true)?;
    let expected = fs::symlink_metadata(place.dir.join("discord.db"))?;
    let mut lease = ConnectorLease::acquire(place, &cfg, &expected, true)?;
    let binding = {
        let tx = lease.db.begin().map_err(io::Error::other)?;
        let key = format!(
            "{}\0{}",
            cfg.text("installation_id"),
            cfg.text("dm_channel_id")
        );
        value(&tx, "teammate_discussions", key.as_bytes())?.ok_or_else(invalid)?
    };
    if wake_intent::reconcile(place, &cfg, &binding, &lease)? {
        return Ok(true);
    }
    if !matches!(
        activation::ready(place, &cfg, &lease.db)?,
        activation::Ready::AdmittedFile { .. }
    ) {
        return Err(io::Error::other(
            "the original connector has no admitted VM file wake",
        ));
    }
    room.verify(&cfg)?;
    VmRelay {
        place,
        cfg: &cfg,
        binding: &binding,
        room,
        authority: VmAuthority::Explicit,
    }
    .run(&mut lease, deliver, opened, || Ok(false))
    .map(|_| true)
}

struct VmRelay<'a, R> {
    place: &'a Place,
    cfg: &'a Config,
    binding: &'a Value,
    room: &'a R,
    authority: VmAuthority,
}

impl<R: Room> VmRelay<'_, R> {
    fn run(
        &self,
        lease: &mut ConnectorLease,
        deliver: bool,
        opened: impl FnOnce(),
        mut stopped: impl FnMut() -> io::Result<bool>,
    ) -> io::Result<Option<Value>> {
        let bridge = Bridge::open(self.place, self.cfg, self.binding)?;
        opened();
        let until = Instant::now() + Duration::from_secs(30);
        while Instant::now() < until {
            if stopped()? {
                return Ok(None);
            }
            let mut result = Ok(true);
            let mut accepted = None;
            if bridge
                .accept_request(|request| {
                    let output = manager_publication::accept_wake(
                        self.place,
                        self.cfg,
                        lease,
                        self.binding,
                        &request,
                        self.authority,
                        |message| self.room.message(self.cfg, message),
                    )?;
                    if deliver {
                        result = publication::deliver_admitted_vm(
                            self.place,
                            self.cfg,
                            lease,
                            &output,
                            self.room,
                            self.authority,
                        );
                    }
                    accepted = Some(output.clone());
                    Ok(output)
                })?
                .is_some()
            {
                if !deliver {
                    println!(
                        "Staged the original Go VM file publication using the original connector nonce format."
                    );
                }
                return result.map(|_| accepted);
            }
            std::thread::sleep(Duration::from_millis(25));
        }
        Err(io::Error::other(
            "no authorized original Go VM file publication was staged",
        ))
    }
}

pub(in crate::discord) fn run(place: &Place) -> io::Result<bool> {
    let cfg = selected_personal(place)?;
    let api = Discord::connect(&cfg)?;
    with(place, &api, None, |cfg| {
        let (mut socket, mut cursor) = gateway::open(cfg)?;
        Ok(move || cursor.event(&mut socket))
    })
}

pub(super) fn with<ReadEvent>(
    place: &Place,
    room: &impl Room,
    limit: Option<usize>,
    mut open: impl FnMut(&Config) -> io::Result<ReadEvent>,
) -> io::Result<bool>
where
    ReadEvent: FnMut() -> io::Result<Option<Value>>,
{
    let cfg = selected_personal(place)?;
    let source = place.dir.join("discord.db");
    let expected = fs::symlink_metadata(&source)?;
    let mut lease = ConnectorLease::acquire(place, &cfg, &expected, true)?;
    if !activation::selected(place)? {
        return Err(invalid());
    }
    let (binding, boundary, vm_discussion) = {
        let tx = lease.db.begin().map_err(io::Error::other)?;
        let key = format!(
            "{}\0{}",
            cfg.text("installation_id"),
            cfg.text("dm_channel_id")
        );
        let saved = value(&tx, "teammate_discussions", key.as_bytes())?.ok_or_else(invalid)?;
        require_repaired(&tx, &cfg, &saved)?;
        let boundary = field(&saved, "Boundary");
        if !snowflake(boundary) {
            return Err(invalid());
        }
        let vm_discussion = if retained_vm(&cfg) {
            let (_, id, _) = voice_location(place, &cfg, &saved)?;
            Some(format!("discord-teammate-{id}"))
        } else {
            None
        };
        let boundary = boundary.parse::<u64>().map_err(io::Error::other)?;
        (saved, boundary, vm_discussion)
    };
    if retained_vm(&cfg) && wake_intent::reconcile(place, &cfg, &binding, &lease)? {
        return Ok(true);
    }
    let readiness = activation::ready_to_run(place, &cfg, &lease.db)?;
    let mut next_mail_check = Instant::now();
    let mut check_mail = || {
        if let Some(discussion) = &vm_discussion
            && Instant::now() >= next_mail_check
        {
            activation::require_no_pending_vm_mail(place, discussion)?;
            next_mail_check = Instant::now() + Duration::from_millis(250);
        }
        Ok::<(), io::Error>(())
    };
    let status = StatusSocket::start(place, &cfg)?;
    room.verify(&cfg)?;
    let mut event = open(&cfg)?;
    let mut pending = history_backlog(room, &cfg, boundary)?;
    status.online(true);
    if matches!(readiness, activation::Ready::QueuedFile) {
        queue::prepare(place, &cfg, &lease, room)?;
        return Err(io::Error::other(
            "the original queued file wake was prepared and remains for the Go scheduler",
        ));
    }
    let mut handled = 0;
    if let activation::Ready::AdmittedFile { parent_message } = readiness {
        let result = VmRelay {
            place,
            cfg: &cfg,
            binding: &binding,
            room,
            authority: VmAuthority::Activated,
        }
        .run(
            &mut lease,
            true,
            || {},
            || {
                if status.stopping() {
                    return Ok(true);
                }
                // A Gateway gap cannot silently transfer an active native effect.
                if let Some(frame) = event()? {
                    if matches!(field(&frame, "t"), "MESSAGE_UPDATE" | "MESSAGE_DELETE")
                        && frame["d"]["id"] == parent_message
                    {
                        return Err(io::Error::other(
                            "original worker's owner source changed during the native wake",
                        ));
                    }
                    queue_event(&cfg, &frame, boundary, &mut pending)?;
                }
                Ok(false)
            },
        )?;
        let Some(output) = result else {
            return Ok(true);
        };
        manager_publication::complete_vm_wake(place, &cfg, &lease, &binding, &output)?;
        if limit == Some(1) {
            return Ok(true);
        }
        if !matches!(
            activation::ready(place, &cfg, &lease.db)?,
            activation::Ready::Idle
        ) {
            return Err(io::Error::other(
                "the original VM scheduler has other active work",
            ));
        }
        handled = 1;
    }
    let mut through = boundary;
    let mut reconnects = 0;
    loop {
        let sequence = loop {
            if status.stopping() {
                return Ok(true);
            }
            check_mail()?;
            if let Some(id) = pending.pop_first() {
                break id;
            }
            match event() {
                Ok(Some(frame)) => {
                    reconnects = 0;
                    queue_event(&cfg, &frame, through, &mut pending)?;
                }
                Ok(None) => {}
                Err(_) if reconnects < 2 => {
                    reconnects += 1;
                    status.online(false);
                    room.verify(&cfg)?;
                    event = open(&cfg)?;
                    pending = history_backlog(room, &cfg, through)?;
                    status.online(true);
                }
                Err(error) => return Err(error),
            }
        };
        let message = sequence.to_string();
        let fetch = || room.message(&cfg, &message);
        capture_under_lease(&cfg, &message, &mut lease, |_, _| fetch())?;
        prepare::under_lease(place, &cfg, &message, &mut lease, |_, _| fetch())?;
        if status.stopping() {
            return Ok(true);
        }
        let row = pending_dispatch(&lease.db, &cfg, &message)?;
        let bridge = Bridge::open(place, &cfg, &row.binding)?;
        dispatch_under_lease(place, &cfg, &message, &mut lease, |_| fetch())?;
        settle_under_lease(place, &cfg, &message, &mut lease)?;
        let deadline = Instant::now() + Duration::from_secs(120);
        loop {
            if status.stopping() {
                return Ok(true);
            }
            check_mail()?;
            if Instant::now() >= deadline {
                return Err(io::Error::other(
                    "the original native manager has no verified final publication",
                ));
            }
            if let Some(id) = bridge.accept(place, &cfg, &mut lease, &row, fetch)? {
                publication::under_lease(place, &cfg, &id, &mut lease, |cfg| {
                    room.verify(cfg)?;
                    let scope = cfg.clone();
                    Ok(move |row: &publication::Publication| {
                        room.post(&scope, &row.nonce, &row.body, &row.files)
                    })
                })?;
                manager_publication::complete(place, &cfg, &lease, &row)?;
                break;
            }
            if let Some(frame) = event()? {
                if matches!(field(&frame, "t"), "MESSAGE_UPDATE" | "MESSAGE_DELETE")
                    && frame["d"]["id"] == message
                {
                    return Err(io::Error::other(
                        "owner source changed during the retained native turn",
                    ));
                }
                queue_event(&cfg, &frame, sequence, &mut pending)?;
            }
        }
        handled += 1;
        through = sequence;
        if limit == Some(handled) {
            return Ok(true);
        }
    }
}

fn history_backlog(
    room: &impl Room,
    cfg: &Config,
    boundary: u64,
) -> io::Result<std::collections::BTreeSet<u64>> {
    let history = room.history(cfg)?;
    if !history
        .iter()
        .filter_map(|row| enrolled_dm(cfg, row))
        .any(|id| id.parse::<u64>() == Ok(boundary))
    {
        return Err(io::Error::other(
            "the original personal boundary is absent from the visible Discord history page",
        ));
    }
    Ok(history
        .iter()
        .filter_map(|row| enrolled_dm(cfg, row))
        .filter_map(|id| id.parse::<u64>().ok())
        .filter(|id| *id > boundary)
        .collect())
}

fn queue_event(
    cfg: &Config,
    frame: &Value,
    through: u64,
    pending: &mut std::collections::BTreeSet<u64>,
) -> io::Result<()> {
    if frame["t"] == "MESSAGE_CREATE"
        && let Some(id) = enrolled_dm(cfg, &frame["d"])
        && let Ok(id) = id.parse::<u64>()
        && id > through
    {
        pending.insert(id);
    }
    if pending.len() > 100 {
        return Err(io::Error::other(
            "retained personal input backlog requires original Go history repair",
        ));
    }
    Ok(())
}
