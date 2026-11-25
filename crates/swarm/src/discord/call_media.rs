//! Private transport for the pinned DAVE helper. No media or credentials leave this module's pipes.
use super::call_helper::Helper;
use super::config::{invalid, snowflake};
use serde_json::{Value, json};
use std::io::{self, Write};
use std::os::fd::OwnedFd;
use std::os::unix::net::UnixStream;
use std::os::unix::process::CommandExt;
use std::process::{Child, Stdio};
use std::time::Duration;

const MAX_LINE: usize = 8192;
const PCM_ENCODED: usize = 1280;

pub(super) struct Media {
    child: Child,
    pipe: UnixStream,
    buffered: Vec<u8>,
    guild: String,
    room: String,
    speakers: Vec<String>,
    bot: String,
    state: Option<Value>,
    server: Option<Value>,
    joined: bool,
    ready: bool,
}

pub(super) enum Event {
    Join,
    Ready,
    Audio { speaker: String, pcm: String },
}

impl Media {
    pub(super) fn start(
        helper: &Helper,
        guild: &str,
        room: &str,
        bot: &str,
        speakers: Vec<String>,
    ) -> io::Result<Self> {
        if ![guild, room, bot].into_iter().all(snowflake)
            || !(1..=16).contains(&speakers.len())
            || speakers.iter().enumerate().any(|(i, speaker)| {
                !snowflake(speaker) || speaker == bot || speakers[..i].contains(speaker)
            })
        {
            return Err(invalid("invalid call helper scope"));
        }
        let (pipe, child_pipe) = UnixStream::pair()?;
        pipe.set_write_timeout(Some(Duration::from_millis(50)))?;
        let stdin: OwnedFd = child_pipe.try_clone()?.into();
        let stdout: OwnedFd = child_pipe.into();
        let child = helper
            .command()?
            .env_clear()
            .process_group(0)
            .stdin(Stdio::from(stdin))
            .stdout(Stdio::from(stdout))
            .stderr(Stdio::null())
            .spawn()
            .map_err(|_| invalid("could not start call helper"))?;
        let mut media = Self {
            child,
            pipe,
            buffered: Vec::new(),
            guild: guild.into(),
            room: room.into(),
            speakers,
            bot: bot.into(),
            state: None,
            server: None,
            joined: false,
            ready: false,
        };
        media.send(json!({"type":"start", "guildId":guild, "channelId":room,
            "botId":bot, "speakers":media.speakers}))?;
        Ok(media)
    }

    fn send(&mut self, value: Value) -> io::Result<()> {
        let mut line = serde_json::to_vec(&value)?;
        if line.len() > MAX_LINE {
            return Err(invalid("call helper command exceeds pipe limit"));
        }
        line.push(b'\n');
        match self.pipe.write(&line) {
            Ok(written) if written == line.len() => Ok(()),
            _ => Err(invalid("call helper pipe write failed")),
        }
    }

    pub(super) fn play(&mut self, pcm: &str) -> io::Result<()> {
        if !self.ready || pcm.len() != PCM_ENCODED {
            return Err(invalid("call playback is outside the ready media session"));
        }
        self.send(json!({"type":"play", "pcm":pcm}))
    }

    pub(super) fn silence(&mut self) -> io::Result<()> {
        self.send(json!({"type":"clear"}))
    }

    pub(super) fn gateway(&mut self, event: &Value) -> io::Result<()> {
        let data = &event["d"];
        if !self.joined || data["guild_id"] != self.guild {
            return Err(invalid("voice dispatch is outside the pending join"));
        }
        let (kind, slot, payload) = match event["t"].as_str() {
            Some("VOICE_STATE_UPDATE")
                if data["user_id"] == self.bot
                    && data["channel_id"] == self.room
                    && ["self_mute", "self_deaf", "mute", "deaf", "suppress"]
                        .into_iter()
                        .all(|key| data[key] == false)
                    && credential(&data["session_id"]) =>
            {
                (
                    "voice_state",
                    &mut self.state,
                    json!({
                        "guild_id": self.guild, "user_id": self.bot, "channel_id": self.room,
                        "self_mute": false, "self_deaf": false, "mute": false, "deaf": false,
                        "session_id": data["session_id"],
                    }),
                )
            }
            Some("VOICE_SERVER_UPDATE")
                if credential(&data["token"]) && endpoint(&data["endpoint"]) =>
            {
                (
                    "voice_server",
                    &mut self.server,
                    json!({
                        "guild_id": self.guild, "endpoint": data["endpoint"], "token": data["token"],
                    }),
                )
            }
            _ => return Err(invalid("invalid Discord voice session material")),
        };
        if let Some(old) = slot {
            return if *old == payload {
                Ok(())
            } else {
                Err(invalid("Discord voice session material changed"))
            };
        }
        *slot = Some(payload.clone());
        self.send(json!({"type":kind, "data":payload}))
    }

    pub(super) fn poll(&mut self) -> io::Result<Option<Event>> {
        // Observe the exit without releasing the PID before group teardown.
        let mut status: libc::siginfo_t = unsafe { std::mem::zeroed() };
        let rc = unsafe {
            libc::waitid(
                libc::P_PID,
                self.child.id(),
                &mut status,
                libc::WEXITED | libc::WNOHANG | libc::WNOWAIT,
            )
        };
        if rc != 0 || unsafe { status.si_pid() } != 0 {
            return Err(invalid("call helper exited"));
        }
        if !self.buffered.contains(&b'\n') {
            let mut chunk = [0_u8; MAX_LINE + 1];
            // MSG_DONTWAIT leaves the separate bounded write behavior intact.
            use std::os::fd::AsRawFd;
            let n = unsafe {
                libc::recv(
                    self.pipe.as_raw_fd(),
                    chunk.as_mut_ptr().cast(),
                    chunk.len(),
                    libc::MSG_DONTWAIT,
                )
            };
            if n < 0 {
                let err = io::Error::last_os_error();
                return if err.kind() == io::ErrorKind::WouldBlock {
                    Ok(None)
                } else {
                    Err(invalid("call helper pipe read failed"))
                };
            }
            if n == 0 {
                return Err(invalid("call helper pipe closed"));
            }
            self.buffered.extend_from_slice(&chunk[..n as usize]);
        }
        let split = self.buffered.iter().position(|&b| b == b'\n');
        if split.unwrap_or(self.buffered.len()) > MAX_LINE {
            return Err(invalid("call helper event exceeds pipe limit"));
        }
        let Some(split) = split else { return Ok(None) };
        let event: Value = serde_json::from_slice(&self.buffered[..split])
            .map_err(|_| invalid("invalid call helper event"))?;
        self.buffered.drain(..=split);
        self.validate(event).map(Some)
    }

    fn validate(&mut self, event: Value) -> io::Result<Event> {
        match event["type"].as_str() {
            Some("gateway")
                if !self.joined
                    && event["data"]["op"] == 4
                    && event["data"]["d"]["guild_id"] == self.guild
                    && event["data"]["d"]["channel_id"] == self.room
                    && event["data"]["d"]["self_mute"] == false
                    && event["data"]["d"]["self_deaf"] == false =>
            {
                self.joined = true;
                Ok(Event::Join)
            }
            Some("ready")
                if self.joined && self.state.is_some() && self.server.is_some() && !self.ready =>
            {
                self.ready = true;
                Ok(Event::Ready)
            }
            Some("audio") if self.ready => {
                let speaker = event["userId"].as_str().unwrap_or_default();
                let pcm = event["pcm"].as_str().unwrap_or_default();
                if !self.speakers.iter().any(|id| id == speaker)
                    || pcm.len() != PCM_ENCODED
                    || !pcm
                        .bytes()
                        .all(|b| b.is_ascii_alphanumeric() || b == b'+' || b == b'/')
                {
                    return Err(invalid("call helper returned unscoped audio"));
                }
                Ok(Event::Audio {
                    speaker: speaker.into(),
                    pcm: pcm.into(),
                })
            }
            Some("fault") => Err(invalid("call helper reported media loss")),
            _ => Err(invalid("invalid call helper event order or scope")),
        }
    }
}

fn credential(value: &Value) -> bool {
    value.as_str().is_some_and(|value| {
        (1..=1024).contains(&value.len()) && value.bytes().all(|b| b.is_ascii_graphic())
    })
}

fn endpoint(value: &Value) -> bool {
    value.as_str().is_some_and(|value| {
        let host = value.strip_suffix(":443").unwrap_or(value);
        (1..=253).contains(&host.len())
            && host
                .bytes()
                .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'.')
    })
}

impl Drop for Media {
    fn drop(&mut self) {
        let _ = self.pipe.shutdown(std::net::Shutdown::Both);
        // The group leader remains reserved until wait(), including after exit.
        let _ = unsafe { libc::kill(-(self.child.id() as libc::pid_t), libc::SIGKILL) };
        let _ = self.child.wait();
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;
    use std::os::unix::fs::PermissionsExt;
    use std::time::Instant;

    fn wait(media: &mut Media) -> io::Result<Event> {
        let limit = Instant::now() + Duration::from_secs(2);
        loop {
            if let Some(event) = media.poll()? {
                return Ok(event);
            }
            assert!(Instant::now() < limit, "helper did not emit an event");
            std::thread::sleep(Duration::from_millis(5));
        }
    }

    #[test]
    #[ignore = "needs Node 24.17 and npm ci for the pinned bundle; uses only the private pipe"]
    fn exact_pinned_helper_runs_with_descendant_permission_removed() {
        let root = std::path::PathBuf::from(std::env::var_os("SWARM_TEST_HELPER_ROOT").unwrap());
        let node = fs::canonicalize(std::env::var_os("SWARM_TEST_HELPER_NODE").unwrap()).unwrap();
        let helper = Helper::pinned(&root.join("swarm-discord-voice"), node.clone()).unwrap();
        let mut media = Media::start(&helper, "1", "2", "3", vec!["4".into()]).unwrap();
        assert!(matches!(wait(&mut media).unwrap(), Event::Join));
        drop(media);
        let denied = std::process::Command::new(node)
            .args(["--permission","--allow-addons","--eval",
                "try {require('node:child_process').spawn('/bin/true'); process.exit(1)} catch(e) {process.exit(e.code==='ERR_ACCESS_DENIED'?0:2)}"])
            .env_clear().output().unwrap();
        assert!(
            denied.status.success(),
            "node permitted detached process creation"
        );
    }

    #[test]
    fn reaps_group_even_after_leader_exits_with_inherited_media_pipe() {
        let dir = std::env::temp_dir().join(format!("swarm-call-group-{}", std::process::id()));
        fs::create_dir(&dir).unwrap();
        let helper = dir.join("helper");
        let pidfile = dir.join("child");
        let script = format!(
            "#!/bin/sh\n/bin/sleep 30 &\nprintf '%s' \"$!\" > '{}'\nexit 0\n",
            pidfile.display()
        );
        fs::write(&helper, script).unwrap();
        fs::set_permissions(&helper, fs::Permissions::from_mode(0o700)).unwrap();
        let mut media =
            Media::start(&Helper::Fixture(helper), "1", "2", "3", vec!["4".into()]).unwrap();
        assert!(matches!(wait(&mut media),Err(err) if err.to_string() == "call helper exited"));
        let pid: libc::pid_t = fs::read_to_string(pidfile).unwrap().parse().unwrap();
        drop(media);
        let deadline = Instant::now() + Duration::from_secs(2);
        loop {
            if unsafe { libc::kill(pid, 0) } == -1
                && io::Error::last_os_error().raw_os_error() == Some(libc::ESRCH)
            {
                break;
            }
            #[cfg(target_os = "linux")]
            if fs::read_to_string(format!("/proc/{pid}/stat"))
                .ok()
                .and_then(|stat| {
                    stat.rsplit_once(") ")
                        .map(|(_, rest)| rest.starts_with("Z"))
                })
                == Some(true)
            {
                break;
            }
            assert!(
                Instant::now() < deadline,
                "helper descendant survived revocation"
            );
            std::thread::sleep(Duration::from_millis(5));
        }
        fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn process_pipe_rejects_unscoped_and_oversized_events() {
        let dir = std::env::temp_dir().join(format!("swarm-call-media-{}", std::process::id()));
        fs::create_dir(&dir).unwrap();
        let helper = dir.join("helper");
        let gateway = r#"{"type":"gateway","data":{"op":4,"d":{"guild_id":"1","channel_id":"2","self_deaf":false,"self_mute":false}}}"#;
        for (output, error) in [
            (
                format!(
                    "{{\"type\":\"audio\",\"userId\":\"9\",\"pcm\":\"{}\"}}",
                    "A".repeat(1280)
                ),
                "call helper returned unscoped audio",
            ),
            (
                "x".repeat(MAX_LINE + 1),
                "call helper event exceeds pipe limit",
            ),
        ] {
            fs::write(&helper, format!(
                "#!/bin/sh\nread -r start\n[ \"$start\" = '{{\"type\":\"start\",\"guildId\":\"1\",\"channelId\":\"2\",\"botId\":\"3\",\"speakers\":[\"4\"]}}' ] || exit 1\nprintf '%s\\n' '{gateway}'\nread -r first\nread -r second\nprintf '%s\\n' '{{\"type\":\"ready\"}}' \"{{\\\"type\\\":\\\"audio\\\",\\\"userId\\\":\\\"4\\\",\\\"pcm\\\":\\\"{}\\\"}}\" '{output}'\nread -r stop\n",
                "A".repeat(1280),
            )).unwrap();
            fs::set_permissions(&helper, fs::Permissions::from_mode(0o700)).unwrap();
            let mut media = Media::start(
                &Helper::Fixture(helper.clone()),
                "1",
                "2",
                "3",
                vec!["4".into()],
            )
            .unwrap();
            assert!(matches!(wait(&mut media).unwrap(), Event::Join));
            let state = json!({"t":"VOICE_STATE_UPDATE","d":{"guild_id":"1","channel_id":"2",
                "user_id":"3","session_id":"test-session","self_mute":false,
                "self_deaf":false,"mute":false,"deaf":false,"suppress":false}});
            let server = json!({"t":"VOICE_SERVER_UPDATE","d":{"guild_id":"1",
                "endpoint":"example.invalid:443","token":"test-token"}});
            let halves = if error.contains("limit") {
                [&state, &server]
            } else {
                [&server, &state]
            };
            for event in halves {
                media.gateway(event).unwrap();
            }
            media.gateway(&state).unwrap();

            assert!(matches!(wait(&mut media).unwrap(), Event::Ready));
            assert!(
                matches!(wait(&mut media).unwrap(), Event::Audio { speaker, pcm }
                if speaker == "4" && pcm.len() == 1280)
            );
            assert!(matches!(wait(&mut media), Err(e) if e.to_string() == error));
            let pid = media.child.id();
            drop(media);
            assert_eq!(unsafe { libc::kill(pid as i32, 0) }, -1);
        }
        fs::remove_dir_all(dir).unwrap();
    }
}
