//! Verify the current Discord guild audience before creating or rechecking a worker source.
use super::{
    config::{Config, snowflake},
    receipt::Inbox,
    rest::Discord,
    worker,
};
use serde_json::{Value, json};
use std::io;

pub(super) fn enabled(cfg: &Config) -> bool {
    matches!(cfg.text("mode"), "" | "legacy" | "teammate")
        && snowflake(cfg.text("owner_id"))
        && cfg.0["guild_ids"]
            .as_array()
            .is_some_and(|ids| !ids.is_empty())
}

fn allowed(cfg: &Config, key: &str, id: &str) -> bool {
    cfg.0[key]
        .as_array()
        .is_some_and(|values| values.iter().any(|value| value == id))
}

pub(super) fn address(cfg: &Config, message: &Value) -> Option<String> {
    let bot = cfg.text("application_id");
    if message["author"]["id"] != cfg.text("owner_id")
        || !message["mentions"]
            .as_array()
            .is_some_and(|users| users.iter().any(|user| user["id"] == bot))
    {
        return None;
    }
    mentioned_text(cfg, message["content"].as_str()?)
}

fn mentioned_text(cfg: &Config, raw: &str) -> Option<String> {
    let bot = cfg.text("application_id");
    let text = raw
        .replace(&format!("<@{bot}>"), "")
        .replace(&format!("<@!{bot}>"), "");
    if raw == text {
        return None;
    }
    worker::target_text(&text)?;
    Some(text.trim().to_owned())
}

pub(super) fn audience_digest(application: &str, source: &Value) -> io::Result<String> {
    let route = &source["route"];
    super::receipt::hash(&json!([
        application,
        source["author"]["id"],
        route["guild_id"],
        route["channel_id"],
        route["parent_id"],
        route["message_id"],
        source["revision"],
        source["audience_verified_at_ms"],
        source["audience_roles"]
    ]))
}

pub(super) fn authorized_worker_route(cfg: &Config, source: &Value, composer: &Value) -> bool {
    let route = &source["route"];
    let guild = route["guild_id"].as_str().unwrap_or_default();
    let channel = route["channel_id"].as_str().unwrap_or_default();
    let parent = route["parent_id"].as_str().unwrap_or_default();
    let origin = &composer["origin"];
    let now = crate::ipc::spawn::now_ms();
    let sealed = audience_digest(cfg.text("application_id"), source)
        .is_ok_and(|digest| source["guild_audience_digest"] == digest);
    sealed
        && enabled(cfg)
        && snowflake(guild)
        && snowflake(channel)
        && allowed(cfg, "guild_ids", guild)
        && (parent.is_empty() || snowflake(parent) && parent != channel)
        && (!cfg.0["channel_ids"]
            .as_array()
            .is_some_and(|ids| !ids.is_empty())
            || allowed(cfg, "channel_ids", channel)
            || allowed(cfg, "channel_ids", parent))
        && origin["guild_id"] == guild
        && origin["channel_id"] == channel
        && origin["message_id"] == route["message_id"]
        && source["audience_verified_at_ms"]
            .as_u64()
            .is_some_and(|at| at <= now && now - at <= 30_000)
        && route["message_id"]
            .as_str()
            .is_some_and(|id| worker::fresh(id, now))
        && source["text"]
            .as_str()
            .and_then(|raw| mentioned_text(cfg, raw))
            .is_some_and(|rendered| {
                source["addressed_text"] == rendered
                    && worker::target_text(&rendered).is_some_and(|(session, text)| {
                        composer["session"] == session && composer["text"] == text
                    })
            })
}

fn audience(cfg: &Config, guild: &str, id: &str, channel: &Value) -> Option<String> {
    if !enabled(cfg)
        || !snowflake(guild)
        || !snowflake(id)
        || !allowed(cfg, "guild_ids", guild)
        || channel["id"] != id
        || channel["guild_id"] != guild
    {
        return None;
    }
    let kind = channel["type"].as_u64()?;
    let parent = match kind {
        0 => "",
        10..=12 => {
            if !channel["thread_metadata"].is_object() {
                return None;
            }
            channel["parent_id"]
                .as_str()
                .filter(|parent| snowflake(parent) && *parent != id)?
        }
        _ => return None,
    };
    if cfg.0["channel_ids"]
        .as_array()
        .is_some_and(|ids| !ids.is_empty())
        && !allowed(cfg, "channel_ids", id)
        && !allowed(cfg, "channel_ids", parent)
    {
        return None;
    }
    Some(parent.to_owned())
}

pub(super) fn current(
    api: &Discord,
    cfg: &Config,
    guild: &str,
    channel_id: &str,
    id: &str,
) -> io::Result<Option<Value>> {
    scoped_message(cfg, guild, channel_id, id, |path| lookup(api, path))
}

fn scoped_message(
    cfg: &Config,
    guild: &str,
    channel_id: &str,
    id: &str,
    mut get: impl FnMut(&str) -> io::Result<Option<Value>>,
) -> io::Result<Option<Value>> {
    if !enabled(cfg)
        || !snowflake(guild)
        || !snowflake(channel_id)
        || !snowflake(id)
        || !allowed(cfg, "guild_ids", guild)
    {
        return Ok(None);
    }
    let Some(channel) = get(&format!("/channels/{channel_id}"))? else {
        return Ok(None);
    };
    let Some(parent) = audience(cfg, guild, channel_id, &channel) else {
        return Ok(None);
    };
    let permission_channel = if parent.is_empty() {
        channel_id
    } else {
        &parent
    };
    if !parent.is_empty() {
        let Some(row) = get(&format!("/channels/{parent}"))? else {
            return Ok(None);
        };
        if row["id"] != parent
            || row["guild_id"] != guild
            || !matches!(row["type"].as_u64(), Some(0 | 5 | 15 | 16))
        {
            return Ok(None);
        }
    }
    let mut roles = std::collections::BTreeSet::from([guild.to_owned()]);
    let mut known_roles = true;
    for member in [cfg.text("application_id"), cfg.text("owner_id")] {
        let membership = get(&format!("/guilds/{guild}/members/{member}"))?;
        match membership
            .as_ref()
            .filter(|row| row["user"]["id"] == member)
            .and_then(|row| row["roles"].as_array())
            .filter(|ids| ids.iter().all(|id| id.as_str().is_some_and(snowflake)))
        {
            Some(ids) => roles.extend(ids.iter().filter_map(Value::as_str).map(str::to_owned)),
            None => known_roles = false,
        }
        let Some(grant) = get(&format!(
            "/channels/{permission_channel}/permissions/member/{member}"
        ))?
        else {
            return Ok(None);
        };
        let bits = grant["permissions"]
            .as_str()
            .and_then(|value| value.parse::<u64>().ok())
            .unwrap_or(0);
        let admin = bits & (1 << 3) != 0;
        if !admin && bits & ((1 << 10) | (1 << 16)) != ((1 << 10) | (1 << 16)) {
            return Ok(None);
        }
        if channel["type"] == 12 && !admin && bits & (1 << 34) == 0 {
            let Some(row) = get(&format!("/channels/{channel_id}/thread-members/{member}"))? else {
                return Ok(None);
            };
            if row["id"] != channel_id || row["user_id"] != member {
                return Ok(None);
            }
        }
    }
    let Some(mut source) = get(&format!("/channels/{channel_id}/messages/{id}"))? else {
        return Ok(None);
    };
    if source["id"] != id
        || source["channel_id"] != channel_id
        || !source["guild_id"].is_null() && source["guild_id"] != guild
        || address(cfg, &source).is_none()
    {
        return Ok(None);
    }
    source["guild_id"] = json!(guild);
    source["verified_parent_id"] = json!(parent);
    source["audience_verified_at_ms"] = json!(crate::ipc::spawn::now_ms());
    if known_roles {
        source["audience_roles"] = json!(roles);
    }
    Ok(Some(source))
}

pub(super) fn lookup(api: &Discord, path: &str) -> io::Result<Option<Value>> {
    match api.request(path, None) {
        Ok(value) => Ok(Some(value)),
        Err(err)
            if matches!(
                err.kind(),
                io::ErrorKind::NotFound | io::ErrorKind::PermissionDenied
            ) =>
        {
            Ok(None)
        }
        Err(err) => Err(err),
    }
}

pub(super) fn needs_ingest(cfg: &Config, inbox: &Inbox, event: &Value) -> bool {
    let hint = &event["d"];
    let guild = hint["guild_id"].as_str().unwrap_or_default();
    let channel = hint["channel_id"].as_str().unwrap_or_default();
    if !allowed(cfg, "guild_ids", guild) || !snowflake(channel) {
        return false;
    }
    let known = |id| inbox.has_guild_source(guild, channel, id);
    match event["t"].as_str() {
        Some("MESSAGE_CREATE") => address(cfg, hint).is_some(),
        Some("MESSAGE_UPDATE" | "MESSAGE_DELETE") => hint["id"].as_str().is_some_and(known),
        Some("MESSAGE_DELETE_BULK") => hint["ids"]
            .as_array()
            .is_some_and(|ids| ids.iter().filter_map(Value::as_str).any(known)),
        _ => false,
    }
}

pub(super) fn ingest(
    cfg: &Config,
    inbox: &mut Inbox,
    event: &Value,
    now: u64,
    lookup: impl FnOnce(&str, &str, &str) -> io::Result<Option<Value>>,
) -> io::Result<()> {
    let hint = &event["d"];
    let ids = (hint["guild_id"].as_str(), hint["channel_id"].as_str());
    let (Some(guild), Some(channel)) = ids else {
        return Ok(());
    };
    if !enabled(cfg)
        || !snowflake(guild)
        || !snowflake(channel)
        || !allowed(cfg, "guild_ids", guild)
    {
        return Ok(());
    }
    if event["t"] == "MESSAGE_DELETE_BULK" {
        if let Some(ids) = hint["ids"].as_array() {
            for id in ids
                .iter()
                .filter_map(Value::as_str)
                .filter(|id| snowflake(id))
            {
                if inbox.has_guild_source(guild, channel, id) {
                    inbox.withdraw(id)?;
                }
            }
        }
        return Ok(());
    }
    let Some(id) = hint["id"].as_str().filter(|id| snowflake(id)) else {
        return Ok(());
    };
    let prior = inbox.has_guild_source(guild, channel, id);
    match event["t"].as_str() {
        Some("MESSAGE_DELETE") if prior => inbox.withdraw(id),
        Some("MESSAGE_CREATE") if address(cfg, hint).is_some() && worker::fresh(id, now) => {
            if let Some(source) = lookup(guild, channel, id)?
                && source["id"] == id
                && source["guild_id"] == guild
                && source["channel_id"] == channel
            {
                inbox.admit_guild(cfg, &source)?;
            }
            Ok(())
        }
        Some("MESSAGE_UPDATE") if prior => {
            if let Some(source) = lookup(guild, channel, id)?
                && source["id"] == id
                && source["guild_id"] == guild
                && source["channel_id"] == channel
                && inbox.admit_guild(cfg, &source)?.is_some()
            {
                return Ok(());
            }
            inbox.withdraw(id)
        }
        _ => Ok(()),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{ipc::spawn, supervisor::runtime};
    use std::{fs, path::Path};

    const TARGET: &str = "ep-1234abcd/1234567890abcdef12345678";

    fn config() -> Config {
        Config(
            json!({"mode":"teammate","installation_id":"install","application_id":"200",
            "owner_id":"300","guild_ids":["101"],"channel_ids":["111"],"daemon_socket":"/fixture/supervisor"}),
        )
    }

    fn source(now: u64, channel: &str) -> Value {
        let id = ((now - 1_420_070_400_000) << 22).to_string();
        json!({"id":id,"guild_id":"101","channel_id":channel,"content":format!("<@200> {TARGET} inspect the diff"),
            "mentions":[{"id":"200"}],"author":{"id":"300"},"attachments":[],"type":0})
    }

    #[test]
    fn private_thread_requires_exact_current_parent_permissions_and_member_coordinates() {
        let cfg = config();
        let now = spawn::now_ms();
        let request = source(now, "112");
        let id = request["id"].as_str().unwrap();
        let route = |grant: bool, bad_thread: bool| {
            scoped_message(&cfg,"101","112",id,|path| {
            let row=match path {
                "/channels/112" => json!({"id":"112","guild_id":"101","parent_id":"111","type":12,"thread_metadata":{}}),
                "/channels/111" => json!({"id":"111","guild_id":"101","type":0}),
                "/guilds/101/members/200" => json!({"user":{"id":"200"},"roles":["211"]}),
                    "/guilds/101/members/300" => json!({"user":{"id":"300"},"roles":["311"]}),
                    "/channels/111/permissions/member/200" => json!({"permissions":"66560"}),
                "/channels/111/permissions/member/300" => json!({"permissions":if grant {"66560"} else {"0"}}),
                "/channels/112/thread-members/200" => json!({"id":"112","user_id":"200"}),
                "/channels/112/thread-members/300" => json!({"id":"112","user_id":if bad_thread {"999"} else {"300"}}),
                _ if path == format!("/channels/112/messages/{id}") => request.clone(),
                _ => panic!("unexpected Discord read: {path}"),
            };
            Ok(Some(row))
        }).unwrap()
        };

        let verified = route(true, false).unwrap();
        assert_eq!(verified["verified_parent_id"], "111");
        assert!(route(false, false).is_none());
        assert!(route(true, true).is_none());
        let root = Path::new("/tmp").join(format!("guild-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&root).unwrap();
        let mut inbox = Inbox::open(&root, &cfg).unwrap();
        inbox.checkpoint("").unwrap();
        let mut foreign = request.clone();
        foreign["author"]["id"] = json!("301");
        ingest(
            &cfg,
            &mut inbox,
            &json!({"t":"MESSAGE_CREATE","d":foreign}),
            now,
            |_, _, _| panic!("human without owner authority reached scoped source"),
        )
        .unwrap();
        ingest(
            &cfg,
            &mut inbox,
            &json!({"t":"MESSAGE_CREATE","d":request}),
            now,
            |_, _, _| Ok(Some(verified)),
        )
        .unwrap();
        let receipt = inbox.next_worker().unwrap();
        assert_eq!(
            receipt["route"],
            json!({"guild_id":"101","channel_id":"112","parent_id":"111","message_id":id})
        );
        assert_eq!(
            worker::target_text(worker::source_text(&receipt)),
            Some((TARGET, "inspect the diff"))
        );
        assert!(inbox.next_prepared().is_none());
        ingest(
            &cfg,
            &mut inbox,
            &json!({"t":"MESSAGE_DELETE","d":{"guild_id":"101","channel_id":"999","id":id}}),
            now,
            |_, _, _| panic!(),
        )
        .unwrap();
        assert!(inbox.next_worker().is_some());
        ingest(
            &cfg,
            &mut inbox,
            &json!({"t":"MESSAGE_DELETE","d":{"guild_id":"101","channel_id":"112","id":id}}),
            now,
            |_, _, _| panic!(),
        )
        .unwrap();
        assert!(inbox.next_worker().is_none());
        fs::remove_dir_all(root).unwrap();
    }
}
