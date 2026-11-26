use std::{
    io,
    path::{Path, PathBuf},
};

pub(super) use crate::service_unit::Platform;
use crate::service_unit::{systemd_string, xml_text};

fn safe(path: &Path) -> bool {
    path.is_absolute()
        && path
            .to_str()
            .is_some_and(|s| !s.chars().any(char::is_control))
}

fn invalid() -> io::Error {
    io::Error::new(io::ErrorKind::InvalidInput, "discord: invalid service path")
}

pub(super) fn path(platform: Platform, home: &Path) -> io::Result<PathBuf> {
    if !safe(home) {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            "discord: invalid home path",
        ));
    }
    Ok(match platform {
        Platform::Launchd => home.join("Library/LaunchAgents/com.swarm.discord.plist"),
        Platform::Systemd => home.join(".config/systemd/user/swarm-discord.service"),
    })
}

pub(super) fn render(platform: Platform, binary: &Path, place: &Path) -> io::Result<String> {
    let log = place.join("connector.log");
    if !safe(binary) || !safe(place) || !safe(&log) {
        return Err(invalid());
    }
    let b = binary.to_str().ok_or_else(invalid)?;
    let p = place.to_str().ok_or_else(invalid)?;
    let l = log.to_str().ok_or_else(invalid)?;
    Ok(match platform {
        Platform::Launchd => format!(
            "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\"><dict>\n<key>Label</key><string>com.swarm.discord</string>\n<key>ProgramArguments</key><array><string>{}</string><string>discord</string><string>run</string><string>--state-dir</string><string>{}</string><string>--unattended</string></array>\n<key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>ThrottleInterval</key><integer>10</integer>\n<key>StandardOutPath</key><string>{}</string><key>StandardErrorPath</key><string>{}</string>\n</dict></plist>\n",
            xml_text(b),
            xml_text(p),
            xml_text(l),
            xml_text(l)
        ),
        Platform::Systemd => {
            if l.trim_end() != l || l.ends_with('\\') {
                return Err(io::Error::new(
                    io::ErrorKind::InvalidInput,
                    "discord: invalid log path",
                ));
            }
            format!(
                "[Unit]\nDescription=Swarm Discord\nStartLimitIntervalSec=0\n[Service]\nExecStart=:{} discord run --state-dir {} --unattended\nRestart=always\nRestartSec=10\nStandardOutput=append:{}\nStandardError=append:{}\n[Install]\nWantedBy=default.target\n",
                systemd_quote(b),
                systemd_quote(p),
                l.replace('%', "%%"),
                l.replace('%', "%%")
            )
        }
    })
}

fn systemd_quote(value: &str) -> String {
    format!("\"{}\"", systemd_string(value))
}

pub(super) fn bound_place(platform: Platform, body: &str) -> io::Result<PathBuf> {
    let (binary, place) = match platform {
        Platform::Launchd => {
            let line = body
                .lines()
                .find_map(|line| line.strip_prefix("<key>ProgramArguments</key><array><string>"))
                .ok_or_else(invalid)?;
            let (binary, rest) = line.split_once("</string><string>discord</string><string>run</string><string>--state-dir</string><string>").ok_or_else(invalid)?;
            let place = rest
                .strip_suffix("</string><string>--unattended</string></array>")
                .ok_or_else(invalid)?;
            (xml_unescape(binary)?, xml_unescape(place)?)
        }
        Platform::Systemd => {
            let line = body
                .lines()
                .find_map(|line| line.strip_prefix("ExecStart=:"))
                .ok_or_else(invalid)?;
            let (binary, rest) = argument(line)?;
            let (place, rest) = argument(
                rest.strip_prefix(" discord run --state-dir ")
                    .ok_or_else(invalid)?,
            )?;
            if rest != " --unattended" {
                return Err(invalid());
            }
            (binary, place)
        }
    };
    let place = PathBuf::from(place);
    if render(platform, Path::new(&binary), &place)? != body {
        return Err(invalid());
    }
    Ok(place)
}

fn xml_unescape(mut text: &str) -> io::Result<String> {
    let mut value = String::new();
    while let Some((head, tail)) = text.split_once('&') {
        value.push_str(head);
        let (entity, rest) = tail.split_once(';').ok_or_else(invalid)?;
        value.push(match entity {
            "amp" => '&',
            "lt" => '<',
            "gt" => '>',
            "#34" => '"',
            "#39" => '\'',
            _ => return Err(invalid()),
        });
        text = rest;
    }
    value.push_str(text);
    Ok(value)
}

fn argument(text: &str) -> io::Result<(String, &str)> {
    let mut chars = text.strip_prefix('"').ok_or_else(invalid)?.chars();
    let mut value = String::new();
    while let Some(ch) = chars.next() {
        match ch {
            '"' => return Ok((value, chars.as_str())),
            '\\' => match chars.next() {
                Some(escaped @ ('\\' | '"')) => value.push(escaped),
                _ => return Err(invalid()),
            },
            '%' if chars.next() != Some('%') => return Err(invalid()),
            other => value.push(other),
        }
    }
    Err(invalid())
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn render_and_recognize_stock_units() {
        for platform in [Platform::Launchd, Platform::Systemd] {
            let body = render(
                platform,
                Path::new("/path with space/swarm"),
                Path::new("/state & space"),
            )
            .unwrap();
            assert_eq!(
                bound_place(platform, &body).unwrap(),
                Path::new("/state & space")
            );
            assert!(body.contains("connector.log"));
            assert!(!body.contains("/bin/sh"));
            assert!(bound_place(platform, &format!("{body}evil")).is_err());
        }
    }
    #[test]
    fn native_unit_bytes_match_the_pinned_go_renderer() {
        use sha2::{Digest, Sha256};
        let exe = Path::new(r#"/t/agency's "p" & 50%"#);
        let place = Path::new(r#"/t/quote &amp; "x" %back\place"#);
        for (platform, digest) in [
            (
                Platform::Launchd,
                "3055cf9b48ef83fc61271df05a877d9172253847912eb1d4a142c127de184e54",
            ),
            (
                Platform::Systemd,
                "ee31bc9899454cf9f89b97f028c624767e87c80aabf34cea122c42b0c4a0d198",
            ),
        ] {
            let body = render(platform, exe, place).unwrap();
            assert_eq!(format!("{:x}", Sha256::digest(body.as_bytes())), digest);
            assert_eq!(bound_place(platform, &body).unwrap(), place);
        }
    }

    #[test]
    fn rejects_unsafe_paths_and_foreign_bodies() {
        assert!(path(Platform::Launchd, Path::new("relative")).is_err());
        assert!(
            render(
                Platform::Systemd,
                Path::new("/bin/swarm\nExecStart=bad"),
                Path::new("/state")
            )
            .is_err()
        );
        assert!(bound_place(Platform::Systemd, "[Service]\nExecStart=/evil\n").is_err());
        assert!(
            render(
                Platform::Systemd,
                Path::new("/bin/swarm"),
                Path::new("/state")
            )
            .unwrap()
            .contains("StandardOutput=append:/state/connector.log")
        );
    }
}
