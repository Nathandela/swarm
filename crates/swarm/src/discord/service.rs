//! Foreground personal connector. The private bridge never acknowledges native work.
use super::config::{Config, Place, invalid, snowflake};
use super::{
    gateway,
    receipt::{Inbox, Observation},
    rest::Discord,
};
use crate::socket::DeadlineReader;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::fs;
use std::io::{self, BufRead, BufReader, Read, Write};
use std::os::unix::fs::MetadataExt;
use std::os::unix::net::{UnixListener, UnixStream};
use std::path::PathBuf;
use std::time::{Duration, Instant};

const STOP_WAIT: Duration = Duration::from_secs(2);

pub(super) fn run(place: &Place, cfg: &Config) -> io::Result<()> {
    if cfg.text("mode") != "personal" {
        return Err(io::Error::other(
            "the Rust foreground connector currently supports personal owner DMs",
        ));
    }
    let (_lease, listener) = place.claim(cfg)?;
    let bridge = Bridge::new(place.dir.join("bridge.sock"), listener)?;
    let mut inbox = Inbox::open(&place.dir, cfg)?;
    let api = Discord::connect(cfg)?;
    api.verify_app(cfg)?;
    api.verify_dm(cfg, None)?;
    if inbox.cursor().is_none() {
        let baseline = api.owner_history(cfg, "", 1)?;
        let baseline = baseline
            .first()
            .and_then(|message| message["id"].as_str())
            .unwrap_or("");
        inbox.checkpoint(baseline)?;
    }
    let dispatcher = super::dispatcher::Dispatcher::open(cfg, &place.dir)?;
    let (mut gateway_socket, mut cursor) = gateway::open(cfg)?;
    repair(&mut inbox, |before| api.owner_history(cfg, before, 100))?;
    if dispatcher.is_none() {
        eprintln!(
            "discord: personal Gateway connected. Verified requests are queued locally pending native dispatcher support."
        );
    }
    loop {
        if bridge.serve(cfg, &mut inbox, cursor.sequence(), dispatcher.is_some())? {
            let _ = gateway_socket.close(None);
            return Ok(());
        }
        super::worker::tick(cfg, &mut inbox, &api)?;
        if let Some(dispatcher) = &dispatcher {
            dispatcher.tick(cfg, &mut inbox, &api)?;
        }
        let Some(event) = cursor.event(&mut gateway_socket)? else {
            continue;
        };
        ingest(cfg, &mut inbox, &event, |id| api.owner_message(cfg, id))?;
    }
}

fn repair(
    inbox: &mut Inbox,
    mut page: impl FnMut(&str) -> io::Result<Vec<Value>>,
) -> io::Result<()> {
    let cursor = inbox
        .cursor()
        .ok_or_else(|| invalid("personal history consent boundary is unavailable"))?
        .parse::<u64>()
        .unwrap_or_default();
    let pending: std::collections::BTreeSet<_> = inbox.pending_ids().into_iter().collect();
    let lower = pending.first().copied().unwrap_or(cursor).min(cursor);
    let mut before = String::new();
    let mut snapshots = std::collections::BTreeMap::new();
    for _ in 0..4 {
        let rows = page(&before)?;
        let mut lowest = u64::MAX;
        for row in &rows {
            let id = row["id"]
                .as_str()
                .filter(|id| snowflake(id))
                .and_then(|id| id.parse::<u64>().ok())
                .ok_or_else(|| invalid("personal history message has no valid identity"))?;
            if !before.is_empty() && id >= before.parse::<u64>().unwrap_or_default() {
                return Err(invalid("personal history pagination did not progress"));
            }
            lowest = lowest.min(id);
            if snapshots.insert(id, row.clone()).is_some() {
                return Err(invalid(
                    "personal history contains duplicate message identities",
                ));
            }
        }
        if rows.len() < 100 || lowest <= lower {
            let newest = snapshots.last_key_value().map(|(id, _)| id.to_string());
            for (&id, row) in &snapshots {
                inbox.observe(row, Observation::History)?;
                if inbox.admit(row)?.is_none() && pending.contains(&id) {
                    inbox.withdraw(&id.to_string())?;
                }
            }
            for id in pending.iter().filter(|id| !snapshots.contains_key(id)) {
                inbox.withdraw(&id.to_string())?;
            }
            if let Some(newest) = newest {
                inbox.checkpoint(&newest)?;
            }
            return Ok(());
        }
        before = lowest.to_string();
    }
    // shortcut: bounded startup history, lift after paginated repair and native FIFO reconcile.
    Err(io::Error::other(
        "personal history repair exceeded its startup page limit",
    ))
}

fn ingest(
    cfg: &Config,
    inbox: &mut Inbox,
    event: &Value,
    current: impl FnOnce(&str) -> io::Result<Option<Value>>,
) -> io::Result<()> {
    let message = &event["d"];
    let channel = cfg.text("dm_channel_id");
    if message["channel_id"] != channel || !message["guild_id"].is_null() {
        return Ok(());
    }
    if event["t"] == "MESSAGE_DELETE_BULK" {
        if let Some(ids) = message["ids"].as_array() {
            for id in ids
                .iter()
                .filter_map(Value::as_str)
                .filter(|id| snowflake(id))
            {
                inbox.withdraw(id)?;
            }
        }
        return Ok(());
    }
    let Some(id) = message["id"].as_str().filter(|id| snowflake(id)) else {
        return Ok(());
    };
    if !inbox.within_consent(id) {
        return Ok(());
    }
    match event["t"].as_str() {
        Some("MESSAGE_CREATE") if message["author"]["id"] == cfg.text("application_id") => {
            inbox.observe(message, Observation::Gateway)?;
            inbox.checkpoint(id)?;
        }
        Some("MESSAGE_CREATE") if message["author"]["id"] == cfg.text("owner_id") => {
            if let Some(source) = current(id)? {
                if source["id"] != id {
                    return Err(invalid(
                        "Discord message recheck returned a different source ID",
                    ));
                }
                inbox.admit(&source)?;
            }
            inbox.checkpoint(id)?;
        }
        Some("MESSAGE_UPDATE") => {
            if let Some(source) = current(id)? {
                if source["id"] != id {
                    return Err(invalid(
                        "Discord message recheck returned a different source ID",
                    ));
                }
                if inbox.admit(&source)?.is_none() {
                    inbox.withdraw(id)?;
                }
            } else {
                inbox.withdraw(id)?;
            }
        }
        Some("MESSAGE_DELETE") => inbox.withdraw(id)?,
        _ => {}
    }
    Ok(())
}

struct Bridge {
    path: PathBuf,
    listener: UnixListener,
    identity: (u64, u64),
}

impl Bridge {
    fn new(path: PathBuf, listener: UnixListener) -> io::Result<Self> {
        let meta = fs::symlink_metadata(&path)?;
        let this = Self {
            path,
            listener,
            identity: (meta.dev(), meta.ino()),
        };
        this.listener.set_nonblocking(true)?;
        Ok(this)
    }

    fn serve(
        &self,
        cfg: &Config,
        inbox: &mut Inbox,
        sequence: u64,
        native: bool,
    ) -> io::Result<bool> {
        let (mut client, _) = match self.listener.accept() {
            Ok(pair) => pair,
            Err(err) if err.kind() == io::ErrorKind::WouldBlock => return Ok(false),
            Err(err) => return Err(err),
        };
        match request(&mut client, cfg, inbox, sequence, native) {
            Ok(stop) => Ok(stop),
            Err(_) => Ok(false), // A bad local request must not stop Gateway.
        }
    }
}

impl Drop for Bridge {
    fn drop(&mut self) {
        if fs::symlink_metadata(&self.path)
            .is_ok_and(|meta| (meta.dev(), meta.ino()) == self.identity)
        {
            let _ = fs::remove_file(&self.path);
        }
    }
}

fn request(
    stream: &mut UnixStream,
    cfg: &Config,
    inbox: &mut Inbox,
    sequence: u64,
    native: bool,
) -> io::Result<bool> {
    stream.set_write_timeout(Some(STOP_WAIT))?;
    let stop = {
        let mut reader =
            BufReader::new(DeadlineReader(stream, Instant::now() + STOP_WAIT).take(8193));
        let mut first = String::new();
        reader.read_line(&mut first)?;
        let mut length = 0usize;
        loop {
            let mut header = String::new();
            if reader.read_line(&mut header)? == 0 {
                return Err(invalid("incomplete local headers"));
            }
            if header == "\r\n" {
                break;
            }
            if let Some((name, value)) = header.split_once(':') {
                if name.eq_ignore_ascii_case("Content-Length") {
                    length = value
                        .trim()
                        .parse()
                        .map_err(|_| invalid("invalid local content length"))?;
                }
                if name.eq_ignore_ascii_case("Transfer-Encoding") {
                    return Err(invalid("invalid local transfer encoding"));
                }
            }
        }
        if length > 128 {
            return Err(invalid("local request body too large"));
        }
        let mut body = vec![0; length];
        reader.read_exact(&mut body)?;
        let mut parts = first.split_whitespace();
        let Some(method) = parts.next() else {
            return Err(invalid("missing local method"));
        };
        let Some(path) = parts.next() else {
            return Err(invalid("missing local path"));
        };
        if !matches!(parts.next(), Some("HTTP/1.0" | "HTTP/1.1")) || parts.next().is_some() {
            return Err(invalid("invalid local HTTP request"));
        }
        let identity = Sha256::digest(cfg.text("installation_id").as_bytes());
        let stop_path = format!("/installation-v1/{identity:x}/shutdown");
        let stop = method == "POST" && path == stop_path && body == b"{}";
        if method == "GET" && path == "/status" && length == 0 {
            let mut status = cfg.status();
            status["connected"] = json!(true);
            status["teammate"] = json!({
                "pending": inbox.pending_count(),
                "gateway_sequence": sequence,
                "gap_epoch": 0,
                "ingress_issue": if native { "" } else { "native dispatcher reconciliation is unavailable" }
            });
            status["connector"] = json!({
                "state": if native { "scoped_dispatcher" } else { "receipt_only" },
                "pending_admissions": inbox.pending_count(),
                "worker_deliveries":inbox.worker_status()
            });
            respond(stream, "200 OK", status)?;
        } else if method == "POST"
            && [
                format!("/installation-v1/{identity:x}/exclude"),
                format!("/installation-v1/{identity:x}/include"),
            ]
            .iter()
            .any(|route| route == path)
        {
            let target: Value = serde_json::from_slice(&body)?;
            let target = target["target_id"]
                .as_str()
                .ok_or_else(|| invalid("missing worker session ID"))?;
            inbox.set_worker_excluded(target, path.ends_with("/exclude"))?;
            let mut status = cfg.status();
            status["connected"] = json!(true);
            status["connector"] = json!({"worker_deliveries":inbox.worker_status()});
            respond(stream, "200 OK", status)?;
        } else if stop {
            respond(stream, "200 OK", json!({"stopped":true}))?;
        } else if method == "POST"
            && path.starts_with("/installation-v1/")
            && path.ends_with("/shutdown")
        {
            respond(
                stream,
                "403 Forbidden",
                json!({"error":"discord: installation does not match or shutdown body is invalid"}),
            )?;
        } else {
            respond(
                stream,
                "405 Method Not Allowed",
                json!({"error":"discord: operation is unavailable in this connector"}),
            )?;
        }
        stop
    };
    Ok(stop)
}

fn respond(stream: &mut UnixStream, status: &str, body: Value) -> io::Result<()> {
    let bytes = serde_json::to_vec(&body)?;
    write!(
        stream,
        "HTTP/1.0 {status}\r\nContent-Type: application/json\r\nContent-Length: {}\r\n\r\n",
        bytes.len()
    )?;
    stream.write_all(&bytes)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::supervisor::runtime;
    use std::path::Path;

    fn fixture_dir() -> PathBuf {
        let root = Path::new("/tmp").join(format!("swarm-dm-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&root).unwrap();
        root
    }

    fn config() -> Config {
        Config(json!({
            "version":1, "name":"fixture", "enabled":true, "mode":"personal",
            "installation_id":"issued", "application_id":"200", "owner_id":"300",
            "dm_channel_id":"400", "daemon_socket":"/fixture/supervisor",
        }))
    }

    fn owner_source(id: &str, content: &str) -> Value {
        json!({"id":id, "channel_id":"400", "content":content, "type":0,
               "attachments":[], "author":{"id":"300"}})
    }

    #[test]
    fn dm_admission_repair_dedup_deletion_and_late_exact_receipt_survive_restart() {
        let dir = fixture_dir();
        let cfg = config();
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        inbox.checkpoint("1000").unwrap();
        let live = owner_source("1002", "current");
        let hint = |message: &Value| json!({"t":"MESSAGE_CREATE", "d":message});
        let mut foreign = live.clone();
        foreign["channel_id"] = json!("401");
        ingest(&cfg, &mut inbox, &hint(&foreign), |_| {
            panic!("outside-DM event reached REST")
        })
        .unwrap();
        ingest(&cfg, &mut inbox, &hint(&live), |_| Ok(Some(live.clone()))).unwrap();
        ingest(&cfg, &mut inbox, &hint(&live), |_| Ok(Some(live.clone()))).unwrap();
        assert_eq!(inbox.pending_count(), 1);
        assert_eq!(inbox.cursor(), Some("1002"));
        let pre_enrollment = owner_source("999", "historical edit");
        ingest(
            &cfg,
            &mut inbox,
            &json!({"t":"MESSAGE_UPDATE","d":pre_enrollment}),
            |_| panic!("historical edit reached REST"),
        )
        .unwrap();
        let mut conflict = live.clone();
        conflict["content"] = json!("unverified rewrite");
        assert!(ingest(&cfg, &mut inbox, &hint(&live), |_| Ok(Some(conflict))).is_err());
        let removed = owner_source("1004", "deleted while disconnected");
        ingest(&cfg, &mut inbox, &hint(&removed), |_| {
            Ok(Some(removed.clone()))
        })
        .unwrap();
        assert_eq!(inbox.cursor(), Some("1004"));
        drop(inbox);

        let file = dir.join("rust-text-v1.json");
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        let (nonce, proof) = super::super::receipt::fixture_publication("confirmed text");
        inbox
            .prepare_publication(&nonce, proof, Value::Null)
            .unwrap();
        inbox.publication_state(&nonce, "sending", None).unwrap();
        drop(inbox);
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        let old = owner_source("999", "prior to selected boundary");
        let gap = owner_source("1001", "missing while disconnected");
        let mut edited = live.clone();
        edited["content"] = json!("edited while disconnected");
        edited["edited_timestamp"] = json!("2026-11-25T09:10:00Z");
        let confirmation = json!({"id":"1005", "author":{"id":"200","bot":true},
            "nonce":nonce, "channel_id":"400", "content":"confirmed text"});
        let mut wrong = confirmation.clone();
        wrong["channel_id"] = json!("401");
        inbox.observe(&wrong, Observation::Gateway).unwrap();
        assert_eq!(
            runtime::read_json(&file).unwrap()["outbox"][&nonce]["state"],
            "sending"
        );
        repair(&mut inbox, |_| {
            Ok(vec![
                confirmation.clone(),
                edited.clone(),
                gap.clone(),
                old.clone(),
            ])
        })
        .unwrap();
        assert_eq!(inbox.cursor(), Some("1005"));
        assert_eq!(inbox.pending_count(), 2);
        let state = runtime::read_json(&file).unwrap();
        assert_eq!(
            state["messages"]["1001"]["text"],
            "missing while disconnected"
        );
        assert_eq!(state["outbox"][&nonce]["message_id"], "1005");
        assert_eq!(state["outbox"][&nonce]["history_seen"], true);
        assert_eq!(
            state["messages"]["1002"]["text"],
            "edited while disconnected"
        );
        assert_eq!(state["messages"]["1004"]["state"], "withdrawn");
        assert!(state["messages"]["999"].is_null());
        ingest(
            &cfg,
            &mut inbox,
            &json!({"t":"MESSAGE_DELETE","d":{"id":"1002","channel_id":"400"}}),
            |_| panic!(),
        )
        .unwrap();
        assert_eq!(inbox.pending_count(), 1);
        drop(inbox);
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        assert_eq!(inbox.pending_count(), 1);
        ingest(&cfg, &mut inbox, &hint(&live), |_| Ok(Some(live.clone()))).unwrap();
        assert_eq!(inbox.pending_count(), 1);
        let mut wrong = cfg.clone();
        wrong.0["owner_id"] = json!("301");
        assert!(Inbox::open(&dir, &wrong).is_err());
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn incomplete_four_page_repair_has_no_effect_and_version_one_cannot_guess_consent() {
        let dir = fixture_dir();
        let cfg = config();
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        inbox.checkpoint("1000").unwrap();
        inbox
            .admit(&owner_source("1001", "keep until absence is proven"))
            .unwrap();
        inbox.checkpoint("2000").unwrap();
        let file = dir.join("rust-text-v1.json");
        let prior = fs::read(&file).unwrap();
        let mut reads = 0;
        assert!(
            repair(&mut inbox, |before| {
                let upper = 2500 - reads * 100;
                assert_eq!(
                    before,
                    if reads == 0 {
                        String::new()
                    } else {
                        (upper + 1).to_string()
                    }
                );
                reads += 1;
                Ok((upper - 99..=upper)
                    .rev()
                    .map(|n| owner_source(&n.to_string(), "verified"))
                    .collect())
            })
            .is_err()
        );
        assert_eq!(reads, 4);
        assert_eq!(fs::read(&file).unwrap(), prior);
        assert_eq!(inbox.pending_count(), 1);
        drop(inbox);

        let mut legacy: Value = serde_json::from_slice(&prior).unwrap();
        legacy["version"] = json!(1);
        legacy.as_object_mut().unwrap().remove("consent_floor");
        runtime::write_json(&file, &legacy).unwrap();
        assert!(Inbox::open(&dir, &cfg).is_err());
        assert_eq!(runtime::read_json(&file).unwrap(), legacy);
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn private_runtime_reports_queue_and_accepts_only_bound_shutdown() {
        let dir = fixture_dir();
        let cfg = config();
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        let socket = dir.join("bridge.sock");
        let listener = runtime::bind(&socket).unwrap();
        let bridge = Bridge::new(socket.clone(), listener).unwrap();
        let server = std::thread::spawn(move || {
            let deadline = Instant::now() + Duration::from_secs(3);
            while Instant::now() < deadline {
                if bridge.serve(&cfg, &mut inbox, 12, false).unwrap() {
                    return;
                }
                std::thread::sleep(Duration::from_millis(5));
            }
            panic!("bound shutdown did not stop bridge");
        });
        let status = super::super::local::call(&dir, "status", "", None).unwrap();
        assert_eq!(status["connector"]["state"], "receipt_only");
        assert_eq!(status["teammate"]["pending"], 0);
        assert_eq!(status["teammate"]["gateway_sequence"], 12);
        let body = json!({});
        assert!(super::super::local::call(&dir, "shutdown", "", Some(("other", &body))).is_err());
        assert_eq!(
            super::super::local::call(&dir, "shutdown", "", Some(("issued", &body))).unwrap()["stopped"],
            true
        );
        server.join().unwrap();
        assert!(!socket.exists());
        fs::remove_dir_all(dir).unwrap();
    }
}
