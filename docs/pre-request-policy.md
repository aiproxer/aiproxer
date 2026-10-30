# Pre-Request Policy Feature

`pre-request-policy` adds chainable admission handlers after canonical request shaping and before route planning for the primary model.

Each handler calls an auxiliary model through the proxy. The auxiliary output is used only for policy matching and is never forwarded to the client.

Example:

```yaml
server:
  pre_request_keepalive:
    enabled: false
    interval: 15s

plugins:
  features:
    - id: pre-request-policy
      enabled: true
      config:
        prompt_dir: ./config/prompts/pre_request
        handlers:
          - id: corporate-compliance
            priority: 10
            prompt_filename: compliance.md
            model_routing_string: openai:gpt-4.1-mini
            policy: allow_on_pattern
            allow_pattern: '\bALLOW\b'
            deny_message: "Request denied by corporate policy."
```

Policies:

- `deny_on_pattern`: allow unless `deny_pattern` matches the auxiliary model output.
- `allow_on_pattern`: deny unless `allow_pattern` matches the auxiliary model output.

`prompt_filename` in YAML configuration must be a plain filename under `prompt_dir`; path traversal and subdirectories are rejected during configuration decoding. Directly constructed `NewHandlers(Config)` inputs also receive a lexical path-boundary check before file loading.

`prompt_dir` is trusted operator configuration, not a filesystem sandbox. Prompt reads follow filesystem symlinks; a lexical path check does not confine symlink targets. Keep the prompt directory and its entries under trusted control, and do not use this feature to read filenames or directories supplied by untrusted request clients.

For streaming clients, `server.pre_request_keepalive` may emit HTTP `102 Processing` informational responses while admission is pending. It does not commit the final response status, so normal protocol errors such as a `403` denial remain unchanged.
