//! Reconcile only the pinned thread. Historical controls never become live consent.
use super::{
    config::{Config, invalid, snowflake},
    dispatcher::Audience,
    receipt::{Inbox, Observation, TEAM_GAP_NOTICE},
    rest::Post,
    team_thread::{self, ThreadAudience},
};
use serde_json::Value;
use std::{collections::BTreeMap, io};

const PAGE: usize = 100;
const MAX_PAGES: usize = 4;

pub(super) fn repair(
    cfg: &Config,
    inbox: &mut Inbox,
    thread: &ThreadAudience<'_>,
) -> io::Result<bool> {
    let access = thread.access(cfg)?;
    if !access.can_read_history {
        inbox.stage_team_gap(&access.parent)?;
        return Ok(false);
    }
    repair_with(cfg, inbox, thread, &access.parent, |before| {
        thread.history(cfg, &access.parent, before)
    })?;
    Ok(true)
}

fn repair_with(
    cfg: &Config,
    inbox: &mut Inbox,
    thread: &impl Audience,
    parent: &str,
    page: impl FnMut(&str) -> io::Result<Vec<Value>>,
) -> io::Result<()> {
    let cursor = inbox.team_cursor().map(parse).transpose()?;
    let pending = inbox.pending_team();
    let rows = gather(cursor, page)?;
    let newest = rows
        .last_key_value()
        .map(|(id, _)| id.to_string())
        .unwrap_or_default();
    // A pre-existing connector without a watermark has no proof of an earlier
    // consent floor. Recheck old pending rows, but never guess prior ingress.
    let old = cursor.unwrap_or(u64::MAX);
    let mut current = Vec::new();
    for (&id, row) in &rows {
        if id > old
            && row["author"]["id"] != cfg.text("application_id")
            && !team_thread::control(cfg, row)
            && (inbox.team_gap_pending() || super::guild::address(cfg, row).is_none())
        {
            current.push((id, thread.current(cfg, &id.to_string())?));
        }
    }
    for &id in &pending {
        if !current.iter().any(|(known, _)| *known == id) {
            current.push((id, thread.current(cfg, &id.to_string())?));
        }
    }
    current.sort_unstable_by_key(|entry| entry.0);
    if inbox.team_gap_pending()
        && current.iter().any(|(id, current)| {
            *id > old
                && current
                    .as_ref()
                    .is_some_and(|row| admissible_gap_human(cfg, inbox, row))
        })
    {
        // The notice must survive if admission or the history cursor advances before a restart.
        inbox.stage_team_gap(parent)?;
    }
    for row in rows
        .values()
        .filter(|row| row["author"]["id"] == cfg.text("application_id"))
    {
        inbox.observe_team_gap(row, parent, Observation::History)?;
        inbox.observe(row, Observation::History)?;
    }
    for (id, row) in current {
        if let Some(row) = row
            && !team_thread::control(cfg, &row)
            && super::guild::address(cfg, &row).is_none()
            && (!inbox.manager().is_null()
                || inbox.has_team_source(&id.to_string())
                || team_thread::explicit(cfg, &row))
            && thread.admit(inbox, &row)?.is_some()
        {
            continue;
        }
        if pending.contains(&id) {
            inbox.withdraw(&id.to_string())?;
        }
    }
    inbox.complete_team_repair(&newest)
}

fn admissible_gap_human(cfg: &Config, inbox: &Inbox, row: &Value) -> bool {
    let text = |key: &str| row[key].as_str().unwrap_or_default();
    row["author"]["id"]
        .as_str()
        .is_some_and(|id| snowflake(id) && id != cfg.text("application_id"))
        && row["author"].get("bot").is_none_or(|flag| flag == false)
        && row["author"].get("system").is_none_or(|flag| flag == false)
        && row["webhook_id"].is_null()
        && matches!(row["type"].as_u64(), Some(0 | 19))
        && !team_thread::control(cfg, row)
        && !inbox.has_guild_source(text("guild_id"), text("channel_id"), text("id"))
}

pub(super) fn send_gap_notice(
    cfg: &Config,
    inbox: &mut Inbox,
    thread: &ThreadAudience<'_>,
) -> io::Result<()> {
    let now = crate::ipc::spawn::now_ms();
    let Some(notice) = inbox.pending_team_gap(now) else {
        return Ok(());
    };
    if thread.access(cfg)?.parent != notice.parent {
        return Ok(());
    }
    inbox.team_gap_attempt(&notice.nonce, None)?;
    match thread.post(cfg, &notice.nonce, TEAM_GAP_NOTICE, &[]) {
        Post::Acknowledged(message) => {
            inbox.observe_team_gap(&message, &notice.parent, Observation::Rest)
        }
        Post::RateLimited(delay) if (1..=120_000).contains(&delay) => inbox.team_gap_attempt(
            &notice.nonce,
            Some(crate::ipc::spawn::now_ms().saturating_add(delay)),
        ),
        _ => Ok(()),
    }
}

pub(super) fn observe_gap_notice(
    cfg: &Config,
    inbox: &mut Inbox,
    thread: &ThreadAudience<'_>,
    event: &Value,
) -> io::Result<()> {
    let message = &event["d"];
    if event["t"] == "MESSAGE_CREATE"
        && thread.route.matches(message)
        && let Some(parent) = inbox.team_gap_message_parent(message)
        && thread
            .access(cfg)
            .is_ok_and(|access| access.parent == parent)
    {
        let parent = parent.to_owned();
        inbox.observe_team_gap(message, &parent, Observation::Gateway)?;
    }
    Ok(())
}

fn gather(
    lower: Option<u64>,
    mut page: impl FnMut(&str) -> io::Result<Vec<Value>>,
) -> io::Result<BTreeMap<u64, Value>> {
    let mut rows = BTreeMap::new();
    let mut before = String::new();
    for _ in 0..MAX_PAGES {
        let batch = page(&before)?;
        if batch.len() > PAGE {
            return Err(invalid("oversized linked thread history page"));
        }
        let size = batch.len();
        let mut lowest = u64::MAX;
        for entry in batch {
            let id = parse(
                entry["id"]
                    .as_str()
                    .ok_or_else(|| invalid("missing thread history identity"))?,
            )?;
            if id == 0
                || !before.is_empty() && id >= parse(&before)?
                || rows.insert(id, entry).is_some()
            {
                return Err(invalid("nonprogressing or duplicate linked thread history"));
            }
            lowest = lowest.min(id);
        }
        if lower.is_none() || size < PAGE || lower.is_some_and(|floor| lowest <= floor) {
            return Ok(rows);
        }
        before = lowest.to_string();
    }
    Err(invalid(
        "linked thread history repair exceeded its safe page bound",
    ))
}

fn parse(id: &str) -> io::Result<u64> {
    if id.is_empty() {
        return Ok(0);
    }
    id.parse()
        .ok()
        .filter(|_| snowflake(id))
        .ok_or_else(|| invalid("invalid linked thread history identity"))
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn failed_history_admission_leaves_the_notice_ahead_of_knowledge_and_cursor() {
        use crate::supervisor::{mail_files::Captured, runtime};
        struct Interrupted(Value);
        impl Audience for Interrupted {
            fn current(&self, _: &Config, _: &str) -> io::Result<Option<Value>> {
                Ok(Some(self.0.clone()))
            }
            fn verify(&self, _: &Config) -> io::Result<()> {
                Ok(())
            }
            fn admit(&self, inbox: &mut Inbox, row: &Value) -> io::Result<Option<Value>> {
                assert_eq!(row, &self.0);
                assert!(inbox.pending_team_gap(u64::MAX).is_some());
                assert_eq!(inbox.team_cursor(), Some("20"));
                Err(io::Error::other("fixture stopped before admission"))
            }
            fn post(&self, _: &Config, _: &str, _: &str, _: &[Captured]) -> Post {
                panic!("history repair should stage the notice")
            }
        }
        let root = crate::test_temp::root().join(format!(
            "swarm-gap-interrupt-{}",
            runtime::random_id().unwrap()
        ));
        runtime::private_dir(&root).unwrap();
        let cfg = Config(
            json!({"mode":"teammate","installation_id":"test","application_id":"3","owner_id":"4",
            "guild_ids":["1"],"team_discussion":{"guild_id":"1","thread_id":"2"}}),
        );
        let row = json!({"id":"21","guild_id":"1","channel_id":"2","author":{"id":"7"},
            "content":"<@3> missed request","mentions":[{"id":"3"}],"attachments":[],"type":0});
        let mut inbox = Inbox::open(&root, &cfg).unwrap();
        inbox.checkpoint_team("20").unwrap();
        inbox.mark_team_gap().unwrap();
        let fixture = Interrupted(row.clone());
        assert_eq!(
            repair_with(&cfg, &mut inbox, &fixture, "10", |_| Ok(vec![row.clone()]))
                .unwrap_err()
                .to_string(),
            "fixture stopped before admission"
        );
        let nonce = inbox.pending_team_gap(u64::MAX).unwrap().nonce;
        drop(inbox);
        let mut inbox = Inbox::open(&root, &cfg).unwrap();
        assert!(!inbox.has_team_source("21"));
        assert_eq!(inbox.team_cursor(), Some("20"));
        assert!(inbox.team_gap_pending());
        assert!(repair_with(&cfg, &mut inbox, &fixture, "10", |_| Ok(vec![row.clone()])).is_err());
        assert_eq!(inbox.pending_team_gap(u64::MAX).unwrap().nonce, nonce);
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn ancient_pending_source_is_rechecked_without_paging_to_it() {
        use super::super::{rest::Post, team_thread::Route};
        use crate::supervisor::{mail_files::Captured, runtime};
        use std::cell::RefCell;
        struct Fixture<'a> {
            route: &'a Route,
            fetched: RefCell<Vec<String>>,
        }
        impl Audience for Fixture<'_> {
            fn current(&self, _: &Config, id: &str) -> io::Result<Option<Value>> {
                self.fetched.borrow_mut().push(id.into());
                Ok(Some(
                    json!({"id":id,"guild_id":"1","channel_id":"2","author":{"id":"4"},
                    "content":"message","attachments":[],"type":0}),
                ))
            }
            fn verify(&self, _: &Config) -> io::Result<()> {
                Ok(())
            }
            fn admit(&self, inbox: &mut Inbox, row: &Value) -> io::Result<Option<Value>> {
                inbox.admit_team(self.route, row)
            }
            fn post(&self, _: &Config, _: &str, _: &str, _: &[Captured]) -> Post {
                panic!("repair does not post")
            }
        }
        let dir =
            std::env::temp_dir().join(format!("swarm-old-guild-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&dir).unwrap();
        let cfg = Config(
            json!({"mode":"teammate","installation_id":"test","application_id":"3","owner_id":"4",
            "guild_ids":["1"],"team_discussion":{"guild_id":"1","thread_id":"2"}}),
        );
        let route = Route::configured(&cfg).unwrap().unwrap();
        let fixture = Fixture {
            route: &route,
            fetched: RefCell::new(Vec::new()),
        };
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        inbox.checkpoint("").unwrap();
        let old = fixture.current(&cfg, "1").unwrap().unwrap();
        fixture.admit(&mut inbox, &old).unwrap().unwrap();
        fixture.fetched.borrow_mut().clear();
        inbox.checkpoint_team("900").unwrap();
        let mut calls = 0;
        repair_with(&cfg, &mut inbox, &fixture, "20", |before| {
            calls += 1;
            let interval = match before {
                "" => 901..=1000,
                "901" => 801..=900,
                _ => panic!("paged below saved frontier"),
            };
            Ok(interval
                .map(|id| json!({"id":id.to_string(),"author":{"id":"3","bot":true}}))
                .collect())
        })
        .unwrap();
        assert_eq!(calls, 2);
        assert_eq!(&*fixture.fetched.borrow(), &["1"]);
        assert_eq!(inbox.team_cursor(), Some("1000"));
        assert!(inbox.pending_team().contains(&1));
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn bounded_pages_reach_the_saved_frontier_without_duplicate_history() {
        let page = |range: std::ops::RangeInclusive<u64>| {
            range
                .rev()
                .map(|id| json!({"id":id.to_string()}))
                .collect::<Vec<_>>()
        };
        let got = gather(Some(95), |before| match before {
            "" => Ok(page(101..=200)),
            "101" => Ok(page(92..=100)),
            _ => panic!("unexpected page"),
        })
        .unwrap();
        assert_eq!(got.first_key_value().unwrap().0, &92);
        assert!(gather(Some(1), |_| Ok(page(101..=200))).is_err());
        let mut next = 1001;
        assert!(
            gather(Some(1), |_| {
                next -= 100;
                Ok(page(next..=next + 99))
            })
            .is_err()
        );
        assert_eq!(gather(None, |_| Ok(page(101..=200))).unwrap().len(), 100);
    }
}
