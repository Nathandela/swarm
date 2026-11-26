use std::{
    io,
    path::{Path, PathBuf},
    str,
};

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(super) enum Platform {
    Launchd,
    Systemd,
}

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
            xml_escape(b),
            xml_escape(p),
            xml_escape(l),
            xml_escape(l)
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

fn xml_escape(s: &str) -> String {
    s.replace('&', "&amp;")
        .replace('<', "&lt;")
        .replace('>', "&gt;")
        .replace('\'', "&apos;")
        .replace('"', "&quot;")
}
fn systemd_quote(s: &str) -> String {
    format!(
        "\"{}\"",
        s.replace('%', "%%")
            .replace('\\', "\\\\")
            .replace('"', "\\\"")
    )
}

// Only accept a complete stock unit; partial matches could authorize deleting a user's unit.
pub(super) fn bound_place(platform: Platform, body: &str) -> io::Result<PathBuf> {
    let place = match platform {
        Platform::Launchd => {
            let start = "<key>ProgramArguments</key><array><string>";
            let rest = body.strip_prefix("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\"><dict>\n<key>Label</key><string>com.swarm.discord</string>\n").and_then(|s| s.strip_prefix(start)).ok_or_else(invalid)?;
            let (_, rest) = rest.split_once("</string><string>discord</string><string>run</string><string>--state-dir</string><string>").ok_or_else(invalid)?;
            let (p, suffix) = rest.split_once("</string><string>--unattended</string></array>\n<key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>ThrottleInterval</key><integer>10</integer>\n<key>StandardOutPath</key><string>").ok_or_else(invalid)?;
            let (log, suffix) = suffix
                .split_once("</string><key>StandardErrorPath</key><string>")
                .ok_or_else(invalid)?;
            let (log2, tail) = suffix
                .split_once("</string>\n</dict></plist>\n")
                .ok_or_else(invalid)?;
            if !tail.is_empty() || log != log2 || !log.ends_with("/connector.log") {
                return Err(invalid());
            }
            xml_unescape(p)?
        }
        Platform::Systemd => {
            let start = "[Unit]\nDescription=Swarm Discord\nStartLimitIntervalSec=0\n[Service]\nExecStart=:";
            let rest = body.strip_prefix(start).ok_or_else(invalid)?;
            let (_, rest) = rest
                .split_once(" discord run --state-dir ")
                .ok_or_else(invalid)?;
            let (place, rest) = rest
                .split_once(" --unattended\nRestart=always\nRestartSec=10\nStandardOutput=append:")
                .ok_or_else(invalid)?;
            let (log, rest) = rest
                .split_once("\nStandardError=append:")
                .ok_or_else(invalid)?;
            if !rest.ends_with("\n[Install]\nWantedBy=default.target\n")
                || !rest.starts_with(log)
                || log != &rest[..log.len()]
                || !log.ends_with("/connector.log")
            {
                return Err(invalid());
            }
            if place.starts_with('"') && place.ends_with('"') {
                systemd_unquote(&place[1..place.len() - 1])?
            } else {
                return Err(invalid());
            }
        }
    };
    let p = PathBuf::from(place);
    if !safe(&p) {
        return Err(invalid());
    }
    Ok(p)
}

fn xml_unescape(s: &str) -> io::Result<String> {
    let mut out = s.to_owned();
    for (from, to) in [
        ("&amp;", "&"),
        ("&lt;", "<"),
        ("&gt;", ">"),
        ("&apos;", "'"),
        ("&quot;", "\""),
    ] {
        out = out.replace(from, to);
    }
    if out.contains('&') {
        return Err(invalid());
    }
    Ok(out)
}
fn systemd_unquote(s: &str) -> io::Result<String> {
    let mut out = String::new();
    let mut chars = s.chars();
    while let Some(c) = chars.next() {
        if c == '\\' {
            match chars.next() {
                Some('\\') => out.push('\\'),
                Some('"') => out.push('"'),
                _ => return Err(invalid()),
            }
        } else if c == '"' {
            return Err(invalid());
        } else {
            out.push(c);
        }
    }
    let out = out.replace("%%", "%");
    Ok(out)
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
