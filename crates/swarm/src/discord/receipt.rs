//! Durable text-only inbox for one enrolled personal installation.
use super::config::{Config, invalid, snowflake};
use crate::supervisor::runtime;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::io;
use std::path::{Path, PathBuf};

const VERSION: u64 = 2;
const MAX_RECORDS: usize = 2048;
const MAX_STORE_BYTES: usize = 1 << 20;

pub(super) enum Observation {
    Gateway,
    History,
}

pub(super) struct Inbox {
    file: PathBuf,
    data: Value,
}

impl Inbox {
    pub(super) fn open(dir: &Path, cfg: &Config) -> io::Result<Self> {
        let file = dir.join("rust-text-v1.json");
        let installation = cfg.text("installation_id");
        let owner = cfg.text("owner_id");
        let app = cfg.text("application_id");
        let channel = cfg.text("dm_channel_id");
        let identity = json!([installation, app, owner, channel, cfg.text("daemon_socket")]);
        let data = match runtime::read_json(&file) {
            Ok(data) => data,
            Err(e) if e.kind() == io::ErrorKind::NotFound => {
                let data =
                    json!({"version": VERSION, "identity": identity, "messages": {}, "outbox": {}});
                runtime::write_json(&file, &data)?;
                data
            }
            Err(e) => return Err(e),
        };
        if data["version"] != VERSION
            || data["identity"] != identity
            || !data["messages"].is_object()
            || !data["outbox"].is_object()
            || !valid_boundary(&data["consent_floor"])
            || !valid_boundary(&data["history_cursor"])
            || data["consent_floor"].is_null() != data["history_cursor"].is_null()
            || boundary(&data["consent_floor"]) > boundary(&data["history_cursor"])
        {
            return Err(invalid(
                "Discord inbox cannot prove its schema, enrolled identity and original consent boundary",
            ));
        }
        Ok(Self { file, data })
    }

    fn save(&mut self, next: Value) -> io::Result<()> {
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
        let [installation, _, owner, channel, supervisor] = self.identity();
        let Some(id) = source["id"].as_str().filter(|id| snowflake(id)) else {
            return Ok(None);
        };
        if !self.within_consent(id) {
            return Ok(None);
        }
        let Some(body) = source["content"]
            .as_str()
            .filter(|body| !body.trim().is_empty() && body.len() <= 16384)
        else {
            return Ok(None);
        };
        if source["channel_id"] != channel
            || source["author"]["id"] != owner
            || !source["guild_id"].is_null()
            || !source["webhook_id"].is_null()
            || !source["message_reference"].is_null()
            || source["author"]
                .get("bot")
                .is_some_and(|value| value != false)
            || source["author"]
                .get("system")
                .is_some_and(|value| value != false)
            || !source["attachments"].as_array().is_some_and(Vec::is_empty)
            || source["message_snapshots"]
                .as_array()
                .is_some_and(|snapshots| !snapshots.is_empty())
            || source["type"] != 0
        {
            return Ok(None);
        }
        let edited = &source["edited_timestamp"];
        if !edited.is_null() && !edited.as_str().is_some_and(|stamp| stamp.len() <= 64) {
            return Ok(None);
        }
        let revision = hash(&json!([owner, body, channel, "", id, edited, "", null]))?;
        let operation = format!("da_{}", &hash(&json!([installation, id]))?[..32]);
        let receipt = json!({
            "operation_id": operation, "revision": revision, "text": body,
            "route": {"guild_id": "", "channel_id": channel, "message_id": id},
            "author": {"id": owner}, "authority": "owner_direct",
            "supervisor_socket": supervisor, "state": "prepared", "edited_at": edited
        });
        let old = &self.data["messages"][id];
        if old["state"] == "withdrawn" || old == &receipt {
            return Ok((old == &receipt).then(|| old.clone()));
        }
        if !old.is_null()
            && (old["edited_at"] == *edited || edited.is_null() || old["state"] != "prepared")
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

    pub(super) fn withdraw(&mut self, id: &str) -> io::Result<()> {
        if !self.data["messages"][id].is_null() && self.data["messages"][id]["state"] != "withdrawn"
        {
            let mut next = self.data.clone();
            next["messages"][id] = json!({"state": "withdrawn"});
            self.save(next)?;
        }
        Ok(())
    }

    pub(super) fn observe(&mut self, message: &Value, via: Observation) -> io::Result<()> {
        let [_, app, _, channel, _] = self.identity();
        let Some(nonce) = message["nonce"].as_str() else {
            return Ok(());
        };
        let Some(id) = message["id"].as_str().filter(|id| snowflake(id)) else {
            return Ok(());
        };
        let entry = &self.data["outbox"][nonce];
        if !self.within_consent(id)
            || entry.is_null()
            || entry["state"] != "sending" && entry["state"] != "uncertain"
            || message["author"]["id"] != app
            || message["author"]["bot"] != true
            || message["channel_id"] != channel
            || !message["guild_id"].is_null()
            || !message["webhook_id"].is_null()
            || message["content"] != entry["body"]
        {
            return Ok(());
        }
        let mut next = self.data.clone();
        next["outbox"][nonce]["state"] = json!("sent");
        next["outbox"][nonce]["message_id"] = json!(id);
        let evidence = match via {
            Observation::Gateway => "gateway_seen",
            Observation::History => "history_seen",
        };
        next["outbox"][nonce][evidence] = json!(true);
        self.save(next)
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
            .filter(|row| row["state"] == "prepared")
            .count()
    }
}

fn valid_boundary(value: &Value) -> bool {
    value.is_null()
        || value
            .as_str()
            .is_some_and(|id| id.is_empty() || snowflake(id))
}

fn boundary(value: &Value) -> u64 {
    value
        .as_str()
        .and_then(|id| id.parse().ok())
        .unwrap_or_default()
}

fn hash(value: &Value) -> io::Result<String> {
    // Go's encoding/json escapes HTML-sensitive characters before hashing.
    let text = serde_json::to_string(value)?
        .replace('&', "\\u0026")
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029");
    Ok(format!("{:x}", Sha256::digest(text.as_bytes())))
}
