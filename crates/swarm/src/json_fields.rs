//! Read Go-compatible JSON object fields without changing the raw value bytes.
use serde::de::{DeserializeOwned, IgnoredAny};
use std::{collections::BTreeMap, io};

fn json_space(text: &str) -> &str {
    text.trim_start_matches([' ', '\n', '\r', '\t'])
}

fn next_json<T: DeserializeOwned>(input: &str) -> io::Result<(T, &str, &str)> {
    let mut stream = serde_json::Deserializer::from_str(input).into_iter::<T>();
    let value = stream
        .next()
        .ok_or_else(|| io::Error::other("missing Go JSON value"))??;
    let (raw, rest) = input.split_at(stream.byte_offset());
    Ok((value, raw, json_space(rest)))
}

pub(crate) fn object_fields(text: &str) -> io::Result<BTreeMap<String, &str>> {
    let mut rest = json_space(text)
        .strip_prefix('{')
        .map(json_space)
        .ok_or_else(|| io::Error::other("Go metadata must be a JSON object"))?;
    let mut fields = BTreeMap::new();
    if !rest.starts_with('}') {
        loop {
            let (key, _, after_key) = next_json::<String>(rest)?;
            let after_colon = after_key
                .strip_prefix(':')
                .map(json_space)
                .ok_or_else(|| io::Error::other("missing Go metadata field separator"))?;
            let (_, raw, after_value) = next_json::<IgnoredAny>(after_colon)?;
            if fields.insert(key, raw).is_some() {
                return Err(io::Error::other("duplicate Go metadata field"));
            }
            if let Some(after_comma) = after_value.strip_prefix(',') {
                rest = json_space(after_comma);
            } else {
                rest = after_value;
                break;
            }
        }
    }
    if rest.strip_prefix('}').map(json_space) != Some("") {
        return Err(io::Error::other("invalid Go metadata object boundary"));
    }
    Ok(fields)
}
