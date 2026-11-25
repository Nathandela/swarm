use super::{CliResult, Client, field, options, report, spawn};
use serde_json::{Value, json};
use std::{env, io, time::Duration};

const USAGE: &str = include_str!("handoff_usage.txt");
const DEFAULTS: &str = "  -cli string\n    \tagent CLI to receive the handoff (required)\n  -context-file string\n    \tagent-authored handoff context file (required)\n  -model string\n    \tmodel to launch the target agent with\n  -name string\n    \ttarget session label (default: the daemon's)\n  -supervision string\n    \tsupervision mode: passive, manual or none (default \"passive\")\n";

pub(super) fn run(client: &mut Client, args: &[String]) -> CliResult {
    let (mut cli, mut model, mut name, mut source, mut mode) = ("", "", "", "", "passive");
    let rest = match options(
        args,
        &["cli", "model", "name", "context-file", "supervision"],
        &[],
        |key, val| {
            match key {
                "cli" => cli = val,
                "model" => model = val,
                "name" => name = val,
                "context-file" => source = val,
                _ => mode = val,
            }
            Ok(())
        },
    ) {
        Ok(rest) => rest,
        Err(e) => {
            if !e.is_empty() {
                eprintln!("{e}");
            }
            eprint!("{USAGE}{DEFAULTS}");
            return if e.is_empty() {
                Ok(())
            } else {
                Err((2, String::new()))
            };
        }
    };
    if let Some(arg) = rest.first() {
        return Err((2, format!("unexpected argument {arg:?}")));
    }
    if cli.is_empty() {
        return Err((2, "--cli is required".into()));
    }
    if source.is_empty() {
        return Err((2, "--context-file is required".into()));
    }
    if !matches!(mode, "passive" | "manual" | "none") {
        return Err((
            2,
            format!("--supervision {mode:?} is not one of passive, manual, none"),
        ));
    }
    let parent = env::var("SWARM_SESSION_ID").unwrap_or_default();
    if parent.is_empty() {
        return Err((
            1,
            "SWARM_SESSION_ID is not set; run this command inside a Swarm-managed session".into(),
        ));
    }
    let rows = client
        .sessions()
        .map_err(|e| (1, format!("list source session: {e}")))?;
    let row = rows
        .iter()
        .find(|row| {
            let id = field(row, "id");
            id == parent || id.split_once('/').is_some_and(|(_, local)| local == parent)
        })
        .ok_or_else(|| {
            (
                1,
                format!("source session {parent:?} is not in the current Swarm roster"),
            )
        })?;
    if row["status"]["Process"] != "running" {
        return Err((1, format!("source session {parent:?} is not running")));
    }
    let doc =
        spawn::snapshot("", source).map_err(|e| (1, format!("copy context {source:?}: {e}")))?;
    let cwd = field(row, "cwd");
    let prompt = format!(
        "You are receiving a supervised Swarm handoff. Read and follow the context in {}. Verify the repository's current state before changing anything.",
        doc.display()
    );
    let environment: Vec<_> = env::vars_os()
        .map(|(k, v)| format!("{}={}", k.to_string_lossy(), v.to_string_lossy()))
        .collect();
    let opts = if model.is_empty() {
        Value::Null
    } else {
        json!({"model":model})
    };
    let mut req = json!({"agent":cli,"cwd":cwd,"env":environment,"initial_prompt":prompt,
                        "cols":80,"rows":24,"options":opts,"spawned_from":parent,"spawn_intent":"handoff","supervision":mode});
    if !name.is_empty() {
        req["name"] = json!(name);
    }
    let timeout = match cli {
        "opencode" => 75,
        "hermes" => 100,
        _ => 10,
    };
    let response = client
        .exchange(
            json!({"op":"launch","launch":req}),
            "launch",
            Duration::from_secs(timeout),
        )
        .map_err(|e| {
            spawn::dispose_refused(&doc, &e, "handoff");
            (1, format!("launch target: {e}"))
        })?;
    let session = &response["session"];
    let id = session["id"].as_str().ok_or_else(|| {
        report(io::Error::other(
            "launch target: protocol: launch reply carried no session",
        ))
    })?;
    println!("{id}");
    eprintln!("handed off to {} ({id})", field(session, "name"));
    Ok(())
}
