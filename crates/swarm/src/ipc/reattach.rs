use super::{CliResult, Client, field, options, report};
use crate::providers::{self, claude, codex, codex_continuity};
use serde_json::{Value, json};
use std::{env, path::PathBuf};

pub(super) fn run(client: &mut Client, args: &[String]) -> CliResult {
    let mut flags = std::collections::BTreeMap::new();
    let remaining = options(
        args,
        &["cli", "session", "all", "take-over", "dry-run"],
        &["all", "take-over", "dry-run"],
        |key, value| {
            flags.insert(key.to_owned(), value);
            Ok(())
        },
    )
    .map_err(|err| (2, err))?;
    let get = |key: &str| flags.get(key).copied().unwrap_or_default();
    let flag = |key: &str| {
        flags
            .get(key)
            .map(|v| crate::flags::parse_bool(key, v))
            .transpose()
            .map(|value| value.unwrap_or(false))
            .map_err(|err| (2, err))
    };
    let selected = get("session");
    if !remaining.is_empty()
        || !matches!(get("cli"), "codex" | "claude")
        || !selected.is_empty()
            && (get("cli") != "codex"
                || !crate::providers::codex_receipt::canonical_thread(selected))
    {
        return Err((
            2,
            "use swarm reattach --cli claude|codex [--session ID] [--all] [--take-over] [--dry-run]"
                .into(),
        ));
    }
    if !client.supports("external-resume") {
        return Err((
            1,
            "the running swarm daemon does not support external resume".into(),
        ));
    }
    let all = flag("all")?;
    let dry = flag("dry-run")?;
    let takeover = flag("take-over")?;
    if get("cli") == "claude" {
        return claude_reattach(client, all, takeover, dry);
    }
    if !dry {
        codex::refuse_unqualified_origin(&Value::Null).map_err(report)?;
    }
    let home = env::var_os("HOME")
        .map(PathBuf::from)
        .ok_or_else(|| (1, "find home: HOME is missing".into()))?;
    let native = codex::explicit_home(&Value::Null, &home)
        .map_err(report)?
        .unwrap_or_else(|| home.join(".codex"));
    if !dry && native != home.join(".codex") {
        return Err((1,"external Codex resume requires the default ~/.codex home; custom homes can be inspected with --dry-run".into()));
    }
    let sessions = codex_continuity::discover_external(&native, all, selected).map_err(report)?;
    if !selected.is_empty() && sessions.is_empty() {
        return Err((
            1,
            format!(
                "Codex session {selected} was not found; use --all to include archived sessions"
            ),
        ));
    }
    if dry {
        for row in &sessions {
            let id = field(row, "id");
            let cwd = field(row, "cwd");
            if row["archived"] == true {
                println!("archived codex {id} (cwd {cwd:?}); unarchive before importing");
            } else {
                println!("would reattach codex {id} (cwd {cwd:?})");
            }
        }
        println!(
            "{} Codex session(s) eligible; native history does not establish whether an original CLI is still running",
            sessions.len()
        );
        return Ok(());
    }
    if let Some(archived) = sessions.iter().find(|row| row["archived"] == true) {
        let id = field(archived, "id");
        return Err((
            1,
            format!(
                "Codex session {id} is archived; run `codex unarchive {id}` before importing (no sessions launched)"
            ),
        ));
    }
    if !sessions.is_empty() && !takeover {
        return Err((1,"stop the original Codex CLI sessions, then rerun with --take-over to confirm; swarm cannot stop Codex automatically (use --session ID to select one)".into()));
    }
    let environment = crate::environment::reliable(
        "codex",
        env::vars_os()
            .filter_map(|(k, v)| Some(format!("{}={}", k.to_str()?, v.to_str()?)))
            .collect(),
    );
    let mut failures = 0;
    for row in &sessions {
        let id = field(row, "id");
        match client.exchange(
            json!({"op":"launch","launch":{
                "agent":"codex","name":format!("codex {}",&id[..8]),"cwd":row["cwd"],
                "env":environment,"cols":80,"rows":24,
                "options":{"resume_conversation_id":id},
            }}),
            "launch",
            super::launch_timeout("codex"),
        ) {
            Ok(reply) if !field(&reply["session"], "id").is_empty() => {
                println!(
                    "reattached Codex {id} as {}",
                    field(&reply["session"], "id")
                );
            }
            outcome => {
                failures += 1;
                eprintln!(
                    "reattach: {id}: {}",
                    outcome.err().map_or_else(
                        || "launch returned no session".into(),
                        |err| err.to_string()
                    )
                );
            }
        }
    }
    println!("{} Codex session(s) processed", sessions.len());
    if failures == 0 {
        Ok(())
    } else {
        Err((1, String::new()))
    }
}

fn claude_reattach(client: &mut Client, all: bool, takeover: bool, dry: bool) -> CliResult {
    let exact =
        client.supports("claude-batch-takeover-v1") || client.supports("claude-external-resume-v1");
    if !exact && !dry {
        // The pinned Go owner has this owner-only settings read; older Rust only
        // shares the general external-resume bit for Codex.
        if !client.supports("context-guard-settings")
            || !client
                .request_body(json!({"op":"context_guard_get"}), "context_guard_get")
                .is_ok_and(|reply| reply["context_guard_settings"].is_object())
        {
            return Err((
                1,
                "the running owner cannot verify Claude external resume".into(),
            ));
        }
    }
    let here = env::current_dir()
        .map_err(report)?
        .canonicalize()
        .map_err(report)?;
    let supplied = json!(
        env::vars_os()
            .filter_map(|(key, value)| Some(format!("{}={}", key.to_str()?, value.to_str()?)))
            .collect::<Vec<_>>()
    );
    let plan = providers::prepare(
        "claude",
        &here,
        "",
        &json!({"env":supplied}),
        crate::supervisor::runtime::launch_env(&supplied),
    )
    .map_err(report)?;
    let rows =
        providers::claude_agent_view(&plan.executable, &here, &plan.env, true).map_err(report)?;
    let snapshot = claude::background_inventory(&rows).map_err(report)?;
    let mut eligible = Vec::new();
    let mut failures = 0;
    for row in rows {
        if row["kind"] != "background" || !all && claude::agent_view_settled(&row) {
            continue;
        }
        let id = field(&row, "sessionId");
        if claude::canonical_id(id)
            && !field(&row, "cwd").trim().is_empty()
            && let Ok(cwd) = here.join(field(&row, "cwd")).canonicalize()
            && cwd.is_dir()
        {
            eligible.push((row, cwd));
        } else {
            failures += 1;
            eprintln!(
                "reattach: skip {:?}: invalid Claude session id or cwd",
                field(&row, "name")
            );
        }
    }
    if dry {
        for (row, _) in &eligible {
            println!(
                "would reattach {} {:?} ({})",
                field(row, "state"),
                field(row, "name"),
                field(row, "sessionId")
            );
        }
        println!("{} Claude background session(s) eligible", eligible.len());
    } else {
        let has_live = eligible
            .iter()
            .any(|(row, _)| !claude::agent_view_settled(row));
        if has_live && !takeover {
            return Err((1,"live Claude background sessions found; rerun with --take-over to stop Claude's supervisor before adopting them".into()));
        }
        let launch = |row: &Value, cwd: &std::path::Path| {
            json!({
                "agent":"claude","name":row["name"],"cwd":cwd,"env":supplied,"cols":80,"rows":24,
                "options":{"resume_conversation_id":row["sessionId"]},
            })
        };
        if has_live {
            if failures != 0 || !client.supports("claude-batch-takeover-v1") {
                return Err((1,"the owner cannot reserve the whole Claude takeover batch; native sessions remain running".into()));
            }
            let launches: Vec<_> = eligible.iter().map(|(row, cwd)| launch(row, cwd)).collect();
            let reply = client
                .exchange(
                    json!({"op":"claude_batch_takeover","launches":launches,"inventory":snapshot}),
                    "claude_batch_takeover",
                    std::time::Duration::from_secs(900),
                )
                .map_err(report)?;
            let results = reply["results"]
                .as_array()
                .filter(|rows| rows.len() == eligible.len())
                .ok_or_else(|| {
                    (
                        1,
                        "Claude takeover result is incomplete; check the owner roster".into(),
                    )
                })?;
            for ((row, _), result) in eligible.iter().zip(results) {
                if result["conversation_id"] != row["sessionId"] {
                    return Err((
                        1,
                        "Claude takeover result does not match the requested native source".into(),
                    ));
                }
                let id = field(&result["session"], "id");
                if !id.is_empty() {
                    println!("reattached {:?} as {id}", field(row, "name"));
                } else {
                    failures += 1;
                    eprintln!(
                        "reattach: {:?}: {}",
                        field(row, "name"),
                        field(result, "error")
                    );
                    if let Some(session) = result["session_id"].as_str() {
                        eprintln!("reattach: retained owner reservation {session}");
                    }
                }
            }
            println!("{} Claude background session(s) processed", eligible.len());
            return if failures == 0 {
                Ok(())
            } else {
                Err((1, String::new()))
            };
        }
        for (row, cwd) in &eligible {
            let result = client.exchange(
                json!({"op":"launch","launch":launch(row,cwd)}),
                "launch",
                std::time::Duration::from_secs(45),
            );
            match result {
                Ok(reply) if !field(&reply["session"], "id").is_empty() => println!(
                    "reattached {:?} as {}",
                    field(row, "name"),
                    field(&reply["session"], "id")
                ),
                outcome => {
                    failures += 1;
                    eprintln!(
                        "reattach: {:?}: {}",
                        field(row, "name"),
                        outcome
                            .err()
                            .map_or_else(|| "missing owner receipt".into(), |err| err.to_string())
                    );
                }
            }
        }
        println!("{} Claude background session(s) processed", eligible.len());
    }
    if failures == 0 {
        Ok(())
    } else {
        Err((1, String::new()))
    }
}
