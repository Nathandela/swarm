//! Room consent follows a fresh Discord roster and is never replayed.
use super::call_team::{Consent, Scope};
use super::config::{invalid, snowflake};
use serde_json::{Value, json};
use std::collections::BTreeMap;
use std::io;
use std::time::{Duration, Instant};

const IDLE: Duration = Duration::from_secs(120);
const MAX_CALL: Duration = Duration::from_secs(30 * 60);

pub(super) struct Room {
    guild: String,
    room: String,
    owner: String,
    bot: String,
    anchor: bool,
    sequence: u64,
    generation: u64,
    occupants: BTreeMap<String, Presence>,
    bot_room: Option<String>,
    bot_session: Option<String>,
    active: Option<Epoch>,
    attempted: bool,
    awaiting_leave: bool,
    leave_sent: bool,
    suspended: bool,
    team: Option<(Scope, Consent)>,
    roster_version: u64,
    retry: Option<(Instant, Instant)>,
}

#[derive(PartialEq)]
struct Presence {
    allowed: bool,
    session: String,
}

struct Epoch {
    generation: u64,
    deadline: Instant,
    last_speech: Instant,
    joined_after: Option<u64>,
    ready: bool,
    speakers: Vec<String>,
    retried: bool,
}

pub(super) enum Change {
    Ignore,
    End,
    Voice(Value),
}

impl Room {
    pub(super) fn new(guild: &str, room: &str, owner: &str, bot: &str) -> io::Result<Self> {
        if ![guild, room, owner, bot].into_iter().all(snowflake) || owner == bot {
            return Err(invalid("invalid personal voice room and owner scope"));
        }
        Ok(Self {
            guild: guild.into(),
            room: room.into(),
            owner: owner.into(),
            bot: bot.into(),
            anchor: false,
            sequence: 0,
            generation: 0,
            occupants: BTreeMap::new(),
            bot_room: None,
            bot_session: None,
            active: None,
            attempted: false,
            awaiting_leave: false,
            leave_sent: false,
            suspended: false,
            team: None,
            roster_version: 0,
            retry: None,
        })
    }

    pub(super) fn team(mut self, scope: Scope) -> Self {
        self.team = Some((scope, Consent::default()));
        self
    }

    pub(super) fn is_team(&self) -> bool {
        self.team.is_some()
    }

    pub(super) fn team_scope(&self) -> Option<&Scope> {
        self.team.as_ref().map(|(scope, _)| scope)
    }

    pub(super) fn team_control(&mut self, event: &Value, now: u64) -> (bool, Change) {
        let Some((scope, consent)) = &mut self.team else {
            return (false, Change::Ignore);
        };
        if event["d"]["guild_id"] == scope.guild
            && event["d"]["channel_id"] == scope.thread
            && consent.revoked(event)
        {
            consent.close(now);
            self.retry = None;
            return (true, self.end());
        }
        let (handled, command) = scope.control(event, now);
        let ended = command.is_some_and(|command| {
            let session = self
                .occupants
                .get(&command.user)
                .filter(|presence| self.anchor && presence.allowed)
                .map(|presence| presence.session.as_str());
            consent.apply(command, session, now)
        });
        if ended {
            self.retry = None;
        }
        (handled, if ended { self.end() } else { Change::Ignore })
    }

    pub(super) fn team_candidate(&self, now: u64) -> Option<(u64, Vec<String>, u64)> {
        let (scope, consent) = self.team.as_ref()?;
        if !self.anchor
            || self.suspended
            || self.occupants.is_empty()
            || self
                .occupants
                .iter()
                .any(|(user, presence)| !presence.allowed || !scope.configured(user))
        {
            return None;
        }
        let valid_until = consent.deadline(
            self.occupants
                .iter()
                .map(|(user, presence)| (user.as_str(), presence.session.as_str())),
            now,
        )?;
        Some((
            self.roster_version,
            self.occupants.keys().cloned().collect(),
            valid_until,
        ))
    }

    #[cfg(test)]
    pub(super) fn begin(&mut self, now: Instant) -> Option<u64> {
        self.begin_verified(now, 0, None)
            .map(|(generation, _)| generation)
    }

    pub(super) fn begin_verified(
        &mut self,
        now: Instant,
        wall: u64,
        approved: Option<u64>,
    ) -> Option<(u64, Vec<String>)> {
        if !self.anchor
            || self.suspended
            || self.awaiting_leave
            || self.active.is_some()
            || self.bot_room.is_some()
        {
            return None;
        }
        let (speakers, deadline) = if self.is_team() {
            let (version, speakers, until) = self.team_candidate(wall)?;
            if approved != Some(version) {
                return None;
            }
            (
                speakers,
                now + Duration::from_millis(until.saturating_sub(wall)).min(MAX_CALL),
            )
        } else {
            if self.attempted && self.retry.is_none()
                || self.occupants.len() != 1
                || !self
                    .occupants
                    .get(&self.owner)
                    .is_some_and(|owner| owner.allowed)
            {
                return None;
            }
            self.attempted = true;
            (vec![self.owner.clone()], now + MAX_CALL)
        };
        let (deadline, last_speech, retried) = match self.retry.take() {
            Some((prior_deadline, last_speech)) => {
                if now >= prior_deadline || now >= last_speech + IDLE {
                    return None;
                }
                (deadline.min(prior_deadline), last_speech, true)
            }
            None => (deadline, now, false),
        };
        self.generation = self.generation.checked_add(1)?;
        self.active = Some(Epoch {
            generation: self.generation,
            deadline,
            last_speech,
            retried,
            joined_after: None,
            ready: false,
            speakers: speakers.clone(),
        });
        Some((self.generation, speakers))
    }

    pub(super) fn join(&mut self, generation: u64, now: Instant) -> Option<Value> {
        let sequence = self.sequence;
        let active = self.current(generation, now)?;
        if active.joined_after.is_some() {
            return None;
        }
        active.joined_after = Some(sequence);
        Some(voice_state(&self.guild, json!(self.room)))
    }

    pub(super) fn ready(&mut self, generation: u64, now: Instant) -> bool {
        let bot_joined = self.bot_room.as_deref() == Some(&self.room);
        if let Some(active) = self.current(generation, now)
            && active.joined_after.is_some()
            && bot_joined
        {
            active.ready = true;
            return true;
        }
        false
    }

    pub(super) fn forward(
        &mut self,
        generation: u64,
        speaker: &str,
        speech: bool,
        now: Instant,
    ) -> bool {
        if let Some(active) = self.current(generation, now)
            && active.ready
            && active.speakers.iter().any(|user| user == speaker)
        {
            if speech {
                active.last_speech = now;
            }
            return true;
        }
        false
    }

    fn current(&mut self, generation: u64, now: Instant) -> Option<&mut Epoch> {
        self.active.as_mut().filter(|active| {
            active.generation == generation
                && now < active.deadline
                && now < active.last_speech + IDLE
        })
    }

    pub(super) fn expire(&mut self, now: Instant) -> bool {
        if self
            .retry
            .is_some_and(|(deadline, last)| now >= deadline || now >= last + IDLE)
        {
            self.invalidate_retry();
        }
        if self.active.is_some() && self.current(self.generation, now).is_none() {
            self.end();
            return true;
        }
        false
    }

    pub(super) fn rekey(&mut self, now: Instant) -> bool {
        let Some(epoch) = self.current(self.generation, now) else {
            return false;
        };
        if !epoch.ready || epoch.joined_after.is_none() || epoch.retried {
            return false;
        }
        let retry = (epoch.deadline, epoch.last_speech);
        self.active.take();
        self.awaiting_leave = true;
        self.retry = Some(retry);
        self.roster_version += 1;
        true
    }

    fn invalidate_retry(&mut self) {
        if self.retry.take().is_some()
            && let Some((_, consent)) = &mut self.team
        {
            consent.close(crate::ipc::spawn::now_ms());
        }
    }

    fn end(&mut self) -> Change {
        if let Some(epoch) = self.active.take() {
            self.awaiting_leave |= epoch.joined_after.is_some();
            if let Some((_, consent)) = &mut self.team {
                consent.close(crate::ipc::spawn::now_ms());
            }
            return Change::End;
        }
        Change::Ignore
    }

    pub(super) fn leave(&mut self) -> Option<Value> {
        self.end();
        if self.awaiting_leave && !self.leave_sent {
            self.leave_sent = true;
            return Some(voice_state(&self.guild, Value::Null));
        }
        None
    }

    pub(super) fn suspend(&mut self) {
        self.suspended = true;
        self.invalidate_retry();
        self.end();
    }

    pub(super) fn event(&mut self, event: &Value) -> Change {
        let Some(kind) = event["t"].as_str() else {
            return Change::Ignore;
        };
        let data = &event["d"];
        let guild = match kind {
            "GUILD_CREATE" | "GUILD_UPDATE" | "GUILD_DELETE" => &data["id"],
            "VOICE_STATE_UPDATE"
            | "VOICE_SERVER_UPDATE"
            | "CHANNEL_UPDATE"
            | "CHANNEL_DELETE"
            | "GUILD_MEMBER_ADD"
            | "GUILD_MEMBER_UPDATE"
            | "GUILD_MEMBER_REMOVE"
            | "GUILD_ROLE_CREATE"
            | "GUILD_ROLE_UPDATE"
            | "GUILD_ROLE_DELETE" => &data["guild_id"],
            "THREAD_UPDATE"
            | "THREAD_DELETE"
            | "THREAD_MEMBERS_UPDATE"
            | "THREAD_MEMBER_UPDATE"
                if self
                    .team
                    .as_ref()
                    .is_some_and(|(scope, _)| data["id"] == scope.thread) =>
            {
                &data["guild_id"]
            }
            _ => return Change::Ignore,
        };
        if guild
            .as_str()
            .is_some_and(|id| snowflake(id) && id != self.guild)
        {
            return Change::Ignore;
        }
        if guild != &self.guild {
            self.anchor = false;
            self.invalidate_retry();
            return self.end();
        }
        let Some(sequence) = event["s"].as_u64().filter(|&sequence| sequence > 0) else {
            self.anchor = false;
            self.invalidate_retry();
            return self.end();
        };
        if sequence <= self.sequence {
            return Change::Ignore;
        }
        self.sequence = sequence;
        match kind {
            "GUILD_CREATE" => {
                self.invalidate_retry();
                self.roster_version += 1;
                if let Some((_, consent)) = &mut self.team {
                    consent.close(crate::ipc::spawn::now_ms());
                }
                let ended = self.end();
                self.anchor = self.snapshot(data);
                // Only a confirmed empty bot state clears a pending leave.
                if self.anchor && self.bot_room.is_none() {
                    self.awaiting_leave = false;
                    self.leave_sent = false;
                }
                // Snapshot refresh never restores consent for an owner who has already called.
                if self.anchor && !self.occupants.contains_key(&self.owner) {
                    self.attempted = false;
                }
                ended
            }
            "VOICE_STATE_UPDATE" if self.anchor => self.voice(data),
            "VOICE_SERVER_UPDATE" if self.anchor => self
                .active
                .as_ref()
                .and_then(|active| active.joined_after)
                .filter(|&joined| joined < sequence)
                .map_or(Change::Ignore, |_| Change::Voice(event.clone())),
            "VOICE_STATE_UPDATE" | "VOICE_SERVER_UPDATE" => Change::Ignore,
            _ => {
                self.invalidate_retry();
                self.roster_version += 1;
                if let Some((_, consent)) = &mut self.team {
                    // Continuous voice states still describe occupancy. New media
                    // needs new consents and a fresh permission decision.
                    consent.close(crate::ipc::spawn::now_ms());
                    if kind == "GUILD_DELETE"
                        || data["unavailable"] == true
                        || matches!(kind, "CHANNEL_UPDATE" | "CHANNEL_DELETE")
                            && data["id"] == self.room
                    {
                        self.anchor = false;
                    }
                } else {
                    self.anchor = false;
                }
                self.end()
            }
        }
    }

    fn snapshot(&mut self, data: &Value) -> bool {
        self.occupants.clear();
        self.bot_room = None;
        self.bot_session = None;
        let Some(channels) = data["channels"].as_array() else {
            return false;
        };
        let mut rooms = channels.iter().filter(|channel| channel["id"] == self.room);
        let ordinary = rooms.next().is_some_and(|room| {
            room["type"] == 2 && (room["guild_id"].is_null() || room["guild_id"] == self.guild)
        });
        if data["unavailable"] == true || !ordinary || rooms.next().is_some() {
            return false;
        }
        let Some(states) = data["voice_states"].as_array() else {
            return false;
        };
        let mut seen = std::collections::BTreeSet::new();
        for state in states {
            let Some(user) = state["user_id"].as_str().filter(|id| snowflake(id)) else {
                return false;
            };
            let Some(channel) = state["channel_id"].as_str().filter(|id| snowflake(id)) else {
                return false;
            };
            if !seen.insert(user)
                || (!state["guild_id"].is_null() && state["guild_id"] != self.guild)
            {
                return false;
            }
            if user == self.bot {
                self.bot_room = Some(channel.into());
                self.bot_session = state["session_id"].as_str().map(str::to_owned);
            } else if channel == self.room {
                let fallback = data["members"]
                    .as_array()
                    .and_then(|members| members.iter().find(|member| member["user"]["id"] == user));
                let Some(presence) = presence(state, fallback) else {
                    return false;
                };
                self.occupants.insert(user.into(), presence);
            }
        }
        true
    }

    fn voice(&mut self, data: &Value) -> Change {
        let user = data["user_id"].as_str().filter(|id| snowflake(id));
        let channel = data["channel_id"].as_str().filter(|id| snowflake(id));
        if user.is_none()
            || (channel.is_none() && !data.get("channel_id").is_some_and(Value::is_null))
        {
            self.anchor = false;
            self.invalidate_retry();
            return self.end();
        }
        let user = user.unwrap();
        if user == self.bot {
            let previous = self.bot_room.replace(channel.unwrap_or_default().into());
            if channel.is_none() {
                self.bot_room = None;
                self.bot_session = None;
                let ended = self.end();
                self.awaiting_leave = false;
                self.leave_sent = false;
                return ended;
            }
            let session = data["session_id"].as_str().filter(|id| !id.is_empty());
            if channel != Some(&self.room)
                || !unmuted(data)
                || session.is_none()
                || previous.is_some() && self.bot_session.as_deref() != session
            {
                return self.end();
            }
            self.bot_session = session.map(str::to_owned);
            return self
                .active
                .as_ref()
                .and_then(|active| active.joined_after)
                .filter(|&joined| self.sequence > joined)
                .map_or(Change::Ignore, |_| {
                    Change::Voice(json!({
                        "t":"VOICE_STATE_UPDATE", "d":data
                    }))
                });
        }
        if channel != Some(&self.room) {
            let removed = self.occupants.remove(user).is_some();
            if removed {
                self.invalidate_retry();
                self.roster_version += 1;
                if let Some((_, consent)) = &mut self.team {
                    consent.remove(user);
                }
            }
            if user == self.owner && removed {
                self.attempted = false;
            }
            return if removed { self.end() } else { Change::Ignore };
        }
        let Some(next) = presence(data, None) else {
            self.anchor = false;
            self.invalidate_retry();
            return self.end();
        };
        if self.occupants.get(user) == Some(&next) {
            return Change::Ignore;
        }
        self.invalidate_retry();
        self.roster_version += 1;
        if let Some((_, consent)) = &mut self.team {
            consent.remove(user);
        }
        self.occupants.insert(user.into(), next);
        self.end()
    }
}

fn voice_state(guild: &str, channel: Value) -> Value {
    json!({"op":4,"d":{"guild_id":guild,"channel_id":channel,"self_mute":false,"self_deaf":false}})
}

fn unmuted(data: &Value) -> bool {
    ["mute", "deaf", "self_mute", "self_deaf", "suppress"]
        .into_iter()
        .all(|flag| data[flag] == false)
}

fn presence(data: &Value, fallback: Option<&Value>) -> Option<Presence> {
    let user = data["user_id"].as_str()?;
    let member = data.get("member").or(fallback);
    let human = member.is_some_and(|member| {
        member["user"]["id"] == user
            && ["bot", "system"]
                .into_iter()
                .all(|field| member["user"].get(field).is_none_or(|value| value == false))
    });
    let session = data["session_id"].as_str().filter(|id| !id.is_empty())?;
    Some(Presence {
        allowed: human && unmuted(data),
        session: session.into(),
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn voice(sequence: u64, user: &str, room: Value, mute: bool) -> Value {
        json!({
            "t":"VOICE_STATE_UPDATE", "s":sequence,
            "d":{"guild_id":"1","channel_id":room,"user_id":user,"session_id":format!("session-{user}"),
                 "mute":mute,"deaf":false,"self_mute":false,"self_deaf":false,"suppress":false,
                 "member":{"user":{"id":user,"bot":user == "3"}}}
        })
    }

    fn snapshot(sequence: u64, kind: u8) -> Value {
        let owner = voice(sequence, "4", json!("2"), false);
        json!({"t":"GUILD_CREATE","s":sequence,
            "d":{"id":"1","channels":[{"id":"2","type":kind}],"voice_states":[owner["d"]]}})
    }

    fn server(sequence: u64) -> Value {
        json!({"t":"VOICE_SERVER_UPDATE","s":sequence,
            "d":{"guild_id":"1","endpoint":"voice.invalid","token":"never-persist"}})
    }

    #[test]
    fn one_dave_rekey_waits_for_bot_leave_and_keeps_the_previous_idle_cutoff() {
        let now = Instant::now();
        let mut room = Room::new("1", "2", "4", "3").unwrap();
        room.event(&snapshot(1, 2));
        let generation = room.begin(now).unwrap();
        room.join(generation, now).unwrap();
        room.event(&voice(2, "3", json!("2"), false));
        assert!(room.ready(generation, now));
        let later = now + Duration::from_secs(119);
        assert!(room.rekey(later));
        assert!(!room.forward(generation, "4", false, later));
        assert!(room.leave().is_some());
        assert!(room.begin(later).is_none());
        room.event(&voice(3, "3", Value::Null, false));
        let restarted = room.begin(later).unwrap();
        assert!(restarted > generation);
        room.join(restarted, later).unwrap();
        room.event(&voice(4, "3", json!("2"), false));
        assert!(room.ready(restarted, later));
        assert!(!room.rekey(later));
        assert!(room.expire(now + IDLE));
        assert!(!room.forward(restarted, "4", false, now + IDLE));
    }

    #[test]
    fn expired_consent_survives_a_fresh_snapshot_with_the_same_owner() {
        let now = Instant::now();
        let mut room = Room::new("1", "2", "4", "3").unwrap();
        room.event(&snapshot(1, 2));
        let id = room.begin(now).unwrap();
        room.join(id, now).unwrap();
        room.event(&voice(2, "3", json!("2"), false));
        assert!(room.ready(id, now));
        assert!(room.expire(now + MAX_CALL));
        assert!(room.leave().is_some());
        room.event(&voice(3, "3", Value::Null, false));
        room.event(&snapshot(4, 2));
        assert!(room.begin(now + MAX_CALL).is_none());
        room.event(&voice(5, "4", Value::Null, false));
        room.event(&voice(6, "4", json!("2"), false));
        assert!(room.begin(now + MAX_CALL).is_some());
    }

    #[test]
    fn requires_fresh_ordinary_room_and_fences_old_generations() {
        let now = Instant::now();
        let mut room = Room::new("1", "2", "4", "3").unwrap();
        room.event(&voice(1, "4", json!("2"), false));
        room.event(&snapshot(2, 13));
        assert_eq!(room.begin(now), None);
        room.event(&snapshot(3, 2));
        let first = room.begin(now).unwrap();
        assert!(matches!(room.event(&server(4)), Change::Ignore));
        assert!(room.join(first, now).is_some());
        assert!(
            matches!(room.event(&server(5)), Change::Voice(frame) if frame["d"]["guild_id"] == "1")
        );
        assert!(!room.ready(first, now));
        assert!(matches!(
            room.event(&voice(6, "3", json!("2"), false)),
            Change::Voice(_)
        ));
        assert!(room.ready(first, now));
        assert!(room.forward(first, "4", true, now));
        assert!(!room.forward(first, "3", true, now));
        assert!(matches!(
            room.event(&voice(7, "5", json!("2"), false)),
            Change::End
        ));
        assert!(!room.forward(first, "4", false, now));
        assert_eq!(room.leave().unwrap()["d"]["channel_id"], Value::Null);
        room.event(&voice(8, "5", Value::Null, false));
        room.event(&voice(9, "4", Value::Null, false));
        room.event(&voice(10, "4", json!("2"), false));
        assert_eq!(room.begin(now), None);
        room.event(&voice(11, "3", Value::Null, false));
        let second = room.begin(now).unwrap();
        assert!(second > first);
        assert!(room.join(second, now).is_some());
        assert!(matches!(room.event(&server(5)), Change::Ignore));
        room.event(&voice(12, "3", json!("2"), false));
        assert!(room.ready(second, now));
        assert!(!room.forward(first, "4", false, now));
        assert!(matches!(
            room.event(&json!({"t":"GUILD_ROLE_UPDATE","s":13,"d":{"guild_id":"1"}})),
            Change::End
        ));
        assert!(!room.forward(second, "4", false, now));
        room.suspend();
        assert!(room.leave().is_some());
        assert!(room.leave().is_none());
        room.event(&snapshot(14, 2));
        assert_eq!(room.begin(now), None);
    }

    #[test]
    fn mute_and_exact_inactivity_cutoff_revoke_media() {
        let now = Instant::now();
        let mut room = Room::new("1", "2", "4", "3").unwrap();
        room.event(&snapshot(1, 2));
        room.event(&voice(2, "4", json!("2"), true));
        assert_eq!(room.begin(now), None);
        room.event(&voice(3, "4", json!("2"), false));
        let id = room.begin(now).unwrap();
        room.join(id, now).unwrap();
        room.event(&voice(4, "3", json!("2"), false));
        assert!(room.ready(id, now));
        assert!(room.forward(id, "4", true, now + IDLE - Duration::from_secs(1)));
        assert!(!room.expire(now + IDLE));
        assert!(!room.forward(id, "4", true, now + IDLE * 2 - Duration::from_secs(1)));
        assert!(room.expire(now + IDLE * 2 - Duration::from_secs(1)));
        assert_eq!(room.leave().unwrap()["d"]["channel_id"], Value::Null);
    }
}
