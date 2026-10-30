# OpenResponses Reference Client Test Data

Immutable fixtures for the independent `internal/refclient/openresponses` emulator.
The reference client must never reuse production OpenResponses codec packages; these
bytes are the only protocol inputs shared with production (`internal/plugins/protocols/openresponses`).

## Provenance

- Upstream repository: `openresponses/openresponses`
- Pinned commit: `92c12d96d7b61d6d15e2214daa5e9c6000ab6e1c`
- License: Apache-2.0

`official_examples/ResponseParam.json` and `official_examples/ResponseResource.json`
are byte-for-byte copies of the upstream `src/examples/*.json` fixtures.

`scenarios/` contains repo-owned declarative scenario fixtures (not upstream artifacts)
that pin expected semantic observations for the client emulator.
