use crate::providers::claude;
use crate::supervisor::runtime;
use serde_json::{Map, Value, json};
use std::env;
use std::io::{self, Read};
use std::os::unix::net::UnixStream;

pub(crate) fn run(args: &[String]) -> u8 {
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
            && !matches!(key, "turn" | "interaction" | "cwd" | "session_id")
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
