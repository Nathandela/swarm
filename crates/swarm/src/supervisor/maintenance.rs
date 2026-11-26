use super::{
    State, runtime,
    session::{Session, maintenance::auth_identity},
};
use crate::{
    ipc::field,
    providers::{self, codex_receipt},
    refresh,
};
use serde_json::{Value, json};
use std::{
    io,
    path::Path,
    sync::Arc,
    thread,
    time::{Duration, Instant},
};

pub(super) fn manual(state: &State, request: &Value) -> io::Result<Value> {
    let root = root(state)?;
    if request["state_dir"].as_str().map(Path::new) != Some(root)
        || !request["dry_run"].is_boolean()
        || !request["force"].is_boolean()
        || state.stopped_go.is_some()
    {
        return Err(io::Error::other(
            "native recovery requires this supervisor and its exact state directory",
        ));
    }
    let messages = tick(
        state,
        Some((request["dry_run"] == true, request["force"] == true)),
    )?;
    Ok(json!({"op":"native_relogin", "messages":messages}))
}

pub(super) fn run(state: Arc<State>) {
    loop {
        let _transaction = state.transactions.read().unwrap();
        if let Err(e) = tick(&state, None) {
            eprintln!("native recovery: {e}");
        }
        drop(_transaction);
        thread::sleep(Duration::from_secs(30));
    }
}

fn root(state: &State) -> io::Result<&Path> {
    state
        .root
        .parent()
        .ok_or_else(|| io::Error::other("missing native maintenance state root"))
}

fn save(root: &Path, doc: &Value) -> io::Result<()> {
    if serde_json::to_vec(doc)?.len() > 900_000 {
        return Err(io::Error::other("native maintenance journal is full"));
    }
    runtime::write_json(&root.join(refresh::RECORDS), doc)
}

fn tick(state: &State, manual: Option<(bool, bool)>) -> io::Result<Vec<String>> {
    let mut cursor = state
        .maintenance_gate
        .try_lock()
        .map_err(|_| io::Error::other("native recovery is already reconciling"))?;
    let root = root(state)?;
    let mut messages = Vec::new();
    let mut acted = false;
    if refresh::legacy_requires_recovery(root)? {
        return Ok(vec![
            "hold      an unresolved Go recovery journal needs offline review".into(),
        ]);
    }
    let auth_off = refresh::disabled(root, refresh::AUTH).unwrap_or(true);
    let cli_off = refresh::disabled(root, refresh::POLICY).unwrap_or(true);
    let mut doc = refresh::records(root)?;
    let mut roster: Vec<_> = state.sessions.read().unwrap().values().cloned().collect();
    if manual.is_none() {
        let next = roster.partition_point(|session| session.id <= *cursor);
        roster.rotate_left(next);
    }
    let mut attempts = 0;
    for source in &roster {
        if attempts >= 8 {
            break;
        }
        let id = &source.id;
        if manual.is_none() {
            cursor.clone_from(id);
        }
        let view = source.view(&state.endpoint);
        let label = format!("{}  {}", field(&view, "id"), field(&view, "name"));
        let existing = doc["records"][id].clone();
        if !existing.is_null()
            && manual.is_none_or(|(dry, _)| !dry)
            && matches!(field(&existing, "state"), "claimed" | "waiting")
        {
            attempts += 1;
            let policy = existing["policy"].as_str();
            let enabled =
                policy.is_none_or(|name| matches!(refresh::disabled(root, name), Ok(false)));
            if enabled
                || source
                    .maintenance_stopped(field(&existing, "nonce"))
                    .unwrap_or(false)
            {
                match advance(state, source, &mut doc) {
                    Ok(Some(replacement)) => {
                        acted = true;
                        messages.push(format!("started   {} -> {replacement}", field(&view, "id")));
                    }
                    Ok(None) => {}
                    Err(err) => messages.push(format!("hold      {label}: {err}")),
                }
            }
            continue;
        }
        if !source.is_live() {
            continue;
        }
        let Ok(meta) = source.maintenance_source() else {
            continue;
        };
        let agent = field(&meta["provider"], "name");
        let observed = source.maintenance_observation().unwrap_or(Value::Null);
        let thread = field(&observed, "conversation_id");
        let account = auth_identity(agent, &meta["maintenance_env"]);
        let save_stamp = meta["auth_stamp"].as_str();
        let changed_auth = account
            .as_deref()
            .is_some_and(|id| save_stamp.is_some_and(|old| id != old));
        let candidate = match manual {
            Some((_, force)) if agent == "codex" => {
                let local = crate::supervisor::runtime::launch_env(&Value::Null);
                let local = json!(
                    local
                        .iter()
                        .map(|(key, value)| format!("{key}={value}"))
                        .collect::<Vec<_>>()
                );
                if account.is_some() && auth_identity(agent, &local) != account {
                    messages.push(format!("hold      {label}: different or unreadable session credentials; automatic recovery cannot judge this store"));
                    continue;
                }
                if account.is_none() {
                    messages.push(format!("hold      {label}: codex credentials unreadable (mid-login?); nothing judged"));
                    continue;
                }
                if changed_auth && !auth_off {
                    messages.push(format!(
                        "watcher   {label}: changed identity; the daemon checks native recovery"
                    ));
                    continue;
                }
                if !force && !changed_auth {
                    continue;
                }
                "auth"
            }
            None if changed_auth && !auth_off => "auth",
            None if !cli_off => "cli",
            _ => continue,
        };
        if !existing.is_null()
            && existing["kind"] == candidate
            && matches!(field(&existing, "state"), "complete" | "blocked")
        {
            continue;
        }
        if !codex_receipt::canonical_thread(thread) {
            if manual.is_some() {
                messages.push(format!(
                    "manual    {label}: no captured conversation id; restart it yourself"
                ));
            }
            continue;
        }
        if candidate == "auth" && changed_auth {
            if manual.is_some() {
                messages.push(format!("hold      {label}: the source's native account changed; prove that the new login can open its original thread before recovery"));
            }
            continue;
        }
        attempts += 1;
        let Ok(target) = source.maintenance_target(&meta) else {
            continue;
        };
        let changed_cli = meta["provider"]["identity"] != target.identity
            || meta["provider"]["executable"] != json!(target.executable);
        let higher = providers::claude::release(&target.version)
            .zip(providers::claude::release(field(
                &meta["provider"],
                "version",
            )))
            .is_some_and(|(candidate, previous)| candidate > previous);
        if candidate == "cli"
            && (!changed_cli
                || agent != "opencode"
                    && (!higher
                        || !target.stable_release
                        || meta["provider"]["stable_release"] != true))
        {
            continue;
        }
        if agent == "opencode" {
            doc["records"][id] = json!({"kind":"cli","agent":agent,"state":"blocked","version":target.version,
                "last_error":"OpenCode native worker replacement requires explicit end/resume; the current worker was retained"});
            save(root, &doc)?;
            continue;
        }
        if candidate == "cli" && save_stamp.is_some() && account.as_deref() != save_stamp {
            continue;
        }
        let status = &observed["status"];
        if status["Turn"] != "idle"
            || status["Interaction"] != "none"
            || status["Attention"] == true
        {
            if manual.is_some() {
                messages.push(format!(
                    "deferred  {label}: mid-turn or awaiting input; rerun when it is quiet"
                ));
            }
            continue;
        }
        if let Err(error) = source.maintenance_preflight(&meta, &target, thread) {
            if manual.is_some() {
                messages.push(format!("hold      {label}: {error}"));
            }
            continue;
        }
        if manual.is_some_and(|(dry, _)| dry) {
            messages.push(format!("would     {label}"));
            continue;
        }
        if candidate == "cli" {
            let confirmed = existing["state"] == "pending"
                && existing["identity"] == target.identity
                && existing["executable"] == json!(target.executable)
                && existing["version"] == target.version;
            if !confirmed {
                doc["records"][id] = json!({"kind":"cli","agent":agent,"state":"pending","version":target.version,
                    "identity":target.identity,"executable":target.executable});
                save(root, &doc)?;
                continue;
            }
        }
        let policy = manual.is_none().then_some(if candidate == "auth" {
            refresh::AUTH
        } else {
            refresh::POLICY
        });
        doc["records"][id] = json!({"kind":candidate,"agent":agent,"state":"claimed","version":target.version,
            "identity":target.identity,"executable":target.executable,"conversation_id":thread,
            "policy":policy,"nonce":runtime::random_id()?,"source_instance":source.incarnation(),
            "account":account,"source_env":meta["maintenance_env"]});
        save(root, &doc)?;
        match advance(state, source, &mut doc) {
            Ok(Some(replacement)) => {
                acted = true;
                messages.push(format!("started   {} -> {replacement}", field(&view, "id")));
            }
            Ok(None) if manual.is_some() => messages.push(format!(
                "deferred  {label}: worker is busy or the native stop is still being verified"
            )),
            Ok(None) => {}
            Err(err) => messages.push(format!("hold      {label}: {err}")),
        }
    }
    if manual.is_some() && !acted {
        messages.push("nothing recycled".into());
    }
    Ok(messages)
}

fn advance(state: &State, source: &Arc<Session>, doc: &mut Value) -> io::Result<Option<String>> {
    let root = root(state)?;
    let id = &source.id;
    let record = doc["records"][id].clone();
    let refuse = |doc: &mut Value, reason: &str| -> io::Result<Option<String>> {
        doc["records"][id]["state"] = json!("blocked");
        doc["records"][id]["last_error"] = json!(reason);
        save(root, doc)?;
        Err(io::Error::other(reason))
    };
    if record["source_instance"] != source.incarnation()
        || !runtime::valid_id(field(&record, "nonce"))
    {
        return refuse(doc, "source incarnation changed");
    }
    let source_id = format!("{}/{}", state.endpoint, id);
    let bound = {
        let table = state.sessions.read().unwrap();
        let candidates: Vec<_> = table
            .values()
            .filter(|row| row.maintenance_lineage(&source_id))
            .cloned()
            .collect();
        if candidates.len() > 1 {
            return refuse(doc, "multiple successors already exist; inspect the roster");
        }
        candidates.into_iter().next()
    };
    if let Some(candidate) = bound {
        if record["replacement_id"].is_string() && record["replacement_id"] != candidate.id {
            return refuse(
                doc,
                "existing successor differs from the durable reservation",
            );
        }
        let actual = candidate.maintenance_identity()?;
        if !matches_target(&record, &actual) {
            return refuse(
                doc,
                "existing successor did not use the pinned target installation",
            );
        }
        if candidate
            .maintenance_observation()
            .ok()
            .is_some_and(|observation| observation["conversation_id"] == record["conversation_id"])
            && candidate.dial().is_ok()
        {
            doc["records"][id]["replacement_id"] = json!(candidate.id);
            doc["records"][id]["state"] = json!("complete");
            save(root, doc)?;
            return Ok(Some(format!("{}/{}", state.endpoint, candidate.id)));
        }
        doc["records"][id]["replacement_id"] = json!(candidate.id);
        doc["records"][id]["state"] = json!("waiting");
        save(root, doc)?;
        return Ok(None);
    }
    if record["replacement_id"].is_string() {
        return refuse(
            doc,
            "a prior replacement reservation is missing; automatic relaunch is refused",
        );
    }
    let meta = source.maintenance_source()?;
    let target = source.maintenance_target(&meta)?;
    if !matches_target(
        &record,
        &json!({"identity":target.identity,"executable":target.executable,"version":target.version}),
    ) {
        return refuse(
            doc,
            "target installation changed after maintenance was claimed",
        );
    }
    if json!(auth_identity(
        field(&record, "agent"),
        &record["source_env"]
    )) != record["account"]
    {
        return refuse(doc, "native identity changed after maintenance was claimed");
    }
    let nonce = field(&record, "nonce");
    if !source.maintenance_stopped(nonce)? {
        if !source.is_live() {
            return Ok(None);
        }
        source.maintenance_preflight(&meta, &target, field(&record, "conversation_id"))?;
        let result = source.status_exchange(&json!({"type":"maintenance_stop","nonce":nonce,
            "conversation_id":record["conversation_id"],"automatic_policy":record["policy"]}))?;
        match field(&result, "type") {
            "busy" => return Ok(None),
            "ok" => {}
            _ => return Err(io::Error::other(field(&result, "error").to_owned())),
        }
        let deadline = Instant::now() + Duration::from_secs(8);
        while !source.maintenance_stopped(nonce)? {
            if Instant::now() >= deadline {
                return Ok(None);
            }
            thread::sleep(Duration::from_millis(100));
        }
    }
    let view = source.view(&state.endpoint);
    let launch = json!({"launch":{
        "agent":record["agent"],"cwd":view["cwd"],"env":record["source_env"],"cols":80,"rows":24,
        "options":{"resume_from":source_id},
    }});
    let replacement = state.reserve_with_recovery(&launch, &[], None, None, Some(nonce))?;
    let actual = replacement.maintenance_identity()?;
    doc["records"][id]["replacement_id"] = json!(replacement.id);
    doc["records"][id]["state"] = json!("waiting");
    save(root, doc)?;
    if !matches_target(&record, &actual) {
        return refuse(
            doc,
            "replacement no longer uses the pinned installation; no launch attempted",
        );
    }
    state.start(&replacement)?;
    if replacement
        .maintenance_observation()
        .ok()
        .is_some_and(|observation| observation["conversation_id"] == record["conversation_id"])
    {
        doc["records"][id]["state"] = json!("complete");
        save(root, doc)?;
    }
    Ok(Some(format!("{}/{}", state.endpoint, replacement.id)))
}

fn matches_target(record: &Value, target: &Value) -> bool {
    ["version", "identity", "executable"]
        .iter()
        .all(|key| record[*key] == target[*key])
}
