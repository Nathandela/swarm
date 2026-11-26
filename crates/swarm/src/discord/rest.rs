//! Discord v10 enrollment and read-side verification. Credentials stay in-process.
#[cfg(test)]
mod live_transport;

use super::config::{Config, invalid, load_token, snowflake};
use crate::supervisor::mail_files::Captured;
use serde_json::{Value, json};
use std::io;
use std::path::Path;
use std::time::Duration;

const API: &str = "https://discord.com/api/v10";
const MAX_RESPONSE: u64 = 1 << 20;

pub(super) struct Discord {
    agent: ureq::Agent,
    authorization: String,
    #[cfg(test)]
    fixture_origin: Option<String>,
}

pub(super) enum Post {
    Acknowledged(Value),
    RateLimited(u64),
    Unconfirmed,
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
            #[cfg(test)]
            fixture_origin: None,
        })
    }

    #[cfg(test)]
    pub(super) fn loopback_fixture(cfg: &Config, port: u16) -> io::Result<Self> {
        let token = load_token(Path::new(cfg.text("token_file")))?;
        let agent = ureq::Agent::config_builder()
            .proxy(None)
            .max_redirects(0)
            .timeout_global(Some(Duration::from_secs(3)))
            .build()
            .new_agent();
        Ok(Self {
            agent,
            authorization: format!("Bot {token}"),
            fixture_origin: Some(format!("http://127.0.0.1:{port}/api/v10")),
        })
    }

    fn endpoint(&self, path: &str) -> String {
        #[cfg(test)]
        if let Some(origin) = &self.fixture_origin {
            return format!("{origin}{path}");
        }
        format!("{API}{path}")
    }

    pub(super) fn request(&self, path: &str, body: Option<&Value>) -> io::Result<Value> {
        let endpoint = self.endpoint(path);
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
            ureq::Error::StatusCode(401 | 403) => io::Error::new(
                io::ErrorKind::PermissionDenied,
                "Discord API rejected the bot credential or permissions",
            ),
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

    pub(super) fn create_public_thread(&self, parent: &str, message: &str, name: &str) -> Post {
        if !snowflake(parent) || !snowflake(message) {
            return Post::Unconfirmed;
        }
        self.post_json(
            &self.endpoint(&format!("/channels/{parent}/messages/{message}/threads")),
            &json!({"name":name}),
        )
    }

    fn post_json(&self, url: &str, body: &Value) -> Post {
        let response = self
            .agent
            .post(url)
            .config()
            .http_status_as_error(false)
            .build()
            .header("Authorization", &self.authorization)
            .send_json(body);
        post_outcome(response)
    }

    pub(super) fn post_publication(
        &self,
        cfg: &Config,
        nonce: &str,
        body: &str,
        files: &[Captured],
    ) -> Post {
        self.post_publication_channel(cfg.text("dm_channel_id"), nonce, body, files)
    }

    pub(super) fn post_publication_channel(
        &self,
        channel: &str,
        nonce: &str,
        body: &str,
        files: &[Captured],
    ) -> Post {
        if !snowflake(channel) {
            return Post::Unconfirmed;
        }
        let url = self.endpoint(&format!("/channels/{channel}/messages"));
        let request = self
            .agent
            .post(url)
            .config()
            .http_status_as_error(false)
            .build()
            .header("Authorization", &self.authorization);
        let payload = json!({"content":body,"nonce":nonce,"enforce_nonce":true,"allowed_mentions":{"parse":[]}});
        let response = if files.is_empty() {
            request.send_json(payload)
        } else {
            let Ok((boundary, data)) = file_multipart(payload, nonce, files) else {
                return Post::Unconfirmed;
            };
            request
                .header(
                    "Content-Type",
                    format!("multipart/form-data; boundary={boundary}"),
                )
                .send(data.as_slice())
        };
        post_outcome(response)
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

fn file_multipart(
    mut payload: Value,
    nonce: &str,
    files: &[Captured],
) -> io::Result<(String, Vec<u8>)> {
    use std::io::Write;
    if nonce.len() != 24 || !nonce.bytes().all(|byte| byte.is_ascii_hexdigit()) {
        return Err(invalid("invalid publication attachment nonce"));
    }
    payload["attachments"] = json!(
        files
            .iter()
            .enumerate()
            .map(|(id, item)| json!({"id":id,"filename":item.file.name}))
            .collect::<Vec<_>>()
    );
    let payload = serde_json::to_vec(&payload)?;
    let boundary = (0..8)
        .map(|counter| format!("swarm-{nonce}-{counter}"))
        .find(|boundary| {
            let bytes = boundary.as_bytes();
            !payload.windows(bytes.len()).any(|window| window == bytes)
                && files
                    .iter()
                    .all(|file| !file.data.windows(bytes.len()).any(|window| window == bytes))
        })
        .ok_or_else(|| invalid("worker file data conflicts with multipart framing"))?;
    let mut body = Vec::new();
    write!(
        &mut body,
        "--{boundary}\r\nContent-Disposition: form-data; name=\"payload_json\"\r\nContent-Type: application/json\r\n\r\n"
    )?;
    body.extend_from_slice(&payload);
    body.extend_from_slice(b"\r\n");
    for (id, item) in files.iter().enumerate() {
        let filename = item.file.name.replace('"', "\\\"");
        write!(
            &mut body,
            "--{boundary}\r\nContent-Disposition: form-data; name=\"files[{id}]\"; filename=\"{filename}\"\r\nContent-Type: {}\r\n\r\n",
            item.file.content_type
        )?;
        body.extend_from_slice(&item.data);
        body.extend_from_slice(b"\r\n");
    }
    write!(&mut body, "--{boundary}--\r\n")?;
    Ok((boundary, body))
}

fn post_outcome(response: Result<ureq::http::Response<ureq::Body>, ureq::Error>) -> Post {
    let mut response = match response {
        Ok(response) => response,
        Err(_) => return Post::Unconfirmed,
    };
    if response.status().as_u16() == 429 {
        let header = response
            .headers()
            .get("retry-after")
            .and_then(|header| header.to_str().ok())
            .and_then(|value| value.parse::<f64>().ok())
            .filter(|secs| secs.is_finite() && *secs > 0.0)
            .map(|secs| (secs * 1000.0).ceil() as u64)
            .unwrap_or_default();
        let body = response
            .body_mut()
            .with_config()
            .limit(MAX_RESPONSE)
            .read_json::<Value>()
            .ok()
            .and_then(|body| body["retry_after"].as_f64())
            .filter(|seconds| seconds.is_finite() && *seconds > 0.0)
            .map(|seconds| (seconds * 1000.0).ceil() as u64)
            .unwrap_or_default();
        return Post::RateLimited(if header.max(body) > 0 {
            header.max(body)
        } else {
            60_000
        });
    }
    if !response.status().is_success() {
        return Post::Unconfirmed;
    }
    match response
        .body_mut()
        .with_config()
        .limit(MAX_RESPONSE)
        .read_json::<Value>()
    {
        Ok(message) => Post::Acknowledged(message),
        Err(_) => Post::Unconfirmed,
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
    #[ignore = "requires local openssl and Node; uses only loopback TLS"]
    fn loopback_tls_distinguishes_definitive_throttle_from_lost_response() {
        use std::io::{BufRead, BufReader};
        use std::process::{Child, Command, Stdio};
        use ureq::tls::{Certificate, RootCerts, TlsConfig};
        struct Stop(Child);
        impl Drop for Stop {
            fn drop(&mut self) {
                let _ = self.0.kill();
                let _ = self.0.wait();
            }
        }
        let dir = std::env::temp_dir().join(format!(
            "swarm-rate-tls-{}",
            crate::supervisor::runtime::random_id().unwrap()
        ));
        crate::supervisor::runtime::private_dir(&dir).unwrap();
        let status = Command::new("openssl")
            .current_dir(&dir)
            .args([
                "req",
                "-x509",
                "-newkey",
                "rsa:2048",
                "-nodes",
                "-days",
                "1",
                "-subj",
                "/CN=127.0.0.1",
                "-addext",
                "subjectAltName=IP:127.0.0.1",
                "-addext",
                "basicConstraints=critical,CA:FALSE",
                "-keyout",
                "key.pem",
                "-out",
                "cert.pem",
            ])
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .status()
            .unwrap();
        assert!(status.success());
        let js = r#"const https=require('node:https'),fs=require('node:fs'); let n=0;
const server=https.createServer({key:fs.readFileSync('key.pem'),cert:fs.readFileSync('cert.pem')},(req,res)=>{
  if(req.method==='GET'&&req.url==='/attachments/2/22/data.csv'&&req.headers.authorization===undefined) {
    res.writeHead(200,{'content-length':'4'}).end('a,b\n'); return;
  }
  if(req.method!=='POST'||req.url!=='/channels/2/messages/9/threads'||req.headers.authorization!=='Bot fixture') {
    res.writeHead(400).end();return;
  }
  req.resume();
  req.on('end',()=>{ if(++n===1){
    res.writeHead(429,{'retry-after':'0.05','content-type':'application/json'}).end('{"retry_after":0.2}');
  }else req.socket.destroy(); });
});
server.listen(0,'127.0.0.1',()=>console.log(server.address().port));"#;
        let child = Command::new("node")
            .current_dir(&dir)
            .args(["-e", js])
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .spawn()
            .unwrap();
        let mut child = Stop(child);
        let mut port = String::new();
        BufReader::new(child.0.stdout.take().unwrap())
            .read_line(&mut port)
            .unwrap();
        let port = port.trim().parse::<u16>().unwrap();
        let certificate = std::fs::read(dir.join("cert.pem")).unwrap();
        let roots = RootCerts::new_with_certs(&[Certificate::from_pem(&certificate).unwrap()]);
        let tls = TlsConfig::builder().root_certs(roots).build();
        let api = Discord {
            authorization: "Bot fixture".into(),
            fixture_origin: None,
            agent: ureq::Agent::config_builder()
                .https_only(true)
                .proxy(None)
                .max_redirects(0)
                .tls_config(tls)
                .timeout_global(Some(Duration::from_secs(2)))
                .build()
                .new_agent(),
        };
        let url = format!("https://127.0.0.1:{port}/channels/2/messages/9/threads");
        assert!(matches!(
            api.post_json(&url, &json!({"name":"fixture"})),
            Post::RateLimited(200)
        ));
        assert!(matches!(
            api.post_json(&url, &json!({"name":"fixture"})),
            Post::Unconfirmed
        ));
        let attachment = format!("https://127.0.0.1:{port}/attachments/2/22/data.csv");
        assert_eq!(
            super::super::received_files::fetch(&api.agent, &attachment, 4).unwrap(),
            b"a,b\n"
        );
        drop(child);
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn attachments_form_uses_one_correlated_payload_and_exact_parts() {
        use crate::supervisor::mail_files::Descriptor;
        let nonce = "0123456789abcdef01234567";
        let data = format!("row,swarm-{nonce}-0\n").into_bytes();
        let files = [Captured {
            file: Descriptor {
                id: "file".into(),
                name: "sample\"data.csv".into(),
                content_type: "text/csv".into(),
                sha256: String::new(),
                size: data.len(),
            },
            data: data.clone(),
        }];
        let payload = json!({"nonce":nonce,"enforce_nonce":true,"allowed_mentions":{"parse":[]}});
        let (boundary, body) = file_multipart(payload, nonce, &files).unwrap();
        assert_eq!(boundary, format!("swarm-{nonce}-1"));
        let body = String::from_utf8(body).unwrap();
        let pieces: Vec<_> = body.split(&format!("--{boundary}")).collect();
        assert_eq!(pieces.len(), 4);
        let payload: Value =
            serde_json::from_str(pieces[1].split_once("\r\n\r\n").unwrap().1.trim()).unwrap();
        assert_eq!(payload["nonce"], nonce);
        assert_eq!(payload["enforce_nonce"], true);
        assert_eq!(payload["allowed_mentions"]["parse"], json!([]));
        assert_eq!(
            payload["attachments"],
            json!([{"id":0,"filename":"sample\"data.csv"}])
        );
        assert!(pieces[2].contains("name=\"files[0]\"; filename=\"sample\\\"data.csv\""));
        assert_eq!(
            pieces[2]
                .split_once("\r\n\r\n")
                .unwrap()
                .1
                .strip_suffix("\r\n")
                .unwrap()
                .as_bytes(),
            data
        );
    }

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
