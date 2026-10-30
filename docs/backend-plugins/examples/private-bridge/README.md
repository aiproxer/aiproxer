# Private Node/Python bridge skeleton

This skeleton shows how a connector may own a private companion process without pairwise protocol translators or root-module leakage.

- `bridge.js` / `bridge.py` are never imported by AIProxer core or `pkg/aipsdk`.
- The Go connector executable (not shown here) may spawn the companion with a private IPC of its own after host configure.
- Packaging keeps companions under `<plugin>/private/`.

Do not add a Go import path from the root module into this directory.
