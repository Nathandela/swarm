//! Plain public-thread history for an already-running original restricted Codex turn.
use super::*;
use crate::{
    discord::legacy_ingress::{
        self, bridge::Custody, manager_publication::prove_live_call, prepare as voice,
    },
    ipc::{Client, field},
};
use bbolt_rs::BucketApi;
use serde::Deserialize;
use std::path::Path;

#[path = "publication.rs"]
pub(super) mod publication;

const TOOL: &str = "swarm_discussion_history";
const CAPS: [&str; 2] = ["manager-provenance", "message-origin"];

fn unavailable() -> io::Error {
    invalid("the original running guild history tool is unqualified")
}

pub(super) fn accepted<'tx>(
    tx: &impl TxApi<'tx>,
    cfg: &Config,
) -> io::Result<Option<capture::Pending>> {
    let ingress = tx.bucket(b"teammate_ingress").ok_or_else(unavailable)?;
    let Some((key, raw)) = ingress.iter_entries().next() else {
        return Ok(None);
    };
    if raw.len() > 1 << 20 {
        return Err(unavailable());
    }
    let row: Value = serde_json::from_slice(raw)?;
    if row["State"] != "accepted" {
        return Err(unavailable());
    }
    capture::indexed_pending(tx, cfg, key, &row, field(&row["Admission"], "Revision")).map(Some)
}

pub(super) fn verify_record<'tx>(
    tx: &impl TxApi<'tx>,
    cfg: &Config,
    pending: &capture::Pending,
    bound: &prepare::Discussion,
) -> io::Result<()> {
    let mut expected: Value =
        serde_json::from_slice(&prepare::sending_record(cfg, pending, bound)?)?;
    expected["State"] = json!("accepted");
    // Go rewrites Mutation first when changing its original sending record to accepted.
    let fields = expected.as_object_mut().ok_or_else(unavailable)?;
    let mutation = fields.shift_remove("Mutation").ok_or_else(unavailable)?;
    let mut canonical = serde_json::Map::new();
    canonical.insert("Mutation".to_owned(), mutation);
    canonical.append(fields);
    let raw = tx.bucket(b"teammate_ingress").ok_or_else(unavailable)?;
    if raw.get(&pending.ingress_key) != Some(voice::go_bytes(&Value::Object(canonical))?.as_slice())
    {
        return Err(invalid(
            "the original accepted guild record is noncanonical",
        ));
    }
    Ok(())
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Request {
    version: u8,
    conversation_id: String,
    session_id: String,
    call: Call,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Call {
    name: String,
    arguments: Value,
    meta: Value,
}

#[derive(Default, Deserialize)]
#[serde(default, deny_unknown_fields)]
struct Arguments {
    source: String,
    cursor: String,
    anchor: String,
}

fn checked_request(raw: &[u8], request: &Value) -> io::Result<(Call, String)> {
    let call = scoped_call(raw, request, TOOL)?;
    let args = Arguments::deserialize(&call.arguments)?;
    if !matches!(args.source.as_str(), "" | "thread") || !args.anchor.is_empty() {
        return Err(unavailable());
    }
    Ok((call, args.cursor))
}

fn scoped_call(raw: &[u8], request: &Value, name: &str) -> io::Result<Call> {
    let wire: Value = serde_json::from_slice(raw)?;
    if !wire.is_object() || !wire["call"].is_object() || voice::go_bytes(&wire)? != raw {
        return Err(unavailable());
    }
    let tool: Request = serde_json::from_slice(raw)?;
    let (_, session) = field(request, "session")
        .split_once('/')
        .ok_or_else(unavailable)?;
    let binding = &request["manager_admission"]["binding"];
    if tool.version != 1
        || tool.conversation_id != binding["conversation_id"]
        || tool.session_id != session
        || tool.call.name != name
        || !tool.call.arguments.is_object()
        || !tool.call.meta.is_object()
    {
        return Err(unavailable());
    }
    Ok(tool.call)
}

fn owner(place: &Place, cfg: &Config, request: &Value) -> io::Result<Client> {
    let (owner, rows) = super::super::migrate_supervisor::verified_owner(
        place.supervisor_root(),
        Path::new(cfg.text("daemon_socket")),
        Path::new(field(&cfg.0["supervisor"], "lock")),
        &CAPS,
    )?;
    cfg.check_supervisor(&owner, &CAPS)?;
    let binding = &request["manager_admission"]["binding"];
    if rows
        .iter()
        .filter(|row| {
            row["id"] == request["session"]
                && row["role"] == "manager"
                && row["manager_conversation_id"] == binding["conversation_id"]
                && row["agent"] == "codex"
                && row["status"]["Process"] == "running"
                && row["cwd"] == cfg.text("work_dir")
                && row["capabilities"]["session_instance"] == request["session_instance"]
        })
        .count()
        != 1
    {
        return Err(invalid(
            "the original restricted Codex manager row is unavailable",
        ));
    }
    Ok(owner)
}

fn verify_live_runtime(native: &Value, request: &Value, custody: &Custody) -> io::Result<()> {
    let expected = &request["manager_admission"];
    let turn = &native["turn"];
    if native["binding"] != expected["binding"]
        || native["ready_for_input"] != false
        || !matches!(native["native_cli"], Value::Null | Value::Bool(false))
        || native["provider_exhausted"] == true
        || !custody.matches_runtime(native)
        || turn["state"] != "running"
        || turn["native_input_id"] != expected["nonce"]
        || field(turn, "native_turn_id").is_empty()
        || turn["external_input"] != 0
        || !matches!(turn["externally_steered"], Value::Null | Value::Bool(false))
    {
        return Err(invalid(
            "the saved request no longer matches the running restricted Codex turn",
        ));
    }
    Ok(())
}

#[derive(PartialEq)]
pub(super) struct Scope {
    pub(super) input: render::Input,
    pub(super) key: Option<history_pages::Key>,
}

pub(super) fn serve(
    place: &Place,
    cfg: &Config,
    binding: &Value,
    status: &StatusSocket,
    recheck: impl Fn() -> io::Result<Scope>,
    older: impl Fn(&history_pages::Cursor) -> io::Result<Value>,
) -> io::Result<()> {
    let scope = recheck()?;
    let input = &scope.input;
    let discussion = field(&input.origin, "discussion_id");
    let upper = field(&input.history_page, "HighWater");
    let mut initial = input.history_page.clone();
    let initial_next = history_pages::advance(&mut initial, scope.key.as_ref(), discussion)?;
    let settings = crate::manager_prompt::load(place.supervisor_root())?;
    let (directory, id, endpoint) = legacy_ingress::voice_location(place, cfg, binding)?;
    let custody = Custody::claim(&directory, &id, true)?;
    open_owner_file(&directory.join("voice.initialized"))?;
    let path = directory.join("voice.db");
    let (pin, db) = legacy_ingress::voice_ledger::open(&path)?;
    let read = || {
        if !legacy_state::unchanged(&pin, &path)?
            || crate::manager_prompt::load(place.supervisor_root())? != settings
        {
            return Err(invalid(
                "the original Voice file or manager settings changed",
            ));
        }
        let tx = db.begin().map_err(io::Error::other)?;
        let mut voice = voice::read_executing_guild(&tx, &id, cfg, &endpoint, &input.origin)?;
        voice::executing_guild_admission(
            &mut voice,
            cfg,
            &input.origin,
            &input.text,
            &input.prompt,
            &input.project,
            &settings,
        )
    };
    let row = read()?;
    let request = &row["request"];
    let mut startup = owner(place, cfg, request)?;
    let runtime = startup.request_body(
        json!({"op":"manager_runtime","session_id":request["session"],"body_version":1,
        "manager_runtime_request":{"session_instance":request["session_instance"]}}),
        "manager_runtime",
    )?;
    verify_live_runtime(&runtime["manager_runtime"], request, &custody)?;
    if recheck()? != scope || read()? != row {
        return Err(invalid(
            "the original guild history source changed before custody",
        ));
    }
    let bridge = {
        let _stop = status.commit_guard()?;
        custody.listen()?
    };
    println!("Serving original public-thread history for the running teammate turn.");
    let mut expected_cursor = Some(String::new());
    let until = Instant::now() + Duration::from_secs(45);
    while !status.stopping() && Instant::now() < until {
        #[cfg(test)]
        let mut closing_fixture = false;
        #[cfg(test)]
        let mut source_verified = false;
        let _reply = bridge.accept_guarded(|raw| {
            let (call, cursor) = checked_request(raw, request)?;
            if expected_cursor.as_ref() != Some(&cursor) {
                return Err(unavailable());
            }
            let mut owner = owner(place, cfg, request)?;
            let prove = |owner: &mut Client| {
                let (native, turn) =
                    prove_live_call(owner, request, TOOL, &call.arguments, &call.meta, false)?;
                verify_live_runtime(&native, request, &custody)?;
                Ok::<_, io::Error>(turn)
            };
            let turn = prove(&mut owner)?;
            #[cfg(test)]
            {
                closing_fixture = super::history_proof_fixture(place, cfg, raw, cursor.is_empty())?;
            }
            let (page, next) = if cursor.is_empty() {
                (initial.clone(), initial_next.clone())
            } else {
                let cursor = history_pages::decode(scope.key.as_ref(), &cursor, discussion, upper)?;
                let mut page = older(&cursor)?;
                let next = history_pages::advance(&mut page, scope.key.as_ref(), discussion)?;
                (page, next)
            };
            if recheck()? != scope || read()? != row {
                return Err(unavailable());
            }
            #[cfg(test)]
            {
                source_verified = true;
            }
            if prove(&mut owner)? != turn {
                return Err(unavailable());
            }
            let stop = status.commit_guard()?;
            expected_cursor = next;
            Ok((page, stop))
        })?;
        #[cfg(test)]
        if closing_fixture {
            assert!(
                source_verified,
                "the delayed REST response must verify before rechecking the native call"
            );
            assert!(
                _reply.is_none(),
                "Rust prepared a page for the closed native call"
            );
            println!("Withheld the history response from the closed native call.");
        }
        std::thread::sleep(Duration::from_millis(20));
    }
    Ok(())
}
