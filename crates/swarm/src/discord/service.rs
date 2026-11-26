//! Discord connector. Personal calls and guild worker admissions each own their Gateway event order.
use super::config::{Config, Place, invalid, snowflake};
use super::{
    gateway,
    receipt::{Inbox, Observation},
    rest::Discord,
};
use crate::socket::DeadlineReader;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::fs;
use std::io::{self, BufRead, BufReader, Read, Write};
use std::os::unix::fs::MetadataExt;
use std::os::unix::net::{UnixListener, UnixStream};
use std::path::PathBuf;
use std::time::{Duration, Instant};

const STOP_WAIT: Duration = Duration::from_secs(2);

pub(super) fn run(place: &Place, cfg: &Config) -> io::Result<()> {
    let personal = cfg.text("mode") == "personal";
    if !personal && !super::guild::enabled(cfg) {
        return Err(io::Error::other(
            "the Rust foreground connector requires an owner DM or enrolled guild with a configured owner",
        ));
    }
    let (_lease, _app_lease, listener) = place.claim(cfg)?;
    let bridge = Bridge::new(place.dir.join("bridge.sock"), listener)?;
    let inbox = Inbox::open(&place.dir, cfg)?;
    let api = Discord::connect(cfg)?;
    api.verify_app(cfg)?;
    if personal {
        run_personal(place, cfg, bridge, inbox, api)
    } else {
        run_guild(place, cfg, bridge, inbox, api)
    }
}

fn run_personal(
    place: &Place,
    cfg: &Config,
    bridge: Bridge,
    mut inbox: Inbox,
    api: Discord,
) -> io::Result<()> {
    api.verify_dm(cfg, None)?;
    if inbox.cursor().is_none() {
        let baseline = api.owner_history(cfg, "", 1)?;
        let baseline = baseline
            .first()
            .and_then(|message| message["id"].as_str())
            .unwrap_or("");
        inbox.checkpoint(baseline)?;
    }
    let dispatcher = super::dispatcher::Dispatcher::open(cfg, &place.dir)?;
    let (gateway_socket, cursor) = gateway::open(cfg)?;
    repair(&mut inbox, |before| api.owner_history(cfg, before, 100))?;
    // Serialize binding the call control socket with normal disconnect.
    let call_start = cfg.0["calls"].is_object().then(|| place.lock()).transpose();
    let scope = match &call_start {
        Ok(_) => dispatcher.as_ref().map(|dispatcher| dispatcher.scope(cfg)),
        Err(_) => None,
    };
    let driver =
        super::call_runtime::Driver::start(gateway_socket, cursor, cfg, &place.dir, scope.clone());
    drop(call_start);
    driver.discussion_ready.store(
        !inbox.manager()["binding"].is_null(),
        std::sync::atomic::Ordering::Release,
    );
    if dispatcher.is_none() {
        eprintln!(
            "discord: personal Gateway connected. Verified requests are queued locally pending native dispatcher support."
        );
    }
    let mut worker_read_after = None;
    let mut worker_send = None;
    let mut next = Instant::now();
    loop {
        if bridge.serve(
            cfg,
            &mut inbox,
            driver.sequence.load(std::sync::atomic::Ordering::Acquire),
            dispatcher.is_some(),
            driver.status(),
        )? {
            return Ok(());
        }
        let wait = next
            .saturating_duration_since(Instant::now())
            .min(Duration::from_millis(200));
        match driver.inbound.recv_timeout(wait) {
            Ok(Ok(super::call_runtime::Ingress::Discord(event))) => {
                ingest(cfg, &mut inbox, &event, |id| api.owner_message(cfg, id))?;
                // Drain the ordered Gateway feed before scheduling native work.
                continue;
            }
            Ok(Ok(super::call_runtime::Ingress::Transcript(origin, text))) => {
                if let Some(scope) = &scope
                    && !inbox.manager()["binding"].is_null()
                    && super::call_runtime::verify_live(&place.dir, &origin).is_ok()
                {
                    inbox.admit_voice(scope, origin, &text)?;
                }
                continue;
            }
            Ok(Err(err)) => return Err(err),
            Err(std::sync::mpsc::RecvTimeoutError::Disconnected) => {
                return Err(invalid("Discord Gateway owner exited"));
            }
            Err(std::sync::mpsc::RecvTimeoutError::Timeout) => {}
        }
        if Instant::now() >= next {
            super::worker::tick(
                cfg,
                &mut inbox,
                &api,
                &mut worker_read_after,
                &mut worker_send,
            )?;
            if let Some(dispatcher) = &dispatcher {
                dispatcher.tick_calls(cfg, &mut inbox, &api, Some((&place.dir, &driver)))?;
                driver.discussion_ready.store(
                    !inbox.manager()["binding"].is_null(),
                    std::sync::atomic::Ordering::Release,
                );
            }
            next = Instant::now()
                + if cfg.0["calls"].is_object() {
                    Duration::from_millis(200)
                } else {
                    Duration::from_secs(1)
                };
        }
    }
}

struct LinkedDiscussion<'a> {
    cfg: Config,
    thread: super::team_thread::ThreadAudience<'a>,
    inbox: Inbox,
    dispatcher: Option<super::dispatcher::Dispatcher>,
    unhealthy: bool,
}

impl<'a> LinkedDiscussion<'a> {
    fn open(
        place: &Place,
        route: super::team_thread::Route,
        cfg: Config,
        api: &'a Discord,
    ) -> io::Result<Self> {
        let directory = route.directory(&place.dir.canonicalize()?)?;
        crate::supervisor::runtime::private_dir(&directory)?;
        let mut inbox = Inbox::open(&directory, &cfg)?;
        if inbox.cursor().is_none() {
            inbox.checkpoint("")?;
        }
        let dispatcher = super::dispatcher::Dispatcher::open(&cfg, &directory)?;
        Ok(Self {
            cfg,
            thread: super::team_thread::ThreadAudience { route, api },
            inbox,
            dispatcher,
            unhealthy: false,
        })
    }

    fn scope(&self, place: &Place) -> io::Result<Option<Value>> {
        let Some(dispatcher) = &self.dispatcher else {
            return Ok(None);
        };
        let mut scope = dispatcher.scope(&self.cfg);
        scope["call_place"] = json!(place.dir.canonicalize()?);
        Ok(Some(scope))
    }

    fn tick(&mut self, place: &Place, driver: &super::call_runtime::Driver) {
        if let Some(dispatcher) = &self.dispatcher {
            let before = self.unhealthy;
            self.unhealthy = dispatcher
                .tick_calls(
                    &self.cfg,
                    &mut self.inbox,
                    &self.thread,
                    Some((&place.dir, driver)),
                )
                .is_err();
            if self.unhealthy && !before {
                eprintln!(
                    "discord: linked discussion {} is waiting for native reconciliation",
                    self.thread.route.thread
                );
            }
        }
    }
}

fn call_ready(inbox: &Inbox, additional: &[LinkedDiscussion<'_>], scope: Option<&Value>) -> bool {
    let Some(scope) = scope else {
        return false;
    };
    if scope["call_place"].is_null() {
        return !inbox.manager()["binding"].is_null();
    }
    additional.iter().any(|slot| {
        scope["guild_id"] == slot.thread.route.guild
            && scope["channel_id"] == slot.thread.route.thread
            && !slot.unhealthy
            && !slot.inbox.manager()["binding"].is_null()
    })
}

fn run_guild(
    place: &Place,
    cfg: &Config,
    bridge: Bridge,
    mut inbox: Inbox,
    api: Discord,
) -> io::Result<()> {
    if inbox.cursor().is_none() {
        inbox.checkpoint("")?;
    }
    use super::dispatcher::Audience;
    if cfg.0["team_discussion"].is_null()
        && let Some(route) = super::thread_creation::recover(cfg, &inbox, &api)?
    {
        let next = place.link_automatic(cfg, route)?;
        drop(inbox);
        let inbox = Inbox::open(&place.dir, &next)?;
        return run_guild(place, &next, bridge, inbox, api);
    }
    let _guild_authority = {
        let verifier = Discord::connect(cfg)?;
        super::worker_proof::Service::bind(
            &place.dir,
            place.supervisor_root().to_owned(),
            cfg.clone(),
            move |cfg, guild, channel, id| {
                super::guild::current(&verifier, cfg, guild, channel, id)
            },
        )?
    };
    let mut worker_read_after = None;
    let mut worker_send: Option<super::worker::Flight> = None;
    let mut guild_events = std::collections::VecDeque::new();
    let thread = super::team_thread::Route::configured(cfg)?
        .map(|route| super::team_thread::ThreadAudience { route, api: &api });
    if let Some(thread) = &thread
        && thread.route.starter.is_some()
        && inbox.manager().is_null()
        && !inbox.has_team_source(&thread.route.thread)
        && let Some(source) = thread.current(cfg, &thread.route.thread)?
        && super::team_thread::explicit(cfg, &source)
    {
        thread.admit(&mut inbox, &source)?;
    }
    let dispatcher = if thread.is_some() {
        super::dispatcher::Dispatcher::open(cfg, &place.dir)?
    } else {
        None
    };
    let mut additional = super::team_thread::Route::additional(cfg)?
        .into_iter()
        .map(|(route, cfg)| LinkedDiscussion::open(place, route, cfg, &api))
        .collect::<io::Result<Vec<_>>>()?;
    let scope = if let Some(selected) = additional
        .iter()
        .find(|slot| slot.thread.route.accepts_voice(cfg))
    {
        selected.scope(place)?
    } else {
        dispatcher.as_ref().map(|dispatcher| dispatcher.scope(cfg))
    };
    let mut driver = guild_gateway(
        place,
        cfg,
        &mut inbox,
        thread.as_ref(),
        &mut additional,
        scope.clone(),
    )?;
    use super::call_runtime::Ingress;
    use std::sync::atomic::Ordering;
    loop {
        if worker_send
            .as_ref()
            .is_some_and(|send| send.join.is_finished())
        {
            let _ = worker_send.take().unwrap().join.join();
        }
        if worker_send.is_none() {
            while let Some(event) = guild_events.pop_front() {
                ingest_guild(
                    cfg,
                    &mut inbox,
                    &api,
                    thread.as_ref(),
                    &mut additional,
                    &event,
                )?;
            }
        }
        if bridge.serve(
            cfg,
            &mut inbox,
            driver.sequence.load(Ordering::Acquire),
            dispatcher.is_some() || additional.iter().any(|slot| slot.dispatcher.is_some()),
            driver.status(),
        )? {
            return Ok(());
        }
        super::worker::tick(
            cfg,
            &mut inbox,
            &api,
            &mut worker_read_after,
            &mut worker_send,
        )?;
        if worker_send.is_none() {
            if let (Some(dispatcher), Some(thread)) = (&dispatcher, &thread) {
                dispatcher.tick_calls(cfg, &mut inbox, thread, Some((&place.dir, &driver)))?;
            }
            for slot in &mut additional {
                slot.tick(place, &driver);
            }
            driver.discussion_ready.store(
                call_ready(&inbox, &additional, scope.as_ref()),
                Ordering::Release,
            );
        }
        if thread.is_none()
            && worker_send.is_none()
            && let Some(route) = super::thread_creation::retry_due(cfg, &mut inbox, &api)?
        {
            let next = place.link_automatic(cfg, route)?;
            drop(driver);
            drop(_guild_authority);
            drop(inbox);
            let inbox = Inbox::open(&place.dir, &next)?;
            return run_guild(place, &next, bridge, inbox, api);
        }
        let event = match driver.inbound.recv_timeout(Duration::from_millis(200)) {
            Ok(Ok(Ingress::Discord(event))) => event,
            Ok(Ok(Ingress::Transcript(origin, text))) => {
                if let Some(scope) = &scope
                    && call_ready(&inbox, &additional, Some(scope))
                    && super::call_runtime::verify_live(&place.dir, &origin).is_ok()
                {
                    if scope["call_place"].is_null() {
                        inbox.admit_voice(scope, origin, &text)?;
                    } else if let Some(slot) = additional.iter_mut().find(|slot| {
                        origin["guild_id"] == slot.thread.route.guild
                            && origin["channel_id"] == slot.thread.route.thread
                    }) {
                        slot.inbox.admit_voice(scope, origin, &text)?;
                    }
                }
                continue;
            }
            Err(std::sync::mpsc::RecvTimeoutError::Timeout) => continue,
            Ok(Err(_)) | Err(std::sync::mpsc::RecvTimeoutError::Disconnected) => {
                // Retire the only Gateway owner before reconnecting. A disputed
                // native effect remains in its original ledger entry.
                drop(driver);
                if let Some(flight) = worker_send.take() {
                    super::worker_proof::invalidate(&place.dir, Some(&flight.operation))?;
                    let _ = flight.join.join();
                }
                inbox.mark_team_gap()?;
                for slot in &mut additional {
                    slot.inbox.mark_team_gap()?;
                }
                guild_events.clear();
                driver = loop {
                    if bridge.serve(cfg, &mut inbox, 0, false, "unavailable")? {
                        return Ok(());
                    }
                    match guild_gateway(
                        place,
                        cfg,
                        &mut inbox,
                        thread.as_ref(),
                        &mut additional,
                        scope.clone(),
                    ) {
                        Ok(connected) => break connected,
                        Err(_) => {
                            eprintln!(
                                "discord: guild repair is waiting for a scoped Gateway and thread readback"
                            );
                            for _ in 0..10 {
                                if bridge.serve(cfg, &mut inbox, 0, false, "unavailable")? {
                                    return Ok(());
                                }
                                std::thread::sleep(Duration::from_millis(100));
                            }
                        }
                    }
                };
                continue;
            }
        };
        if thread.is_none()
            && worker_send.is_none()
            && let Some(route) = super::thread_creation::ingest(cfg, &mut inbox, &api, &event)?
        {
            let next = place.link_automatic(cfg, route)?;
            drop(driver);
            drop(_guild_authority);
            drop(inbox);
            let inbox = Inbox::open(&place.dir, &next)?;
            return run_guild(place, &next, bridge, inbox, api);
        }
        if let Some(flight) = &worker_send {
            use super::worker_proof::Invalidation;
            match super::worker_proof::invalidates(
                cfg,
                inbox.worker_source(&flight.operation),
                &event,
            ) {
                Invalidation::None => {}
                Invalidation::Refresh => super::worker_proof::invalidate(&place.dir, None)?,
                Invalidation::Cancel => {
                    super::worker_proof::invalidate(&place.dir, Some(&flight.operation))?
                }
            }
        }
        if worker_send.is_some() {
            if !super::guild::needs_ingest(cfg, &inbox, &event)
                && !thread
                    .as_ref()
                    .is_some_and(|thread| thread.route.matches(&event["d"]))
                && !additional
                    .iter()
                    .any(|slot| slot.thread.route.matches(&event["d"]))
            {
                continue;
            }
            if guild_events.len() >= 128 {
                return Err(invalid(
                    "guild event backlog exceeded its safe bound during a worker effect",
                ));
            }
            guild_events.push_back(event);
        } else {
            ingest_guild(
                cfg,
                &mut inbox,
                &api,
                thread.as_ref(),
                &mut additional,
                &event,
            )?;
        }
    }
}

fn guild_gateway(
    place: &Place,
    cfg: &Config,
    inbox: &mut Inbox,
    thread: Option<&super::team_thread::ThreadAudience<'_>>,
    additional: &mut [LinkedDiscussion<'_>],
    scope: Option<Value>,
) -> io::Result<super::call_runtime::Driver> {
    let (socket, cursor) = gateway::open(cfg)?;
    if let Some(thread) = thread {
        super::team_history::repair(cfg, inbox, thread)?;
    }
    for slot in additional.iter_mut() {
        super::team_history::repair(&slot.cfg, &mut slot.inbox, &slot.thread)?;
    }
    let ready = call_ready(inbox, additional, scope.as_ref());
    let call_start = cfg.0["calls"].is_object().then(|| place.lock()).transpose();
    let scope = if call_start.is_ok() { scope } else { None };
    let driver = super::call_runtime::Driver::start(socket, cursor, cfg, &place.dir, scope);
    drop(call_start);
    driver
        .discussion_ready
        .store(ready, std::sync::atomic::Ordering::Release);
    Ok(driver)
}

fn ingest_guild(
    cfg: &Config,
    inbox: &mut Inbox,
    api: &Discord,
    thread: Option<&super::team_thread::ThreadAudience<'_>>,
    additional: &mut [LinkedDiscussion<'_>],
    event: &Value,
) -> io::Result<()> {
    if let Some(slot) = additional
        .iter_mut()
        .find(|slot| slot.thread.route.matches(&event["d"]))
        && super::team_thread::ingest(&slot.cfg, &mut slot.inbox, &slot.thread, event)?
    {
        checkpoint_linked(&mut slot.inbox, event)?;
        return Ok(());
    }
    let handled = match thread {
        Some(thread) => super::team_thread::ingest(cfg, inbox, thread, event)?,
        None => false,
    };
    if !handled {
        super::guild::ingest(
            cfg,
            inbox,
            event,
            crate::ipc::spawn::now_ms(),
            |guild, channel, id| super::guild::current(api, cfg, guild, channel, id),
        )?;
    }
    if thread.is_some_and(|thread| thread.route.matches(&event["d"])) {
        checkpoint_linked(inbox, event)?;
    }
    Ok(())
}

fn checkpoint_linked(inbox: &mut Inbox, event: &Value) -> io::Result<()> {
    if event["t"] == "MESSAGE_CREATE"
        && let Some(id) = event["d"]["id"].as_str().filter(|id| snowflake(id))
    {
        inbox.checkpoint_team(id)?;
    }
    Ok(())
}

fn repair(
    inbox: &mut Inbox,
    mut page: impl FnMut(&str) -> io::Result<Vec<Value>>,
) -> io::Result<()> {
    let cursor = inbox
        .cursor()
        .ok_or_else(|| invalid("personal history consent boundary is unavailable"))?
        .parse::<u64>()
        .unwrap_or_default();
    let pending: std::collections::BTreeSet<_> = inbox.pending_ids().into_iter().collect();
    let lower = pending.first().copied().unwrap_or(cursor).min(cursor);
    let mut before = String::new();
    let mut snapshots = std::collections::BTreeMap::new();
    for _ in 0..4 {
        let rows = page(&before)?;
        let mut lowest = u64::MAX;
        for row in &rows {
            let id = row["id"]
                .as_str()
                .filter(|id| snowflake(id))
                .and_then(|id| id.parse::<u64>().ok())
                .ok_or_else(|| invalid("personal history message has no valid identity"))?;
            if !before.is_empty() && id >= before.parse::<u64>().unwrap_or_default() {
                return Err(invalid("personal history pagination did not progress"));
            }
            lowest = lowest.min(id);
            if snapshots.insert(id, row.clone()).is_some() {
                return Err(invalid(
                    "personal history contains duplicate message identities",
                ));
            }
        }
        if rows.len() < 100 || lowest <= lower {
            let newest = snapshots.last_key_value().map(|(id, _)| id.to_string());
            for (&id, row) in &snapshots {
                inbox.observe(row, Observation::History)?;
                if inbox.admit(row)?.is_none() && pending.contains(&id) {
                    inbox.withdraw(&id.to_string())?;
                }
            }
            for id in pending.iter().filter(|id| !snapshots.contains_key(id)) {
                inbox.withdraw(&id.to_string())?;
            }
            if let Some(newest) = newest {
                inbox.checkpoint(&newest)?;
            }
            return Ok(());
        }
        before = lowest.to_string();
    }
    // shortcut: bounded startup history, lift after paginated repair and native FIFO reconcile.
    Err(io::Error::other(
        "personal history repair exceeded its startup page limit",
    ))
}

fn ingest(
    cfg: &Config,
    inbox: &mut Inbox,
    event: &Value,
    current: impl FnOnce(&str) -> io::Result<Option<Value>>,
) -> io::Result<()> {
    let message = &event["d"];
    let channel = cfg.text("dm_channel_id");
    if message["channel_id"] != channel || !message["guild_id"].is_null() {
        return Ok(());
    }
    if event["t"] == "MESSAGE_DELETE_BULK" {
        if let Some(ids) = message["ids"].as_array() {
            for id in ids
                .iter()
                .filter_map(Value::as_str)
                .filter(|id| snowflake(id))
            {
                inbox.withdraw(id)?;
            }
        }
        return Ok(());
    }
    let Some(id) = message["id"].as_str().filter(|id| snowflake(id)) else {
        return Ok(());
    };
    if !inbox.within_consent(id) {
        return Ok(());
    }
    match event["t"].as_str() {
        Some("MESSAGE_CREATE") if message["author"]["id"] == cfg.text("application_id") => {
            inbox.observe(message, Observation::Gateway)?;
            inbox.checkpoint(id)?;
        }
        Some("MESSAGE_CREATE") if message["author"]["id"] == cfg.text("owner_id") => {
            if let Some(source) = current(id)? {
                if source["id"] != id {
                    return Err(invalid(
                        "Discord message recheck returned a different source ID",
                    ));
                }
                inbox.admit(&source)?;
            }
            inbox.checkpoint(id)?;
        }
        Some("MESSAGE_UPDATE") => {
            if let Some(source) = current(id)? {
                if source["id"] != id {
                    return Err(invalid(
                        "Discord message recheck returned a different source ID",
                    ));
                }
                if inbox.admit(&source)?.is_none() {
                    inbox.withdraw(id)?;
                }
            } else {
                inbox.withdraw(id)?;
            }
        }
        Some("MESSAGE_DELETE") => inbox.withdraw(id)?,
        _ => {}
    }
    Ok(())
}

struct Bridge {
    path: PathBuf,
    listener: UnixListener,
    identity: (u64, u64),
}

impl Bridge {
    fn new(path: PathBuf, listener: UnixListener) -> io::Result<Self> {
        let meta = fs::symlink_metadata(&path)?;
        let this = Self {
            path,
            listener,
            identity: (meta.dev(), meta.ino()),
        };
        this.listener.set_nonblocking(true)?;
        Ok(this)
    }

    fn serve(
        &self,
        cfg: &Config,
        inbox: &mut Inbox,
        sequence: u64,
        native: bool,
        calls: &str,
    ) -> io::Result<bool> {
        let (mut client, _) = match self.listener.accept() {
            Ok(pair) => pair,
            Err(err) if err.kind() == io::ErrorKind::WouldBlock => return Ok(false),
            Err(err) => return Err(err),
        };
        match request(&mut client, cfg, inbox, sequence, native, calls) {
            Ok(stop) => Ok(stop),
            Err(_) => Ok(false), // A bad local request must not stop Gateway.
        }
    }
}

impl Drop for Bridge {
    fn drop(&mut self) {
        if fs::symlink_metadata(&self.path)
            .is_ok_and(|meta| (meta.dev(), meta.ino()) == self.identity)
        {
            let _ = fs::remove_file(&self.path);
        }
    }
}

fn request(
    stream: &mut UnixStream,
    cfg: &Config,
    inbox: &mut Inbox,
    sequence: u64,
    native: bool,
    calls: &str,
) -> io::Result<bool> {
    stream.set_write_timeout(Some(STOP_WAIT))?;
    let stop = {
        let mut reader =
            BufReader::new(DeadlineReader(stream, Instant::now() + STOP_WAIT).take(8193));
        let mut first = String::new();
        reader.read_line(&mut first)?;
        let mut length = 0usize;
        loop {
            let mut header = String::new();
            if reader.read_line(&mut header)? == 0 {
                return Err(invalid("incomplete local headers"));
            }
            if header == "\r\n" {
                break;
            }
            if let Some((name, value)) = header.split_once(':') {
                if name.eq_ignore_ascii_case("Content-Length") {
                    length = value
                        .trim()
                        .parse()
                        .map_err(|_| invalid("invalid local content length"))?;
                }
                if name.eq_ignore_ascii_case("Transfer-Encoding") {
                    return Err(invalid("invalid local transfer encoding"));
                }
            }
        }
        if length > 128 {
            return Err(invalid("local request body too large"));
        }
        let mut body = vec![0; length];
        reader.read_exact(&mut body)?;
        let mut parts = first.split_whitespace();
        let Some(method) = parts.next() else {
            return Err(invalid("missing local method"));
        };
        let Some(path) = parts.next() else {
            return Err(invalid("missing local path"));
        };
        if !matches!(parts.next(), Some("HTTP/1.0" | "HTTP/1.1")) || parts.next().is_some() {
            return Err(invalid("invalid local HTTP request"));
        }
        let identity = Sha256::digest(cfg.text("installation_id").as_bytes());
        let stop_path = format!("/installation-v1/{identity:x}/shutdown");
        let stop = method == "POST" && path == stop_path && body == b"{}";
        if method == "GET" && path == "/status" && length == 0 {
            let mut status = cfg.status();
            status["connected"] = json!(true);
            if cfg.0["calls"].is_object() {
                status["calls"] = json!(calls);
            }
            status["teammate"] = json!({
                "pending": inbox.pending_count(),
                "gateway_sequence": sequence,
                "gap_epoch": inbox.team_gap_epoch(),
                "ingress_issue": if inbox.team_gap_epoch() > 0 {
                    "guild mentions outside the linked thread may have been missed during a Gateway gap"
                } else if native { "" } else { "native dispatcher reconciliation is unavailable" }
            });
            status["connector"] = json!({
                "state": if native { "scoped_dispatcher" } else { "receipt_only" },
                "pending_admissions": inbox.pending_count(),
                "worker_deliveries":inbox.worker_status()
            });
            respond(stream, "200 OK", status)?;
        } else if method == "POST"
            && [
                format!("/installation-v1/{identity:x}/exclude"),
                format!("/installation-v1/{identity:x}/include"),
            ]
            .iter()
            .any(|route| route == path)
        {
            let target: Value = serde_json::from_slice(&body)?;
            let target = target["target_id"]
                .as_str()
                .ok_or_else(|| invalid("missing worker session ID"))?;
            inbox.set_worker_excluded(target, path.ends_with("/exclude"))?;
            let mut status = cfg.status();
            status["connected"] = json!(true);
            status["connector"] = json!({"worker_deliveries":inbox.worker_status()});
            respond(stream, "200 OK", status)?;
        } else if stop {
            respond(stream, "200 OK", json!({"stopped":true}))?;
        } else if method == "POST"
            && path.starts_with("/installation-v1/")
            && path.ends_with("/shutdown")
        {
            respond(
                stream,
                "403 Forbidden",
                json!({"error":"discord: installation does not match or shutdown body is invalid"}),
            )?;
        } else {
            respond(
                stream,
                "405 Method Not Allowed",
                json!({"error":"discord: operation is unavailable in this connector"}),
            )?;
        }
        stop
    };
    Ok(stop)
}

fn respond(stream: &mut UnixStream, status: &str, body: Value) -> io::Result<()> {
    let bytes = serde_json::to_vec(&body)?;
    write!(
        stream,
        "HTTP/1.0 {status}\r\nContent-Type: application/json\r\nContent-Length: {}\r\n\r\n",
        bytes.len()
    )?;
    stream.write_all(&bytes)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::supervisor::runtime;
    use std::path::Path;

    fn fixture_dir() -> PathBuf {
        let root = Path::new("/tmp").join(format!("swarm-dm-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&root).unwrap();
        root
    }

    fn config() -> Config {
        Config(json!({
            "version":1, "name":"fixture", "enabled":true, "mode":"personal",
            "installation_id":"issued", "application_id":"200", "owner_id":"300",
            "dm_channel_id":"400", "daemon_socket":"/fixture/supervisor",
        }))
    }

    fn owner_source(id: &str, content: &str) -> Value {
        json!({"id":id, "channel_id":"400", "content":content, "type":0,
               "attachments":[], "author":{"id":"300"}})
    }

    #[test]
    fn history_checkpoint_waits_for_an_inflight_native_effect() {
        let dir = fixture_dir();
        let mut inbox = Inbox::open(&dir, &config()).unwrap();
        let held = crate::discord::worker_input::lock_ledger(&dir).unwrap();
        let (started, start) = std::sync::mpsc::channel();
        let (complete, result) = std::sync::mpsc::channel();
        let writer = std::thread::spawn(move || {
            started.send(()).unwrap();
            let saved = inbox.checkpoint("1000");
            complete.send(saved).unwrap();
        });
        start.recv().unwrap();
        assert!(matches!(
            result.recv_timeout(std::time::Duration::from_millis(100)),
            Err(std::sync::mpsc::RecvTimeoutError::Timeout)
        ));
        drop(held);
        result
            .recv_timeout(std::time::Duration::from_secs(3))
            .unwrap()
            .unwrap();
        writer.join().unwrap();
        assert_eq!(Inbox::open(&dir, &config()).unwrap().cursor(), Some("1000"));
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn dm_admission_repair_dedup_deletion_and_late_exact_receipt_survive_restart() {
        let dir = fixture_dir();
        let cfg = config();
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        inbox.checkpoint("1000").unwrap();
        let live = owner_source("1002", "current");
        let hint = |message: &Value| json!({"t":"MESSAGE_CREATE", "d":message});
        let mut foreign = live.clone();
        foreign["channel_id"] = json!("401");
        ingest(&cfg, &mut inbox, &hint(&foreign), |_| {
            panic!("outside-DM event reached REST")
        })
        .unwrap();
        ingest(&cfg, &mut inbox, &hint(&live), |_| Ok(Some(live.clone()))).unwrap();
        ingest(&cfg, &mut inbox, &hint(&live), |_| Ok(Some(live.clone()))).unwrap();
        assert_eq!(inbox.pending_count(), 1);
        assert_eq!(inbox.cursor(), Some("1002"));
        let pre_enrollment = owner_source("999", "historical edit");
        ingest(
            &cfg,
            &mut inbox,
            &json!({"t":"MESSAGE_UPDATE","d":pre_enrollment}),
            |_| panic!("historical edit reached REST"),
        )
        .unwrap();
        let mut conflict = live.clone();
        conflict["content"] = json!("unverified rewrite");
        assert!(ingest(&cfg, &mut inbox, &hint(&live), |_| Ok(Some(conflict))).is_err());
        let removed = owner_source("1004", "deleted while disconnected");
        ingest(&cfg, &mut inbox, &hint(&removed), |_| {
            Ok(Some(removed.clone()))
        })
        .unwrap();
        assert_eq!(inbox.cursor(), Some("1004"));
        drop(inbox);

        let file = dir.join("rust-text-v1.json");
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        let (nonce, proof) = super::super::receipt::fixture_publication("confirmed text");
        inbox
            .prepare_publication(&nonce, proof, Value::Null)
            .unwrap();
        inbox.publication_state(&nonce, "sending", None).unwrap();
        drop(inbox);
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        let old = owner_source("999", "prior to selected boundary");
        let gap = owner_source("1001", "missing while disconnected");
        let mut edited = live.clone();
        edited["content"] = json!("edited while disconnected");
        edited["edited_timestamp"] = json!("2026-11-25T09:10:00Z");
        let confirmation = json!({"id":"1005", "author":{"id":"200","bot":true},
            "nonce":nonce, "channel_id":"400", "content":"confirmed text"});
        let mut wrong = confirmation.clone();
        wrong["channel_id"] = json!("401");
        inbox.observe(&wrong, Observation::Gateway).unwrap();
        assert_eq!(
            runtime::read_json(&file).unwrap()["outbox"][&nonce]["state"],
            "sending"
        );
        repair(&mut inbox, |_| {
            Ok(vec![
                confirmation.clone(),
                edited.clone(),
                gap.clone(),
                old.clone(),
            ])
        })
        .unwrap();
        assert_eq!(inbox.cursor(), Some("1005"));
        assert_eq!(inbox.pending_count(), 2);
        let state = runtime::read_json(&file).unwrap();
        assert_eq!(
            state["messages"]["1001"]["text"],
            "missing while disconnected"
        );
        assert_eq!(state["outbox"][&nonce]["message_id"], "1005");
        assert_eq!(state["outbox"][&nonce]["history_seen"], true);
        assert_eq!(
            state["messages"]["1002"]["text"],
            "edited while disconnected"
        );
        assert_eq!(state["messages"]["1004"]["state"], "withdrawn");
        assert!(state["messages"]["999"].is_null());
        ingest(
            &cfg,
            &mut inbox,
            &json!({"t":"MESSAGE_DELETE","d":{"id":"1002","channel_id":"400"}}),
            |_| panic!(),
        )
        .unwrap();
        assert_eq!(inbox.pending_count(), 1);
        drop(inbox);
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        assert_eq!(inbox.pending_count(), 1);
        ingest(&cfg, &mut inbox, &hint(&live), |_| Ok(Some(live.clone()))).unwrap();
        assert_eq!(inbox.pending_count(), 1);
        let mut wrong = cfg.clone();
        wrong.0["owner_id"] = json!("301");
        assert!(Inbox::open(&dir, &wrong).is_err());
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn incomplete_four_page_repair_has_no_effect_and_version_one_cannot_guess_consent() {
        let dir = fixture_dir();
        let cfg = config();
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        inbox.checkpoint("1000").unwrap();
        inbox
            .admit(&owner_source("1001", "keep until absence is proven"))
            .unwrap();
        inbox.checkpoint("2000").unwrap();
        let file = dir.join("rust-text-v1.json");
        let prior = fs::read(&file).unwrap();
        let mut reads = 0;
        assert!(
            repair(&mut inbox, |before| {
                let upper = 2500 - reads * 100;
                assert_eq!(
                    before,
                    if reads == 0 {
                        String::new()
                    } else {
                        (upper + 1).to_string()
                    }
                );
                reads += 1;
                Ok((upper - 99..=upper)
                    .rev()
                    .map(|n| owner_source(&n.to_string(), "verified"))
                    .collect())
            })
            .is_err()
        );
        assert_eq!(reads, 4);
        assert_eq!(fs::read(&file).unwrap(), prior);
        assert_eq!(inbox.pending_count(), 1);
        drop(inbox);

        let mut legacy: Value = serde_json::from_slice(&prior).unwrap();
        legacy["version"] = json!(1);
        legacy.as_object_mut().unwrap().remove("consent_floor");
        runtime::write_json(&file, &legacy).unwrap();
        assert!(Inbox::open(&dir, &cfg).is_err());
        assert_eq!(runtime::read_json(&file).unwrap(), legacy);
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn private_runtime_reports_queue_and_accepts_only_bound_shutdown() {
        let dir = fixture_dir();
        let cfg = config();
        let mut inbox = Inbox::open(&dir, &cfg).unwrap();
        let socket = dir.join("bridge.sock");
        let listener = runtime::bind(&socket).unwrap();
        let bridge = Bridge::new(socket.clone(), listener).unwrap();
        let server = std::thread::spawn(move || {
            let deadline = Instant::now() + Duration::from_secs(3);
            while Instant::now() < deadline {
                if bridge
                    .serve(&cfg, &mut inbox, 12, false, "inactive")
                    .unwrap()
                {
                    return;
                }
                std::thread::sleep(Duration::from_millis(5));
            }
            panic!("bound shutdown did not stop bridge");
        });
        let status = super::super::local::call(&dir, "status", "", None).unwrap();
        assert_eq!(status["connector"]["state"], "receipt_only");
        assert_eq!(status["teammate"]["pending"], 0);
        assert_eq!(status["teammate"]["gateway_sequence"], 12);
        let target = "ep-1234abcd/1234567890abcdef12345678";
        let change = json!({"target_id":target});
        assert!(super::super::local::call(&dir, "exclude", "", Some(("other", &change))).is_err());
        super::super::local::call(&dir, "exclude", "", Some(("issued", &change))).unwrap();
        let file = dir.join("rust-text-v1.json");
        assert_eq!(
            runtime::read_json(&file).unwrap()["worker_excluded"][target],
            true
        );
        super::super::local::call(&dir, "include", "", Some(("issued", &change))).unwrap();
        assert!(runtime::read_json(&file).unwrap()["worker_excluded"][target].is_null());
        let body = json!({});
        assert!(super::super::local::call(&dir, "shutdown", "", Some(("other", &body))).is_err());
        assert_eq!(
            super::super::local::call(&dir, "shutdown", "", Some(("issued", &body))).unwrap()["stopped"],
            true
        );
        server.join().unwrap();
        assert!(!socket.exists());
        fs::remove_dir_all(dir).unwrap();
    }
}
