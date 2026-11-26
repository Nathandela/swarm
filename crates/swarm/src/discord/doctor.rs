//! Read-only guild channel checks for the public Discord doctor.
use super::config::{Config, invalid, snowflake};
use super::guild;
use serde_json::{Value, json};
use std::io;

pub(super) fn finding(name: &str, result: io::Result<&str>) -> Value {
    match result {
        Ok(detail) => json!({"check":name,"status":"ok","detail":detail}),
        Err(error) => json!({"check":name,"status":"fail","detail":error.to_string()}),
    }
}

pub(super) fn channels(
    cfg: &Config,
    selected: Option<&str>,
    mut get: impl FnMut(&str) -> io::Result<Option<Value>>,
) -> Vec<Value> {
    let configured = cfg.0["channel_ids"]
        .as_array()
        .into_iter()
        .flatten()
        .filter_map(Value::as_str);
    let ids: Vec<_> = match selected {
        Some(id) => vec![id],
        None => configured.collect(),
    };
    if ids.is_empty() {
        return vec![
            json!({"check":"discussion access","status":"warn","detail":"access varies by location",
            "fix":"run swarm discord doctor --channel CHANNEL_ID"}),
        ];
    }
    ids.into_iter()
        .map(|id| finding(&format!("channel {id}"), check_channel(cfg, id, &mut get)))
        .collect()
}

fn check_channel(
    cfg: &Config,
    id: &str,
    mut get: impl FnMut(&str) -> io::Result<Option<Value>>,
) -> io::Result<&'static str> {
    let outside = || invalid("channel unavailable or outside configured scope");
    if !snowflake(id) {
        return Err(outside());
    }
    let channel = get(&format!("/channels/{id}"))?.ok_or_else(outside)?;
    let guild_id = channel["guild_id"]
        .as_str()
        .filter(|id| snowflake(id))
        .ok_or_else(outside)?;
    if channel["id"] != id
        || !cfg.0["guild_ids"]
            .as_array()
            .is_some_and(|ids| ids.iter().any(|v| v == guild_id))
    {
        return Err(outside());
    }
    let kind = channel["type"].as_u64().ok_or_else(outside)?;
    let thread = matches!(kind, 10..=12);
    let parent = if thread {
        let parent = channel["parent_id"]
            .as_str()
            .filter(|p| snowflake(p) && *p != id)
            .ok_or_else(outside)?;
        if !channel["thread_metadata"].is_object() {
            return Err(outside());
        }
        let location = get(&format!("/channels/{parent}"))?.ok_or_else(outside)?;
        if location["id"] != parent
            || location["guild_id"] != guild_id
            || !matches!(location["type"].as_u64(), Some(0 | 5 | 15 | 16))
        {
            return Err(outside());
        }
        parent
    } else {
        id
    };
    if cfg.0["channel_ids"]
        .as_array()
        .is_some_and(|ids| !ids.is_empty() && !ids.iter().any(|v| v == id || v == parent))
    {
        return Err(outside());
    }
    if !thread && kind != 0 {
        return Err(invalid(
            "select a regular text channel or a discussion thread",
        ));
    }
    let bot = cfg.text("application_id");
    let bits = guild::member_permissions(&mut get, parent, bot)?;
    let admin = bits & (1 << 3) != 0;
    let mut needed = (1 << 10) | (1 << 16) | (1 << 38);
    if !thread {
        needed |= (1 << 11) | (1 << 35);
    }
    if !admin && bits & needed != needed {
        return Err(invalid(
            "missing channel history, thread creation or send permission",
        ));
    }
    if kind == 12 {
        let member = get(&format!("/channels/{id}/thread-members/{bot}"))?.ok_or_else(outside)?;
        if member["id"] != id || member["user_id"] != bot {
            return Err(outside());
        }
    }
    if thread && channel["thread_metadata"]["locked"] == true {
        return Err(invalid("thread is locked"));
    }
    if thread && channel["thread_metadata"]["archived"] != false {
        return Err(invalid(
            "thread is archived or its active state is unavailable",
        ));
    }
    Ok("discussion permissions verified")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn guild_findings_bind_current_parent_membership_and_permissions() {
        let mut cfg = Config(json!({"mode":"teammate","guild_ids":["1"],"application_id":"3"}));
        let none = channels(&cfg, None, |_| panic!("unselected channel was queried"));
        assert_eq!(none[0]["status"], "warn");
        cfg.0["channel_ids"] = json!(["4", "5"]);
        let rights = (1_u64 << 10) | (1 << 16) | (1 << 38);
        let check = |selected, bits: u64, locked, archived, member: &str, parent_guild: &str| {
            channels(&cfg, selected, |path| {
                Ok(Some(match path {
                    "/channels/2" => {
                        json!({"type":12,"guild_id":"1","id":"2","parent_id":"4","thread_metadata":{"locked":locked,"archived":archived}})
                    }
                    "/channels/4" => json!({"type":0,"id":"4","guild_id":parent_guild}),
                    "/channels/5" => json!({"type":2,"id":"5","guild_id":"1"}),
                    "/channels/4/permissions/member/3" => json!({"permissions":bits.to_string()}),
                    "/channels/2/thread-members/3" => json!({"id":"2","user_id":member}),
                    _ => panic!("unexpected route {path}"),
                }))
            })
        };
        let enrolled = check(None, rights | (1 << 35) | (1 << 11), false, false, "3", "1");
        assert_eq!(enrolled[0]["status"], "ok");
        assert_eq!(
            enrolled[1]["detail"],
            "select a regular text channel or a discussion thread"
        );
        assert_eq!(
            check(Some("4"), rights, false, false, "3", "1")[0]["status"],
            "fail"
        );
        assert_eq!(
            check(Some("2"), rights, false, false, "3", "1")[0]["status"],
            "ok"
        );
        assert_eq!(
            check(Some("2"), rights, false, false, "9", "1")[0]["status"],
            "fail"
        );
        assert_eq!(
            check(Some("2"), rights, false, false, "3", "8")[0]["status"],
            "fail"
        );
        assert_eq!(
            check(Some("2"), 8, false, false, "9", "1")[0]["status"],
            "fail"
        );
        assert_eq!(
            check(Some("2"), 8, false, true, "3", "1")[0]["status"],
            "fail"
        );
        assert_eq!(
            check(Some("2"), 8, true, false, "3", "1")[0]["detail"],
            "thread is locked"
        );
        assert_eq!(
            check(Some("../4"), 8, false, false, "", "1")[0]["status"],
            "fail"
        );
    }
}
