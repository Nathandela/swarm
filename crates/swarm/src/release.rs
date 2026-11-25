use serde_json::{Value, json};
use sha2::{Digest, Sha256};

pub(crate) const MEDIA: &[u8] = include_bytes!(concat!(env!("OUT_DIR"), "/release-media.json"));

pub(crate) fn info() -> Value {
    json!({
        "format": 1,
        "channel": "rust-preview",
        "version": option_env!("SWARM_BUILD_VERSION").unwrap_or("dev"),
        "source": option_env!("SWARM_BUILD_COMMIT").unwrap_or("dev"),
        "protocol": crate::ipc::VERSION,
        "shimwire": crate::supervisor::runtime::SHIM_VERSION,
        "media_protocol": crate::discord::MEDIA_PROTOCOL,
        "media_pins_sha256": format!("{:x}", Sha256::digest(MEDIA)),
    })
}
