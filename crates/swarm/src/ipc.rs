mod duration;
mod wire;

use serde_json::{Value, json};
use std::collections::VecDeque;
use std::env;
use std::io::{self, Read, Write};
use std::os::unix::net::UnixStream;
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

const VERSION: u64 = 1;
const REQUEST_TIMEOUT: Duration = Duration::from_secs(10);
const WATCH_TIMEOUT: Duration = Duration::from_secs(600);
const EVENT_LIMIT: usize = 256;
const OBSERVATION: &str = "opencode-observation";

pub fn run(args: &[String]) -> i32 {
    let verb = args[0].as_str();
    let caps = match verb {
        "ls" => &[OBSERVATION][..],
        "watch" => &["subscribe", OBSERVATION],
        "peek" => &[],
        _ => unreachable!(),
    };
    let path = match socket_path() {
        Ok(path) => path,
        Err(err) => {
            eprintln!("swarm: {err}");
            return 1;
        }
    };
    let mut client = match Client::dial(&path, caps) {
        Ok(client) => client,
        Err(err) => {
            let hint = if err.kind() == io::ErrorKind::PermissionDenied {
                "the daemon socket refused this process; a Codex sandbox without network access blocks it (enable sandbox_workspace_write.network_access), or check the socket's permissions"
            } else {
                "no daemon running? start swarm first"
            };
            eprintln!("swarm: {err} ({hint})");
            return 1;
        }
    };
    let result = match verb {
        "ls" => list(&mut client, &args[1..]),
        "peek" => peek(&mut client, &args[1..]),
        "watch" => watch(&mut client, &args[1..]),
        _ => unreachable!(),
    };
    match result {
        Ok(()) => 0,
        Err((status, message)) => {
            if !message.is_empty() {
                eprintln!("{verb}: {message}");
            }
            status
        }
    }
}

fn env_path(name: &str) -> Option<PathBuf> {
    env::var_os(name)
        .filter(|value| !value.is_empty())
        .map(PathBuf::from)
}

fn socket_path() -> io::Result<PathBuf> {
    let state = if let Some(path) = env_path("SWARM_DAEMON_STATE") {
        path
    } else if let Some(path) = env_path("XDG_STATE_HOME").filter(|path| path.is_absolute()) {
        path.join("swarm/sessions")
    } else {
        env_path("HOME")
            .ok_or_else(|| io::Error::other("$HOME is not defined"))?
            .join(".local/state/swarm/sessions")
    };
    Ok(env_path("SWARM_DAEMON_SOCK").unwrap_or_else(|| state.join("daemon.sock")))
}

struct Client {
    stream: UnixStream,
    endpoint: String,
    pushes: VecDeque<Value>,
}

impl Client {
    fn dial(path: &Path, caps: &[&str]) -> io::Result<Self> {
        let mut client = Self {
            stream: UnixStream::connect(path).map_err(|err| {
                let reason = match err.kind() {
                    io::ErrorKind::NotFound => "no such file or directory",
                    io::ErrorKind::ConnectionRefused => "connection refused",
                    io::ErrorKind::PermissionDenied => "permission denied",
                    _ => return err,
                };
                io::Error::new(
                    err.kind(),
                    format!("dial unix {}: connect: {reason}", path.display()),
                )
            })?,
            endpoint: String::new(),
            pushes: VecDeque::new(),
        };
        client.stream.set_write_timeout(Some(REQUEST_TIMEOUT))?;
        let mut hello = json!({
            "op": "hello", "endpoint_id": "", "protocol_version": VERSION, "build_version": "dev"
        });
        if !caps.is_empty() {
            hello["capabilities"] = json!(caps);
        }
        client.send(&hello)?;
        let reply = client.receive(Instant::now() + REQUEST_TIMEOUT)?;
        let op = field(&reply, "op");
        let daemon_version = reply["protocol_version"].as_i64().unwrap_or_default();
        let incompatibility = if op == "error" {
            Some(format!(
                "daemon rejected the protocol handshake (client speaks v{VERSION})"
            ))
        } else if op != "hello" || reply["protocol_version"].as_u64() != Some(VERSION) {
            Some(format!(
                "daemon speaks protocol v{daemon_version}, client v{VERSION}"
            ))
        } else {
            None
        };
        if let Some(reason) = incompatibility {
            return Err(io::Error::other(format!(
                "protocol: incompatible daemon version: {reason}; run `swarm daemon restart` (safe: your running sessions keep running and are reconnected — no live sessions are lost)"
            )));
        }
        client.endpoint = reply["endpoint_id"]
            .as_str()
            .filter(|id| !id.is_empty())
            .ok_or_else(|| io::Error::other("protocol: handshake reply has no endpoint id"))?
            .to_owned();
        Ok(client)
    }

    fn send(&mut self, message: &Value) -> io::Result<()> {
        wire::write_frame(
            &mut self.stream,
            wire::CONTROL,
            &serde_json::to_vec(message)?,
        )
    }

    fn receive(&mut self, deadline: Instant) -> io::Result<Value> {
        let (tag, payload) = wire::read_frame(&mut DeadlineReader(&mut self.stream, deadline))?;
        if tag != wire::CONTROL {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "protocol: expected a control frame",
            ));
        }
        Ok(serde_json::from_slice(&payload)?)
    }

    fn request(&mut self, op: &str, session: Option<&str>, expected: &str) -> io::Result<Value> {
        let mut message = json!({"op": op, "endpoint_id": self.endpoint});
        if let Some(id) = session.filter(|id| !id.is_empty()) {
            message["session_id"] = json!(id);
        }
        let deadline = Instant::now() + REQUEST_TIMEOUT;
        self.send(&message)?;
        loop {
            let reply = self.receive(deadline).map_err(|err| {
                if is_timeout(&err) {
                    io::Error::other("protocol: request timed out")
                } else if err.kind() == io::ErrorKind::UnexpectedEof {
                    io::Error::other("protocol: connection closed")
                } else {
                    err
                }
            })?;
            match field(&reply, "op") {
                "error" => return Err(io::Error::other(field(&reply, "error").to_owned())),
                "event" | "terminal_snapshot" if self.pushes.len() < EVENT_LIMIT => {
                    self.pushes.push_back(reply);
                }
                op if op == expected => return Ok(reply),
                _ => return Err(io::Error::other("protocol: unexpected reply")),
            }
        }
    }

    fn push(&mut self, deadline: Instant) -> io::Result<Value> {
        match self.pushes.pop_front() {
            Some(message) => Ok(message),
            None => self.receive(deadline),
        }
    }

    fn sessions(&mut self) -> io::Result<Vec<Value>> {
        match self.request("list", None, "list")?.get_mut("sessions") {
            None | Some(Value::Null) => Ok(Vec::new()),
            Some(Value::Array(rows)) => Ok(std::mem::take(rows)),
            _ => Err(io::Error::other("protocol: invalid session roster")),
        }
    }
}

struct DeadlineReader<'a>(&'a mut UnixStream, Instant);

impl Read for DeadlineReader<'_> {
    fn read(&mut self, bytes: &mut [u8]) -> io::Result<usize> {
        let remaining = self.1.saturating_duration_since(Instant::now());
        if remaining.is_zero() {
            return Err(io::ErrorKind::TimedOut.into());
        }
        self.0.set_read_timeout(Some(remaining))?;
        self.0.read(bytes)
    }
}

type CliResult = Result<(), (i32, String)>;

fn report(err: io::Error) -> (i32, String) {
    (1, err.to_string())
}

fn field<'a>(row: &'a Value, key: &str) -> &'a str {
    row[key].as_str().unwrap_or_default()
}

fn json_out(value: &Value) -> io::Result<()> {
    // Go encoding/json escapes these characters even for terminal output.
    let output = serde_json::to_string_pretty(value)?
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('&', "\\u0026")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029");
    writeln!(io::stdout().lock(), "{output}")
}

fn list(client: &mut Client, args: &[String]) -> CliResult {
    let mut as_json = false;
    options(args, &["json"], Some("json"), |name, value| {
        as_json = match value {
            "1" | "t" | "T" | "true" | "TRUE" | "True" => true,
            "0" | "f" | "F" | "false" | "FALSE" | "False" => false,
            _ => {
                return Err(format!(
                    "invalid boolean value {value:?} for -{name}: parse error"
                ));
            }
        };
        Ok(())
    })
    .map_err(|error| flag_error("ls", 2, error))?;
    let mut rows = client.sessions().map_err(report)?;
    rows.retain(|row| row["roster_hidden"] != true && field(row, "superseded_by").is_empty());
    if as_json {
        return json_out(&Value::Array(rows)).map_err(report);
    }

    let mut table = vec![["ID", "AGENT", "GROUP", "NAME", "NOTE"].map(str::to_owned)];
    table.extend(rows.iter().map(|row| {
        let note = field(row, "backend_plan_error");
        [
            field(row, "id").to_owned(),
            field(row, "agent").to_owned(),
            field(row, "group").to_owned(),
            field(row, "name").to_owned(),
            if note.is_empty() {
                String::new()
            } else {
                format!("no backend: {}", note.replace(['\t', '\n'], " "))
            },
        ]
    }));
    let mut widths = [4; 4];
    for row in &table {
        for (width, cell) in widths.iter_mut().zip(row) {
            *width = (*width).max(cell.chars().count() + 2);
        }
    }
    let mut output = io::stdout().lock();
    for row in table {
        for (width, cell) in widths.iter().zip(&row) {
            write!(output, "{cell:<width$}").map_err(report)?;
        }
        writeln!(output, "{}", row[4]).map_err(report)?;
    }
    Ok(())
}

fn peek(client: &mut Client, args: &[String]) -> CliResult {
    let Some((id, args)) = args.split_first().filter(|(id, _)| !id.starts_with('-')) else {
        return Err((
            2,
            "need a session id first: swarm peek <session> [--lines N]".into(),
        ));
    };
    let mut count = None;
    let rest = options(args, &["lines"], None, |name, value| {
        count = Some(
            duration::go_int(value)
                .map_err(|err| format!("invalid value {value:?} for flag -{name}: {err}"))?,
        );
        Ok(())
    })
    .map_err(|error| flag_error("peek", 2, error))?;
    if let Some(arg) = rest.first() {
        return Err((2, format!("unexpected argument {arg:?}")));
    }
    if let Some(count) = count.filter(|count| *count <= 0) {
        return Err((2, format!("--lines must be a positive count, got {count}")));
    }

    let local = id.split_once('/').map_or(id.as_str(), |(_, local)| local);
    client
        .request("terminal_subscribe", Some(id), "ok")
        .map_err(report)?;
    let deadline = Instant::now() + REQUEST_TIMEOUT;
    loop {
        let push = client.push(deadline).map_err(|err| {
            if is_timeout(&err) {
                (1, "protocol: no terminal snapshot from the daemon".into())
            } else if err.kind() == io::ErrorKind::UnexpectedEof {
                (1, "protocol: connection closed during peek".into())
            } else {
                report(err)
            }
        })?;
        let snapshot = &push["terminal"];
        if field(&push, "op") != "terminal_snapshot" || field(snapshot, "session") != local {
            continue;
        }
        let lines = match snapshot["lines"].as_array() {
            Some(lines) => lines.as_slice(),
            None if snapshot["lines"].is_null() => &[],
            None => return Err((1, "protocol: invalid terminal snapshot".into())),
        };
        let start = count.map_or(0, |count| lines.len().saturating_sub(count as usize));
        let mut output = io::stdout().lock();
        for line in &lines[start..] {
            let line = line
                .as_str()
                .ok_or_else(|| (1, "protocol: invalid terminal line".into()))?;
            writeln!(output, "{line}").map_err(report)?;
        }
        return Ok(());
    }
}

fn watch(client: &mut Client, args: &[String]) -> CliResult {
    let (leading, args) = match args.split_first() {
        Some((id, args)) if !id.is_empty() && !id.starts_with('-') => (Some(id), args),
        _ => (None, args),
    };
    let mut until = "needs_input";
    let mut timeout_ns = WATCH_TIMEOUT.as_nanos() as i64;
    let rest = options(args, &["until", "timeout"], None, |name, value| {
        if name == "until" {
            until = value;
        } else {
            timeout_ns = duration::parse(value)
                .ok_or_else(|| format!("invalid value {value:?} for flag -{name}: parse error"))?;
        }
        Ok(())
    })
    .map_err(|error| flag_error("watch", 1, error))?;
    let id = match (leading, rest) {
        (Some(id), []) | (None, [id]) => id,
        _ => {
            return Err((
                1,
                "need exactly one session id: swarm watch <session>".into(),
            ));
        }
    };
    let groups: Vec<_> = until.split(',').map(str::trim).collect();
    let change = groups == ["change"];
    if !change {
        for group in &groups {
            match *group {
                "needs_input" | "ready_for_review" | "completed" => {}
                "" => {
                    return Err((
                        1,
                        format!(
                            "invalid --until value {until:?}: empty state in comma-separated list"
                        ),
                    ));
                }
                "change" => {
                    return Err((
                        1,
                        format!(
                            "invalid --until value {until:?}: change cannot be combined with status states"
                        ),
                    ));
                }
                _ => {
                    return Err((
                        1,
                        format!(
                            "unknown --until state {group:?} in {until:?}: want needs_input, ready_for_review, completed, or change"
                        ),
                    ));
                }
            }
        }
    }
    let matches = |row: &Value| groups.contains(&field(row, "group"));
    client.request("subscribe", None, "ok").map_err(report)?;
    let rows = client.sessions().map_err(report)?;
    let row = rows
        .iter()
        .find(|row| field(row, "id") == id)
        .ok_or_else(|| (1, format!("no such session {id:?}")))?;
    if matches(row) {
        return json_out(row).map_err(report);
    }
    let deadline = Instant::now() + Duration::from_nanos(timeout_ns.max(0) as u64);
    loop {
        match client.push(deadline) {
            Ok(event) => {
                let row = &event["session"];
                if field(&event, "op") == "event"
                    && field(row, "id") == id
                    && (change || matches(row))
                {
                    return json_out(row).map_err(report);
                }
            }
            Err(err) if is_timeout(&err) => {
                return Err((
                    2,
                    format!(
                        "timeout after {} waiting for {id} to reach {until:?}",
                        duration::format(timeout_ns)
                    ),
                ));
            }
            Err(err) if err.kind() == io::ErrorKind::UnexpectedEof => {
                return Err((1, "the daemon closed the event stream".into()));
            }
            Err(err) => return Err(report(err)),
        }
    }
}

fn is_timeout(err: &io::Error) -> bool {
    matches!(
        err.kind(),
        io::ErrorKind::TimedOut | io::ErrorKind::WouldBlock
    )
}

fn flag_error(verb: &str, status: i32, error: String) -> (i32, String) {
    if !error.is_empty() {
        eprintln!("{error}");
    }
    eprintln!("Usage of {verb}:");
    eprint!(
        "{}",
        match verb {
            "ls" => "  -json\n    \tprint the full session roster as JSON\n",
            "peek" =>
                "  -lines int\n    \tprint only the last N lines (default: the whole screen)\n",
            "watch" => concat!(
                "  -timeout duration\n    \tgive up after this long (default 10m0s)\n",
                "  -until string\n    \twait for one or more comma-separated states: needs_input, ready_for_review, completed; or change (default \"needs_input\")\n",
            ),
            _ => unreachable!(),
        }
    );
    (status, String::new())
}

fn options<'a>(
    mut args: &'a [String],
    names: &[&str],
    boolean: Option<&str>,
    mut set: impl FnMut(&str, &'a str) -> Result<(), String>,
) -> Result<&'a [String], String> {
    while let Some((arg, rest)) = args.split_first() {
        if arg == "--" {
            return Ok(rest);
        }
        let Some(flag) = arg.strip_prefix('-').filter(|flag| !flag.is_empty()) else {
            break;
        };
        let flag = flag.strip_prefix('-').unwrap_or(flag);
        if flag.starts_with(['-', '=']) {
            return Err(format!("bad flag syntax: {arg}"));
        }
        let (name, value) = match flag.split_once('=') {
            Some((name, value)) => (name, Some(value)),
            None => (flag, None),
        };
        if !names.contains(&name) {
            return Err(if matches!(name, "h" | "help") {
                String::new()
            } else {
                format!("flag provided but not defined: -{name}")
            });
        }
        args = rest;
        let value = match value {
            Some(value) => value,
            None if boolean == Some(name) => "true",
            None => {
                let (value, rest) = args
                    .split_first()
                    .ok_or_else(|| format!("flag needs an argument: -{name}"))?;
                args = rest;
                value
            }
        };
        set(name, value)?;
    }
    Ok(args)
}
