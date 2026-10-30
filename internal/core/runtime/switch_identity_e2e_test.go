package runtime_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/prometheus/client_golang/prometheus"
)

func switchE2ETestKey() runtime.SeamDigestKey {
	var key runtime.SeamDigestKey
	for i := range key {
		key[i] = byte(0xA0 + i)
	}
	return key
}

func switchE2EUsageStream(cacheRead int, account string) aipapi.ManagedEventStream {
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{
			Kind:            aipapi.EventUsageDelta,
			InputTokens:     8000,
			CacheReadTokens: cacheRead,
			Accounting: aipapi.UsageAccountingMetadata{
				ProviderAccountKey: account,
			},
		},
		{Kind: aipapi.EventResponseFinished},
	})
}

// TestSwitchIdentity_EndToEnd drives the real executor, usage transport and
// collapse detector. It covers the R2 temporary-switch cases plus the R3
// connector-local provider-account path: a backend that reports no open-time
// credential but identifies account A then B in usage accounting must still
// produce credential-switch. Unknown adapter targets are exercised by leaving
// the target list empty.
func TestSwitchIdentity_EndToEnd(t *testing.T) {
	t.Parallel()
	scenarios := []struct {
		name        string
		targets     []string
		credentials []string
		accounts    []string
		want        string
		forbid      []string
	}{
		{
			name:    "temporary_cache_target_reverts",
			targets: []string{"target-a", "target-b", "target-a", "target-a", "target-a", "target-a"},
			want:    string(metrics.CollapseCauseUnexplained),
			forbid:  []string{string(metrics.CollapseCauseAffinitySwitch)},
		},
		{
			name:    "cache_target_switch",
			targets: []string{"target-a", "target-b", "target-b", "target-b", "target-b", "target-b"},
			want:    string(metrics.CollapseCauseAffinitySwitch),
		},
		{
			name:    "unknown_adapter_target_to_known",
			targets: []string{"", "target-b", "target-b", "target-b", "target-b", "target-b"},
			want:    string(metrics.CollapseCauseUnexplained),
			forbid:  []string{string(metrics.CollapseCauseAffinitySwitch)},
		},
		{
			name:    "known_adapter_target_to_unknown",
			targets: []string{"target-a", "", "", "", "", ""},
			want:    string(metrics.CollapseCauseUnexplained),
			forbid:  []string{string(metrics.CollapseCauseAffinitySwitch)},
		},
		{
			name:     "credential_usage_rotation_without_open_credential",
			targets:  []string{"target-a", "target-a", "target-a", "target-a", "target-a", "target-a"},
			accounts: []string{"account-a", "account-b", "account-b", "account-b", "account-b", "account-b"},
			want:     string(metrics.CollapseCauseCredentialSwitch),
		},
		{
			name:     "credential_usage_unchanged",
			targets:  []string{"target-a", "target-a", "target-a", "target-a", "target-a", "target-a"},
			accounts: []string{"account-a", "account-a", "account-a", "account-a", "account-a", "account-a"},
			want:     string(metrics.CollapseCauseUnexplained),
		},
		{
			name:        "credential_open_rotation",
			targets:     []string{"target-a", "target-a", "target-a", "target-a", "target-a", "target-a"},
			credentials: []string{"credential-a", "credential-b", "credential-b", "credential-b", "credential-b", "credential-b"},
			want:        string(metrics.CollapseCauseCredentialSwitch),
		},
	}
	for _, tc := range scenarios {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			detector := metrics.NewPromptCacheCollapseWithSettings(
				metrics.RegisterPromptCacheCollapseProm(prometheus.NewRegistry()),
				slog.New(slog.NewJSONHandler(&logs, nil)),
				nil,
				metrics.PromptCacheCollapseSettings{MinPromptTokens: 1},
			)
			st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			bus := hooks.New(hooks.Config{})
			snap := extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{UsageObserver: detector})
			ex := runtime.TestExecutor()
			ex.Store = st
			ex.Bus = bus
			ex.RuntimeSnapshot = snap
			ex.SystemPromptDigestEnabled = true
			ex.SystemPromptDigestKey = switchE2ETestKey()
			attempt := 0
			ex.Backends = map[string]execbackend.Backend{
				"openai": {
					Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
					Open: func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
						index := attempt
						attempt++
						if index >= len(tc.targets) {
							t.Fatalf("unexpected extra backend open %d", index)
						}
						execbackend.ReportAttemptIdentity(ctx, execbackend.AttemptIdentity{
							Cache:      tc.targets[index],
							Credential: stringAt(tc.credentials, index),
						})
						cacheRead := 0
						if index == 0 {
							cacheRead = 8000
						}
						return switchE2EUsageStream(cacheRead, stringAt(tc.accounts, index)), nil
					},
				},
			}
			ex.Rand = routing.NewSeededRng(1)
			first := &aipapi.Call{
				Route:    aipapi.RouteIntent{Selector: "openai:gpt-4"},
				Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hi")}}},
			}
			call := first
			for i := range tc.targets {
				if i > 0 {
					call = continuedStructuralCall(first, "openai:gpt-4", nil)
				}
				stream, err := ex.Execute(context.Background(), call)
				if err != nil {
					t.Fatalf("request %d: %v", i, err)
				}
				if _, err := aipapi.Collect(context.Background(), stream); err != nil {
					t.Fatalf("request %d collect: %v", i, err)
				}
			}
			text := logs.String()
			if !strings.Contains(text, `"cause":"`+tc.want+`"`) {
				t.Fatalf("missing cause %q in %s", tc.want, text)
			}
			for _, cause := range tc.forbid {
				if strings.Contains(text, `"cause":"`+cause+`"`) {
					t.Fatalf("unexpected cause %q in %s", cause, text)
				}
			}
		})
	}
}

func stringAt(values []string, index int) string {
	if index < 0 || index >= len(values) {
		return ""
	}
	return values[index]
}
