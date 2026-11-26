//! Supervisor-independent validation of the enrolled worker tool's typed intent.
use crate::{ipc::field, providers::codex_receipt, supervisor::mail};
use serde_json::{Value, json};
use std::{io, path::Path};

pub(crate) const CAP: &str = "manager-enrolled-worker-v1";

pub(crate) fn arguments_hash(arguments: &Value) -> io::Result<String> {
    super::receipt::hash(arguments)
}

pub(super) fn key(input: &Value, arguments: &Value) -> io::Result<String> {
    let action = field(arguments, "action_id");
    if action.is_empty()
        || action.len() > 128
        || action.trim() != action
        || action.chars().any(char::is_control)
    {
        return Err(super::invalid("invalid enrolled worker action ID"));
    }
    Ok(format!(
        "dact_{}",
        super::receipt::hash(&json!([
            input["composer"]["manager_admission"]["nonce"],
            action
        ]))?
    ))
}

pub(crate) fn launch(scope: &Value, input: &Value, action: &Value) -> io::Result<(Value, Value)> {
    let args = &action["arguments"];
    let prompt = field(args, "prompt");
    let agent = field(args, "agent");
    let name = field(args, "name");
    let model = field(args, "model");
    let cwd = Path::new(field(args, "cwd"));
    if scope["launch"]["manager"]["native_cli"] == true
        || !matches!(agent, "codex" | "claude")
        || !crate::teammate::valid_text(prompt)
        || name.len() > 64
        || model.len() > 128
        || model.starts_with('-')
        || name.chars().chain(model.chars()).any(char::is_control)
        || args.as_object().is_none_or(|fields| {
            fields.iter().any(|(field, value)| match field.as_str() {
                "action_id" | "agent" | "cwd" | "prompt" | "name" | "model" => !value.is_string(),
                "worktree" => !value.is_boolean(),
                _ => true,
            })
        })
        || action["key"] != key(input, args)?
        || !crate::ipc::spawn::replayable(
            field(action, "operation_id"),
            crate::ipc::spawn::now_ms(),
        )
        || !cwd.is_absolute()
        || cwd.canonicalize()? != cwd
        || !cwd.is_dir()
        || (scope["launch"]["cwd"] != json!(cwd)
            && !scope["projects"]
                .as_object()
                .is_some_and(|projects| projects.values().any(|path| *path == json!(cwd))))
    {
        return Err(super::invalid(
            "worker launch exceeds the exact enrolled project grant",
        ));
    }
    let composer = &input["composer"];
    let admission = &composer["manager_admission"];
    let nonce = field(admission, "nonce");
    let binding = &admission["binding"];
    if !codex_receipt::canonical_thread(field(binding, "native_session_id")) || nonce.len() != 64 {
        return Err(super::invalid(
            "enrolled worker launch has no native parent admission",
        ));
    }
    let parent = field(composer, "session")
        .rsplit('/')
        .next()
        .unwrap_or_default();
    let operation = field(action, "operation_id");
    let options = if model.is_empty() {
        json!({})
    } else {
        json!({"model":model})
    };
    let launch = json!({"agent":agent,"name":name,"cwd":cwd,"options":options,
        "initial_prompt":format!("Use this approved project for the delegated task. Report verified results only.\n\n{prompt}"),
        "worktree":args["worktree"]==true,"cols":120,"rows":32,"spawned_from":parent,"spawn_intent":"delegate"});
    let child = json!({"discussion":binding["conversation_id"],"parent":parent,
        "parent_incarnation":binding["session_instance"],"parent_input_hash":mail::parent_hash(nonce),
        "attempt":operation,"project":cwd,"structured":true,
        "prompt_hash":codex_receipt::hash(field(&launch,"initial_prompt"))});
    Ok((launch, child))
}
