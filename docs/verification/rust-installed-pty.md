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
