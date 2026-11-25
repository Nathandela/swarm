//! Discord v10 enrollment and read-side verification. Credentials stay in-process.
use super::config::{Config, invalid, load_token, snowflake};
use serde_json::{Value, json};
use std::io;
use std::path::Path;
use std::time::Duration;

const API: &str = "https://discord.com/api/v10";
const MAX_RESPONSE: u64 = 1 << 20;

pub(super) struct Discord {
    agent: ureq::Agent,
    authorization: String,
}

impl Discord {
    pub(super) fn connect(cfg: &Config) -> io::Result<Self> {
        let token = load_token(Path::new(cfg.text("token_file")))?;
        let authorization = format!("Bot {token}");
        let config = ureq::Agent::config_builder()
            .https_only(true)
            .proxy(None)
            .max_redirects(0)
            .timeout_global(Some(Duration::from_secs(8)))
            .build();
        Ok(Self {
            agent: config.into(),
            authorization,
        })
    }

    fn request(&self, path: &str, body: Option<&Value>) -> io::Result<Value> {
        let endpoint = format!("{API}{path}");
        let response = match body {
            Some(body) => self
                .agent
                .post(&endpoint)
                .header("Authorization", &self.authorization)
                .send_json(body),
            None => self
                .agent
                .get(&endpoint)
                .header("Authorization", &self.authorization)
                .call(),
        };
        let mut response = response.map_err(|err| match err {
            ureq::Error::StatusCode(429) => {
                io::Error::other("Discord API rate limited the request, try again later")
            }
            ureq::Error::StatusCode(404) => io::ErrorKind::NotFound.into(),
            ureq::Error::StatusCode(401 | 403) => {
                io::Error::other("Discord API rejected the bot credential or permissions")
            }
            ureq::Error::StatusCode(status) => {
                io::Error::other(format!("Discord API returned HTTP {status}"))
            }
            _ => io::Error::other("Discord API transport failed"),
        })?;
        if !response.status().is_success() {
            return Err(io::Error::other(
                "Discord API returned an unexpected status",
            ));
        }
        response
            .body_mut()
            .with_config()
            .limit(MAX_RESPONSE)
            .read_json()
            .map_err(|_| invalid("invalid Discord API response"))
    }

    fn bot(&self, configured: &str) -> io::Result<Value> {
        let me = self.request("/users/@me", None)?;
        let id = me["id"].as_str().filter(|id| snowflake(id));
        if me["bot"] != true || id.is_none() || (!configured.is_empty() && id != Some(configured)) {
            return Err(invalid("token does not match the configured bot identity"));
        }
        Ok(me)
    }

    pub(super) fn discover(&self, cfg: &mut Config) -> io::Result<()> {
        let me = self.bot(cfg.text("application_id"))?;
        cfg.0["application_id"] = me["id"].clone();
        if cfg.text("name").is_empty() {
            cfg.0["name"] = me["username"].clone();
        }
        if cfg.text("mode") == "personal" {
            let owner = cfg.text("owner_id");
            if !snowflake(owner) || owner == cfg.text("application_id") {
                return Err(invalid("personal mode requires a valid human --owner-id"));
            }
            let dm = self.request("/users/@me/channels", Some(&json!({"recipient_id": owner})))?;
            let channel = personal_channel(&dm, owner)
                .ok_or_else(|| invalid("Discord DM is outside the enrolled owner scope"))?;
            if !cfg.text("dm_channel_id").is_empty() && cfg.text("dm_channel_id") != channel {
                return Err(invalid("Discord DM does not match the enrolled channel"));
            }
            cfg.0["dm_channel_id"] = json!(channel);
        } else if cfg.0["guild_ids"].as_array().is_none_or(Vec::is_empty) {
            let guilds = self.request("/users/@me/guilds", None)?;
            let id = guilds
                .as_array()
                .filter(|guilds| guilds.len() == 1)
                .and_then(|guilds| guilds[0]["id"].as_str())
                .filter(|id| snowflake(id))
                .ok_or_else(|| invalid("select an installed server using --guild"))?;
            cfg.0["guild_ids"] = json!([id]);
        }
        Ok(())
    }

    pub(super) fn verify_app(&self, cfg: &Config) -> io::Result<String> {
        let me = self.bot(cfg.text("application_id"))?;
        me["username"]
            .as_str()
            .filter(|name| {
                !name.is_empty() && name.len() <= 256 && !name.chars().any(char::is_control)
            })
            .map(str::to_owned)
            .ok_or_else(|| invalid("invalid Discord bot username"))
    }

    pub(super) fn owner_history(
        &self,
        cfg: &Config,
        before: &str,
        limit: usize,
    ) -> io::Result<Vec<Value>> {
        if !before.is_empty() && !snowflake(before) {
            return Err(invalid("invalid Discord history cursor"));
        }
        self.verify_dm(cfg, None)?;
        let channel = cfg.text("dm_channel_id");
        let mut path = format!("/channels/{channel}/messages?limit={limit}");
        if !before.is_empty() {
            path.push_str(&format!("&before={before}"));
        }
        let value = self.request(&path, None)?;
        let rows = value
            .as_array()
            .filter(|rows| rows.len() <= limit)
            .ok_or_else(|| invalid("invalid Discord history page"))?;
        if rows.iter().any(|message| {
            message["channel_id"] != channel
                || !message["guild_id"].is_null()
                || !message["id"].as_str().is_some_and(snowflake)
        }) {
            return Err(invalid(
                "Discord history page contains a different audience or invalid identity",
            ));
        }
        Ok(rows.clone())
    }

    pub(super) fn owner_message(&self, cfg: &Config, id: &str) -> io::Result<Option<Value>> {
        if !snowflake(id) {
            return Err(invalid("invalid Discord message ID"));
        }
        self.verify_dm(cfg, None)?;
        let channel = cfg.text("dm_channel_id");
        let message = match self.request(&format!("/channels/{channel}/messages/{id}"), None) {
            Ok(message) => message,
            Err(err) if err.kind() == io::ErrorKind::NotFound => return Ok(None),
            Err(err) => return Err(err),
        };
        if message["id"] != id || message["channel_id"] != channel {
            return Err(invalid(
                "Discord source message does not match the requested route",
            ));
        }
        Ok(Some(message))
    }

    pub(super) fn verify_dm(
        &self,
        cfg: &Config,
        channel: Option<&str>,
    ) -> io::Result<&'static str> {
        let enrolled = cfg.text("dm_channel_id");
        if channel.is_some_and(|channel| channel != enrolled) {
            return Err(invalid("channel is outside the enrolled owner DM"));
        }
        let dm = self.request(&format!("/channels/{enrolled}"), None)?;
        if personal_channel(&dm, cfg.text("owner_id")) != Some(enrolled) {
            return Err(invalid("channel is outside the enrolled owner DM"));
        }
        Ok("verified one-to-one channel for the enrolled owner")
    }
}

fn personal_channel<'a>(dm: &'a Value, owner: &str) -> Option<&'a str> {
    let id = dm["id"].as_str().filter(|id| snowflake(id))?;
    let recipients = dm["recipients"].as_array()?;
    let [member] = recipients.as_slice() else {
        return None;
    };
    let empty = |key| dm[key].is_null() || dm[key] == "";
    (dm["type"] == 1
        && empty("guild_id")
        && empty("parent_id")
        && snowflake(owner)
        && member["id"] == owner
        && (member.get("bot").is_none() || member["bot"] == false)
        && (member.get("system").is_none() || member["system"] == false))
        .then_some(id)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn owner_dm_rejects_group_cross_scope_and_bot_recipient() {
        let dm = json!({"id": "123", "type": 1, "recipients": [{"id": "456"}]});
        assert_eq!(personal_channel(&dm, "456"), Some("123"));
        assert_eq!(personal_channel(&dm, "789"), None);
        for (field, value) in [
            ("type", json!(3)),
            ("guild_id", json!("789")),
            ("recipients", json!([{"id":"456","bot":true}])),
            ("recipients", json!([{"id":"456"},{"id":"789"}])),
        ] {
            let mut wrong = dm.clone();
            wrong[field] = value;
            assert_eq!(personal_channel(&wrong, "456"), None);
        }
    }
}
