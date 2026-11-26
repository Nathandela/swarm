use super::{export, runtime};
use crate::{
    ipc::field,
    providers::{codex, codex_continuity},
    refresh,
    supervisor::session::{Prepared, Session, codex_resume_args},
};
use serde_json::{Value, json};
use std::{collections::BTreeSet, io, path::Path};

fn saved_environment(env: &Value) -> io::Result<Vec<(String, String)>> {
    let entries = runtime::launch_env(env);
    let unique: BTreeSet<_> = entries.iter().map(|(key, _)| key).collect();
    if env.as_array().map(Vec::len) != Some(entries.len())
        || unique.len() != entries.len()
        || entries
            .iter()
            .any(|(key, value)| key == "OPENAI_API_KEY" && !value.is_empty())
    {
        return Err(io::Error::other(
            "unqualified or ambiguous saved Go Codex environment",
        ));
    }
    Ok(entries)
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
        || field(evidence, "backend_program").is_empty()
    {
        return Err(io::Error::other(
            "Go Codex resume requires an ordinary managed source in its original checkout",
        ));
    }

    let options = &meta["launch_options"];
    let env = &meta["env"];
    let saved = saved_environment(env)?;
    codex::refuse_unqualified_origin(&launch["env"])?;
    let caller = runtime::launch_env(&launch["env"]);
    let cwd = Path::new(field(meta, "cwd"));
    let profile = codex_continuity::profile(&saved, cwd, options)?;
    let home = Path::new(field(&profile, "home"));
    let native = home.join(".codex");
    if std::env::var_os("HOME").as_deref() != Some(home.as_os_str())
        || codex::explicit_home(&launch["env"], home)?.is_some()
        || caller
            .iter()
            .filter(|(key, _)| key == "HOME")
            .map(|(_, value)| Path::new(value))
            .ne([home])
        || caller
            .iter()
            .any(|(key, value)| key == "OPENAI_API_KEY" && !value.is_empty())
        || field(&profile, "path").is_empty()
        || profile["native_home"] != json!(native)
    {
        return Err(io::Error::other(
            "Go Codex resume requires the saved default native home and PATH",
        ));
    }

    let effective = &evidence["env"];
    codex::refuse_unqualified_origin(effective)?;
    if !effective.is_array()
        || codex::explicit_home(effective, home)?.is_some()
        || runtime::launch_env(effective) != saved
        || evidence["backend_env"]
            .as_array()
            .is_some_and(|env| !env.is_empty())
        || refresh::auth_identity("codex", env).as_deref() != meta["auth_identity"].as_str()
        || !meta["auth_identity"].is_string()
    {
        return Err(io::Error::other(
            "Go Codex saved account or launch environment changed",
        ));
    }

    let mut bound = launch.clone();
    bound["env"] = env.clone();
    bound["options"] = options.clone();
    bound["tag"] = meta["tag"].clone();
    if field(&bound, "name").is_empty() {
        bound["name"] = meta["name"].clone();
    }
    let request = json!({"launch":bound});
    let mut prepared = Session::prepare(&request)?;
    prepared.bind_go_rollback(&request)?;
    if prepared.go_rollback["cli_identity"] != meta["cli_identity"]
        || prepared.go_rollback["codex_profile"] != profile
    {
        return Err(io::Error::other(
            "Go Codex saved executable or authority changed",
        ));
    }

    let thread = field(meta, "conversation_id");
    let rollout = codex_continuity::go_rollout(&native, cwd, thread, export::codex::RELEASE)?;
    prepared.provider.argv = codex_resume_args(thread, cwd, field(options, "model"));
    prepared.codex_resume = json!({
        "thread":thread,"resumed_from":qualified,"profile":profile,"rollout":rollout,"source_cwd":cwd
    });
    prepared.go_rollback["codex_resume"] = prepared.codex_resume.clone();
    prepared.go_rollback["auth_identity"] = meta["auth_identity"].clone();
    Ok(prepared)
}
