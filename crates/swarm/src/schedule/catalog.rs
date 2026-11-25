use super::{
    Job, Result,
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

pub(crate) fn serve(root: &Path, request: &Value) -> Result<Value> {
    let read: Read = serde_json::from_value(request["query"].clone())?;
    if matches!(&read, Read::Get { id } | Read::History { id } if !super::valid_id(id)) {
        return Err("invalid schedule job ID".into());
    }
    let records = match (Store::existing(root, Path::new("/etc/localtime"))?, read) {
        (None, Read::List) => Records::List(Vec::new()),
        (None, _) => return Err("schedule record not found".into()),
        (Some(store), Read::List) => Records::List(store.jobs()?),
        (Some(store), Read::Get { id }) => Records::Get(Box::new(store.job(&id)?)),
        (Some(store), Read::History { id }) => Records::History(store.history(&id)?),
    };
    let reply = json!({"op":OP, "catalog":records});
    if serde_json::to_vec(&reply)?.len() > (1 << 20) - 1024 {
        return Err("automation catalog result exceeds the owner socket frame limit".into());
    }
    Ok(reply)
}
