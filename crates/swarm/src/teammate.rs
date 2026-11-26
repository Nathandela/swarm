//! Worker CLI for the owner mailbox and historical per-worker bridge.
use crate::{flags, ipc, socket::DeadlineReader};
use serde_json::{Value, json};
use std::env;
use std::io::{self, BufRead, BufReader, Read, Write};
use std::os::unix::{
    fs::{FileTypeExt, MetadataExt},
    net::UnixStream,
};
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

pub(crate) const MAIL_CAP: &str = "dispatcher-worker-mail";
pub(crate) const FILE_CAP: &str = "dispatcher-worker-mail-files";
pub(crate) const SOCKET_ENV: &str = "SWARM_WORKER_MAILBOX_SOCKET";
pub(crate) const PROOF_ENV: &str = "SWARM_WORKER_MAILBOX_PROOF";
const BRIDGE_ENV: &str = "SWARM_TEAMMATE_WORKER_BRIDGE";
const SESSION_ENV: &str = "SWARM_SESSION_ID";
const REQUEST_TIMEOUT: Duration = Duration::from_secs(10);
const USAGE: &str = "swarm teammate message --text TEXT [--kind needs-input|complete] [--id MESSAGE_ID]\nswarm teammate message --kind complete --file PATH [--file PATH ...] [--text CAPTION] [--id MESSAGE_ID]\nswarm teammate receipt --id MESSAGE_ID\nswarm teammate reply --id MESSAGE_ID [--wait 90s]\nswarm dispatcher-guide    read the dispatcher workflow";
const MESSAGE_HELP: &str = concat!(
    "Usage of teammate message:\n",
    "  -file value\n    \tattach a file from this worker's checkout, such as PDF, image, CSV, Office, ZIP or text; programs, scripts and web pages are refused (repeatable, up to 10 files, 8 MiB total)\n",
    "  -id string\n    \tmessage ID from the original attempt when retrying\n",
    "  -kind string\n    \tneeds-input or complete\n",
    "  -text string\n    \tmessage to your own manager\n",
);

pub(crate) fn valid_text(text: &str) -> bool {
    !text.trim().is_empty() && text.len() <= 4096 && !text.contains('\0')
}

pub(crate) fn valid_kind(kind: &str) -> bool {
    matches!(kind, "" | "needs-input" | "complete")
}

fn new_message_id() -> io::Result<String> {
    let mut random = [0u8; 16];
    std::fs::File::open("/dev/urandom")?.read_exact(&mut random)?;
    let nonce: String = random.iter().map(|b| format!("{b:02x}")).collect();
    Ok(format!("semantic:{}:{nonce}", ipc::spawn::now_ms()))
}

fn error(text: &str) -> io::Error {
    io::Error::other(text.to_owned())
}

pub(crate) fn run(args: &[String]) -> u8 {
    let Some((action, args)) = args.split_first() else {
        println!("{USAGE}");
        return 0;
    };
    if matches!(action.as_str(), "-h" | "--help") {
        println!("{USAGE}");
        return 0;
    }
    let names = match action.as_str() {
        "message" => &["id", "text", "kind", "file"][..],
        "receipt" => &["id"],
        "reply" => &["id", "wait"],
        _ => {
            eprintln!("teammate: expected message --text TEXT or reply --id MESSAGE_ID");
            return 2;
        }
    };
    let mut id = "";
    let mut kind = "";
    let mut text = "";
    let mut files = Vec::new();
    let mut wait = Duration::ZERO;
    let result = flags::options(args, names, &[], |key, value| {
        match key {
            "id" => id = value,
            "kind" => kind = value,
            "text" => text = value,
            "file" => {
                if value.is_empty() || files.len() >= 10 {
                    return Err("provide 1 to 10 file paths".into());
                }
                let path = std::path::absolute(value)
                    .map_err(|_| "unable to resolve the file path".to_owned())?;
                if path.as_os_str().len() > 4096 {
                    return Err("unable to resolve the file path".into());
                }
                files.push(path);
            }
            "wait" => {
                let nanos = ipc::duration::parse(value)
                    .filter(|n| (0..=120_000_000_000).contains(n))
                    .ok_or_else(|| "expected reply --id MESSAGE_ID [--wait 90s]".to_owned())?;
                wait = Duration::from_nanos(nanos as u64);
            }
            _ => unreachable!(),
        }
        Ok(())
    });
    match result {
        Err(err) if err.is_empty() => {
            if action == "message" {
                eprint!("{MESSAGE_HELP}");
            } else {
                println!("{USAGE}");
            }
            return 0;
        }
        Err(err) => {
            eprintln!("teammate: {err}");
            return 2;
        }
        Ok(rest) if !rest.is_empty() => {
            eprintln!("teammate: unexpected positional argument");
            return 2;
        }
        _ => {}
    }
    if action == "message" {
        if !valid_kind(kind) || text.is_empty() && files.is_empty() {
            eprintln!(
                "teammate: provide --text TEXT or attach a file with --kind complete --file PATH"
            );
            return 2;
        }
        if !files.is_empty() && kind != "complete" {
            eprintln!("teammate: files require --kind complete");
            return 2;
        }
    }
    if action != "message" && id.is_empty() {
        eprintln!("teammate: expected {action} --id MESSAGE_ID");
        return 2;
    }
    if action == "message"
        && env::var_os(crate::schedule::worker::RUN).is_some_and(|s| !s.is_empty())
        && [SOCKET_ENV, BRIDGE_ENV]
            .iter()
            .all(|name| env::var_os(name).is_none_or(|s| s.is_empty()))
    {
        eprintln!(
            r#"teammate: this is a scheduled run with no manager to message; finish it with "$SWARM_SCHEDULE_BIN" schedule complete --summary TEXT [--link URL]"#
        );
        return 1;
    }
    let id = if id.is_empty() {
        new_message_id()
    } else {
        Ok(id.to_owned())
    };
    let id = match id {
        Ok(id) => id,
        Err(err) => {
            eprintln!("{err}");
            return 1;
        }
    };
    if action == "message"
        && !files.is_empty()
        && env::var_os(SOCKET_ENV).is_none_or(|s| s.is_empty())
    {
        eprintln!("teammate: files require a mailbox-enabled Swarm worker");
        return 1;
    }
    let result = if action == "reply" {
        wait_reply(&id, wait)
    } else {
        let mut req = json!({"action":action,"id":id});
        if !kind.is_empty() {
            req["kind"] = json!(kind);
        }
        if !text.is_empty() {
            req["text"] = json!(text);
        }
        if !files.is_empty() {
            req["paths"] = json!(files);
        }
        call(&req, Instant::now() + REQUEST_TIMEOUT)
    };
    match result {
        Ok(result) => {
            let mut out = io::stdout().lock();
            u8::from(
                serde_json::to_writer(&mut out, &result).is_err() || out.write_all(b"\n").is_err(),
            )
        }
        Err(err) => {
            if action == "message" {
                eprintln!("{err} (message ID {id})");
            } else {
                eprintln!("{err}");
            }
            1
        }
    }
}

fn wait_reply(id: &str, wait: Duration) -> io::Result<Value> {
    let deadline = Instant::now() + wait;
    let req = json!({"action":"reply","id":id});
    loop {
        let attempt = Instant::now() + REQUEST_TIMEOUT;
        let result = call(
            &req,
            if wait.is_zero() {
                attempt
            } else {
                attempt.min(deadline)
            },
        );
        match result {
            Ok(result) if result["state"] == "superseded" => {
                return Ok(json!({"id":id,"state":"superseded"}));
            }
            Ok(result)
                if result["state"] == "available"
                    && result["text"].as_str().is_some_and(valid_text) =>
            {
                return Ok(json!({"id":id,"state":"available","text":result["text"]}));
            }
            Ok(result) if result["state"] == "pending" => {}
            Ok(_) => return Err(error("teammate: manager reply receipt unavailable")),
            Err(err) if !wait.is_zero() && Instant::now() >= deadline => {
                drop(err);
                break;
            }
            Err(err) => return Err(err),
        }
        if wait.is_zero() || Instant::now() >= deadline {
            break;
        }
        std::thread::sleep(
            Duration::from_secs(1).min(deadline.saturating_duration_since(Instant::now())),
        );
    }
    Ok(json!({"id":id,"state":"pending"}))
}

fn call(request: &Value, deadline: Instant) -> io::Result<Value> {
    let session = env::var(SESSION_ENV).unwrap_or_default();
    if let Some(socket) = env::var_os(SOCKET_ENV).filter(|s| !s.is_empty()) {
        let proof = env::var(PROOF_ENV).unwrap_or_default();
        return mailbox(&PathBuf::from(socket), &session, &proof, request, deadline);
    }
    legacy_bridge(
        &env::var(BRIDGE_ENV).unwrap_or_default(),
        &session,
        request,
        deadline,
    )
}

fn historical_text(value: &str, maximum: usize) -> bool {
    !value.is_empty() && value.len() <= maximum && !value.chars().any(char::is_control)
}

fn mailbox(
    socket: &Path,
    session: &str,
    proof: &str,
    req: &Value,
    deadline: Instant,
) -> io::Result<Value> {
    if proof.len() != 64
        || !proof.bytes().all(|b| b.is_ascii_hexdigit())
        || !historical_text(session, 256)
        || session.contains('/')
        || !socket.is_absolute()
    {
        return Err(error("run this command inside a mailbox-enabled worker"));
    }
    let mut client = ipc::Client::dial_before(socket, &[MAIL_CAP, FILE_CAP], deadline)?;
    if !client.supports(MAIL_CAP) {
        return Err(error("worker mailbox requires a compatible supervisor"));
    }
    if !req["paths"].is_null() && !client.supports(FILE_CAP) {
        return Err(error("worker files require an updated supervisor"));
    }
    let session_id = format!("{}/{session}", client.endpoint());
    let mut req = req.clone();
    req["proof"] = json!(proof);
    let remaining = deadline.saturating_duration_since(Instant::now());
    if remaining.is_zero() {
        return Err(io::ErrorKind::TimedOut.into());
    }
    let response = client.exchange(
        json!({"op":"worker_mail","session_id":session_id,"worker_mail":req}),
        "worker_mail",
        remaining,
    )?;
    let receipt = &response["worker_mail_receipt"];
    if response["endpoint_id"] != client.endpoint()
        || response["session_id"] != session_id
        || receipt["id"] != req["id"]
        || !receipt["state"]
            .as_str()
            .is_some_and(|state| historical_text(state, 128))
    {
        return Err(error("worker mailbox: uncorrelated receipt"));
    }
    Ok(receipt.clone())
}

fn legacy_bridge(bridge: &str, session: &str, req: &Value, deadline: Instant) -> io::Result<Value> {
    let delegated = || error("teammate: run this command from a delegated Swarm worker");
    let (socket, token) = bridge.split_once(':').ok_or_else(delegated)?;
    let socket = Path::new(socket);
    if !socket.is_absolute()
        || token.len() != 64
        || !token.bytes().all(|b| b.is_ascii_hexdigit())
        || !historical_text(session, 256)
    {
        return Err(delegated());
    }
    let meta = std::fs::symlink_metadata(socket)?;
    // SAFETY: geteuid takes no arguments.
    if !meta.file_type().is_socket()
        || meta.mode() & 0o777 != 0o600
        || meta.nlink() != 1
        || meta.uid() != unsafe { libc::geteuid() }
    {
        return Err(error("teammate: unsafe worker bridge socket"));
    }
    let mut input = json!({"id":req["id"]});
    match req["action"].as_str() {
        Some("reply") => input["read_reply"] = json!(true),
        Some("receipt") => input["read_receipt"] = json!(true),
        Some("message") => {
            let text = req["text"].as_str().unwrap_or_default();
            if !valid_text(text) {
                return Err(error(
                    "teammate: use a current message ID and 1–4096 bytes of message text",
                ));
            }
            input["text"] = json!(text);
            if !req["kind"].is_null() {
                input["kind"] = req["kind"].clone();
            }
        }
        _ => return Err(error("teammate: invalid bridge action")),
    }
    let body = serde_json::to_vec(&input)?;
    let mut stream = UnixStream::connect(socket)?;
    let remaining = deadline.saturating_duration_since(Instant::now());
    if remaining.is_zero() {
        return Err(io::ErrorKind::TimedOut.into());
    }
    stream.set_write_timeout(Some(remaining))?;
    write!(
        stream,
        "POST /worker/message HTTP/1.0\r\nHost: voice\r\nAuthorization: Bearer {token}\r\nSwarm-Worker-Session: {session}\r\nContent-Type: application/json\r\nConnection: close\r\nContent-Length: {}\r\n\r\n",
        body.len()
    )?;
    stream.write_all(&body)?;
    let mut reader = BufReader::new(DeadlineReader(&stream, deadline).take(1 << 17));
    let mut status = String::new();
    reader.read_line(&mut status)?;
    if !matches!(status.split_whitespace().nth(1), Some("200")) {
        return Err(error("teammate: manager bridge refused the message"));
    }
    loop {
        let mut header = String::new();
        if reader.read_line(&mut header)? == 0 {
            return Err(error("teammate: incomplete bridge response"));
        }
        if header == "\r\n" {
            break;
        }
        if header
            .to_ascii_lowercase()
            .starts_with("transfer-encoding:")
        {
            return Err(error("teammate: unsupported bridge response framing"));
        }
    }
    let reply: Value = serde_json::from_reader(reader)?;
    if !reply["error"].is_null()
        || reply["id"] != req["id"]
        || !reply["state"]
            .as_str()
            .is_some_and(|state| historical_text(state, 128))
        || req["action"] == "message"
            && !matches!(reply["state"].as_str(), Some("queued" | "superseded"))
    {
        return Err(error("teammate: manager queue receipt unavailable"));
    }
    Ok(reply)
}
