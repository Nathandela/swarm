//! Current project and settled tool launch checks for original Go publication proofs.
use super::*;

pub(super) fn project(cfg: &Config, root: &str) -> bool {
    let path = Path::new(root);
    if !path.is_absolute()
        || !fs::canonicalize(path).is_ok_and(|resolved| resolved == path)
        || !path.is_dir()
        || cfg.text("project_access") == "vm"
    {
        return false;
    }
    match cfg.0["projects"].as_object() {
        Some(grants) if !grants.is_empty() => grants.values().any(|configured| {
            configured.as_str().is_some_and(|path| {
                fs::canonicalize(path).is_ok_and(|current| current == Path::new(root))
            })
        }),
        _ => fs::canonicalize(cfg.text("work_dir")).is_ok_and(|current| current == path),
    }
}

fn list(value: &Value) -> io::Result<Vec<String>> {
    match value {
        Value::Null => Ok(Vec::new()),
        Value::String(body) if body.is_empty() => Ok(Vec::new()),
        Value::String(body) if body.len() <= 1 << 16 => {
            let values: Vec<String> = serde_json::from_str(body)?;
            if values.len() > 32
                || values
                    .iter()
                    .any(|value| value.is_empty() || value.len() > 4096 || value.contains('\0'))
            {
                return Err(invalid());
            }
            Ok(values)
        }
        _ => Err(invalid()),
    }
}

pub(super) fn output<'tx>(
    records: &impl BucketApi<'tx>,
    cfg: &Config,
    endpoint: &str,
    conversation: &str,
    discussion: &str,
    publication: &Value,
) -> io::Result<()> {
    for root in list(&publication["discussion_projects"])? {
        if !project(cfg, &root) {
            return Err(io::Error::other(
                "the original publication project is no longer enrolled",
            ));
        }
    }
    let mut attempts = list(&publication["discussion_attempts"])?;
    let direct = field(&publication["discussion_source"], "attempt_id");
    if !direct.is_empty() {
        attempts.push(direct.to_owned());
    }
    for id in attempts {
        // Unconfined VM actions require a separately qualified custody lineage.
        if id.starts_with("vm-child-") {
            return Err(invalid());
        }
        let key = format!(
            "actions\0{}",
            serde_json::to_string(&[conversation, id.as_str()])?
        );
        let raw = records
            .get(key.as_bytes())
            .filter(|bytes| bytes.len() <= 1 << 20)
            .ok_or_else(invalid)?;
        let attempt: Value = serde_json::from_slice(raw)?;
        let request = &attempt["request"];
        let receipt = &attempt["receipt"];
        let origin = &request["origin"];
        let message = field(origin, "message_id");
        if attempt["conversation_id"] != conversation
            || attempt["id"] != id
            || attempt["state"] != "settled"
            || !matches!(
                (field(request, "tool"), field(receipt, "state")),
                ("swarm_create", "created") | ("swarm_resume", "resumed")
            )
            || !field(receipt, "session")
                .strip_prefix(&format!("{endpoint}/"))
                .is_some_and(|id| !id.is_empty() && !id.contains('/'))
            || field(receipt, "session_instance").is_empty()
            || origin["kind"] != "owner_direct"
            || origin["installation_id"] != cfg.text("installation_id")
            || origin["discussion_id"] != discussion
            || origin["channel_id"] != cfg.text("dm_channel_id")
            || !origin["guild_id"].is_null()
            || origin["actor_id"] != cfg.text("owner_id")
            || origin["requester_id"] != cfg.text("owner_id")
            || !snowflake(message)
            || origin["request_id"] != operation(cfg.text("installation_id"), message)?
            || !request["launch"].is_object()
            || request["launch"]["cwd"] != request["project"]
            || attempt["request_hash"]
                != format!(
                    "{:x}",
                    Sha256::digest(
                        crate::ipc::escape_json(serde_json::to_string(request)?).as_bytes()
                    )
                )
            || !project(cfg, field(request, "project"))
        {
            return Err(io::Error::other(
                "the original publication tool attempt is not currently authorized",
            ));
        }
    }
    Ok(())
}
