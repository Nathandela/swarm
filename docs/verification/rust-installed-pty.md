# Installed Rust PTY recovery rehearsal

The signed Swarm 0.1.0 artifacts were built from `5e42c2cf3cba0c5c05a938bf97378ae23b973134`. Each witness verified the archive with the disposable Ed25519 public key, bootstrapped a fresh managed prefix inside its scratch root, then executed the installed stable `prefix/swarm` launcher for the daemon, CLI and board. The sandbox blocked external network traffic.

| Native host | Signed archive SHA-256 | Installed Swarm SHA-256 |
| --- | --- | --- |
| macOS arm64 | `59a5e2cf27de75062e3f91bb1b86973973d0f3bbfb1bdea4900cdc954e45bde0` | `6e094c861110bcffeafdeb425d7ac69ea63391a91b4d73a8147c5c609ba4fb5c` |
| Linux x86_64 | `c3b61207cc308528b2b3be75db4fb478ecba1ea5ae5964ff2cd2b3a2e3152c41` | `4daf7038b3b9b7cc35dd773f8dc8f9ebf05ab5d33c85319f24f3115df952b521` |

Both receipts show the pinned bundled media self-check, PTY launch and attach, input and resize, daemon SIGKILL, and restoration of the same session and worker birth identity from a different daemon PID. The shim's writer lease remained held with the old daemon exited, then released after the worker and replacement daemon exited. The installed generation stayed selected, the terminal settings were restored and cleanup reported no survivors. These checks cover the shipped media self-test and PTY lifecycle. A live encrypted call has its own qualification gate.

Run the witness with an unused evidence path and space to unpack the selected archive. On the Linux test VM, `TMPDIR=/dev/shm` supplied disposable space after the root volume's `/tmp` filled.

```sh
python3 scripts/verification/parity/installed_board.py \
  --dist /path/to/signed-dist \
  --archive swarm-rust_0.1.0_linux_amd64.tar.gz \
  --public-key /path/to/trusted-disposable-public.pem \
  --evidence /tmp/installed-pty.json
```

## Separate first-hop and ownership canaries

The genuine v0.29.22 Go binary read the public stable release metadata and reported itself current. Its GOBIN copy refused self-staging and identified Go as the owner. The native archive installer refused the occupied predecessor prefix and the Go executable retained its SHA-256. A new private managed prefix passed signed installation, reported Rust 0.1.0 and passed the packaged media check. The old explicit path still reported Go v0.29.22. The side-by-side canary left Go state in its original custody.

The exact Rust image also refused self-staging through a disposable Caskroom symlink and through a private dpkg database, reporting the relevant package manager. These package-owner probes used isolated copies to represent the external owners. The installed PTY witness above exercises the managed launcher and its leases. No existing service or package installation changed.


## Linux arm64 managed generation

The same signed `5e42c2cf` package was installed and exercised inside an offline ARM64 Linux guest on Apple Silicon. The ordinary test user (UID 65534) launched the dashboard and both daemon generations by the stable managed path. The surviving shim kept the writer lock after SIGKILL. The replacement daemon reattached to the same worker. The lease was released after worker exit, terminal settings matched the original, and the reaper reported zero survivors. The bundled native media self-check passed. The guest powered down with status 0.

| Item | SHA-256 |
|---|---|
| Signed Linux arm64 archive | `73bc972cf5691acbcc8a6f0a8a4186ec3eefaa4dd449643892aec5405a662cbe` |
| Executed Rust generation | `676d8687c8318627bd6c4891cbdf3a6483b42cfbf264ac31f4ae7d126080599e` |
| Full guest serial capture | `a23fd61f326b91adf2aa30ed8a312d196eff6081bfa9d9f2d40b843a3f2414db` |

The CPU architecture was native to the host. The guest kernel was LinuxKit 6.10.14 with an ext4 filesystem and an isolated Debian Bookworm userland. The guest setup and kernel hash are recorded in the [archive rehearsal](rust-managed-upgrade.md). The stable-launcher test used witness revision `64b037f5`.


## macOS x86_64 under Rosetta

The same signed universal archive passed the stable-launcher rehearsal from an x86_64 Python process under Rosetta on the Apple Silicon Mac. It rejoined the same worker, restored terminal settings and observed the writer lease across daemon death and release after worker exit. An independent macOS sample identified the actual media-checking Swarm process as `X86-64 (translated)`. The signed package's cold media check took 24.56 seconds in this translated rehearsal. The installed-package witness times media verification separately from terminal input and recovery. The receipt SHA-256 is `666bddaba33fc8d46271cf199f45edb4fc2663f361cf839b8847bd144059c215`. Native Intel hardware was not exercised in this run.

## Signed development checkpoint from November 26

The signed archives for `ded21c92e384ac8560827ce11d3567da0ed190a6` passed the managed install, packaged-media self-check, writer-lease continuity, PTY recovery and fixture cleanup on all four exercised targets. The ARM Linux guest was offline under Apple's Virtualization framework. This checkpoint uses a disposable signing key.

| Execution environment | Archive SHA-256 | PTY receipt SHA-256 |
| --- | --- | --- |
| macOS arm64 native | `bb34a9c08b9074e990611630d63a780bedb59d5be0fa0adcdb11c3bacdca6d5f` | `cc21db32c0b0349912dac963927120192321268fb9321cde4df89b5f9ad73398` |
| macOS x86_64 under Rosetta | `bb34a9c08b9074e990611630d63a780bedb59d5be0fa0adcdb11c3bacdca6d5f` | `3748dda299a04e671f8bd279bbc48e947995f5cc879624996c7171d6b2ab719a` |
| Linux x86_64 native | `23002ac6613ad459054ab122a0e12df8a1a2fdc0b232957ad3b3196767ad5c64` | `895b19c8730caa4576667abcde320c3f2d309f673adde45aefd6d5c5476fec8d` |
| Linux arm64 guest on Apple Silicon | `cd06e43267757469dfbaaaac11b13ed531bc136787fc30130c86bb2253a34ee7` | `f4c11874298033b9fe107c35993e77b8d77163f423ea8cd807f6e2539b57291f` |
