# Official Codex npm launcher fixture

`npm-codex.js` is one unmodified copy of OpenAI's Apache-2.0 licensed npm
entrypoint. Copyright and license notices are retained in `NOTICE` and `LICENSE`.

Both qualified release sources contain the exact same bytes:

| Release | Upstream source commit |
| --- | --- |
| `rust-v0.163.0` | [`83086188ae0614bd882818ff1aa23f3194962012`](https://github.com/openai/codex/blob/83086188ae0614bd882818ff1aa23f3194962012/codex-cli/bin/codex.js) |
| `rust-v0.164.0` | [`6aaac694803004a63c6f393d153c96d22521a6b9`](https://github.com/openai/codex/blob/6aaac694803004a63c6f393d153c96d22521a6b9/codex-cli/bin/codex.js) |

SHA-256: `61b0194f3bb6534439c8d26a3ed57d0805f84b884588b761795323eeb92fcf70`.
The source tags, source commits and hash were checked against OpenAI's GitHub
repository on 2026-10-24. Tests use this repository fixture without network access.
