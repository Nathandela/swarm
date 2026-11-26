//! Opt-in live check in a dedicated private thread on an owner-verified guild.
use super::{API, Discord, Post};
use crate::discord::{
    config::{Place, invalid, snowflake},
    local, received_files,
};
use crate::supervisor::{
    mail_files::{Captured, Descriptor},
    runtime,
};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{fs, io, path::PathBuf};

fn configured(name: &str) -> io::Result<String> {
    std::env::var(name).map_err(|_| {
        invalid("live fixture requires explicit bot, guild and private evidence directory")
    })
}

fn only<'a>(mut values: impl Iterator<Item = &'a Value>) -> io::Result<&'a Value> {
    let first = values
        .next()
        .ok_or_else(|| invalid("fixture coordinate is missing"))?;
    if values.next().is_some() {
        return Err(invalid("fixture coordinate is ambiguous"));
    }
    Ok(first)
}

fn id(row: &Value) -> io::Result<&str> {
    row["id"]
        .as_str()
        .filter(|id| snowflake(id))
        .ok_or_else(|| invalid("invalid Discord identity"))
}

fn array(value: &Value) -> io::Result<&[Value]> {
    value
        .as_array()
        .map(Vec::as_slice)
        .ok_or_else(|| invalid("expected Discord array"))
}

#[test]
#[ignore = "creates two synthetic messages in a new private thread, then archives it"]
fn personal_guild_text_and_attachment() -> io::Result<()> {
    let read_only = std::env::var_os("SWARM_LIVE_RECHECK_ONLY").is_some();
    let expected_bot = configured("SWARM_LIVE_BOT_NAME")?;
    let expected_guild = (!read_only)
        .then(|| configured("SWARM_LIVE_GUILD_NAME"))
        .transpose()?;
    let root = PathBuf::from(configured("SWARM_LIVE_EVIDENCE_DIR")?).canonicalize()?;
    runtime::private_dir(&root)?;
    let receipt = root.join("transport.json");
    let _lease = runtime::lock(&root.join("transport.lock"))?;
    let saved = match runtime::read_json(&receipt) {
        Ok(value) => Some(value),
        Err(error) if !read_only && error.kind() == io::ErrorKind::NotFound => None,
        Err(error) => return Err(error),
    };
    let place = Place::resolve(None)?;
    let cfg = place.load()?;
    let api = Discord::connect(&cfg)?;
    let app = cfg.text("application_id");
    let owner = cfg.text("owner_id");
    let active = || -> io::Result<()> {
        let status = local::call(&place.dir, "status", "", None)?;
        if cfg.text("mode") != "personal"
            || status["connected"] != true
            || status["installation_id"] != cfg.text("installation_id")
            || api.verify_app(&cfg)? != expected_bot
        {
            return Err(invalid("active personal connector identity changed"));
        }
        Ok(())
    };
    active()?;
    let enrolled = api.request("/users/@me/guilds", None)?;
    let guild = id(only(array(&enrolled)?.iter().filter(|row| {
        if let Some(name) = &expected_guild {
            row["name"] == *name
        } else {
            saved
                .as_ref()
                .is_some_and(|value| row["id"] == value["guild"])
        }
    }))?)?;
    let verify_guild = || -> io::Result<()> {
        let live = api.request(&format!("/guilds/{guild}"), None)?;
        if live["id"] != guild
            || expected_guild
                .as_ref()
                .is_some_and(|name| live["name"] != *name)
            || live["owner_id"] != owner
        {
            return Err(invalid("guild is not owned by the enrolled human"));
        }
        Ok(())
    };
    verify_guild()?;
    let channels = api.request(&format!("/guilds/{guild}/channels"), None)?;
    let parent = id(only(array(&channels)?.iter().filter(|row| {
        row["guild_id"] == guild
            && row["type"] == 0
            && (!read_only
                || saved
                    .as_ref()
                    .is_some_and(|value| row["id"] == value["parent"]))
    }))?)?;
    let rights = api.request(
        &format!("/channels/{parent}/permissions/member/{app}"),
        None,
    )?;
    let bits = rights["permissions"]
        .as_str()
        .and_then(|n| n.parse::<u64>().ok())
        .unwrap_or(0);
    let required = (1 << 10) | (1 << 15) | (1 << 16) | (1 << 36) | (1 << 38);
    if bits & 8 == 0 && bits & required != required {
        return Err(invalid("private test thread permissions are unavailable"));
    }
    let mut state = match saved {
        Some(value) => value,
        None => {
            json!({"guild":guild,"parent":parent,"app":app,"name":format!("swarm-transport-{}", &runtime::random_id()?[..12])})
        }
    };
    if state["guild"] != guild || state["parent"] != parent || state["app"] != app {
        return Err(invalid("fixture evidence belongs to another placement"));
    }
    let name = state["name"]
        .as_str()
        .filter(|n| n.starts_with("swarm-transport-") && n.len() == 28)
        .ok_or_else(|| invalid("invalid fixture thread name"))?
        .to_owned();
    let exact = |row: &Value| {
        row["guild_id"] == guild
            && row["parent_id"] == parent
            && row["type"] == 12
            && row["owner_id"] == app
            && row["name"] == name
    };
    let thread = if let Some(id) = state["thread"].as_str().filter(|s| snowflake(s)) {
        id.to_owned()
    } else {
        if read_only {
            return Err(invalid(
                "read-only recheck requires a recorded private thread",
            ));
        }
        let row = if state["creating"] == true {
            let active = api.request(&format!("/guilds/{guild}/threads/active"), None)?;
            only(array(&active["threads"])?.iter().filter(|r| exact(r)))?.clone()
        } else {
            state["creating"] = json!(true);
            runtime::write_json(&receipt, &state)?;
            api.request(
                &format!("/channels/{parent}/threads"),
                Some(&json!({"name":name,"type":12,"auto_archive_duration":60,"invitable":false})),
            )?
        };
        if !exact(&row) {
            return Err(invalid("created thread differs from test scope"));
        }
        let result = id(&row)?.to_owned();
        state["thread"] = json!(result);
        runtime::write_json(&receipt, &state)?;
        result
    };
    let verify_thread = || -> io::Result<Value> {
        active()?;
        verify_guild()?;
        let value = api.request(&format!("/channels/{thread}"), None)?;
        if value["id"] != thread || !exact(&value) {
            return Err(invalid("private test thread changed"));
        }
        Ok(value)
    };
    let csv = b"case,result\ntransport,ok\n";
    let digest = format!("{:x}", Sha256::digest(csv));
    for label in ["text", "file"] {
        let channel = verify_thread()?;
        let nonce = format!("{:x}", Sha256::digest(format!("{name}/{label}")))[..24].to_owned();
        let body = format!("Swarm transport check {name} ({label}). Synthetic test data.");
        if state[label].is_null() {
            if read_only {
                return Err(invalid(
                    "read-only recheck requires the previous message attempt",
                ));
            }
            if channel["thread_metadata"]["archived"] != false {
                return Err(invalid("fixture thread is already archived"));
            }
            state[label] = json!({"attempted":true,"nonce":nonce});
            runtime::write_json(&receipt, &state)?;
            let files = if label == "file" {
                vec![Captured {
                    file: Descriptor {
                        id: nonce.clone(),
                        name: "swarm-transport.csv".into(),
                        content_type: "text/csv".into(),
                        sha256: digest.clone(),
                        size: csv.len(),
                    },
                    data: csv.to_vec(),
                }]
            } else {
                vec![]
            };
            match api.post_publication_channel(&thread, &nonce, &body, &files) {
                Post::Acknowledged(msg) => {
                    state[label]["ack"] = json!(id(&msg)?);
                    runtime::write_json(&receipt, &state)?;
                }
                Post::RateLimited(_) => {
                    return Err(io::Error::other(
                        "Discord rate limited this fixture; inspect its recorded attempt",
                    ));
                }
                Post::Unconfirmed => (),
            }
        }
        let history = api.request(&format!("/channels/{thread}/messages?limit=10"), None)?;
        let matches = |r: &&Value| {
            r["channel_id"] == thread
                && r["content"] == body
                && r["author"]["id"] == app
                && r["author"]["bot"] == true
        };
        let msg = only(array(&history)?.iter().filter(matches))?;
        if state[label].get("ack").is_some_and(|ack| ack != &msg["id"]) {
            return Err(invalid("Discord history differs from submission receipt"));
        }
        let read = api.request(&format!("/channels/{thread}/messages/{}", id(msg)?), None)?;
        if !matches(&&read)
            || read["id"] != msg["id"]
            || (read["guild_id"] != Value::Null && read["guild_id"] != guild)
            || read["mention_everyone"] != false
            || !array(&read["mentions"])?.is_empty()
        {
            return Err(invalid("message readback differs from test scope"));
        }
        if label == "file" {
            let attachments = received_files::capture(&root, &read, &thread, 4096)?;
            if attachments.len() != 1
                || attachments[0]["filename"] != "swarm-transport.csv"
                || attachments[0]["sha256"] != digest
                || fs::read(attachments[0]["path"].as_str().unwrap())? != csv
            {
                return Err(invalid("received CSV differs from uploaded bytes"));
            }
            state[label]["sha256"] = json!(digest);
        } else if !array(&read["attachments"])?.is_empty() {
            return Err(invalid("plain text has unexpected attachments"));
        }
        state[label]["readback"] = msg["id"].clone();
        runtime::write_json(&receipt, &state)?;
    }
    if verify_thread()?["thread_metadata"]["archived"] == false {
        if read_only {
            return Err(invalid("read-only recheck found an active test thread"));
        }
        let mut response = api
            .agent
            .patch(format!("{API}/channels/{thread}"))
            .header("Authorization", &api.authorization)
            .send_json(json!({"archived":true}))
            .map_err(|_| io::Error::other("unable to archive the private fixture thread"))?;
        let row: Value = response
            .body_mut()
            .with_config()
            .limit(1 << 20)
            .read_json()
            .map_err(|_| invalid("invalid archive acknowledgment"))?;
        if row["id"] != thread || !exact(&row) {
            return Err(invalid("archive acknowledgment named another channel"));
        }
    }
    if verify_thread()?["thread_metadata"]["archived"] != true {
        return Err(invalid("private thread archive is unconfirmed"));
    }
    state["archived"] = json!(true);
    runtime::write_json(&receipt, &state)?;
    println!(
        "Existing Gateway remained connected. Two private-thread messages read back; CSV SHA-256 {digest}; thread archived."
    );
    Ok(())
}
