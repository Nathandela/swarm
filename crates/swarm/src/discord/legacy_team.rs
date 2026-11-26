//! Gateway custody and capture of one original Go thread.
mod admit;
mod capture;
mod gap;
mod history;
mod history_pages;
mod prepare;
mod render;
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

#[derive(Clone, Copy)]
enum Mode<'a> {
    Observe,
    Capture,
    Prepare {
        message: &'a str,
        dry_run: bool,
        before_commit: fn(),
    },
    Admit {
        message: &'a str,
        dry_run: bool,
        before_commit: fn(),
    },
    StagePublication {
        message: &'a str,
        dry_run: bool,
    },
}

pub(super) fn run(place: &Place, cfg: &Config, capture_once: bool) -> io::Result<()> {
    let api = Discord::connect(cfg)?;
    let mode = if capture_once {
        Mode::Capture
    } else {
        Mode::Observe
    };
    observe(place, cfg, &api, mode, RECHECK_INTERVAL, |cfg| {
        let (mut socket, mut cursor) = gateway::open_observer(cfg)?;
        Ok(move || cursor.event(&mut socket))
    })
}

pub(super) fn prepare_ingress(place: &Place, message: &str, dry_run: bool) -> io::Result<bool> {
    let cfg = place.load()?;
    let api = Discord::connect(&cfg)?;
    prepare_with(place, &cfg, &api, message, dry_run)?;
    Ok(true)
}

pub(super) fn admit_ingress(place: &Place, message: &str, dry_run: bool) -> io::Result<bool> {
    let cfg = place.load()?;
    let api = Discord::connect(&cfg)?;
    continue_without_gateway(
        place,
        &cfg,
        &api,
        message,
        Mode::Admit {
            message,
            dry_run,
            before_commit: || {},
        },
    )?;
    Ok(true)
}

pub(super) fn stage_publication(place: &Place, message: &str, dry_run: bool) -> io::Result<bool> {
    let cfg = place.load()?;
    let api = Discord::connect(&cfg)?;
    continue_without_gateway(
        place,
        &cfg,
        &api,
        message,
        Mode::StagePublication { message, dry_run },
    )?;
    Ok(true)
}

fn prepare_with(
    place: &Place,
    cfg: &Config,
    api: &Discord,
    message: &str,
    dry_run: bool,
) -> io::Result<()> {
    continue_without_gateway(
        place,
        cfg,
        api,
        message,
        Mode::Prepare {
            message,
            dry_run,
            before_commit: || {},
        },
    )
}

fn continue_without_gateway(
    place: &Place,
    cfg: &Config,
    api: &Discord,
    message: &str,
    mode: Mode<'_>,
) -> io::Result<()> {
    if !super::config::snowflake(message) {
        return Err(invalid("invalid retained teammate message ID"));
    }
    observe(place, cfg, api, mode, RECHECK_INTERVAL, |_| {
        Err::<fn() -> io::Result<Option<Value>>, _>(invalid("preparation cannot open Gateway"))
    })
}

fn observe<ReadEvent: FnMut() -> io::Result<Option<Value>>>(
    place: &Place,
    cfg: &Config,
    api: &Discord,
    mode: Mode<'_>,
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
    let changing = matches!(
        mode,
        Mode::Prepare { .. } | Mode::Admit { .. } | Mode::StagePublication { .. }
    );
    if changing {
        prepare::verify_config(open_owner_file(&config_file)?, cfg)?;
    }
    let source = place.dir.join("discord.db");
    let (pin, db) = open_bolt(&source, 64 << 20)?;
    let (route, guard, pending, binding, native_preparation) = {
        let tx = db.begin().map_err(io::Error::other)?;
        let (pending, native_preparation) = match mode {
            Mode::Observe => (history::accepted(&tx, cfg)?, false),
            Mode::StagePublication { message, .. } => {
                let accepted = history::accepted(&tx, cfg)?
                    .filter(|pending| pending.candidate.route["message_id"] == message)
                    .ok_or_else(|| {
                        invalid("requires the exact original accepted teammate message")
                    })?;
                (Some(accepted), false)
            }
            Mode::Admit { message, .. } => (Some(admit::pending(&tx, cfg, message)?), false),
            Mode::Prepare { message, .. } => match capture::pending(&tx, cfg) {
                Ok(pending) => (pending, false),
                Err(error) => (
                    Some(admit::pending(&tx, cfg, message).map_err(|_| error)?),
                    true,
                ),
            },
            Mode::Capture => (capture::pending(&tx, cfg)?, false),
        };
        let history_service =
            matches!(mode, Mode::Observe | Mode::StagePublication { .. }) && pending.is_some();
        let (route, guard) = read_scope_for_history(&tx, cfg, pending.as_ref(), history_service)?;
        if changing || history_service {
            prepare::verify_ledger(&tx, cfg, &route, &guard)?;
        }
        let binding = (!matches!(mode, Mode::Observe) || history_service)
            .then(|| capture::binding(&tx, cfg, &route, &guard, pending.as_ref()))
            .transpose()?;
        (route, guard, pending, binding, native_preparation)
    };
    let connector_state = match mode {
        Mode::Observe if pending.is_some() => "retained_teammate_history",
        Mode::Observe => "retained_teammate_read_only",
        Mode::Capture => "retained_teammate_capture_once",
        Mode::Prepare { .. } => "retained_teammate_preparing",
        Mode::Admit { .. } => "retained_teammate_admitting",
        Mode::StagePublication { .. } => "retained_teammate_staging_publication",
    };
    let status = StatusSocket::start(place, cfg, Some(connector_state))?;
    let verify_pins = || {
        if !legacy_state::unchanged(&pin, &source)? || !verify_config()? {
            return Err(invalid(
                "original connector evidence changed during observation",
            ));
        }
        Ok(())
    };
    api.verify_app(cfg)?;
    if matches!(mode, Mode::Observe | Mode::StagePublication { .. })
        && let (Some(pending), Some(binding)) = (&pending, &binding)
    {
        prepare::verify_config(open_owner_file(&config_file)?, cfg)?;
        let bound = prepare::ready_discussion(binding)?;
        let recheck = || {
            let tx = db.begin().map_err(io::Error::other)?;
            if history::accepted(&tx, cfg)?.as_ref() != Some(pending)
                || capture::binding(&tx, cfg, &route, &guard, Some(pending))? != *binding
            {
                return Err(invalid("original teammate source binding changed"));
            }
            history::verify_record(&tx, cfg, pending, &bound)?;
            let key = history_pages::key(&tx)?;
            let input = render::current_history(api, cfg, &route, &guard, pending, &bound)?;
            verify_pins()?;
            if status.stopping() {
                return Err(invalid("original teammate place stopped"));
            }
            Ok(history::Scope { input, key })
        };
        if let Mode::StagePublication { dry_run, .. } = mode {
            return history::publication::serve(
                place,
                cfg,
                binding,
                &status,
                dry_run,
                recheck,
                || {
                    verify_pins()?;
                    api.verify_credential(cfg)
                },
            );
        }
        return history::serve(place, cfg, binding, &status, recheck, |cursor| {
            history_pages::older(api, cfg, &route, cursor)
        });
    }
    if let Mode::Prepare {
        message, dry_run, ..
    } = mode
        && !native_preparation
    {
        let pending = pending
            .as_ref()
            .filter(|row| row.candidate.route["message_id"] == message)
            .ok_or_else(|| invalid("requires the exact original captured teammate message"))?;
        let binding = binding
            .as_ref()
            .ok_or_else(|| invalid("original thread binding is unavailable"))?;
        prepare::ready_discussion(binding)?;
        capture::verify_pending(api, cfg, &route, &guard, pending, true)?;
        let manager = prepare::Manager::claim(place, cfg, binding, None)?;
        verify_pins()?;
        if status.stopping() {
            return Ok(());
        }
        if dry_run {
            println!("The original Go connector can resume this teammate message.");
            return Ok(());
        }
        let source_identity = pin.metadata()?;
        drop(db);
        drop(pin);
        prepare::commit(
            &source,
            &source_identity,
            cfg,
            pending,
            binding,
            |route, guard| {
                capture::verify_pending(api, cfg, route, guard, pending, true)?;
                manager.verify(place, cfg)?;
                if status.stopping() || !verify_config()? {
                    return Err(invalid("original teammate place stopped or changed"));
                }
                Ok(())
            },
        )?;
        println!("Prepared the original teammate message for Go to resume.");
        return Ok(());
    }
    let advance = match mode {
        Mode::Admit {
            message,
            dry_run,
            before_commit,
        }
        | Mode::Prepare {
            message,
            dry_run,
            before_commit,
        } => Some((message, dry_run, before_commit)),
        _ => None,
    };
    if let Some((message, dry_run, before_commit)) = advance {
        let pending = pending
            .as_ref()
            .ok_or_else(|| invalid("original sending record is unavailable"))?;
        let binding = binding
            .as_ref()
            .ok_or_else(|| invalid("original thread binding is unavailable"))?;
        let bound = prepare::ready_discussion(binding)?;
        let recheck = || {
            {
                let tx = db.begin().map_err(io::Error::other)?;
                let fresh = admit::pending(&tx, cfg, message)?;
                if fresh != *pending
                    || capture::binding(&tx, cfg, &route, &guard, Some(&fresh))? != *binding
                {
                    return Err(invalid("original teammate ingress changed"));
                }
                admit::verify_record(&tx, cfg, &fresh, &bound)?;
            }
            let current = render::current(api, cfg, &route, &guard, pending, &bound)?;
            verify_pins()?;
            if status.stopping() {
                return Err(invalid("original teammate place stopped"));
            }
            Ok(current)
        };
        let input = recheck()?;
        return admit::advance(
            place,
            cfg,
            binding,
            &input,
            admit::Advancement {
                native: native_preparation,
                dry_run,
            },
            recheck,
            || {
                before_commit();
                status.commit_guard()
            },
        );
    }
    if let Some(pending) = pending {
        capture::verify_pending(api, cfg, &route, &guard, &pending, false)?;
        verify_pins()?;
        if !status.stopping() {
            println!("Verified that the original captured thread message is still prepared.");
        }
        return Ok(());
    }
    let mut event = open(cfg)?;
    verify_boundary(api, cfg, &route, &guard)?;
    verify_pins()?;
    status.online(true);
    let mut next_authority_check = Instant::now() + recheck_interval;
    while !status.stopping() {
        let frame = event()?;
        verify_pins()?;
        if let Some(frame) = frame {
            let data = &frame["d"];
            if matches!(mode, Mode::Capture)
                && frame["t"] == "MESSAGE_CREATE"
                && data["guild_id"] == route.guild_id
                && data["channel_id"] == route.channel_id
                && data["id"] != guard.route.message_id
            {
                status.online(false);
                let captured = capture::read_source(api, cfg, &route, &guard, data, false)?;
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
                    |captured| {
                        if status.stopping() || !verify_config()? {
                            return Ok(false);
                        }
                        let fresh = capture::read_source(api, cfg, &route, &guard, data, false)?;
                        Ok(fresh == *captured && !status.stopping() && verify_config()?)
                    },
                )?;
                println!("Captured one thread message in the original Go ingress queue.");
                return Ok(());
            }
            verify_event(cfg, &route, &guard, &frame)?;
        }
        if Instant::now() >= next_authority_check && !status.stopping() {
            status.online(false);
            verify_thread_authority(api, cfg, &route, false)?;
            verify_pins()?;
            status.online(true);
            next_authority_check = Instant::now() + recheck_interval;
        }
    }
    Ok(())
}

fn read_scope<'tx>(
    tx: &impl TxApi<'tx>,
    cfg: &Config,
    pending: Option<&capture::Pending>,
) -> io::Result<(Route, Guard)> {
    read_scope_for_history(tx, cfg, pending, false)
}

fn read_scope_for_history<'tx>(
    tx: &impl TxApi<'tx>,
    cfg: &Config,
    pending: Option<&capture::Pending>,
    history: bool,
) -> io::Result<(Route, Guard)> {
    let mut inventory = legacy_state::inventory(cfg);
    legacy_state::scan(
        tx,
        &mut inventory,
        Instant::now() + Duration::from_secs(10),
        true,
        pending.map(|row| (row.ingress_key.as_slice(), row.pending_key.as_slice())),
        true,
    )?;
    if inventory.unresolved != 0 || !inventory.issues.is_empty() {
        return Err(invalid(
            "observer requires a settled original connector ledger",
        ));
    }
    let (route, guard) = legacy_discussions::observer_scope(tx)?;
    let settled_gap = gap::settled(tx, cfg, &route)?;
    if settled_gap {
        prepare::verify_ledger(tx, cfg, &route, &guard)?;
    }
    legacy_discussions::verify(
        tx,
        cfg,
        cfg.text("daemon_socket"),
        u64::from(pending.is_some()),
        settled_gap,
        history,
    )?;
    Ok((route, guard))
}

fn verify_thread_authority(
    api: &Discord,
    cfg: &Config,
    route: &Route,
    sending: bool,
) -> io::Result<()> {
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
        || sending && parent["type"] == 16
    {
        return Err(unavailable());
    }
    let rights = guild::member_permissions(
        |path| guild::lookup(api, path),
        &route.parent_id,
        cfg.text("application_id"),
    )?;
    const READ: u64 = (1 << 10) | (1 << 16);
    let required = if sending { READ | (1 << 38) } else { READ };
    if rights & (1 << 3) == 0 && rights & required != required {
        return Err(unavailable());
    }
    Ok(())
}

fn verify_boundary(api: &Discord, cfg: &Config, route: &Route, guard: &Guard) -> io::Result<()> {
    verify_thread_authority(api, cfg, route, false)?;
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
    if plain_revision(route, &guard.route.message_id, &guard.actor_id, content)? != guard.revision {
        return Err(unavailable());
    }
    Ok(())
}

fn plain_revision(route: &Route, id: &str, actor: &str, content: &str) -> io::Result<String> {
    let evidence = json!([
        actor,
        content,
        route.channel_id,
        route.guild_id,
        id,
        null,
        "",
        null
    ]);
    let canonical = crate::ipc::escape_json(serde_json::to_string(&evidence)?);
    Ok(format!("{:x}", Sha256::digest(canonical)))
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
pub(super) fn native_prepare_witness() {
    let root =
        std::fs::canonicalize(std::env::var_os("SWARM_TEST_DISCORD_ACTIVE_ROOT").unwrap()).unwrap();
    let place = Place::for_supervisor(&root).unwrap();
    let phase = std::env::var("SWARM_TEST_DISCORD_GUILD_PHASE").unwrap();
    let cfg = match place.load() {
        Err(_) if phase == "refuse" => return,
        result => result.unwrap(),
    };
    let port = std::env::var("SWARM_TEST_DISCORD_REST_PORT")
        .unwrap()
        .parse()
        .unwrap();
    if phase.starts_with("publish-") {
        isolated_guild_fixture(&place, &cfg).unwrap();
        super::rest::LOOPBACK_FIXTURE_PORT.set(Some(port));
        let mut args = if phase == "publish-observer" {
            vec!["run", "--observe-retained-teammate"]
        } else {
            let message = if phase == "publish-wrong-message" {
                "702"
            } else {
                "701"
            };
            vec!["migrate-state", "--stage-teammate-publication", message]
        };
        if phase.ends_with("dry-run") {
            args.push("--dry-run");
        }
        let code = super::run(&args.into_iter().map(str::to_owned).collect::<Vec<_>>());
        super::rest::LOOPBACK_FIXTURE_PORT.set(None);
        assert_eq!(
            code,
            match phase.as_str() {
                "publish-stage" | "publish-observer" | "publish-dry-run" => 0,
                "publish-before-commit" | "publish-after-commit" =>
                    panic!("the commit fixture did not exit"),
                _ => 1,
            }
        );
        return;
    }
    let api = Discord::loopback_fixture(&cfg, port).unwrap();
    match phase.as_str() {
        "history-watch"
        | "history-watch-edit"
        | "history-watch-stop"
        | "history-watch-credential"
        | "history-watch-closed"
        | "history-watch-pages"
        | "history-watch-pages-revoked"
        | "history-watch-pages-closed"
        | "history-refuse" => {
            let result = observe(
                &place,
                &cfg,
                &api,
                Mode::Observe,
                RECHECK_INTERVAL,
                |_| -> io::Result<fn() -> io::Result<Option<Value>>> {
                    panic!("the running history witness cannot open Gateway")
                },
            );
            if phase == "history-refuse" {
                assert!(result.is_err())
            } else {
                result.unwrap()
            }
        }
        "gap-refuse" => {
            assert!(
                observe(
                    &place,
                    &cfg,
                    &api,
                    Mode::Capture,
                    RECHECK_INTERVAL,
                    |_| -> io::Result<fn() -> io::Result<Option<Value>>> {
                        panic!("unqualified gap opened Gateway")
                    }
                )
                .is_err()
            );
        }
        "gap-capture" => observe(&place, &cfg, &api, Mode::Capture, RECHECK_INTERVAL, |_| {
            Ok(|| {
                Ok(Some(json!({"t":"MESSAGE_CREATE","d":{
                    "id":"703","guild_id":"300","channel_id":"401","author":{"id":"201"}
                }})))
            })
        })
        .unwrap(),
        "gap-check" => {
            let (_pin, db) = open_bolt(&place.dir.join("discord.db"), 64 << 20).unwrap();
            let tx = db.begin().unwrap();
            let pending = capture::pending(&tx, &cfg).unwrap().unwrap();
            let (route, guard) = read_scope(&tx, &cfg, Some(&pending)).unwrap();
            prepare::verify_ledger(&tx, &cfg, &route, &guard).unwrap();
            let binding = capture::binding(&tx, &cfg, &route, &guard, Some(&pending)).unwrap();
            assert_eq!(binding["GapMissed"], 1);
            prepare::ready_discussion(&binding).unwrap();
        }
        "capture" => observe(&place, &cfg, &api, Mode::Capture, RECHECK_INTERVAL, |_| {
            Ok(|| {
                Ok(Some(json!({"t":"MESSAGE_CREATE","d":{
                    "id":"701","guild_id":"300","channel_id":"401","author":{"id":"201"}
                }})))
            })
        })
        .unwrap(),
        "prepare" => prepare_with(&place, &cfg, &api, "701", false).unwrap(),
        "native-bridge-hold" => {
            prepare_with(&place, &cfg, &api, "701", false).unwrap();
            let duplicate = BRIDGE_LOCK_DUPLICATE.with(|slot| slot.take()).unwrap();
            wait_at_test_boundary("holding duplicate descriptor after controller release").unwrap();
            duplicate.metadata().unwrap();
        }
        "prepare-and-exit" => {
            prepare_with(&place, &cfg, &api, "701", false).unwrap();
            std::process::exit(75);
        }
        "voice-storage" | "voice-storage-and-exit" | "voice-storage-refuse-alias" => {
            use bbolt_rs::BucketApi;
            let evidence: Value =
                serde_json::from_str(&std::env::var("SWARM_TEST_DISCORD_VOICE_EVIDENCE").unwrap())
                    .unwrap();
            legacy_state::verify_controls_place(&place, &cfg).unwrap();
            let _configuration = place.lock().unwrap();
            let _bridge = LockDescription(runtime::lock(&place.dir.join("bridge.lock")).unwrap());
            let source = place.dir.join("discord.db");
            let (_pin, db) = open_bolt(&source, 64 << 20).unwrap();
            let tx = db.begin().unwrap();
            let (route, guard) = legacy_discussions::observer_scope(&tx).unwrap();
            prepare::verify_ledger(&tx, &cfg, &route, &guard).unwrap();
            let origin = &evidence["origin"];
            let install = cfg.text("installation_id");
            let key = format!("{install}\0{}", origin["message_id"].as_str().unwrap()).into_bytes();
            let ingress = tx.bucket(b"teammate_ingress").unwrap();
            let accepted: Value = serde_json::from_slice(ingress.get(&key).unwrap()).unwrap();
            let a = &accepted["Admission"];
            assert_eq!(accepted["State"], "accepted");
            assert_eq!(a["OperationID"], origin["request_id"]);
            assert_eq!(a["Revision"], origin["revision"]);
            assert_eq!(a["Authority"], origin["kind"]);
            assert_eq!(a["InvokerID"], origin["requester_id"]);
            assert_eq!(a["Discussion"], evidence["binding"]);
            assert_eq!(a["Author"]["id"], origin["actor_id"]);
            assert_eq!(a["Route"]["guild_id"], route.guild_id);
            assert_eq!(a["Route"]["channel_id"], route.channel_id);
            let sequence = accepted["Sequence"].as_u64().unwrap();
            let pending_key = [install.as_bytes(), b"\0", &sequence.to_be_bytes()].concat();
            assert_eq!(
                tx.bucket(b"teammate_pending").unwrap().get(&pending_key),
                Some(key.as_slice())
            );
            api.verify_app(&cfg).unwrap();
            let current = capture::read_source(
                &api,
                &cfg,
                &route,
                &guard,
                &json!({"id":origin["message_id"],"author":{"id":origin["actor_id"]}}),
                true,
            )
            .unwrap();
            assert_eq!(Some(current.revision.as_str()), origin["revision"].as_str());
            assert_eq!(current.author["name"], origin["actor_name"]);
            let pending = capture::Pending {
                ingress_key: key,
                pending_key,
                sequence,
                candidate: current,
            };
            read_scope(&tx, &cfg, Some(&pending)).unwrap();
            let binding = capture::binding(&tx, &cfg, &route, &guard, Some(&pending)).unwrap();
            assert_eq!(
                serde_json::to_value(prepare::ready_discussion(&binding).unwrap()).unwrap(),
                evidence["binding"]
            );
            let stored = super::legacy_ingress::prepare::guild_storage_witness(
                &place,
                &cfg,
                &evidence["binding"],
                origin,
                phase == "voice-storage-and-exit",
            );
            if phase == "voice-storage-refuse-alias" {
                assert_eq!(
                    stored.unwrap_err().to_string(),
                    "unqualified fixture Voice copy"
                );
            } else {
                stored.unwrap();
            }
        }
        "dry-run" => prepare_with(&place, &cfg, &api, "701", true).unwrap(),
        "native-prepare-before" | "native-prepare-after" | "native-prepare-refuse" => {
            let result = continue_without_gateway(
                &place,
                &cfg,
                &api,
                "701",
                Mode::Prepare {
                    message: "701",
                    dry_run: false,
                    before_commit: if phase == "native-prepare-before" {
                        || std::process::exit(79)
                    } else {
                        || {}
                    },
                },
            );
            if phase == "native-prepare-refuse" {
                assert!(result.is_err());
            } else {
                result.unwrap();
                if phase == "native-prepare-after" {
                    std::process::exit(80);
                }
            }
        }
        "admit"
        | "admit-before"
        | "admit-after"
        | "admit-dry-run"
        | "admit-refuse"
        | "admit-delayed-stop"
        | "admit-delayed-edit"
        | "admit-stop-at-commit" => {
            if phase.starts_with("admit-delayed-") {
                assert_eq!(cfg.text("installation_id"), "fixture-teammate");
                let mut replies = 0;
                super::legacy_ingress::activation::MANAGER_RUNTIME_REPLY.with(|probe| {
                    *probe.borrow_mut() = Some(Box::new(move || {
                        replies += 1;
                        if replies == 2 {
                            wait_at_test_boundary("holding writer manager_runtime reply")?;
                        }
                        Ok(())
                    }));
                });
            }
            let result = continue_without_gateway(
                &place,
                &cfg,
                &api,
                "701",
                Mode::Admit {
                    message: "701",
                    dry_run: phase == "admit-dry-run",
                    before_commit: match phase.as_str() {
                        "admit-before" => || std::process::exit(77),
                        "admit-stop-at-commit" => {
                            || wait_at_test_boundary("holding staged Voice commit").unwrap()
                        }
                        _ => || {},
                    },
                },
            );
            match phase.as_str() {
                "admit-stop-at-commit" => assert_eq!(
                    result.unwrap_err().to_string(),
                    "original connector stopped before Voice commit"
                ),
                "admit-delayed-stop" => assert_eq!(
                    result.unwrap_err().to_string(),
                    "original teammate place stopped"
                ),
                "admit-delayed-edit" => assert_eq!(
                    result.unwrap_err().to_string(),
                    "retained teammate ingress lacks an exact open, active, repaired source"
                ),
                "admit-refuse" => assert!(result.is_err()),
                _ => result.unwrap(),
            }
            if phase == "admit-after" {
                std::process::exit(78);
            }
        }
        "refuse" => assert!(prepare_with(&place, &cfg, &api, "701", false).is_err()),
        "malformed-binding" | "malformed-binding-dry-run" => {
            let error =
                prepare_with(&place, &cfg, &api, "701", phase.ends_with("dry-run")).unwrap_err();
            assert!(error.to_string().contains("expected a string"), "{error}");
        }
        phase => panic!("unknown guild test phase {phase}"),
    }
}

#[cfg(test)]
fn publication_proof_fixture(place: &Place, cfg: &Config, raw: &[u8]) -> io::Result<()> {
    use std::{io::Write, os::unix::fs::OpenOptionsExt};
    let Ok(phase) = std::env::var("SWARM_TEST_DISCORD_GUILD_PHASE") else {
        return Ok(());
    };
    if !phase.starts_with("publish-") {
        return Ok(());
    }
    isolated_guild_fixture(place, cfg)?;
    std::fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .open(place.supervisor_root().join("rust-guild-publish-call"))?
        .write_all(raw)?;
    if phase != "publish-stage" {
        wait_at_test_boundary("holding supervisor-proven publication call")?;
    }
    Ok(())
}

#[cfg(test)]
fn publication_commit_fixture(place: &Place, cfg: &Config, before: bool) -> io::Result<()> {
    let phase = if before {
        "publish-before-commit"
    } else {
        "publish-after-commit"
    };
    if std::env::var("SWARM_TEST_DISCORD_GUILD_PHASE").as_deref() == Ok(phase) {
        isolated_guild_fixture(place, cfg)?;
        std::process::exit(if before { 87 } else { 88 });
    }
    Ok(())
}

#[cfg(test)]
fn history_proof_fixture(
    place: &Place,
    cfg: &Config,
    raw: &[u8],
    initial: bool,
) -> io::Result<bool> {
    use std::{io::Write, os::unix::fs::OpenOptionsExt};
    let Ok(phase) = std::env::var("SWARM_TEST_DISCORD_GUILD_PHASE") else {
        return Ok(false);
    };
    if !phase.starts_with("history-watch") {
        return Ok(false);
    }
    isolated_guild_fixture(place, cfg)?;
    let name = if initial {
        "rust-guild-history-call"
    } else {
        "rust-guild-history-next-call"
    };
    std::fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .open(place.supervisor_root().join(name))?
        .write_all(raw)?;
    if phase != "history-watch"
        && phase != "history-watch-pages"
        && !(initial && phase.starts_with("history-watch-pages-"))
    {
        wait_at_test_boundary("holding supervisor-proven history call")?;
    }
    Ok(phase == "history-watch-closed" || !initial && phase == "history-watch-pages-closed")
}

#[cfg(test)]
thread_local! {
    static BRIDGE_LOCK_DUPLICATE: std::cell::RefCell<Option<File>> = const { std::cell::RefCell::new(None) };
}

#[cfg(test)]
fn isolated_guild_fixture(place: &Place, cfg: &Config) -> io::Result<()> {
    use std::os::unix::fs::MetadataExt;
    let outer = std::env::var("SWARM_NATIVE_FIXTURE_HOST_NETNS")
        .ok()
        .and_then(|value| value.parse::<u64>().ok())
        .filter(|inode| *inode != 0)
        .ok_or_else(|| invalid("the native bridge fixture requires a private network namespace"))?;
    if std::fs::metadata("/proc/self/ns/net")?.ino() == outer
        || cfg.text("installation_id") != "fixture-teammate"
        || place
            .supervisor_root()
            .parent()
            .map(|root| root.join("connector"))
            .as_ref()
            != Some(&place.dir)
    {
        return Err(invalid("unqualified native bridge fixture"));
    }
    Ok(())
}

#[cfg(test)]
fn hold_prepared_bridge<'a>(
    place: &Place,
    cfg: &Config,
    manager: &prepare::Manager,
    guard_commit: impl FnOnce() -> io::Result<std::sync::MutexGuard<'a, ()>>,
) -> io::Result<()> {
    use std::io::Write;
    isolated_guild_fixture(place, cfg)?;
    let session = manager.binding["session_id"]
        .as_str()
        .and_then(|id| id.split_once('/'))
        .ok_or_else(|| invalid("original manager session is unavailable"))?
        .1;
    let bridge = manager.controller.listen()?;
    println!("holding the original prepared controller bridge");
    io::stdout().flush()?;
    // This witness has no admitted native turn and cannot authorize a tool call.
    let until = Instant::now() + Duration::from_secs(20);
    let mut wrong_session = 0;
    let mut original_session = 0;
    while wrong_session == 0 || original_session == 0 {
        if Instant::now() >= until {
            return Err(invalid(
                "the signed Go bridge client did not reach the fixture",
            ));
        }
        bridge.accept(|raw| {
            let req: Value = serde_json::from_slice(raw)?;
            assert_eq!(req["version"], 1);
            assert_eq!(req["conversation_id"], manager.binding["conversation_id"]);
            assert_eq!(req["call"]["name"], "swarm_discussion_history");
            if req["session_id"] == session {
                original_session += 1;
            } else {
                assert_eq!(req["session_id"], "fixture-other-session");
                wrong_session += 1;
            }
            Err(invalid(
                "prepared native requests have no observed manager call",
            ))
        })?;
        std::thread::sleep(Duration::from_millis(20));
    }
    assert_eq!((wrong_session, original_session), (1, 1));
    wait_at_test_boundary("withheld native input and original Go tool calls")?;
    assert_eq!(
        guard_commit().unwrap_err().to_string(),
        "original connector stopped before Voice commit"
    );
    drop(bridge);
    manager.verify_pins()?;
    let duplicate = manager.controller.duplicate_lock_for_test()?;
    BRIDGE_LOCK_DUPLICATE.with(|slot| *slot.borrow_mut() = Some(duplicate));
    Ok(())
}

#[cfg(test)]
fn wait_at_test_boundary(marker: &str) -> io::Result<()> {
    use std::io::Write;
    {
        let mut output = std::io::stdout().lock();
        writeln!(output, "{marker}")?;
        output.flush()?;
    }
    let mut signal = String::new();
    std::io::stdin().read_line(&mut signal)?;
    if signal != "continue\n" {
        return Err(invalid("fixture did not release the Voice admission"));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use bbolt_rs::{BucketApi, BucketRwApi, DbRwAPI, TxRwApi, TxRwRefApi};
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
        capture_case: Option<(&'static str, std::path::PathBuf)>,
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
            if let Some((fault, _)) = &capture_case {
                let fault = *fault;
                let authority = [
                    responses[1].clone(),
                    responses[2].clone(),
                    responses[3].clone(),
                ];
                let mut latest = responses[4].1.as_array().unwrap().clone();
                latest.insert(
                    0,
                    json!({"id":"503","channel_id":"401","author":{"id":"200","username":"fixture"},
                    "content":"one plain follow-up","type":0,"mention_everyone":false,"mentions":[],
                    "mention_roles":[],"attachments":[]}),
                );
                match fault {
                    "wrong source" => latest[0]["author"]["id"] = json!("201"),
                    "restart edited" => latest[0]["content"] = json!("changed before restart"),
                    "restart deleted" => {
                        latest.remove(0);
                    }
                    _ => {}
                }
                if fault.starts_with("restart ") {
                    responses.truncate(1);
                }
                responses.extend(authority.clone());
                responses.push(("/channels/401/messages?limit=20", json!(latest)));

                if !matches!(fault, "wrong source" | "restart deleted") {
                    responses.extend(authority);
                    if matches!(fault, "withdrawn" | "restart withdrawn") {
                        responses.last_mut().unwrap().1["permissions"] = json!("0");
                    }
                }
            }
            if let Some((fault, _)) = &capture_case
                && !fault.starts_with("restart ")
                && !matches!(*fault, "wrong source" | "withdrawn")
            {
                responses.extend([
                    responses[1].clone(),
                    responses[2].clone(),
                    responses[3].clone(),
                ]);
                if *fault == "late withdrawal" {
                    responses.last_mut().unwrap().1["permissions"] = json!("0");
                } else {
                    let mut latest = responses[8].1.as_array().unwrap().clone();
                    if *fault == "late deletion" {
                        latest.remove(0);
                    } else if *fault == "late edit" {
                        latest[0]["content"] = json!("changed after the first read");
                    }
                    responses.push(("/channels/401/messages?limit=20", json!(latest)));
                    if *fault != "late deletion" {
                        responses.extend([
                            responses[1].clone(),
                            responses[2].clone(),
                            responses[3].clone(),
                        ]);
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
                let late = capture_case
                    .as_ref()
                    .filter(|(fault, _)| fault.starts_with("late "));
                let delay = if index == 11
                    && let Some((_, path)) = late
                {
                    Some(open_bolt(path, 64 << 20).unwrap())
                } else {
                    None
                };
                let (mut stream, _) = rest.accept().unwrap();
                stream
                    .set_read_timeout(Some(Duration::from_secs(3)))
                    .unwrap();
                let mut input = BufReader::new(&mut stream);
                let mut line = String::new();
                input.read_line(&mut line).unwrap();
                assert_eq!(line, format!("GET /api/v10{path} HTTP/1.1\r\n"));
                if index == 12
                    && let Some((_, path)) = &capture_case
                {
                    assert!(
                        open_bolt(path, 64 << 20).is_err(),
                        "recheck must run under the Go writer"
                    );
                }
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
                if let Some(reader) = delay {
                    thread::sleep(Duration::from_millis(150));
                    drop(reader);
                }
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
        assert!(
            observe(
                &place,
                &cfg,
                &api,
                Mode::Observe,
                RECHECK_INTERVAL,
                unopened
            )
            .is_err()
        );
        drop(held);
        let writer = LockDescription(runtime::lock(&dir.join("discord.db")).unwrap());
        assert!(
            observe(
                &place,
                &cfg,
                &api,
                Mode::Observe,
                RECHECK_INTERVAL,
                unopened
            )
            .is_err()
        );
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
        let err = observe(&place, &cfg, &api, Mode::Observe, RECHECK_INTERVAL, |cfg| {
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
                observe(&place, &cfg, &api, Mode::Observe, Duration::ZERO, |_| {
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
        for fault in [
            "wrong source",
            "withdrawn",
            "late edit",
            "late deletion",
            "late withdrawal",
            "valid",
        ] {
            let rest = TcpListener::bind("127.0.0.1:0").unwrap();
            let api = Discord::loopback_fixture(&cfg, rest.local_addr().unwrap().port()).unwrap();
            let server = rest_fixture(rest, None, Some((fault, dir.join("discord.db"))));
            let result = observe(&place, &cfg, &api, Mode::Capture, RECHECK_INTERVAL, |_| {
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
        let saved = || {
            let (_pin, db) = open_bolt(&dir.join("discord.db"), 64 << 20).unwrap();
            let tx = db.begin().unwrap();
            let key = format!("{}\0{}", cfg.text("installation_id"), "503");
            let ingress = tx.bucket(b"teammate_ingress").unwrap();
            serde_json::from_slice::<Value>(ingress.get(key.as_bytes()).unwrap()).unwrap()
        };
        let captured = saved();
        assert!(captured["CaptureRevision"].as_str().is_some());
        let copy = root.join("copy");
        runtime::private_dir(&copy).unwrap();
        fs::copy(dir.join("config.json"), copy.join("config.json")).unwrap();
        let copied_source = copy.join("discord.db");
        fs::copy(dir.join("discord.db"), &copied_source).unwrap();
        let raw = captured.to_string();
        let prefix = raw.strip_suffix('}').unwrap();
        for (raw, field, go_value) in [
            (
                format!(r#"{prefix},"deleted":true}}"#),
                "Deleted",
                json!(true),
            ),
            (
                format!(r#"{prefix},"state":"sending"}}"#),
                "State",
                json!("sending"),
            ),
            (
                format!(r#"{{"Deleted":true,{}"#, &raw[1..]),
                "Deleted",
                json!(false),
            ),
        ] {
            {
                let (_, mut db) = legacy_state::bounded_writer(
                    legacy_state::writable_pin(&copied_source).unwrap(),
                )
                .unwrap();
                let mut tx = db.begin_rw().unwrap();
                let key = format!("{}\0{}", cfg.text("installation_id"), "503");
                tx.bucket_mut(b"teammate_ingress")
                    .unwrap()
                    .put(key, raw)
                    .unwrap();
                tx.commit().unwrap();
            }
            let opaque = fs::read(&copied_source).unwrap();
            let reopened: Value =
                serde_json::from_str(&go(&["ingress-state", copy.to_str().unwrap(), "existing"]))
                    .unwrap();
            assert_eq!(reopened["pending"][0][field], go_value);
            let (_pin, db) = open_bolt(&copied_source, 64 << 20).unwrap();
            assert!(capture::pending(&db.begin().unwrap(), &cfg).is_err());
            assert_eq!(fs::read(&copied_source).unwrap(), opaque);
        }
        for trial in [
            "restart valid",
            "restart edited",
            "restart deleted",
            "restart withdrawn",
        ] {
            let rest = TcpListener::bind("127.0.0.1:0").unwrap();
            let api = Discord::loopback_fixture(&cfg, rest.local_addr().unwrap().port()).unwrap();
            let server = rest_fixture(rest, None, Some((trial, dir.join("discord.db"))));
            let result = observe(
                &place,
                &cfg,
                &api,
                Mode::Capture,
                RECHECK_INTERVAL,
                unopened,
            );
            assert_eq!(result.is_ok(), trial == "restart valid", "{result:?}");
            server.join().unwrap();
            assert_eq!(read_original(), preserved);
            go(&["bridge-lease", p]);
        }
        assert_eq!(go(&["ingress", p, "existing", "501", "replay"]).trim(), "1");
        assert_eq!(go(&["ingress", p, "existing", "503", "replay"]).trim(), "1");
        assert_eq!(saved(), captured);
        assert_eq!(go(&["ingress", p, "existing", "503", "edit"]).trim(), "1");
        let rewritten = saved();
        assert_eq!(rewritten["Sequence"], captured["Sequence"]);
        assert_eq!(rewritten["Mutation"], 1);
        assert!(rewritten["CaptureRevision"].is_null());
        assert!(
            observe(
                &place,
                &cfg,
                &api,
                Mode::Capture,
                RECHECK_INTERVAL,
                unopened
            )
            .is_err()
        );
        assert_eq!(go(&["ingress", p, "existing", "503", "settle"]).trim(), "1");
        assert_eq!(go(&["ingress", p, "existing", "503", "replay"]).trim(), "0");
        assert_eq!(go(&["ingress", p, "existing", "505", "replay"]).trim(), "1");
        assert!(
            observe(
                &place,
                &cfg,
                &api,
                Mode::Capture,
                RECHECK_INTERVAL,
                unopened
            )
            .is_err()
        );
        assert_eq!(go(&["traffic-read", p, "sent"]).trim(), "sent");
        fs::remove_dir_all(root).unwrap();
    }
}
