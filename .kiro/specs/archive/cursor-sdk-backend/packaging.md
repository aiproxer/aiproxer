# Packaging and Configuration — cursor-sdk-backend

## Release metadata (planned)

```yaml
schema: golip.connector.release/v1
plugin_id: io.golip.backend.cursorsdk
factory_kind: cursorsdk
module: github.com/matdev83/go-llm-interactive-proxy/connectors/cursorsdk
command: ./cmd/lip-backend-cursorsdk
manifest_template: manifest/template.backendplugin.json
version: 0.1.0
build_id: localdev
tag: cursorsdk-v0.1.0
profiles:
  - full
published_root_module: github.com/matdev83/go-llm-interactive-proxy
replace_policy: development-replace-to-monorepo-root
private_companions:
  - bridge-node
```

## Closed manifest (planned)

```json
{
  "schema": "golip.backendplugin.manifest/v1",
  "plugin_id": "io.golip.backend.cursorsdk",
  "exports": [
    {
      "kind": "cursorsdk",
      "credential_mode": "static",
      "access_scope": "local_only",
      "process_sharing": "per_instance"
    }
  ]
}
```

Manifests remain installation metadata only: no secrets, no npm hooks, no download URLs, no catalogs, no arbitrary argv/env maps.

## Operator configuration (discovered kind)

```yaml
plugins:
  backend_discovery:
    enabled: true
    paths: ["…trusted plugin root…"]
  backends:
    - kind: cursorsdk
      id: cursor-sdk-1
      enabled: true
      config:
        api_key: ""              # or CURSOR_API_KEY via host secret injection
        bridge_executable: ""    # optional absolute path under trusted tree
        default_workspace: ""
        max_agents: 8
        cancel_timeout: 5s
        stale_kill_delay_seconds: 0.5
```

## Lifecycle (host + connector)

1. Host discovers closed manifest (no process launch).
2. Digest-bound trust binds exact native `lip-backend-cursorsdk` bytes.
3. Lazy activation: approved secure local IPC after CreateProcess/job or platform equivalent.
4. Configure exports `cursorsdk`; secrets never on argv.
5. Connector may lazily spawn the packaged Node bridge companion over adapter-private stdio NDJSON.
6. Cancel: provider cancel → bounded transport kill → process-tree cleanup.
7. Shutdown: reject new work, dispose agents, bridge shutdown, reap children; host closer reap of plugin process.

## Structural discovery

`tools/backendplugin/discover_modules` and package indexes must find `connectors/cursorsdk/go.mod` + `release.yaml` the same way as other connectors. Bridge-node is a `private_companion`, not a separate Go module and not a root workspace package.
