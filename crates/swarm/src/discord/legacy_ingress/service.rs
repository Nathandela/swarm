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
}

struct Bridge {
    listener: UnixListener,
    socket: PathBuf,
    identity: (u64, u64),
    credential: String,
    _lock: fs::File,
}

impl Bridge {
    fn open(place: &Place, cfg: &Config, row: &Candidate) -> io::Result<Self> {
        let (directory, id, _) = voice_location(place, cfg, &row.binding)?;
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
        let mut connection = match self.listener.accept() {
            Ok((connection, _)) => connection,
            Err(error) if error.kind() == io::ErrorKind::WouldBlock => return Ok(None),
            Err(error) => return Err(error),
        };
        let result = read_tool_bytes(&connection, &self.credential).and_then(|raw| {
            let request: ManagerRequest = serde_json::from_slice(&raw)?;
            let request = serde_json::to_value(request)?;
            if original_revision(&source()?, &c.origin)? != c.origin["revision"] {
                return Err(invalid());
            }
            manager_publication::accept(place, cfg, lease, c, &request)
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
    fn post(&self, cfg: &Config, nonce: &str, body: &str) -> crate::discord::rest::Post;
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

    fn post(&self, cfg: &Config, nonce: &str, body: &str) -> crate::discord::rest::Post {
        self.post_publication(cfg, nonce, body, &[])
    }
}

pub(in crate::discord) fn run(place: &Place) -> io::Result<bool> {
    let cfg = retained_config(place)?;
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
    open: impl FnOnce(&Config) -> io::Result<ReadEvent>,
) -> io::Result<bool>
where
    ReadEvent: FnMut() -> io::Result<Option<Value>>,
{
    let cfg = retained_config(place)?;
    let source = place.dir.join("discord.db");
    let expected = fs::symlink_metadata(&source)?;
    let mut lease = ConnectorLease::open(place, &cfg, &expected)?;
    let boundary = {
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
        boundary.parse::<u64>().map_err(io::Error::other)?
    };
    room.verify(&cfg)?;
    let mut event = open(&cfg)?;
    let history = room.history(&cfg)?;
    if !history
        .iter()
        .any(|message| field(message, "id").parse::<u64>() == Ok(boundary))
    {
        return Err(io::Error::other(
            "the original personal boundary is absent from the visible Discord history page",
        ));
    }
    let mut pending: std::collections::BTreeSet<_> = history
        .iter()
        .filter_map(|row| enrolled_dm(&cfg, row))
        .filter_map(|id| id.parse::<u64>().ok())
        .filter(|id| *id > boundary)
        .collect();
    let mut handled = 0;
    let mut through = boundary;
    loop {
        let sequence = loop {
            if let Some(id) = pending.pop_first() {
                break id;
            }
            if let Some(frame) = event()? {
                queue_event(&cfg, &frame, through, &mut pending)?;
            }
        };
        let message = sequence.to_string();
        let fetch = || room.message(&cfg, &message);
        capture_under_lease(&cfg, &message, &mut lease, |_, _| fetch())?;
        prepare::under_lease(place, &cfg, &message, &mut lease, |_, _| fetch())?;
        let row = pending_dispatch(&lease.db, &cfg, &message)?;
        let bridge = Bridge::open(place, &cfg, &row)?;
        dispatch_under_lease(place, &cfg, &message, &mut lease, |_| fetch())?;
        settle_under_lease(place, &cfg, &message, &mut lease)?;
        let deadline = Instant::now() + Duration::from_secs(120);
        loop {
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
                        room.post(&scope, &row.nonce, &row.body)
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
