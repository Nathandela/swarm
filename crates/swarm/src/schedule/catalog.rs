use super::{
    Job, Result, Spec,
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
    },
    State {
        id: String,
        revision: String,
        expected: String,
        value: State,
    },
}

impl Change {
    pub(crate) fn remote(self, client: &mut Client) -> io::Result<Records> {
        if !client.supports(CONTROL) {
            return Err(io::Error::other(
                "selected supervisor does not support automation changes",
            ));
        }
        let reply = client.request_body(json!({"op":CHANGE,"change":self}), CHANGE)?;
        let job: Job = serde_json::from_value(reply["job"].clone())?;
        let target = match &self {
            Self::Create { operation, .. } => operation == &job.id,
            Self::Edit { id, .. } => id == &job.id,
            Self::State { id, value, .. } => id == &job.id && job.state == value.token(),
        };
        if field(&reply, "endpoint_id") != client.endpoint() || !target {
            return Err(io::Error::other(
                "automation change reply did not match the request",
            ));
        }
        Ok(Records::Get(Box::new(job)))
    }
}

pub(crate) fn change(root: &Path, request: &Value) -> Result<Value> {
    let change: Change = serde_json::from_value(request["change"].clone())?;
    if matches!(&change, Change::Create { operation, .. } if !super::valid_id(operation)) {
        return Err("invalid automation creation operation ID".into());
    }
    if matches!(&change,
        Change::Edit { id, revision, .. } | Change::State { id, revision, .. }
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
        Change::Edit { id, revision, spec } => store.edit(&id, Some(&revision), *spec, now)?,
        Change::State {
            id,
            revision,
            expected,
            value,
        } => store.set_state_bound(&id, value.token(), Some((&revision, &expected)), now)?,
    };
    Ok(json!({"op":CHANGE,"job":job}))
}
