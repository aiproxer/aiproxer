#!/usr/bin/env bash
# Enforce a tiny allowlist of explicit `go` statements in non-test code.
# Run from repository root. Requires ripgrep (rg).

set -euo pipefail

if ! command -v rg >/dev/null 2>&1; then
	echo "check-adhoc-goroutines: ripgrep (rg) not found; install rg or skip in CI where guaranteed."
	exit 0
fi

mapfile -t hits < <(
	rg --files-with-matches --glob '!*_test.go' '^\s+go\s' internal pkg cmd connector-support connectors 2>/dev/null | sed 's#\\#/#g' | sort -u || true
)

# Exact-path allowlist for intentional owned workers / stream pumps only.
# internal/core/terminalwork/app/processor.go: Start/Run/Shutdown owner, ProcessDue
# claim fan-out, per-claim renew loop, and tick/renew ticker pumps (Phase 4.4).
# internal/core/terminalwork/app/ambiguous_append_reconciler.go: one process-owned
# worker draining WorkID-keyed ambiguous append ownership (task 3.6 remediation B).
# internal/infra/runtimehost/shutdown.go: bounded fan-out retiring retained
# generations concurrently so one pinned generation cannot stall unrelated drains.
# internal/infra/runtimehost/manager.go: one-goroutine-per-replaced-generation
# background retirement (scheduleRetire) armed by Publish (task 7.3).
# internal/stdhttp/admin/configreload/server.go: process-owned management HTTP
# listen/serve worker (task 5.3; separate from data-plane generation host).
# cmd/aipstd/reload_signal_adapter_unix.go: one process-owned SIGHUP worker
# delivering bounded reload triggers into the coordinator sink (task 5.2).
# internal/infra/runtimebundle/backend_resource_pool.go: pool-owned builder
# worker canceled by Pool.Close and joined before physical cleanup completes.
# internal/infra/geoip/service.go: process-owned managed updater scheduler and
# bounded context-join watcher, both stopped/joined by Service.Close.
# internal/core/auxreq/background.go: process-owned bounded auxiliary worker
# pool; every worker is joined by Shutdown and no per-request fallback exists.
# enterprise/internal/core/billing/economic_revision_worker.go: one process-owned polling
# worker; Start arms cancel/done and Stop cancels then joins.
# internal/infra/runtimebundle/observation_economic_bridge.go: one
# generation-owned relay worker; Start arms cancel/done and Stop joins.
# pkg/aipsdk/backendplugin/forward_execute.go: one bounded cancel watcher per
# plugin Execute stream, disarmed via stopWatch when the pump returns (M3).
# pkg/aipsdk/backendplugin/forward_execute_active.go: reader pumps and optional stream
# closer / cancel worker coordinated and joined by ForwardExecute.
# internal/plugins/frontends/openresponses/websocket_upgrade.go: per-session read
# pump + pinger owned and joined by WSSession.Run before it returns (Task 6.1).
# internal/plugins/frontends/openresponses/websocket_turn.go: one peer-close
# watcher per in-flight turn, owned and joined by executeTurn's deferred stop
# (Task 6.2); exits on peer close or derived-context cancel.
# connector-support/acp/runtime_pool.go: one-goroutine-per-detached-incarnation
# background retirement, so a process-tree cleanup that never confirms extinction
# cannot block the request path (same shape as the runtimehost per-generation
# retirement above).
# connector-support/acp/scripted_stdio.go: in-process scripted mock ACP agent background loop for connector tests.
# connectors/codex/cmd/fake-codex-cli/main.go: deterministic emulator grandchild process waiter for e2e tests.
# internal/testkit/conformance/acp_connector.go: test harness stderr drain for the
# launched connector process (conformance matrix harness; test-only package).
# internal/testkit/conformance/connector_host.go: test harness stderr drain for the
# launched connector processes (conformance matrix harness; test-only package).
# internal/infra/backendplugins/processhost/drain.go: one process-owned drain worker
# per launched connector pipe (stdout/stderr), owned and joined by the host slot
# reaper so an unread pipe can never block the connector.
# proxy-memory capture owns one bounded writer; worker owns one cancellable tick
# loop. Both are joined by their generation lifecycle Stop methods.
# connectors/openaichatgptplan/internal/siwc/callback.go: attempt-owned loopback
# listener, started and closed with one SIWC authorization attempt.
bad=()
for f in "${hits[@]}"; do
	case "$f" in
	internal/plugins/features/proxymemory/capture.go | internal/plugins/features/proxymemory/worker.go) ;;
	internal/stdhttp/server.go | internal/stdhttp/generation_host.go | internal/stdhttp/admin/configreload/server.go | internal/infra/runtimehost/shutdown.go | internal/infra/runtimehost/manager.go | internal/core/stream/keepalive.go | internal/core/runtime/parallel_race.go | internal/core/runtime/executor_execute_large_body.go | internal/core/leglifecycle/coordinator.go | internal/core/runtime/lease_heartbeat.go | enterprise/internal/core/billing/handoff_retry_worker.go | internal/core/auxreq/background.go | internal/core/extensions/decision_timeout.go | internal/plugins/frontends/holdalive/wait.go | internal/infra/runtimebundle/modelcatalog_refresh_loop.go | internal/infra/runtimebundle/backend_resource_pool.go | internal/infra/geoip/service.go | connector-support/acp/transport_stdio.go | connector-support/acp/runtime_pool.go | connector-support/acp/scripted_stdio.go | connectors/codex/cmd/fake-codex-cli/main.go | internal/core/terminalwork/app/processor.go | internal/core/terminalwork/app/ambiguous_append_reconciler.go | internal/plugins/features/keepwarm/manager.go | internal/plugins/features/keepwarm/scheduler.go | internal/plugins/features/keepwarm/lifecycle.go | enterprise/internal/core/billing/post_turn_worker.go | enterprise/internal/core/billing/call_post_usage_worker.go | enterprise/internal/core/billing/call_provider_cost_worker.go | enterprise/internal/infra/billingspool/spool.go | enterprise/internal/core/billing/append_outbox.go | enterprise/internal/core/billing/economic_revision_worker.go | internal/infra/runtimebundle/observation_economic_bridge.go | cmd/aipstd/reload_signal_adapter_unix.go | pkg/aipsdk/backendplugin/host/session.go | internal/infra/backendplugins/adapter/stream.go | internal/infra/backendplugins/processhost/drain.go | internal/infra/backendplugins/processhost/launch_linux.go | internal/infra/backendplugins/processhost/launch_windows.go | pkg/aipsdk/backendplugin/server.go | pkg/aipsdk/backendplugin/forward_execute.go | pkg/aipsdk/backendplugin/forward_execute_active.go | internal/plugins/frontends/openresponses/websocket_upgrade.go | internal/plugins/frontends/openresponses/websocket_session.go | internal/plugins/frontends/openresponses/websocket_turn.go | internal/testkit/conformance/acp_connector.go | internal/testkit/conformance/connector_host.go | internal/testkit/contract/frontend/harness.go | connectors/openaichatgptplan/internal/siwc/callback.go) ;;
	*) bad+=("$f") ;;
	esac
done

if [[ ${#bad[@]} -gt 0 ]]; then
	echo "ERROR: disallowed explicit goroutine spawn in non-test code (use long-lived workers / stream pumps; update allowlist in scripts/check-adhoc-goroutines.* only when intentional):"
	printf '  %s\n' "${bad[@]}"
	exit 1
fi

echo "OK: ad-hoc goroutine allowlist check passed (${#hits[@]} allowed file(s))"
