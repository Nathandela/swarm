use super::{Calendar, Result, worker};
use crate::ipc::{self, Client};
use serde_json::{Value, json};
use std::{env, io, time::Duration};

const CAPS: &[&str] = &[
    "schedule",
    "schedule-run-id",
    "schedule-workspace-check",
    "schedule-route-check",
    "schedule-host-time",
    "schedule-native",
];
const UNSUPPORTED: &str =
    "running daemon does not support scheduled jobs; upgrade and restart the Swarm daemon";

fn dial() -> io::Result<Client> {
    Client::dial(&ipc::socket_path()?, CAPS)
}

fn missing(e: &io::Error) -> bool {
    matches!(
        e.kind(),
        io::ErrorKind::NotFound | io::ErrorKind::ConnectionRefused
    )
}

pub(super) fn owner(from_timer: bool) -> Result<Client> {
    let client = match dial() {
        Ok(client) => client,
        Err(e) if missing(&e) => {
            crate::bootstrap::ensure(from_timer)?;
            dial()?
        }
        Err(e) => return Err(e.into()),
    };
    if !client.supports("schedule") {
        return Err(UNSUPPORTED.into());
    }
    Ok(client)
}

pub(super) fn qualified_timer() -> Result<()> {
    if !owner(false)?.supports("schedule-native") {
        return Err("timer installation requires the native schedule supervisor with unattended startup support".into());
    }
    Ok(())
}

pub(super) fn host_calendar(calendar: &Calendar) -> Result<()> {
    if calendar.time_zone != "host" {
        return Ok(());
    }
    match dial() {
        Ok(client) if client.supports("schedule-host-time") => Ok(()),
        Ok(_) => Err(
            "restart the updated Swarm daemon before enabling a schedule that follows host time"
                .into(),
        ),
        Err(e) if missing(&e) => Ok(()),
        Err(e) => Err(e.into()),
    }
}

fn request(client: &mut Client, body: Value, expected: &str) -> Result<Value> {
    let reply = client.exchange(body, expected, Duration::from_secs(110))?;
    if reply["endpoint_id"].as_str() != Some(client.endpoint()) {
        return Err("scheduled automation response could not be correlated".into());
    }
    Ok(reply)
}

pub(super) fn tick() -> Result<()> {
    request(
        &mut owner(true)?,
        json!({"op":"schedule_tick"}),
        "schedule_tick",
    )?;
    Ok(())
}

pub(super) fn run(client: &mut Client, job: &str, invocation: &str) -> Result<String> {
    if !client.supports("schedule-run-id") {
        return Err("run now requires a daemon that supports schedule operation IDs".into());
    }
    let reply = request(
        client,
        json!({"op":"schedule_run", "schedule_id":job, "operation_id":invocation}),
        "schedule_run",
    )?;
    if reply["operation_id"].as_str() != Some(invocation) {
        return Err("schedule run response could not be correlated with its operation ID".into());
    }
    Ok(reply["schedule_id"]
        .as_str()
        .filter(|s| !s.is_empty())
        .ok_or("scheduled automation response could not be correlated")?
        .to_owned())
}

pub(super) fn complete(args: &[&str]) -> Result<()> {
    let mut summary = "";
    let mut links = Vec::new();
    let usage = "usage: swarm schedule complete --summary TEXT [--link LINK]";
    let mut args = args.iter().copied();
    while let Some(arg) = args.next() {
        let (flag, explicit) = arg
            .split_once('=')
            .map_or((arg, None), |(k, v)| (k, Some(v)));
        let value = explicit.or_else(|| args.next()).ok_or(usage)?;
        match flag {
            "--summary" | "-summary" => summary = value,
            "--link" | "-link" => links.push(value),
            _ => return Err(usage.into()),
        }
    }
    if summary.trim().is_empty() {
        return Err(usage.into());
    }
    submit(
        summary,
        &links.into_iter().map(str::to_owned).collect::<Vec<_>>(),
    )
}

pub(super) fn submit(summary: &str, links: &[String]) -> Result<()> {
    let env = |name| {
        env::var(name)
            .ok()
            .filter(|s| !s.is_empty())
            .ok_or("complete must run inside the active scheduled worker session")
    };
    let run_id = env(worker::RUN)?;
    let attempt_id = env(worker::ATTEMPT)?;
    let token = env(worker::PROOF)?;
    let socket = env(worker::SOCKET)?;
    let session = env(worker::SESSION)?;
    let mut client = Client::dial(socket.as_ref(), &["schedule"])?;
    if !client.supports("schedule") {
        return Err(UNSUPPORTED.into());
    }
    let namespaced = format!("{}/{}", client.endpoint(), session);
    request(
        &mut client,
        json!({
            "op":"schedule_complete", "session_id":namespaced,
            "schedule_completion":{"run_id":run_id, "attempt_id":attempt_id, "token":token, "summary":summary, "links":links},
        }),
        "schedule_complete",
    )?;
    Ok(())
}
