use super::{export, runtime};
use crate::{
    ipc::field,
    providers::{self, claude},
    supervisor::session::{Prepared, Session, sanitize_name},
};
use serde_json::{Value, json};
use std::{
    collections::BTreeSet,
    fs,
    io::{self, Read},
    path::Path,
};

const RELEASE: &str = "2.1.329";

fn fresh_argv(meta: &Value, evidence: &Value) -> bool {
    let Some(args) = evidence["argv"].as_array() else {
        return false;
    };
    let [_, flag, settings, tail @ ..] = args.as_slice() else {
        return false;
    };
    let events = [
        "UserPromptSubmit",
        "PreToolUse",
        "PostToolUse",
        "Notification",
        "Stop",
        "SubagentStart",
        "SubagentStop",
        "PermissionRequest",
    ];
    let hooks = events
        .into_iter()
        .map(|event| {
            (
                event.into(),
                json!([{"hooks":[{"type":"command","command":format!("swarm hook {event}")}]}]),
            )
        })
        .collect::<serde_json::Map<_, _>>();
    if flag != "--settings"
        || settings
            .as_str()
            .and_then(|s| serde_json::from_str::<Value>(s).ok())
            != Some(json!({"hooks":hooks}))
    {
        return false;
    }
    let tail = match tail {
        [name, label, rest @ ..] if name == "--name" && label.is_string() => rest,
        _ => tail,
    };
    let options = &meta["launch_options"];
    if !options.is_null()
        && options.as_object().is_none_or(|options| {
            options.iter().any(|(key, value)| {
                !matches!(key.as_str(), "model" | "dangerously-skip-permissions")
                    || !value.is_string()
            })
        })
    {
        return false;
    }
    let mut expected = Vec::new();
    let model = field(options, "model");
    if !model.is_empty() && !model.starts_with('-') {
        expected.extend([json!("--model"), json!(model)]);
    }
    if options["dangerously-skip-permissions"] == "true" {
        expected.push(json!("--dangerously-skip-permissions"));
    }
    tail == expected
}

fn saved_environment(meta: &Value, evidence: &Value) -> io::Result<Vec<(String, String)>> {
    let saved = runtime::launch_env(&meta["env"]);
    let allowed_hook_keys = [
        "SWARM_SESSION_ID",
        "SWARM_HOOK_TOKEN",
        "SWARM_DAEMON_SOCK",
        "SWARM_HOOK_SEQ_FILE",
        "SWARM_SHIM_HOOK_SOCK",
        "SWARM_HOOK_CAPTURE",
    ];
    if meta["env"].as_array().map(Vec::len) != Some(saved.len())
        || saved
            .iter()
            .map(|(key, _)| key)
            .collect::<BTreeSet<_>>()
            .len()
            != saved.len()
        || evidence["env"].as_array().is_none_or(|env| {
            let original = meta["env"].as_array().expect("validated Go environment");
            !env.starts_with(original)
                || env
                    .iter()
                    .skip(saved.len())
                    .map(|item| {
                        item.as_str()
                            .and_then(|item| item.split_once('='))
                            .map(|(key, _)| key)
                    })
                    .ne(allowed_hook_keys.map(Some))
        })
    {
        return Err(io::Error::other("Go Claude launch environment is unproved"));
    }
    Ok(saved)
}

fn identity(executable: &Path, version: &str, observed: &Value) -> io::Result<Value> {
    let mut magic = [0; 4];
    fs::File::open(executable)?.read_exact(&mut magic)?;
    #[cfg(target_os = "macos")]
    let native = matches!(magic, [0xcf, 0xfa, 0xed, 0xfe] | [0xca, 0xfe, 0xba, 0xbe]);
    #[cfg(target_os = "linux")]
    let native = magic == *b"\x7fELF";
    if version != RELEASE || !native {
        return Err(io::Error::other(
            "Go Claude requires the 2.1.329 native executable",
        ));
    }
    export::legacy_identity(executable, version, observed, "claude")
}

pub(super) fn prepare(
    launch: &Value,
    qualified: &str,
    meta: &Value,
    evidence: &Value,
) -> io::Result<Prepared> {
    if launch["options"]
        .as_object()
        .is_none_or(|options| options.len() != 1)
        || launch.as_object().is_none_or(|keys| {
            keys.iter().any(|(key, value)| {
                !matches!(
                    key.as_str(),
                    "agent" | "name" | "cwd" | "env" | "cols" | "rows" | "options"
                ) && !value.is_null()
            })
        })
        || launch["cwd"] != meta["cwd"]
        || !super::executable_binding(meta, evidence, "claude")
        || meta["shim_wire_version"] != 2
        || !meta["auth_identity"].is_null()
        || !fresh_argv(meta, evidence)
    {
        return Err(io::Error::other(
            "Go Claude resume requires a fresh ordinary source in its original checkout",
        ));
    }

    let env = &meta["env"];
    let saved = saved_environment(meta, evidence)?;
    let options = &meta["launch_options"];
    let profile = claude::go_api_profile(&saved, env, options)?;
    let home = Path::new(field(&profile, "home"));
    let cwd = Path::new(field(meta, "cwd"));
    let caller = runtime::launch_env(&launch["env"]);
    if std::env::var_os("HOME").as_deref() != Some(home.as_os_str())
        || claude::go_api_profile(&caller, &launch["env"], options)?.get("origin")
            != profile.get("origin")
        || caller
            .iter()
            .filter(|(key, _)| key == "HOME")
            .map(|(_, value)| Path::new(value))
            .ne([home])
        || saved
            .iter()
            .filter(|(key, _)| key == "HOME")
            .map(|(_, value)| Path::new(value))
            .ne([home])
        || field(&profile, "path").is_empty()
        || !field(meta, "cwd").is_ascii()
    {
        return Err(io::Error::other(
            "Go Claude saved account or checkout is unproved",
        ));
    }
    let authority = claude::project_authority(home, cwd)?;
    let thread = field(meta, "conversation_id");
    let (path, transcript) = claude::literal_transcript(home, cwd, thread)?;
    let expected = &meta["cli_identity"];
    let executable = Path::new(field(expected, "path"));
    if providers::resolve("claude", cwd, field(&profile, "path"))? != executable
        || identity(
            executable,
            field(expected, "version"),
            &providers::identity(executable)?,
        )? != *expected
    {
        return Err(io::Error::other("Go Claude saved executable changed"));
    }

    let mut bound = launch.clone();
    bound["env"] = env.clone();
    bound["options"] = options.clone();
    bound["tag"] = meta["tag"].clone();
    if field(&bound, "name").is_empty() {
        bound["name"] = meta["name"].clone();
    }
    let mut request = json!({"launch": bound});
    let mut prepared = Session::prepare(&request)?;
    prepared.bind_go_rollback(&request)?;
    if !prepared.provider.stable_release
        || identity(
            &prepared.provider.executable,
            &prepared.provider.version,
            &prepared.provider.identity,
        )? != *expected
        || prepared.claude_profile != profile
    {
        return Err(io::Error::other(
            "Go Claude saved executable or authority changed",
        ));
    }

    let bound = &mut request["launch"];
    bound["claude_retained_conversation"] = json!(thread);
    prepared.provider.argv = claude::argv(&sanitize_name(field(bound, "name")), bound)?;
    prepared.claude_resume = json!({
        "conversation_id":thread,"resumed_from":qualified,"transcript":transcript,"path":path,"project_authority":authority
    });
    prepared.go_rollback["claude_resume"] = prepared.claude_resume.clone();
    prepared.go_rollback["cli_identity"] = expected.clone();
    Ok(prepared)
}

pub(super) fn export_proof(dir: &Path, config: &Value, conversation: &Value) -> io::Result<Value> {
    let rollback = &config["go_rollback"];
    let resume = &config["claude_resume"];
    let provider = &config["provider"];
    let cwd = Path::new(field(config, "cwd"));
    let profile = claude::go_api_profile(
        &runtime::launch_env(&rollback["env"]),
        &rollback["env"],
        &rollback["options"],
    )?;
    let home = Path::new(field(&profile, "home"));
    let (path, transcript) =
        claude::literal_transcript(home, cwd, field(resume, "conversation_id"))?;
    let adoption = runtime::read_json(&dir.join("claude-go-resume-observed.json"));
    if !matches!(dir.join("claude-thread-changed.json").symlink_metadata(),
        Err(error) if error.kind() == io::ErrorKind::NotFound)
        || adoption.ok()
            != Some(json!({"token":config["token"],"conversation_id":conversation,"path":path}))
    {
        return Err(io::Error::other(
            "Go Claude rollback requires a confirmed native transcript adoption",
        ));
    }
    if resume["conversation_id"] != *conversation
        || providers::resolve("claude", cwd, field(&profile, "path"))?
            != Path::new(field(provider, "executable"))
        || profile != config["claude_profile"]
        || std::env::var_os("HOME").as_deref() != Some(home.as_os_str())
        || identity(
            Path::new(field(provider, "executable")),
            field(provider, "version"),
            &provider["identity"],
        )? != rollback["cli_identity"]
        || transcript != resume["transcript"]
        || json!(path) != resume["path"]
    {
        return Err(io::Error::other(
            "Go Claude rollback conversation, account or executable changed",
        ));
    }
    claude::verify_project_authority(home, cwd, &resume["project_authority"])?;
    Ok(
        json!({"conversation":conversation,"cwd":cwd,"profile":profile,"transcript":resume["transcript"]}),
    )
}
