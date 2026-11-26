//! Plain public-thread pages and the original Go catalog's authenticated continuation.
use super::*;
use crate::{discord::legacy_ingress::prepare::go_bytes, ipc::field};
use base64::{Engine as _, engine::general_purpose::URL_SAFE_NO_PAD};
use bbolt_rs::BucketApi;
use hmac::{Hmac, Mac};
use serde::{Deserialize, Serialize};
use time::OffsetDateTime;

const PAGE_ROWS: usize = 20;
const PAGE_BYTES: usize = 24 << 10;
const CURSOR_BYTES: usize = 1024;

pub(super) type Key = [u8; 32];

#[derive(Serialize, Deserialize)]
#[serde(rename_all = "PascalCase", deny_unknown_fields)]
pub(super) struct Cursor {
    discussion: String,
    source: String,
    pub(super) upper: String,
    pub(super) before: String,
}

fn unavailable() -> io::Error {
    invalid("the original public-thread history page or cursor is unqualified")
}

pub(super) fn key<'tx>(tx: &impl TxApi<'tx>) -> io::Result<Option<Key>> {
    let meta = tx.bucket(b"teammate_meta").ok_or_else(unavailable)?;
    meta.get(b"history_cursor_key")
        .map(|bytes| bytes.try_into().map_err(|_| unavailable()))
        .transpose()
}

fn id(value: &str) -> io::Result<u64> {
    if !super::super::config::snowflake(value) {
        return Err(unavailable());
    }
    value.parse().map_err(io::Error::other)
}

fn mac(key: &Key, payload: &[u8]) -> Hmac<Sha256> {
    let mut tag = Hmac::<Sha256>::new_from_slice(key).expect("fixed HMAC key size");
    tag.update(payload);
    tag
}

fn encode(key: &Key, cursor: &Cursor) -> io::Result<String> {
    let raw = go_bytes(&serde_json::to_value(cursor)?)?;
    Ok(format!(
        "{}.{}",
        URL_SAFE_NO_PAD.encode(&raw),
        URL_SAFE_NO_PAD.encode(mac(key, &raw).finalize().into_bytes())
    ))
}

pub(super) fn decode(
    key: Option<&Key>,
    token: &str,
    discussion: &str,
    upper: &str,
) -> io::Result<Cursor> {
    let key = key.ok_or_else(unavailable)?;
    if token.len() > CURSOR_BYTES {
        return Err(unavailable());
    }
    let (payload, signature) = token.split_once('.').ok_or_else(unavailable)?;
    let raw = URL_SAFE_NO_PAD.decode(payload).map_err(io::Error::other)?;
    let signature = URL_SAFE_NO_PAD
        .decode(signature)
        .map_err(io::Error::other)?;
    mac(key, &raw)
        .verify_slice(&signature)
        .map_err(|_| unavailable())?;
    let cursor: Cursor = serde_json::from_slice(&raw)?;
    if cursor.discussion != discussion
        || cursor.source != "thread"
        || cursor.upper != upper
        || id(&cursor.before)? >= id(&cursor.upper)?
        || encode(key, &cursor)? != token
    {
        return Err(unavailable());
    }
    Ok(cursor)
}

pub(super) fn rows(
    route: &Route,
    raw: &[Value],
    mut before: Option<u64>,
) -> io::Result<Vec<(OffsetDateTime, Value)>> {
    if raw.len() > PAGE_ROWS {
        return Err(unavailable());
    }
    let mut rows = Vec::with_capacity(raw.len());
    for message in raw {
        let current = id(field(message, "id"))?;
        if before.is_some_and(|before| current >= before) {
            return Err(unavailable());
        }
        rows.push(render::evidence(route, message)?);
        before = Some(current);
    }
    rows.reverse();
    Ok(rows)
}

pub(super) fn page(
    rows: &[(OffsetDateTime, Value)],
    starter: Option<&Value>,
    upper: &str,
) -> io::Result<Value> {
    let low = rows.first().map_or("", |(_, first)| field(first, "id"));
    let messages: Vec<_> = rows.iter().map(|(_, entry)| entry).collect();
    let result = json!({"Messages":messages,"Cursor":"","HighWater":upper,"LowWater":low,
                        "Notice":"","Starter":starter,"ReplyChain":null});
    if go_bytes(&result)?.len() > PAGE_BYTES {
        return Err(unavailable());
    }
    Ok(result)
}

pub(super) fn advance(
    page: &mut Value,
    key: Option<&Key>,
    discussion: &str,
) -> io::Result<Option<String>> {
    if page["Messages"].as_array().ok_or_else(unavailable)?.len() < PAGE_ROWS {
        return Ok(None);
    }
    let upper = field(page, "HighWater");
    let before = field(page, "LowWater");
    if id(before)? >= id(upper)? {
        return Err(unavailable());
    }
    let token = encode(
        key.ok_or_else(unavailable)?,
        &Cursor {
            discussion: discussion.into(),
            source: "thread".into(),
            upper: upper.into(),
            before: before.into(),
        },
    )?;
    page["Cursor"] = json!(token);
    Ok(Some(token))
}

pub(super) fn older(
    api: &Discord,
    cfg: &Config,
    route: &Route,
    cursor: &Cursor,
) -> io::Result<Value> {
    verify_thread_authority(api, cfg, route, false)?;
    let raw = api.request(
        &format!(
            "/channels/{}/messages?before={}&limit={PAGE_ROWS}",
            route.channel_id, cursor.before
        ),
        None,
    )?;
    let rows = rows(
        route,
        raw.as_array().ok_or_else(unavailable)?,
        Some(id(&cursor.before)?),
    )?;
    page(&rows, None, &cursor.upper)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_signed_thread_cursor_cannot_cross_discussion_source_or_upper_bound() {
        let key = [7; 32];
        let mut cursor = Cursor {
            discussion: "room".into(),
            source: "thread".into(),
            upper: "701".into(),
            before: "483".into(),
        };
        let token = encode(&key, &cursor).unwrap();
        assert_eq!(
            decode(Some(&key), &token, "room", "701").unwrap().before,
            "483"
        );
        assert!(decode(Some(&key), &token, "elsewhere", "701").is_err());
        assert!(decode(Some(&key), &token, "room", "702").is_err());
        assert!(decode(Some(&[6; 32]), &token, "room", "701").is_err());
        cursor.source = "parent".into();
        assert!(decode(Some(&key), &encode(&key, &cursor).unwrap(), "room", "701").is_err());
    }
}
