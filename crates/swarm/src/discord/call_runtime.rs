//! The call and media gate stays on the Gateway owner while native and speech I/O run separately.
use super::{
    call_floor::{FRAME_BYTES, Floor, authorized_words},
    call_helper::Helper,
    call_media::{Event, Media},
    call_origin,
    call_room::{Change, Room},
    call_speech,
    config::{Config, invalid, load_token},
    gateway,
};
use crate::{socket::DeadlineReader, supervisor::runtime};
use base64::{Engine, engine::general_purpose::STANDARD};
use serde_json::{Value, json};
use std::collections::{BTreeMap, BTreeSet};
use std::io::{self, Read, Write};
use std::os::unix::fs::{FileTypeExt, MetadataExt};
use std::os::unix::net::{UnixListener, UnixStream};
use std::path::{Path, PathBuf};
use std::sync::{
    Arc,
    atomic::{AtomicBool, AtomicU8, AtomicU64, Ordering},
    mpsc,
};
use std::thread;
use std::time::{Duration, Instant};

const FRAME: Duration = Duration::from_millis(20);
const AUTH_TIMEOUT: Duration = Duration::from_millis(150);
pub(super) const WITHDRAWN: &str = "voice cutover lacked current room authority";
const CALL_SOCKET: &str = "call-auth.sock";
const WAITING: u8 = 1;
const ACTIVE: u8 = 2;
const SUSPENDED: u8 = 3;
const UNAVAILABLE: u8 = 4;

pub(super) enum Readout {
    Acknowledge(Value),
    Publication(String, Value, String),
}

pub(super) enum Ingress {
    Discord(Value),
    Transcript(Value, String),
}

pub(super) struct Driver {
    pub(super) inbound: mpsc::Receiver<io::Result<Ingress>>,
    pub(super) sequence: Arc<AtomicU64>,
    pub(super) readout: mpsc::SyncSender<Readout>,
    stop: Arc<AtomicBool>,
    status: Arc<AtomicU8>,
    pub(super) discussion_ready: Arc<AtomicBool>,
    join: Option<thread::JoinHandle<()>>,
}

#[derive(Clone, Copy)]
struct Backends {
    audience: fn(&Config, &super::call_team::Scope, &[String]) -> bool,
    transcribe: fn(&ureq::Agent, &str, &[u8]) -> io::Result<String>,
    live: bool,
    render: fn(&call_speech::RenderCancel, &str, &str) -> io::Result<Vec<u8>>,
}

impl Default for Backends {
    fn default() -> Self {
        Self {
            audience: |cfg, scope, speakers| {
                super::rest::Discord::connect(cfg)
                    .and_then(|api| {
                        scope.verify_audience(speakers, |path| super::guild::lookup(&api, path))
                    })
                    .unwrap_or(false)
            },
            transcribe: call_speech::transcribe,
            live: !cfg!(test),
            render: call_speech::render,
        }
    }
}

impl Driver {
    pub(super) fn start(
        socket: gateway::Connection,
        cursor: gateway::Cursor,
        cfg: &Config,
        dir: &Path,
        scope: Option<Value>,
    ) -> Self {
        Self::start_with(socket, cursor, cfg, dir, scope, Backends::default())
    }

    fn start_with(
        socket: gateway::Connection,
        cursor: gateway::Cursor,
        cfg: &Config,
        dir: &Path,
        scope: Option<Value>,
        backends: Backends,
    ) -> Self {
        let (events, inbound) = mpsc::sync_channel(128);
        let (readout, publications) = mpsc::sync_channel(8);
        let sequence = Arc::new(AtomicU64::new(cursor.sequence()));
        let stop = Arc::new(AtomicBool::new(false));
        let status = Arc::new(AtomicU8::new(UNAVAILABLE));
        let discussion_ready = Arc::new(AtomicBool::new(false));
        let runtime = cfg.0["calls"]
            .is_object()
            .then(|| {
                scope
                    .ok_or_else(|| {
                        invalid("personal calls require the qualified native discussion")
                    })
                    .and_then(|scope| Calls::open(cfg, dir, scope, status.clone(), backends))
            })
            .and_then(|result| match result {
                Ok(calls) => Some(calls),
                Err(err) => {
                    eprintln!("discord: calls unavailable ({err}); continuing in text");
                    None
                }
            });
        let guild_mode = cfg.text("mode") != "personal";
        let join = {
            let stop = stop.clone();
            let sequence = sequence.clone();
            let discussion_ready = discussion_ready.clone();
            Some(thread::spawn(move || {
                let mut socket = socket;
                let mut cursor = cursor;
                let mut calls = runtime;
                let result = (|| {
                    while !stop.load(Ordering::Acquire) {
                        if let Some(event) = cursor.event(&mut socket)? {
                            sequence.store(cursor.sequence(), Ordering::Release);
                            let handled = if let Some(calls) = &mut calls {
                                calls.event(&event, &mut socket)?
                            } else {
                                false
                            };
                            if !handled
                                && (matches!(
                                    event["t"].as_str(),
                                    Some(
                                        "MESSAGE_CREATE"
                                            | "MESSAGE_UPDATE"
                                            | "MESSAGE_DELETE"
                                            | "MESSAGE_DELETE_BULK"
                                    )
                                ) || guild_mode
                                    && matches!(
                                        event["t"].as_str(),
                                        Some(
                                            "CHANNEL_UPDATE"
                                                | "CHANNEL_DELETE"
                                                | "THREAD_UPDATE"
                                                | "THREAD_DELETE"
                                                | "THREAD_MEMBERS_UPDATE"
                                                | "THREAD_MEMBER_UPDATE"
                                                | "GUILD_UPDATE"
                                                | "GUILD_DELETE"
                                                | "GUILD_MEMBER_UPDATE"
                                                | "GUILD_MEMBER_REMOVE"
                                                | "GUILD_ROLE_UPDATE"
                                                | "GUILD_ROLE_DELETE"
                                        )
                                    ))
                            {
                                send_ingress(&events, Ingress::Discord(event))?;
                            }
                        }
                        if let Some(calls) = &mut calls {
                            calls.tick(
                                &mut socket,
                                &events,
                                &publications,
                                discussion_ready.load(Ordering::Acquire),
                            )?;
                        }
                    }
                    if let Some(calls) = &mut calls {
                        calls.end(&mut socket)?;
                    }
                    let _ = socket.close(None);
                    Ok(())
                })();
                drop(calls);
                if let Err(err) = result {
                    let _ = events.try_send(Err(err));
                }
            }))
        };
        Self {
            inbound,
            sequence,
            readout,
            stop,
            status,
            discussion_ready,
            join,
        }
    }

    pub(super) fn status(&self) -> &'static str {
        match self.status.load(Ordering::Acquire) {
            WAITING => "waiting",
            ACTIVE => "active",
            SUSPENDED => "suspended",
            _ => "unavailable",
        }
    }
}

impl Drop for Driver {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Release);
        if let Some(join) = self.join.take() {
            let _ = join.join();
        }
    }
}

fn send_ingress(events: &mpsc::SyncSender<io::Result<Ingress>>, event: Ingress) -> io::Result<()> {
    events
        .try_send(Ok(event))
        .map_err(|_| invalid("Gateway receipt backlog requires history repair"))
}

struct Call {
    id: String,
    generation: u64,
    media: Media,
    floor: Floor,
    opened: Instant,
    ready: bool,
    next_turn: u64,
    turns: BTreeMap<u64, (String, String)>,
    speakers: Vec<String>,
    play_started: Option<Instant>,
    seen: BTreeSet<String>,
    play: Option<(Vec<u8>, usize, Instant)>,
    render_epoch: u64,
    transcriber: ureq::Agent,
    render_cancel: Option<Arc<call_speech::RenderCancel>>,
    live: Option<super::call_live::Live>,
}

impl Drop for Call {
    fn drop(&mut self) {
        self.transcriber.clone().shutdown();
        if let Some(cancel) = &self.render_cancel {
            cancel.cancel();
        }
    }
}

struct Calls {
    room: Room,
    guild: String,
    channel: String,
    bot: String,
    team_config: Option<Config>,
    audience: mpsc::Receiver<(u64, Instant, bool)>,
    audience_tx: mpsc::Sender<(u64, Instant, bool)>,
    checking: bool,
    last_check: Option<(u64, Instant)>,
    approved: Option<(u64, Instant)>,
    helper: Helper,
    key: String,
    key_file: PathBuf,
    next_key_check: Instant,
    key_valid: bool,
    scope: Value,
    current: Option<Call>,
    auth: UnixListener,
    auth_path: PathBuf,
    tx: mpsc::Sender<Speech>,
    rx: mpsc::Receiver<Speech>,
    transcribing: bool,
    rendering: bool,
    status: Arc<AtomicU8>,
    backends: Backends,
}

enum Speech {
    Text(u64, String, Instant, io::Result<String>, Vec<u8>),
    Render(u64, u64, io::Result<Vec<u8>>),
}

impl Calls {
    fn open(
        cfg: &Config,
        dir: &Path,
        scope: Value,
        status: Arc<AtomicU8>,
        backends: Backends,
    ) -> io::Result<Self> {
        let cfg_call = &cfg.0["calls"];
        let team = super::call_team::Scope::from_config(cfg)?;
        if let Some(team) = &team {
            if scope["channel_id"] != team.thread || scope["guild_id"] != team.guild {
                return Err(invalid(
                    "team call scope differs from the enrolled discussion",
                ));
            }
        } else if cfg_call["mode"] != "personal"
            || cfg_call["discussion_id"] != cfg.text("dm_channel_id")
            || !cfg_call["user_ids"].is_null()
            || cfg.text("mode") != "personal"
        {
            return Err(invalid(
                "call mode and linked discussion must match the installation",
            ));
        }
        let get = |k| cfg_call[k].as_str().unwrap_or_default();
        let guild = get("guild_id");
        let channel = get("room_id");
        let bot = cfg.text("application_id");
        let mut room = Room::new(guild, channel, cfg.text("owner_id"), bot)?;
        if let Some(team) = team {
            room = room.team(team);
        }
        let team_config = room.is_team().then(|| cfg.clone());
        let helper = if get("helper_path").is_empty() {
            Helper::bundled()?
        } else {
            Helper::open(Path::new(get("helper_path")))?
        };
        let keys = Path::new(get("speech_state_dir"));
        if !keys.is_absolute() {
            return Err(invalid("configure an absolute speech credential directory"));
        }
        let key_file = keys.join("openai.key");
        let key = load_token(&key_file).map_err(|_| invalid("speech credential unavailable"))?;
        if !key.bytes().all(|byte| byte.is_ascii_graphic()) {
            return Err(invalid("speech credential is invalid"));
        }
        let auth_path = dir.join(CALL_SOCKET);
        runtime::reclaim_socket(&auth_path)?;
        let auth = runtime::bind(&auth_path)?;
        auth.set_nonblocking(true)?;
        let (tx, rx) = mpsc::channel();
        let (audience_tx, audience) = mpsc::channel();
        status.store(WAITING, Ordering::Release);
        Ok(Self {
            room,
            guild: guild.into(),
            channel: channel.into(),
            bot: bot.into(),
            team_config,
            audience,
            audience_tx,
            checking: false,
            last_check: None,
            approved: None,
            helper,
            key,
            key_file,
            next_key_check: Instant::now(),
            key_valid: true,
            scope,
            current: None,
            auth,
            auth_path,
            tx,
            rx,
            transcribing: false,
            rendering: false,
            status,
            backends,
        })
    }

    fn end(&mut self, socket: &mut gateway::Connection) -> io::Result<()> {
        if self.status.load(Ordering::Acquire) != SUSPENDED {
            self.status.store(
                if self.key_valid { WAITING } else { UNAVAILABLE },
                Ordering::Release,
            );
        }
        self.current.take();
        if let Some(leave) = self.room.leave() {
            gateway::send(socket, leave)?;
        }
        Ok(())
    }

    fn event(&mut self, event: &Value, socket: &mut gateway::Connection) -> io::Result<bool> {
        let (handled, change) = self.room.team_control(event, crate::ipc::spawn::now_ms());
        let change = if handled {
            change
        } else {
            self.room.event(event)
        };
        match change {
            Change::Ignore => {}
            Change::End => self.end(socket)?,
            Change::Voice(event) => {
                if self
                    .current
                    .as_mut()
                    .is_none_or(|call| call.media.gateway(&event).is_err())
                {
                    self.end(socket)?;
                }
            }
        }
        Ok(handled)
    }

    fn tick(
        &mut self,
        socket: &mut gateway::Connection,
        ingress: &mpsc::SyncSender<io::Result<Ingress>>,
        publications: &mpsc::Receiver<Readout>,
        discussion_ready: bool,
    ) -> io::Result<()> {
        let now = Instant::now();
        if now >= self.next_key_check {
            self.key_valid = load_token(&self.key_file).is_ok_and(|key| key == self.key);
            self.next_key_check = now + Duration::from_secs(1);
            if !self.key_valid && self.current.is_some() {
                self.end(socket)?;
            }
        }
        if self.room.expire(now)
            || self.current.as_ref().is_some_and(|call| {
                !call.ready && now.duration_since(call.opened) >= Duration::from_secs(10)
                    || call
                        .live
                        .as_ref()
                        .is_some_and(super::call_live::Live::failed)
            })
        {
            self.end(socket)?;
        }
        let wall = crate::ipc::spawn::now_ms();
        if self.room.is_team() {
            self.team_audience(socket, now, wall, discussion_ready)?;
        }
        if discussion_ready
            && self.key_valid
            && let Some((generation, speakers)) =
                self.room
                    .begin_verified(now, wall, self.approved.map(|(version, _)| version))
        {
            let mut id = [0_u8; 16];
            std::fs::File::open("/dev/urandom")?.read_exact(&mut id)?;
            match Media::start(
                &self.helper,
                &self.guild,
                &self.channel,
                &self.bot,
                speakers.clone(),
            ) {
                Ok(media) => {
                    self.current = Some(Call {
                        id: id.iter().map(|byte| format!("{byte:02x}")).collect(),
                        generation,
                        media,
                        floor: Floor::new(self.room.is_team()),
                        speakers,
                        play_started: None,
                        opened: now,
                        ready: false,
                        next_turn: 0,
                        turns: BTreeMap::new(),
                        seen: BTreeSet::new(),
                        play: None,
                        render_epoch: 0,
                        transcriber: call_speech::transcription_session(),
                        render_cancel: None,
                        live: None,
                    })
                }
                Err(_) => {
                    eprintln!("discord: call helper unavailable; continuing in text");
                    self.end(socket)?;
                }
            }
        }
        for _ in 0..8 {
            let Some(call) = &mut self.current else {
                break;
            };
            match call.media.poll() {
                Ok(None) => break,
                Ok(Some(Event::Join)) => {
                    if let Some(join) = self.room.join(call.generation, now) {
                        gateway::send(socket, join)?;
                    } else {
                        self.end(socket)?;
                        break;
                    }
                }
                Ok(Some(Event::Ready)) => {
                    call.ready = self.room.ready(call.generation, now);
                    self.status
                        .store(if call.ready { ACTIVE } else { WAITING }, Ordering::Release);
                    if !call.ready {
                        self.end(socket)?;
                        break;
                    }
                    call.play = Some((cue(), 0, now));
                    if self.backends.live {
                        call.live = Some(super::call_live::Live::start(self.key.clone()));
                    }
                }
                Ok(Some(Event::Audio { speaker, pcm })) => {
                    let mut bytes = [0; FRAME_BYTES];
                    if STANDARD.decode_slice(pcm, &mut bytes).ok() != Some(FRAME_BYTES) {
                        self.end(socket)?;
                        break;
                    }
                    let progress = call.floor.push(&speaker, &bytes, now);
                    if !self
                        .room
                        .forward(call.generation, &speaker, progress.voiced, now)
                    {
                        self.end(socket)?;
                        break;
                    }
                    if progress.interrupt {
                        if let Some(cancel) = call.render_cancel.take() {
                            cancel.cancel();
                        }
                        call.render_epoch += 1;
                        call.play = None;
                        if call.media.silence().is_err() {
                            self.end(socket)?;
                            break;
                        }
                    }
                    if let Some(turn) = progress.complete {
                        self.transcribe(turn, socket)?;
                    }
                }
                Err(err) => {
                    if err.kind() == io::ErrorKind::ConnectionAborted && self.room.rekey(now) {
                        self.approved = None;
                        self.last_check = None;
                    }
                    self.end(socket)?;
                    break;
                }
            }
        }
        self.check_auth(socket, now)?;
        if let Some(turn) = self
            .current
            .as_mut()
            .and_then(|call| call.floor.settle(now))
        {
            self.transcribe(turn, socket)?;
        }
        while let Ok(result) = self.rx.try_recv() {
            match result {
                Speech::Text(generation, speaker, last_voice, text, pcm) => {
                    self.transcribing = false;
                    let Some(call) = self
                        .current
                        .as_mut()
                        .filter(|call| call.generation == generation)
                    else {
                        continue;
                    };
                    let Ok(text) = text else {
                        self.end(socket)?;
                        continue;
                    };
                    let Some(text) = authorized_words(&text, self.room.is_team()) else {
                        continue;
                    };
                    if self.room.is_team()
                        && call
                            .play_started
                            .is_some_and(|started| started <= last_voice)
                    {
                        if let Some(cancel) = call.render_cancel.take() {
                            cancel.cancel();
                        }
                        call.render_epoch += 1;
                        call.play = None;
                        call.play_started = None;
                        if call.media.silence().is_err() {
                            self.end(socket)?;
                            continue;
                        }
                    }
                    call.next_turn += 1;
                    if call.next_turn > 256 {
                        self.end(socket)?;
                        continue;
                    }
                    if call
                        .live
                        .as_ref()
                        .is_some_and(|live| !live.turn(call.next_turn, pcm))
                    {
                        self.end(socket)?;
                        continue;
                    }
                    let origin = call_origin::speaker_origin(
                        &self.scope,
                        &call.id,
                        generation,
                        call.next_turn,
                        &speaker,
                        text,
                    )?;
                    call.turns.insert(
                        call.next_turn,
                        (speaker, origin["revision"].as_str().unwrap().into()),
                    );
                    send_ingress(ingress, Ingress::Transcript(origin, text.into()))?;
                }
                Speech::Render(generation, epoch, audio) => {
                    self.rendering = false;
                    if let Some(call) = self
                        .current
                        .as_mut()
                        .filter(|call| call.generation == generation && call.render_epoch == epoch)
                    {
                        call.render_cancel = None;
                        if let Ok(audio) = audio {
                            call.play = Some((audio, 0, now));
                        }
                    }
                }
            }
        }
        if !self.rendering
            && let Some(call) = self
                .current
                .as_mut()
                .filter(|call| call.play.is_none() && call.ready)
            && let Ok(readout) = publications.try_recv()
        {
            match readout {
                Readout::Acknowledge(origin)
                    if matches_turn(call, &origin) && call.floor.quiet(now) =>
                {
                    call.play = Some((cue(), 0, now));
                    if let Some(live) = &call.live
                        && let Some(turn) = origin["voice"]["turn_id"].as_u64()
                    {
                        live.commentary(turn, "The dispatcher accepted the request.");
                    }
                }
                Readout::Publication(nonce, origin, text)
                    if matches_turn(call, &origin)
                        && call.seen.len() < 256
                        && call.seen.insert(nonce.clone()) =>
                {
                    if let Some(live) = &call.live
                        && let Some(turn) = origin["voice"]["turn_id"].as_u64()
                    {
                        live.commentary(turn, &text);
                    }
                    self.rendering = true;
                    let (generation, epoch) = (call.generation, call.render_epoch);
                    let (key, tx) = (self.key.clone(), self.tx.clone());
                    let cancel = Arc::new(call_speech::RenderCancel::default());
                    call.render_cancel = Some(cancel.clone());
                    let render = self.backends.render;
                    thread::spawn(move || {
                        let _ = tx.send(Speech::Render(
                            generation,
                            epoch,
                            render(&cancel, &key, &text),
                        ));
                    });
                }
                _ => {}
            }
        }
        if let Some(call) = self.current.as_mut()
            && let Some((pcm, offset, next)) = &mut call.play
            && now >= *next
            && call.floor.quiet(now)
        {
            if !self
                .room
                .forward(call.generation, &call.speakers[0], false, now)
            {
                return self.end(socket);
            }
            let mut frame = [0; FRAME_BYTES];
            let count = (pcm.len() - *offset).min(FRAME_BYTES);
            frame[..count].copy_from_slice(&pcm[*offset..*offset + count]);
            if call.media.play(&STANDARD.encode(frame)).is_err() {
                return self.end(socket);
            }
            if *offset == 0 {
                call.play_started = Some(now);
            }
            *offset += count;
            *next = now + FRAME;
            if *offset == pcm.len() {
                call.play = None;
            }
        }
        Ok(())
    }

    fn team_audience(
        &mut self,
        socket: &mut gateway::Connection,
        now: Instant,
        wall: u64,
        ready: bool,
    ) -> io::Result<()> {
        let candidate = self.room.team_candidate(wall);
        while let Ok((version, checked, permitted)) = self.audience.try_recv() {
            self.checking = false;
            if candidate
                .as_ref()
                .is_some_and(|(current, _, _)| *current == version)
                && permitted
            {
                self.approved = Some((version, checked + Duration::from_secs(15)));
            } else {
                self.approved = None;
            }
        }
        if self.approved.is_some_and(|(version, until)| {
            now >= until
                || candidate
                    .as_ref()
                    .is_none_or(|(current, _, _)| *current != version)
        }) {
            self.approved = None;
        }
        if self.current.is_some() && self.approved.is_none() {
            self.end(socket)?;
            return Ok(());
        }
        if ready
            && !self.checking
            && let Some((version, speakers, _)) = candidate
            && self.last_check.is_none_or(|(previous, at)| {
                previous != version || now.saturating_duration_since(at) >= Duration::from_secs(10)
            })
            && let Some(scope) = self.room.team_scope().cloned()
            && let Some(cfg) = self.team_config.clone()
        {
            self.last_check = Some((version, now));
            self.checking = true;
            let tx = self.audience_tx.clone();
            let verify = self.backends.audience;
            thread::spawn(move || {
                let allowed = verify(&cfg, &scope, &speakers);
                let _ = tx.send((version, now, allowed));
            });
        }
        Ok(())
    }

    fn transcribe(
        &mut self,
        turn: super::call_floor::Utterance,
        socket: &mut gateway::Connection,
    ) -> io::Result<()> {
        let Some(call) = &self.current else {
            return Ok(());
        };
        if self.transcribing
            || !call.speakers.contains(&turn.speaker)
            || !self.key_valid
            || !call.ready
            || turn.last_voice.elapsed() > Duration::from_secs(2)
        {
            return self.end(socket);
        }
        self.transcribing = true;
        let generation = call.generation;
        let (key, tx) = (self.key.clone(), self.tx.clone());
        let transcriber = call.transcriber.clone();
        let transcribe = self.backends.transcribe;
        thread::spawn(move || {
            let _ = tx.send(Speech::Text(
                generation,
                turn.speaker,
                turn.last_voice,
                transcribe(&transcriber, &key, &turn.pcm),
                turn.pcm,
            ));
        });
        Ok(())
    }

    fn check_auth(&mut self, socket: &mut gateway::Connection, now: Instant) -> io::Result<()> {
        let Ok((mut stream, _)) = self.auth.accept() else {
            return Ok(());
        };
        // This owner-only caller sends a complete bounded packet followed by EOF.
        stream.set_write_timeout(Some(Duration::from_millis(10)))?;
        let mut bytes = Vec::new();
        if DeadlineReader(&stream, Instant::now() + Duration::from_millis(10))
            .take(2049)
            .read_to_end(&mut bytes)
            .is_err()
            || bytes.len() > 2048
        {
            return Ok(());
        }
        let Ok(value) = serde_json::from_slice::<Value>(&bytes) else {
            return Ok(());
        };
        if value.as_object().is_some_and(|fields| fields.len() == 1)
            && value["stop"].as_str() == self.scope["installation_id"].as_str()
            && value["stop"].is_string()
        {
            self.room.suspend();
            self.status.store(SUSPENDED, Ordering::Release);
            self.current.take();
            let _ = stream.write_all(b"y");
            return self.end(socket);
        }
        if let Some(call) = &self.current
            && value.as_object().is_some_and(|fields| fields.len() == 4)
            && matches_coordinate(call, &value)
            && self
                .room
                .forward(call.generation, &call.speakers[0], false, now)
        {
            let _ = stream.write_all(b"y");
        }
        Ok(())
    }
}

impl Drop for Calls {
    fn drop(&mut self) {
        self.status.store(UNAVAILABLE, Ordering::Release);
        let _ = std::fs::remove_file(&self.auth_path);
    }
}

fn cue() -> Vec<u8> {
    const SAMPLES: usize = 2400;
    (0..SAMPLES)
        .flat_map(|sample| {
            let phase = sample as f64;
            let envelope = (std::f64::consts::PI * phase / SAMPLES as f64).sin();
            let signal =
                2700.0 * envelope * (std::f64::consts::TAU * 660.0 * phase / 24_000.0).sin();
            (signal.round() as i16).to_le_bytes()
        })
        .collect::<Vec<_>>()
}

fn matches_turn(call: &Call, origin: &Value) -> bool {
    let voice = &origin["voice"];
    matches_coordinate(call, voice)
        && voice["turn_id"]
            .as_u64()
            .and_then(|turn| call.turns.get(&turn))
            .is_some_and(|(speaker, _)| voice["speaker_id"] == *speaker)
}

fn matches_coordinate(call: &Call, voice: &Value) -> bool {
    voice["call_id"] == call.id
        && voice["generation"] == call.generation
        && voice["turn_id"]
            .as_u64()
            .and_then(|turn| call.turns.get(&turn))
            .is_some_and(|(_, digest)| voice["body_hash"] == *digest)
}

pub(super) fn live_coordinate(origin: &Value) -> Value {
    let voice = &origin["voice"];
    json!({"call_id":voice["call_id"],"generation":voice["generation"],
        "turn_id":voice["turn_id"],"body_hash":voice["body_hash"]})
}

pub(super) fn verify_live(dir: &Path, origin: &Value) -> io::Result<()> {
    auth_request(dir, &live_coordinate(origin))
}

pub(super) fn stop(dir: &Path, installation: &str) -> io::Result<()> {
    auth_request(dir, &json!({"stop":installation}))
}

pub(super) fn stop_if_running(dir: &Path, installation: &str) -> io::Result<()> {
    // No independent call listener is expected for a disabled or older connector.
    match std::fs::symlink_metadata(dir.join(CALL_SOCKET)) {
        Err(err) if err.kind() == io::ErrorKind::NotFound => return Ok(()),
        Err(err) => return Err(err),
        Ok(_) => {}
    }
    stop(dir, installation)
}

fn auth_request(dir: &Path, input: &Value) -> io::Result<()> {
    let path = dir.join(CALL_SOCKET);
    let meta =
        std::fs::symlink_metadata(&path).map_err(|_| invalid("call authority is unavailable"))?;
    if !meta.file_type().is_socket()
        || meta.mode() & 0o777 != 0o600
        || meta.uid() != unsafe { libc::geteuid() }
    {
        return Err(invalid("unsafe call authority socket"));
    }
    let mut stream =
        UnixStream::connect(path).map_err(|_| invalid("call authority is unavailable"))?;
    stream.set_write_timeout(Some(AUTH_TIMEOUT))?;
    stream.set_read_timeout(Some(AUTH_TIMEOUT))?;
    stream.write_all(&serde_json::to_vec(input)?)?;
    stream.shutdown(std::net::Shutdown::Write)?;
    let mut response = [0; 1];
    if stream.read_exact(&mut response).is_err() || &response != b"y" {
        return Err(invalid("call authority expired or was withdrawn"));
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    use std::fs;
    use std::net::{TcpListener, TcpStream};
    use std::os::unix::fs::PermissionsExt;
    use tungstenite::{Message, WebSocket, accept, client, stream::MaybeTlsStream};

    fn voice(sequence: u64, user: &str, channel: Value) -> Value {
        json!({"op":0,"t":"VOICE_STATE_UPDATE","s":sequence,"d":{
            "guild_id":"1","channel_id":channel,"user_id":user,"session_id":"fixture-session",
            "member":{"user":{"id":user,"bot":user=="3"}},"mute":false,
            "deaf":false,"self_mute":false,"self_deaf":false,"suppress":false
        }})
    }

    fn recv<T: Read + Write>(socket: &mut WebSocket<T>) -> Value {
        let message = socket.read().unwrap();
        serde_json::from_str(message.to_text().unwrap()).unwrap()
    }

    fn send<T: Read + Write>(socket: &mut WebSocket<T>, value: Value) {
        socket
            .send(Message::Text(value.to_string().into()))
            .unwrap();
    }

    #[test]
    fn two_team_participants_use_one_media_child_and_one_linked_discussion() {
        let dir =
            std::env::temp_dir().join(format!("swarm-team-call-{}", runtime::random_id().unwrap()));
        runtime::private_dir(&dir).unwrap();
        let helper = dir.join("helper");
        let key = dir.join("openai.key");
        fs::write(&key, "fixture-no-provider-credential").unwrap();
        fs::set_permissions(&key, fs::Permissions::from_mode(0o600)).unwrap();
        let frame = |user: &str, n: i16| {
            json!({"type":"audio","userId":user,
            "pcm":STANDARD.encode(n.to_le_bytes().repeat(FRAME_BYTES/2))})
        };
        let script = format!(
            r#"#!/bin/sh
read -r start
case "$start" in *'"speakers":["8","9"]'*) ;; *) exit 1;; esac
printf '%s\n' '{{"type":"gateway","data":{{"op":4,"d":{{"guild_id":"1","channel_id":"2","self_deaf":false,"self_mute":false}}}}}}'
read -r first
read -r second
printf '%s\n' '{{"type":"ready"}}'
for row in '{}' '{}' '{}'; do
    for n in 1 2 3 4 5; do printf '%s\n' "$row"; done
    /bin/sleep 0.7
done
while read -r line; do :; done
"#,
            frame("8", 1200),
            frame("8", 1500),
            frame("9", 1800)
        );
        fs::write(&helper, script).unwrap();
        fs::set_permissions(&helper, fs::Permissions::from_mode(0o700)).unwrap();
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let url = format!("ws://{}", listener.local_addr().unwrap());
        let (leave, paused) = mpsc::sync_channel::<()>(0);
        let fixture = thread::spawn(move || {
            let (tcp, _) = listener.accept().unwrap();
            tcp.set_read_timeout(Some(Duration::from_secs(5))).unwrap();
            let mut socket = accept(tcp).unwrap();
            send(
                &mut socket,
                json!({"op":10,"d":{"heartbeat_interval":60000}}),
            );
            assert_eq!(recv(&mut socket)["d"]["intents"], 37505);
            send(
                &mut socket,
                json!({"op":0,"t":"READY","s":1,"d":{"application":{"id":"3"},"user":{"id":"3","bot":true}}}),
            );
            send(
                &mut socket,
                json!({"op":0,"t":"GUILD_CREATE","s":2,"d":{"id":"1","channels":[{"id":"2","type":2}],
                "voice_states":[voice(2,"8",json!("2"))["d"],voice(2,"9",json!("2"))["d"]]}}),
            );
            thread::sleep(Duration::from_millis(50));
            let id =
                |at: u64, sequence: u64| (((at - 1_420_070_400_000) << 22) | sequence).to_string();
            let at = crate::ipc::spawn::now_ms();
            let stamp = time::OffsetDateTime::from_unix_timestamp_nanos(i128::from(at) * 1_000_000)
                .unwrap()
                .format(&time::format_description::well_known::Rfc3339)
                .unwrap();
            let message = |seq, user, action| {
                json!({"op":0,"t":"MESSAGE_CREATE","s":seq,"d":{"guild_id":"1","channel_id":"6",
                "author":{"id":user},"mentions":[{"id":"3"}],"type":0,"id":id(at,seq),"timestamp":stamp,
                "content":format!("<@3> voice {action}")}})
            };
            send(&mut socket, message(3, "8", "join"));
            send(&mut socket, message(4, "9", "consent"));
            assert_eq!(recv(&mut socket)["d"]["channel_id"], "2");
            send(&mut socket, voice(5, "3", json!("2")));
            send(
                &mut socket,
                json!({"op":0,"t":"VOICE_SERVER_UPDATE","s":6,"d":{
                        "guild_id":"1","endpoint":"voice.invalid","token":"synthetic"}}),
            );
            paused.recv_timeout(Duration::from_secs(4)).unwrap();
            send(
                &mut socket,
                json!({"op":0,"t":"MESSAGE_DELETE","s":7,"d":{"guild_id":"1","channel_id":"6","id":id(at,4)}}),
            );
            assert!(recv(&mut socket)["d"]["channel_id"].is_null());
        });
        let tcp = TcpStream::connect(url.trim_start_matches("ws://")).unwrap();
        tcp.set_read_timeout(Some(FRAME)).unwrap();
        let (mut socket, _) = client(url, MaybeTlsStream::Plain(tcp)).unwrap();
        let cfg = Config(
            json!({"mode":"teammate","application_id":"3","owner_id":"4","installation_id":"fixture",
            "daemon_socket":"/tmp/native-fixture.sock","guild_ids":["1"],"team_discussion":{"guild_id":"1","thread_id":"6"},
            "calls":{"mode":"team","guild_id":"1","room_id":"2","discussion_id":"6","user_ids":["8","9"],
            "helper_path":helper,"speech_state_dir":dir}}),
        );
        let scope = json!({"installation_id":"fixture","application_id":"3","owner_id":"4","guild_id":"1","channel_id":"6",
            "supervisor_socket":"/tmp/native-fixture.sock","launch":{"manager":{"conversation_id":"discord-teammate-team"}}});
        let cursor = gateway::ready(&mut socket, &cfg, "fixture-token").unwrap();
        let backends = Backends {
            live: false,
            audience: |_, scope, speakers| scope.thread == "6" && speakers == ["8", "9"],
            transcribe: |_, _, pcm| {
                Ok(match i16::from_le_bytes([pcm[0], pcm[1]]) {
                    1200 => "conversations are fun",
                    1500 => "Dispatcher, first marker",
                    1800 => "Hey dispatcher, second marker",
                    _ => panic!("unexpected frame"),
                }
                .to_owned())
            },
            render: |_, _, _| panic!("fixture never uses a remote renderer"),
        };
        let driver = Driver::start_with(socket, cursor, &cfg, &dir, Some(scope.clone()), backends);
        driver.discussion_ready.store(true, Ordering::Release);
        let mut inbox = super::super::receipt::Inbox::open(&dir, &cfg).unwrap();
        inbox.checkpoint("").unwrap();
        for (speaker, words) in [("8", "first marker"), ("9", "second marker")] {
            let Ingress::Transcript(origin, text) = driver
                .inbound
                .recv_timeout(Duration::from_secs(4))
                .unwrap()
                .unwrap()
            else {
                panic!("team controls were replayed as ordinary text")
            };
            assert_eq!(text, words);
            assert_eq!(origin["actor_id"], speaker);
            assert_eq!(origin["voice"]["speaker_id"], speaker);
            assert_eq!(origin["channel_id"], "6");
            verify_live(&dir, &origin).unwrap();
            inbox.admit_voice(&scope, origin, &text).unwrap();
        }
        leave.send(()).unwrap();
        fixture.join().unwrap();
        drop(driver);
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn process_and_gateway_revoke_during_stalled_native_consumer() {
        stalled_consumer(false);
    }

    #[test]
    fn normal_disconnect_revokes_before_unblocking_text_shutdown() {
        stalled_consumer(true);
    }

    fn stalled_consumer(disconnect: bool) {
        let mut random = [0_u8; 8];
        fs::File::open("/dev/urandom")
            .unwrap()
            .read_exact(&mut random)
            .unwrap();
        let dir =
            std::env::temp_dir().join(format!("swarm-call-{:016x}", u64::from_ne_bytes(random)));
        runtime::private_dir(&dir).unwrap();
        let text_shutdown = disconnect.then(|| {
            let lease = runtime::lock(&dir.join("bridge.lock")).unwrap();
            let listener = runtime::bind(&dir.join("bridge.sock")).unwrap();
            let (release, wait) = mpsc::sync_channel::<()>(0);
            let thread = thread::spawn(move || {
                let (mut stream, _) = listener.accept().unwrap();
                // The normal text bridge cannot answer until the native operation returns.
                wait.recv_timeout(Duration::from_secs(4)).unwrap();
                let mut bytes = [0; 2048];
                let _ = stream.read(&mut bytes).unwrap();
                stream
                    .write_all(b"HTTP/1.0 200 OK\r\nContent-Length: 16\r\n\r\n{\"stopped\":true}")
                    .unwrap();
                drop(lease);
            });
            (release, thread)
        });
        let helper = dir.join("helper");
        let key_file = dir.join("openai.key");
        fs::write(&key_file, "unused-fixture-key").unwrap();
        fs::set_permissions(&key_file, fs::Permissions::from_mode(0o600)).unwrap();
        fs::write(&helper,r#"#!/bin/sh
[ -z "$HOME" ] || exit 2
read -r start
printf '%s\n' '{"type":"gateway","data":{"op":4,"d":{"guild_id":"1","channel_id":"2","self_deaf":false,"self_mute":false}}}'
read -r first
read -r second
case "$first" in *voice_server*) ;; *) exit 3;; esac
case "$second" in *voice_state*) ;; *) exit 4;; esac
printf '%s\n' '{"type":"ready"}'
while read -r command; do :; done
"#).unwrap();
        fs::set_permissions(&helper, fs::Permissions::from_mode(0o700)).unwrap();
        let server = TcpListener::bind("127.0.0.1:0").unwrap();
        let (owner_ready, wait_ready) = mpsc::sync_channel::<()>(1);
        let address = server.local_addr().unwrap();
        let fixture = thread::spawn(move || {
            let (tcp, _) = server.accept().unwrap();
            tcp.set_read_timeout(Some(Duration::from_secs(3))).unwrap();
            let mut server = accept(tcp).unwrap();
            send(
                &mut server,
                json!({"op":10,"d":{"heartbeat_interval":60000}}),
            );
            assert_eq!(recv(&mut server)["d"]["intents"], 4225);
            send(
                &mut server,
                json!({"op":0,"t":"READY","s":1,"d":{
                    "application":{"id":"3"},"user":{"id":"3","bot":true}
                }}),
            );
            send(
                &mut server,
                json!({"op":0,"t":"GUILD_CREATE","s":2,"d":{
                    "id":"1","channels":[{"id":"2","type":2}],
                    "voice_states":[voice(2,"4",json!("2"))["d"]]
                }}),
            );
            let join = recv(&mut server);
            assert_eq!(join["op"], 4);
            assert_eq!(join["d"]["channel_id"], "2");
            send(
                &mut server,
                json!({"op":0,"t":"VOICE_SERVER_UPDATE","s":3,"d":{
                    "guild_id":"1","endpoint":"voice.invalid","token":"transient-fixture-token"
                }}),
            );
            send(&mut server, voice(4, "3", json!("2")));
            wait_ready.recv_timeout(Duration::from_secs(2)).unwrap();
            send(
                &mut server,
                json!({"op":0,"t":"MESSAGE_CREATE","s":5,"d":{"id":"10"}}),
            );
            if !disconnect {
                send(&mut server, voice(6, "5", json!("2")));
            }
            let leave = recv(&mut server);
            assert_eq!(leave["op"], 4);
            assert!(leave["d"]["channel_id"].is_null());
            send(
                &mut server,
                voice(if disconnect { 6 } else { 7 }, "3", Value::Null),
            );
        });
        let tcp = TcpStream::connect(address).unwrap();
        tcp.set_read_timeout(Some(FRAME)).unwrap();
        let (mut socket, _) =
            client(format!("ws://{address}"), MaybeTlsStream::Plain(tcp)).unwrap();
        let cfg = Config(
            json!({"version":1,"enabled":true,"name":"fixture","mode":"personal","manager_cli":"codex",
            "installation_id":"test","application_id":"3","owner_id":"4","dm_channel_id":"6",
            "token_file":dir.join("synthetic-token"),"daemon_socket":dir.join("daemon.sock"),"work_dir":dir,
            "calls":{"mode":"personal","guild_id":"1","room_id":"2","discussion_id":"6",
            "helper_path":helper,"speech_state_dir":dir}}),
        );
        if disconnect {
            runtime::write_json(&dir.join("config.json"), &cfg.0).unwrap();
            runtime::write_json(&dir.join(".rust-offline-owner.json"), &json!("test")).unwrap();
        }
        let cursor = gateway::ready(&mut socket, &cfg, "never-real").unwrap();
        let driver = Driver::start(
            socket,
            cursor,
            &cfg,
            &dir,
            Some(json!({"installation_id":"test"})),
        );
        driver.discussion_ready.store(true, Ordering::Release);
        let until = Instant::now() + Duration::from_secs(2);
        while driver.status() != "active" {
            assert!(Instant::now() < until, "fixture helper never became ready");
            thread::sleep(Duration::from_millis(5));
        }
        owner_ready.send(()).unwrap();
        let shutdown = disconnect.then(|| {
            let place = super::super::config::Place::fixture(dir.clone());
            thread::spawn(move || place.disconnect())
        });
        fixture.join().unwrap();
        assert_ne!(driver.status(), "active");
        if let Some((release, text)) = text_shutdown {
            let shutdown = shutdown.unwrap();
            if shutdown.is_finished() {
                panic!(
                    "text shutdown completed before release: {:?}",
                    shutdown.join().unwrap()
                );
            }
            release.send(()).unwrap();
            text.join().unwrap();
            shutdown.join().unwrap().unwrap();
            assert_eq!(
                runtime::read_json(&dir.join("config.json")).unwrap()["enabled"],
                false
            );
        }
        // Native consumer remained blocked through the leave. Only the text event was queued.
        assert!(
            matches!(driver.inbound.try_recv(),Ok(Ok(Ingress::Discord(event))) if event["d"]["id"] == "10")
        );
        assert!(!matches!(driver.inbound.try_recv(), Ok(Ok(_))));
        drop(driver);
        assert!(!dir.join(CALL_SOCKET).exists());
        fs::remove_dir_all(dir).unwrap();
    }
}
