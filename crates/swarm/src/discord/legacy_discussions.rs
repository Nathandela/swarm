//! Read the original Go teammate discussion and replay indexes without changing them.
use super::config::{Config, snowflake};
use bbolt_rs::{BucketApi, TxApi};
use serde::Deserialize;
use sha2::{Digest, Sha256};
use std::{collections::BTreeMap, io};

#[derive(Deserialize)]
#[serde(rename_all = "PascalCase")]
struct Binding {
    #[serde(rename = "ID")]
    id: String,
    #[serde(rename = "InstallationID")]
    installation: String,
    route: Route,
    #[serde(rename = "supervisor_socket", default)]
    supervisor_socket: String,
}

#[derive(Deserialize)]
struct Route {
    guild_id: String,
    channel_id: String,
    message_id: String,
    #[serde(default)]
    parent_id: String,
}

#[derive(Deserialize)]
#[serde(rename_all = "PascalCase")]
struct Guard {
    route: Route,
    #[serde(rename = "ActorID")]
    actor_id: String,
    #[serde(rename = "DiscussionID")]
    discussion_id: String,
    #[serde(rename = "OperationID")]
    operation_id: String,
    revision: String,
    disposition: String,
    last_change: String,
}

#[derive(Deserialize)]
#[serde(rename_all = "PascalCase")]
struct Checkpoint {
    session: String,
    gateway_sequence: i64,
    gap_epoch: u64,
    error: String,
}

fn scoped(cfg: &Config, route: &Route) -> bool {
    snowflake(&route.channel_id)
        && snowflake(&route.message_id)
        && if cfg.text("mode") == "personal" {
            route.guild_id.is_empty()
                && route.parent_id.is_empty()
                && route.channel_id == cfg.text("dm_channel_id")
        } else {
            snowflake(&route.guild_id)
                && cfg.0["guild_ids"]
                    .as_array()
                    .is_some_and(|ids| ids.iter().any(|id| id == &route.guild_id))
                && (route.parent_id.is_empty() || snowflake(&route.parent_id))
                && cfg.0["channel_ids"].as_array().is_none_or(|channels| {
                    channels.is_empty()
                        || channels
                            .iter()
                            .any(|id| id == &route.channel_id || id == &route.parent_id)
                })
        }
}

pub(super) fn operation(installation: &str, message: &str) -> io::Result<String> {
    // Original installations and Discord snowflakes use identical Go/Rust JSON encoding.
    if !installation
        .bytes()
        .all(|byte| byte.is_ascii_alphanumeric() || b"-_".contains(&byte))
    {
        return Err(io::Error::other("invalid original Discord installation ID"));
    }
    let digest = format!(
        "{:x}",
        Sha256::digest(serde_json::to_vec(&[installation, message])?)
    );
    Ok(format!("da_{}", &digest[..32]))
}

pub(super) fn verify<'tx>(tx: &impl TxApi<'tx>, cfg: &Config, old_socket: &str) -> io::Result<()> {
    let invalid = || io::Error::other("unqualified original discussion or replay index");
    let discussions = tx.bucket(b"teammate_discussions").ok_or_else(invalid)?;
    let ids = tx.bucket(b"teammate_discussion_ids").ok_or_else(invalid)?;
    let active = tx
        .bucket(b"teammate_active_discussions")
        .ok_or_else(invalid)?;
    let guards = tx.bucket(b"teammate_ingress_guard").ok_or_else(invalid)?;
    let meta = tx.bucket(b"teammate_meta").ok_or_else(invalid)?;
    let install = cfg.text("installation_id");
    let prefix = format!("{install}\0");
    let mut routes = BTreeMap::new();
    for (n, (key, raw)) in discussions.iter_entries().enumerate() {
        if n >= 32_768 || raw.len() > 1 << 20 {
            return Err(invalid());
        }
        if !key.starts_with(prefix.as_bytes()) {
            return Err(invalid());
        }
        let row: Binding = serde_json::from_slice(raw).map_err(|_| invalid())?;
        let route = row.route;
        if row.installation != install
            || row.id.is_empty()
            || row.id.len() > 512
            || row.id.contains('\0')
            || key != format!("{prefix}{}", route.channel_id).as_bytes()
            || ids.get(format!("{prefix}{}", row.id).as_bytes()) != Some(key)
            || !scoped(cfg, &route)
            || (!row.supervisor_socket.is_empty() && row.supervisor_socket != old_socket)
            || routes.insert(row.id, route).is_some()
        {
            return Err(invalid());
        }
    }
    for (n, (key, target)) in ids.iter_entries().enumerate() {
        if n >= 32_768
            || !key
                .strip_prefix(prefix.as_bytes())
                .and_then(|id| std::str::from_utf8(id).ok())
                .and_then(|id| routes.get(id))
                .is_some_and(|route| target == format!("{prefix}{}", route.channel_id).as_bytes())
        {
            return Err(invalid());
        }
    }
    for (n, (key, flag)) in active.iter_entries().enumerate() {
        if n >= 32_768
            || !key.starts_with(prefix.as_bytes())
            || flag != [1]
            || discussions.get(key).is_none()
        {
            return Err(invalid());
        }
    }
    for (n, (key, raw)) in guards.iter_entries().enumerate() {
        if n >= 32_768 || raw.len() > 1 << 20 || !key.starts_with(prefix.as_bytes()) {
            return Err(invalid());
        }
        let row: Guard = serde_json::from_slice(raw).map_err(|_| invalid())?;
        let operation = operation(install, &row.route.message_id)?;
        if key != format!("{prefix}{}", row.route.message_id).as_bytes()
            || !scoped(cfg, &row.route)
            || !snowflake(&row.actor_id)
            || cfg.text("mode") == "personal" && row.actor_id != cfg.text("owner_id")
            || !matches!(row.disposition.as_str(), "sent" | "failed")
            || !matches!(row.last_change.as_str(), "" | "edited" | "deleted")
            || (!row.discussion_id.is_empty()
                && (!routes.get(&row.discussion_id).is_some_and(|binding| {
                    binding.guild_id == row.route.guild_id
                        && binding.channel_id == row.route.channel_id
                }) || row.operation_id != operation
                    || row.revision.len() != 64
                    || !row
                        .revision
                        .bytes()
                        .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))))
            || (row.discussion_id.is_empty()
                && (row.disposition == "sent"
                    || !row.operation_id.is_empty()
                    || !row.revision.is_empty()))
        {
            return Err(invalid());
        }
    }
    for (n, (key, raw)) in meta.iter_entries().enumerate() {
        if n >= 64 || raw.len() > 1 << 20 {
            return Err(invalid());
        }
        if key == b"active_ingress_count" && raw == [0; 8] {
            continue;
        }
        if key == format!("{prefix}checkpoint").as_bytes() {
            let cp: Checkpoint = serde_json::from_slice(raw).map_err(|_| invalid())?;
            if cp.session.len() <= 512
                && cp.gateway_sequence >= 0
                && cp.gap_epoch < u64::MAX
                && cp.error.len() <= 2048
            {
                continue;
            }
        }
        return Err(invalid());
    }
    if [&discussions, &ids, &active, &guards, &meta]
        .iter()
        .any(|bucket| bucket.iter_buckets().next().is_some())
    {
        return Err(invalid());
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn legacy_team_route_requires_enrolled_channel_or_parent() {
        let cfg = Config(json!({"mode":"teammate","guild_ids":["300"],"channel_ids":["401"]}));
        let mut route = Route {
            guild_id: "300".into(),
            channel_id: "402".into(),
            message_id: "500".into(),
            parent_id: "403".into(),
        };
        assert!(!scoped(&cfg, &route));
        route.parent_id = "401".into();
        assert!(scoped(&cfg, &route));
        route.parent_id.clear();
        route.channel_id = "401".into();
        assert!(scoped(&cfg, &route));
    }
}
