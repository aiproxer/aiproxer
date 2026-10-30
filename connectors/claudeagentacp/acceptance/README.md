# Spawned-stack turn acceptance (#1314)

From the repository root on Linux/amd64, with Go and Python 3.9+:

```sh
make claude-turn-acceptance
```

The target builds `aipstd`, runs the normal Claude connector packaging command,
starts both as separate processes, and launches an external fake ACP peer. No
provider credentials or network access are needed beyond Go build dependencies.
Scratch/evidence stays outside the repository; the printed directory retains
build logs, the rendered config, HTTP receipts, peer PID/prompt records, the build
overlay and its source, host logs, and the result with the plugin digest.

| Claim | Source | Failure defended against |
| --- | --- | --- |
| Two turns and the byte-identical second-turn retry return `ok`, `41`, `41` | #1314 original request shapes | shared/global runtime identity or stale turn claim |
| Two concurrent conversations retain their own numbers through retries | #1314 acceptance | cross-conversation runtime/history reuse |
| Fresh work succeeds while an aborted turn's cleanup join is held | #1314 delayed Close/Release acceptance | synchronous cleanup blocks unrelated admission |
| All fake ACP children are gone after host shutdown | pool ownership contract | child cleanup abandoned at shutdown |
| Only the deliberately disconnected stream logs an attempt cancellation | #1314 cancellation investigation | successful ordinary requests canceled as transport loss |

The fixture uses the existing private `engineDeps` construction seam **only in
the packaged test build**, through a Go overlay. It substitutes runtime byte
certification and the external login probe, and decorates the real OS process's
`Wait` to hold one cleanup join after physical extinction. The production
supervisor and its kill policy are unchanged. A filesystem gate establishes
ordering; polling retries a condition under a deadline, not an assumed duration.
The shared pool, transcript mapping, identity admission, plugin RPC, packaging,
host session assignment and HTTP frontend remain real. Production source is not
edited and there is no production flag accepting uncertified bytes.

Concurrent HTTP clients rendezvous after submitting their requests and before
consuming either response. The host's existing per-instance `Session.execMu`
serializes plugin Execute calls, so this asserts concurrent outstanding client
conversations, not simultaneous ACP prompts. Shared-pool concurrency regressions
cover simultaneous process admission separately.

This is **not** a production-runtime or live-provider certification. The original
reproduction against the exact pinned Node/Claude ACP installation and an
authorized account remains a separate acceptance gate. In particular, a correct
`client_gone` (or `context_done`) event for this test's intentional disconnect does not explain the
reported live cancellation while the client was still waiting.
