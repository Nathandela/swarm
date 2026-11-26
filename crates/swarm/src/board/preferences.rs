use super::navigation::{GROUPS, ORDERS};
use crate::{ipc, supervisor::runtime};
use serde_json::json;
use std::io;
use std::os::unix::fs::DirBuilderExt;
use std::path::PathBuf;

#[derive(Clone, Copy, Default)]
pub(super) struct Layout {
    pub(super) group: usize,
    pub(super) order: usize,
    pub(super) touch_back: bool,
}

impl Layout {
    fn path() -> io::Result<PathBuf> {
        Ok(ipc::user_config_dir()?.join("swarm/board-layout.json"))
    }

    pub(super) fn load() -> io::Result<Self> {
        match runtime::read_json(&Self::path()?) {
            Ok(stored) => {
                let choice = |key, options: &[&str]| {
                    options
                        .iter()
                        .position(|option| Some(*option) == stored[key].as_str())
                        .unwrap_or(0)
                };
                Ok(Self {
                    group: choice("grouping", &GROUPS),
                    order: choice("ordering", &ORDERS),
                    touch_back: stored["touch_back"].as_bool().unwrap_or_default(),
                })
            }
            Err(error) if error.kind() == io::ErrorKind::NotFound => Ok(Self::default()),
            Err(error) => Err(error),
        }
    }

    pub(super) fn save(self) -> io::Result<()> {
        let path = Self::path()?;
        std::fs::DirBuilder::new()
            .recursive(true)
            .mode(0o700)
            .create(path.parent().expect("layout file has config directory"))?;
        runtime::write_json(
            &path,
            &json!({
                "schema_version":1, "grouping":GROUPS[self.group], "ordering":ORDERS[self.order],
                "touch_back":self.touch_back,
            }),
        )
    }
}
