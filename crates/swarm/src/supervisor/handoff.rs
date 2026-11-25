//! Resolve handoff lineage at its authoritative source.
use super::{State, runtime};
use crate::ipc::field;
use serde_json::{Value, json};
use std::{fs, io, path::Path};

pub(super) fn bind(
    state: &State,
    request: &Value,
) -> io::Result<(Value, Option<(String, String)>)> {
    let mut bound = request.clone();
    let launch = &mut bound["launch"];
    let source = field(launch, "spawned_from").to_owned();
    let intent = field(launch, "spawn_intent");
    let mode = field(launch, "supervision");
    for key in ["spawned_from", "spawn_intent", "supervision"] {
        if !launch[key].is_null() && !launch[key].is_string() {
            return Err(io::Error::other("invalid launch lineage field type"));
        }
    }
    if !matches!(intent, "" | "handoff" | "delegate")
        || !matches!(mode, "" | "passive" | "manual" | "none")
        || !mode.is_empty() && intent != "handoff"
        || source.is_empty() && !intent.is_empty()
    {
        return Err(io::Error::other(
            "invalid handoff lineage or supervision mode",
        ));
    }
    if source.is_empty() {
        return Ok((bound, None));
    }
    if !runtime::valid_id(&source) {
        return Err(io::Error::other("invalid local source session id"));
    }
    let parent = state
        .sessions
        .read()
        .unwrap()
        .get(&source)
        .cloned()
        .ok_or_else(|| {
            io::Error::other("handoff: source session is no longer in this supervisor's roster")
        })?;
    if !parent.owner_interactive {
        return Err(io::Error::other(
            "handoff: restricted source requires its bound manager or worker route",
        ));
    }
    if !mode.is_empty() {
        let cwd = parent.verify_effective()?;
        if !parent.is_live() || !cwd.is_dir() {
            return Err(io::Error::other(
                "handoff: source session or its checkout is no longer available",
            ));
        }
        launch["cwd"] = json!(cwd);
        if crate::workspace::is_repo(cwd) {
            launch["worktree"] = json!(true);
        }
    }
    if launch["worktree"] == true {
        let target = Path::new(field(launch, "cwd")).canonicalize()?;
        for dir in [std::env::temp_dir(), "/tmp".into(), "/var/tmp".into()] {
            if let Ok(tmp) = fs::canonicalize(dir)
                && target.starts_with(tmp)
            {
                return Err(io::Error::other(
                    "handoff: the worktree destination is temporary; hand off from a durable checkout instead",
                ));
            }
        }
    }
    Ok((bound, Some((source, parent.incarnation()))))
}
