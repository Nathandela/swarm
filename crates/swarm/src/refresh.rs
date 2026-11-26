use crate::{flags, ipc, supervisor::runtime};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{fs, io, path::Path, time::Duration};

pub(crate) const POLICY: &str = "cli-refresh.json";
pub(crate) const AUTH: &str = "auth-watch.json";
pub(crate) const RECORDS: &str = "rust-refresh.json";
pub(crate) const CAP: &str = "native-relogin-v1";

pub(crate) fn auth_identity(agent: &str, environment: &Value) -> Option<String> {
    if agent != "codex" {
        return None;
    }
    let entries = environment.as_array()?;
    let get = |name: &str| {
        entries
            .iter()
            .rev()
            .filter_map(Value::as_str)
            .filter_map(|entry| entry.split_once('='))
            .find_map(|(key, value)| (key == name).then_some(value))
    };
    let home = Path::new(get("HOME")?);
    if !home.is_absolute()
        || get("CODEX_HOME").is_some_and(|path| Path::new(path) != home.join(".codex"))
    {
        return None;
    }
    let auth = runtime::read_json(&home.join(".codex/auth.json")).ok()?;
    let mode = ipc::field(&auth, "auth_mode");
    let account = if mode == "apikey" {
        ipc::field(&auth, "OPENAI_API_KEY")
    } else {
        ipc::field(&auth["tokens"], "account_id")
    };
    (!account.is_empty()).then(|| format!("{:x}", Sha256::digest(format!("{mode}\0{account}"))))
}

pub(crate) fn disabled(root: &Path, name: &str) -> io::Result<bool> {
    match runtime::read_json(&root.join(name)) {
        Ok(value) if value.as_object().is_some_and(|fields| fields.len() == 1) => value["disabled"]
            .as_bool()
            .ok_or_else(|| io::Error::other("disabled must be a boolean")),
        Ok(_) => Err(io::Error::other("settings must contain exactly disabled")),
        Err(err) if err.kind() == io::ErrorKind::NotFound => Ok(false),
        Err(err) => Err(err),
    }
}

fn set_disabled(root: &Path, name: &str, is_disabled: bool) -> io::Result<()> {
    if !is_disabled && !root.exists() {
        return Ok(());
    }
    runtime::private_dir(root)?;
    let _policy = runtime::lock(&root.join(".native-maintenance-policy.lock"))?;
    if is_disabled {
        runtime::private_dir(root)?;
        runtime::write_json(&root.join(name), &json!({"disabled":true}))
    } else {
        match fs::remove_file(root.join(name)) {
            Ok(()) => fs::File::open(root)?.sync_all(),
            Err(err) if err.kind() == io::ErrorKind::NotFound => Ok(()),
            Err(err) => Err(err),
        }
    }
}

pub(crate) fn records(root: &Path) -> io::Result<Value> {
    match runtime::read_json(&root.join(RECORDS)) {
        Ok(value)
            if value["version"] == 1
                && value["records"].as_object().is_some_and(|records| {
                    records.iter().all(|(source, entry)| {
                        runtime::valid_id(source)
                            && matches!(ipc::field(entry, "kind"), "cli" | "auth")
                            && matches!(
                                ipc::field(entry, "agent"),
                                "agy" | "claude" | "codex" | "opencode"
                            )
                            && matches!(
                                ipc::field(entry, "state"),
                                "pending" | "claimed" | "waiting" | "blocked" | "complete"
                            )
                            && !ipc::field(entry, "version").is_empty()
                            && match ipc::field(entry, "state") {
                                "claimed" | "waiting" => {
                                    runtime::valid_id(ipc::field(entry, "nonce"))
                                        && entry["identity"].is_object()
                                        && Path::new(ipc::field(entry, "executable")).is_absolute()
                                        && entry["source_env"].is_array()
                                        && matches!(
                                            entry["policy"].as_str(),
                                            None | Some(POLICY | AUTH)
                                        )
                                }
                                "complete" => {
                                    runtime::valid_id(ipc::field(entry, "replacement_id"))
                                }
                                _ => true,
                            }
                    })
                }) =>
        {
            Ok(value)
        }
        Ok(_) => Err(io::Error::other("unrecognized Rust refresh journal")),
        Err(err) if err.kind() == io::ErrorKind::NotFound => Ok(json!({"version":1,"records":{}})),
        Err(err) => Err(err),
    }
}

fn legacy(root: &Path) -> io::Result<Value> {
    match runtime::read_json(&root.join("auth-watch-state.json")) {
        Ok(value)
            if value.is_object()
                && value.as_object().unwrap().keys().all(|key| {
                    matches!(
                        key.as_str(),
                        "identities"
                            | "pending"
                            | "killed"
                            | "candidates"
                            | "retries"
                            | "cli_refresh"
                            | "cli_candidates"
                    )
                })
                && value
                    .as_object()
                    .unwrap()
                    .values()
                    .all(|field| field.is_null() || field.is_object())
                && value["identities"]
                    .as_object()
                    .is_none_or(|items| items.values().all(Value::is_string)) =>
        {
            Ok(value)
        }
        Ok(_) => Err(io::Error::other(
            "unrecognized combined Go recovery journal",
        )),
        Err(err) if err.kind() == io::ErrorKind::NotFound => Ok(Value::Null),
        Err(err) => Err(err),
    }
}

pub(crate) fn legacy_requires_recovery(root: &Path) -> io::Result<bool> {
    let old = legacy(root)?;
    Ok([
        "pending",
        "killed",
        "candidates",
        "retries",
        "cli_candidates",
    ]
    .iter()
    .any(|key| {
        old[key]
            .as_object()
            .is_some_and(|records| !records.is_empty())
    }) || old["cli_refresh"]
        .as_object()
        .is_some_and(|records| records.values().any(|record| record["state"] != "complete")))
}

const REFRESH_USAGE: &str = "Usage of refresh:\n  -auto string\n    \ton|off: enable or disable new automatic CLI refreshes\n  -json\n    \tprint refresh configuration and progress as JSON";
const RELOGIN_USAGE: &str = "Usage of relogin:\n  -auto string\n    \ton|off: enable or disable the daemon's automatic watcher, then exit\n  -dry-run\n    \treport what would be recycled; recycle nothing\n  -force\n    \tassert a re-login happened: also recycle the sessions the watcher cannot judge (unstamped, and same-account re-logins whose stamps still match)";

pub(crate) fn run(verb: &str, args: &[String]) -> u8 {
    let status = verb == "refresh";
    let mut auto = "";
    let (mut as_json, mut dry, mut force) = (false, false, false);
    let (options, booleans): (&[&str], &[&str]) = if status {
        (&["auto", "json"], &["json"])
    } else {
        (&["auto", "dry-run", "force"], &["dry-run", "force"])
    };
    match flags::options(args, options, booleans, |name, value| {
        match name {
            "auto" => auto = value,
            "json" => as_json = flags::parse_bool(name, value)?,
            "dry-run" => dry = flags::parse_bool(name, value)?,
            _ => force = flags::parse_bool(name, value)?,
        }
        Ok(())
    }) {
        Err(error) => {
            if !error.is_empty() {
                eprintln!("{error}");
            }
            eprintln!("{}", if status { REFRESH_USAGE } else { RELOGIN_USAGE });
            return 2;
        }
        Ok(rest) if !rest.is_empty() => {
            eprintln!("{verb}: unexpected argument {:?}", rest[0]);
            return 2;
        }
        Ok(_) => {}
    }
    if !matches!(auto, "" | "on" | "off") {
        eprintln!("{verb}: --auto takes on or off");
        return 2;
    }
    let result = (|| {
        let _installation = if !auto.is_empty() || !status && !dry {
            crate::upgrade::writer(&[verb.into()])?
        } else {
            None
        };
        let root = ipc::state_path()?;
        if !auto.is_empty() {
            set_disabled(&root, if status { POLICY } else { AUTH }, auto == "off")?;
            if !status {
                println!("automatic auth watcher: {auto}");
                return Ok(());
            }
        }
        if status {
            return report(&root, as_json);
        }
        let socket = ipc::socket_path()?;
        let mut client = ipc::Client::dial(&socket, &[CAP])?;
        if !client.supports(CAP) {
            return Err(io::Error::other(
                "running daemon has no native relogin support",
            ));
        }
        let result = client.exchange(
            json!({"op":"native_relogin","state_dir":root.canonicalize()?,"dry_run":dry,"force":force}),
            "native_relogin", Duration::from_secs(115),
        )?;
        if let Some(messages) = result["messages"].as_array() {
            for line in messages.iter().filter_map(Value::as_str) {
                println!("{line}");
            }
        }
        if let Some(error) = result["error"].as_str() {
            return Err(io::Error::other(error.to_owned()));
        }
        Ok(())
    })();
    match result {
        Ok(()) => 0,
        Err(err) => {
            eprintln!("{verb}: {err}");
            1
        }
    }
}

fn report(root: &Path, as_json: bool) -> io::Result<()> {
    let policy = disabled(root, POLICY);
    let recorded = records(root);
    let legacy = legacy_requires_recovery(root);
    let frozen = recorded.is_err() || !matches!(legacy, Ok(false));
    let error = policy
        .as_ref()
        .err()
        .or(recorded.as_ref().err())
        .or(legacy.as_ref().err());
    let mut rows = Vec::new();
    if let Ok(doc) = &recorded {
        for (source, record) in doc["records"].as_object().unwrap() {
            if record["kind"] == "cli" {
                let mut row = json!({
                    "source_id":source, "agent_type":record["agent"],"state":record["state"],"target_version":record["version"],
                });
                for key in ["replacement_id", "last_error"] {
                    if record[key].as_str().is_some_and(|value| !value.is_empty()) {
                        row[key] = record[key].clone();
                    }
                }
                rows.push(row);
            }
        }
    }
    if let Ok(old) = crate::refresh::legacy(root)
        && let Some(prior) = old["cli_refresh"].as_object()
    {
        for (source, record) in prior {
            if rows.iter().any(|row| row["source_id"] == *source)
                || !source
                    .bytes()
                    .all(|byte| byte.is_ascii_lowercase() || byte.is_ascii_digit())
                || !(16..=24).contains(&source.len())
                || !matches!(
                    ipc::field(record, "agent_type"),
                    "agy" | "claude" | "codex" | "opencode"
                )
            {
                continue;
            }
            let version = ipc::field(&record["target"], "version");
            let version = if version.len() <= 32
                && version
                    .bytes()
                    .all(|byte| byte.is_ascii_digit() || byte == b'.')
            {
                version
            } else {
                ""
            };
            let mut row = json!({"source_id":source,"agent_type":record["agent_type"],"target_version":version});
            if record["state"] == "complete" {
                row["state"] = json!("complete");
                let id = ipc::field(record, "replacement_id");
                if (16..=24).contains(&id.len())
                    && id
                        .bytes()
                        .all(|byte| byte.is_ascii_lowercase() || byte.is_ascii_digit())
                {
                    row["replacement_id"] = json!(id);
                }
            } else {
                row["state"] = json!("blocked");
                row["last_error"] = json!("preserved Go evidence requires offline custody review");
            }
            rows.push(row);
        }
    }
    rows.sort_by(|a, b| ipc::field(a, "source_id").cmp(ipc::field(b, "source_id")));
    let off = policy.as_ref().copied().unwrap_or(true);
    if as_json {
        let mut value = json!({"disabled":off,"frozen":frozen,"records":if rows.is_empty(){Value::Null}else{json!(rows)}});
        if let Some(error) = error {
            value["error"] = json!(error.to_string());
        }
        println!("{}", serde_json::to_string_pretty(&value)?);
    } else {
        println!("Automatic CLI refresh: {}", if off { "off" } else { "on" });
        if frozen {
            println!("Refresh state needs attention; automatic actions are held.");
        }
        if rows.is_empty() {
            println!(
                "No recorded refresh requests. Legacy sessions without launch evidence remain untouched."
            );
        } else {
            println!("SOURCE  AGENT  STATE  TARGET  REPLACEMENT  DETAIL");
            for row in rows {
                println!(
                    "{}",
                    [
                        "source_id",
                        "agent_type",
                        "state",
                        "target_version",
                        "replacement_id",
                        "last_error"
                    ]
                    .map(|key| ipc::field(&row, key))
                    .join("  ")
                );
            }
        }
    }
    if let Some(error) = error {
        return Err(io::Error::other(error.to_string()));
    }
    Ok(())
}
