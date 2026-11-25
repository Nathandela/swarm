use std::ffi::OsString;

// An execution-only fleet host receives the exact binary beside its test binaries.
pub fn executable() -> OsString {
    std::env::var_os("SWARM_TEST_BIN").unwrap_or_else(|| env!("CARGO_BIN_EXE_swarm").into())
}
