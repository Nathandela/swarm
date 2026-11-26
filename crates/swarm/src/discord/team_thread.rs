//! Installed guild thread route for the common dispatcher.
use super::{
    config::{Config, invalid, snowflake},
    dispatcher::Audience,
    receipt::Inbox,
    rest::{Discord, Post},
};
use crate::supervisor::mail_files::Captured;
use serde_json::{Value, json};
use std::{
    io,
    path::{Path, PathBuf},
};

pub(super) fn linked_values(cfg: &Config) -> impl Iterator<Item = &Value> {
    std::iter::once(&cfg.0["team_discussion"])
        .filter(|value| !value.is_null())
        .chain(cfg.0["team_discussions"].as_array().into_iter().flatten())
}

#[derive(Clone)]
pub(super) struct Route {
    pub(super) guild: String,
    pub(super) thread: String,
    pub(super) starter: Option<(String, String)>,
}

impl Route {
    pub(super) fn configured(cfg: &Config) -> io::Result<Option<Self>> {
        let route = &cfg.0["team_discussion"];
        if route.is_null() {
            return Ok(None);
        }
        let guild = route["guild_id"].as_str().filter(|id| snowflake(id));
        let thread = route["thread_id"].as_str().filter(|id| snowflake(id));
        let (Some(guild), Some(thread)) = (guild, thread) else {
            return Err(invalid("invalid linked team discussion"));
        };
        if cfg.text("mode") != "teammate"
            || guild == thread
            || !route
                .as_object()
                .is_some_and(|fields| matches!(fields.len(), 2 | 4))
            || !cfg.0["guild_ids"]
                .as_array()
                .is_some_and(|ids| ids.iter().any(|id| id == guild))
        {
            return Err(invalid(
                "linked team discussion is outside the installation",
            ));
        }
        let starter = match (
            route["starter_channel_id"].as_str(),
            route["starter_revision"].as_str(),
        ) {
            (None, None) if route.as_object().is_some_and(|fields| fields.len() == 2) => None,
            (Some(parent), Some(revision))
                if snowflake(parent)
                    && parent != thread
                    && revision.len() == 64
                    && revision.bytes().all(|byte| byte.is_ascii_hexdigit()) =>
            {
                Some((parent.into(), revision.into()))
            }
            _ => return Err(invalid("invalid linked team starter")),
        };
        Ok(Some(Self {
            guild: guild.into(),
            thread: thread.into(),
            starter,
        }))
    }

    pub(super) fn additional(cfg: &Config) -> io::Result<Vec<(Self, Config)>> {
        let configured = &cfg.0["team_discussions"];
        if configured.is_null() {
            return Ok(Vec::new());
        }
        let entries = configured
            .as_array()
            .filter(|entries| entries.len() <= 16)
            .ok_or_else(|| invalid("too many or malformed additional team discussions"))?;
        let mut seen = std::collections::BTreeSet::new();
        if let Some(primary) = Self::configured(cfg)? {
            seen.insert((primary.guild, primary.thread));
        }
        entries
            .iter()
            .map(|entry| {
                let mut selected = cfg.clone();
                selected.0["team_discussion"] = entry.clone();
                selected.0["team_discussions"] = Value::Null;
                let route = Self::configured(&selected)?
                    .ok_or_else(|| invalid("missing additional team discussion"))?;
                if !seen.insert((route.guild.clone(), route.thread.clone())) {
                    return Err(invalid("duplicate linked team discussion"));
                }
                Ok((route, selected))
            })
            .collect()
    }

    pub(super) fn directory(&self, place: &Path) -> io::Result<PathBuf> {
        let digest = super::receipt::hash(&json!([self.guild, self.thread]))?;
        Ok(place.join(format!("d-{}", &digest[..16])))
    }

    pub(super) fn accepts_voice(&self, cfg: &Config) -> bool {
        let calls = &cfg.0["calls"];
        calls["mode"] == "team"
            && calls["guild_id"] == self.guild
            && calls["discussion_id"] == self.thread
    }

    pub(super) fn identity(&self) -> Value {
        json!({"guild_id":self.guild,"thread_id":self.thread})
    }

    pub(super) fn matches(&self, message: &Value) -> bool {
        message["guild_id"] == self.guild && message["channel_id"] == self.thread
    }

    fn current(
        &self,
        cfg: &Config,
        id: &str,
        mut lookup: impl FnMut(&str) -> io::Result<Option<Value>>,
    ) -> io::Result<Option<Value>> {
        if !snowflake(id) {
            return Ok(None);
        }
        if !self.verify(cfg, &mut lookup, &[])? {
            return Ok(None);
        }
        let starter = self.starter.as_ref().filter(|_| id == self.thread);
        let source = starter.map_or(self.thread.as_str(), |(parent, _)| parent);
        let Some(mut value) = lookup(&format!("/channels/{source}/messages/{id}"))? else {
            return Ok(None);
        };
        if value["id"] != id
            || value["channel_id"] != source
            || !value["guild_id"].is_null() && value["guild_id"] != self.guild
            || starter.is_some_and(|(_, expected)| {
                super::thread_creation::revision(&value).ok().as_ref() != Some(expected)
            })
        {
            return Ok(None);
        }
        if starter.is_some() {
            value["source_channel_id"] = json!(source);
            value["channel_id"] = json!(self.thread);
        }
        let Some(actor) = value["author"]["id"]
            .as_str()
            .filter(|id| *id != cfg.text("application_id"))
        else {
            return Ok(None);
        };
        if !self.verify(cfg, lookup, &[actor])? {
            return Ok(None);
        }
        value["guild_id"] = json!(self.guild);
        Ok(Some(value))
    }

    pub(super) fn verify(
        &self,
        cfg: &Config,
        get: impl FnMut(&str) -> io::Result<Option<Value>>,
        users: &[&str],
    ) -> io::Result<bool> {
        Ok(self
            .access(cfg, get, users)?
            .is_some_and(|access| access.can_read_history))
    }

    fn access(
        &self,
        cfg: &Config,
        mut get: impl FnMut(&str) -> io::Result<Option<Value>>,
        users: &[&str],
    ) -> io::Result<Option<ThreadAccess>> {
        let Some(thread) = get(&format!("/channels/{}", self.thread))? else {
            return Ok(None);
        };
        let Some(parent) = thread["parent_id"]
            .as_str()
            .filter(|id| snowflake(id) && *id != self.thread)
        else {
            return Ok(None);
        };
        if thread["id"] != self.thread
            || thread["guild_id"] != self.guild
            || !matches!(thread["type"].as_u64(), Some(10..=12))
            || self
                .starter
                .as_ref()
                .is_some_and(|(expected, _)| expected != parent)
            || thread["thread_metadata"]["archived"] != false
            || thread["thread_metadata"]["locked"] != false
            || cfg.0["channel_ids"].as_array().is_some_and(|ids| {
                !ids.is_empty() && !ids.iter().any(|id| id == &self.thread || id == parent)
            })
        {
            return Ok(None);
        }
        let Some(container) = get(&format!("/channels/{parent}"))? else {
            return Ok(None);
        };
        if container["id"] != parent
            || container["guild_id"] != self.guild
            || !matches!(container["type"].as_u64(), Some(0 | 5 | 15 | 16))
        {
            return Ok(None);
        }
        const ADMIN: u64 = 1 << 3;
        const VIEW_AND_SEND: u64 = (1 << 10) | (1 << 38);
        const HISTORY: u64 = 1 << 16;
        let mut can_read_history = true;
        let bot = cfg.text("application_id");
        for user in std::iter::once(bot).chain(users.iter().copied()) {
            if !snowflake(user) {
                return Ok(None);
            }
            let Some(member) = get(&format!("/guilds/{}/members/{user}", self.guild))? else {
                return Ok(None);
            };
            if member["user"]["id"] != user
                || member["user"]["system"] == true
                || (user == bot && member["user"]["bot"] != true)
                || (user != bot
                    && !member["user"]["bot"].is_null()
                    && member["user"]["bot"] != false)
            {
                return Ok(None);
            }
            let bits = super::guild::member_permissions(&mut get, parent, user)?;
            if bits & ADMIN == 0 && bits & VIEW_AND_SEND != VIEW_AND_SEND {
                return Ok(None);
            }
            can_read_history &= bits & (ADMIN | HISTORY) != 0;
            if thread["type"] == 12 {
                let Some(row) = get(&format!("/channels/{}/thread-members/{user}", self.thread))?
                else {
                    return Ok(None);
                };
                if row["id"] != self.thread || row["user_id"] != user {
                    return Ok(None);
                }
            }
        }
        Ok(Some(ThreadAccess {
            parent: parent.into(),
            can_read_history,
        }))
    }
}

pub(super) struct ThreadAccess {
    pub(super) parent: String,
    pub(super) can_read_history: bool,
}

pub(super) struct ThreadAudience<'a> {
    pub(super) route: Route,
    pub(super) api: &'a Discord,
}

impl ThreadAudience<'_> {
    pub(super) fn access(&self, cfg: &Config) -> io::Result<ThreadAccess> {
        self.route
            .access(cfg, |path| super::guild::lookup(self.api, path), &[])?
            .ok_or_else(|| invalid("linked team thread is no longer accessible"))
    }

    pub(super) fn history(
        &self,
        cfg: &Config,
        parent: &str,
        before: &str,
    ) -> io::Result<Vec<Value>> {
        let access = self.access(cfg)?;
        if !access.can_read_history || access.parent != parent {
            return Err(invalid("linked team history authority changed"));
        }
        let suffix = if before.is_empty() {
            String::new()
        } else if snowflake(before) {
            format!("&before={before}")
        } else {
            return Err(invalid("invalid thread history cursor"));
        };
        let data = self.api.request(
            &format!("/channels/{}/messages?limit=100{suffix}", self.route.thread),
            None,
        )?;
        let mut rows = data
            .as_array()
            .ok_or_else(|| invalid("invalid thread history page"))?
            .clone();
        for row in &mut rows {
            if row["channel_id"] != self.route.thread
                || !row["guild_id"].is_null() && row["guild_id"] != self.route.guild
                || !row["id"].as_str().is_some_and(snowflake)
            {
                return Err(invalid("thread history returned another audience"));
            }
            row["guild_id"] = json!(self.route.guild);
        }
        Ok(rows)
    }
}

impl Audience for ThreadAudience<'_> {
    fn current(&self, cfg: &Config, id: &str) -> io::Result<Option<Value>> {
        self.route
            .current(cfg, id, |path| super::guild::lookup(self.api, path))
    }

    fn verify(&self, cfg: &Config) -> io::Result<()> {
        if self
            .route
            .verify(cfg, |path| super::guild::lookup(self.api, path), &[])?
        {
            Ok(())
        } else {
            Err(invalid("linked team thread is no longer accessible"))
        }
    }

    fn admit(&self, inbox: &mut Inbox, source: &Value) -> io::Result<Option<Value>> {
        inbox.admit_team(&self.route, source)
    }

    fn post(&self, _: &Config, nonce: &str, body: &str, files: &[Captured]) -> Post {
        self.api
            .post_publication_channel(&self.route.thread, nonce, body, files)
    }
}

pub(super) fn explicit(cfg: &Config, source: &Value) -> bool {
    let bot = cfg.text("application_id");
    source["mentions"]
        .as_array()
        .is_some_and(|mentions| mentions.iter().any(|mention| mention["id"] == bot))
        && source["content"].as_str().is_some_and(|text| {
            [format!("<@{bot}>"), format!("<@!{bot}>")]
                .iter()
                .any(|prefix| {
                    text.trim_start().strip_prefix(prefix).is_some_and(|tail| {
                        (!tail.trim().is_empty()
                            || super::received_files::visible(source)
                                .is_ok_and(|visible| visible.has_files() || visible.is_forwarded()))
                            && !matches!(
                                tail.trim(),
                                "voice join" | "voice consent" | "voice leave"
                            )
                    })
                })
        })
}

pub(super) fn control(cfg: &Config, message: &Value) -> bool {
    let bot = cfg.text("application_id");
    message["content"].as_str().is_some_and(|text| {
        [format!("<@{bot}> "), format!("<@!{bot}> ")]
            .iter()
            .any(|prefix| {
                text.trim().strip_prefix(prefix).is_some_and(|tail| {
                    matches!(tail, "voice join" | "voice consent" | "voice leave")
                })
            })
    })
}

pub(super) fn ingest(
    cfg: &Config,
    inbox: &mut Inbox,
    thread: &ThreadAudience<'_>,
    event: &Value,
) -> io::Result<bool> {
    let message = &event["d"];
    if !thread.route.matches(message) {
        return Ok(false);
    }
    if super::guild::address(cfg, message).is_some() {
        // Existing explicit native worker requests keep the worker ingress.
        return Ok(false);
    }
    let kind = event["t"].as_str().unwrap_or_default();
    if kind == "MESSAGE_DELETE_BULK" {
        if let Some(ids) = message["ids"].as_array() {
            for id in ids.iter().filter_map(Value::as_str) {
                if inbox.has_team_source(id) {
                    inbox.withdraw(id)?;
                }
            }
        }
        return Ok(true);
    }
    let Some(id) = message["id"].as_str().filter(|id| snowflake(id)) else {
        return Ok(false);
    };
    if kind == "MESSAGE_DELETE" || control(cfg, message) {
        if inbox.has_team_source(id) {
            inbox.withdraw(id)?;
        }
        return Ok(true);
    }
    if message["author"]["id"] == cfg.text("application_id") {
        if kind == "MESSAGE_CREATE" {
            inbox.observe(message, super::receipt::Observation::Gateway)?;
        }
        return Ok(true);
    }
    if kind == "MESSAGE_UPDATE" && !inbox.has_team_source(id) {
        return Ok(false);
    }
    if kind != "MESSAGE_UPDATE" && kind != "MESSAGE_CREATE" {
        return Ok(false);
    }
    if let Some(current) = thread.current(cfg, id)?
        && !control(cfg, &current)
        && (!inbox.manager().is_null() || explicit(cfg, &current))
        && thread.admit(inbox, &current)?.is_some()
    {
        return Ok(true);
    }
    if inbox.has_team_source(id) {
        inbox.withdraw(id)?;
    }
    Ok(true)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::supervisor::runtime;

    #[test]
    fn separately_linked_discussions_resolve_and_verify_their_own_native_custody() {
        use super::super::config::Place;
        let place =
            std::env::temp_dir().join(format!("swarm-linked-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&place).unwrap();
        let place = place.canonicalize().unwrap();
        let cfg = Config(
            json!({"enabled":true,"mode":"teammate","manager_cli":"codex","installation_id":"fixture",
            "application_id":"3","owner_id":"4","guild_ids":["1"],"daemon_socket":"/tmp/supervisor-scope",
            "team_discussion":{"guild_id":"1","thread_id":"2"},
            "team_discussions":[{"guild_id":"1","thread_id":"8"},{"guild_id":"1","thread_id":"9"}]}),
        );
        let mut inputs = Vec::new();
        let places = || vec![(Place::fixture(place.clone()), cfg.clone())];
        for (route, selected) in Route::additional(&cfg).unwrap() {
            let directory = route.directory(&place).unwrap();
            runtime::private_dir(&directory).unwrap();
            let scope = super::super::manager_scope_for_directory(places(), &directory).unwrap();
            assert_eq!(scope["channel_id"], route.thread);
            assert_eq!(scope["call_place"], json!(place));
            let mut inbox = Inbox::open(&directory, &selected).unwrap();
            inbox.checkpoint("").unwrap();
            let id = format!("10{}", route.thread);
            let source = json!({"id":id,"type":0,"channel_id":route.thread,"guild_id":"1",
                "author":{"id":"5"},"attachments":[],"content":"qualified ordinary request"});
            let admitted = inbox.admit_team(&route, &source).unwrap().unwrap();
            let binding = json!({"provider":"codex","session":"native-scope"});
            let composer = json!({"session":"native-scope","text":admitted["text"],
                "origin":{"kind":"team_participant","actor_id":"5","guild_id":"1","message_id":id,
                    "revision":admitted["revision"],"request_id":admitted["operation_id"]},
                "manager_admission":{"nonce":"a".repeat(64),"binding":binding}});
            let state = json!({"generation":1,"launch":scope["launch"],"session":"native-scope","binding":binding,
                "input":{"operation":"same-test-operation","composer":composer,"source_id":id}});
            inbox
                .cutover(&id, admitted["revision"].as_str().unwrap(), state)
                .unwrap();
            crate::discord::verify_connector_input(&scope, "same-test-operation", &composer, None)
                .unwrap();
            inputs.push((scope, composer));
        }
        assert_ne!(
            inputs[0].0["launch"]["manager"]["conversation_id"],
            inputs[1].0["launch"]["manager"]["conversation_id"]
        );
        assert!(
            crate::discord::verify_connector_input(
                &inputs[1].0,
                "same-test-operation",
                &inputs[0].1,
                None
            )
            .is_err()
        );
        assert!(
            super::super::manager_scope_for_directory(places(), &place.join("d-unenrolled"))
                .is_err()
        );
        let mut duplicate = cfg;
        duplicate.0["team_discussions"][1] = duplicate.0["team_discussions"][0].clone();
        assert!(Route::additional(&duplicate).is_err());
        std::fs::remove_dir_all(place).unwrap();
    }

    #[test]
    fn private_membership_is_required_even_for_moderators_and_the_bot() {
        let cfg = Config(
            json!({"mode":"teammate","application_id":"3","guild_ids":["1"],
            "team_discussion":{"guild_id":"1","thread_id":"2"}}),
        );
        let route = Route::configured(&cfg).unwrap().unwrap();
        let verify = |kind, missing: &str| {
            route
                .verify(
                    &cfg,
                    |path| {
                        let row = match path {
                            "/channels/2" => {
                                json!({"id":"2","guild_id":"1","parent_id":"4","type":kind,
                    "thread_metadata":{"archived":false,"locked":false}})
                            }
                            "/channels/4" => json!({"id":"4","guild_id":"1","type":0}),
                            _ if path.starts_with("/guilds/1/members/") => {
                                let id = path.rsplit('/').next().unwrap();
                                json!({"user":{"id":id,"bot":id=="3"}})
                            }
                            _ if path.contains("/permissions/member/") => {
                                json!({"permissions":"8"})
                            }
                            _ if path.starts_with("/channels/2/thread-members/") => {
                                let id = path.rsplit('/').next().unwrap();
                                if id == missing {
                                    return Ok(None);
                                }
                                json!({"id":"2","user_id":id})
                            }
                            _ => panic!("unexpected lookup {path}"),
                        };
                        Ok(Some(row))
                    },
                    &["9"],
                )
                .unwrap()
        };
        assert!(verify(12, ""));
        assert!(!verify(12, "3"));
        assert!(!verify(12, "9"));
        assert!(verify(11, "9"));
    }

    #[test]
    fn enrolled_controls_refuse_deleted_cards_and_revoked_private_members() {
        use crate::ipc::field;
        struct Live<'a> {
            route: &'a Route,
            card: Value,
            missing: bool,
            author_is_member: bool,
        }
        impl Audience for Live<'_> {
            fn current(&self, cfg: &Config, id: &str) -> io::Result<Option<Value>> {
                self.route.current(cfg, id, |path| {
                    let row = match path {
                        "/channels/2" => json!({"id":"2","guild_id":"1","parent_id":"4","type":12,
                            "thread_metadata":{"archived":false,"locked":false}}),
                        "/channels/4" => json!({"id":"4","guild_id":"1","type":0}),
                        "/channels/2/messages/101" if self.missing => return Ok(None),
                        "/channels/2/messages/101" => self.card.clone(),
                        _ if path.starts_with("/guilds/1/members/") => {
                            let user = path.rsplit('/').next().unwrap();
                            json!({"user":{"id":user,"bot":user=="3"}})
                        }
                        _ if path.contains("/permissions/member/") => json!({"permissions":"8"}),
                        "/channels/2/thread-members/9" if !self.author_is_member => {
                            return Ok(None);
                        }
                        _ if path.starts_with("/channels/2/thread-members/") => {
                            json!({"id":"2","user_id":path.rsplit('/').next().unwrap()})
                        }
                        _ => panic!("{path}"),
                    };
                    Ok(Some(row))
                })
            }
            fn verify(&self, _: &Config) -> io::Result<()> {
                Ok(())
            }
            fn admit(&self, inbox: &mut Inbox, source: &Value) -> io::Result<Option<Value>> {
                inbox.admit_team(self.route, source)
            }
            fn post(&self, _: &Config, _: &str, _: &str, _: &[Captured]) -> Post {
                panic!("no effect after failed grant")
            }
        }
        let cfg = Config(
            json!({"mode":"teammate","application_id":"3","owner_id":"7","guild_ids":["1"],
            "team_discussion":{"guild_id":"1","thread_id":"2"},"installation_id":"fixture","daemon_socket":"/tmp/team-worker"}),
        );
        let route = Route::configured(&cfg).unwrap().unwrap();
        let root = std::env::temp_dir().join(format!(
            "swarm-enrolled-source-{}",
            runtime::random_id().unwrap()
        ));
        runtime::private_dir(&root).unwrap();
        let mut inbox = Inbox::open(&root, &cfg).unwrap();
        inbox.checkpoint("100").unwrap();
        let card = json!({"id":"101","guild_id":"1","channel_id":"2","type":0,
            "author":{"id":"9","bot":false},"content":"Use our scoped worker","attachments":[]});
        let retained = inbox.admit_team(&route, &card).unwrap().unwrap();
        let nonce = "a".repeat(64);
        let binding = json!({"session":"ep-fixture/manager","native_session_id":"native"});
        let mut state = json!({"binding":binding,"input":{"source_id":"101","composer":{
            "origin":{"message_id":"101","actor_id":"9","channel_id":"2","revision":retained["revision"]},
            "manager_admission":{"nonce":nonce,"binding":binding}
        }}});
        inbox
            .cutover("101", field(&retained, "revision"), state.clone())
            .unwrap();
        state["input"]["turn_id"] = json!("fixture-native-turn");
        inbox.settled_input("101", state.clone()).unwrap();
        let verify = |inbox: &mut Inbox, input: &Value, missing, author_is_member| {
            let api = Live {
                route: &route,
                card: card.clone(),
                missing,
                author_is_member,
            };
            // A bot-only check stays green in all cases.
            assert!(api.verify(&cfg).is_ok());
            super::super::dispatcher::enrolled_source(&cfg, inbox, &api, input)
        };
        let input = &state["input"];
        assert!(verify(&mut inbox, input, false, true).is_ok());
        assert!(verify(&mut inbox, input, true, true).is_err());
        assert!(verify(&mut inbox, input, false, false).is_err());
        let mut wake = input.clone();
        wake["worker_wake"] = json!({"owner_revision":retained["revision"]});
        wake["composer"]["origin"]["revision"] = json!("distinct-mail-wake-revision");
        assert!(verify(&mut inbox, &wake, false, true).is_ok());
        assert!(verify(&mut inbox, &wake, true, true).is_err());
        assert!(verify(&mut inbox, &wake, false, false).is_err());
        wake["source_id"] = json!("voice-worker-wake");
        assert!(verify(&mut inbox, &wake, false, true).is_err());
        std::fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn linked_thread_uses_the_original_participant_and_native_custody() {
        let dir =
            std::env::temp_dir().join(format!("swarm-team-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&dir).unwrap();
        let cfg = Config(json!({"mode":"teammate","installation_id":"fixture",
            "application_id":"3","owner_id":"4","daemon_socket":"/tmp/team-native",
            "guild_ids":["1"],"team_discussion":{"guild_id":"1","thread_id":"2"}}));
        let route = Route::configured(&cfg).unwrap().unwrap();
        let manager = super::super::manager::Manager::new(&cfg).unwrap();
        let auth = dir.join("manager-v1/bridge.auth");
        let launch = manager.launch_request(&auth.with_file_name("bridge.sock"), &auth);
        let scope = json!({"launch":launch,"installation_id":"fixture","application_id":"3",
            "owner_id":"4","channel_id":"2","guild_id":"1","supervisor_socket":"/tmp/team-native"});
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        inbox.checkpoint("100").unwrap();
        let source = |id, author| {
            json!({"guild_id":"1","channel_id":"2","id":id,"type":0,
            "author":{"id":author},"attachments":[],"content":"thread marker"})
        };
        let unrelated = {
            let mut message = source("101", "5");
            message["channel_id"] = json!("99");
            message
        };
        assert!(inbox.admit_team(&route, &unrelated).unwrap().is_none());
        let first = inbox
            .admit_team(&route, &source("101", "5"))
            .unwrap()
            .unwrap();
        let second = inbox
            .admit_team(&route, &source("102", "6"))
            .unwrap()
            .unwrap();
        assert_eq!(first["author"]["id"], "5");
        assert_eq!(second["author"]["id"], "6");
        let binding = json!({"session":"s","session_instance":"instance",
            "conversation_id":launch["manager"]["conversation_id"]});
        let composer = manager
            .composer(
                &first,
                &json!({"ready_for_input":true,"binding":binding,
            "turn":{"state":"idle","external_input":0}}),
                "",
            )
            .unwrap();
        assert_eq!(composer["origin"]["guild_id"], "1");
        assert_eq!(composer["origin"]["actor_id"], "5");
        assert_eq!(composer["origin"]["requester_id"], "5");
        inbox.cutover("101",first["revision"].as_str().unwrap(),json!({"generation":1,"launch":launch,
            "session":"s","binding":binding,"input":{"operation":"fixture","source_id":"101","composer":composer}})).unwrap();
        crate::discord::verify_connector_input(&scope, "fixture", &composer, None).unwrap();
        assert!(
            crate::discord::verify_connector_input(&scope, "another", &composer, None).is_err()
        );
        assert_eq!(
            runtime::read_json(&dir.join("rust-text-v1.json")).unwrap()["team_discussion"],
            route.identity()
        );
        let (_, mut proof) = super::super::receipt::fixture_publication("reply");
        proof["audience"] = route.identity();
        let nonce = super::super::receipt::publication_nonce(&proof).unwrap();
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        let state = inbox.manager().clone();
        inbox.prepare_publication(&nonce, proof, state).unwrap();
        inbox.publication_state(&nonce, "sending", None).unwrap();
        let mut reply = json!({"id":"103","guild_id":"1","channel_id":"99","author":{"id":"3","bot":true},
            "nonce":nonce,"content":"reply","attachments":[]});
        assert!(
            !inbox
                .observe(&reply, super::super::receipt::Observation::Gateway)
                .unwrap()
        );
        reply["channel_id"] = json!("2");
        assert!(
            inbox
                .observe(&reply, super::super::receipt::Observation::Gateway)
                .unwrap()
        );
        std::fs::remove_dir_all(dir).unwrap();
    }
}
