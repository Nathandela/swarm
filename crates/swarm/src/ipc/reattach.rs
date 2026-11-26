use super::{CliResult, Client, field, options, report};
use crate::providers::{codex, codex_continuity};
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
        || get("cli") != "codex"
        || !selected.is_empty() && !crate::providers::codex_receipt::canonical_thread(selected)
    {
        return Err((
            2,
            "use swarm reattach --cli codex [--session ID] [--all] [--take-over] [--dry-run]"
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
