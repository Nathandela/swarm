//! Quoting for native per-user service definitions shared by the timer and Discord connector.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(crate) enum Platform {
    Launchd,
    Systemd,
}

pub(crate) fn xml_text(value: &str) -> String {
    value
        .replace('&', "&amp;")
        .replace('<', "&lt;")
        .replace('>', "&gt;")
        .replace('"', "&#34;")
        .replace('\'', "&#39;")
}

/// Escape the contents of one double-quoted systemd argument.
pub(crate) fn systemd_string(value: &str) -> String {
    value
        .replace('\\', "\\\\")
        .replace('"', "\\\"")
        .replace('%', "%%")
}
