use super::{absolute, directory, entries, existing, receipt, receipts::bolt};
use crate::{
    discord,
    ipc::field,
    supervisor::{endpoint, runtime},
};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{
    collections::{BTreeMap, BTreeSet},
    env, io,
    path::{Path, PathBuf},
    time::{Duration, Instant},
};

fn values(v: &Value) -> impl Iterator<Item = &Value> {
    v.as_array().into_iter().flatten()
}
fn selector(dir: &Path) -> io::Result<Option<PathBuf>> {
    let path = dir.join("discord-active.json");
    if !existing(&path)? {
        return Ok(None);
    }
    directory(dir)?;
    let value = runtime::read_json(&path)?;
    let chosen = PathBuf::from(
        value
            .as_str()
            .ok_or_else(|| io::Error::other("invalid active integration"))?,
    );
    if !chosen.is_absolute() {
        return Err(io::Error::other("invalid active integration"));
    }
    Ok(Some(chosen))
}

pub(in crate::checkout) fn connectors(
    roots: &mut Vec<PathBuf>,
    mut dirs: Vec<PathBuf>,
    report: &mut crate::checkout::Report,
    end: Instant,
) -> Vec<PathBuf> {
    let mut paths = Vec::new();
    let root = roots[0].clone();
    let home = env::var_os("HOME").map(PathBuf::from);
    let config_home = if cfg!(target_os = "macos") {
        home.map(|h| h.join("Library/Application Support"))
    } else {
        env::var_os("XDG_CONFIG_HOME")
            .map(PathBuf::from)
            .or_else(|| home.map(|h| h.join(".config")))
    };
    if let Some(config_home) = config_home {
        match selector(&config_home.join("swarm/installed-discord")) {
            Ok(Some(dir)) => dirs.push(dir),
            Ok(None) => {}
            Err(e) => report
                .unknowns
                .push(format!("installed Discord selection is unreadable: {e}")),
        }
    } else {
        report
            .unknowns
            .push("installed Discord selection cannot be resolved: HOME is unavailable".into());
    }
    match selector(&root) {
        Ok(Some(dir)) => dirs.push(dir),
        Ok(None) => match existing(&root.join("discord/config.json")) {
            Ok(true) => dirs.push(root.join("discord")),
            Ok(false) => {}
            Err(e) => report
                .unknowns
                .push(format!("default Discord configuration is unreadable: {e}")),
        },
        Err(e) => report
            .unknowns
            .push(format!("active Discord selection is unreadable: {e}")),
    }
    let mut seen = BTreeSet::new();
    for dir in dirs {
        if Instant::now() >= end {
            report
                .unknowns
                .push("Discord scope discovery timed out".into());
            break;
        }
        let dir = dir.canonicalize().unwrap_or(dir);
        if !seen.insert(dir.clone()) {
            continue;
        }
        let cfg = match directory(&dir).and_then(|()| runtime::read_json(&dir.join("config.json")))
        {
            Ok(cfg) if cfg["version"] == 1 && !field(&cfg, "installation_id").is_empty() => cfg,
            other => {
                report.unknowns.push(format!(
                    "Discord configuration at {dir:?} is unreadable: {}",
                    other
                        .err()
                        .map_or("unknown schema or installation".into(), |e| e.to_string())
                ));
                continue;
            }
        };
        if !matches!(field(&cfg, "mode"), "teammate" | "personal") {
            report.unknowns.push(format!("Discord at {dir:?} uses a legacy connector; see swarm discord status for its receipts"));
            continue;
        }
        let mut endpoints = BTreeMap::new();
        let socket = field(&cfg, "daemon_socket");
        let bound = absolute(field(&cfg["supervisor"], "state_dir"));
        if !bound.as_os_str().is_empty() {
            if Path::new(socket).is_absolute() {
                endpoints.insert(socket.to_owned(), endpoint(&bound));
                roots.push(bound);
            }
        } else if crate::ipc::socket_path().is_ok_and(|p| p == Path::new(socket)) {
            endpoints.insert(
                socket.to_owned(),
                endpoint(&root.canonicalize().unwrap_or_else(|_| root.clone())),
            );
        }
        if let Some(name) = endpoints.get(socket).cloned() {
            endpoints.insert(String::new(), name);
        } else {
            report.unknowns.push(format!(
                "Discord at {dir:?} has no recorded state root for its supervisor socket {socket:?}"
            ));
        }
        if !cfg["previous_supervisor"].is_null() {
            let previous = &cfg["previous_supervisor"];
            let legacy = absolute(field(previous, "state_dir"));
            let prior_socket = field(previous, "socket");
            let prior_endpoint = field(previous, "endpoint");
            if !legacy.as_os_str().is_empty()
                && !prior_socket.is_empty()
                && endpoint(&legacy) == prior_endpoint
            {
                roots.push(legacy);
                endpoints.insert(prior_socket.into(), prior_endpoint.into());
                endpoints.insert(String::new(), prior_endpoint.into());
            } else {
                report.unknowns.push(format!(
                    "Discord at {dir:?} has an unverified previous supervisor"
                ));
            }
        }
        let native = dir.join("rust-text-v1.json");
        if existing(&native).unwrap_or(true) {
            match rust_discussion(&native, &cfg) {
                Ok(Some(row)) => report.discussions.push(row),
                Ok(None) => {}
                Err(e) => report
                    .unknowns
                    .push(format!("Discord receipts at {dir:?} unavailable: {e}")),
            }
        } else {
            inspect_go(&dir, &cfg, &endpoints, report, end);
        }
    }
    report
        .discussions
        .sort_by(|a, b| field(a, "id").cmp(field(b, "id")));
    for row in &report.discussions {
        for p in std::iter::once(field(row, "project"))
            .chain(values(&row["worker_projects"]).filter_map(Value::as_str))
        {
            let path = absolute(p);
            if !path.as_os_str().is_empty() {
                paths.push(path);
            }
        }
    }
    paths
}

fn add(row: &mut Value, key: &str, value: Value) {
    if row[key].is_null() {
        row[key] = json!([]);
    }
    row[key].as_array_mut().expect("checkout array").push(value);
}
fn optional(row: &mut Value, key: &str, value: &str) {
    if !value.is_empty() {
        row[key] = json!(value);
    }
}
fn description(id: &str, native: &Value) -> Value {
    let state = field(native, "state");
    let mut row = json!({"id":id,"state":if state.is_empty(){"unknown"}else{state}});
    for key in ["project", "manager_session", "manager_instance"] {
        optional(&mut row, key, field(native, key));
    }
    if row["state"] == "unknown" {
        add(
            &mut row,
            "unknowns",
            json!("native discussion status is unavailable"),
        );
    }
    for job in values(&native["jobs"]) {
        for (field_name, key) in [
            ("session", "worker_sessions"),
            ("project", "worker_projects"),
        ] {
            if let Some(value) = job[field_name].as_str().filter(|s| !s.is_empty()) {
                add(&mut row, key, json!(value));
            }
        }
        let state = field(job, "state");
        if job["recovery_required"] == true
            || matches!(
                state,
                "pending_launch" | "exited_unconfirmed" | "evidence_limited"
            )
        {
            add(
                &mut row,
                "unresolved_receipts",
                receipt(
                    field(job, "attempt_id"),
                    "worker_job",
                    state,
                    "",
                    field(job, "instance"),
                ),
            );
        }
    }
    let retirement = field(native, "retirement_operation");
    if !retirement.is_empty() && row["state"] != "parked" {
        add(
            &mut row,
            "unresolved_receipts",
            receipt(retirement, "manager_retirement", state, "", ""),
        );
    }
    for note in values(&native["worker_notes"]) {
        let state = field(note, "state");
        if !matches!(state, "settled" | "complete" | "rejected") {
            add(
                &mut row,
                "unresolved_receipts",
                receipt(
                    field(note, "id"),
                    "worker_note",
                    state,
                    "",
                    field(note, "instance"),
                ),
            );
        }
    }
    for (key, kind) in [
        ("pending_admissions", "native_admissions"),
        ("pending_actions", "native_actions"),
        ("pending_replies", "pending_replies"),
        ("unconfirmed_replies", "unconfirmed_replies"),
    ] {
        if let Some(n) = native[key].as_u64().filter(|n| *n > 0) {
            add(
                &mut row,
                "unresolved_receipts",
                receipt("", kind, &format!("{n} unresolved"), "", ""),
            );
        }
    }
    row
}

fn rust_discussion(path: &Path, cfg: &Value) -> io::Result<Option<Value>> {
    let data = runtime::read_json(path)?;
    let identity = json!([
        cfg["installation_id"],
        cfg["application_id"],
        cfg["owner_id"],
        cfg["dm_channel_id"],
        cfg["daemon_socket"]
    ]);
    if !matches!(data["version"].as_u64(), Some(2 | 3))
        || data["identity"] != identity
        || !data["messages"].is_object()
        || !data["outbox"].is_object()
    {
        return Err(io::Error::other(
            "unverified native Discord receipt schema or identity",
        ));
    }
    let manager = &data["manager"];
    let binding = &manager["binding"];
    let launch = &manager["launch"];
    let id = binding["conversation_id"]
        .as_str()
        .unwrap_or_else(|| field(&launch["manager"], "conversation_id"));
    let state = if binding.is_object() {
        if manager["retirement"].is_object() {
            "parking"
        } else {
            "active"
        }
    } else if manager.is_object() {
        "starting"
    } else {
        "unknown"
    };
    let mut row =
        json!({"id":if id.is_empty(){field(cfg,"installation_id")}else{id},"state":state});
    optional(&mut row, "project", field(launch, "cwd"));
    optional(&mut row, "manager_session", field(binding, "session"));
    optional(
        &mut row,
        "manager_instance",
        field(binding, "session_instance"),
    );
    optional(&mut row, "manager_session", field(manager, "session"));
    for (msg, item) in data["messages"].as_object().unwrap() {
        let state = field(item, "state");
        if matches!(state, "prepared" | "sending" | "uncertain" | "submitted") {
            add(
                &mut row,
                "unresolved_receipts",
                receipt(
                    item["operation_id"].as_str().unwrap_or(msg),
                    "discord_ingress",
                    state,
                    "",
                    "",
                ),
            );
        }
    }
    for (id, item) in data["outbox"].as_object().unwrap() {
        let state = field(item, "state");
        if state != "sent" {
            add(
                &mut row,
                "unresolved_receipts",
                receipt(
                    id,
                    "discord_publication",
                    if state == "prepared" {
                        "pending"
                    } else {
                        "unconfirmed"
                    },
                    "",
                    "",
                ),
            );
        }
    }
    if manager.is_object()
        && field(manager, "session").is_empty()
        && !field(manager, "launch_operation").is_empty()
    {
        add(
            &mut row,
            "unresolved_receipts",
            receipt(
                field(manager, "launch_operation"),
                "owner_launch",
                "pending_launch",
                "",
                "",
            ),
        );
    }
    let retire = field(&manager["retirement"], "operation_id");
    if !retire.is_empty() {
        add(
            &mut row,
            "unresolved_receipts",
            receipt(retire, "manager_retirement", state, "", ""),
        );
    }
    if manager.is_null() && row["unresolved_receipts"].is_null() {
        return Ok(None);
    }
    if id.is_empty() {
        add(
            &mut row,
            "unknowns",
            json!("native discussion binding is unverified"),
        );
    }
    Ok(Some(row))
}

fn inspect_go(
    dir: &Path,
    cfg: &Value,
    endpoints: &BTreeMap<String, String>,
    report: &mut crate::checkout::Report,
    end: Instant,
) {
    let installation = field(cfg, "installation_id");
    let response = discord::checkout_status(dir, end.min(Instant::now() + Duration::from_secs(1)))
        .or_else(|_| offline(dir, installation, end));
    let status = match response {
        Ok(value) => value,
        Err(e) => {
            report.unknowns.push(format!("Discord receipts at {dir:?} unavailable: {e} (inspect the connector on its owning VM)"));
            Value::Null
        }
    };
    if !field(&status, "ingress_issue").is_empty()
        || !field(&status, "worker_mail_issue").is_empty()
    {
        report.unknowns.push(format!(
            "Discord at {dir:?} reports unresolved ingress or mailbox work"
        ));
    }
    if let Some(n) = status["pending_notices"].as_u64().filter(|n| *n > 0) {
        report.unknowns.push(format!(
            "Discord at {dir:?} has {n} connector notices without a linked checkout"
        ));
    }
    let mut rows = Vec::new();
    let mut disk_linked = BTreeMap::new();
    let mut shown = 0;
    for item in values(&status["discussions"]) {
        let id = field(item, "id");
        let proof = &item["native"];
        let mut row = description(
            id,
            if field(proof, "checkout_evidence").is_empty() {
                &Value::Null
            } else {
                proof
            },
        );
        if let Some(endpoint) = endpoints.get(field(item, "supervisor_socket")) {
            let hashed = ledger_id(
                &dir.join("discussions"),
                endpoint,
                installation,
                &item["thread"],
                id,
            );
            disk_linked.insert(hashed, rows.len());
        } else {
            add(
                &mut row,
                "unknowns",
                json!("Discord discussion owning supervisor is unverified"),
            );
        }
        for input in values(&item["pending"]) {
            shown += 1;
            add(
                &mut row,
                "unresolved_receipts",
                receipt(
                    input["operation_id"]
                        .as_str()
                        .filter(|s| !s.is_empty())
                        .unwrap_or_else(|| field(input, "message_id")),
                    "discord_ingress",
                    field(input, "state"),
                    "",
                    "",
                ),
            );
        }
        for output in values(&item["outbox"]) {
            add(
                &mut row,
                "unresolved_receipts",
                receipt(
                    field(output, "id"),
                    "discord_publication",
                    if field(output, "error").is_empty() {
                        "pending"
                    } else {
                        "unconfirmed"
                    },
                    "",
                    "",
                ),
            );
        }
        rows.push(row);
    }
    if let Some(n) = status["pending"]
        .as_u64()
        .and_then(|n| n.checked_sub(shown))
        .filter(|n| *n > 0)
    {
        report.unknowns.push(format!(
            "Discord at {dir:?} has {n} queued messages without an inspected checkout binding"
        ));
    }
    let root = dir.join("discussions");
    match entries(&root, 256, end) {
        Ok(list) => {
            for entry in list {
                let name = entry.file_name();
                let Some(id) = name
                    .to_str()
                    .filter(|s| s.len() == 64 && s.bytes().all(|b| b.is_ascii_hexdigit()))
                else {
                    continue;
                };
                let linked = disk_linked.get(id).copied();
                if linked
                    .is_some_and(|i| rows[i]["state"] != "unknown" && rows[i]["unknowns"].is_null())
                {
                    continue;
                }
                match entry.file_type() {
                    Ok(kind) if kind.is_symlink() => {
                        report
                            .unknowns
                            .push(format!("Discord discussion {id:?} is a symlink"));
                        continue;
                    }
                    Ok(kind) if kind.is_dir() => {}
                    _ => continue,
                }
                match native_ledger(&entry.path(), id, end) {
                    Ok(proof) => {
                        let public = if let Some(i) = linked {
                            field(&rows[i], "id").to_owned()
                        } else {
                            format!("discord-teammate-{id}")
                        };
                        let mut view = description(&public, &proof);
                        if let Some(i) = linked {
                            for item in values(&rows[i]["unresolved_receipts"]) {
                                add(&mut view, "unresolved_receipts", item.clone());
                            }
                            rows[i] = view;
                        } else {
                            rows.push(view);
                        }
                    }
                    Err(err) if err.kind() == io::ErrorKind::NotFound && linked.is_none() => {}
                    Err(err) => {
                        if let Some(i) = linked {
                            add(
                                &mut rows[i],
                                "unknowns",
                                json!("native discussion ledger is unavailable"),
                            );
                        } else {
                            report
                                .unknowns
                                .push(format!("Discord discussion {id:?} is unavailable: {err}"));
                        }
                    }
                }
            }
        }
        Err(e) if e.kind() == io::ErrorKind::NotFound => {}
        Err(e) => report
            .unknowns
            .push(format!("Discord discussions at {root:?} unavailable: {e}")),
    }
    report.discussions.extend(rows);
}

fn ledger_id(dir: &Path, endpoint: &str, installation: &str, thread: &Value, id: &str) -> String {
    let key = [
        dir.to_str().unwrap_or_default(),
        endpoint,
        installation,
        field(thread, "guild_id"),
        field(thread, "channel_id"),
        id,
    ]
    .join("\0");
    format!("{:x}", Sha256::digest(key.as_bytes()))
}

fn offline(dir: &Path, installation: &str, end: Instant) -> io::Result<Value> {
    let prefix = format!("{installation}\0");
    bolt(&dir.join("discord.db"), 256 << 20, end, |bucket| {
        let bindings: BTreeMap<_, _> = bucket(&[b"teammate_discussions"])?.into_iter().collect();
        let ingress: BTreeMap<_, _> = bucket(&[b"teammate_ingress"])?.into_iter().collect();
        let mut pending = Vec::new();
        for (_, key) in bucket(&[b"teammate_pending"])?
            .into_iter()
            .filter(|(key, _)| key.starts_with(prefix.as_bytes()))
        {
            let row: Value = serde_json::from_slice(
                ingress
                    .get(&key)
                    .ok_or_else(|| io::Error::other("missing Discord ingress receipt"))?,
            )?;
            pending.push(row);
        }
        let outbox: Vec<Value> = bucket(&[b"teammate_outbox"])?
            .into_iter()
            .filter(|(key, _)| key.starts_with(prefix.as_bytes()))
            .map(|(_, data)| serde_json::from_slice(&data))
            .collect::<Result<_, _>>()?;
        let mut rows = Vec::new();
        for (key, _) in bucket(&[b"teammate_active_discussions"])?
            .into_iter()
            .filter(|(key, _)| key.starts_with(prefix.as_bytes()))
        {
            let item: Value = serde_json::from_slice(
                bindings
                    .get(&key)
                    .ok_or_else(|| io::Error::other("missing Discord binding"))?,
            )?;
            if item["InstallationID"] != installation || !item["ID"].is_string() {
                return Err(io::Error::other("unverified Discord binding"));
            }
            let route = &item["Route"];
            let receipts:Vec<_>=pending.iter().filter(|p|p["Route"]["channel_id"]==route["channel_id"]||p["Admission"]["Discussion"]["Route"]["channel_id"]==route["channel_id"])
                .take(16).map(|p|json!({"operation_id":p["Admission"]["OperationID"],"message_id":p["Route"]["message_id"],"state":p["State"]})).collect();
            let published: Vec<_> = outbox
                .iter()
                .filter(|p| p["DiscussionID"] == item["ID"])
                .map(|p| json!({"id":p["Publication"]["ID"],"error":p["Error"]}))
                .collect();
            rows.push(json!({"id": item["ID"],"thread":route,"supervisor_socket":item["supervisor_socket"],"pending":receipts,"outbox":published}));
        }
        Ok(json!({"pending":pending.len(),"discussions":rows}))
    })
}

fn native_ledger(dir: &Path, id: &str, end: Instant) -> io::Result<Value> {
    directory(dir)?;
    if !existing(&dir.join("voice.initialized"))? {
        return Err(io::ErrorKind::NotFound.into());
    }
    let conversation = format!("discord-teammate-{id}");
    bolt(&dir.join("voice.db"), 256 << 20, end, |bucket| {
        let header: BTreeMap<_, _> = bucket(&[b"controller"])?.into_iter().collect();
        let raw = header
            .get(b"state".as_slice())
            .ok_or_else(|| io::Error::other("missing native discussion header"))?;
        if header.get(b"digest".as_slice()).map(Vec::as_slice)
            != Some(format!("{:x}", Sha256::digest(raw)).as_bytes())
        {
            return Err(io::Error::other("unverified native discussion header"));
        }
        let header: Value = serde_json::from_slice(raw)?;
        let records = bucket(&[b"controller", b"records"])?;
        if !matches!(header["version"].as_u64(), Some(3 | 4))
            || header["current_conversation"] != conversation
            || header["records"] != records.len() as u64
        {
            return Err(io::Error::other("unqualified native discussion header"));
        }
        let mut sum = [0u8; 32];
        let mut selected = None;
        let mut jobs = Vec::new();
        let (mut admissions, mut actions, mut replies, mut unconfirmed) = (0, 0, 0, 0);
        for (key, raw) in records {
            let mut hash = Sha256::new();
            hash.update(&key);
            hash.update([0]);
            hash.update(&raw);
            for (byte, digest) in sum.iter_mut().zip(hash.finalize()) {
                *byte ^= digest;
            }
            let key = std::str::from_utf8(&key).map_err(io::Error::other)?;
            let (table, record) = key
                .split_once('\0')
                .ok_or_else(|| io::Error::other("invalid native discussion record"))?;
            if !matches!(
                table,
                "conversations" | "workers" | "admissions" | "actions"
            ) {
                continue;
            }
            let value: Value = serde_json::from_slice(&raw)?;
            if table == "conversations" && record == conversation {
                selected = Some(value);
                continue;
            }
            if value["conversation_id"] != conversation {
                continue;
            }
            match table {
                "workers" => {
                    let state = if value["reserved"] == true {
                        "pending_launch"
                    } else if value["evidence_limited"] == true {
                        "evidence_limited"
                    } else if value["recovery_required"] == true {
                        "uncertain"
                    } else if value["ended"] == true {
                        "exited_unconfirmed"
                    } else {
                        "tracked"
                    };
                    jobs.push(json!({"session":value["session"],"project":value["project"],"instance":value["session_instance"],"attempt_id":if value["reserved"]==true{&value["action_id"]}else{&value["attempt_id"]},"state":state,"recovery_required":value["recovery_required"]}));
                }
                "admissions" => {
                    if value["settled_at"]
                        .as_str()
                        .is_none_or(|stamp| stamp.starts_with("0001-01-01"))
                    {
                        admissions += 1;
                    }
                    if value["reply_delivery_unknown"] == true {
                        unconfirmed += 1;
                    }
                    if value["reply_check_after"].as_u64().is_some_and(|n| n > 0) {
                        replies += 1;
                    }
                }
                "actions" if value["state"] != "settled" => actions += 1,
                _ => {}
            }
        }
        if header["record_hash"] != json!(sum) {
            return Err(io::Error::other("unverified native discussion records"));
        }
        let row = selected
            .filter(|row| row["binding"]["conversation_id"] == conversation)
            .ok_or_else(|| io::Error::other("unverified native discussion binding"))?;
        let phase = if row["parked"] == true {
            "parked"
        } else if row["retirement"].is_object() {
            "parking"
        } else if row["custody_lost"] == true {
            "blocked"
        } else {
            "active"
        };
        Ok(
            json!({"state":phase,"manager_session":row["binding"]["session_id"],"manager_instance":row["binding"]["session_instance"],"project":row["project"],"jobs":jobs,"pending_admissions":admissions,"pending_actions":actions,"pending_replies":replies,"unconfirmed_replies":unconfirmed}),
        )
    })
}
