//! The connector records the worker effect before the supervisor can perform it.
use super::config::{Config, Place, invalid, snowflake};
use super::receipt::{boundary, hash, valid_boundary};
use crate::ipc::field;
use crate::providers::codex_receipt;
use crate::supervisor::runtime;
use serde_json::{Value, json};
use std::path::Path;
use std::{fs::File, io};

fn owned(supervisor: &Path, socket: &str) -> io::Result<(Place, Config)> {
    let place = Place::for_supervisor(supervisor)?;
    let config = place.load()?;
    place.ensure_owned(&config)?;
    if !config.enabled()
        || config.text("mode") != "personal"
        || config.text("daemon_socket") != socket
    {
        return Err(invalid("no owned personal connector for this supervisor"));
    }
    Ok((place, config))
}

pub(crate) fn worker_input_installation(supervisor: &Path, socket: &str) -> Option<String> {
    let (_, config) = owned(supervisor, socket).ok()?;
    Some(config.text("installation_id").into())
}

pub(crate) fn lock_connector_worker_input(
    supervisor: &Path,
    socket: &str,
    installation: &str,
    provider: &str,
    operation: &str,
    composer: &Value,
) -> io::Result<(File, File, File)> {
    let place = Place::for_supervisor(supervisor)?;
    // Match configure/disconnect order, then serialize worker ledger changes.
    let (supervisor_lock, connector_lock) = place.lock()?;
    let ledger_lock = lock_ledger(&place.dir)?;
    place.verify_selection()?;
    let config = place.load()?;
    place.ensure_owned(&config)?;
    verify(
        &place,
        &config,
        socket,
        installation,
        provider,
        operation,
        composer,
    )?;
    Ok((supervisor_lock, connector_lock, ledger_lock))
}

pub(super) fn lock_ledger(place: &Path) -> io::Result<File> {
    runtime::lock(&place.join(".rust-text-worker.lock"))
}

fn verify(
    place: &Place,
    config: &Config,
    socket: &str,
    installation: &str,
    provider: &str,
    operation: &str,
    composer: &Value,
) -> io::Result<()> {
    let refused = || invalid("semantic input does not match the exact connector worker cutover");
    let origin = &composer["origin"];
    let owner = config.text("owner_id");
    let channel = config.text("dm_channel_id");
    let source_id = origin["message_id"]
        .as_str()
        .filter(|id| snowflake(id))
        .ok_or_else(refused)?;
    if !config.enabled()
        || config.text("mode") != "personal"
        || config.text("daemon_socket") != socket
        || installation != config.text("installation_id")
        || origin["installation_id"] != installation
        || origin["kind"] != "owner_direct"
        || origin["actor_id"] != owner
        || origin["channel_id"] != channel
        || !field(origin, "requester_id").is_empty() && origin["requester_id"] != owner
        || ["guild_id", "endorser_id", "discussion_id"]
            .into_iter()
            .any(|key| !field(origin, key).is_empty())
        || !codex_receipt::timely(operation)
        || !composer["text"]
            .as_str()
            .is_some_and(|text| !text.is_empty() && text.len() <= 4096)
    {
        return Err(refused());
    }
    let ledger = runtime::read_json(&place.dir.join("rust-text-v1.json"))?;
    let source = &ledger["messages"][source_id];
    let cutover = &ledger["worker_inputs"][operation];
    let checkpoint = |field| {
        let value = &ledger[field];
        (value.is_string() && valid_boundary(value))
            .then(|| boundary(value))
            .ok_or_else(refused)
    };
    let floor = checkpoint("consent_floor")?;
    let cursor = checkpoint("history_cursor")?;
    let message = source_id.parse::<u64>().map_err(|_| refused())?;
    let revision = hash(&json!([
        owner,
        source["text"],
        channel,
        "",
        source_id,
        source["edited_at"],
        "",
        null
    ]))?;
    let orig_op = format!("da_{}", &hash(&json!([installation, source_id]))?[..32]);
    let expected = json!([
        installation,
        config.text("application_id"),
        owner,
        channel,
        socket
    ]);
    if ledger["version"] != 2
        || ledger["identity"] != expected
        || message <= floor
        || cursor < floor
        || !ledger["worker_excluded"][field(composer, "session")].is_null()
            && ledger["worker_excluded"][field(composer, "session")] != false
        || cutover["source_id"] != source_id
        || cutover["provider"] != provider
        || cutover["composer"] != *composer
        || source["state"] != "sending"
        || source["worker_operation"] != operation
        || !source["native_binding"].is_null()
        || source["authority"] != "owner_direct"
        || source["author"]["id"] != owner
        || source["route"] != json!({"guild_id":"","channel_id":channel,"message_id":source_id})
        || source["supervisor_socket"] != socket
        || !source["text"]
            .as_str()
            .is_some_and(|body| !body.trim().is_empty() && body.len() <= 16384)
        || source["revision"] != revision
        || origin["revision"] != revision
        || source["operation_id"] != orig_op
        || origin["request_id"] != orig_op
    {
        return Err(refused());
    }
    Ok(())
}
