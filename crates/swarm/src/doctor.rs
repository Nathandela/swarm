//! Lifecycle diagnostics are read-only, including when a machine has never run Swarm.
mod storage;

use crate::{
    environment,
    ipc::{self, Client, field},
};
use serde_json::{Value, json};
use std::os::unix::fs::{MetadataExt, OpenOptionsExt};
use std::path::{Path, PathBuf};
use std::{env, fs, io};
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

struct Finding {
    check: String,
    status: &'static str,
    detail: String,
    fix: &'static str,
}
fn finding(
    check: impl Into<String>,
    status: &'static str,
    detail: impl Into<String>,
    fix: &'static str,
) -> Finding {
    Finding {
        check: check.into(),
        status,
        detail: detail.into(),
        fix,
    }
}
fn daemon(root: &Path, sock: &Path) -> Finding {
    if let Err(e) = sock.metadata() {
        return if e.kind() == io::ErrorKind::NotFound {
            finding(
                "daemon",
                "ok",
                "no daemon running; the next swarm command starts one from its own shell",
                "",
            )
        } else {
            finding(
                "daemon",
                "warn",
                format!("cannot read the socket path: {e}"),
                "",
            )
        };
    }
    let version = option_env!("SWARM_BUILD_VERSION").unwrap_or("dev");
    match Client::dial(sock, &[]) {
        Ok(c) if c.build_version() == version => finding(
            "daemon",
            "ok",
            format!("running {version}, matching this binary"),
            "",
        ),
        Ok(c) => finding(
            "daemon",
            "warn",
            format!(
                "the daemon runs {} but this binary is {version}",
                c.build_version()
            ),
            "swarm daemon restart (adopts live sessions)",
        ),
        Err(e) => {
            let lock =
                ipc::env_path("SWARM_DAEMON_LOCK").unwrap_or_else(|| root.join("daemon.lock"));
            let free = match fs::OpenOptions::new()
                .read(true)
                .custom_flags(libc::O_NOFOLLOW | libc::O_NONBLOCK)
                .open(lock)
            {
                Ok(f) => f.metadata().is_ok_and(|m| m.is_file()) && f.try_lock().is_ok(),
                Err(e) => e.kind() == io::ErrorKind::NotFound,
            };
            if free {
                finding(
                    "daemon",
                    "ok",
                    "a stale socket from an exited daemon; the next swarm command replaces it",
                    "",
                )
            } else {
                finding(
                    "daemon",
                    "warn",
                    format!("a daemon holds the lock but did not answer the hello: {e}"),
                    "swarm daemon restart",
                )
            }
        }
    }
}
pub(crate) fn resolve(path: &str, name: &str) -> Option<PathBuf> {
    env::split_paths(path)
        .filter(|p| p.is_absolute())
        .map(|p| p.join(name))
        .find(|p| {
            p.metadata()
                .is_ok_and(|m| m.is_file() && m.mode() & 0o111 != 0)
        })
}
fn saved(root: &Path, rows: &mut Vec<Finding>) {
    let values = match environment::load_saved(root) {
        Ok(v) if !v.is_empty() => v,
        Ok(_) => {
            rows.push(finding("daemon.env","fail","the saved daemon environment is empty: nothing safe to spawn from, no PATH for non-terminal launches","swarm daemon restart (from a terminal)"));
            return;
        }
        Err(e) if e.kind() == io::ErrorKind::NotFound => {
            rows.push(finding(
                "daemon.env",
                "warn",
                "no saved daemon environment; unattended restarts refuse",
                "swarm daemon restart (from a terminal)",
            ));
            return;
        }
        Err(e) => {
            rows.push(finding(
                "daemon.env",
                "fail",
                format!("cannot read the saved daemon environment: {e}"),
                "swarm daemon restart (from a terminal)",
            ));
            return;
        }
    };
    rows.push(finding(
        "daemon.env",
        "ok",
        format!("saved, {} variables", values.len()),
        "",
    ));
    let saved = values
        .iter()
        .find_map(|v| v.strip_prefix("PATH="))
        .unwrap_or("");
    let caller = env::var("PATH").unwrap_or_default();
    for name in ["agy", "claude", "codex", "hermes", "opencode"] {
        let check = format!("agent:{name}");
        rows.push(match (resolve(&caller,name),resolve(saved,name)){
            (Some(a),None)=>finding(check,"fail",format!("resolves for your shell ({}) but NOT for the daemon's saved environment -- non-terminal launches of {name} break",a.display()),"swarm daemon restart (from this terminal)"),
            (None,Some(b))=>finding(check,"warn",format!("resolves for the daemon's saved environment ({}) but not for your shell",b.display()),""),
            (Some(a),Some(b)) if a!=b=>finding(check,"warn",format!("your shell resolves {}, the saved environment resolves {} -- two different binaries",a.display(),b.display()),"swarm daemon restart (from this terminal)"),
            (Some(a),_)=>finding(check,"ok",a.display().to_string(),""),
            _=>finding(check,"ok","not installed",""),
        });
    }
    for key in ["ANTHROPIC_API_KEY", "OPENAI_API_KEY"] {
        if env::var_os(key).is_some_and(|s| !s.is_empty())
            && !values
                .iter()
                .filter_map(|v| v.split_once('='))
                .any(|(k, v)| k == key && !v.is_empty())
        {
            rows.push(finding(format!("credential:{key}"),"warn","set in your shell but missing from the daemon's saved environment; non-terminal launches lose it","swarm daemon restart (from this terminal)"));
        }
    }
}
fn sessions(root: &Path, rows: &mut Vec<Finding>) {
    let entries = match fs::read_dir(root) {
        Ok(e) => e,
        Err(e) if e.kind() == io::ErrorKind::NotFound => {
            rows.push(finding(
                "sessions",
                "ok",
                "no state dir yet; nothing has ever run here",
                "",
            ));
            return;
        }
        Err(e) => {
            rows.push(finding(
                "sessions",
                "warn",
                format!("cannot read the state dir: {e}"),
                "",
            ));
            return;
        }
    };
    let (mut total, mut live, mut past) = (0, 0, 0);
    for entry in entries
        .flatten()
        .filter(|e| e.file_type().is_ok_and(|t| t.is_dir()))
    {
        let p = entry.path();
        if entry.file_name() == "rust-v1" {
            match p.read_dir() {
                Ok(list) => {
                    for e in list.flatten().filter(|r| {
                        r.file_name()
                            .to_str()
                            .is_some_and(crate::supervisor::runtime::valid_id)
                    }) {
                        match crate::supervisor::runtime::read_json(&e.path().join("session.json"))
                        {
                            Ok(meta) if meta["id"].as_str() == e.file_name().to_str() => total += 1,
                            _ => rows.push(finding(
                                "sessions",
                                "warn",
                                "unreadable or inconsistent Rust session record",
                                "inspect the affected session directory before recovery",
                            )),
                        }
                    }
                }
                Err(e) => rows.push(finding(
                    "sessions",
                    "warn",
                    format!("cannot read the state dir: {e}"),
                    "",
                )),
            }
            continue;
        }
        let Ok(meta) = read_json(&p.join("meta.json")) else {
            continue;
        };
        total += 1;
        let error = field(&meta, "backend_plan_error");
        if !error.is_empty() {
            if meta["status"]["Process"] == "running" {
                live += 1;
                rows.push(finding(
                    format!("session:{}", field(&meta, "id")),
                    "fail",
                    format!("running with no backend: {error}"),
                    "fix the cause above, then kill and relaunch the session",
                ));
            } else {
                past += 1;
            }
        }
    }
    rows.push(finding(
        "sessions",
        "ok",
        format!("{total} persisted, {live} running degraded, {past} past degraded"),
        "",
    ));
}
fn read_json(path: &Path) -> io::Result<Value> {
    Ok(serde_json::from_str(&environment::private_text(path)?)?)
}
fn upgrade(root: &Path) -> Finding {
    if let Ok(text) = environment::private_text(&root.join("upgrade/pending-converge"))
        && !text.trim().is_empty()
    {
        return finding(
            "auto-update",
            "warn",
            format!(
                "{} is installed but its converge has not completed; the daemon may still run the previous build",
                text.trim()
            ),
            "swarm upgrade --unattended (retries the converge)",
        );
    }
    let journal = match crate::upgrade::identify() {
        Ok(Some((install, _))) => install.join("upgrade.json"),
        Ok(None) => root.join("upgrade.json"),
        Err(error) => {
            return finding(
                "auto-update",
                "warn",
                format!("cannot identify upgrade owner: {error}"),
                "",
            );
        }
    };
    let st = match read_json(&journal) {
        Ok(v) => v,
        Err(e) if e.kind() == io::ErrorKind::NotFound => {
            return finding("auto-update", "ok", "has not run on this machine", "");
        }
        Err(e) => {
            return finding(
                "auto-update",
                "warn",
                format!("cannot read the last run's record: {e}"),
                "",
            );
        }
    };
    let Ok(at) = OffsetDateTime::parse(field(&st, "checked_at"), &Rfc3339) else {
        return finding(
            "auto-update",
            "warn",
            "cannot read the last run's record: invalid check timestamp",
            "",
        );
    };
    let result = field(&st, "outcome");
    let mut detail = format!(
        "last run {:04}-{:02}-{:02} {:02}:{:02}: {result}",
        at.year(),
        u8::from(at.month()),
        at.day(),
        at.hour(),
        at.minute()
    );
    if !field(&st, "staged_version").is_empty() {
        detail += &format!(
            " ({} staged, awaiting activation)",
            field(&st, "staged_version")
        );
    }
    let seconds = (OffsetDateTime::now_utc() - at).whole_seconds();
    if seconds > 48 * 3600 {
        return finding(
            "auto-update",
            "warn",
            format!(
                "{detail} -- {}h0m0s ago; the scheduler has been quiet",
                (seconds + 1800) / 3600
            ),
            "swarm upgrade --unattended",
        );
    }
    if result.starts_with("failed") {
        return finding(
            "auto-update",
            "fail",
            format!("{detail}: {}", field(&st, "detail")),
            "swarm upgrade --stage (and read the detail)",
        );
    }
    if result == "refused-owner" {
        detail += &format!(": {}", field(&st, "detail"));
    }
    finding("auto-update", "ok", detail, "")
}
pub(crate) fn run(args: &[String]) -> u8 {
    let mut as_json = false;
    let rest = match crate::flags::options(args, &["json"], &["json"], |key, value| {
        as_json = crate::flags::parse_bool(key, value)?;
        Ok(())
    }) {
        Ok(rest) => rest,
        Err(e) => {
            if !e.is_empty() {
                eprintln!("{e}");
            }
            eprintln!("Usage of doctor:\n  -json\n    \tprint the findings as JSON");
            return 2;
        }
    };
    if let Some(arg) = rest.first() {
        eprintln!("doctor: unexpected argument {arg:?}");
        return 2;
    }
    let (state, sock) = match ipc::state_path().and_then(|p| Ok((p, ipc::socket_path()?))) {
        Ok(pair) => pair,
        Err(e) => {
            eprintln!("doctor: {e}");
            return 1;
        }
    };
    let mut report = vec![daemon(&state, &sock)];
    saved(&state, &mut report);
    sessions(&state, &mut report);
    report.push(upgrade(&state));
    report.push(storage::check(&state));
    if as_json {
        let rows: Vec<_> = report
            .iter()
            .map(|f| {
                let mut v = json!({"check":f.check,"status":f.status,"detail":f.detail});
                if !f.fix.is_empty() {
                    v["fix"] = json!(f.fix);
                }
                v
            })
            .collect();
        println!(
            "{}",
            serde_json::to_string_pretty(&rows).expect("JSON findings")
        );
    } else {
        let mut rows = vec![[
            "STATUS".into(),
            "CHECK".into(),
            "DETAIL".into(),
            "FIX".into(),
        ]];
        rows.extend(report.iter().map(|f| {
            [
                f.status.to_uppercase(),
                f.check.clone(),
                f.detail.clone(),
                f.fix.into(),
            ]
        }));
        let widths: [usize; 3] = std::array::from_fn(|i| {
            rows.iter().map(|r| r[i].chars().count()).max().unwrap_or(0) + 2
        });
        for row in rows {
            for i in 0..3 {
                print!("{:<width$}", row[i], width = widths[i]);
            }
            println!("{}", row[3]);
        }
    }
    u8::from(report.iter().any(|f| f.status == "fail"))
}
