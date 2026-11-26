//! Render Go's bounded initial history for one plain retained public thread.
use super::*;
use crate::{
    discord::legacy_ingress::prepare::{DISCUSSION_INSTRUCTIONS, go_bytes, human_instruction},
    ipc::field,
};
use std::os::unix::fs::MetadataExt;
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

fn unavailable() -> io::Error {
    invalid("original teammate input has unqualified starter or thread history")
}

#[derive(PartialEq)]
pub(super) struct Input {
    pub(super) origin: Value,
    pub(super) text: String,
    pub(super) prompt: String,
    pub(super) project: String,
    project_identity: (u64, u64),
}

pub(super) fn authority(cfg: &Config, actor: &str) -> &'static str {
    if actor == cfg.text("owner_id") {
        "owner_direct"
    } else {
        "external"
    }
}

fn evidence(route: &Route, message: &Value) -> io::Result<(OffsetDateTime, Value)> {
    let id = field(message, "id");
    let actor = &message["author"];
    let author = field(actor, "id");
    let name = field(actor, "username");
    let text = field(message, "content");
    let empty = |field: &str| message[field].as_array().is_some_and(Vec::is_empty);
    if !super::super::config::snowflake(id)
        || !super::super::config::snowflake(author)
        || message["channel_id"] != route.channel_id
        || !message["guild_id"].is_null() && message["guild_id"] != route.guild_id
        || message["type"] != 0
        || !matches!(actor["bot"], Value::Null | Value::Bool(false))
        || !matches!(actor["system"], Value::Null | Value::Bool(false))
        || name.is_empty()
        || name.len() > 256
        || name.chars().any(char::is_control)
        || text.is_empty()
        || text.len() >= 1000
        || text.contains('\0')
        || !message["edited_timestamp"].is_null()
        || !message["message_reference"].is_null()
        || !message["webhook_id"].is_null()
        || !empty("attachments")
        || !(message["message_snapshots"].is_null() || empty("message_snapshots"))
    {
        return Err(unavailable());
    }
    let time = match &message["timestamp"] {
        Value::Null => "0001-01-01T00:00:00Z",
        value => value.as_str().ok_or_else(unavailable)?,
    };
    let stamp = OffsetDateTime::parse(time, &Rfc3339).map_err(io::Error::other)?;
    let encoded = stamp.format(&Rfc3339).map_err(io::Error::other)?;
    Ok((
        stamp,
        json!({"id":id,"author":{"id":author,"name":name},"text":text,"created_at":encoded,
        "ReplyTo":"","Attachments":null}),
    ))
}

pub(super) fn current(
    api: &Discord,
    cfg: &Config,
    route: &Route,
    guard: &Guard,
    pending: &capture::Pending,
    bound: &prepare::Discussion,
) -> io::Result<Input> {
    let starter = &bound.starter;
    let project = cfg.text("work_dir");
    // shortcut: one full two-message page and an older in-thread starter, extend with pinned Go history fixtures.
    if bound.gap != 0
        || !bound.parent_anchor.is_empty()
        || starter.guild_id != route.guild_id
        || starter.channel_id != route.channel_id
        || !starter.parent_id.is_empty() && starter.parent_id != route.parent_id
        || !super::super::config::snowflake(&starter.message_id)
        || starter
            .message_id
            .parse::<u64>()
            .map_err(io::Error::other)?
            >= guard
                .route
                .message_id
                .parse::<u64>()
                .map_err(io::Error::other)?
        || !(cfg.0["projects"].is_null()
            || cfg.0["projects"]
                .as_object()
                .is_some_and(serde_json::Map::is_empty))
        || project.is_empty()
        || std::fs::canonicalize(project)? != std::path::Path::new(project)
    {
        return Err(unavailable());
    }
    let checkout = std::fs::symlink_metadata(project)?;
    if !checkout.is_dir() {
        return Err(unavailable());
    }
    capture::verify_pending(api, cfg, route, guard, pending, true)?;
    let page = api.request(
        &format!("/channels/{}/messages?limit=20", route.channel_id),
        None,
    )?;
    let [latest, prior] = page.as_array().map(Vec::as_slice).ok_or_else(unavailable)? else {
        return Err(unavailable());
    };
    let message = field(&pending.candidate.route, "message_id");
    let actor = field(&pending.candidate.author, "id");
    let source = api.request(
        &format!("/channels/{}/messages/{message}", route.channel_id),
        None,
    )?;
    let opening = api.request(
        &format!(
            "/channels/{}/messages/{}",
            route.channel_id, starter.message_id
        ),
        None,
    )?;
    let text = field(&source, "content");
    if source != *latest
        || source["id"] != message
        || source["author"]["id"] != actor
        || source["author"]["username"] != pending.candidate.author["name"]
        || plain_revision(route, message, actor, text)? != pending.candidate.revision
        || opening["id"] != starter.message_id
    {
        return Err(unavailable());
    }
    evidence(route, &source)?;
    verify_revision(route, guard, prior)?;
    let mut rows = [evidence(route, &opening)?, evidence(route, prior)?];
    if rows
        .iter()
        .any(|(_, row)| row["author"]["id"] == cfg.text("application_id"))
        || go_bytes(&rows[0].1)?.len() + go_bytes(&rows[1].1)?.len() > 900
    {
        return Err(unavailable());
    }
    rows.sort_by_key(|(time, _)| *time);
    let history =
        String::from_utf8(go_bytes(&json!([rows[0].1, rows[1].1]))?).map_err(io::Error::other)?;
    let body = human_instruction(&pending.candidate.author, &source["id"], text)?;
    verify_thread_authority(api, cfg, route, true)?;
    let origin = json!({"kind":authority(cfg,actor),"installation_id":cfg.text("installation_id"),
        "discussion_id":bound.id,"request_id":legacy_discussions::operation(cfg.text("installation_id"), message)?,
        "actor_id":actor,"actor_name":pending.candidate.author["name"],"requester_id":actor,
        "guild_id":route.guild_id,"channel_id":route.channel_id,"message_id":message,"revision":pending.candidate.revision});
    Ok(Input {
        origin,
        text: text.into(),
        project: project.into(),
        project_identity: (checkout.dev(), checkout.ino()),
        prompt: format!(
            "{DISCUSSION_INSTRUCTIONS}Earlier Discord messages for context (JSON) {history}\nEvidence source thread, latest message {message}. The history tool can retrieve remaining pages.\n{body}"
        ),
    })
}
