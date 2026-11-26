//! Gateway custody of one settled original Go thread.
mod capture;
use super::{
    config::{Config, LockDescription, Place, invalid, open_owner_file},
    gateway, guild,
    legacy_discussions::{self, Guard, Route},
    legacy_ingress::service::StatusSocket,
    legacy_state,
    rest::Discord,
};
use crate::{checkout::state::receipts::open_bolt, supervisor::runtime};
use bbolt_rs::{DbApi, TxApi};
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{
    fs::File,
    io,
    time::{Duration, Instant},
};

const RECHECK_INTERVAL: Duration = Duration::from_secs(5);

pub(super) fn run(place: &Place, cfg: &Config, capture_once: bool) -> io::Result<()> {
    let api = Discord::connect(cfg)?;
    observe(place, cfg, &api, capture_once, RECHECK_INTERVAL, |cfg| {
        let (mut socket, mut cursor) = gateway::open_observer(cfg)?;
        Ok(move || cursor.event(&mut socket))
    })
}

fn observe<ReadEvent: FnMut() -> io::Result<Option<Value>>>(
    place: &Place,
    cfg: &Config,
    api: &Discord,
    capture_once: bool,
    recheck_interval: Duration,
    open: impl FnOnce(&Config) -> io::Result<ReadEvent>,
) -> io::Result<()> {
    let _configuration = place.lock()?;
    legacy_state::verify_controls_place(place, cfg)?;
    if cfg.0 != place.load()?.0
        || cfg.text("mode") != "teammate"
        || cfg.text("project_access") == "vm"
        || !cfg.0["previous_supervisor"].is_null()
        || !cfg.0["calls"].is_null()
        || super::team_thread::linked_values(cfg).next().is_some()
    {
        return Err(invalid(
            "observer requires an original selected teammate enrollment",
        ));
    }
    let _bridge = LockDescription(runtime::lock(&place.dir.join("bridge.lock"))?);
    let _app = LockDescription(runtime::lock(
        &place
            .supervisor_root()
            .join(format!(".discord-app-{}.lock", cfg.text("application_id"))),
    )?);
    let config_file = place.dir.join("config.json");
    let config_pin = open_owner_file(&config_file)?;
    let config_digest = |file: &File| {
        legacy_state::file_digest(file, 1 << 20, Instant::now() + Duration::from_secs(1))
    };
    let startup_digest = config_digest(&config_pin)?;
    let verify_config = || {
        legacy_state::verify_controls_place(place, cfg)?;
        Ok::<_, io::Error>(
            legacy_state::unchanged(&config_pin, &config_file)?
                && config_digest(&open_owner_file(&config_file)?)? == startup_digest,
        )
    };
    if cfg.0 != place.load()?.0 || !verify_config()? {
        return Err(invalid("original connector configuration changed"));
    }
    let source = place.dir.join("discord.db");
    let (pin, db) = open_bolt(&source, 64 << 20)?;
    let (route, guard) = read_scope(&db.begin().map_err(io::Error::other)?, cfg)?;
    if capture_once {
        capture::binding(&db.begin().map_err(io::Error::other)?, cfg, &route, &guard)?;
    }
    let connector_state = if capture_once {
        "retained_teammate_capture_once"
    } else {
        "retained_teammate_read_only"
    };
    let status = StatusSocket::start(place, cfg, Some(connector_state))?;
    api.verify_app(cfg)?;
    let mut event = open(cfg)?;
    verify_boundary(api, cfg, &route, &guard)?;
    let verify_pins = || {
        if !legacy_state::unchanged(&pin, &source)? || !verify_config()? {
            return Err(invalid(
                "original connector evidence changed during observation",
            ));
        }
        Ok(())
    };
    verify_pins()?;
    status.online(true);
    let mut next_authority_check = Instant::now() + recheck_interval;
    while !status.stopping() {
        let frame = event()?;
        verify_pins()?;
        if let Some(frame) = frame {
            let data = &frame["d"];
            if capture_once
                && frame["t"] == "MESSAGE_CREATE"
                && data["guild_id"] == route.guild_id
                && data["channel_id"] == route.channel_id
                && data["id"] != guard.route.message_id
            {
                status.online(false);
                let captured = capture::read_source(api, cfg, &route, &guard, data)?;
                if status.stopping() {
                    return Ok(());
                }
                verify_pins()?;
                let source_identity = pin.metadata()?;
                drop(db);
                drop(pin);
                capture::commit(
                    &source,
                    &source_identity,
                    cfg,
                    &route,
                    &guard,
                    captured,
                    || Ok(!status.stopping() && verify_config()?),
                )?;
                println!("Captured one thread message in the original Go ingress queue.");
                return Ok(());
            }
            verify_event(cfg, &route, &guard, &frame)?;
        }
        if Instant::now() >= next_authority_check && !status.stopping() {
            status.online(false);
            verify_read_authority(api, cfg, &route)?;
            verify_pins()?;
            status.online(true);
            next_authority_check = Instant::now() + recheck_interval;
        }
    }
    Ok(())
}

fn read_scope<'tx>(tx: &impl TxApi<'tx>, cfg: &Config) -> io::Result<(Route, Guard)> {
    let mut inventory = legacy_state::inventory(cfg);
    legacy_state::scan(
        tx,
        &mut inventory,
        Instant::now() + Duration::from_secs(10),
        true,
    )?;
    if inventory.unresolved != 0 || !inventory.issues.is_empty() {
        return Err(invalid(
            "observer requires a settled original connector ledger",
        ));
    }
    legacy_discussions::verify(tx, cfg, cfg.text("daemon_socket"))?;
    legacy_discussions::observer_scope(tx)
}

fn verify_read_authority(api: &Discord, cfg: &Config, route: &Route) -> io::Result<()> {
    let unavailable = || invalid("original thread or read authority changed");
    let thread = api.request(&format!("/channels/{}", route.channel_id), None)?;
    let parent = api.request(&format!("/channels/{}", route.parent_id), None)?;
    if thread["id"] != route.channel_id
        || thread["guild_id"] != route.guild_id
        || thread["parent_id"] != route.parent_id
        || !matches!(thread["type"].as_u64(), Some(10 | 11))
        || thread["thread_metadata"]["archived"] != false
        || thread["thread_metadata"]["locked"] != false
        || parent["id"] != route.parent_id
        || parent["guild_id"] != route.guild_id
        || !matches!(parent["type"].as_u64(), Some(0 | 5 | 15 | 16))
    {
        return Err(unavailable());
    }
    let rights = guild::member_permissions(
        |path| guild::lookup(api, path),
        &route.parent_id,
        cfg.text("application_id"),
    )?;
    const READ: u64 = (1 << 10) | (1 << 16);
    if rights & (1 << 3) == 0 && rights & READ != READ {
        return Err(unavailable());
    }
    Ok(())
}

fn verify_boundary(api: &Discord, cfg: &Config, route: &Route, guard: &Guard) -> io::Result<()> {
    verify_read_authority(api, cfg, route)?;
    let unavailable = || invalid("original thread or settled read boundary changed");
    let messages = human_boundary_rows(api, cfg, route)?;
    let message = messages.first().ok_or_else(unavailable)?;
    verify_revision(route, guard, message)
}

fn human_boundary_rows(api: &Discord, cfg: &Config, route: &Route) -> io::Result<Vec<Value>> {
    let unavailable = || invalid("original thread or settled read boundary changed");
    let messages = api.request(
        &format!("/channels/{}/messages?limit=20", route.channel_id),
        None,
    )?;
    let rows = messages
        .as_array()
        .filter(|rows| rows.len() <= 20)
        .ok_or_else(unavailable)?;
    Ok(rows
        .iter()
        .filter(|message| {
            message["author"]["id"] != cfg.text("application_id")
                || message["author"]["bot"] != true
                || message["channel_id"] != route.channel_id
                || !message["guild_id"].is_null() && message["guild_id"] != route.guild_id
                || !message["webhook_id"].is_null()
        })
        .take(2)
        .cloned()
        .collect())
}

fn verify_revision(route: &Route, guard: &Guard, message: &Value) -> io::Result<()> {
    let unavailable = || invalid("original thread or settled read boundary changed");
    // shortcut: plain latest human message only, add revision formats with pinned Go witnesses.
    let content = message["content"]
        .as_str()
        .filter(|text| !text.is_empty())
        .ok_or_else(unavailable)?;
    if message["id"] != guard.route.message_id
        || message["channel_id"] != route.channel_id
        || (!message["guild_id"].is_null() && message["guild_id"] != route.guild_id)
        || message["author"]["id"] != guard.actor_id
        || message["author"]["bot"] == true
        || message["author"]["system"] == true
        || message["type"] != 0
        || !message["edited_timestamp"].is_null()
        || !message["message_reference"].is_null()
        || !message["webhook_id"].is_null()
        || !message["attachments"].as_array().is_some_and(Vec::is_empty)
    {
        return Err(unavailable());
    }
    let evidence = json!([
        guard.actor_id,
        content,
        route.channel_id,
        route.guild_id,
        guard.route.message_id,
        null,
        "",
        null
    ]);
    let canonical = crate::ipc::escape_json(serde_json::to_string(&evidence)?);
    if format!("{:x}", Sha256::digest(canonical)) != guard.revision {
        return Err(unavailable());
    }
    Ok(())
}

fn verify_event(cfg: &Config, route: &Route, guard: &Guard, frame: &Value) -> io::Result<()> {
    let data = &frame["d"];
    let changed = match frame["t"].as_str() {
        Some("GUILD_DELETE" | "GUILD_UPDATE") => data["id"] == route.guild_id,
        Some("GUILD_ROLE_CREATE" | "GUILD_ROLE_UPDATE" | "GUILD_ROLE_DELETE") => {
            data["guild_id"] == route.guild_id
        }
        Some("GUILD_MEMBER_UPDATE" | "GUILD_MEMBER_REMOVE") => {
            data["guild_id"] == route.guild_id && data["user"]["id"] == cfg.text("application_id")
        }
        Some("THREAD_MEMBERS_UPDATE") => {
            data["id"] == route.channel_id
                && data["removed_member_ids"]
                    .as_array()
                    .is_some_and(|ids| ids.iter().any(|id| id == cfg.text("application_id")))
        }
        Some("THREAD_LIST_SYNC") if data["guild_id"] == route.guild_id => {
            (data["channel_ids"].is_null()
                || data["channel_ids"]
                    .as_array()
                    .is_some_and(|ids| ids.iter().any(|id| id == &route.parent_id)))
                && data["threads"].as_array().is_none_or(|threads| {
                    !threads.iter().any(|thread| {
                        thread["id"] == route.channel_id && thread["parent_id"] == route.parent_id
                    })
                })
        }
        Some("THREAD_UPDATE" | "THREAD_DELETE" | "CHANNEL_UPDATE" | "CHANNEL_DELETE") => {
            data["id"] == route.channel_id || data["id"] == route.parent_id
        }
        Some("MESSAGE_CREATE" | "MESSAGE_UPDATE" | "MESSAGE_DELETE" | "MESSAGE_DELETE_BULK")
            if data["channel_id"] == route.channel_id =>
        {
            frame["t"] != "MESSAGE_CREATE"
                || data["guild_id"] != route.guild_id
                || data["id"] != guard.route.message_id
                || data["author"]["id"] != guard.actor_id
        }
        _ => false,
    };
    if changed {
        return Err(invalid(
            "retained teammate observation stopped at a changed route or source",
        ));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{
        fs,
        io::{BufRead, BufReader, Write},
        net::{TcpListener, TcpStream},
        process::Command,
        thread,
    };
    use tungstenite::{Message, accept};

    fn rest_fixture(
        rest: TcpListener,
        revoked: Option<std::path::PathBuf>,
        capture_fault: Option<&'static str>,
    ) -> thread::JoinHandle<()> {
        thread::spawn(move || {
            let mut responses = vec![
                (
                    "/users/@me",
                    json!({"bot":true,"id":"100","username":"fixture"}),
                ),
                (
                    "/channels/401",
                    json!({"id":"401","guild_id":"300","parent_id":"399","type":11,
                    "thread_metadata":{"archived":false,"locked":false}}),
                ),
                (
                    "/channels/399",
                    json!({"id":"399","guild_id":"300","type":0}),
                ),
                (
                    "/channels/399/permissions/member/100",
                    json!({"permissions":"66560"}),
                ),
                (
                    "/channels/401/messages?limit=20",
                    json!([{"id":"502","channel_id":"401","author":{"id":"100","bot":true}}, {"id":"501","channel_id":"401","author":{"id":"200"},
                    "content":"offline replay witness","type":0,"attachments":[]}]),
                ),
            ];
            if revoked.is_some() {
                responses.extend([responses[1].clone(), responses[2].clone()]);
                responses.push((
                    "/channels/399/permissions/member/100",
                    json!({"permissions":"0"}),
                ));
            }
            if let Some(fault) = capture_fault {
                responses.extend([
                    responses[1].clone(),
                    responses[2].clone(),
                    responses[3].clone(),
                ]);
                let mut latest = responses[4].1.as_array().unwrap().clone();
                latest.insert(
                    0,
                    json!({"id":"503","channel_id":"401","author":{"id":"200","username":"fixture"},
                    "content":"one plain follow-up","type":0,"mention_everyone":false,"mentions":[],
                    "mention_roles":[],"attachments":[]}),
                );
                if fault == "wrong source" {
                    latest[0]["author"]["id"] = json!("201");
                }
                responses.push(("/channels/401/messages?limit=20", json!(latest)));
                if fault != "wrong source" {
                    responses.extend([
                        responses[1].clone(),
                        responses[2].clone(),
                        responses[3].clone(),
                    ]);
                    if fault == "withdrawn" {
                        responses.last_mut().unwrap().1["permissions"] = json!("0");
                    }
                }
            }
            for (index, (path, response)) in responses.into_iter().enumerate() {
                if index == 7
                    && let Some(dir) = &revoked
                {
                    let status = super::super::local::call(dir, "status", "", None).unwrap();
                    assert_eq!(status["connected"], false);
                }
                let (mut stream, _) = rest.accept().unwrap();
                stream
                    .set_read_timeout(Some(Duration::from_secs(3)))
                    .unwrap();
                let mut input = BufReader::new(&mut stream);
                let mut line = String::new();
                input.read_line(&mut line).unwrap();
                assert_eq!(line, format!("GET /api/v10{path} HTTP/1.1\r\n"));
                let mut authenticated = false;
                loop {
                    line.clear();
                    assert!(input.read_line(&mut line).unwrap() > 0);
                    if line == "\r\n" {
                        break;
                    }
                    authenticated |= line
                        .trim_end()
                        .eq_ignore_ascii_case("authorization: Bot synthetic-unused-credential");
                }
                assert!(authenticated);
                let body = response.to_string();
                write!(stream, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}", body.len()).unwrap();
            }
        })
    }

    #[test]
    #[ignore = "requires the pinned original Go Discord fixture"]
    fn pinned_go_retained_thread_observation_preserves_replay() {
        let fixture = std::env::var_os("SWARM_TEST_GO_DISCORD_FIXTURE")
            .expect("set SWARM_TEST_GO_DISCORD_FIXTURE to the pinned helper");
        let root = std::env::temp_dir()
            .canonicalize()
            .unwrap()
            .join(format!("rt-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&root).unwrap();
        let dir = root.join("p");
        let p = dir.to_str().unwrap();
        let go = |args: &[&str]| {
            let output = Command::new(&fixture).args(args).output().unwrap();
            assert!(
                output.status.success(),
                "{}",
                String::from_utf8_lossy(&output.stderr)
            );
            String::from_utf8(output.stdout).unwrap()
        };
        go(&["create", p, root.to_str().unwrap(), "teammate", "true"]);
        go(&["bind-thread", p, "existing", "401", "399"]);
        go(&["traffic", p, "sent"]);
        assert_eq!(go(&["ingress", p, "existing", "501", "settle"]).trim(), "1");
        let place = Place::for_supervisor(&root).unwrap();
        let cfg = place.load().unwrap();
        let read_original = || {
            ["discord.db", "config.json", "fixture-token"]
                .map(|name| fs::read(dir.join(name)).unwrap())
        };
        let protected = read_original();
        let rest = TcpListener::bind("127.0.0.1:0").unwrap();
        let api = Discord::loopback_fixture(&cfg, rest.local_addr().unwrap().port()).unwrap();
        fn unopened(_: &Config) -> io::Result<fn() -> io::Result<Option<Value>>> {
            panic!("blocked observer opened a Gateway");
        }
        let held = LockDescription(runtime::lock(&dir.join("bridge.lock")).unwrap());
        assert!(observe(&place, &cfg, &api, false, RECHECK_INTERVAL, unopened).is_err());
        drop(held);
        let writer = LockDescription(runtime::lock(&dir.join("discord.db")).unwrap());
        assert!(observe(&place, &cfg, &api, false, RECHECK_INTERVAL, unopened).is_err());
        drop(writer);
        let rest_worker = rest_fixture(rest, None, None);
        let gateway = TcpListener::bind("127.0.0.1:0").unwrap();
        let address = gateway.local_addr().unwrap();
        let gateway_worker = thread::spawn(move || {
            let mut socket = accept(gateway.accept().unwrap().0).unwrap();
            gateway::send(
                &mut socket,
                json!({"op":10,"d":{"heartbeat_interval":30000}}),
            )
            .unwrap();
            let Message::Text(identify) = socket.read().unwrap() else {
                panic!("expected identify")
            };
            let identify: Value = serde_json::from_str(&identify).unwrap();
            assert_eq!(identify["d"]["intents"], gateway::OBSERVER_INTENTS);
            gateway::send(
                &mut socket,
                json!({"op":0,"t":"READY","s":1,
                "d":{"application":{"id":"100"},"user":{"id":"100","bot":true}}}),
            )
            .unwrap();
            gateway::send(
                &mut socket,
                json!({"op":0,"t":"GUILD_CREATE","s":2,"d":{"id":"300"}}),
            )
            .unwrap();
            for (seq, guild, channel, id) in [
                (3, "300", "401", "501"),
                (4, "301", "402", "502"),
                (5, "300", "401", "503"),
            ] {
                gateway::send(
                    &mut socket,
                    json!({"op":0,"t":"MESSAGE_CREATE","s":seq,"d":
                    {"id":id,"guild_id":guild,"channel_id":channel,"author":{"id":"200"}}}),
                )
                .unwrap();
            }
        });
        let err = observe(&place, &cfg, &api, false, RECHECK_INTERVAL, |cfg| {
            let stream = TcpStream::connect(address)?;
            stream.set_read_timeout(Some(Duration::from_secs(3)))?;
            let (mut socket, _) =
                tungstenite::client(format!("ws://{address}"), stream).map_err(io::Error::other)?;
            let mut cursor = gateway::ready_with(
                &mut socket,
                cfg,
                "synthetic-unused-credential",
                gateway::OBSERVER_INTENTS,
                false,
            )?;
            let mut checked = false;
            let status_dir = dir.clone();
            Ok(move || {
                if !checked {
                    let status = super::super::local::call(&status_dir, "status", "", None)?;
                    assert_eq!(status["connected"], true);
                    assert_eq!(status["connector"]["state"], "retained_teammate_read_only");
                    checked = true;
                }
                cursor.event(&mut socket)
            })
        })
        .unwrap_err();
        assert!(err.to_string().contains("changed route or source"), "{err}");
        rest_worker.join().unwrap();
        gateway_worker.join().unwrap();
        assert_eq!(read_original(), protected);
        assert!(!dir.join("bridge.sock").exists());
        assert!(!dir.join("rust-text-v1.json").exists());
        for fault in [
            "quiet role loss",
            "in-place rewrite",
            "bot membership event",
        ] {
            let quiet_role_loss = fault == "quiet role loss";
            let rewrite = fault == "in-place rewrite";
            let rest = TcpListener::bind("127.0.0.1:0").unwrap();
            let api = Discord::loopback_fixture(&cfg, rest.local_addr().unwrap().port()).unwrap();
            let rest_worker = rest_fixture(rest, quiet_role_loss.then(|| dir.clone()), None);
            let config_file = dir.join("config.json");
            use std::os::unix::fs::MetadataExt;
            let original_inode = fs::metadata(&config_file).unwrap().ino();
            let disabled = std::str::from_utf8(&protected[1])
                .unwrap()
                .replace(r#""enabled": true"#, r#""enabled":false"#);
            assert_ne!(disabled.as_bytes(), &protected[1]);
            assert_eq!(disabled.len(), protected[1].len());
            let error =
                observe(&place, &cfg, &api, false, Duration::ZERO, |_| {
                    let mut checked = false;
                    let (dir, config_file, disabled) = (&dir, &config_file, &disabled);
                    Ok(move || {
                        assert!(
                            !std::mem::replace(&mut checked, true),
                            "observer stayed online"
                        );
                        let status = super::super::local::call(dir, "status", "", None)?;
                        assert_eq!(status["connected"], true);
                        if rewrite {
                            fs::write(config_file, disabled)?;
                            assert_eq!(fs::metadata(config_file)?.ino(), original_inode);
                        }
                        Ok((fault == "bot membership event").then(|| json!({
                        "t":"GUILD_MEMBER_UPDATE", "d":{"guild_id":"300","user":{"id":"100"}}
                    })))
                    })
                })
                .unwrap_err();
            let expected = match fault {
                "quiet role loss" => "read authority changed",
                "in-place rewrite" => "connector evidence changed",
                _ => "changed route or source",
            };
            assert!(error.to_string().contains(expected), "{error}");
            rest_worker.join().unwrap();
            assert!(super::super::local::call(&dir, "status", "", None).is_err());
            assert_eq!(fs::read(dir.join("discord.db")).unwrap(), protected[0]);
            assert_eq!(fs::read(dir.join("fixture-token")).unwrap(), protected[2]);
            assert_eq!(
                fs::read(&config_file).unwrap(),
                if rewrite {
                    disabled.as_bytes()
                } else {
                    &protected[1][..]
                }
            );
            go(&["bridge-lease", p]);
            fs::write(&config_file, &protected[1]).unwrap();
        }
        assert_eq!(go(&["traffic-read", p, "sent"]).trim(), "sent");
        let original: Value = serde_json::from_str(&go(&["ingress-state", p, "existing"])).unwrap();
        for fault in ["wrong source", "withdrawn", "valid"] {
            let rest = TcpListener::bind("127.0.0.1:0").unwrap();
            let api = Discord::loopback_fixture(&cfg, rest.local_addr().unwrap().port()).unwrap();
            let server = rest_fixture(rest, None, Some(fault));
            let result = observe(&place, &cfg, &api, true, RECHECK_INTERVAL, |_| {
                assert!(runtime::lock(&dir.join("bridge.lock")).is_err());
                Ok(|| {
                    Ok(Some(json!({"t":"MESSAGE_CREATE","d":{
                        "id":"503","guild_id":"300","channel_id":"401","author":{"id":"200"}
                    }})))
                })
            });
            assert_eq!(result.is_ok(), fault == "valid", "{result:?}");
            server.join().unwrap();
            assert!(!dir.join("bridge.sock").exists());
            go(&["bridge-lease", p]);
            if fault != "valid" {
                assert_eq!(read_original(), protected);
            }
        }
        let reopened: Value = serde_json::from_str(&go(&["ingress-state", p, "existing"])).unwrap();
        assert_eq!(reopened["checkpoint"], original["checkpoint"]);
        assert_eq!(
            reopened["binding"]["Boundary"],
            original["binding"]["Boundary"]
        );
        let pending = reopened["pending"].as_array().unwrap();
        assert_eq!(pending.len(), 1);
        assert_eq!(
            pending[0]["Route"],
            json!({"guild_id":"300","channel_id":"401","message_id":"503"})
        );
        assert_eq!(pending[0]["Author"], json!({"id":"200","name":"fixture"}));
        assert_eq!(pending[0]["State"], "prepared");
        assert_eq!(pending[0]["Admission"], Value::Null);
        assert_eq!(
            pending[0]["Sequence"],
            reopened["binding"]["LastIngressSequence"]
        );
        let preserved = read_original();
        assert_eq!(&preserved[1..], &protected[1..]);
        assert!(observe(&place, &cfg, &api, true, RECHECK_INTERVAL, unopened).is_err());
        assert_eq!(read_original(), preserved);
        assert_eq!(go(&["ingress", p, "existing", "501", "replay"]).trim(), "1");
        assert_eq!(go(&["ingress", p, "existing", "503", "replay"]).trim(), "1");
        assert_eq!(go(&["traffic-read", p, "sent"]).trim(), "sent");
        fs::remove_dir_all(root).unwrap();
    }
}
