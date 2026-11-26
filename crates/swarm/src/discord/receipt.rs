//! Durable connector inbox for one enrolled personal installation.
use super::config::{Config, invalid, snowflake};
use crate::supervisor::runtime;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::io;
use std::path::{Path, PathBuf};

const VERSION: u64 = 2;
const VOICE_VERSION: u64 = 3;
const MAX_RECORDS: usize = 2048;
const MAX_STORE_BYTES: usize = 1 << 20;

pub(super) enum Observation {
    Gateway,
    History,
    Rest,
}

pub(super) struct Inbox {
    file: PathBuf,
    data: Value,
}

fn certify_attachment_source(saved: &Value, fresh: &Value) -> io::Result<()> {
    use super::received_files::{LEGACY_PROMPT_HEADER, PROMPT_HEADER, prompt_files, visible};

    let route = &saved["route"];
    let guild = route["guild_id"].as_str().unwrap_or_default();
    let channel = route["channel_id"].as_str().unwrap_or_default();
    let edited = &fresh["edited_timestamp"];
    let changed = || invalid("Discord attachment source revision changed");
    let text = fresh["content"].as_str().ok_or_else(changed)?;
    if fresh["id"] != route["message_id"]
        || fresh["channel_id"] != channel
        || (if guild.is_empty() {
            !fresh["guild_id"].is_null()
        } else {
            fresh["guild_id"] != guild
        })
        || fresh["author"]["id"] != saved["author"]["id"]
        || !fresh["webhook_id"].is_null()
        || fresh["author"]
            .get("bot")
            .is_some_and(|value| value != false)
        || fresh["author"]
            .get("system")
            .is_some_and(|value| value != false)
        || fresh["type"] != 0
        || !fresh["attachments"].is_array()
        || *edited != saved["edited_at"]
    {
        return Err(changed());
    }
    let visible = visible(fresh)?;
    let files = &saved["received_files"];
    let source_channel = fresh["source_channel_id"]
        .as_str()
        .filter(|_| !guild.is_empty())
        .unwrap_or(channel);
    if !visible.has_files() || !visible.matches(files) || !visible.same_cdn_scope(source_channel) {
        return Err(changed());
    }
    let body = format!("{text}{}", visible.context());
    let prompt = format!("{body}{PROMPT_HEADER}{}", prompt_files(files));
    let legacy = format!("{body}{LEGACY_PROMPT_HEADER}{files}");
    let admitted = saved["text"].as_str().ok_or_else(changed)?;
    if (admitted != prompt && admitted != legacy)
        || saved["revision"]
            != hash(&json!([
                fresh["author"]["id"],
                admitted,
                channel,
                guild,
                fresh["id"],
                edited,
                visible.revision()?,
                files
            ]))?
    {
        return Err(changed());
    }
    Ok(())
}

impl Inbox {
    pub(super) fn open(dir: &Path, cfg: &Config) -> io::Result<Self> {
        let file = dir.join("rust-text-v1.json");
        let installation = cfg.text("installation_id");
        let owner = cfg.text("owner_id");
        let app = cfg.text("application_id");
        let channel = cfg.text("dm_channel_id");
        let identity = json!([installation, app, owner, channel, cfg.text("daemon_socket")]);
        let mut data = match runtime::read_json(&file) {
            Ok(data) => data,
            Err(e) if e.kind() == io::ErrorKind::NotFound => {
                let data =
                    json!({"version": VERSION, "identity": identity, "messages": {}, "outbox": {}});
                runtime::write_json(&file, &data)?;
                data
            }
            Err(e) => return Err(e),
        };
        if !matches!(data["version"].as_u64(), Some(VERSION | VOICE_VERSION))
            || data["identity"] != identity
            || !data["messages"].is_object()
            || !data["outbox"].is_object()
            || !valid_boundary(&data["consent_floor"])
            || !valid_boundary(&data["history_cursor"])
            || !valid_boundary(&data["team_history_cursor"])
            || !valid_boundary(&data["team_consent_floor"])
            || data["team_history_cursor"].is_null() != data["team_consent_floor"].is_null()
            || boundary(&data["team_consent_floor"]) > boundary(&data["team_history_cursor"])
            || data["consent_floor"].is_null() != data["history_cursor"].is_null()
            || boundary(&data["consent_floor"]) > boundary(&data["history_cursor"])
        {
            return Err(invalid(
                "Discord inbox cannot prove its schema, enrolled identity and original consent boundary",
            ));
        }
        if data["outbox"]
            .as_object()
            .unwrap()
            .iter()
            .any(|(nonce, entry)| verify_publication(nonce, entry).is_err())
        {
            return Err(invalid(
                "Discord outbox publication evidence failed integrity checks",
            ));
        }
        let team = super::team_thread::Route::configured(cfg)?.map(|route| route.identity());
        if let Some(team) = team {
            if data["team_discussion"].is_null() && data["manager"].is_null() {
                data["team_discussion"] = team.clone();
                runtime::write_json(&file, &data)?;
            }
            if data["team_discussion"] != team {
                return Err(invalid("linked team discussion changed"));
            }
        } else if !data["team_discussion"].is_null() {
            return Err(invalid("linked team discussion is no longer configured"));
        }
        Ok(Self { file, data })
    }

    fn lock_writer(&self) -> io::Result<std::fs::File> {
        super::worker_input::lock_ledger_writer(
            self.file
                .parent()
                .ok_or_else(|| invalid("missing connector ledger directory"))?,
        )
    }

    fn save(&mut self, next: Value) -> io::Result<()> {
        let _writer = self.lock_writer()?;
        self.write_locked(next)
    }

    fn write_locked(&mut self, next: Value) -> io::Result<()> {
        if serde_json::to_vec(&next)?.len() >= MAX_STORE_BYTES {
            return Err(io::Error::other("Discord inbox reached its storage limit"));
        }
        if let Err(err) = runtime::write_json(&self.file, &next) {
            // A directory fsync failure can follow a successful rename. Stop this
            // writer before it can update a stale in-memory snapshot.
            return Err(io::Error::other(format!(
                "Discord inbox could not commit receipt: {err}"
            )));
        }
        self.data = next;
        Ok(())
    }

    pub(super) fn admit(&mut self, source: &Value) -> io::Result<Option<Value>> {
        let channel = self.identity()[3].to_owned();
        self.admit_scoped(source, "", &channel, "", None, false)
    }

    pub(super) fn admit_guild(
        &mut self,
        cfg: &Config,
        source: &Value,
    ) -> io::Result<Option<Value>> {
        let field = |key| source[key].as_str().unwrap_or_default();
        let (guild, channel, parent) = (
            field("guild_id"),
            field("channel_id"),
            field("verified_parent_id"),
        );
        if !super::guild::enabled(cfg)
            || !snowflake(guild)
            || !snowflake(channel)
            || (!parent.is_empty() && (!snowflake(parent) || parent == channel))
        {
            return Ok(None);
        }
        let Some(address) = super::guild::address(cfg, source) else {
            return Ok(None);
        };
        self.admit_scoped(source, guild, channel, parent, Some(&address), false)
    }

    pub(super) fn admit_team(
        &mut self,
        route: &super::team_thread::Route,
        source: &Value,
    ) -> io::Result<Option<Value>> {
        if !source["source_channel_id"].is_null()
            && !route.starter.as_ref().is_some_and(|(parent, _)| {
                source["id"] == route.thread && source["source_channel_id"] == *parent
            })
        {
            return Ok(None);
        }
        if !route.matches(source)
            || self.data["team_discussion"] != route.identity()
            || source["id"].as_str().is_some_and(|id| {
                !self.has_team_source(id)
                    && !(route.starter.is_some() && id == route.thread)
                    && id
                        .parse::<u64>()
                        .is_ok_and(|id| id <= boundary(&self.data["team_consent_floor"]))
            })
        {
            return Ok(None);
        }
        self.admit_scoped(source, &route.guild, &route.thread, "", None, true)
    }

    pub(super) fn has_team_source(&self, id: &str) -> bool {
        self.data["messages"][id]["authority"] == "team_participant"
    }

    pub(super) fn has_guild_source(&self, guild: &str, channel: &str, id: &str) -> bool {
        let route = &self.data["messages"][id]["route"];
        route["guild_id"] == guild && route["channel_id"] == channel
    }

    fn admit_scoped(
        &mut self,
        source: &Value,
        guild: &str,
        channel: &str,
        parent: &str,
        address: Option<&str>,
        team: bool,
    ) -> io::Result<Option<Value>> {
        let [installation, application, owner, _, supervisor] = self.identity();
        let Some(id) = source["id"].as_str().filter(|id| snowflake(id)) else {
            return Ok(None);
        };
        if !self.within_consent(id) {
            return Ok(None);
        }
        let Some(body) = source["content"]
            .as_str()
            .filter(|body| body.len() <= 16384)
        else {
            return Ok(None);
        };
        let actor = if team {
            let Some(actor) = source["author"]["id"]
                .as_str()
                .filter(|actor| snowflake(actor) && *actor != application)
            else {
                return Ok(None);
            };
            actor
        } else {
            owner
        };
        if source["channel_id"] != channel
            || source["author"]["id"] != actor
            || if guild.is_empty() {
                !source["guild_id"].is_null()
            } else {
                source["guild_id"] != guild
            }
            || !source["webhook_id"].is_null()
            || source["author"]
                .get("bot")
                .is_some_and(|value| value != false)
            || source["author"]
                .get("system")
                .is_some_and(|value| value != false)
            || !source["attachments"].is_array()
            || source["type"] != 0
        {
            return Ok(None);
        }
        let edited = &source["edited_timestamp"];
        if !edited.is_null() && !edited.as_str().is_some_and(|stamp| stamp.len() <= 64) {
            return Ok(None);
        }
        if self.data["messages"][id]["state"] == "withdrawn"
            || (self.data["messages"][id]["state"] == "rejected"
                && self.data["messages"][id]["edited_at"] == *edited)
        {
            return Ok(None);
        }
        let visible = match super::received_files::visible(source) {
            Ok(visible) => visible,
            Err(err) if err.kind() == io::ErrorKind::InvalidInput => return Ok(None),
            Err(err) => return Err(err),
        };
        let forwarding = visible.revision()?;
        if visible.is_forwarded()
            && (address.is_some()
                || super::worker::target_text(body).is_some()
                || !guild.is_empty() && !team)
        {
            return Ok(None);
        }
        let context = visible.context();
        let body = format!("{body}{context}");
        if visible.is_forwarded() && body.len() > 4096 {
            return Ok(None);
        }
        let empty = !visible.has_files();
        let (body, files) = if empty {
            if body.trim().is_empty() {
                return Ok(None);
            }
            (body.to_owned(), Value::Null)
        } else {
            if address.is_some()
                || super::worker::target_text(&body).is_some()
                || !guild.is_empty() && !team
            {
                return Ok(None);
            }
            let place = self
                .file
                .parent()
                .ok_or_else(|| invalid("missing attachment custody directory"))?;
            let source_channel = source["source_channel_id"]
                .as_str()
                .filter(|_| team)
                .unwrap_or(channel);
            let old = &self.data["messages"][id];
            let saved = &old["received_files"];
            let legacy_header = super::received_files::LEGACY_PROMPT_HEADER;
            let legacy = saved.is_array() && old["text"] == format!("{body}{legacy_header}{saved}");
            let header = if legacy {
                legacy_header
            } else {
                super::received_files::PROMPT_HEADER
            };
            let Some(budget) = 4096_usize.checked_sub(body.len() + header.len()) else {
                return Ok(None);
            };
            let display = |files: &Value| {
                if legacy {
                    files.clone()
                } else {
                    super::received_files::prompt_files(files)
                }
            };
            let existing_revision = matches!(old["state"].as_str(), Some("accepted" | "sending"))
                || !old.is_null() && old["edited_at"] == *edited;
            let captured = if existing_revision {
                if !saved.is_array()
                    || old["text"] != format!("{body}{header}{}", display(saved))
                    || !visible.matches(saved)
                    || !visible.same_cdn_scope(source_channel)
                {
                    return Ok(None);
                }
                super::received_files::verify_receipt(&place.canonicalize()?, saved)?;
                saved.clone()
            } else {
                match super::received_files::capture(place, source, source_channel, budget) {
                    Ok(files) => json!(files),
                    Err(err) if err.kind() == io::ErrorKind::InvalidInput => return Ok(None),
                    Err(err) => return Err(err),
                }
            };
            let body = format!("{body}{header}{}", display(&captured));
            if body.len() > 4096 {
                return Ok(None);
            }
            (body, captured)
        };
        let revision = hash(&json!([
            actor, body, channel, guild, id, edited, forwarding, files
        ]))?;
        let operation = format!("da_{}", &hash(&json!([installation, id]))?[..32]);
        let mut receipt = json!({
            "operation_id": operation, "revision": revision, "text": body,
            "route": {"guild_id": guild, "channel_id": channel, "message_id": id},
            "author": {"id": actor}, "authority": if team {"team_participant"} else {"owner_direct"},
            "supervisor_socket": supervisor, "state": "prepared", "edited_at": edited
        });
        if !files.is_null() {
            receipt["received_files"] = files;
        }
        if let Some(address) = address {
            let verified = source["audience_verified_at_ms"].as_u64().ok_or_else(|| {
                invalid("guild worker source lacks its scoped permission observation")
            })?;
            receipt["addressed_text"] = json!(address);
            receipt["audience_verified_at_ms"] = json!(verified);
            if source["audience_roles"].is_array() {
                receipt["audience_roles"] = source["audience_roles"].clone();
            }
        }
        if !parent.is_empty() {
            receipt["route"]["parent_id"] = json!(parent);
        }
        if !guild.is_empty() && !team {
            receipt["guild_audience_digest"] =
                json!(super::guild::audience_digest(application, &receipt)?);
        }
        let old = &self.data["messages"][id];
        if old["state"] == "withdrawn" || old == &receipt {
            return Ok((old == &receipt).then(|| old.clone()));
        }
        if !guild.is_empty() && !team && old["state"] == "prepared" {
            let mut comparable = receipt.clone();
            comparable["audience_verified_at_ms"] = old["audience_verified_at_ms"].clone();
            comparable["guild_audience_digest"] = old["guild_audience_digest"].clone();
            if old["audience_roles"].is_null() {
                comparable
                    .as_object_mut()
                    .unwrap()
                    .shift_remove("audience_roles");
            } else {
                comparable["audience_roles"] = old["audience_roles"].clone();
            }
            if &comparable == old {
                let mut next = self.data.clone();
                next["messages"][id] = receipt.clone();
                self.save(next)?;
                return Ok(Some(receipt));
            }
        }
        if !old["worker_operation"].is_null() && old["revision"] != receipt["revision"] {
            self.withdraw(id)?;
            return Ok(None);
        }
        if matches!(old["state"].as_str(), Some("sending" | "accepted"))
            && old["revision"] == receipt["revision"]
            && old["route"] == receipt["route"]
        {
            return Ok(Some(old.clone()));
        }
        if !old.is_null()
            && (old["edited_at"] == *edited
                || edited.is_null()
                || !matches!(old["state"].as_str(), Some("prepared" | "rejected")))
        {
            return Err(invalid(
                "Discord admission conflicts with the persisted source",
            ));
        }
        if old.is_null() && self.data["messages"].as_object().unwrap().len() >= MAX_RECORDS {
            return Err(io::Error::other(
                "Discord inbox reached its admission limit",
            ));
        }
        let mut next = self.data.clone();
        next["messages"][id] = receipt.clone();
        self.save(next)?;
        Ok(Some(receipt))
    }

    pub(super) fn admit_voice(
        &mut self,
        scope: &Value,
        origin: Value,
        text: &str,
    ) -> io::Result<Value> {
        super::call_origin::validate(scope, &origin, text)?;
        let [installation, app, owner, channel, supervisor] = self.identity();
        if scope["installation_id"] != installation
            || scope["application_id"] != app
            || scope["owner_id"] != owner
            || if let Some(guild) = scope["guild_id"].as_str().filter(|id| snowflake(id)) {
                !channel.is_empty()
                    || self.data["team_discussion"]["guild_id"] != guild
                    || self.data["team_discussion"]["thread_id"] != scope["channel_id"]
            } else {
                scope["channel_id"] != channel
            }
            || scope["supervisor_socket"] != supervisor
            || self.cursor().is_none()
        {
            return Err(invalid(
                "voice admission is outside the repaired discussion",
            ));
        }
        let id = origin["request_id"].as_str().unwrap();
        let old = &self.data["messages"][id];
        if !old.is_null() {
            if old["voice_origin"] != origin || old["state"] != "withdrawn" && old["text"] != text {
                return Err(invalid("voice admission conflicts with the original turn"));
            }
            return Ok(old.clone());
        }
        if self.data["messages"].as_object().unwrap().len() >= MAX_RECORDS {
            return Err(invalid("Discord inbox reached its admission limit"));
        }
        let receipt = json!({
            "operation_id":id, "revision":origin["revision"], "text":text,
            "route":{"guild_id":scope["guild_id"].as_str().unwrap_or_default(),"channel_id":scope["channel_id"]},
            "author":{"id":origin["actor_id"]}, "authority":origin["kind"], "supervisor_socket":supervisor,
            "state":"prepared", "voice_origin":origin,
        });
        let mut next = self.data.clone();
        next["version"] = json!(VOICE_VERSION);
        next["messages"][id] = receipt.clone();
        self.save(next)?;
        Ok(receipt)
    }

    pub(super) fn abort_voice(&mut self, id: &str) -> io::Result<()> {
        if !super::call_origin::key(id)
            || self.data["manager"]["input"]["source_id"] != id
            || self.data["messages"][id]["state"] != "sending"
        {
            return Err(invalid("no matching voice cutover to withdraw"));
        }
        let mut next = self.data.clone();
        next["messages"][id]
            .as_object_mut()
            .unwrap()
            .shift_remove("text");
        next["messages"][id]["state"] = json!("withdrawn");
        next["manager"]["input"] = Value::Null;
        self.save(next)
    }

    pub(super) fn begin_draft_retry(&mut self, expected: &Value) -> io::Result<Value> {
        use crate::providers::codex_receipt;

        let input = &expected["input"];
        if self.manager() != expected
            || input["draft_retry"] != "prepared"
            || !input["turn_id"].is_null()
            || !input["composer"].is_object()
            || !input["operation"].is_string()
        {
            return Err(invalid("dispatcher retry is no longer prepared"));
        }
        let _writer = self.lock_writer()?;
        if runtime::read_json(&self.file)? != self.data {
            return Err(invalid("dispatcher retry changed before commit"));
        }
        let mut manager = expected.clone();
        if !codex_receipt::timely(crate::ipc::field(input, "operation")) {
            manager["input"]["operation"] = json!(codex_receipt::operation()?);
        }
        manager["input"]["draft_retry"] = json!("executing");
        let mut next = self.data.clone();
        next["manager"] = manager.clone();
        self.write_locked(next)?;
        Ok(manager)
    }

    pub(super) fn withdraw_draft_retry(&mut self, id: &str) -> io::Result<()> {
        let input = &self.data["manager"]["input"];
        if input["draft_retry"] != "prepared"
            || input["source_id"] != id
            || !input["turn_id"].is_null()
            || !input["worker_wake"].is_null()
        {
            return Err(invalid("no matching prepared dispatcher retry to withdraw"));
        }
        let mut next = self.data.clone();
        if let Some(source) = next["messages"][id].as_object_mut() {
            source.shift_remove("text");
            source.shift_remove("addressed_text");
            source.insert("state".into(), json!("withdrawn"));
        }
        next["manager"]["input"] = Value::Null;
        self.save(next)
    }

    pub(super) fn read_active_attachment(
        &self,
        input: &Value,
        id: &str,
        digest: &str,
        offset: u64,
        current: impl FnOnce(&str) -> io::Result<Option<Value>>,
    ) -> io::Result<Value> {
        let source_id = input["source_id"]
            .as_str()
            .filter(|id| snowflake(id))
            .ok_or_else(|| invalid("no original Discord attachment source"))?;
        let source = &self.data["messages"][source_id];
        let composer = &input["composer"];
        if input != &self.data["manager"]["input"]
            || !input["worker_wake"].is_null()
            || source["state"] != "accepted"
            || !input["turn_id"].is_string()
            || source["native_turn_id"] != input["turn_id"]
            || source["native_input_id"] != composer["manager_admission"]["nonce"]
            || source["revision"] != composer["origin"]["revision"]
        {
            return Err(invalid(
                "attachment grant does not match the active admitted turn",
            ));
        }
        let fresh = current(source_id)
            .map_err(|_| invalid("Discord attachment source could not be verified"))?
            .ok_or_else(|| invalid("Discord attachment source is no longer visible"))?;
        certify_attachment_source(source, &fresh)?;
        let place = self
            .file
            .parent()
            .ok_or_else(|| invalid("missing attachment custody directory"))?
            .canonicalize()?;
        super::received_files::read_granted(&place, &source["received_files"], id, digest, offset)
    }

    pub(super) fn accepted_voice(&self, id: &str, origin: &Value) -> bool {
        let row = &self.data["messages"][id];
        super::call_origin::key(id) && row["state"] == "accepted" && row["voice_origin"] == *origin
    }

    pub(super) fn prepared_voice(&self) -> Option<Value> {
        self.data["messages"]
            .as_object()
            .unwrap()
            .values()
            .find(|row| row["state"] == "prepared" && row["voice_origin"].is_object())
            .cloned()
    }

    pub(super) fn reject_manager_input(
        &mut self,
        id: &str,
        revision: &Value,
        reason: &str,
    ) -> io::Result<()> {
        let source = &self.data["messages"][id];
        if source["state"] != "prepared" || source["revision"] != *revision {
            return Err(invalid("manager input changed before refusal"));
        }
        let mut next = self.data.clone();
        next["messages"][id]["state"] = json!("rejected");
        next["messages"][id]["error"] = json!(reason);
        self.save(next)
    }

    pub(super) fn withdraw(&mut self, id: &str) -> io::Result<()> {
        if !self.data["messages"][id].is_null() && self.data["messages"][id]["state"] != "withdrawn"
        {
            let mut next = self.data.clone();
            let record = &mut next["messages"][id];
            if let Some(fields) = record.as_object_mut() {
                fields.shift_remove("text");
                fields.shift_remove("addressed_text");
            }
            record["state"] = json!("withdrawn");
            self.save(next)?;
        }
        Ok(())
    }

    pub(super) fn observe(&mut self, message: &Value, via: Observation) -> io::Result<bool> {
        let [_, app, _, channel, _] = self.identity();
        let Some(nonce) = message["nonce"].as_str() else {
            return Ok(false);
        };
        let Some(id) = message["id"].as_str().filter(|id| snowflake(id)) else {
            return Ok(false);
        };
        let entry = &self.data["outbox"][nonce];
        if !self.within_consent(id)
            || entry.is_null()
            || entry["state"] != "sending" && entry["state"] != "uncertain"
            || message["author"]["id"] != app
            || message["author"]["bot"] != true
            || if entry["proof"]["audience"].is_null() {
                message["channel_id"] != channel || !message["guild_id"].is_null()
            } else {
                let audience = &entry["proof"]["audience"];
                audience != &self.data["team_discussion"]
                    || message["channel_id"] != audience["thread_id"]
                    || !message["guild_id"].is_null() && message["guild_id"] != audience["guild_id"]
            }
            || !message["webhook_id"].is_null()
            || message["content"] != entry["body"]
            || !matches_attachments(&entry["proof"]["file_source"], &message["attachments"])
        {
            return Ok(false);
        }
        let mut next = self.data.clone();
        next["outbox"][nonce]["state"] = json!("sent");
        next["outbox"][nonce]["message_id"] = json!(id);
        let evidence = match via {
            Observation::Gateway => "gateway_seen",
            Observation::History => "history_seen",
            Observation::Rest => "rest_acknowledged",
        };
        next["outbox"][nonce][evidence] = json!(true);
        self.save(next)?;
        Ok(true)
    }

    fn identity(&self) -> [&str; 5] {
        std::array::from_fn(|n| self.data["identity"][n].as_str().unwrap())
    }

    pub(super) fn cursor(&self) -> Option<&str> {
        self.data["history_cursor"].as_str()
    }

    pub(super) fn within_consent(&self, id: &str) -> bool {
        self.data["consent_floor"].is_string()
            && id
                .parse::<u64>()
                .is_ok_and(|id| id > boundary(&self.data["consent_floor"]))
    }

    pub(super) fn checkpoint(&mut self, id: &str) -> io::Result<()> {
        if !id.is_empty() && !snowflake(id) {
            return Err(invalid("invalid personal history checkpoint"));
        }
        let id_value = json!(id);
        if self.cursor().is_none() || boundary(&id_value) > boundary(&self.data["history_cursor"]) {
            let mut next = self.data.clone();
            if self.cursor().is_none() {
                next["consent_floor"] = id_value.clone();
            }
            next["history_cursor"] = id_value;
            self.save(next)?;
        }
        Ok(())
    }

    pub(super) fn thread_creation(&self) -> Option<&Value> {
        self.data["thread_creation"]
            .is_object()
            .then(|| &self.data["thread_creation"])
    }

    pub(super) fn reserve_thread(&mut self, plan: Value) -> io::Result<()> {
        if self.thread_creation().is_some() {
            return Err(invalid(
                "connector already has its thread creation boundary",
            ));
        }
        let mut next = self.data.clone();
        next["thread_creation"] = plan;
        next["thread_create_attempts"] = json!(1);
        self.save(next)
    }

    pub(super) fn thread_creation_linked(&self, cfg: &Config) -> bool {
        self.thread_creation().is_some_and(|plan| {
            super::team_thread::linked_values(cfg).any(|route| {
                route["guild_id"] == plan["guild_id"]
                    && route["thread_id"] == plan["message_id"]
                    && route["starter_channel_id"] == plan["parent_id"]
                    && route["starter_revision"] == plan["revision"]
            })
        })
    }

    pub(super) fn finish_thread_creation(&mut self, cfg: &Config) -> io::Result<()> {
        if !self.thread_creation_linked(cfg) {
            return Err(invalid(
                "thread create remains unresolved for its original opener",
            ));
        }
        let mut next = self.data.clone();
        for key in [
            "thread_creation",
            "thread_create_attempts",
            "thread_create_retry_at",
            "thread_create_retry_allowed",
        ] {
            next[key] = Value::Null;
        }
        self.save(next)
    }

    pub(super) fn thread_throttle_due(&self, now: u64) -> bool {
        self.thread_creation().is_some()
            && self.data["thread_create_retry_at"]
                .as_u64()
                .is_some_and(|due| due <= now)
    }

    pub(super) fn thread_retry_due(&self, now: u64) -> bool {
        self.thread_throttle_due(now)
            && self.data["thread_create_retry_allowed"] != false
            && self.data["thread_create_attempts"]
                .as_u64()
                .is_some_and(|attempts| (1..3).contains(&attempts))
    }

    pub(super) fn start_thread_retry(&mut self, now: u64) -> io::Result<()> {
        if !self.thread_retry_due(now) {
            return Err(invalid("thread creation retry has no definitive refusal"));
        }
        let mut next = self.data.clone();
        next["thread_create_attempts"] =
            json!(self.data["thread_create_attempts"].as_u64().unwrap() + 1);
        next["thread_create_retry_at"] = Value::Null;
        next["thread_create_retry_allowed"] = Value::Null;
        self.save(next)
    }

    pub(super) fn retire_throttled_thread(&mut self, plan: &Value, now: u64) -> io::Result<()> {
        if self.thread_creation() != Some(plan) || !self.thread_throttle_due(now) {
            return Err(invalid(
                "cannot retire a thread creation with an unresolved effect",
            ));
        }
        let mut next = self.data.clone();
        next["thread_creation"] = Value::Null;
        next["thread_create_attempts"] = Value::Null;
        next["thread_create_retry_at"] = Value::Null;
        next["thread_create_retry_allowed"] = Value::Null;
        self.save(next)
    }

    pub(super) fn record_thread_throttle(
        &mut self,
        at: u64,
        retry_allowed: bool,
    ) -> io::Result<()> {
        let mut next = self.data.clone();
        next["thread_create_retry_at"] = json!(at);
        next["thread_create_retry_allowed"] = json!(retry_allowed);
        self.save(next)
    }

    pub(super) fn thread_retry_at(&mut self, at: Option<u64>) -> io::Result<()> {
        let mut next = self.data.clone();
        next["thread_create_retry_at"] = json!(at);
        self.save(next)
    }

    pub(super) fn team_gap_epoch(&self) -> u64 {
        self.data["team_gap_epoch"].as_u64().unwrap_or_default()
    }

    pub(super) fn mark_team_gap(&mut self) -> io::Result<()> {
        let epoch = self
            .team_gap_epoch()
            .checked_add(1)
            .ok_or_else(|| invalid("guild gap epoch exhausted"))?;
        let mut next = self.data.clone();
        next["team_gap_epoch"] = json!(epoch);
        self.save(next)
    }

    pub(super) fn team_cursor(&self) -> Option<&str> {
        self.data["team_history_cursor"].as_str()
    }

    pub(super) fn checkpoint_team(&mut self, id: &str) -> io::Result<()> {
        if !id.is_empty() && !snowflake(id) || self.data["team_discussion"].is_null() {
            return Err(invalid("invalid linked team checkpoint"));
        }
        if self.team_cursor().is_none()
            || boundary(&json!(id)) > boundary(&self.data["team_history_cursor"])
        {
            let mut next = self.data.clone();
            if self.team_cursor().is_none() {
                next["team_consent_floor"] = json!(id);
            }
            next["team_history_cursor"] = json!(id);
            self.save(next)?;
        }
        Ok(())
    }

    pub(super) fn pending_team(&self) -> std::collections::BTreeSet<u64> {
        self.pending_ids()
            .into_iter()
            .filter(|id| self.has_team_source(&id.to_string()))
            .collect()
    }

    pub(super) fn pending_ids(&self) -> Vec<u64> {
        self.data["messages"]
            .as_object()
            .unwrap()
            .iter()
            .filter(|(_, row)| row["state"] == "prepared")
            .filter_map(|(id, _)| id.parse().ok())
            .collect()
    }

    pub(super) fn pending_count(&self) -> usize {
        self.data["messages"]
            .as_object()
            .unwrap()
            .values()
            .filter(|row| matches!(row["state"].as_str(), Some("prepared" | "sending")))
            .count()
    }
}

pub(super) fn valid_boundary(value: &Value) -> bool {
    value.is_null()
        || value
            .as_str()
            .is_some_and(|id| id.is_empty() || snowflake(id))
}

pub(super) fn boundary(value: &Value) -> u64 {
    value
        .as_str()
        .and_then(|id| id.parse().ok())
        .unwrap_or_default()
}

fn matches_attachments(source: &Value, attachments: &Value) -> bool {
    if source.is_null() {
        return attachments.is_null() || attachments.as_array().is_some_and(Vec::is_empty);
    }
    let (Some(files), Some(attachments)) = (source["files"].as_array(), attachments.as_array())
    else {
        return false;
    };
    files.len() == attachments.len()
        && files.iter().all(|file| {
            attachments
                .iter()
                .filter(|attachment| {
                    attachment["filename"] == file["name"]
                        && attachment["size"] == file["size"]
                        && attachment["id"].as_str().is_some_and(snowflake)
                })
                .count()
                == 1
        })
}

pub(super) fn publication_nonce(proof: &Value) -> io::Result<String> {
    Ok(hash(&json!([
        proof["binding"],
        proof["turn_id"],
        proof["output_id"]
    ]))?[..24]
        .to_owned())
}

pub(super) fn verify_publication(nonce: &str, entry: &Value) -> io::Result<()> {
    let proof = &entry["proof"];
    let args = &proof["arguments"];
    let native = &proof["call_receipt"];
    let host = super::provenance::coordinates(&proof["binding"], &proof["host_meta"])?;
    let mut canonical = args.clone();
    canonical.sort_all_objects();
    let staged_files = &proof["file_source"];
    let valid_files = if staged_files.is_null() {
        args.as_object()
            .is_some_and(|fields| fields.len() == 4 && !fields.contains_key("file_ids"))
    } else {
        let manifest = staged_files["files"].as_array();
        let ids = args["file_ids"].as_array();
        matches!((manifest, ids), (Some(manifest), Some(ids))
            if !manifest.is_empty() && manifest.len() <= 10 && manifest.len()==ids.len()
                && manifest.iter().zip(ids).all(|(file,id)| file["id"] == *id && id.as_str().is_some_and(|id|
                    id.len() == 64 && id.bytes().all(|byte| byte.is_ascii_hexdigit()))))
            && args.as_object().is_some_and(|fields| fields.len() == 5)
            && staged_files["id"].as_str().is_some_and(|id| !id.is_empty())
            && staged_files["binding"].is_object()
    };
    if entry["proof_digest"] != hash(proof)?
        || publication_nonce(proof)? != nonce
        || entry["body"] != proof["body"]
        || !valid_files
        || args["final"] != true
        || args["sequence"] != 1
        || args["output_id"] != proof["output_id"]
        || args["text"] != proof["body"]
        || proof["arguments_hash"] != hash(&canonical)?
        || !proof["source_id"]
            .as_str()
            .is_some_and(|id| snowflake(id) || super::call_origin::key(id))
        || native["main"] != true
        || native["native_turn_id"] != proof["turn_id"]
        || native["native_call_id"] != proof["native_call_id"]
        || native["native_call_id"] != host.call
        || host
            .turn
            .is_some_and(|turn| native["native_turn_id"] != turn)
        || !native["native_input_id"]
            .as_str()
            .is_some_and(|id| id.len() == 64 && id.bytes().all(|b| b.is_ascii_hexdigit()))
    {
        return Err(invalid(
            "publication differs from its authenticated native tool receipt",
        ));
    }
    Ok(())
}

pub(super) fn hash(value: &Value) -> io::Result<String> {
    // Go's encoding/json escapes HTML-sensitive characters before hashing.
    let text = serde_json::to_string(value)?
        .replace('&', "\\u0026")
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029");
    Ok(format!("{:x}", Sha256::digest(text.as_bytes())))
}

impl Inbox {
    pub(super) fn matches_owner_admission(
        &self,
        source: &str,
        revision: &Value,
        state: &str,
    ) -> bool {
        let current = &self.data["messages"][source];
        current["state"] == state && current["revision"] == *revision
    }

    pub(super) fn worker_mail_anchor(
        &self,
        binding: &Value,
        note: &Value,
    ) -> Option<(String, Value)> {
        super::mail_wake::anchor(&self.data, binding, note)
    }

    pub(super) fn manager(&self) -> &Value {
        &self.data["manager"]
    }

    pub(super) fn set_manager(&mut self, state: Value) -> io::Result<()> {
        let mut next = self.data.clone();
        next["manager"] = state;
        self.save(next)
    }

    fn next_source(&self, worker: bool) -> Option<Value> {
        self.pending_ids()
            .into_iter()
            .map(|id| (id, &self.data["messages"][id.to_string()]))
            .filter(|(_, row)| {
                if row["authority"] == "team_participant" {
                    return !worker;
                }
                super::worker::target_text(super::worker::source_text(row)).is_some() == worker
            })
            .min_by_key(|(id, _)| *id)
            .map(|(_, row)| row.clone())
    }

    pub(super) fn next_prepared(&self) -> Option<Value> {
        self.next_source(false)
    }

    pub(super) fn next_worker(&self) -> Option<Value> {
        self.next_source(true)
    }

    pub(super) fn has_worker_work(&self) -> bool {
        self.next_worker().is_some() || self.unsettled_worker(None).is_some()
    }

    pub(super) fn worker_source(&self, operation: &str) -> &Value {
        let id = self.data["worker_inputs"][operation]["source_id"]
            .as_str()
            .unwrap_or_default();
        &self.data["messages"][id]
    }

    pub(super) fn worker_status(&self) -> Value {
        let delivered = &self.data["worker_delivery"];
        let states: Vec<_> = self.data["worker_inputs"].as_object().into_iter().flatten().map(|(key,request)| {
            let outcome = &delivered[key];
            json!({"operation_id":key,"source_id":request["source_id"],"session_id":request["composer"]["session"],
                "state":outcome["state"].as_str().unwrap_or("unconfirmed")})
        }).collect();
        json!(states)
    }

    pub(super) fn worker_excluded(&self, target: &str) -> bool {
        self.data["worker_excluded"][target] == true
    }

    pub(super) fn set_worker_excluded(&mut self, target: &str, excluded: bool) -> io::Result<()> {
        if !super::worker::target_id(target) {
            return Err(invalid("worker exclusion requires a stable session ID"));
        }
        let mut next = self.data.clone();
        if excluded {
            next["worker_excluded"][target] = json!(true);
        } else if let Some(rows) = next["worker_excluded"].as_object_mut() {
            rows.shift_remove(target);
        }
        self.save(next)
    }

    pub(super) fn cutover_worker(
        &mut self,
        operation: &str,
        source: &str,
        provider: &str,
        composer: &Value,
    ) -> io::Result<()> {
        let row = &self.data["messages"][source];
        let origin = &composer["origin"];
        let target = composer["session"].as_str().unwrap_or_default();
        if row["state"] != "prepared"
            || !row["native_binding"].is_null()
            || !self.data["worker_inputs"][operation].is_null()
            || self.worker_excluded(target)
            || origin["revision"] != row["revision"]
            || origin["request_id"] != row["operation_id"]
            || origin["actor_id"] != row["author"]["id"]
            || origin["message_id"] != source
            || origin["channel_id"] != row["route"]["channel_id"]
            || origin["guild_id"].as_str().unwrap_or_default()
                != row["route"]["guild_id"].as_str().unwrap_or_default()
            || super::worker::target_text(super::worker::source_text(row))
                .is_none_or(|(id, text)| id != target || composer["text"] != text)
        {
            return Err(invalid(
                "worker input differs from the current Discord source and target",
            ));
        }
        let mut next = self.data.clone();
        next["messages"][source]["worker_operation"] = json!(operation);
        next["messages"][source]["state"] = json!("sending");
        next["worker_inputs"][operation] =
            json!({"source_id":source,"provider":provider,"composer":composer});
        self.save(next)
    }

    pub(super) fn unsettled_worker(&self, after: Option<&str>) -> Option<(String, String, Value)> {
        let rows = self.data["worker_inputs"].as_object()?;
        let entries = || {
            rows.iter()
                .filter(|(op, _)| self.data["worker_delivery"][*op].is_null())
        };
        let (op, entry) = entries()
            .filter(|(op, _)| after.is_none_or(|after| op.as_str() > after))
            .min_by_key(|(op, _)| op.as_str())
            .or_else(|| entries().min_by_key(|(op, _)| op.as_str()))?;
        Some((
            op.clone(),
            entry["provider"].as_str()?.to_owned(),
            entry["composer"].clone(),
        ))
    }

    pub(super) fn settle_worker(&mut self, operation: &str, receipt: &Value) -> io::Result<()> {
        let source = self.data["worker_inputs"][operation]["source_id"]
            .as_str()
            .ok_or_else(|| invalid("worker receipt has no source"))?;
        let mut next = self.data.clone();
        next["worker_delivery"][operation] = json!({"state":receipt["state"],
            "native_session_id":receipt["native_session_id"],"native_turn_id":receipt["native_turn_id"]});
        if next["messages"][source]["worker_operation"] == operation
            && next["messages"][source]["state"] == "sending"
        {
            next["messages"][source]["state"] = json!(if receipt["state"] == "rejected" {
                "withdrawn"
            } else {
                "accepted"
            });
        }
        self.save(next)
    }

    pub(super) fn pending_publications(&self) -> Vec<String> {
        self.data["outbox"]
            .as_object()
            .unwrap()
            .iter()
            .filter(|(_, entry)| entry["state"] == "prepared")
            .map(|(nonce, _)| nonce.clone())
            .collect()
    }

    pub(super) fn publication(&self, nonce: &str) -> Value {
        self.data["outbox"][nonce].clone()
    }

    pub(super) fn prepare_publication(
        &mut self,
        nonce: &str,
        proof: Value,
        manager: Value,
    ) -> io::Result<()> {
        let entry_new = json!({
            "proof":proof, "proof_digest":hash(&proof)?, "body":proof["body"], "state":"prepared"
        });
        verify_publication(nonce, &entry_new)?;
        let entry = &self.data["outbox"][nonce];
        if !entry.is_null() {
            if entry["proof"] == proof {
                return Ok(());
            }
            return Err(invalid(
                "publication key conflicts with the original native publication",
            ));
        }
        if let Some(call) = proof["native_call_id"].as_str()
            && self.data["outbox"]
                .as_object()
                .unwrap()
                .values()
                .any(|prior| {
                    prior["proof"]["native_call_id"] == call
                        && prior["proof"]["binding"]["native_session_id"]
                            == proof["binding"]["native_session_id"]
                })
        {
            return Err(invalid(
                "native manager publication call already belongs to another effect",
            ));
        }
        if self.data["outbox"].as_object().unwrap().len() >= MAX_RECORDS {
            return Err(invalid("publication ledger reached its capacity"));
        }
        let mut next = self.data.clone();
        next["outbox"][nonce] = entry_new;
        next["manager"] = manager;
        self.save(next)
    }

    pub(super) fn publication_state(
        &mut self,
        nonce: &str,
        state: &str,
        retry_at: Option<u64>,
    ) -> io::Result<()> {
        let old = &self.data["outbox"][nonce]["state"];
        if !(old == "prepared" && state == "sending"
            || old == "sending" && matches!(state, "prepared" | "uncertain"))
        {
            return Err(invalid("invalid publication state transition"));
        }
        let mut next = self.data.clone();
        next["outbox"][nonce]["state"] = json!(state);
        next["outbox"][nonce]["retry_at"] = json!(retry_at);
        self.save(next)
    }

    pub(super) fn cutover(
        &mut self,
        source_id: &str,
        revision: &str,
        manager: Value,
    ) -> io::Result<()> {
        if self.data["messages"][source_id]["state"] != "prepared"
            || self.data["messages"][source_id]["revision"] != revision
        {
            return Err(invalid(
                "manager source is no longer prepared at the verified revision",
            ));
        }
        let nonce = manager["input"]["composer"]["manager_admission"]["nonce"]
            .as_str()
            .filter(|id| {
                id.len() == 64
                    && id
                        .bytes()
                        .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
            })
            .ok_or_else(|| invalid("manager input lacks a valid native admission nonce"))?;
        if self.data["messages"]
            .as_object()
            .unwrap()
            .values()
            .any(|prior| prior["native_input_id"] == nonce)
        {
            return Err(invalid(
                "native manager admission nonce already belongs to a source",
            ));
        }
        let mut next = self.data.clone();
        next["messages"][source_id]["state"] = json!("sending");
        next["messages"][source_id]["native_input_id"] =
            manager["input"]["composer"]["manager_admission"]["nonce"].clone();
        next["messages"][source_id]["native_binding"] = manager["binding"].clone();
        next["manager"] = manager;
        self.save(next)
    }

    pub(super) fn settled_input(&mut self, source_id: &str, manager: Value) -> io::Result<()> {
        if self.data["messages"][source_id]["state"] != "sending" {
            return Err(invalid("manager input has no recorded cutover"));
        }
        let turn = manager["input"]["turn_id"]
            .as_str()
            .filter(|turn| !turn.is_empty())
            .ok_or_else(|| invalid("manager input lacks a native turn receipt"))?;
        let input = &self.data["messages"][source_id];
        if input["native_input_id"] != manager["input"]["composer"]["manager_admission"]["nonce"]
            || input["native_binding"] != manager["binding"]
            || self.data["messages"]
                .as_object()
                .unwrap()
                .iter()
                .any(|(id, previous)| {
                    id != source_id
                        && previous["native_turn_id"] == turn
                        && previous["native_binding"]["native_session_id"]
                            == input["native_binding"]["native_session_id"]
                })
        {
            return Err(invalid(
                "native turn evidence conflicts with another dispatcher input",
            ));
        }
        let mut next = self.data.clone();
        next["messages"][source_id]["state"] = json!("accepted");
        next["messages"][source_id]["native_turn_id"] = json!(turn);
        next["manager"] = manager;
        self.save(next)
    }
}

#[cfg(test)]
pub(super) fn fixture_publication(body: &str) -> (String, Value) {
    let mut args = json!({"output_id":"fixture-output","sequence":1,"text":body,"final":true});
    args.sort_all_objects();
    let proof = json!({
        "body":body, "source_id":"101", "output_id":"fixture-output", "turn_id":"fixture-turn",
        "binding":{"provider":"codex","native_session_id":"fixture-thread"},
        "native_call_id":"fixture-call", "arguments":args, "arguments_hash":hash(&args).unwrap(),
        "host_meta":{"callId":"fixture-call", "threadId":"fixture-thread", "sessionId":"fixture-thread",
            "x-codex-turn-metadata":{"turn_id":"fixture-turn","thread_id":"fixture-thread","session_id":"fixture-thread"}},
        "call_receipt":{"main":true,"native_call_id":"fixture-call","native_turn_id":"fixture-turn","native_input_id":"a".repeat(64)}
    });
    (publication_nonce(&proof).unwrap(), proof)
}
