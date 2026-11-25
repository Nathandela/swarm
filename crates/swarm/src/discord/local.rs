//! Compatibility client for the installed connector's private Unix HTTP interface.
use super::config::invalid;
use crate::socket::DeadlineReader;
use serde_json::Value;
use sha2::{Digest, Sha256};
use std::fs;
use std::io::{self, Read, Write};
use std::os::unix::fs::{FileTypeExt, MetadataExt};
use std::os::unix::net::UnixStream;
use std::path::Path;
use std::time::{Duration, Instant};

const MAX_RESPONSE: usize = 4 << 20;
const MAX_HEADERS: usize = 16 << 10;
const TIMEOUT: Duration = Duration::from_secs(35);

pub(super) fn escape(value: &str) -> String {
    let mut result = String::new();
    for byte in value.bytes() {
        match byte {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'.' | b'_' | b'~' => {
                result.push(char::from(byte))
            }
            b' ' => result.push('+'),
            _ => result.push_str(&format!("%{byte:02X}")),
        }
    }
    result
}

pub(super) fn call(
    dir: &Path,
    operation: &str,
    query: &str,
    request: Option<(&str, &Value)>,
) -> io::Result<Value> {
    let socket = dir.join("bridge.sock");
    let meta = fs::symlink_metadata(&socket).map_err(|err| {
        if err.kind() == io::ErrorKind::NotFound {
            io::Error::new(err.kind(), "connector local service is unavailable")
        } else {
            err
        }
    })?;
    // SAFETY: geteuid takes no arguments.
    if !meta.file_type().is_socket()
        || meta.nlink() != 1
        || meta.mode() & 0o777 != 0o600
        || meta.uid() != unsafe { libc::geteuid() }
    {
        return Err(invalid("unsafe local socket"));
    }
    let mut stream = UnixStream::connect(socket)
        .map_err(|err| io::Error::new(err.kind(), format!("connect local service: {err}")))?;
    stream.set_write_timeout(Some(TIMEOUT))?;
    let (method, path, body) = match request {
        Some((installation, body)) => {
            // Stock Go dispatches exact paths. An unsupported peer refuses the
            // versioned route before invoking the unbound mutation.
            let identity = Sha256::digest(installation.as_bytes());
            (
                "POST",
                format!("/installation-v1/{identity:x}/{operation}"),
                serde_json::to_vec(body)?,
            )
        }
        None => ("GET", format!("/{operation}{query}"), Vec::new()),
    };
    let headers = format!(
        "{method} {path} HTTP/1.0\r\nHost: local\r\nConnection: close\r\nContent-Type: application/json\r\nContent-Length: {}\r\n\r\n",
        body.len()
    );
    stream.write_all(headers.as_bytes())?;
    stream.write_all(&body)?;

    let mut reader = DeadlineReader(&stream, Instant::now() + TIMEOUT);
    let mut response = Vec::new();
    let mut buf = [0; 8192];
    let mut header_end = None;
    loop {
        let start = response.len().saturating_sub(3);
        let count = reader.read(&mut buf)?;
        if count == 0 {
            break;
        }
        response.extend_from_slice(&buf[..count]);
        if header_end.is_none() {
            header_end = response[start..]
                .windows(4)
                .position(|part| part == b"\r\n\r\n")
                .map(|end| start + end);
        }
        if header_end.unwrap_or(response.len()) > MAX_HEADERS
            || header_end.is_some_and(|end| response.len() - end - 4 > MAX_RESPONSE)
        {
            return Err(invalid("local response exceeds size limit"));
        }
    }
    let header_end = header_end.ok_or_else(|| invalid("invalid local HTTP response"))?;
    let headers = std::str::from_utf8(&response[..header_end])
        .map_err(|_| invalid("invalid local HTTP response"))?;
    let mut lines = headers.split("\r\n");
    let mut status = lines.next().unwrap_or_default().split_whitespace();
    if !matches!(status.next(), Some("HTTP/1.0" | "HTTP/1.1")) {
        return Err(invalid("invalid local HTTP response"));
    }
    let status = status
        .next()
        .and_then(|status| status.parse::<u16>().ok())
        .ok_or_else(|| invalid("invalid local HTTP response status"))?;
    let body = &response[header_end + 4..];
    for line in lines {
        let (name, value) = line
            .split_once(':')
            .ok_or_else(|| invalid("invalid local HTTP response header"))?;
        if name.eq_ignore_ascii_case("transfer-encoding")
            || name.eq_ignore_ascii_case("content-length")
                && value.trim().parse::<usize>().ok() != Some(body.len())
        {
            return Err(invalid("invalid local HTTP response body framing"));
        }
    }
    let body: Value =
        serde_json::from_slice(body).map_err(|_| invalid("invalid connector response JSON"))?;
    if request.is_some() && matches!(status, 404 | 405) {
        return Err(io::Error::other(
            "serving connector lacks installation-bound controls",
        ));
    }
    if status != 200 {
        let detail = body["error"]
            .as_str()
            .filter(|detail| !detail.is_empty())
            .unwrap_or("local request failed");
        return Err(io::Error::other(
            detail
                .strip_prefix("discord: ")
                .unwrap_or(detail)
                .to_owned(),
        ));
    }
    Ok(body)
}
