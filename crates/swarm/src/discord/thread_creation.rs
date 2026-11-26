//! Provision the Discord-defined thread coordinate of one verified parent mention.
use super::{
    config::{Config, invalid, snowflake},
    guild,
    receipt::{Inbox, hash},
    rest::{Discord, Post, ThreadPost},
    team_thread,
};
use serde_json::{Value, json};
use std::io;

pub(super) fn recover(cfg: &Config, inbox: &Inbox, api: &Discord) -> io::Result<Option<Value>> {
    let Some(plan) = inbox.thread_creation() else {
        return Ok(None);
    };
    readback(cfg, plan, |path| strict_lookup(api, path)).map(Readback::route)
}

pub(super) fn ingest(
    cfg: &Config,
    inbox: &mut Inbox,
    api: &Discord,
    event: &Value,
) -> io::Result<Option<Value>> {
    if cfg.text("mode") != "teammate" {
        return Ok(None);
    }
    if let Some(plan) = inbox.thread_creation() {
        return if event["d"]["id"] == plan["message_id"] {
            recover(cfg, inbox, api)
        } else {
            Ok(None)
        };
    }
    if super::team_thread::linked_values(cfg).count() >= 17 {
        return Ok(None);
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
    let Some(source) = authorize(cfg, guild, parent, id, lookup)? else {
        return Ok(None);
    };
    let prior = match readback(cfg, &source.plan, |path| strict_lookup(api, path))? {
        Readback::Present(Some(route)) => Some(route),
        Readback::Missing if source.can_create => None,
        _ => return Ok(None),
    };
    attempt(cfg, inbox, api, &source, prior)
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
    if let Readback::Present(route) = readback(cfg, &plan, |path| strict_lookup(api, path))? {
        if route.is_none() {
            inbox.thread_retry_at(Some(now.saturating_add(60_000)))?;
        }
        return Ok(route);
    }
    let (Some(guild), Some(parent), Some(message)) = (
        id(&plan["guild_id"]),
        id(&plan["parent_id"]),
        id(&plan["message_id"]),
    ) else {
        return Err(invalid("invalid persisted thread retry scope"));
    };
    let source = authorize(cfg, guild, parent, message, lookup)?
        .filter(|current| current.plan == plan && current.can_create);
    let Some(source) = source else {
        inbox.retire_throttled_thread(&plan, now)?;
        return Ok(None);
    };
    if !inbox.thread_retry_due(now) {
        inbox.thread_retry_at(Some(now.saturating_add(60_000)))?;
        return Ok(None);
    }
    attempt(cfg, inbox, api, &source, None)
}

struct Authorized {
    plan: Value,
    title: String,
    can_create: bool,
}

fn authorize(
    cfg: &Config,
    guild: &str,
    parent: &str,
    id: &str,
    lookup: impl Fn(&str) -> io::Result<Option<Value>>,
) -> io::Result<Option<Authorized>> {
    if super::team_thread::linked_values(cfg)
        .any(|route| route["guild_id"] == guild && route["thread_id"] == id)
    {
        return Ok(None);
    }
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
    let capacity = guild::member_permissions(&lookup, parent, bot)?;
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
    let actor_rights = guild::member_permissions(&lookup, parent, actor)?;
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
    Ok(Some(Authorized {
        plan,
        title: opener_title(
            cfg.text("name"),
            source["content"].as_str().unwrap_or_default(),
        ),
        can_create: capacity & ((1 << 3) | (1 << 35)) != 0,
    }))
}

fn default_title(name: &str) -> String {
    clip_title(&format!("{name} discussion")).to_owned()
}

fn opener_title(name: &str, mut source: &str) -> String {
    let mut text = String::with_capacity(source.len());
    while let Some((prefix, tail)) = source.split_once('<') {
        text.push_str(prefix);
        let mention = tail
            .strip_prefix('@')
            .map(|id| id.strip_prefix(['!', '&']).unwrap_or(id))
            .or_else(|| tail.strip_prefix('#'));
        if let Some(mention) = mention {
            let digits = mention.bytes().take_while(u8::is_ascii_digit).count();
            if digits != 0
                && let Some(rest) = mention[digits..].strip_prefix('>')
            {
                text.push(' ');
                source = rest;
                continue;
            }
        }
        text.push('<');
        source = tail;
    }
    text.push_str(source);
    let title = text
        .split_whitespace()
        .take(8)
        .collect::<Vec<_>>()
        .join(" ");
    if title.is_empty() {
        default_title(name)
    } else {
        clip_title(&title).to_owned()
    }
}

fn clip_title(text: &str) -> &str {
    let mut width = 0;
    let end = text.char_indices().find_map(|(end, ch)| {
        width += ch.len_utf16();
        (width > 95).then_some(end)
    });
    &text[..end.unwrap_or(text.len())]
}

fn attempt(
    cfg: &Config,
    inbox: &mut Inbox,
    api: &Discord,
    source: &Authorized,
    prior: Option<Value>,
) -> io::Result<Option<Value>> {
    let plan = &source.plan;
    let guild = id(&plan["guild_id"]).ok_or_else(|| invalid("invalid thread guild"))?;
    let parent = id(&plan["parent_id"]).ok_or_else(|| invalid("invalid thread parent"))?;
    let message = id(&plan["message_id"]).ok_or_else(|| invalid("invalid thread opener"))?;
    provision(
        inbox,
        plan,
        prior,
        crate::ipc::spawn::now_ms,
        |fallback| api.create_public_thread(parent, message, fallback.unwrap_or(&source.title)),
        || readback(cfg, plan, |path| strict_lookup(api, path)),
        || {
            let authorized =
                authorize(cfg, guild, parent, message, |path| guild::lookup(api, path))?
                    .is_some_and(|current| current.plan == *plan && current.can_create);
            Ok(authorized.then(|| default_title(cfg.text("name"))))
        },
    )
}

fn provision(
    inbox: &mut Inbox,
    plan: &Value,
    prior: Option<Value>,
    clock: impl Fn() -> u64,
    mut post: impl FnMut(Option<&str>) -> ThreadPost,
    read: impl Fn() -> io::Result<Readback>,
    fallback: impl FnOnce() -> io::Result<Option<String>>,
) -> io::Result<Option<Value>> {
    let now = clock();
    match inbox.thread_creation() {
        None => inbox.reserve_thread(plan.clone())?,
        Some(saved) if saved == plan && inbox.thread_retry_due(now) => {
            inbox.start_thread_retry(now)?
        }
        Some(_) => return read().map(Readback::route),
    }
    if prior.is_some() {
        return Ok(prior);
    }
    let result = match post(inbox.thread_fallback()) {
        ThreadPost::BadRequest if inbox.thread_fallback().is_none() => {
            if let Readback::Present(route) = read()? {
                return Ok(route);
            }
            let Some(name) = fallback()? else {
                return Ok(None);
            };
            // A definitive throttle may retry this exact fallback name after restart.
            inbox.choose_thread_fallback(plan, &name)?;
            post(Some(&name))
        }
        other => other,
    };
    match result {
        ThreadPost::Outcome(Post::Acknowledged(observed)) if !exact(&observed, plan) => {
            return Err(invalid("Discord create response named a different thread"));
        }
        ThreadPost::Outcome(Post::RateLimited(delay)) => {
            inbox.record_thread_throttle(
                clock().saturating_add(delay.max(1)),
                (1..=120_000).contains(&delay),
            )?;
            return Ok(None);
        }
        _ => {}
    }
    read().map(Readback::route)
}

enum Readback {
    Missing,
    Present(Option<Value>),
}

impl Readback {
    fn route(self) -> Option<Value> {
        match self {
            Self::Missing => None,
            Self::Present(route) => route,
        }
    }
}

fn strict_lookup(api: &Discord, path: &str) -> io::Result<Option<Value>> {
    match api.request(path, None) {
        Ok(value) => Ok(Some(value)),
        Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(None),
        Err(error) => Err(error),
    }
}

fn readback(
    cfg: &Config,
    plan: &Value,
    mut lookup: impl FnMut(&str) -> io::Result<Option<Value>>,
) -> io::Result<Readback> {
    let (Some(guild), Some(parent), Some(message), Some(actor)) = (
        id(&plan["guild_id"]),
        id(&plan["parent_id"]),
        id(&plan["message_id"]),
        id(&plan["actor_id"]),
    ) else {
        return Err(invalid("invalid persisted thread creation binding"));
    };
    let coordinate = format!("/channels/{message}");
    let channel = match lookup(&coordinate) {
        Ok(Some(channel)) => channel,
        Ok(None) => return Ok(Readback::Missing),
        Err(error) if error.kind() == io::ErrorKind::PermissionDenied => {
            return Ok(Readback::Present(None));
        }
        Err(error) => return Err(error),
    };
    if !exact(&channel, plan) {
        return Err(invalid("Discord thread readback named another audience"));
    }
    let route = team_thread::Route {
        guild: guild.into(),
        thread: message.into(),
        starter: None,
    };
    let verified = match route.verify(
        cfg,
        |path| {
            if path == coordinate {
                Ok(Some(channel.clone()))
            } else {
                lookup(path)
            }
        },
        &[actor],
    ) {
        Ok(verified) => verified,
        Err(error) if error.kind() == io::ErrorKind::PermissionDenied => false,
        Err(error) => return Err(error),
    };
    Ok(Readback::Present(verified.then(|| {
        json!({"guild_id":guild,"thread_id":message,"starter_channel_id":parent,
               "starter_revision":plan["revision"]})
    })))
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
#[cfg(test)]
mod tests {
    use super::*;
    use crate::supervisor::runtime;
    use std::cell::Cell;

    #[test]
    fn opener_names_match_go_fields_mentions_and_utf16_limit() {
        let long = "x".repeat(120);
        for (source, title) in [
            (
                "<@99> check why staging fund page is slow",
                "check why staging fund page is slow",
            ),
            (
                "<@!99>   look at\n<@&5> the <#20> logs  ",
                "look at the logs",
            ),
            (
                "<@99> one two three four five six seven eight nine ten",
                "one two three four five six seven eight",
            ),
            (
                "a<@2>b\u{00a0}<@not-a-mention>\u{2028}c",
                "a b <@not-a-mention> c",
            ),
            ("<@99>", "Claudio discussion"),
            ("   ", "Claudio discussion"),
            (&long, &long[..95]),
        ] {
            assert_eq!(opener_title("Claudio", source), title, "{source:?}");
        }
        let long = "x".repeat(94);
        assert_eq!(opener_title("Claudio", &format!("{long}😀x")), long);
        let emoji = "😀".repeat(47);
        assert_eq!(
            opener_title("Claudio", &format!("{emoji}x😀")),
            format!("{emoji}x")
        );
        assert_eq!(opener_title(&format!("{emoji}😀"), ""), emoji);
    }

    #[test]
    fn authenticated_create_fallback_obeys_current_rest_audience_and_source() {
        use std::{
            io::{BufRead, BufReader, Read, Write},
            net::TcpListener,
            sync::{
                Arc,
                atomic::{AtomicBool, Ordering},
            },
            thread,
            time::Duration,
        };

        #[derive(Clone, Copy, Debug, PartialEq)]
        enum Scenario {
            Named,
            Fallback,
            BlankDefault,
            FallbackRejected,
            Forbidden,
            NoResponse,
            AlreadyExists,
            FoundAfter400,
            LostAck,
            WrongAudience,
            DeniedReadback,
            Edited,
            Deleted,
            Revoked,
        }
        use Scenario::*;

        let title = "first two three four five six seven eight";
        let default = "Claudio discussion";
        for (scenario, names, linked) in [
            (Named, vec![title], true),
            (Fallback, vec![title, default], true),
            (BlankDefault, vec![default, default], true),
            (FallbackRejected, vec![title, default], false),
            (Forbidden, vec![title], false),
            (NoResponse, vec![title], false),
            (AlreadyExists, vec![], true),
            (FoundAfter400, vec![title], true),
            (LostAck, vec![title], true),
            (WrongAudience, vec![title], false),
            (DeniedReadback, vec![title], false),
            (Edited, vec![title], false),
            (Deleted, vec![title], false),
            (Revoked, vec![title], false),
        ] {
            let dir = std::env::temp_dir().join(format!(
                "swarm-thread-names-{}",
                runtime::random_id().unwrap()
            ));
            runtime::private_dir(&dir).unwrap();
            let token = dir.join("token");
            runtime::write_atomic(&token, |file| file.write_all(b"fixture-token")).unwrap();
            let cfg = Config(json!({"mode":"teammate","installation_id":"fixture",
                "application_id":"3","owner_id":"4","guild_ids":["1"],
                "name":"Claudio","token_file":token}));
            let listener = TcpListener::bind("127.0.0.1:0").unwrap();
            listener.set_nonblocking(true).unwrap();
            let api =
                Discord::loopback_fixture(&cfg, listener.local_addr().unwrap().port()).unwrap();
            let stop = Arc::new(AtomicBool::new(false));
            let done = Arc::clone(&stop);
            let fixture = thread::spawn(move || {
                let mut posts = Vec::new();
                let exact = json!({"id":"9","type":11,"guild_id":"1","parent_id":"2",
                    "thread_metadata":{"archived":false,"locked":false}});
                while !done.load(Ordering::Relaxed) {
                    let mut socket = match listener.accept() {
                        Ok((socket, _)) => socket,
                        Err(error) if error.kind() == io::ErrorKind::WouldBlock => {
                            thread::sleep(Duration::from_millis(1));
                            continue;
                        }
                        Err(error) => panic!("accept {error}"),
                    };
                    socket.set_nonblocking(false).unwrap();
                    socket
                        .set_read_timeout(Some(Duration::from_secs(1)))
                        .unwrap();
                    let mut reader = BufReader::new(&mut socket);
                    let mut line = String::new();
                    reader.read_line(&mut line).unwrap();
                    let (request, _) = line.trim_end().rsplit_once(' ').unwrap();
                    let request = request.to_owned();
                    let mut size = 0;
                    let mut authorized = false;
                    loop {
                        line.clear();
                        assert_ne!(reader.read_line(&mut line).unwrap(), 0);
                        if line == "\r\n" {
                            break;
                        }
                        let (name, value) = line.split_once(':').unwrap();
                        if name.eq_ignore_ascii_case("authorization") {
                            authorized = value.trim() == "Bot fixture-token";
                        }
                        if name.eq_ignore_ascii_case("content-length") {
                            size = value.trim().parse().unwrap();
                        }
                    }
                    assert!(authorized, "{request}");
                    let first_attempted = !posts.is_empty();
                    let (status, body) = match request.as_str() {
                        "POST /api/v10/channels/2/messages/9/threads" => {
                            let mut bytes = vec![0; size];
                            reader.read_exact(&mut bytes).unwrap();
                            let body: Value = serde_json::from_slice(&bytes).unwrap();
                            posts.push(body["name"].as_str().unwrap().to_owned());
                            match scenario {
                                Named => (200, exact.clone()),
                                Fallback | BlankDefault if first_attempted => (200, exact.clone()),
                                Forbidden => (403, json!({})),
                                NoResponse | LostAck => continue,
                                _ => (400, json!({})),
                            }
                        }
                        "GET /api/v10/channels/9" => match scenario {
                            AlreadyExists => (200, exact.clone()),
                            Named | FoundAfter400 | LostAck if first_attempted => {
                                (200, exact.clone())
                            }
                            Fallback | BlankDefault if posts.len() == 2 => (200, exact.clone()),
                            WrongAudience if first_attempted => {
                                let mut wrong = exact.clone();
                                wrong["parent_id"] = json!("99");
                                (200, wrong)
                            }
                            DeniedReadback if first_attempted => (403, json!({})),
                            _ => (404, json!({})),
                        },
                        "GET /api/v10/channels/2" => {
                            (200, json!({"id":"2","guild_id":"1","type":0}))
                        }
                        "GET /api/v10/channels/2/messages/9" => {
                            if scenario == Deleted && first_attempted {
                                (404, json!({}))
                            } else {
                                let content = match scenario {
                                    Edited if first_attempted => "<@!3> edited opener",
                                    BlankDefault => "<@!3> <#20>",
                                    _ => {
                                        "<@!3> first  two <@&5> three four\n five <#6> six seven eight nine"
                                    }
                                };
                                (
                                    200,
                                    json!({"id":"9","channel_id":"2","guild_id":"1","type":0,
                                    "author":{"id":"7"},"content":content,"mentions":[{"id":"3"}]}),
                                )
                            }
                        }
                        "GET /api/v10/guilds/1/members/3" => {
                            (200, json!({"user":{"id":"3","bot":true}}))
                        }
                        "GET /api/v10/guilds/1/members/7" => (200, json!({"user":{"id":"7"}})),
                        "GET /api/v10/channels/2/permissions/member/3"
                            if scenario == Revoked && first_attempted =>
                        {
                            let permissions = (1u64 << 10) | (1 << 16) | (1 << 38);
                            (200, json!({"permissions":permissions.to_string()}))
                        }
                        "GET /api/v10/channels/2/permissions/member/3"
                        | "GET /api/v10/channels/2/permissions/member/7" => {
                            (200, json!({"permissions":"8"}))
                        }
                        _ => panic!("unexpected request {request}"),
                    };
                    write!(socket, "HTTP/1.1 {status} Fixture\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}", body.to_string().len()).unwrap();
                }
                posts
            });
            let gateway = json!({"t":"MESSAGE_CREATE","d":{"id":"9","channel_id":"2",
                "guild_id":"1","author":{"id":"7"},"content":"<@3> gateway hint",
                "mentions":[{"id":"3"}]}});
            let mut inbox = Inbox::open(&dir, &cfg).unwrap();
            let first = ingest(&cfg, &mut inbox, &api, &gateway);
            drop(inbox);
            let mut inbox = Inbox::open(&dir, &cfg).unwrap();
            let replay = ingest(&cfg, &mut inbox, &api, &gateway);
            stop.store(true, Ordering::Relaxed);
            assert_eq!(fixture.join().unwrap(), names, "{scenario:?}");
            for result in [first, replay] {
                if scenario == WrongAudience {
                    assert_eq!(result.unwrap_err().kind(), io::ErrorKind::InvalidInput);
                } else {
                    let route = result.unwrap();
                    assert_eq!(route.is_some(), linked, "{scenario:?}");
                    if let Some(route) = route {
                        assert_eq!(route["thread_id"], "9");
                        assert_eq!(route["starter_channel_id"], "2");
                        assert_eq!(
                            route["starter_revision"],
                            inbox.thread_creation().unwrap()["revision"]
                        );
                    }
                }
            }
            std::fs::remove_dir_all(dir).unwrap();
        }
    }

    #[test]
    fn fallback_throttles_keep_the_durable_name_and_existing_retry_budget() {
        let dir = std::env::temp_dir().join(format!(
            "swarm-thread-fallback-rate-{}",
            runtime::random_id().unwrap()
        ));
        runtime::private_dir(&dir).unwrap();
        let cfg = Config(
            json!({"mode":"teammate","installation_id":"test","application_id":"3","owner_id":"4"}),
        );
        let plan = json!({"guild_id":"1","parent_id":"2","message_id":"9","actor_id":"4","revision":"a".repeat(64)});
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        let calls = Cell::new(0);
        let name = "Claudio discussion";
        provision(
            &mut inbox,
            &plan,
            None,
            || 100,
            |chosen| {
                calls.set(calls.get() + 1);
                if calls.get() == 1 {
                    assert_eq!(chosen, None);
                    ThreadPost::BadRequest
                } else {
                    assert_eq!(chosen, Some(name));
                    let state = runtime::read_json(&dir.join("rust-text-v1.json")).unwrap();
                    assert_eq!(state["thread_creation"], plan);
                    assert_eq!(state["thread_create_fallback"], name);
                    ThreadPost::Outcome(Post::RateLimited(200))
                }
            },
            || Ok(Readback::Missing),
            || Ok(Some(name.into())),
        )
        .unwrap();
        assert_eq!(calls.get(), 2);
        drop(inbox);
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        for now in [300, 500] {
            assert!(inbox.thread_retry_due(now));
            provision(
                &mut inbox,
                &plan,
                None,
                || now,
                |chosen| {
                    assert_eq!(chosen, Some(name));
                    let state = runtime::read_json(&dir.join("rust-text-v1.json")).unwrap();
                    assert!(state["thread_create_retry_at"].is_null());
                    ThreadPost::Outcome(Post::RateLimited(200))
                },
                || panic!("definitive throttle"),
                || panic!("fallback was already chosen"),
            )
            .unwrap();
        }
        assert!(!inbox.thread_retry_due(u64::MAX));
        provision(
            &mut inbox,
            &plan,
            None,
            || u64::MAX,
            |_| panic!("retry budget exhausted"),
            || Ok(Readback::Missing),
            || panic!("fallback was already chosen"),
        )
        .unwrap();
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn proven_creation_installs_successive_threads_and_releases_only_the_exact_plan() {
        use super::super::config::Place;
        let dir = std::env::temp_dir().join(format!(
            "swarm-thread-link-more-{}",
            runtime::random_id().unwrap()
        ));
        runtime::private_dir(&dir).unwrap();
        let mut cfg = Config(
            json!({"version":1,"enabled":true,"mode":"teammate","manager_cli":"codex",
            "installation_id":"fixture","application_id":"3","owner_id":"4","name":"fixture","guild_ids":["1"],
            "token_file":dir.join("token"),"daemon_socket":dir.join("daemon"),"work_dir":dir,
            "team_discussion":{"guild_id":"1","thread_id":"5"}}),
        );
        runtime::write_json(&dir.join("config.json"), &cfg.0).unwrap();
        runtime::write_json(&dir.join(".rust-offline-owner.json"), &json!("fixture")).unwrap();
        let place = Place::fixture(dir.clone());
        for id in ["9", "10"] {
            let mut inbox = Inbox::open(&dir, &cfg).unwrap();
            let revision = "a".repeat(64);
            let plan = json!({"guild_id":"1","parent_id":"2","message_id":id,"actor_id":"4","revision":revision});
            let route = json!({"guild_id":"1","thread_id":id,"starter_channel_id":"2","starter_revision":revision});
            inbox.reserve_thread(plan).unwrap();
            assert!(inbox.finish_thread_creation(&cfg).is_err());
            cfg = place.link_automatic(&cfg, route).unwrap();
            drop(inbox);
            // Equivalent to the process dying between publishing the owner config
            // and retiring the in-progress creation in the ledger.
            let mut recovered = Inbox::open(&dir, &cfg).unwrap();
            assert!(recovered.thread_creation_linked(&cfg));
            recovered.finish_thread_creation(&cfg).unwrap();
            assert!(recovered.thread_creation().is_none());
        }
        assert_eq!(cfg.0["team_discussions"].as_array().unwrap().len(), 2);
        assert_eq!(cfg.0["team_discussion"]["thread_id"], "5");
        std::fs::remove_dir_all(dir).unwrap();
    }

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
            |_| ThreadPost::Outcome(Post::RateLimited(200)),
            || panic!("definitive throttle"),
            || panic!("unexpected fallback"),
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
            |_| ThreadPost::Outcome(Post::Unconfirmed),
            || Ok(Readback::Missing),
            || panic!("unexpected fallback"),
        )
        .unwrap();
        assert!(inbox.retire_throttled_thread(&another, u64::MAX).is_err());
        provision(
            &mut inbox,
            &plan,
            None,
            || 302,
            |_| panic!("uncertain create still controls slot"),
            || Ok(Readback::Missing),
            || panic!("unexpected fallback"),
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
            |_| {
                clock.set(125);
                ThreadPost::Outcome(Post::RateLimited(200))
            },
            || panic!("known refusal"),
            || panic!("unexpected fallback"),
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
            |_| panic!("other message retry"),
            || Ok(Readback::Missing),
            || panic!("unexpected fallback"),
        )
        .unwrap();
        provision(
            &mut inbox,
            &plan,
            None,
            || 325,
            |_| {
                let state = runtime::read_json(&dir.join("rust-text-v1.json")).unwrap();
                assert!(state["thread_create_retry_at"].is_null());
                assert_eq!(state["thread_create_attempts"], 2);
                ThreadPost::Outcome(Post::Unconfirmed)
            },
            || Ok(Readback::Missing),
            || panic!("unexpected fallback"),
        )
        .unwrap();
        assert!(!inbox.thread_retry_due(u64::MAX));
        provision(
            &mut inbox,
            &plan,
            None,
            || u64::MAX,
            |_| panic!("uncertain writes may not repeat"),
            || Ok(Readback::Missing),
            || panic!("unexpected fallback"),
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
            |_| {
                assert_eq!(
                    runtime::read_json(&dir.join("rust-text-v1.json")).unwrap()["thread_creation"],
                    plan
                );
                create_calls.set(create_calls.get() + 1);
                ThreadPost::Outcome(Post::Unconfirmed)
            },
            || Ok(Readback::Missing),
            || panic!("unexpected fallback"),
        )
        .unwrap();
        assert!(result.is_none());
        drop(inbox);
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        let route = provision(&mut inbox,&plan,None,||120_001,|_|panic!("uncertain writes may not repeat"),|| {
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
        }, || panic!("unexpected fallback")).unwrap().unwrap();
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
