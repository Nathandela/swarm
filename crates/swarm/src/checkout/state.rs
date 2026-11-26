use super::{Report, Session, git};
use crate::{
    ipc::field,
    providers,
    supervisor::{runtime, session},
};
use serde_json::{Value, json};
use std::os::unix::fs::MetadataExt;
use std::{
    collections::BTreeMap,
    fs, io,
    path::{Path, PathBuf},
    time::Instant,
};
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

mod connectors;
mod receipts;
pub(super) use connectors::connectors;

pub(super) fn directory(path: &Path) -> io::Result<()> {
    let info = fs::symlink_metadata(path)?;
    if !info.is_dir() || info.uid() != unsafe { libc::geteuid() } || info.mode() & 0o077 != 0 {
        return Err(io::Error::other("expected a private owner state directory"));
    }
    Ok(())
}

pub(super) fn entries(path: &Path, max: usize, end: Instant) -> io::Result<Vec<fs::DirEntry>> {
    directory(path)?;
    let mut found = Vec::new();
    for entry in fs::read_dir(path)? {
        if found.len() == max || Instant::now() >= end {
            return Err(io::Error::other("state inventory limit reached"));
        }
        found.push(entry?);
    }
    found.sort_by_key(fs::DirEntry::file_name);
    Ok(found)
}

pub(super) fn existing(path: &Path) -> io::Result<bool> {
    match fs::symlink_metadata(path) {
        Ok(_) => Ok(true),
        Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(false),
        Err(e) => Err(e),
    }
}

pub(super) fn qualified(endpoint: &str, id: &str) -> String {
    if id.is_empty() || id.contains('/') {
        id.to_owned()
    } else {
        format!("{endpoint}/{id}")
    }
}

fn valid_go_id(id: &str) -> bool {
    (1..=128).contains(&id.len())
        && !id.starts_with('-')
        && !matches!(id, "." | "..")
        && id
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b"._-".contains(&b))
}

fn absolute(text: &str) -> PathBuf {
    let path = Path::new(text);
    if path.is_absolute() {
        git::canonical(path).unwrap_or_default()
    } else {
        PathBuf::new()
    }
}

fn alive(pid: &Value, birth: &Value) -> Option<bool> {
    let pid = u32::try_from(pid.as_u64()?)
        .ok()
        .filter(|pid| (2..=i32::MAX as u32).contains(pid))?;
    let birth = birth.as_u64().filter(|n| *n > 0)?;
    match runtime::birth(pid) {
        Ok(observed) => Some(observed == birth),
        Err(e) if e.kind() == io::ErrorKind::NotFound || e.raw_os_error() == Some(libc::ESRCH) => {
            Some(false)
        }
        Err(_) => None,
    }
}

fn candidate(agent: &str, id: &str) -> bool {
    if agent == "opencode" {
        providers::opencode::valid_id(id, "ses_")
    } else {
        providers::codex_receipt::canonical_thread(id)
    }
}

pub(super) fn receipt(id: &str, kind: &str, state: &str, endpoint: &str, instance: &str) -> Value {
    let mut out = json!({"id":id,"kind":kind,"state":state});
    if !endpoint.is_empty() {
        out["supervisor"] = json!(endpoint);
    }
    if !instance.is_empty() {
        out["session_instance"] = json!(instance);
    }
    out
}

fn go_row(dir: &Path, id: &str, endpoint: &str, record: &Value) -> io::Result<Session> {
    if record["id"] != id
        || !matches!(record["schema_version"].as_u64(), Some(1 | 3..=5))
        || !record["agent_type"].is_string()
        || !record["status"].is_object()
        || record["schema_version"] == 3
            && (record["agent_type"] != "opencode" || !record["opencode_binding"].is_object())
        || record["schema_version"] == 4 && !record["login_runtime"].is_object()
        || record["schema_version"] == 5
            && (record["agent_type"] != "hermes" || record["hermes_native"] != true)
    {
        return Err(io::Error::other("unqualified Go session record"));
    }
    let source = field(record, "cwd");
    let checked = field(record, "agent_cwd");
    let provider = if checked.is_empty() { source } else { checked };
    let mut row = Session {
        id: qualified(endpoint, id),
        name: field(record, "name").into(),
        agent: field(record, "agent_type").into(),
        launch_path: PathBuf::from(source),
        provider_path: PathBuf::from(provider),
        canonical_launch: absolute(source),
        canonical_provider: absolute(provider),
        state: "unknown".into(),
        resume: "none_recorded".into(),
        hidden: record["roster_hidden"] == true,
        spawned_from: qualified(endpoint, field(record, "spawned_from")),
        resumed_from: qualified(endpoint, field(record, "resumed_from")),
        discussion: record["manager_conversation_id"]
            .as_str()
            .filter(|s| !s.is_empty())
            .unwrap_or_else(|| field(record, "worker_discussion"))
            .into(),
        worker_instance: field(record, "worker_instance").into(),
        worker_attempt: field(record, "worker_attempt").into(),
        resume_claim: field(record, "manager_resume_operation_id").into(),
        isolated: !checked.is_empty()
            && checked != source
            && record["launch_options"]["worktree"] == "true",
        ..Default::default()
    };
    if checked.is_empty() && record["launch_options"]["worktree"] == "true" {
        row.legacy = absolute(
            &Path::new(source)
                .join(".swarm/worktrees")
                .join(id)
                .to_string_lossy(),
        );
        row.unknowns
            .push("legacy worktree association has no recorded provider checkout".into());
    }
    let is_live = alive(&record["shim_pid"], &record["shim_start_time"]);
    let has_identity = record["shim_pid"].as_u64().is_some_and(|pid| pid > 0);
    if record["status"]["NativeWorkUnknown"] == true || record["hermes_native"] == true {
        row.unknowns.push("native work outcome is unknown".into());
    } else {
        match (field(&record["status"], "Process"), is_live) {
            ("running", Some(true)) => row.state = "live".into(),
            ("exited", Some(false)) | ("exited", None)
                if !has_identity || is_live == Some(false) =>
            {
                row.state = "exited".into();
                if candidate(&row.agent, field(record, "conversation_id")) {
                    row.resume = "candidate".into();
                }
            }
            _ => row.unknowns.push(
                "persisted process state and shim identity do not prove current liveness".into(),
            ),
        }
    }
    let retirement = &record["manager_retirement"];
    let operation = field(retirement, "operation_id");
    if !operation.is_empty() {
        row.retirement = operation.into();
        row.manager_instance = field(retirement, "instance").into();
        if row.state == "exited" && field(retirement, "replacement").is_empty() {
            let to_time =
                |data: &Value, key| OffsetDateTime::parse(field(data, key), &Rfc3339).ok();
            let proven = record["role"] == "manager"
                && retirement["shim_pid"] == record["shim_pid"]
                && retirement["shim_start_time"] == record["shim_start_time"]
                && is_live == Some(false)
                && retirement["native_session_id"] == record["conversation_id"]
                && runtime::read_json(&dir.join("exit.json"))
                    .ok()
                    .and_then(|exit| {
                        to_time(&exit, "finished_at").zip(to_time(retirement, "claimed_at"))
                    })
                    .is_some_and(|(exit, claimed)| claimed.year() > 1 && exit >= claimed);
            if proven {
                row.state = "parked_resume_source".into();
                row.resume = "parked_candidate".into();
            } else {
                row.resume = "unresolved".into();
                row.unknowns
                    .push("manager retirement has no verified exit".into());
            }
        }
        if row.state != "parked_resume_source" {
            row.unresolved_receipts.push(receipt(
                operation,
                "manager_retirement",
                "requires_reconciliation",
                endpoint,
                "",
            ));
        }
    }
    if !field(record, "resume_error").is_empty() || !field(record, "input_embargo").is_empty() {
        row.resume = "unresolved".into();
        row.unknowns
            .push("a resume or input recovery issue remains".into());
    }
    if existing(&dir.join("direct-input-unresolved.json"))? {
        row.unresolved_receipts
            .push(receipt("", "direct_input", "unresolved", endpoint, ""));
    }
    Ok(row)
}

fn rust_row(dir: &Path, id: &str, endpoint: &str, record: &Value) -> io::Result<Option<Session>> {
    let token = field(record, "token");
    if record["id"] != id
        || !matches!(record["schema"].as_u64(), Some(1 | 2))
        || !runtime::valid_id(token)
        || !record["provider"]["name"].is_string()
    {
        return Err(io::Error::other("unqualified Rust session record"));
    }
    let mut export_unresolved = false;
    if existing(&dir.join("go-rollback-intent.json"))?
        || existing(&dir.join("go-rollback-published.json"))?
    {
        let marker =
            json!({"schema":1,"id":id,"reservation":providers::codex_receipt::hash(token)});
        let published = dir.join("go-rollback-published.json");
        if existing(&published)? {
            if runtime::read_json(&published)? != marker {
                return Err(io::Error::other("unverified Go rollback publication"));
            }
            return Ok(None);
        }
        let legacy = dir
            .parent()
            .and_then(Path::parent)
            .expect("supervisor root")
            .join(id);
        if existing(&legacy)? {
            directory(&legacy)?;
            if runtime::read_json(&legacy.join("rust-origin.json"))? != marker {
                return Err(io::Error::other("conflicting Go rollback destination"));
            }
            return Ok(None);
        }
        export_unresolved = true;
    }
    let source = field(record, "cwd");
    let provider = record["checkout"]["path"].as_str().unwrap_or(source);
    let agent = field(&record["provider"], "name");
    let owner = runtime::read_json(&dir.join("owner.json"));
    let live = owner
        .as_ref()
        .ok()
        .filter(|o| o["token"] == token)
        .and_then(|o| alive(&o["pid"], &o["birth"]))
        == Some(true);
    let closed = session::owner_gone(dir, token).is_ok_and(|gone| gone)
        && session::exit_report(dir, token).is_ok();
    let mut row = Session {
        id: qualified(endpoint, id),
        name: field(record, "name").into(),
        agent: agent.into(),
        launch_path: PathBuf::from(source),
        provider_path: PathBuf::from(provider),
        canonical_launch: absolute(source),
        canonical_provider: absolute(provider),
        state: if record["provider"]["mode"] == "shared-native" {
            "unknown"
        } else if live {
            "live"
        } else if closed {
            "exited"
        } else {
            "unknown"
        }
        .into(),
        resume: "none_recorded".into(),
        hidden: record["roster_hidden"] == true,
        spawned_from: qualified(endpoint, field(&record["lineage"], "spawned_from")),
        resumed_from: ["agy_launch", "opencode_resume", "claude_resume"]
            .into_iter()
            .find_map(|key| record[key]["resumed_from"].as_str())
            .map(|id| qualified(endpoint, id))
            .unwrap_or_default(),
        discussion: record["manager"]["launch"]["manager"]["conversation_id"]
            .as_str()
            .unwrap_or_else(|| field(&record["mailbox"], "discussion"))
            .into(),
        worker_instance: if record["mailbox"].is_object() {
            providers::codex_receipt::instance(token)
        } else {
            String::new()
        },
        worker_attempt: field(&record["mailbox"], "attempt").into(),
        isolated: record["checkout"].is_object(),
        ..Default::default()
    };
    if row.state == "unknown" {
        row.unknowns
            .push("persisted process state and shim identity do not prove current liveness".into());
    }
    if row.state == "exited"
        && let Ok(observation) = runtime::read_json(&dir.join("observation.json"))
    {
        if observation["token"] == token && candidate(agent, field(&observation, "conversation_id"))
        {
            row.resume = "candidate".into();
        } else if observation["token"] != token {
            row.unknowns
                .push("provider conversation observation is unverified".into());
        }
    }
    if export_unresolved {
        row.unknowns
            .push("Go rollback export publication is unverified".into());
    }
    if agent == "opencode"
        && row.state == "exited"
        && let Ok(identity) = providers::opencode::receipts::native(dir, token)
        && candidate(agent, field(&identity, "rootID"))
    {
        row.resume = "candidate".into();
    }
    let retired = dir.join("manager-retire.json");
    if existing(&retired)? {
        let claim = runtime::read_json(&retired)?;
        let request = &claim["request"];
        let binding = &request["binding"];
        let operation = crate::supervisor::manager::retirement_id(request).unwrap_or_default();
        row.retirement = operation.clone();
        row.manager_instance = providers::codex_receipt::instance(token);
        let proven = claim["token"] == token
            && request.is_object()
            && !operation.is_empty()
            && binding["conversation_id"] == row.discussion
            && binding["session"] == row.id
            && binding["session_instance"] == providers::codex_receipt::instance(token)
            && request["replacement"].is_null();
        if proven && row.state == "exited" {
            row.state = "parked_resume_source".into();
            row.resume = "parked_candidate".into();
        } else {
            row.resume = "unresolved".into();
            row.unresolved_receipts.push(receipt(
                &operation,
                "manager_retirement",
                "requires_reconciliation",
                endpoint,
                "",
            ));
            if !proven {
                row.unknowns.push("manager retirement is unverified".into());
            }
        }
    }
    if existing(&dir.join("direct-input-unresolved.json"))? {
        row.unresolved_receipts
            .push(receipt("", "direct_input", "unresolved", endpoint, ""));
    }
    Ok(Some(row))
}

pub(super) fn inspect(
    root: &Path,
    endpoint: &str,
    rows: &mut BTreeMap<String, Session>,
    report: &mut Report,
    additional: &mut Vec<PathBuf>,
    end: Instant,
) {
    let mut collect = |area: &Path, native: bool| {
        let list = match entries(area, 10_000, end) {
            Ok(list) => list,
            Err(e) => {
                if !native || e.kind() != io::ErrorKind::NotFound {
                    report.unknowns.push(format!("supervisor {root:?}: {e}"));
                }
                return;
            }
        };
        for entry in list {
            let inspect = || -> io::Result<Option<Session>> {
                let file = entry.file_name();
                let Some(id) = file.to_str().filter(|id| {
                    if native {
                        runtime::valid_id(id)
                    } else {
                        valid_go_id(id)
                    }
                }) else {
                    return Ok(None);
                };
                let kind = entry.file_type()?;
                if kind.is_symlink() {
                    return Err(io::Error::other(format!("state entry {id:?} is a symlink")));
                }
                if !kind.is_dir() {
                    return Ok(None);
                }
                let dir = entry.path();
                let path = dir.join(if native { "session.json" } else { "meta.json" });
                if !existing(&path)? {
                    if native
                        || ["shim-launch.json", "shim.sock", "exit.json"]
                            .iter()
                            .any(|p| existing(&dir.join(p)).unwrap_or(true))
                    {
                        return Err(io::Error::other(format!(
                            "session evidence without readable metadata in {id:?}"
                        )));
                    }
                    return Ok(None);
                }
                directory(&dir)?;
                let record = runtime::read_json(&path)?;
                if native {
                    rust_row(&dir, id, endpoint, &record)
                } else {
                    go_row(&dir, id, endpoint, &record).map(Some)
                }
            };
            match inspect() {
                Ok(Some(row)) => {
                    if row.canonical_launch.as_os_str().is_empty()
                        || row.canonical_provider.as_os_str().is_empty()
                    {
                        report.unknowns.push(format!(
                            "session {:?} has an unresolved checkout path",
                            row.id
                        ));
                    }
                    if rows.contains_key(&row.id) {
                        report
                            .unknowns
                            .push(format!("duplicate session custody for {:?}", row.id));
                    } else {
                        rows.insert(row.id.clone(), row);
                    }
                }
                Ok(None) => {}
                Err(e) => report
                    .unknowns
                    .push(format!("supervisor {root:?}: {:?}: {e}", entry.file_name())),
            }
        }
    };
    collect(root, false);
    collect(&root.join("rust-v1"), true);
    receipts::inspect(root, endpoint, rows, report, additional, end);
}
