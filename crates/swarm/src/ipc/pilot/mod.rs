mod context;
mod guidance;
mod watch;

use super::{Client, duration, field, json_out, options, spawn};
use crate::providers::codex_receipt;
use crate::supervisor::runtime;
use context::{Context, lock};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::env;
use std::io;
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

const CAPS: &[&str] = &[
    "journal",
    "journal-subscribe-from",
    "subscribe",
    "opencode-observation",
    "semantic-send",
    "semantic-target",
    "owner-launch",
];
const TEN_MINUTES: Duration = Duration::from_secs(600);
type Result<T> = std::result::Result<T, (u8, String)>;

fn fail(err: impl std::fmt::Display) -> (u8, String) {
    (1, err.to_string())
}

fn misuse(err: impl std::fmt::Display) -> (u8, String) {
    (2, err.to_string())
}

pub(crate) fn run(args: &[String], entry: bool) -> u8 {
    let result = if entry { begin(args) } else { execute(args) };
    match result {
        Ok(code) => code,
        Err((code, message)) => {
            eprintln!("pilot: {message}");
            code
        }
    }
}

fn begin(args: &[String]) -> Result<u8> {
    if !args.is_empty() {
        return Err(misuse("use swarm --pilot without arguments"));
    }
    let (ctx, mut client) = Context::entry().map_err(fail)?;
    let result = (|| {
        let rows = roster(&mut client)?;
        json_out(&json!({
            "context":ctx.path,
            "trusted_guidance":format!("{}\n{}",guidance::ROLE,guidance::HELP),
            "worker_data":rows,
        }))
        .map_err(fail)?;
        Ok(0)
    })();
    if result.is_err() {
        let _ = Context::exit(&ctx.path);
    }
    result
}

fn execute(args: &[String]) -> Result<u8> {
    if matches!(args, [flag] if flag=="--help" || flag=="-h") {
        println!("{}", guidance::HELP);
        return Ok(0);
    }
    let mut path = "";
    let rest = options(args, &["context"], &[], |_, value| {
        path = value;
        Ok(())
    })
    .map_err(misuse)?;
    let Some((op, args)) = rest.split_first().filter(|_| !path.is_empty()) else {
        return Err(misuse(
            "use swarm pilot --context <path> roster|open|view|send|await|watch|create|resume|exit",
        ));
    };
    let path = Path::new(path);
    if op == "exit" {
        no_args(args, "exit")?;
        Context::exit(path).map_err(fail)?;
        json_out(&json!({"trusted_guidance":"Pilot context closed.","receipt":"exited"}))
            .map_err(fail)?;
        return Ok(0);
    }
    let _guard = if matches!(op.as_str(), "watch" | "await") {
        None
    } else {
        Some(lock(path).map_err(fail)?)
    };
    let (ctx, mut client) = Context::connect(path, CAPS).map_err(fail)?;
    let mut receipt = op.as_str();
    let mut outcome = json!({"trusted_guidance":guidance::REMINDER});
    let mut settle = None;
    let mut code = 0;
    let data = match op.as_str() {
        "watch" => return watch::run(&ctx, &mut client, args),
        "roster" => {
            no_args(args, "roster")?;
            roster(&mut client)?
        }
        "open" | "view" => {
            let id = one(args, op)?;
            view(&mut client, id)?
        }
        "create" | "resume" => {
            let (data, outbound, operation) = launch(&ctx, &mut client, op, args)?;
            if let Some(outbound) = outbound {
                outcome["outbound"] = outbound;
            }
            receipt = if op == "create" { "created" } else { "resumed" };
            settle = Some(operation);
            data
        }
        "launch-status" => {
            let id = one(args, op)?;
            let data = launch_status(&mut client, id)?;
            if data["operation"]["state"] == "applied"
                && !field(&data["operation"], "session_id").is_empty()
            {
                settle = Some(id.to_owned());
            }
            data
        }
        "send" => {
            let (data, outbound) = send(&ctx, &mut client, args)?;
            outcome["outbound"] = outbound;
            receipt = "submitted";
            data
        }
        "await" => {
            let (data, timed_out) = watch::await_reply(&ctx, &mut client, args)?;
            outcome["work_state"] = json!(work_state(&data["state"]));
            receipt = if timed_out {
                code = 2;
                "waiting"
            } else if data["reply_confirmed"] == true {
                "reply_received"
            } else {
                "state_changed"
            };
            data
        }
        _ => return Err(misuse(format!("unknown operation {op}"))),
    };
    ctx.check().map_err(fail)?;
    outcome["receipt"] = json!(receipt);
    if receipt == "reply_received" && native_terminal(&data["operation"]) {
        outcome["receipt"] = json!(format!("input_{}", field(&data["operation"], "state")));
    }
    outcome["worker_data"] = data;
    json_out(&outcome).map_err(fail)?;
    if let Some(operation) = settle {
        settle_launch(path, &operation)?;
    }
    if receipt == "submitted" {
        let data = &outcome["worker_data"];
        let pending = pending_path(&ctx, field(data, "discussion_id"));
        if let Some(mut record) = read_optional(&pending)?
            && record["operation_id"] == data["operation_id"]
        {
            record["uncertain"] = json!(false);
            save(&pending, &record)?;
        }
    }
    Ok(code)
}

fn no_args(args: &[String], action: &str) -> Result<()> {
    if args.is_empty() {
        Ok(())
    } else {
        Err(misuse(format!("{action} takes no arguments")))
    }
}

fn one<'a>(args: &'a [String], action: &str) -> Result<&'a str> {
    if let [id] = args
        && !id.is_empty()
        && !id.starts_with('-')
    {
        return Ok(id);
    }
    Err(misuse(format!("{action} needs one explicit discussion ID")))
}

fn roster(client: &mut Client) -> Result<Value> {
    Ok(json!(
        client
            .sessions()
            .map_err(fail)?
            .into_iter()
            .filter(|row| row["roster_hidden"] != true && field(row, "superseded_by").is_empty())
            .collect::<Vec<_>>()
    ))
}

fn target(client: &mut Client, id: &str) -> Result<Value> {
    client
        .sessions()
        .map_err(fail)?
        .into_iter()
        .find(|row| row["id"] == id)
        .ok_or_else(|| fail(format!("discussion {id:?} is absent; refresh roster")))
}

fn history(client: &mut Client, id: &str) -> io::Result<Vec<Value>> {
    let message = client.request_body(
        json!({
            "op":"interaction_history","session_id":id,"history":{"session":id,"limit":20}
        }),
        "interaction_history",
    )?;
    Ok(message["journal"].as_array().cloned().unwrap_or_default())
}

fn view(client: &mut Client, id: &str) -> Result<Value> {
    let row = target(client, id)?;
    if let Ok(items) = history(client, id)
        && !items.is_empty()
    {
        return Ok(json!({"state":row,"history":items,"source":"structured_history"}));
    }
    client
        .request("terminal_subscribe", Some(id), "ok")
        .map_err(fail)?;
    let deadline = Instant::now() + Duration::from_secs(10);
    let local = id.split_once('/').map_or(id, |(_, local)| local);
    loop {
        let frame = client.push(deadline).map_err(fail)?;
        if frame["op"] == "terminal_snapshot"
            && (frame["terminal"]["session"] == local || frame["terminal"]["session"] == id)
        {
            return Ok(
                json!({"state":row,"terminal_snapshot":frame["terminal"],"source":"terminal_screen_fallback"}),
            );
        }
    }
}

fn read_optional(path: &Path) -> Result<Option<Value>> {
    match runtime::read_json(path) {
        Ok(record) if record.is_object() => Ok(Some(record)),
        Ok(_) => Err(fail(
            "saved pilot operation is corrupt; reconcile before sending new work",
        )),
        Err(err) if err.kind() == io::ErrorKind::NotFound => Ok(None),
        Err(err) => Err(fail(err)),
    }
}

fn save(path: &Path, body: &Value) -> Result<()> {
    runtime::write_json(path, body).map_err(fail)
}

fn pending_path(ctx: &Context, id: &str) -> PathBuf {
    ctx.path.join(format!("pending-{:x}", Sha256::digest(id)))
}

fn launch(
    ctx: &Context,
    client: &mut Client,
    op: &str,
    args: &[String],
) -> Result<(Value, Option<Value>, String)> {
    if !client.supports("owner-launch") {
        return Err(fail(
            "reliable launch requires an updated daemon; run swarm daemon restart",
        ));
    }
    let path = ctx.path.join("launch-pending");
    let pending = match read_optional(&path)? {
        Some(record) if record["settled"] == false => {
            if field(&record, "operation_id").is_empty() || !record["request"].is_object() {
                return Err(fail(
                    "saved launch is corrupt; reconcile before launching new work",
                ));
            }
            if record["kind"] != op || record["arguments"] != json!(args) {
                return Err(fail(format!(
                    "operation_id={}: another launch has an unresolved outcome; retry its exact command",
                    field(&record, "operation_id")
                )));
            }
            record
        }
        Some(record) if record["settled"] != true => return Err(fail("saved launch is corrupt")),
        _ => {
            let mut request = if op == "resume" {
                let id = one(args, op)?;
                let row = target(client, id)?;
                if row["status"]["Process"] == "running" {
                    return Err(fail("discussion is already running"));
                }
                json!({"agent":row["agent"],"name":row["name"],"cwd":row["cwd"],"options":{"resume_from":id}})
            } else {
                let mut flags = std::collections::BTreeMap::new();
                let extra = options(
                    args,
                    &["cli", "name", "dir", "prompt", "tag"],
                    &[],
                    |k, v| {
                        flags.insert(k.to_owned(), v);
                        Ok(())
                    },
                )
                .map_err(misuse)?;
                let get = |k: &str| flags.get(k).copied().unwrap_or_default();
                if !extra.is_empty() || get("cli").is_empty() || get("prompt").is_empty() {
                    return Err(misuse(
                        "create needs --cli <agent> --prompt <exact task> [--dir d] [--name n] [--tag t]",
                    ));
                }
                let cwd = if get("dir").is_empty() {
                    env::current_dir().map_err(fail)?
                } else {
                    spawn::absolute(Path::new(get("dir"))).map_err(fail)?
                };
                let mut result =
                    json!({"agent":get("cli"),"cwd":cwd,"initial_prompt":get("prompt")});
                if !get("name").is_empty() {
                    result["name"] = json!(get("name"))
                }
                if !get("tag").is_empty() {
                    result["tag"] = json!(get("tag"))
                }
                result
            };
            let env: Vec<_> = env::vars_os()
                .map(|(k, v)| format!("{}={}", k.to_string_lossy(), v.to_string_lossy()))
                .collect();
            if request["agent"] == "codex" {
                crate::providers::codex::refuse_unqualified_origin(&json!(env)).map_err(fail)?;
            }
            request["env"] = json!(crate::environment::reliable(field(&request, "agent"), env));
            request["cols"] = json!(80);
            request["rows"] = json!(24);
            let record = json!({"operation_id":spawn::new_id().map_err(fail)?,"kind":op,"arguments":args,"request":request,"settled":false});
            save(&path, &record)?;
            record
        }
    };
    let id = field(&pending, "operation_id");
    if !spawn::replayable(id, spawn::now_ms()) {
        return Err(fail(format!(
            "operation_id={id}: launch retry window expired; read launch-status"
        )));
    }
    ctx.check().map_err(fail)?;
    let reply = client
        .exchange(
            json!({"op":"owner_launch","operation_id":id,"launch":pending["request"]}),
            "launch",
            Duration::from_secs(110),
        )
        .map_err(|err| fail(format!("operation_id={id}: {err}")))?;
    if reply["operation_id"] != id || field(&reply["session"], "id").is_empty() {
        return Err(fail(format!(
            "operation_id={id}: uncorrelated launch receipt; outcome unknown"
        )));
    }
    let mut data = json!({"discussion_id":reply["session"]["id"],"name":field(&reply["session"],"name"),"operation_id":id});
    let outbound = if op == "create" {
        Some(json!({"prompt":pending["request"]["initial_prompt"]}))
    } else {
        data["resumed_from"] = json!(args[0]);
        None
    };
    Ok((data, outbound, id.to_owned()))
}

fn settle_launch(path: &Path, id: &str) -> Result<()> {
    let path = path.join("launch-pending");
    if let Some(mut record) = read_optional(&path)?
        && record["operation_id"] == id
    {
        record["settled"] = json!(true);
        save(&path, &record)?;
    }
    Ok(())
}

fn launch_status(client: &mut Client, id: &str) -> Result<Value> {
    if !client.supports("owner-launch") {
        return Err(fail("launch status requires an updated daemon"));
    }
    let reply = client
        .request_body(
            json!({"op":"owner_launch_status","subject_operation_id":id}),
            "owner_launch_status",
        )
        .map_err(fail)?;
    if reply["subject_operation_id"] != id || !reply["operation_outcome"].is_object() {
        return Err(fail("uncorrelated launch receipt"));
    }
    Ok(json!({"operation_id":id,"operation":reply["operation_outcome"]}))
}

fn operation_status(client: &mut Client, session: &str, id: &str) -> Result<Value> {
    let reply = client
        .request_body(
            json!({"op":"semantic_status","session_id":session,"operation_id":id}),
            "semantic_status",
        )
        .map_err(fail)?;
    let r = &reply["semantic_receipt"];
    if reply["operation_id"] != id || r["operation_id"] != id || r["session"] != session {
        return Err(fail("uncorrelated semantic receipt"));
    }
    Ok(r.clone())
}

fn semantic_instance(client: &mut Client, id: &str) -> Result<String> {
    if client.supports("semantic-target") {
        let reply = client
            .request("semantic_target", Some(id), "semantic_target")
            .map_err(fail)?;
        if reply["session_id"] != id || field(&reply, "semantic_target_instance").is_empty() {
            return Err(fail("uncorrelated semantic target"));
        }
        return Ok(field(&reply, "semantic_target_instance").to_owned());
    }
    let row = target(client, id)?;
    if row["status"]["Process"] != "running" {
        return Err(fail("discussion is not running; resume it explicitly"));
    }
    let instance = field(&row["capabilities"], "session_instance");
    if instance.is_empty() {
        return Err(fail("discussion has no current session incarnation"));
    }
    Ok(instance.to_owned())
}

fn send(ctx: &Context, client: &mut Client, args: &[String]) -> Result<(Value, Value)> {
    let Some((session, rest)) = args
        .split_first()
        .filter(|(s, _)| !s.is_empty() && !s.starts_with('-'))
    else {
        return Err(misuse("send needs an explicit discussion ID"));
    };
    let mut text = "";
    let extra = options(rest, &["text"], &[], |_, value| {
        text = value;
        Ok(())
    })
    .map_err(misuse)?;
    if !extra.is_empty() || text.is_empty() {
        return Err(misuse("send requires --text <exact message>"));
    }
    if !client.supports("semantic-send") {
        return Err(fail("semantic messages require an updated daemon"));
    }
    let path = pending_path(ctx, session);
    let prior = read_optional(&path)?;
    if prior.as_ref().is_some_and(|pending| {
        !pending["uncertain"].is_boolean() || field(pending, "operation_id").is_empty()
    }) {
        return Err(fail("pending operation is corrupt; nothing was sent"));
    }
    let req = json!({"session":session,"session_instance":semantic_instance(client,session)?,"text":text});
    let retry = prior
        .as_ref()
        .is_some_and(|record| record["uncertain"] == true);
    let mut pending = if let Some(prior) = prior.filter(|record| record["uncertain"] == true) {
        if prior["request"] != req {
            return Err(fail(format!(
                "operation_id={} remains uncertain; only its exact request can be retried",
                field(&prior, "operation_id")
            )));
        }
        prior
    } else {
        let lower = history(client, session).ok().map(|rows| {
            rows.iter()
                .filter_map(|r| r["cursor"].as_u64())
                .max()
                .unwrap_or(0)
        });
        json!({"operation_id":codex_receipt::operation().map_err(fail)?,"request":req,
            "sent_at":OffsetDateTime::now_utc().format(&Rfc3339).map_err(fail)?,
            "cursor":lower.unwrap_or_default(),"history_ready":lower.is_some(),"uncertain":true})
    };
    let id = field(&pending, "operation_id").to_owned();
    if !codex_receipt::timely(&id) {
        return Err(fail(format!(
            "operation_id={id}: expired; original outcome remains uncertain"
        )));
    }
    save(&path, &pending)?;
    ctx.check().map_err(fail)?;
    client.send(&json!({"op":"semantic_send","endpoint_id":client.endpoint(),"session_id":session,"operation_id":id,
        "body_version":1,"composer_send":req})).map_err(fail)?;
    let reply = client
        .receive(Instant::now() + Duration::from_secs(35))
        .map_err(|err| fail(format!("operation_id={id}: {err}")))?;
    if reply["op"] == "error" {
        if !retry
            && matches!(
                field(&reply, "error_code"),
                "input_busy" | "stale_turn" | "stale_instance" | "structured_unsupported"
            )
        {
            std::fs::remove_file(&path).map_err(fail)?;
        }
        return Err(fail(format!(
            "operation_id={id}: {}",
            field(&reply, "error")
        )));
    }
    if reply["op"] != "ok"
        || reply["operation_id"] != id
        || reply["endpoint_id"] != client.endpoint()
    {
        return Err(fail(format!(
            "operation_id={id}: uncorrelated semantic reply; outcome unknown"
        )));
    }
    // Keep the exact retry guard until the submission receipt reaches the caller.
    if let Ok(op) = operation_status(client, session, &id) {
        pending["receipt"] = op;
    }
    save(&path, &pending)?;
    let mut data = json!({"discussion_id":session,"operation_id":id});
    if pending["history_ready"] == true {
        data["watch_after_cursor"] = pending["cursor"].clone()
    }
    if pending["receipt"].is_object() {
        data["operation"] = pending["receipt"].clone()
    }
    Ok((data, json!({"discussion_id":session,"text":text})))
}

fn native_terminal(receipt: &Value) -> bool {
    native_input(receipt)
        && matches!(
            field(receipt, "state"),
            "completed" | "failed" | "cancelled" | "declined"
        )
}

fn native_input(receipt: &Value) -> bool {
    !field(receipt, "native_message_id").is_empty() || !field(receipt, "native_turn_id").is_empty()
}

fn work_state(row: &Value) -> &'static str {
    match field(&row["status"], "Process") {
        "exited" => "finished",
        "lost" | "" => "unknown",
        "running" if row["group"] == "working" => "working",
        _ => "waiting",
    }
}
