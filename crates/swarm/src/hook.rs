use crate::providers::{agy, claude};
use crate::supervisor::runtime;
use serde_json::{Map, Value, json};
use std::env;
use std::io::{self, Read, Write};
use std::os::unix::net::UnixStream;
use std::os::unix::process::ExitStatusExt;
use std::process::{Command, Stdio};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

pub(crate) fn run(args: &[String]) -> u8 {
    if args.first().is_some_and(|event| event == agy::EVENT) {
        return agy_status_line(&args[1..]);
    }
    match post(args) {
        Ok(()) => 0,
        Err(error) => {
            eprintln!("hook: {error}");
            1
        }
    }
}

fn post(args: &[String]) -> io::Result<()> {
    let (event, arguments) = args
        .split_first()
        .filter(|(event, _)| claude::EVENTS.contains(&event.as_str()))
        .ok_or_else(|| io::Error::other("unsupported provider hook event"))?;
    let socket = env::var_os("SWARM_RUST_HOOK_SOCKET")
        .ok_or_else(|| io::Error::other("missing shim hook socket in environment"))?;
    let token = env::var("SWARM_HOOK_TOKEN")
        .map_err(|_| io::Error::other("missing session token in environment"))?;
    if !runtime::valid_id(&token) {
        return Err(io::Error::other("invalid session token"));
    }
    // Oversized or malformed native data must never stop an otherwise valid status event.
    let mut input = Vec::new();
    io::stdin().lock().take(96 * 1024).read_to_end(&mut input)?;
    let raw: Value = serde_json::from_slice(&input).unwrap_or(Value::Null);
    let mut fields: Map<_, _> = raw
        .as_object()
        .into_iter()
        .flatten()
        .filter(|(key, value)| {
            !matches!(key.as_str(), "turn" | "interaction")
                && value.as_str().is_some_and(|text| text.len() <= 4096)
        })
        .map(|(key, value)| (key.clone(), value.clone()))
        .collect();
    for argument in arguments {
        if let Some((key, value)) = argument.split_once('=')
            && !matches!(
                key,
                "turn" | "interaction" | "cwd" | "session_id" | "last_assistant_message"
            )
            && value.len() <= 4096
        {
            fields.insert(key.to_owned(), json!(value));
        }
    }
    let mut stream = UnixStream::connect(socket)?;
    stream.set_write_timeout(Some(runtime::IO_TIMEOUT))?;
    stream.set_read_timeout(Some(runtime::IO_TIMEOUT))?;
    runtime::write_control(
        &mut stream,
        &json!({
            "type": "hook", "event": event, "token": token, "payload": fields,
        }),
    )?;
    let result = runtime::read_control(&mut stream)?;
    if result["type"] != "ok" {
        return Err(io::Error::other("shim refused provider hook"));
    }
    Ok(())
}

fn agy_status_line(args: &[String]) -> u8 {
    const LIMIT: u64 = 96 * 1024;
    let mut stdin = io::stdin().lock();
    let mut input = Vec::new();
    let _ = (&mut stdin).take(LIMIT + 1).read_to_end(&mut input);
    if input.len() as u64 <= LIMIT
        && let Ok(raw) = serde_json::from_slice::<Value>(&input)
    {
        let _ = post_agy(&raw);
    }
    let [command] = args else { return 0 };
    if command.is_empty()
        || command.starts_with("swarm hook AgyStatusLine")
        || command.contains(" hook AgyStatusLine ")
    {
        return 0;
    }
    let mut saved = Command::new("/bin/sh");
    saved.args(["-c", command]).stdin(Stdio::piped());
    for (key, _) in env::vars_os().filter(|(key, _)| key.as_encoded_bytes().starts_with(b"SWARM_"))
    {
        saved.env_remove(key);
    }
    let mut child = match saved.spawn() {
        Ok(child) => child,
        Err(_) => return 1,
    };
    if let Some(mut pipe) = child.stdin.take() {
        let _ = pipe
            .write_all(&input)
            .and_then(|_| io::copy(&mut stdin, &mut pipe).map(|_| ()));
    }
    match child.wait() {
        Ok(status) => status
            .code()
            .or_else(|| status.signal().map(|n| 128 + n))
            .unwrap_or(1) as u8,
        Err(_) => 1,
    }
}

fn post_agy(raw: &Value) -> io::Result<()> {
    let (Ok(socket), Ok(token)) = (
        env::var("SWARM_RUST_HOOK_SOCKET"),
        env::var("SWARM_HOOK_TOKEN"),
    ) else {
        return Ok(());
    };
    if !runtime::valid_id(&token) {
        return Ok(());
    }
    let field = |key| raw[key].as_str().unwrap_or("").trim();
    let id = agy::status_conversation(raw)?;
    let state = field("agent_state");
    let title = field("conversation_title");
    if state.is_empty() && title.is_empty() && id.is_none() {
        return Ok(());
    }
    let at = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_err(io::Error::other)?;
    let mut stream = UnixStream::connect(socket)?;
    stream.set_write_timeout(Some(Duration::from_millis(100)))?;
    stream.set_read_timeout(Some(Duration::from_millis(100)))?;
    let mut payload = json!({
        "agent_state":state.chars().take(32).collect::<String>(),
        "conversation_title":title.chars().take(128).collect::<String>(),
        "observed_ns":u64::try_from(at.as_nanos()).map_err(io::Error::other)?,
    });
    if let Some(id) = id {
        payload["conversation_id"] = json!(id);
    }
    runtime::write_control(
        &mut stream,
        &json!({
            "type":"hook", "event":agy::EVENT, "token":token, "payload":payload,
        }),
    )?;
    if runtime::read_control(&mut stream)?["type"] != "ok" {
        return Err(io::Error::other("shim refused Antigravity status line"));
    }
    Ok(())
}
