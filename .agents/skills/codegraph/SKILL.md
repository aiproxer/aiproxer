---
name: codegraph
description: Use CodeGraph to understand code through its AST-derived symbol and relationship graph. Use for architecture, implementations, callers/callees, call paths, dependency direction, change impact, affected tests, or structural code navigation in an indexed repository. CodeGraph is a read-only exploration tool, never an agent or delegation target.
---

# CodeGraph

Query CodeGraph directly. It is a local code index, not a worker: never delegate, assign, or prompt it to perform tasks.

## Workflow

1. In a new worktree, finish `codegraph init` before moving the session there or attaching its MCP watcher; successful initialization is the readiness criterion. Check `codegraph status`, and run `codegraph sync` only when status is stale or no watcher is active. For locks or cleanup, use the lifecycle guidance below.
2. Start with one `codegraph_explore` MCP call, or its CLI equivalent:

   ```text
   codegraph explore --max-files 8 "internal/infra/runtimebundle/host_build.go: BuildHost -> buildProcessServicesOp -> NewProcessServices; show the call path, implementations, ownership, tests, and blast radius"
   ```

   Name the relevant file/package and 2–6 distinctive symbols. State the relationship or flow sought. Avoid broad names such as `Request`, `Context`, or `Stream`.
3. Treat returned line-numbered source as already read. If it answers the question, stop; do not repeat the lookup with file reads, FFF, or ripgrep. If the display is truncated, read the missing range from its saved output instead of repeating the query. For general retrieval and output sizing, use `docs/agent-workflow.md` (Exploration).
4. If incomplete, issue one narrower `explore` using symbols learned from the first result. Use specialized commands only for a precise follow-up:
   - `node SYMBOL -f FILE`: exact body plus caller/callee trail; disambiguate common names.
   - `query NAME --kind KIND`: locate an unknown symbol name.
   - `callers` / `callees`: one graph hop.
   - `impact SYMBOL --depth N`: transitive pre-edit blast radius.
   - `affected FILE...`: candidate tests for changed source files.
   - Add `--json` only for machine processing.
5. Keep `--max-files` as small as useful. Verify any ambiguous or heuristic edge against the returned source. Heed stale-index warnings.

Use FFF/ripgrep instead for exact text, filenames, configuration, documentation, or logs. Fall back to file reads only when the graph is absent, stale for the referenced file, unsupported, or genuinely incomplete.

## Index lifecycle

A live lock owner is an initialization/attachment conflict: retain the lock, identify the owner, and coordinate attachment rather than repeatedly retrying initialization. Before removing a task worktree, move the session out and release only its task-owned index processes; preserve other sessions' watchers.
