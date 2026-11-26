//! GPT-Live commentary sidecar. It cannot admit work or supply room playback.
use super::{
    call_speech::{RenderCancel, speech_socket},
    config::invalid,
};
use base64::{Engine, engine::general_purpose::STANDARD};
use serde_json::{Value, json};
use std::collections::{BTreeMap, BTreeSet};
use std::io::{self, Read, Write};
use std::sync::{
    Arc,
    atomic::{AtomicBool, Ordering},
    mpsc,
};
use std::time::{Duration, Instant};
use tungstenite::{Message, WebSocket, stream::MaybeTlsStream};

const ENDPOINT: &str = "wss://api.openai.com/v1/live/sessions";
const AUDIO_SECOND: usize = 48_000;
const PADDING_MS: u64 = 900;
const MATCH_TAIL_MS: u64 = 770;
const MAX_EVENTS: usize = 8192;
const MAX_TURNS: usize = 256;
const MAX_COMMENTARY: usize = 500;

enum Command {
    Turn(u64, Vec<u8>),
    Commentary(u64, String),
}

pub(super) struct Live {
    send: mpsc::SyncSender<Command>,
    cancel: Arc<RenderCancel>,
    failed: Arc<AtomicBool>,
}

impl Live {
    pub(super) fn start(key: String) -> Self {
        let (send, receive) = mpsc::sync_channel(4);
        let cancel = Arc::new(RenderCancel::default());
        let failed = Arc::new(AtomicBool::new(false));
        let worker_cancel = cancel.clone();
        let worker_failed = failed.clone();
        std::thread::spawn(move || {
            let result = (|| {
                let deadline = Instant::now() + Duration::from_secs(15);
                let mut socket = speech_socket(ENDPOINT, &key, &worker_cancel, deadline)?;
                const POLL: Duration = Duration::from_millis(100);
                match socket.get_mut() {
                    MaybeTlsStream::Rustls(stream) => stream.sock.set_read_timeout(Some(POLL))?,
                    MaybeTlsStream::Plain(stream) => stream.set_read_timeout(Some(POLL))?,
                    _ => return Err(invalid("unsupported GPT-Live socket")),
                }
                startup(&mut socket, deadline, &worker_cancel)?;
                pump(&mut socket, receive, &worker_cancel)
            })();
            if result.is_err() && !worker_cancel.canceled() {
                worker_failed.store(true, Ordering::Release);
            }
        });
        Self {
            send,
            cancel,
            failed,
        }
    }

    pub(super) fn turn(&self, id: u64, pcm: Vec<u8>) -> bool {
        self.send.try_send(Command::Turn(id, pcm)).is_ok()
    }

    pub(super) fn commentary(&self, id: u64, text: &str) {
        let text = text.trim();
        let text = &text[..text.floor_char_boundary(text.len().min(MAX_COMMENTARY))];
        if !text.is_empty() {
            let _ = self.send.try_send(Command::Commentary(id, text.into()));
        }
    }

    pub(super) fn failed(&self) -> bool {
        self.failed.load(Ordering::Acquire)
    }
}

impl Drop for Live {
    fn drop(&mut self) {
        self.cancel.cancel();
    }
}

#[derive(Default)]
struct Delegations {
    cursor: u64,
    turns: BTreeMap<u64, Window>,
    events: BTreeSet<String>,
    claimed: BTreeSet<String>,
}

struct Window {
    start: u64,
    end: u64,
    valid_until: Instant,
    delegation: Option<String>,
    commentary: Vec<String>,
}

impl Delegations {
    fn record(&mut self, id: u64, pcm: &[u8], now: Instant) -> io::Result<()> {
        if id == 0
            || self.turns.len() >= MAX_TURNS
            || pcm.is_empty()
            || pcm.len() > 20 * AUDIO_SECOND
            || !pcm.len().is_multiple_of(48)
            || self.turns.contains_key(&id)
        {
            return Err(invalid("invalid GPT-Live utterance window"));
        }
        let end = self.cursor + (pcm.len() / 48) as u64;
        self.turns.insert(
            id,
            Window {
                start: self.cursor,
                end,
                valid_until: now + Duration::from_millis(2200),
                delegation: None,
                commentary: Vec::new(),
            },
        );
        self.cursor = end + PADDING_MS;
        Ok(())
    }

    fn event(&mut self, event: &Value, now: Instant) -> io::Result<()> {
        match event["type"].as_str() {
            Some("error" | "session.closed") | None => {
                return Err(invalid("GPT-Live call session closed"));
            }
            Some("session.delegation.created") if event["delegation"]["target"] == "client" => {}
            Some("session.input_transcript.delta") => {
                let start = event["start_ms"]
                    .as_u64()
                    .ok_or_else(|| invalid("invalid GPT-Live transcript offset"))?;
                if !event["end_ms"]
                    .as_u64()
                    .is_some_and(|end| start <= end && end <= self.cursor + 1000)
                    || !event["delta"]
                        .as_str()
                        .is_some_and(|word| word.len() <= 4096)
                {
                    return Err(invalid("invalid GPT-Live transcript cursor"));
                }
                return Ok(());
            }
            _ => return Ok(()), // Model audio and generated words are never used.
        }
        let id = coordinate(&event["event_id"])
            .ok_or_else(|| invalid("invalid GPT-Live event identity"))?;
        if self.events.contains(id) {
            return Ok(());
        }
        if self.events.len() >= MAX_EVENTS {
            return Err(invalid("GPT-Live event capacity exceeded"));
        }
        self.events.insert(id.into());
        let delegate = coordinate(&event["delegation"]["id"])
            .ok_or_else(|| invalid("invalid GPT-Live delegation identity"))?;
        let offset = event["offset_ms"]
            .as_u64()
            .ok_or_else(|| invalid("invalid GPT-Live delegation offset"))?;
        if self.claimed.contains(delegate) {
            return Ok(());
        }
        if self.claimed.len() >= MAX_TURNS {
            return Err(invalid("GPT-Live delegation capacity exceeded"));
        }
        self.claimed.insert(delegate.into());
        if let Some(window) = self.turns.values_mut().find(|window| {
            window.start <= offset
                && offset <= window.end + MATCH_TAIL_MS
                && now < window.valid_until
                && window.delegation.is_none()
        }) {
            window.delegation = Some(delegate.into());
        }
        Ok(())
    }

    fn commentary(&mut self, id: u64, text: String) {
        if let Some(turn) = self.turns.get_mut(&id)
            && turn.commentary.len() < 4
        {
            turn.commentary.push(text);
        }
    }

    fn flush(&mut self, socket: &mut WebSocket<impl Read + Write>, now: Instant) -> io::Result<()> {
        for window in self.turns.values_mut() {
            if let Some(delegation) = &window.delegation {
                for text in window.commentary.drain(..) {
                    write(
                        socket,
                        json!({"type":"session.commentary.append","content":text,
                        "event_id":crate::supervisor::runtime::random_id()?,"delegation_id":delegation}),
                    )?;
                }
            } else if now >= window.valid_until {
                window.commentary.clear();
            }
        }
        Ok(())
    }
}

fn startup(
    socket: &mut WebSocket<impl Read + Write>,
    deadline: Instant,
    cancel: &RenderCancel,
) -> io::Result<()> {
    let config = json!({"model":"gpt-live-1",
        "instructions":"Delegate each complete caller request to the client. Never perform work or invent results or confirmations.",
        "audio":{"format":{"type":"audio/pcm","rate":24000},"output":{"voice":"marin"}},
        "delegation":{"type":"client"}});
    write(socket, json!({"type":"session.start","session":config}))?;
    while Instant::now() < deadline && !cancel.canceled() {
        if let Some(event) = read(socket)? {
            let session = &event["session"];
            if event["type"] == "session.started"
                && coordinate(&session["id"]).is_some()
                && session["status"] == "active"
                && session["model"] == config["model"]
                && session["audio"]["format"] == config["audio"]["format"]
                && session["audio"]["output"]["voice"] == config["audio"]["output"]["voice"]
                && session["delegation"]["type"] == "client"
            {
                return Ok(());
            }
            break;
        }
    }
    Err(invalid("GPT-Live did not confirm client delegation"))
}

fn pump(
    socket: &mut WebSocket<impl Read + Write>,
    receive: mpsc::Receiver<Command>,
    cancel: &RenderCancel,
) -> io::Result<()> {
    let mut delegates = Delegations::default();
    while !cancel.canceled() {
        match receive.try_recv() {
            Ok(Command::Turn(id, pcm)) => {
                delegates.record(id, &pcm, Instant::now())?;
                for chunk in pcm.chunks(AUDIO_SECOND) {
                    write(
                        socket,
                        json!({"type":"session.input_audio.append","audio":STANDARD.encode(chunk)}),
                    )?;
                }
                let padding = vec![0_u8; (PADDING_MS * 48) as usize];
                write(
                    socket,
                    json!({"type":"session.input_audio.append","audio":STANDARD.encode(padding)}),
                )?;
            }
            Ok(Command::Commentary(id, text)) => delegates.commentary(id, text),
            Err(mpsc::TryRecvError::Empty) => {}
            Err(mpsc::TryRecvError::Disconnected) => return Ok(()),
        }
        if let Some(event) = read(socket)? {
            delegates.event(&event, Instant::now())?;
        }
        delegates.flush(socket, Instant::now())?;
    }
    Ok(())
}

fn coordinate(value: &Value) -> Option<&str> {
    value
        .as_str()
        .filter(|value| (1..=512).contains(&value.len()) && !value.chars().any(char::is_control))
}

fn write(socket: &mut WebSocket<impl Read + Write>, value: Value) -> io::Result<()> {
    socket
        .send(Message::Text(value.to_string().into()))
        .map_err(|_| invalid("GPT-Live write failed"))
}

fn read(socket: &mut WebSocket<impl Read + Write>) -> io::Result<Option<Value>> {
    match socket.read() {
        Ok(Message::Text(payload)) => serde_json::from_str(&payload)
            .map(Some)
            .map_err(|_| invalid("invalid GPT-Live event")),
        Ok(Message::Ping(_)) => {
            socket
                .flush()
                .map_err(|_| invalid("GPT-Live pong failed"))?;
            Ok(None)
        }
        Ok(Message::Pong(_)) => Ok(None),
        Err(tungstenite::Error::Io(err))
            if matches!(
                err.kind(),
                io::ErrorKind::WouldBlock | io::ErrorKind::TimedOut | io::ErrorKind::Interrupted
            ) =>
        {
            Ok(None)
        }
        _ => Err(invalid("GPT-Live read failed")),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::os::unix::net::UnixStream;

    fn delegation(event: &str, id: &str, offset: u64) -> Value {
        json!({"type":"session.delegation.created","event_id":event,"offset_ms":offset,
            "delegation":{"id":id,"target":"client"}})
    }

    #[test]
    fn delayed_and_duplicate_delegations_never_move_an_earlier_turn() {
        let now = Instant::now();
        let mut state = Delegations::default();
        state.record(1, &[0; 960], now).unwrap();
        state
            .record(2, &[0; 960], now + Duration::from_secs(1))
            .unwrap();
        let late = now + Duration::from_millis(2300);
        state
            .event(&delegation("old-event", "old", 10), late)
            .unwrap();
        state
            .event(&delegation("new-event", "fresh", 940), late)
            .unwrap();
        state
            .event(&delegation("different-id", "fresh", 20), late)
            .unwrap();
        assert!(state.turns[&1].delegation.is_none());
        assert_eq!(state.turns[&2].delegation.as_deref(), Some("fresh"));
        state
            .event(
                &json!({"type":"session.output_audio.delta","delta":"AA=="}),
                late,
            )
            .unwrap();
        assert!(state.turns[&1].commentary.is_empty());
        assert!(
            state
                .event(&json!({"type":"session.closed","reason":"expired"}), late)
                .is_err()
        );
    }

    #[test]
    fn wire_start_audio_and_scoped_commentary_cross_one_private_socket() {
        let (left, right) = UnixStream::pair().unwrap();
        left.set_read_timeout(Some(Duration::from_millis(50)))
            .unwrap();
        right
            .set_read_timeout(Some(Duration::from_secs(3)))
            .unwrap();
        let (send, receive) = mpsc::sync_channel(4);
        let worker = std::thread::spawn(move || {
            let (mut socket, _) = tungstenite::client("ws://fixture.invalid", left).unwrap();
            let cancel = RenderCancel::default();
            startup(
                &mut socket,
                Instant::now() + Duration::from_secs(3),
                &cancel,
            )
            .unwrap();
            assert!(pump(&mut socket, receive, &cancel).is_err());
        });
        let mut peer = tungstenite::accept(right).unwrap();
        let start = read(&mut peer).unwrap().unwrap();
        assert_eq!(start["session"]["delegation"]["type"], "client");
        let mut config = start["session"].clone();
        config["id"] = json!("session-fixture");
        config["status"] = json!("active");
        write(
            &mut peer,
            json!({"type":"session.started","session":config}),
        )
        .unwrap();
        send.send(Command::Turn(1, vec![2; 960])).unwrap();
        send.send(Command::Turn(2, vec![3; 960])).unwrap();
        for length in [
            960,
            (PADDING_MS * 48) as usize,
            960,
            (PADDING_MS * 48) as usize,
        ] {
            let audio = read(&mut peer).unwrap().unwrap();
            assert_eq!(audio["type"], "session.input_audio.append");
            assert_eq!(
                STANDARD
                    .decode(audio["audio"].as_str().unwrap())
                    .unwrap()
                    .len(),
                length
            );
        }
        write(&mut peer, delegation("e2", "later", 925)).unwrap();
        write(&mut peer, delegation("e1", "first", 20)).unwrap();
        send.send(Command::Commentary(2, "second verified result".into()))
            .unwrap();
        send.send(Command::Commentary(1, "first verified result".into()))
            .unwrap();
        for (id, body) in [
            ("later", "second verified result"),
            ("first", "first verified result"),
        ] {
            let commentary = read(&mut peer).unwrap().unwrap();
            assert_eq!(commentary["delegation_id"], id);
            assert_eq!(commentary["content"], body);
        }
        write(
            &mut peer,
            json!({"type":"session.closed","reason":"expired"}),
        )
        .unwrap();
        worker.join().unwrap();
    }
}
