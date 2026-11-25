//! The Gateway startup handshake is shared by connector verification and replay tests.
use super::config::{Config, invalid, load_token};
use serde_json::{Value, json};
use std::io::{self, Read, Write};
use std::net::{TcpStream, ToSocketAddrs};
use std::path::Path;
use std::time::{Duration, Instant};
use tungstenite::{Message, WebSocket, client_tls_with_config, protocol::WebSocketConfig};

const HOST: &str = "gateway.discord.gg";
const URL: &str = "wss://gateway.discord.gg/?v=10&encoding=json";
const MAX_FRAME: usize = 1 << 20;
const TIMEOUT: Duration = Duration::from_secs(5);

pub(super) fn probe(config: &Config) -> io::Result<&'static str> {
    let (mut socket, _) = open(config)?;
    let _ = socket.close(None);
    Ok("message intents accepted")
}

pub(super) type Connection = WebSocket<tungstenite::stream::MaybeTlsStream<TcpStream>>;

pub(super) fn open(config: &Config) -> io::Result<(Connection, Cursor)> {
    let token = load_token(Path::new(config.text("token_file")))?;
    let timeout = Instant::now() + TIMEOUT;
    let mut stream = None;
    for address in (HOST, 443).to_socket_addrs()? {
        let remaining = timeout.saturating_duration_since(Instant::now());
        if remaining.is_zero() {
            break;
        }
        if let Ok(socket) = TcpStream::connect_timeout(&address, remaining) {
            stream = Some(socket);
            break;
        }
    }
    let stream = stream.ok_or_else(|| io::Error::other("Discord Gateway TCP connect failed"))?;
    stream.set_read_timeout(Some(Duration::from_secs(1)))?;
    stream.set_write_timeout(Some(TIMEOUT))?;
    let limits = WebSocketConfig::default()
        .max_message_size(Some(MAX_FRAME))
        .max_frame_size(Some(MAX_FRAME));
    let (mut socket, _) = client_tls_with_config(URL, stream, Some(limits), None)
        .map_err(|_| io::Error::other("Discord Gateway TLS or WebSocket handshake failed"))?;
    let cursor = ready(&mut socket, config, &token)?;
    if config.0["calls"].is_object() {
        use tungstenite::stream::MaybeTlsStream;
        match socket.get_mut() {
            MaybeTlsStream::Plain(stream) => {
                stream.set_read_timeout(Some(Duration::from_millis(20)))?
            }
            MaybeTlsStream::Rustls(stream) => stream
                .sock
                .set_read_timeout(Some(Duration::from_millis(20)))?,
            _ => return Err(invalid("unsupported Gateway transport for calls")),
        }
    }
    Ok((socket, cursor))
}

pub(super) struct Cursor {
    sequence: Value,
    interval: Duration,
    next: Instant,
    awaiting: bool,
}

impl Cursor {
    pub(super) fn sequence(&self) -> u64 {
        self.sequence.as_u64().unwrap_or(0)
    }

    pub(super) fn event(
        &mut self,
        socket: &mut WebSocket<impl Read + Write>,
    ) -> io::Result<Option<Value>> {
        if Instant::now() >= self.next {
            if self.awaiting {
                return Err(io::Error::other(
                    "Discord Gateway did not acknowledge heartbeat",
                ));
            }
            send(socket, json!({"op": 1, "d": self.sequence}))?;
            self.awaiting = true;
            self.next = Instant::now() + self.interval;
        }
        let Some(event) = read(socket)? else {
            return Ok(None);
        };
        if event["op"] == 11 {
            self.awaiting = false;
        } else if event["op"] == 1 {
            send(socket, json!({"op": 1, "d": self.sequence}))?;
        } else if event["op"] == 7 || event["op"] == 9 {
            return Err(io::Error::other(
                "Discord Gateway session interrupted; history repair is required",
            ));
        } else if event["op"] == 0 {
            let current = event["s"]
                .as_u64()
                .ok_or_else(|| invalid("Discord dispatch lacks sequence"))?;
            let previous = self
                .sequence
                .as_u64()
                .ok_or_else(|| invalid("Discord READY lacks sequence"))?;
            if current <= previous {
                return Ok(None);
            }
            if previous.checked_add(1) != Some(current) {
                return Err(invalid(
                    "Discord Gateway sequence gap; history repair is required",
                ));
            }
            self.sequence = event["s"].clone();
            return Ok(Some(event));
        }
        Ok(None)
    }
}

pub(super) fn ready(
    socket: &mut WebSocket<impl Read + Write>,
    config: &Config,
    token: &str,
) -> io::Result<Cursor> {
    let timeout = Instant::now() + TIMEOUT;
    let hello = next(socket, timeout)?;
    let interval = hello["d"]["heartbeat_interval"]
        .as_u64()
        .filter(|ms| *ms > 0 && *ms <= 120_000)
        .ok_or_else(|| invalid("invalid Discord Gateway heartbeat interval"))?;
    if hello["op"] != 10 {
        return Err(invalid("Discord Gateway did not send Hello"));
    }
    // Personal DMs need only DIRECT_MESSAGES. Guild installations require
    // GUILDS, GUILD_MESSAGES, DIRECT_MESSAGES and MESSAGE_CONTENT.
    let calls = config.0["calls"].is_object() && config.text("mode") == "personal";
    let intents = if config.text("mode") == "personal" {
        1 << 12 | if calls { 1 | 1 << 7 } else { 0 }
    } else {
        1 | (1 << 9) | (1 << 12) | (1 << 15)
    };
    send(
        socket,
        json!({"op": 2, "d": {
            "token": token, "intents": intents,
            "properties": {"os": std::env::consts::OS, "browser": "swarm", "device": "swarm"}
        }}),
    )?;
    let mut sequence = Value::Null;
    let mut last_heartbeat = Instant::now();
    loop {
        let event = next(socket, timeout)?;
        if event["s"].is_u64() {
            sequence = event["s"].clone();
        }
        if event["op"] == 0 && event["t"] == "READY" {
            if event["d"]["application"]["id"] != config.text("application_id")
                || event["d"]["user"]["id"] != config.text("application_id")
                || event["d"]["user"]["bot"] != true
            {
                return Err(invalid("Discord Gateway returned a different bot identity"));
            }
            if !event["s"].is_u64() {
                return Err(invalid("Discord READY lacks sequence"));
            }
            return Ok(Cursor {
                sequence,
                interval: Duration::from_millis(interval),
                next: last_heartbeat + Duration::from_millis(interval),
                awaiting: false,
            });
        }
        if event["op"] == 7 || event["op"] == 9 {
            return Err(io::Error::other("Discord Gateway requested a new session"));
        }
        if event["op"] == 1 || last_heartbeat.elapsed() >= Duration::from_millis(interval) {
            send(socket, json!({"op": 1, "d": sequence}))?;
            last_heartbeat = Instant::now();
        }
    }
}

fn next(socket: &mut WebSocket<impl Read + Write>, timeout: Instant) -> io::Result<Value> {
    while Instant::now() < timeout {
        if let Some(event) = read(socket)? {
            return Ok(event);
        }
    }
    Err(io::Error::new(
        io::ErrorKind::TimedOut,
        "Discord Gateway startup timed out",
    ))
}

fn read(socket: &mut WebSocket<impl Read + Write>) -> io::Result<Option<Value>> {
    match socket.read() {
        Ok(Message::Text(body)) => serde_json::from_str(&body)
            .map(Some)
            .map_err(|_| invalid("invalid Discord Gateway event")),
        Ok(Message::Ping(_)) => {
            socket
                .flush()
                .map_err(|_| io::Error::other("Discord Gateway pong failed"))?;
            Ok(None)
        }
        Ok(Message::Pong(_)) => Ok(None),
        Ok(Message::Close(frame)) => {
            let code = frame.map(|close| u16::from(close.code)).unwrap_or(1005);
            Err(io::Error::other(format!(
                "Discord Gateway closed with code {code}"
            )))
        }
        Err(tungstenite::Error::Io(err))
            if matches!(
                err.kind(),
                io::ErrorKind::WouldBlock | io::ErrorKind::TimedOut | io::ErrorKind::Interrupted
            ) =>
        {
            Ok(None)
        }
        _ => Err(io::Error::other("Discord Gateway event read failed")),
    }
}

pub(super) fn send(socket: &mut WebSocket<impl Read + Write>, event: Value) -> io::Result<()> {
    socket
        .send(Message::text(event.to_string()))
        .map_err(|_| io::Error::other("Discord Gateway event write failed"))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::os::unix::net::UnixStream;

    #[test]
    fn synthetic_personal_gateway_verifies_bot_and_only_requests_dm_intent() {
        for ready_id in ["123", "789"] {
            let (client, server) = UnixStream::pair().unwrap();
            let server = std::thread::spawn(move || {
                let mut ws = tungstenite::accept(server).unwrap();
                send(&mut ws, json!({"op":10, "d":{"heartbeat_interval":45000}})).unwrap();
                let identify = next(&mut ws, Instant::now() + TIMEOUT).unwrap();
                assert_eq!(identify["op"], 2);
                assert_eq!(identify["d"]["intents"], 1 << 12);
                assert_eq!(identify["d"]["token"], "fixture");
                send(&mut ws, json!({"op":1, "d":null})).unwrap();
                let heartbeat = next(&mut ws, Instant::now() + TIMEOUT).unwrap();
                assert_eq!(heartbeat, json!({"op":1, "d":null}));
                send(&mut ws, json!({"op":11})).unwrap();
                send(
                    &mut ws,
                    json!({"op":0, "s":1, "t":"READY", "d":{"application":{"id":ready_id},"user":{"id":ready_id,"bot":true}}}),
                )
                .unwrap();
            });
            let (mut ws, _) = tungstenite::client("ws://localhost", client).unwrap();
            let config = Config(json!({"mode":"personal","application_id":"123"}));
            assert_eq!(
                ready(&mut ws, &config, "fixture").is_ok(),
                ready_id == "123"
            );
            server.join().unwrap();
        }
    }
}
