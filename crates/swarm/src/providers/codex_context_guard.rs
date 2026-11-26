//! Context Guard policy and native notification contract from the pinned Go oracle.
use serde::{Deserialize, Serialize};
use serde_json::Value;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Support {
    Unsupported,
    ObserveOnly,
    Automatic,
}

pub(crate) fn support(version: &str) -> Support {
    if version == "0.151.0" {
        return Support::Automatic;
    }
    if version
        .strip_prefix("0.150.")
        .or_else(|| version.strip_prefix("0.151."))
        .is_some_and(|patch| !patch.is_empty() && patch.bytes().all(|byte| byte.is_ascii_digit()))
    {
        Support::ObserveOnly
    } else {
        Support::Unsupported
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Evidence {
    Usage { used: u64, limit: u64 },
    CompactStarted,
    CompactCompleted { legacy: bool },
}

pub(crate) fn evidence(frame: &Value, thread: &str) -> Option<Evidence> {
    use super::codex_receipt::canonical_thread;
    use crate::ipc::field;
    fn keys(value: &Value, names: &[&str]) -> bool {
        value.as_object().is_some_and(|obj| {
            obj.len() == names.len() && names.iter().all(|key| obj.contains_key(*key))
        })
    }
    // The provider can stamp its outer envelope. It is not usage evidence.
    let envelope = keys(frame, &["method", "params"])
        || keys(frame, &["method", "params", "emittedAtMs"]) && frame["emittedAtMs"].is_u64();
    if !canonical_thread(thread) || !envelope {
        return None;
    }
    let body = &frame["params"];
    if body["threadId"] != thread || field(body, "turnId").is_empty() {
        return None;
    }
    match field(frame, "method") {
        "thread/tokenUsage/updated" if keys(body, &["threadId", "turnId", "tokenUsage"]) => {
            let usage = &body["tokenUsage"];
            if !keys(usage, &["last", "total", "modelContextWindow"]) {
                return None;
            }
            let breakdown = [
                "cachedInputTokens",
                "inputTokens",
                "outputTokens",
                "reasoningOutputTokens",
                "totalTokens",
                "cacheWriteInputTokens",
            ];
            for name in ["last", "total"] {
                let body = &usage[name];
                let n = if body.get("cacheWriteInputTokens").is_some() {
                    6
                } else {
                    5
                };
                if !keys(body, &breakdown[..n])
                    || breakdown[..n]
                        .iter()
                        .any(|key| body[*key].as_u64().is_none())
                {
                    return None;
                }
            }
            let limit = usage["modelContextWindow"]
                .as_u64()
                .filter(|value| *value > 0)?;
            Some(Evidence::Usage {
                used: usage["last"]["totalTokens"].as_u64()?,
                limit,
            })
        }
        method @ ("item/started" | "item/completed") => {
            let started = method == "item/started";
            let timestamp = if started {
                "startedAtMs"
            } else {
                "completedAtMs"
            };
            if !keys(body, &["threadId", "turnId", "item", timestamp])
                || body[timestamp].as_u64().is_none()
            {
                return None;
            }
            let item = &body["item"];
            if !keys(item, &["id", "type"])
                || item["type"] != "contextCompaction"
                || field(item, "id").is_empty()
            {
                return None;
            }
            Some(if started {
                Evidence::CompactStarted
            } else {
                Evidence::CompactCompleted { legacy: false }
            })
        }
        "thread/compacted" if keys(body, &["threadId", "turnId"]) => {
            Some(Evidence::CompactCompleted { legacy: true })
        }
        _ => None,
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub(crate) enum Phase {
    Disabled,
    Unsupported,
    Armed,
    PendingIdle,
    Prepared,
    Executing,
    AwaitingConfirmation,
    ProviderCompacting,
    Latched,
    OutcomeUnknownHold,
    EventLossHold,
    BlockedCorrupt,
}

impl Phase {
    fn held(self) -> bool {
        matches!(
            self,
            Self::OutcomeUnknownHold | Self::EventLossHold | Self::BlockedCorrupt
        )
    }
    fn prewrite(self) -> bool {
        matches!(
            self,
            Self::Disabled | Self::Unsupported | Self::Armed | Self::PendingIdle | Self::Prepared
        )
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct Settings {
    pub enabled: bool,
    pub threshold: u8,
    pub revision: u64,
}

impl Settings {
    fn valid(self) -> bool {
        (40..=95).contains(&self.threshold)
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub(crate) struct Durable {
    pub phase: Phase,
    pub trigger: u8,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Signal {
    Provider { sequence: u64, evidence: Evidence },
    Settings(Settings),
    Idle,
    Busy,
    Begin,
    Written,
    Unknown,
    EventLoss,
    Refused,
}

pub(crate) struct Machine {
    pub durable: Durable,
    pub settings: Settings,
    pub support: Support,
    pub usage: Option<(u64, u64)>,
    pub last_compacted: bool,
    seen: u64,
    observed_millis: Option<u64>,
    fresh_after: u64,
}

impl Machine {
    pub(crate) fn new(
        settings: Settings,
        support: Support,
        previous: Option<Durable>,
        corrupt: bool,
    ) -> Self {
        let mut result = Self {
            durable: Durable {
                phase: Phase::BlockedCorrupt,
                trigger: 0,
            },
            settings,
            support,
            usage: None,
            last_compacted: false,
            seen: 0,
            observed_millis: None,
            fresh_after: 0,
        };
        result.durable.phase = if corrupt || !settings.valid() {
            Phase::BlockedCorrupt
        } else if let Some(previous) = previous {
            result.durable.trigger = previous.trigger;
            result.last_compacted = previous.phase == Phase::Latched;
            match previous.phase {
                Phase::Executing | Phase::AwaitingConfirmation => Phase::OutcomeUnknownHold,
                Phase::ProviderCompacting => Phase::EventLossHold,
                phase if phase.prewrite() => {
                    result.durable.trigger = 0;
                    result.normal()
                }
                phase => phase,
            }
        } else {
            result.normal()
        };
        result
    }
    fn normal(&self) -> Phase {
        if !self.settings.enabled {
            Phase::Disabled
        } else if self.support == Support::Unsupported {
            Phase::Unsupported
        } else {
            Phase::Armed
        }
    }

    fn fresh(&self, now: u64) -> bool {
        self.observed_millis
            .is_some_and(|at| now >= at && now - at <= 30_000)
    }

    /// Returns true only when policy accepts this event. The caller compares
    /// the durable before/after snapshots to detect a persistence boundary.
    pub(crate) fn reduce(&mut self, signal: Signal, now: u64) -> bool {
        let phase = self.durable.phase;
        match signal {
            Signal::Provider { sequence, evidence } => {
                if sequence == 0 || sequence <= self.seen || self.support == Support::Unsupported {
                    return false;
                }
                match evidence {
                    Evidence::Usage { used, limit } if limit > 0 && now > self.fresh_after => {
                        self.usage = Some((used, limit));
                        self.observed_millis = Some(now);
                        let percent = u128::from(used) * 100;
                        if self.settings.enabled
                            && phase == Phase::Armed
                            && percent >= u128::from(limit) * u128::from(self.settings.threshold)
                        {
                            self.durable = Durable {
                                phase: Phase::PendingIdle,
                                trigger: self.settings.threshold,
                            };
                        } else if self.settings.enabled && phase == Phase::Latched {
                            let trigger = if self.durable.trigger > 0 {
                                self.durable.trigger
                            } else {
                                self.settings.threshold
                            };
                            if percent <= u128::from(limit) * u128::from(trigger.saturating_sub(10))
                            {
                                self.durable = Durable {
                                    phase: Phase::Armed,
                                    trigger: 0,
                                };
                                self.fresh_after = 0;
                            }
                        }
                    }
                    Evidence::CompactStarted if !phase.held() => {
                        self.durable.phase = Phase::ProviderCompacting
                    }
                    Evidence::CompactCompleted { .. }
                        if matches!(
                            phase,
                            Phase::Armed
                                | Phase::PendingIdle
                                | Phase::Prepared
                                | Phase::AwaitingConfirmation
                                | Phase::ProviderCompacting
                        ) =>
                    {
                        self.durable.phase = Phase::Latched;
                        self.fresh_after = now;
                        self.last_compacted = true;
                    }
                    _ => return false,
                }
                self.seen = sequence;
            }
            Signal::Settings(settings)
                if settings.valid() && settings.revision > self.settings.revision =>
            {
                self.settings = settings;
                self.observed_millis = None;
                self.fresh_after = now;
                if phase.prewrite() {
                    self.durable = Durable {
                        phase: self.normal(),
                        trigger: 0,
                    };
                }
            }
            Signal::Idle if phase == Phase::PendingIdle && self.fresh(now) => {
                self.durable.phase = Phase::Prepared
            }
            Signal::Busy if phase == Phase::Prepared => self.durable.phase = Phase::PendingIdle,
            Signal::Begin
                if self.support == Support::Automatic
                    && self.settings.enabled
                    && phase == Phase::Prepared
                    && self.fresh(now) =>
            {
                self.durable.phase = Phase::Executing
            }
            Signal::Written if phase == Phase::Executing => {
                self.durable.phase = Phase::AwaitingConfirmation
            }
            Signal::Unknown
                if matches!(
                    phase,
                    Phase::Executing | Phase::AwaitingConfirmation | Phase::ProviderCompacting
                ) =>
            {
                self.durable.phase = Phase::OutcomeUnknownHold
            }
            Signal::EventLoss if phase != Phase::BlockedCorrupt => {
                self.durable.phase = Phase::EventLossHold
            }
            Signal::Refused if phase == Phase::Executing => {
                self.durable = Durable {
                    phase: self.normal(),
                    trigger: 0,
                };
                self.observed_millis = None;
            }
            _ => return false,
        }
        true
    }
}

/// serde_json::Value alone would overwrite duplicate fields before checking the contract.
pub(crate) fn unique_keys(raw: &str) -> bool {
    use serde::de::{self, Deserializer, MapAccess, SeqAccess, Visitor};
    struct Unique;
    impl<'de> Deserialize<'de> for Unique {
        fn deserialize<D: Deserializer<'de>>(d: D) -> Result<Self, D::Error> {
            struct Scan;
            impl<'de> Visitor<'de> for Scan {
                type Value = Unique;
                fn expecting(&self, f: &mut std::fmt::Formatter) -> std::fmt::Result {
                    f.write_str("a unique-key JSON value")
                }
                fn visit_bool<E: de::Error>(self, _: bool) -> Result<Unique, E> {
                    Ok(Unique)
                }
                fn visit_i64<E: de::Error>(self, _: i64) -> Result<Unique, E> {
                    Ok(Unique)
                }
                fn visit_u64<E: de::Error>(self, _: u64) -> Result<Unique, E> {
                    Ok(Unique)
                }
                fn visit_f64<E: de::Error>(self, _: f64) -> Result<Unique, E> {
                    Ok(Unique)
                }
                fn visit_str<E: de::Error>(self, _: &str) -> Result<Unique, E> {
                    Ok(Unique)
                }
                fn visit_unit<E: de::Error>(self) -> Result<Unique, E> {
                    Ok(Unique)
                }
                fn visit_seq<A: SeqAccess<'de>>(self, mut seq: A) -> Result<Unique, A::Error> {
                    while seq.next_element::<Unique>()?.is_some() {}
                    Ok(Unique)
                }
                fn visit_map<A: MapAccess<'de>>(self, mut map: A) -> Result<Unique, A::Error> {
                    let mut keys = std::collections::BTreeSet::new();
                    while let Some((key, _)) = map.next_entry::<String, Unique>()? {
                        if !keys.insert(key) {
                            return Err(de::Error::custom("duplicate field"));
                        }
                    }
                    Ok(Unique)
                }
            }
            d.deserialize_any(Scan)
        }
    }
    serde_json::from_str::<Unique>(raw).is_ok()
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;
    const THREAD: &str = "01a05ce6-7524-7642-9833-beae667c99a7";
    fn policy(revision: u64) -> Settings {
        Settings {
            enabled: true,
            threshold: 80,
            revision,
        }
    }
    fn usage(sequence: u64, used: u64, limit: u64) -> Signal {
        Signal::Provider {
            sequence,
            evidence: Evidence::Usage { used, limit },
        }
    }
    fn done(sequence: u64) -> Signal {
        Signal::Provider {
            sequence,
            evidence: Evidence::CompactCompleted { legacy: false },
        }
    }

    #[test]
    fn pinned_source_and_exact_counter_shape() {
        for (version, claimed) in [
            ("0.151.0", Support::Automatic),
            ("0.150.7", Support::ObserveOnly),
            ("0.151.3", Support::ObserveOnly),
            ("0.151.00", Support::ObserveOnly),
            ("0.151.0-preview", Support::Unsupported),
            ("0.152.0", Support::Unsupported),
            ("0.172.0", Support::Unsupported),
        ] {
            assert_eq!(support(version), claimed);
        }
        let counters = |used| json!({"cachedInputTokens":0,"inputTokens":used,"outputTokens":0,"reasoningOutputTokens":0,"totalTokens":used});
        let mut frame = json!({"method":"thread/tokenUsage/updated","params":{"threadId":THREAD,"turnId":"turn",
            "tokenUsage":{"last":counters(81),"total":counters(9000),"modelContextWindow":100}}});
        assert_eq!(
            evidence(&frame, THREAD),
            Some(Evidence::Usage {
                used: 81,
                limit: 100
            })
        );
        frame["params"]["tokenUsage"]["last"]["totalTokens"] = json!(81.0);
        assert_eq!(evidence(&frame, THREAD), None);
        assert!(!unique_keys(
            r#"{"method":"ignore","params":{"used":1,"used":20}}"#
        ));
        assert!(unique_keys(
            r#"{"method":"test","params":{"usage":[1,false,null]}}"#
        ));
    }

    #[test]
    fn durable_handoff_and_hysteresis_use_exact_arithmetic() {
        let mut machine = Machine::new(policy(0), Support::Automatic, None, false);
        assert!(machine.reduce(usage(1, u64::MAX, u64::MAX), 1));
        assert_eq!(machine.durable.phase, Phase::PendingIdle);
        assert!(!machine.reduce(Signal::Idle, 30_002));
        assert!(machine.reduce(usage(2, 800, 1000), 30_003));
        assert!(machine.reduce(Signal::Idle, 30_004));
        assert!(machine.reduce(Signal::Begin, 30_005));
        let recovered = Machine::new(policy(0), Support::Automatic, Some(machine.durable), false);
        assert_eq!(recovered.durable.phase, Phase::OutcomeUnknownHold);
        assert!(machine.reduce(Signal::Written, 30_006));
        assert!(machine.reduce(done(3), 30_007));
        assert!(machine.reduce(usage(4, 701, 1000), 30_008));
        assert_eq!(machine.durable.phase, Phase::Latched);
        assert!(machine.reduce(usage(5, 700, 1000), 30_009));
        assert_eq!(machine.durable.phase, Phase::Armed);
        assert!(!machine.reduce(usage(4, 1000, 1000), 30_010));
    }

    #[test]
    fn settings_supersede_prewrite_and_high_versions_cannot_dispatch() {
        let mut machine = Machine::new(policy(2), Support::Automatic, None, false);
        machine.reduce(usage(1, 95, 100), 1);
        machine.reduce(Signal::Idle, 2);
        machine.reduce(
            Signal::Settings(Settings {
                enabled: false,
                ..policy(3)
            }),
            3,
        );
        assert!(!machine.reduce(Signal::Begin, 4));
        assert_eq!(machine.durable.phase, Phase::Disabled);
        let mut observe = Machine::new(policy(2), Support::ObserveOnly, None, false);
        observe.reduce(usage(1, 100, 100), 1);
        observe.reduce(Signal::Idle, 2);
        assert!(!observe.reduce(Signal::Begin, 3));
        let mut newest = Machine::new(policy(2), Support::Unsupported, None, false);
        assert!(!newest.reduce(usage(1, 100, 100), 1));
        assert_eq!(newest.durable.phase, Phase::Unsupported);
    }
}
