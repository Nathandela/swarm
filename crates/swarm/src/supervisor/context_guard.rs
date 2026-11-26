//! The local owner holds the revisioned Context Guard policy across all sessions.
use super::runtime;
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::io;
use std::path::{Path, PathBuf};
use std::sync::Mutex;

pub(super) const CAPABILITY: &str = "context-guard-settings";
const FILE: &str = "context-guard-settings.json";

#[derive(Copy, Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub(super) struct AutoCompact {
    #[serde(default)]
    pub(super) enabled: bool,
    pub(super) threshold_percent: i64,
}

#[derive(Copy, Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(deny_unknown_fields)]
pub(super) struct Settings {
    schema_version: u32,
    #[serde(default)]
    pub(super) revision: u64,
    pub(super) auto_compact: AutoCompact,
}

impl Default for Settings {
    fn default() -> Self {
        Self {
            schema_version: 1,
            revision: 0,
            auto_compact: AutoCompact {
                enabled: false,
                threshold_percent: 80,
            },
        }
    }
}

pub(super) struct Store {
    path: PathBuf,
    current: Mutex<Option<Settings>>,
}

impl Store {
    pub(super) fn open(root: &Path) -> Self {
        let path = root.join(FILE);
        let current = Self::read(&path).ok();
        Self {
            path,
            current: Mutex::new(current),
        }
    }

    fn read(path: &Path) -> io::Result<Settings> {
        let bytes = match runtime::read_private(path, 64 * 1024) {
            Ok(bytes) => bytes,
            Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(Settings::default()),
            Err(error) => return Err(error),
        };
        let next: Settings = serde_json::from_slice(&bytes)?;
        if next.schema_version != 1 || !(40..=95).contains(&next.auto_compact.threshold_percent) {
            return Err(io::Error::other("invalid saved context guard settings"));
        }
        Ok(next)
    }

    pub(super) fn request(&self, request: &Value, negotiated: bool) -> Value {
        let op = crate::ipc::field(request, "op");
        let error =
            |code: &str, detail: &str| json!({"op":"error","error_code":code,"error":detail});
        if !negotiated {
            return error(
                "capability_refused",
                "context guard settings capability was not negotiated",
            );
        }
        let unavailable = || error("unavailable", "context guard settings are unavailable");
        let update = if op == "context_guard_set" {
            let body = &request["context_guard_set"];
            if !body.is_object() {
                return error(
                    "invalid_field",
                    "context_guard_set requires context_guard_set",
                );
            }
            let policy = &body["auto_compact"];
            let threshold = policy["threshold_percent"].as_i64().unwrap_or(0);
            if !(40..=95).contains(&threshold) {
                return error(
                    "invalid_field",
                    &format!(
                        "context_guard_set: auto_compact.threshold_percent {threshold} is outside 40..95"
                    ),
                );
            }
            let enabled = match policy.get("enabled") {
                Some(Value::Bool(value)) => *value,
                None => false,
                _ => return error("invalid_field", "invalid auto_compact.enabled"),
            };
            let revision = match body.get("expected_revision") {
                None => 0,
                Some(value) => match value.as_u64() {
                    Some(revision) => revision,
                    None => return error("invalid_field", "invalid expected_revision"),
                },
            };
            Some((
                revision,
                AutoCompact {
                    enabled,
                    threshold_percent: threshold,
                },
            ))
        } else {
            None
        };
        let mut current = self.current.lock().unwrap();
        let Some(old) = *current else {
            return unavailable();
        };
        if let Some((expected, auto_compact)) = update {
            if old.revision != expected {
                return error("stale_revision", "context guard settings revision is stale");
            }
            if old.auto_compact != auto_compact {
                let Some(revision) = old.revision.checked_add(1) else {
                    return unavailable();
                };
                let next = Settings {
                    revision,
                    auto_compact,
                    ..old
                };
                if runtime::write_atomic(&self.path, |file| {
                    serde_json::to_writer(file, &next).map_err(io::Error::from)
                })
                .is_err()
                {
                    // A failed directory sync after rename leaves the new generation uncertain.
                    if Self::read(&self.path).ok() != Some(old) {
                        *current = None;
                    }
                    return unavailable();
                }
                *current = Some(next);
            }
        }
        json!({"op":op,"context_guard_settings":*current})
    }
}
