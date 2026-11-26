use super::{
    Job, Result, Spec, Template,
    store::{Occurrence, Store},
};
use crate::ipc::{Client, field};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::{io, path::Path};

pub(crate) const CAP: &str = "schedule-catalog-v1";
const OP: &str = "schedule_catalog";

#[derive(Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
pub(crate) enum Read {
    List,
    Get { id: String },
    History { id: String },
}

#[derive(Serialize, Deserialize)]
#[serde(tag = "kind", content = "records", rename_all = "snake_case")]
pub(crate) enum Records {
    List(Vec<Job>),
    Get(Box<Job>),
    History(Vec<Occurrence>),
}

impl Read {
    pub(crate) fn remote(self, client: &mut Client) -> io::Result<Records> {
        if !client.supports(CAP) {
            return Err(io::Error::other(
                "selected supervisor does not support the automation catalog",
            ));
        }
        let reply = client.request_body(json!({"op": OP, "query": self}), OP)?;
        if field(&reply, "endpoint_id") != client.endpoint() {
            return Err(io::Error::other(
                "automation catalog reply came from another supervisor",
            ));
        }
        let records: Records = serde_json::from_value(reply["catalog"].clone())?;
        match (&self, &records) {
            (Self::List, Records::List(_)) => Ok(records),
            (Self::Get { id }, Records::Get(job)) if id == &job.id => Ok(records),
            (Self::History { id }, Records::History(runs))
                if runs.iter().all(|run| &run.job_id == id) =>
            {
                Ok(records)
            }
            _ => Err(io::Error::other(
                "automation catalog reply did not match the request",
            )),
        }
    }
}

pub(super) fn project(root: &Path, read: Read) -> Result<Records> {
    if matches!(&read, Read::Get { id } | Read::History { id } if !super::valid_id(id)) {
        return Err("invalid schedule job ID".into());
    }
    Ok(
        match (Store::existing(root, Path::new("/etc/localtime"))?, read) {
            (None, Read::List) => Records::List(Vec::new()),
            (None, _) => return Err("schedule record not found".into()),
            (Some(store), Read::List) => Records::List(store.jobs()?),
            (Some(store), Read::Get { id }) => Records::Get(Box::new(store.job(&id)?)),
            (Some(store), Read::History { id }) => Records::History(store.history(&id)?),
        },
    )
}

const TEMPLATES: &str = "schedule_templates";

pub(crate) struct TemplateRead;

impl TemplateRead {
    #[allow(dead_code)]
    pub(crate) fn remote(client: &mut Client) -> io::Result<Vec<Template>> {
        if !client.supports(CAP) {
            return Err(io::Error::other(
                "selected supervisor does not support the automation catalog",
            ));
        }
        let reply = client.request_body(json!({"op":TEMPLATES}), TEMPLATES)?;
        if field(&reply, "endpoint_id") != client.endpoint() {
            return Err(io::Error::other(
                "personal template reply came from another supervisor",
            ));
        }
        serde_json::from_value(reply["templates"].clone()).map_err(Into::into)
    }
}

pub(crate) fn serve_templates(root: &Path) -> Result<Value> {
    let templates = match Store::existing(root, Path::new("/etc/localtime"))? {
        Some(store) => store.templates()?,
        None => Vec::new(),
    };
    let reply = json!({"op":TEMPLATES,"templates":templates});
    if serde_json::to_vec(&reply)?.len() > (1 << 20) - 1024 {
        return Err("personal template result exceeds the owner socket frame limit".into());
    }
    Ok(reply)
}

pub(crate) fn serve(root: &Path, request: &Value) -> Result<Value> {
    let read = serde_json::from_value(request["query"].clone())?;
    let records = project(root, read)?;
    let reply = json!({"op":OP, "catalog":records});
    if serde_json::to_vec(&reply)?.len() > (1 << 20) - 1024 {
        return Err("automation catalog result exceeds the owner socket frame limit".into());
    }
    Ok(reply)
}

pub(crate) const CONTROL: &str = "schedule-catalog-control-v1";
const CHANGE: &str = "schedule_catalog_change";
const TEMPLATE_CHANGE: &str = "schedule_template_change";

#[derive(Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
pub(crate) enum TemplateChange {
    Save {
        id: Option<String>,
        name: String,
        prompt: String,
    },
    Delete {
        id: String,
    },
}

impl TemplateChange {
    #[allow(dead_code)]
    pub(crate) fn remote(self, client: &mut Client) -> io::Result<Option<Template>> {
        if !client.supports(CONTROL) {
            return Err(io::Error::other(
                "selected supervisor does not support template changes",
            ));
        }
        let reply =
            client.request_body(json!({"op":TEMPLATE_CHANGE,"change":self}), TEMPLATE_CHANGE)?;
        if field(&reply, "endpoint_id") != client.endpoint() {
            return Err(io::Error::other(
                "personal template reply came from another supervisor",
            ));
        }
        let template = match self {
            Self::Save { id, name, prompt } => {
                let template: Template = serde_json::from_value(reply["template"].clone())?;
                if id.as_ref().is_some_and(|id| id != &template.id)
                    || template.name != name
                    || template.prompt != prompt
                {
                    return Err(io::Error::other(
                        "personal template change reply did not match the request",
                    ));
                }
                Some(template)
            }
            Self::Delete { id } => {
                if field(&reply, "template_id") != id {
                    return Err(io::Error::other(
                        "personal template delete reply did not match the request",
                    ));
                }
                None
            }
        };
        Ok(template)
    }
}

pub(crate) fn change_template(root: &Path, request: &Value) -> Result<Value> {
    let change: TemplateChange = serde_json::from_value(request["change"].clone())?;
    let store = Store::open(root, Path::new("/etc/localtime"))?;
    match change {
        TemplateChange::Save { id, name, prompt } => {
            if id.as_ref().is_some_and(|id| !super::valid_id(id)) {
                return Err("invalid personal template ID".into());
            }
            let template =
                store.save_template(id.as_deref(), name, prompt, jiff::Timestamp::now())?;
            Ok(json!({"op":TEMPLATE_CHANGE,"template":template}))
        }
        TemplateChange::Delete { id } => {
            if !super::valid_id(&id) {
                return Err("invalid personal template ID".into());
            }
            store.delete_template(&id)?;
            Ok(json!({"op":TEMPLATE_CHANGE,"template_id":id}))
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{
        fs::{self, OpenOptions},
        os::unix::fs::OpenOptionsExt,
        path::PathBuf,
    };

    struct Temp(PathBuf);

    impl Temp {
        fn new() -> Self {
            let path = std::env::temp_dir().join(format!(
                "swarm-template-catalog-{}",
                super::super::id(jiff::Timestamp::now()).unwrap()
            ));
            crate::supervisor::runtime::private_dir(&path).unwrap();
            Self(path)
        }

        fn with_go_catalog() -> Self {
            let temp = Self::new();
            let directory = temp.0.join("schedules");
            crate::supervisor::runtime::private_dir(&directory).unwrap();
            let mut file = OpenOptions::new()
                .write(true)
                .create_new(true)
                .mode(0o600)
                .open(directory.join("state.db"))
                .unwrap();
            std::io::Write::write_all(
                &mut file,
                include_bytes!("../../tests/fixtures/schedule/go-catalog.db"),
            )
            .unwrap();
            temp
        }
    }

    impl Drop for Temp {
        fn drop(&mut self) {
            let _ = fs::remove_dir_all(&self.0);
        }
    }

    #[test]
    fn template_catalog_operations_preserve_go_written_template() {
        let temp = Temp::with_go_catalog();
        Store::open(&temp.0, Path::new("/etc/localtime")).unwrap();
        let listed = serve_templates(&temp.0).unwrap();
        let templates: Vec<Template> = serde_json::from_value(listed["templates"].clone()).unwrap();
        assert_eq!(templates.len(), 1);
        let original = &templates[0];
        assert_eq!(original.name, "Existing template");
        assert_eq!(original.prompt, "Copy me");
        assert_eq!(original.created_at, "2026-11-01T05:00:00Z".parse().unwrap());

        let created = change_template(
            &temp.0,
            &json!({"change":{"kind":"save","name":"New","prompt":"First\nSecond"}}),
        )
        .unwrap();
        let created: Template = serde_json::from_value(created["template"].clone()).unwrap();
        let edited = change_template(
            &temp.0,
            &json!({"change":{"kind":"save","id":created.id,"name":"Edited","prompt":"Updated"}}),
        )
        .unwrap();
        let edited: Template = serde_json::from_value(edited["template"].clone()).unwrap();
        assert_eq!(edited.id, created.id);
        assert_eq!(edited.created_at, created.created_at);
        assert_eq!(edited.name, "Edited");

        let deleted =
            change_template(&temp.0, &json!({"change":{"kind":"delete","id":edited.id}})).unwrap();
        assert_eq!(deleted["template_id"], created.id);
        let templates = Store::open(&temp.0, Path::new("/etc/localtime"))
            .unwrap()
            .templates()
            .unwrap();
        assert_eq!(templates.len(), 1);
        assert_eq!(templates[0].id, original.id);
        assert_eq!(templates[0].prompt, "Copy me");
    }
}

#[derive(Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub(crate) enum State {
    Active,
    Paused,
    Archived,
}

impl State {
    fn token(&self) -> &'static str {
        match self {
            Self::Active => "active",
            Self::Paused => "paused",
            Self::Archived => "archived",
        }
    }
}

#[derive(Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
pub(crate) enum Change {
    Create {
        operation: String,
        spec: Box<Spec>,
    },
    Edit {
        id: String,
        revision: String,
        spec: Box<Spec>,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        paused: Option<(u64, jiff::Timestamp)>,
    },
    Enable {
        id: String,
        revision: String,
        generation: u64,
        updated_at: jiff::Timestamp,
        spec: Box<Spec>,
        install_timer: bool,
    },
    State {
        id: String,
        revision: String,
        expected: String,
        generation: u64,
        updated_at: jiff::Timestamp,
        value: State,
    },
}

impl Change {
    pub(crate) fn remote(self, client: &mut Client) -> io::Result<Option<Records>> {
        if !client.supports(CONTROL) {
            return Err(io::Error::other(
                "selected supervisor does not support automation changes",
            ));
        }
        let request = json!({"op":CHANGE,"change":self});
        let reply = if matches!(self, Self::Enable { .. }) {
            client.exchange(request, CHANGE, std::time::Duration::from_secs(60))?
        } else {
            client.request_body(request, CHANGE)?
        };
        if field(&reply, "endpoint_id") != client.endpoint() {
            return Err(io::Error::other(
                "automation change reply came from another supervisor",
            ));
        }
        if let Self::Enable {
            id,
            revision,
            install_timer: false,
            ..
        } = &self
            && reply["timer_confirmation"] == json!({"id":id,"revision":revision})
        {
            return Ok(None);
        }
        let job: Job = serde_json::from_value(reply["job"].clone())?;
        let target = match &self {
            Self::Create { operation, .. } => operation == &job.id,
            Self::Edit { id, .. } => id == &job.id,
            Self::Enable { id, .. } => id == &job.id && job.state == "active",
            Self::State { id, value, .. } => id == &job.id && job.state == value.token(),
        };
        if !target {
            return Err(io::Error::other(
                "automation change reply did not match the request",
            ));
        }
        Ok(Some(Records::Get(Box::new(job))))
    }
}

pub(crate) fn change(
    root: &Path,
    request: &Value,
    readiness: impl FnOnce(&Spec) -> Result<()>,
    conflict: impl Fn(&Path) -> Option<String>,
) -> Result<Value> {
    let change: Change = serde_json::from_value(request["change"].clone())?;
    if matches!(&change, Change::Create { operation, .. } if !super::valid_id(operation)) {
        return Err("invalid automation creation operation ID".into());
    }
    if matches!(&change,
        Change::Edit { id, revision, .. } | Change::Enable { id, revision, .. }
            | Change::State { id, revision, .. }
            if !super::valid_id(id) || !super::valid_id(revision))
    {
        return Err("invalid automation job or revision ID".into());
    }
    let store = Store::open(root, Path::new("/etc/localtime"))?;
    let now = jiff::Timestamp::now();
    let job = match change {
        Change::Create { operation, spec } => {
            store.create_bound(*spec, now, true, Some(&operation))?
        }
        Change::Edit {
            id,
            revision,
            spec,
            paused,
        } => store.edit_bound(
            &id,
            Some(&revision),
            *spec,
            paused.map(|(generation, updated)| (generation, updated, false)),
            now,
        )?,
        Change::Enable {
            id,
            revision,
            generation,
            updated_at,
            mut spec,
            install_timer,
        } => {
            let target = store.job(&id)?;
            if target.revision_id != revision || !target.paused_revision(generation, updated_at) {
                return Err("automation revision or state changed".into());
            }
            spec.prepare(Path::new("/etc/localtime"))?;
            store.check_future(&spec, now)?;
            readiness(&spec)?;
            super::workspace::preview(root, &id, &spec, &store.active()?, conflict)?;
            let installed =
                super::timer::command("status")?.as_deref() != Some("installed, active");
            if installed {
                if !install_timer {
                    return Ok(
                        json!({"op":CHANGE,"timer_confirmation":{"id":id,"revision":revision}}),
                    );
                }
                super::timer::command("install")?;
                if super::timer::command("status")?.as_deref() != Some("installed, active") {
                    return Err("host timer is not active; retry or save paused".into());
                }
            }
            store
                .edit_bound(
                    &id,
                    Some(&revision),
                    *spec,
                    Some((generation, updated_at, true)),
                    jiff::Timestamp::now(),
                )
                .map_err(|error| {
                    if installed {
                        format!("host timer enabled; job save failed: {error}").into()
                    } else {
                        error
                    }
                })?
        }
        Change::State {
            id,
            revision,
            expected,
            generation,
            updated_at,
            value,
        } => store.set_state_bound(
            &id,
            value.token(),
            Some((&revision, &expected, generation, updated_at)),
            now,
        )?,
    };
    Ok(json!({"op":CHANGE,"job":job}))
}

pub(crate) const MANUAL_CAP: &str = "schedule-board-manual-v1";
pub(crate) const MANUAL_OP: &str = "schedule_board_manual";

#[derive(Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
pub(crate) enum Manual {
    Status,
    Run { previous: Option<String> },
}

#[derive(Serialize, Deserialize)]
pub(crate) struct RunState {
    pub(crate) id: String,
    pub(crate) state: String,
    pub(crate) pending: bool,
}

impl Manual {
    pub(crate) fn remote(self, client: &mut Client, job: &str) -> io::Result<Option<RunState>> {
        if !client.supports(MANUAL_CAP) {
            return Err(io::Error::other(
                "selected supervisor does not support durable board runs",
            ));
        }
        let reply = client.exchange(
            json!({"op":MANUAL_OP,"schedule_id":job,"intent":self}),
            MANUAL_OP,
            std::time::Duration::from_secs(110),
        )?;
        if field(&reply, "endpoint_id") != client.endpoint() || field(&reply, "schedule_id") != job
        {
            return Err(io::Error::other(
                "automation run reply did not match the request",
            ));
        }
        Ok(serde_json::from_value(reply["run"].clone())?)
    }
}
