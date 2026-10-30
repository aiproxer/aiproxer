# File Plan — cursor-sdk-backend

Planned tree for a future **external** implementation (not present in root today).

```text
connectors/cursorsdk/
  go.mod
  go.sum
  release.yaml
  manifest/template.backendplugin.json
  cmd/lip-backend-cursorsdk/          # plugin executable entry (backendplugin server)
  internal/
    service/                         # Describe/Configure/Execute/ListModels
    bridge/                          # Go process owner + NDJSON RPC client
    pool/                            # agent pool + history fingerprint
    stream/                          # canonical ManagedEventStream mapping
    inventory/                       # structured models/list
    config/                          # YAML decode (api_key, bridge_path, bounds)
  bridge-node/                       # project-owned Node companion (NOT root package.json)
    package.json                     # exact @cursor/sdk pin + lockfile
    package-lock.json
    src/                             # SDK import boundary only
    dist/                            # packaged entry for companion executable/script
  testdata/
    fake-bridge/                     # deterministic emulator for Go tests
```

## Root forbidden paths (architecture gates)

| Path / import | Reason |
| --- | --- |
| `internal/plugins/backends/cursorsdk/**` | Product must not land in root backends tree |
| Root `package.json` / `node_modules` for Cursor SDK | Node runtime stays inside connector companion |
| Root `go.mod` `require`/`replace` of `connectors/cursorsdk` | Root isolation |
| `pkg/lipapi` / `pkg/lipsdk` Cursor SDK types | Canonical packages stay provider-neutral |
| `internal/core/**` importing `connectors/cursorsdk` or `@cursor/sdk` | Core isolation |
| Generic `BackendFactoryDeps` / migration deps naming Cursor SDK | Factory hygiene |

## Packaging files

- `release.yaml`: `plugin_id: io.golip.backend.cursorsdk`, `factory_kind: cursorsdk`, `module: .../connectors/cursorsdk`, `private_companions` listing bridge-node pack assets.
- Closed manifest exports one kind `cursorsdk` with `credential_mode: static`, `access_scope: local_only`, `process_sharing: per_instance`.
- Bridge-node files ship beside the native Go executable under the trusted plugin install root; discovery never executes npm install.
