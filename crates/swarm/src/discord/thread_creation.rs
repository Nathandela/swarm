//! Provision the Discord-defined thread coordinate of one verified parent mention.
use super::{
    config::{Config, invalid, snowflake},
    guild,
    receipt::{Inbox, hash},
    rest::{Discord, Post},
    team_thread,
};
use serde_json::{Value, json};
use std::io;

pub(super) fn recover(cfg: &Config, inbox: &Inbox, api: &Discord) -> io::Result<Option<Value>> {
    let Some(plan) = inbox.thread_creation() else {
        return Ok(None);
    };
    readback(cfg, plan, |path| guild::lookup(api, path))
}

pub(super) fn ingest(
    cfg: &Config,
    inbox: &mut Inbox,
    api: &Discord,
    event: &Value,
) -> io::Result<Option<Value>> {
    if cfg.text("mode") != "teammate" || cfg.text("project_access") == "vm" {
        return Ok(None);
    }
    if let Some(plan) = inbox.thread_creation() {
        return if event["d"]["id"] == plan["message_id"] {
            recover(cfg, inbox, api)
        } else {
            Ok(None)
        };
    }
    let message = &event["d"];
    if event["t"] != "MESSAGE_CREATE"
        || !team_thread::explicit(cfg, message)
        || guild::address(cfg, message).is_some()
    {
        return Ok(None);
    }
    let (Some(guild), Some(parent), Some(id)) = (
        id(&message["guild_id"]),
        id(&message["channel_id"]),
        id(&message["id"]),
    ) else {
        return Ok(None);
    };
    let lookup = |path: &str| guild::lookup(api, path);
    let Some((plan, can_create)) = authorize(cfg, guild, parent, id, lookup)? else {
        return Ok(None);
    };
    let prior = readback(cfg, &plan, lookup)?;
    if prior.is_none() && !can_create {
        return Ok(None);
    }
    attempt(cfg, inbox, api, &plan, prior)
}

pub(super) fn retry_due(
    cfg: &Config,
    inbox: &mut Inbox,
    api: &Discord,
) -> io::Result<Option<Value>> {
    let now = crate::ipc::spawn::now_ms();
    if !inbox.thread_throttle_due(now) {
        return Ok(None);
    }
    let plan = inbox.thread_creation().unwrap().clone();
    let lookup = |path: &str| guild::lookup(api, path);
    if let Some(route) = readback(cfg, &plan, lookup)? {
        return Ok(Some(route));
    }
    let (Some(guild), Some(parent), Some(message)) = (
        id(&plan["guild_id"]),
        id(&plan["parent_id"]),
        id(&plan["message_id"]),
    ) else {
        return Err(invalid("invalid persisted thread retry scope"));
    };
    if !authorize(cfg, guild, parent, message, lookup)?
        .is_some_and(|(current, can_create)| current == plan && can_create)
    {
        inbox.retire_throttled_thread(&plan, now)?;
        return Ok(None);
    }
    if !inbox.thread_retry_due(now) {
        inbox.thread_retry_at(Some(now.saturating_add(60_000)))?;
        return Ok(None);
    }
    attempt(cfg, inbox, api, &plan, None)
}

fn authorize(
    cfg: &Config,
    guild: &str,
    parent: &str,
    id: &str,
    lookup: impl Fn(&str) -> io::Result<Option<Value>>,
) -> io::Result<Option<(Value, bool)>> {
    if !allowed(cfg, "guild_ids", guild)
        || cfg.0["channel_ids"]
            .as_array()
            .is_some_and(|ids| !ids.is_empty() && !allowed(cfg, "channel_ids", parent))
    {
        return Ok(None);
    }
    let Some(channel) = lookup(&format!("/channels/{parent}"))? else {
        return Ok(None);
    };
    if channel["type"] != 0 || channel["id"] != parent || channel["guild_id"] != guild {
        return Ok(None);
    }
    let bot = cfg.text("application_id");
    let capacity = permissions(&lookup, parent, bot)?;
    const RIGHTS: u64 = (1 << 10) | (1 << 16) | (1 << 38);
    if capacity & (1 << 3) == 0 && capacity & RIGHTS != RIGHTS {
        return Ok(None);
    }
    let Some(source) = lookup(&format!("/channels/{parent}/messages/{id}"))? else {
        return Ok(None);
    };
    let Some(actor) =
        super::thread_creation::id(&source["author"]["id"]).filter(|actor| *actor != bot)
    else {
        return Ok(None);
    };
    if source["id"] != id
        || source["channel_id"] != parent
        || !source["guild_id"].is_null() && source["guild_id"] != guild
        || source["author"]["bot"] == true
        || source["author"]["system"] == true
        || !source["webhook_id"].is_null()
        || source["type"] != 0
        || !team_thread::explicit(cfg, &source)
    {
        return Ok(None);
    }
    let actor_rights = permissions(&lookup, parent, actor)?;
    const ACTOR: u64 = (1 << 10) | (1 << 11) | (1 << 16);
    if actor_rights & (1 << 3) == 0 && actor_rights & ACTOR != ACTOR {
        return Ok(None);
    }
    let revision = match revision(&source) {
        Ok(revision) => revision,
        Err(err) if err.kind() == io::ErrorKind::InvalidInput => return Ok(None),
        Err(err) => return Err(err),
    };
    let plan = json!({"guild_id":guild,"parent_id":parent,"message_id":id,"actor_id":actor,
        "revision":revision});
    Ok(Some((plan, capacity & ((1 << 3) | (1 << 35)) != 0)))
}

fn attempt(
    cfg: &Config,
    inbox: &mut Inbox,
    api: &Discord,
    plan: &Value,
    prior: Option<Value>,
) -> io::Result<Option<Value>> {
    let parent = id(&plan["parent_id"]).ok_or_else(|| invalid("invalid thread parent"))?;
    let message = id(&plan["message_id"]).ok_or_else(|| invalid("invalid thread opener"))?;
    let name = format!(
        "{} discussion",
        cfg.text("name").chars().take(40).collect::<String>()
    );
    provision(
        inbox,
        plan,
        prior,
        crate::ipc::spawn::now_ms,
        || api.create_public_thread(parent, message, &name),
        || readback(cfg, plan, |path| guild::lookup(api, path)),
    )
}

fn provision(
    inbox: &mut Inbox,
    plan: &Value,
    prior: Option<Value>,
    clock: impl Fn() -> u64,
    post: impl FnOnce() -> Post,
    read: impl FnOnce() -> io::Result<Option<Value>>,
) -> io::Result<Option<Value>> {
    let now = clock();
    match inbox.thread_creation() {
        None => inbox.reserve_thread(plan.clone())?,
        Some(saved) if saved == plan && inbox.thread_retry_due(now) => {
            inbox.start_thread_retry(now)?
        }
        Some(_) => return read(),
    }
    if prior.is_some() {
        return Ok(prior);
    }
    match post() {
        Post::Acknowledged(observed) if !exact(&observed, plan) => {
            return Err(invalid("Discord create response named a different thread"));
        }
        Post::RateLimited(delay) => {
            inbox.record_thread_throttle(
                clock().saturating_add(delay.max(1)),
                (1..=120_000).contains(&delay),
            )?;
            return Ok(None);
        }
        _ => {}
    }
    read()
}

fn readback(
    cfg: &Config,
    plan: &Value,
    mut lookup: impl FnMut(&str) -> io::Result<Option<Value>>,
) -> io::Result<Option<Value>> {
    let (Some(guild), Some(parent), Some(message), Some(actor)) = (
        id(&plan["guild_id"]),
        id(&plan["parent_id"]),
        id(&plan["message_id"]),
        id(&plan["actor_id"]),
    ) else {
        return Err(invalid("invalid persisted thread creation binding"));
    };
    let Some(channel) = lookup(&format!("/channels/{message}"))? else {
        return Ok(None);
    };
    if !exact(&channel, plan) {
        return Err(invalid("Discord thread readback named another audience"));
    }
    let route = team_thread::Route {
        guild: guild.into(),
        thread: message.into(),
        starter: None,
    };
    if !route.verify(cfg, &mut lookup, &[actor])? {
        return Ok(None);
    }
    Ok(Some(
        json!({"guild_id":guild,"thread_id":message,"starter_channel_id":parent,
        "starter_revision":plan["revision"]}),
    ))
}

fn exact(channel: &Value, plan: &Value) -> bool {
    channel["id"] == plan["message_id"]
        && channel["guild_id"] == plan["guild_id"]
        && channel["parent_id"] == plan["parent_id"]
        && channel["type"] == 11
}
pub(super) fn revision(source: &Value) -> io::Result<String> {
    let attachments = source["attachments"].as_array().map(|items| {
        items
            .iter()
            .map(|item| {
                json!([
                    item["id"],
                    item["filename"],
                    item["size"],
                    item["content_type"]
                ])
            })
            .collect::<Vec<_>>()
    });
    let original = hash(&json!([
        source["id"],
        source["channel_id"],
        source["author"]["id"],
        source["content"],
        source["edited_timestamp"],
        attachments
    ]))?;
    if source["message_reference"]["type"] == 1 {
        return hash(&json!([
            original,
            super::received_files::visible(source)?.revision()?
        ]));
    }
    Ok(original)
}
fn id(value: &Value) -> Option<&str> {
    value.as_str().filter(|id| snowflake(id))
}
fn allowed(cfg: &Config, key: &str, id: &str) -> bool {
    cfg.0[key]
        .as_array()
        .is_some_and(|rows| rows.iter().any(|entry| entry == id))
}
fn permissions(
    lookup: impl FnOnce(&str) -> io::Result<Option<Value>>,
    channel: &str,
    actor: &str,
) -> io::Result<u64> {
    Ok(
        lookup(&format!("/channels/{channel}/permissions/member/{actor}"))?
            .and_then(|bits| bits["permissions"].as_str()?.parse().ok())
            .unwrap_or(0),
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::supervisor::runtime;
    use std::cell::Cell;

    #[test]
    fn revoked_throttled_opener_releases_the_slot_but_uncertain_write_cannot() {
        let dir = std::env::temp_dir().join(format!(
            "swarm-thread-revoke-{}",
            runtime::random_id().unwrap()
        ));
        runtime::private_dir(&dir).unwrap();
        let cfg = Config(
            json!({"mode":"teammate","installation_id":"test","application_id":"3","owner_id":"4"}),
        );
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        let plan = json!({"guild_id":"1","parent_id":"2","message_id":"9","actor_id":"4","revision":"a".repeat(64)});
        provision(
            &mut inbox,
            &plan,
            None,
            || 100,
            || Post::RateLimited(200),
            || panic!("definitive throttle"),
        )
        .unwrap();
        assert!(inbox.retire_throttled_thread(&plan, 299).is_err());
        let mut another = plan.clone();
        another["message_id"] = json!("10");
        assert!(inbox.retire_throttled_thread(&another, 300).is_err());
        inbox.retire_throttled_thread(&plan, 300).unwrap();
        drop(inbox);
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        assert!(inbox.thread_creation().is_none());
        provision(
            &mut inbox,
            &another,
            None,
            || 301,
            || Post::Unconfirmed,
            || Ok(None),
        )
        .unwrap();
        assert!(inbox.retire_throttled_thread(&another, u64::MAX).is_err());
        provision(
            &mut inbox,
            &plan,
            None,
            || 302,
            || panic!("uncertain create still controls slot"),
            || Ok(None),
        )
        .unwrap();
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn definitive_cooldown_retry_is_durable_and_switches_to_read_only_on_lost_reply() {
        let dir = std::env::temp_dir().join(format!(
            "swarm-thread-rate-{}",
            runtime::random_id().unwrap()
        ));
        runtime::private_dir(&dir).unwrap();
        let cfg = Config(
            json!({"mode":"teammate","installation_id":"test","application_id":"3","owner_id":"4"}),
        );
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        let plan = json!({"guild_id":"1","parent_id":"2","message_id":"9","actor_id":"4","revision":"a".repeat(64)});
        let clock = Cell::new(100);
        provision(
            &mut inbox,
            &plan,
            None,
            || clock.get(),
            || {
                clock.set(125);
                Post::RateLimited(200)
            },
            || panic!("known refusal"),
        )
        .unwrap();
        drop(inbox);
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        assert!(!inbox.thread_retry_due(324));
        assert!(inbox.thread_retry_due(325));
        let mut other = plan.clone();
        other["message_id"] = json!("10");
        provision(
            &mut inbox,
            &other,
            None,
            || 325,
            || panic!("other message retry"),
            || Ok(None),
        )
        .unwrap();
        provision(
            &mut inbox,
            &plan,
            None,
            || 325,
            || {
                let state = runtime::read_json(&dir.join("rust-text-v1.json")).unwrap();
                assert!(state["thread_create_retry_at"].is_null());
                assert_eq!(state["thread_create_attempts"], 2);
                Post::Unconfirmed
            },
            || Ok(None),
        )
        .unwrap();
        assert!(!inbox.thread_retry_due(u64::MAX));
        provision(
            &mut inbox,
            &plan,
            None,
            || u64::MAX,
            || panic!("uncertain writes may not repeat"),
            || Ok(None),
        )
        .unwrap();
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn lost_thread_creation_receipt_is_read_back_without_another_write() {
        let dir =
            std::env::temp_dir().join(format!("swarm-thread-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&dir).unwrap();
        let cfg = Config(
            json!({"mode":"teammate","installation_id":"place","application_id":"3","owner_id":"4","guild_ids":["1"]}),
        );
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        let plan = json!({"guild_id":"1","parent_id":"2","message_id":"9","actor_id":"4","revision":"a".repeat(64)});
        let create_calls = Cell::new(0);
        let result = provision(
            &mut inbox,
            &plan,
            None,
            || 1,
            || {
                assert_eq!(
                    runtime::read_json(&dir.join("rust-text-v1.json")).unwrap()["thread_creation"],
                    plan
                );
                create_calls.set(create_calls.get() + 1);
                Post::Unconfirmed
            },
            || Ok(None),
        )
        .unwrap();
        assert!(result.is_none());
        drop(inbox);
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        let route = provision(&mut inbox,&plan,None,||120_001,||panic!("uncertain writes may not repeat"),|| {
            readback(&cfg,&plan,|path| {
                let value=match path {
                    "/channels/9"=>json!({"type":11,"id":"9","guild_id":"1","parent_id":"2","thread_metadata":{"archived":false,"locked":false}}),
                    "/channels/2"=>json!({"type":0,"id":"2","guild_id":"1"}),
                    "/guilds/1/members/3"=>json!({"user":{"id":"3","bot":true}}),
                    "/guilds/1/members/4"=>json!({"user":{"id":"4"}}),
                    _ if path.starts_with("/channels/2/permissions/member/")=>json!({"permissions":"8"}),
                    _=>panic!("unexpected scoped lookup {path}"),
                };
                Ok(Some(value))
            })
        }).unwrap().unwrap();
        assert_eq!(create_calls.get(), 1);
        assert_eq!(route["thread_id"], "9");
        assert_eq!(route["starter_channel_id"], "2");
        assert!(!exact(
            &json!({"type":11,"id":"9","guild_id":"1","parent_id":"8"}),
            &plan
        ));
        std::fs::remove_dir_all(dir).unwrap();
    }
}
