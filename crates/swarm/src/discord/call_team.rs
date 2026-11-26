//! Team call policy. Consent is per participant and only belongs to a live Gateway epoch.
use super::config::{Config, invalid, snowflake};
use serde_json::Value;
use std::collections::{BTreeMap, BTreeSet};
use std::io;

const DISCORD_EPOCH_MS: u64 = 1_420_070_400_000;
const COMMAND_AGE_MS: u64 = 30_000;
const CLOCK_SKEW_MS: u64 = 2_000;
const CONSENT_MS: u64 = 30 * 60 * 1_000;

#[derive(Clone, Debug)]
pub(super) struct Scope {
    pub(super) guild: String,
    pub(super) room: String,
    pub(super) thread: String,
    bot: String,
    users: BTreeSet<String>,
}

impl Scope {
    pub(super) fn from_config(cfg: &Config) -> io::Result<Option<Self>> {
        let calls = &cfg.0["calls"];
        if cfg.text("mode") != "teammate" || calls["mode"] != "team" {
            return Ok(None);
        }
        let field = |key| {
            calls[key]
                .as_str()
                .filter(|id| snowflake(id))
                .ok_or_else(|| {
                    invalid("team call requires guild, ordinary room and linked thread IDs")
                })
        };
        let guild = field("guild_id")?;
        let room = field("room_id")?;
        let thread = field("discussion_id")?;
        let bot = cfg.text("application_id");
        if guild == room
            || guild == thread
            || room == thread
            || !snowflake(bot)
            || !cfg.0["guild_ids"]
                .as_array()
                .is_some_and(|ids| ids.iter().any(|id| id == guild))
        {
            return Err(invalid(
                "team call room or thread is outside the enrolled guild",
            ));
        }
        let list = calls["user_ids"]
            .as_array()
            .filter(|users| (1..=16).contains(&users.len()))
            .ok_or_else(|| invalid("team call requires one to sixteen configured participants"))?;
        let users: BTreeSet<_> = list
            .iter()
            .filter_map(Value::as_str)
            .filter(|id| snowflake(id) && *id != bot)
            .map(str::to_owned)
            .collect();
        if users.len() != list.len() {
            return Err(invalid(
                "team call participants must be distinct human identities",
            ));
        }
        Ok(Some(Self {
            guild: guild.into(),
            room: room.into(),
            thread: thread.into(),
            bot: bot.into(),
            users,
        }))
    }

    /// Return true for call-control content even when its grant is refused. A
    /// command cannot also become a native text admission.
    pub(super) fn control(&self, event: &Value, now: u64) -> (bool, Option<Control>) {
        let data = &event["d"];
        if !matches!(
            event["t"].as_str(),
            Some("MESSAGE_CREATE" | "MESSAGE_UPDATE")
        ) || data["guild_id"] != self.guild
            || data["channel_id"] != self.thread
        {
            return (false, None);
        }
        let Some(text) = data["content"].as_str() else {
            return (false, None);
        };
        let Some(action) = [format!("<@{}> ", self.bot), format!("<@!{}> ", self.bot)]
            .iter()
            .find_map(|prefix| text.trim().strip_prefix(prefix.as_str()))
            .and_then(|suffix| match suffix {
                "voice consent" => Some(Action::Consent),
                "voice join" => Some(Action::Join),
                "voice leave" => Some(Action::Leave),
                _ => None,
            })
        else {
            return (false, None);
        };
        let admitted = (|| {
            if event["t"] != "MESSAGE_CREATE"
                || data["type"] != 0
                || !data["webhook_id"].is_null()
                || !data["edited_timestamp"].is_null()
                || ["bot", "system"]
                    .into_iter()
                    .any(|key| data["author"].get(key).is_some_and(|value| value != false))
                || !data["mentions"]
                    .as_array()
                    .is_some_and(|users| users.iter().any(|user| user["id"] == self.bot))
            {
                return None;
            }
            let user = data["author"]["id"]
                .as_str()
                .filter(|id| self.users.contains(*id))?;
            let id = data["id"]
                .as_str()
                .filter(|id| snowflake(id))?
                .parse::<u64>()
                .ok()?;
            let issued = data["timestamp"]
                .as_str()
                .and_then(|timestamp| {
                    time::OffsetDateTime::parse(
                        timestamp,
                        &time::format_description::well_known::Rfc3339,
                    )
                    .ok()
                })
                .and_then(|time| u64::try_from(time.unix_timestamp_nanos() / 1_000_000).ok())?;
            let encoded = DISCORD_EPOCH_MS.checked_add(id >> 22)?;
            if issued.abs_diff(encoded) > CLOCK_SKEW_MS
                || issued > now.saturating_add(CLOCK_SKEW_MS)
                || now.saturating_sub(issued) > COMMAND_AGE_MS
            {
                return None;
            }
            Some(Control {
                action,
                user: user.into(),
                id,
                issued: issued.min(now),
            })
        })();
        (true, admitted)
    }

    pub(super) fn verify_audience(
        &self,
        speakers: &[String],
        mut get: impl FnMut(&str) -> io::Result<Option<Value>>,
    ) -> io::Result<bool> {
        if speakers.is_empty()
            || speakers.iter().any(|user| !self.configured(user))
            || speakers.iter().collect::<BTreeSet<_>>().len() != speakers.len()
        {
            return Ok(false);
        }
        let Some(room) = get(&format!("/channels/{}", self.room))? else {
            return Ok(false);
        };
        let Some(thread) = get(&format!("/channels/{}", self.thread))? else {
            return Ok(false);
        };
        let private = thread["type"] == 12;
        let Some(parent) = thread["parent_id"]
            .as_str()
            .filter(|id| snowflake(id) && *id != self.thread)
        else {
            return Ok(false);
        };
        if room["id"] != self.room
            || room["guild_id"] != self.guild
            || room["type"] != 2
            || thread["id"] != self.thread
            || thread["guild_id"] != self.guild
            || !matches!(thread["type"].as_u64(), Some(10..=12))
            || thread["thread_metadata"]["archived"] != false
            || thread["thread_metadata"]["locked"] != false
        {
            return Ok(false);
        }
        let Some(container) = get(&format!("/channels/{parent}"))? else {
            return Ok(false);
        };
        if container["id"] != parent
            || container["guild_id"] != self.guild
            || !matches!(container["type"].as_u64(), Some(0 | 5 | 15 | 16))
        {
            return Ok(false);
        }
        const ADMIN: u64 = 1 << 3;
        const REQUIRED_TEXT: u64 = (1 << 10) | (1 << 16) | (1 << 38);
        const REQUIRED_VOICE: u64 = (1 << 10) | (1 << 20) | (1 << 21);
        for user in std::iter::once(&self.bot).chain(speakers) {
            let Some(member) = get(&format!("/guilds/{}/members/{user}", self.guild))? else {
                return Ok(false);
            };
            if member["user"]["id"] != *user
                || (!member["user"]["bot"].is_null()
                    && member["user"]["bot"] != (user == &self.bot))
                || (user == &self.bot && member["user"]["bot"] != true)
                || member["user"]["system"] == true
            {
                return Ok(false);
            }
            let mut permission = |channel| -> io::Result<u64> {
                Ok(
                    get(&format!("/channels/{channel}/permissions/member/{user}"))?
                        .and_then(|response| response["permissions"].as_str()?.parse().ok())
                        .unwrap_or(0),
                )
            };
            let text = permission(parent)?;
            let voice = permission(&self.room)?;
            if text & ADMIN == 0 && text & REQUIRED_TEXT != REQUIRED_TEXT
                || voice & ADMIN == 0 && voice & REQUIRED_VOICE != REQUIRED_VOICE
            {
                return Ok(false);
            }
            if private {
                let Some(member) =
                    get(&format!("/channels/{}/thread-members/{user}", self.thread))?
                else {
                    return Ok(false);
                };
                if member["id"] != self.thread || member["user_id"] != *user {
                    return Ok(false);
                }
            }
        }
        Ok(true)
    }

    pub(super) fn configured(&self, user: &str) -> bool {
        self.users.contains(user)
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Action {
    Consent,
    Join,
    Leave,
}

pub(super) struct Control {
    action: Action,
    pub(super) user: String,
    id: u64,
    issued: u64,
}

/// Media derives authority from the intersection of a fresh roster, a new
/// invitation, current permissions and a session-bound consent for everyone.
#[derive(Default)]
pub(super) struct Consent {
    by_user: BTreeMap<String, (String, u64)>,
    sources: BTreeSet<u64>,
    last_control: BTreeMap<String, u64>,
    last_leave: u64,
    invitation: Option<(String, u64)>,
    rearm_after: u64,
}

impl Consent {
    /// Return true when the current media epoch must be revoked.
    pub(super) fn apply(&mut self, control: Control, session: Option<&str>, now: u64) -> bool {
        if control.id <= self.last_leave
            || control.id == *self.last_control.get(&control.user).unwrap_or(&0)
        {
            return false;
        }
        let previous = self.last_control.entry(control.user.clone()).or_default();
        if control.id < *previous && control.action != Action::Leave {
            return false;
        }
        *previous = (*previous).max(control.id);
        if control.action == Action::Leave {
            self.last_leave = control.id;
            self.close(now);
            return true;
        }
        let Some(session) = session else {
            return false;
        };
        if control.issued <= self.rearm_after || now.saturating_sub(control.issued) > COMMAND_AGE_MS
        {
            return false;
        }
        let deadline = control.issued.saturating_add(CONSENT_MS);
        if self.sources.len() >= 256 {
            self.close(now);
            return true;
        }
        self.sources.insert(control.id);
        if control.action == Action::Join {
            self.invitation = Some((control.user.clone(), deadline));
        }
        self.by_user
            .insert(control.user, (session.to_owned(), deadline));
        false
    }

    pub(super) fn deadline<'a>(
        &self,
        participants: impl Iterator<Item = (&'a str, &'a str)>,
        now: u64,
    ) -> Option<u64> {
        let mut deadline = self.invitation.as_ref()?.1;
        if deadline <= now {
            return None;
        }
        let mut count = 0;
        for (user, session) in participants {
            let (bound, until) = self.by_user.get(user)?;
            if bound != session || *until <= now {
                return None;
            }
            count += 1;
            deadline = deadline.min(*until);
        }
        (count > 0).then_some(deadline)
    }

    pub(super) fn revoked(&self, event: &Value) -> bool {
        let data = &event["d"];
        let contains = |id: &Value| {
            id.as_str()
                .and_then(|id| id.parse::<u64>().ok())
                .is_some_and(|id| self.sources.contains(&id))
        };
        match event["t"].as_str() {
            Some("MESSAGE_UPDATE" | "MESSAGE_DELETE") => contains(&data["id"]),
            Some("MESSAGE_DELETE_BULK") => data["ids"]
                .as_array()
                .is_some_and(|ids| ids.iter().any(contains)),
            _ => false,
        }
    }

    pub(super) fn remove(&mut self, user: &str) {
        self.by_user.remove(user);
        if self
            .invitation
            .as_ref()
            .is_some_and(|(inviter, _)| inviter == user)
        {
            self.invitation = None;
        }
    }

    pub(super) fn close(&mut self, at: u64) {
        self.rearm_after = self.rearm_after.max(at);
        self.by_user.clear();
        self.sources.clear();
        self.invitation = None;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    use time::{OffsetDateTime, format_description::well_known::Rfc3339};

    const NOW: u64 = DISCORD_EPOCH_MS + 123_000_000;

    fn command(user: &str, action: &str, at: u64, nonce: u64) -> Value {
        let timestamp = OffsetDateTime::from_unix_timestamp_nanos(i128::from(at) * 1_000_000)
            .unwrap()
            .format(&Rfc3339)
            .unwrap();
        let id = ((at - DISCORD_EPOCH_MS) << 22) | nonce;
        json!({"t":"MESSAGE_CREATE","d":{"guild_id":"1","channel_id":"2","type":0,
            "id":id.to_string(),"timestamp":timestamp,"author":{"id":user},"mentions":[{"id":"4"}],
            "content":format!("<@4> voice {action}")}})
    }

    fn scope() -> Scope {
        Scope::from_config(&Config(json!({"mode":"teammate","application_id":"4","guild_ids":["1"],
            "calls":{"mode":"team","guild_id":"1","room_id":"3","discussion_id":"2","user_ids":["8","9"]}})))
            .unwrap().unwrap()
    }

    #[test]
    fn every_room_listener_can_read_and_send_in_the_exact_private_thread() {
        let scope = scope();
        let verify = |excluded: &str| {
            scope
                .verify_audience(&["8".into(), "9".into()], |path| {
                    let row = match path {
                        "/channels/3" => json!({"id":"3","guild_id":"1","type":2}),
                        "/channels/2" => json!({"id":"2","guild_id":"1","type":12,"parent_id":"7",
                    "thread_metadata":{"archived":false,"locked":false}}),
                        "/channels/7" => json!({"id":"7","guild_id":"1","type":0}),
                        _ if path.starts_with("/guilds/1/members/") => {
                            let id = path.rsplit('/').next().unwrap();
                            json!({"user":{"id":id,"bot":id=="4"}})
                        }
                        _ if path.contains("/permissions/member/") => {
                            let bits: u64 = if path.starts_with("/channels/7/") {
                                (1 << 10) | (1 << 16) | (1 << 38)
                            } else {
                                (1 << 10) | (1 << 20) | (1 << 21)
                            };
                            json!({"permissions":bits.to_string()})
                        }
                        _ if path.starts_with("/channels/2/thread-members/") => {
                            let id = path.rsplit('/').next().unwrap();
                            if id == excluded {
                                return Ok(None);
                            }
                            json!({"id":"2","user_id":id})
                        }
                        _ => panic!("out of scope lookup: {path}"),
                    };
                    Ok(Some(row))
                })
                .unwrap()
        };
        assert!(verify(""));
        assert!(!verify("9"));
    }

    #[test]
    fn all_listeners_consent_in_live_sessions_and_leave_rearms() {
        let scope = scope();
        let parse = |user, action, at, nonce| {
            scope
                .control(&command(user, action, at, nonce), at)
                .1
                .unwrap()
        };
        let mut consent = Consent::default();
        let users = |session| [("8", "first"), ("9", session)].into_iter();
        assert!(!consent.apply(parse("8", "join", NOW, 1), Some("first"), NOW));
        assert_eq!(consent.deadline(users("second"), NOW), None);
        assert!(!consent.apply(parse("9", "consent", NOW + 1, 2), Some("second"), NOW + 1));
        assert_eq!(
            consent.deadline(users("second"), NOW + 1),
            Some(NOW + CONSENT_MS)
        );
        assert_eq!(consent.deadline(users("replacement"), NOW + 1), None);
        assert_eq!(consent.deadline(users("second"), NOW + CONSENT_MS), None);
        assert!(consent.apply(parse("9", "leave", NOW + 3, 3), None, NOW + 3));
        assert!(!consent.apply(parse("8", "join", NOW, 1), Some("first"), NOW + 4));
        assert!(!consent.apply(parse("9", "consent", NOW + 4, 4), Some("second"), NOW + 4));
        assert_eq!(consent.deadline(users("second"), NOW + 4), None);
        assert!(scope.configured("8"));
        assert!(!scope.configured("4"));
    }

    #[test]
    fn only_fresh_linked_human_mentions_reach_the_control_plane() {
        let scope = scope();
        assert_eq!(scope.room, "3");
        let mut alternate = command("8", "consent", NOW, 1);
        alternate["d"]["channel_id"] = json!("99");
        assert!(!scope.control(&alternate, NOW).0);
        for invalid in [
            command("8", "consent", NOW - COMMAND_AGE_MS - 1, 1),
            command("4", "join", NOW, 2),
            command("8", "join", NOW + CLOCK_SKEW_MS + 1, 3),
        ] {
            let (classified, grant) = scope.control(&invalid, NOW);
            assert!(classified && grant.is_none());
        }
        let mut edited = command("8", "consent", NOW, 1);
        edited["t"] = json!("MESSAGE_UPDATE");
        let (classified, grant) = scope.control(&edited, NOW);
        assert!(classified && grant.is_none());
    }
}
