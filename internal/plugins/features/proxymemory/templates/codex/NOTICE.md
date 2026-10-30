# Vendored Codex memory prompt templates

These files are byte-for-byte (unmodified) copies of prompt templates from the
OpenAI Codex repository, licensed under the Apache License, Version 2.0
(<https://github.com/openai/codex/blob/0df4aa67781ae45a3c97befa4db48b734afe46d9/LICENSE>).

- Source repository: <https://github.com/openai/codex>
- Source commit: `0df4aa67781ae45a3c97befa4db48b734afe46d9`
- License: Apache-2.0
- Local provenance note: `prompts.go` renders these templates with literal
  `{{ name }}` replacement only; the vendored files themselves are untouched.

| Vendored file | Upstream path (at the commit above) | sha256 |
| --- | --- | --- |
| `stage_one_system.md` | `codex-rs/memories/write/templates/memories/stage_one_system.md` | `cf795e8a2f5f52d333af2613bf1ff79178112f5fd2161cc181a8ddf52e59da33` |
| `stage_one_input.md` | `codex-rs/memories/write/templates/memories/stage_one_input.md` | `2e54c74909238022305c269c862910bb29509fda8b58ce671ef011f8d6453047` |
| `consolidation.md` | `codex-rs/memories/write/templates/memories/consolidation.md` | `1450e24f84c03375aa5114c6c0857f515395129dcc00f65263221d03866852a0` |
| `ad_hoc_instructions.md` | `codex-rs/memories/write/templates/extensions/ad_hoc/instructions.md` | `d36a36083d92f9d44efbd95e0e4b6e81d7d149e812f2bca2009b6dd4b8aa93e7` |
| `read_path.md` | `codex-rs/ext/memories/templates/memories/read_path.md` | `2bc7736029884b714860a6f0d6b2fd26598bba58f2213b787f01fe2a491f4326` |

The digests above were computed with `sha256sum` over both the vendored files
and two independent `curl` downloads of each upstream raw file.
