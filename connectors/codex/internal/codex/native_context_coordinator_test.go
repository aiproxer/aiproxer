package codex

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

type coordinatorCompactorFunc func(context.Context, CompactionRequest) (CompactionResult, error)

func (f coordinatorCompactorFunc) Compact(ctx context.Context, req CompactionRequest) (CompactionResult, error) {
	return f(ctx, req)
}

func TestNativeContextCoordinator_MatrixModesAndTransports(t *testing.T) {
	t.Parallel()
	for _, account := range []string{"static", "managed-a"} {
		for _, transport := range []string{"http", "websocket"} {
			for _, mode := range []struct {
				name       string
				reasoning  bool
				compaction bool
			}{
				{name: "disabled"},
				{name: "reasoning-only", reasoning: true},
				{name: "compaction-only", compaction: true},
				{name: "full", reasoning: true, compaction: true},
			} {
				name := account + "/" + transport + "/" + mode.name
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					cfg := Config{BaseURL: "http://127.0.0.1", AccountID: account, HTTPClient: http.DefaultClient}
					cfg.NativeContext = &NativeContextConfig{
						Enabled:                   mode.reasoning || mode.compaction,
						RequestEncryptedReasoning: mode.reasoning,
						ReasoningContinuity:       ContinuityDisabled,
						Compaction:                NativeCompactionConfig{Enabled: mode.compaction, TriggerTokens: 100, RetainedMessageTokens: 1, MinSavingsTokens: 1},
					}
					if !mode.reasoning && mode.compaction {
						cfg.NativeContext.ReasoningContinuity = ContinuityBestEffort
					}
					coord := newNativeContextCoordinatorWithCompactor(cfg, "instance-1", coordinatorCompactorFunc(func(context.Context, CompactionRequest) (CompactionResult, error) {
						t.Fatal("disabled/non-trigger matrix cell issued compaction")
						return CompactionResult{}, nil
					}))
					if !mode.compaction && coord != nil {
						t.Fatalf("mode %s allocated a compaction coordinator", mode.name)
					}
				})
			}
		}
	}
}

func TestNativeContextCoordinator_PreparesAfterSelectedIdentityAndMarker(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Session: aipapi.SessionRef{AuthoritativeSessionID: "proxy-session"},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("old")}},
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("latest")}},
		},
	}
	payload := Payload{Model: "selected-model", Input: []inputItem{textMessageItem{Type: "message", Role: "user", Content: "old"}}}
	var got CompactionRequest
	coord := newNativeContextCoordinatorWithCompactor(Config{
		BaseURL: "http://selected", AccountID: "account-b", NativeContext: &NativeContextConfig{
			Enabled: true, RequestEncryptedReasoning: true, ReasoningContinuity: ContinuityBestEffort,
			Compaction: NativeCompactionConfig{Enabled: true, TriggerTokens: 1, RetainedMessageTokens: 0, MinSavingsTokens: 0},
		},
	}, "instance-1", coordinatorCompactorFunc(func(_ context.Context, req CompactionRequest) (CompactionResult, error) {
		got = req
		return CompactionResult{Item: opaqueResponseItem{raw: []byte(`{"type":"compaction","id":"cmp","encrypted_content":"opaque"}`)}}, nil
	}))
	if coord == nil {
		t.Fatal("expected coordinator")
	}
	prepared, err := coord.Prepare(context.Background(), NativeContextPrepareInput{
		Call:           call,
		Payload:        payload,
		Account:        Config{BaseURL: "http://selected", AccountID: "account-b"},
		Model:          "selected-model",
		MarkerEligible: true,
		ClientFamily:   "codex",
		ConversationID: "conversation-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Account.AccountID != "account-b" || got.Payload.Model != "selected-model" || got.Conversation != "conversation-b" {
		t.Fatalf("selected identity was not used: account=%q model=%q conversation=%q", got.Account.AccountID, got.Payload.Model, got.Conversation)
	}
	if len(got.Payload.Input) != 1 || isCompactionTrigger(got.Payload.Input[0]) {
		t.Fatalf("compaction request input = %#v", got.Payload.Input)
	}
	if prepared.Payload.Model != "selected-model" || !prepared.Compacted {
		t.Fatalf("prepared = %+v", prepared)
	}
	telemetry := coord.telemetry.snapshot()
	if telemetry.CompactionProtocolFails != 0 || telemetry.CompactionRewriteFails != 0 || telemetry.CheckpointCommits != 1 || telemetry.CompactionSuccesses != 1 {
		t.Fatalf("preparation outcome = %s", telemetry)
	}
}

func TestNativeContextCoordinator_ManagedRetryRebuildsFromOriginalBaseline(t *testing.T) {
	t.Parallel()
	call := testCoordinatorCall()
	payload := testCoordinatorPayload()
	var seen []CompactionRequest
	coord := newNativeContextCoordinatorWithCompactor(Config{NativeContext: &NativeContextConfig{
		Enabled: true, ReasoningContinuity: ContinuityBestEffort,
		Compaction: NativeCompactionConfig{Enabled: true, TriggerTokens: 1, RetainedMessageTokens: 0, MinSavingsTokens: 0},
	}}, "instance", coordinatorCompactorFunc(func(_ context.Context, req CompactionRequest) (CompactionResult, error) {
		seen = append(seen, req)
		return CompactionResult{Item: opaqueResponseItem{raw: []byte(`{"type":"compaction","id":"cmp","encrypted_content":"opaque"}`)}}, nil
	}))
	in := NativeContextPrepareInput{Call: call, Payload: payload, Model: "model", MarkerEligible: true, ConversationID: "conv"}
	in.Account = Config{AccountID: "account-a"}
	first, err := coord.Prepare(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	in.Account.AccountID = "account-b"
	keyB := coord.keyFor(in, "")
	if keyB.AccountID != "account-b" {
		t.Fatalf("account key = %+v", keyB)
	}
	second, err := coord.Prepare(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0].Account.AccountID != "account-a" || seen[1].Account.AccountID != "account-b" {
		t.Fatalf("compaction accounts = %#v", seen)
	}
	if len(first.Payload.Input) == 0 || len(second.Payload.Input) == 0 || inputItemType(seen[1].Payload.Input[0]) == "compaction" {
		t.Fatalf("account retry inherited rewritten checkpoint: first=%#v second=%#v", first.Payload.Input, second.Payload.Input)
	}
}

func TestNativeContextCoordinator_MissingAuthoritySkipsCompactor(t *testing.T) {
	t.Parallel()
	called := false
	coord := testCoordinator(t, coordinatorCompactorFunc(func(context.Context, CompactionRequest) (CompactionResult, error) {
		called = true
		return CompactionResult{}, nil
	}), true)
	in := NativeContextPrepareInput{Call: aipapi.Call{}, Payload: testCoordinatorPayload(), Account: Config{AccountID: "account"}, Model: "model", MarkerEligible: true}
	if _, err := coord.Prepare(context.Background(), in); !errors.Is(err, ErrNativeContextAuthority) {
		t.Fatalf("error = %v", err)
	}
	if called {
		t.Fatal("compactor accessed without authoritative session")
	}
}

func TestNativeContextCoordinator_OldABIWithoutTypedAuthorityKeepsFullHistory(t *testing.T) {
	t.Parallel()
	coord := testCoordinator(t, coordinatorCompactorFunc(func(context.Context, CompactionRequest) (CompactionResult, error) {
		t.Fatal("old ABI must bypass compaction")
		return CompactionResult{}, nil
	}), true)
	payload := testCoordinatorPayload()
	prepared, err := coord.Prepare(context.Background(), NativeContextPrepareInput{
		Call:           testCoordinatorCallWithoutAuthority(),
		Payload:        payload,
		Account:        Config{AccountID: "account"},
		Model:          "model",
		MarkerEligible: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Compacted || prepared.ReusedCheckpoint || len(prepared.Payload.Input) != len(payload.Input) {
		t.Fatalf("old ABI lost or rewrote history: %+v", prepared)
	}
}

func TestNativeContextCoordinator_FailureFallsBackOnlyWhenFullHistoryFits(t *testing.T) {
	t.Parallel()
	coord := testCoordinator(t, coordinatorCompactorFunc(func(context.Context, CompactionRequest) (CompactionResult, error) {
		return CompactionResult{}, errors.New("provider failure")
	}), true)
	_, err := coord.Prepare(context.Background(), NativeContextPrepareInput{
		Call:           testCoordinatorCall(),
		Payload:        testCoordinatorPayload(),
		Account:        Config{BaseURL: "http://selected", AccountID: "account"},
		Model:          "model",
		MarkerEligible: true,
		ClientFamily:   "codex",
		ConversationID: "conversation",
	})
	if err != nil {
		t.Fatal(err)
	}
	telemetry := coord.telemetry.snapshot()
	if telemetry.CompactionProtocolFails != 1 || telemetry.CheckpointCommits != 0 {
		t.Fatalf("failure outcome = %s", telemetry)
	}
}

func TestNativeContextCoordinator_CancellationAbortsReservationAndKeepsOldCheckpoint(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	coord := testCoordinator(t, coordinatorCompactorFunc(func(ctx context.Context, _ CompactionRequest) (CompactionResult, error) {
		<-ctx.Done()
		return CompactionResult{}, ctx.Err()
	}), true)
	cancel()
	_, err := coord.Prepare(ctx, NativeContextPrepareInput{
		Call:           testCoordinatorCall(),
		Payload:        testCoordinatorPayload(),
		Account:        Config{BaseURL: "http://selected", AccountID: "account"},
		Model:          "model",
		MarkerEligible: true,
		ClientFamily:   "codex",
		ConversationID: "conversation",
	})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if coord.store == nil {
		t.Fatal("expected store")
	}
	key := coord.keyFor(NativeContextPrepareInput{Call: testCoordinatorCall(), Payload: testCoordinatorPayload(), Account: Config{AccountID: "account"}, Model: "model", ClientFamily: "codex"}, "")
	if _, ok := coord.store.Reserve(key); !ok {
		t.Fatal("cancellation leaked reservation or cooldown")
	}
}

func TestNativeContextCoordinator_NilInputContextUsesPrepareContext(t *testing.T) {
	t.Parallel()
	called := false
	coord := testCoordinator(t, coordinatorCompactorFunc(func(ctx context.Context, _ CompactionRequest) (CompactionResult, error) {
		called = true
		if ctx == nil {
			t.Fatal("compactor received nil context")
		}
		return CompactionResult{}, errors.New("stop")
	}), true)
	_, err := coord.Prepare(context.Background(), NativeContextPrepareInput{
		Call: testCoordinatorCall(), Payload: testCoordinatorPayload(), Account: Config{AccountID: "account"},
		Model: "model", MarkerEligible: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("expected compactor call")
	}
}

func TestCompactionUsageEvidence_DoesNotReusePreviousCheckpointAsNewBilling(t *testing.T) {
	t.Parallel()
	previous := &NativeUsageEvidence{
		InputTokens:  900,
		OutputTokens: 90,
		TotalTokens:  990,
		UsagePresence: aipapi.UsagePresence{
			InputTokens: true, OutputTokens: true, TotalTokens: true,
		},
		Source: aipapi.UsageSourceProviderReported, Authority: aipapi.UsageAuthorityAuthoritative,
	}
	got := compactionUsageEvidence(nil, CompactionPlan{
		EffectiveTokens:    120,
		ExistingCheckpoint: &CheckpointView{CompactionUsage: previous},
	}, ReplacementResult{ResultEstimatedTokens: 8})
	if got.Source != aipapi.UsageSourceLocalEstimator || got.Authority != aipapi.UsageAuthorityEstimated {
		t.Fatalf("new compaction inherited prior billing authority: %#v", got)
	}
	if got.InputTokens != 120 || got.OutputTokens != 8 || got.TotalTokens != 128 {
		t.Fatalf("estimated usage = %#v", got)
	}
}

func TestNativeContextCoordinator_MissingProviderUsageCreatesEstimatedEvidence(t *testing.T) {
	t.Parallel()
	coord := testCoordinator(t, coordinatorCompactorFunc(func(context.Context, CompactionRequest) (CompactionResult, error) {
		return CompactionResult{Item: opaqueResponseItem{raw: []byte(`{"type":"compaction","id":"cmp","encrypted_content":"opaque"}`)}}, nil
	}), true)
	prepared, err := coord.Prepare(context.Background(), NativeContextPrepareInput{
		Call:           testCoordinatorCall(),
		Payload:        testCoordinatorPayload(),
		Account:        Config{AccountID: "account"},
		Model:          "model",
		MarkerEligible: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.CompactionUsage == nil {
		t.Fatal("missing provider usage must still produce compaction evidence")
	}
	usage := prepared.CompactionUsage
	if usage.Source != aipapi.UsageSourceLocalEstimator || usage.Authority != aipapi.UsageAuthorityEstimated {
		t.Fatalf("usage authority = %#v, want local estimated evidence", usage)
	}
	if usage.TotalTokens <= 0 || !usage.UsagePresence.Any() {
		t.Fatalf("estimated usage = %#v, want positive present counters", usage)
	}
	key := coord.keyFor(NativeContextPrepareInput{Call: testCoordinatorCall(), Payload: testCoordinatorPayload(), Account: Config{AccountID: "account"}, Model: "model"}, "")
	checkpoint, ok := coord.store.Get(key)
	if !ok || checkpoint.CompactionUsage == nil {
		t.Fatalf("checkpoint did not retain estimated usage metadata: ok=%t checkpoint=%#v", ok, checkpoint)
	}
	if checkpoint.CompactionUsage.Source != aipapi.UsageSourceLocalEstimator || checkpoint.CompactionUsage.Authority != aipapi.UsageAuthorityEstimated {
		t.Fatalf("checkpoint usage authority = %#v", checkpoint.CompactionUsage)
	}
	accounting := accountingEvidence(prepared.NativeUsage())
	if accounting.Source != backendplugin.AccountingSourceLocalEstimator || accounting.Authority != backendplugin.AccountingAuthorityEstimated {
		t.Fatalf("estimated sideband evidence was not explicitly classified: %#v", accounting)
	}
}

func TestNativeContextCoordinator_ProviderUsageWinsOverEstimate(t *testing.T) {
	t.Parallel()
	input, output, total := int64(73), int64(11), int64(84)
	coord := testCoordinator(t, coordinatorCompactorFunc(func(context.Context, CompactionRequest) (CompactionResult, error) {
		return CompactionResult{
			Item:  opaqueResponseItem{raw: []byte(`{"type":"compaction","id":"cmp","encrypted_content":"opaque"}`)},
			Usage: &completedUsage{InputTokens: &input, OutputTokens: &output, TotalTokens: &total},
		}, nil
	}), true)
	prepared, err := coord.Prepare(context.Background(), NativeContextPrepareInput{
		Call:           testCoordinatorCall(),
		Payload:        testCoordinatorPayload(),
		Account:        Config{AccountID: "account"},
		Model:          "model",
		MarkerEligible: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	usage := prepared.CompactionUsage
	if usage == nil || usage.InputTokens != input || usage.OutputTokens != output || usage.TotalTokens != total {
		t.Fatalf("provider usage was not selected: %#v", usage)
	}
	if usage.Source != aipapi.UsageSourceProviderReported || usage.Authority != aipapi.UsageAuthorityAuthoritative {
		t.Fatalf("provider usage authority = %#v", usage)
	}
}

func TestNativeContextCoordinator_DedicatedOutputWithoutUsageCommitsCheckpoint(t *testing.T) {
	t.Parallel()
	coord := testCoordinator(t, coordinatorCompactorFunc(func(context.Context, CompactionRequest) (CompactionResult, error) {
		return CompactionResult{Output: []inputItem{
			textMessageItem{Type: "message", Role: "user", Content: "retained"},
			opaqueResponseItem{raw: []byte(`{"type":"compaction_summary","id":"summary","encrypted_content":"opaque"}`)},
		}}, nil
	}), true)
	prepared, err := coord.Prepare(context.Background(), NativeContextPrepareInput{
		Call:           testCoordinatorCall(),
		Payload:        testCoordinatorPayload(),
		Account:        Config{AccountID: "account"},
		Model:          "model",
		MarkerEligible: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !prepared.Compacted || prepared.CompactionUsage == nil || prepared.CompactionUsage.Source != aipapi.UsageSourceLocalEstimator || prepared.CompactionUsage.Authority != aipapi.UsageAuthorityEstimated {
		t.Fatalf("prepared = %#v", prepared)
	}
}

func TestNativeContextCoordinator_CheckpointOverCheckpointExcludesCurrentTail(t *testing.T) {
	t.Parallel()
	const (
		olderTail   = "LIVE_TAIL_SECOND"
		currentTail = "LIVE_TAIL_THIRD"
	)
	var requests []CompactionRequest
	coord := testCoordinator(t, coordinatorCompactorFunc(func(_ context.Context, req CompactionRequest) (CompactionResult, error) {
		requests = append(requests, req)
		return CompactionResult{Output: []inputItem{
			textMessageItem{Type: "message", Role: "user", Content: "retained history"},
			opaqueResponseItem{raw: []byte(`{"type":"compaction_summary","id":"summary","encrypted_content":"opaque"}`)},
		}}, nil
	}), true)
	base := NativeContextPrepareInput{
		Payload: Payload{Model: "model"},
		Account: Config{AccountID: "account"},
		Model:   "model", MarkerEligible: true, ClientFamily: "codex", ConversationID: "conversation",
	}
	base.Call = aipapi.Call{Session: aipapi.SessionRef{AuthoritativeSessionID: "session"}, Messages: []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("old history")}},
		nativeText(aipapi.RoleUser, olderTail),
	}}
	if prepared, err := coord.Prepare(context.Background(), base); err != nil {
		t.Fatal(err)
	} else if !prepared.Compacted {
		t.Fatalf("first preparation did not compact: %+v", prepared)
	}

	base.Call.Messages = append(
		base.Call.Messages,
		nativeText(aipapi.RoleAssistant, "prior response"),
		nativeText(aipapi.RoleUser, currentTail),
		nativeText(aipapi.RoleAssistant, "current response"),
	)
	if prepared, err := coord.Prepare(context.Background(), base); err != nil {
		t.Fatal(err)
	} else if !prepared.Compacted {
		t.Fatalf("second preparation did not compact: %+v", prepared)
	}
	if len(requests) != 2 {
		t.Fatalf("compaction request count = %d, want 2", len(requests))
	}
	secondInput := requests[1].Payload.Input
	var joined strings.Builder
	for _, item := range secondInput {
		body, err := nativeItemJSON(item)
		if err != nil {
			t.Fatal(err)
		}
		joined.Write(body)
	}
	if strings.Contains(joined.String(), currentTail) {
		t.Fatalf("second checkpoint compaction input included current user tail: %s", joined.String())
	}
	if !strings.Contains(joined.String(), olderTail) {
		t.Fatalf("second checkpoint compaction input lost permitted prior tail: %s", joined.String())
	}
}

func testCoordinator(t *testing.T, compactor nativeCompactionClient, _ bool) *NativeContextCoordinator {
	t.Helper()
	return newNativeContextCoordinatorWithCompactor(Config{BaseURL: "http://selected", AccountID: "account", NativeContext: &NativeContextConfig{
		Enabled: true, ReasoningContinuity: ContinuityBestEffort,
		Compaction: NativeCompactionConfig{Enabled: true, TriggerTokens: 1, RetainedMessageTokens: 0, MinSavingsTokens: 0},
	}}, "instance-1", compactor)
}

func testCoordinatorCall() aipapi.Call {
	return aipapi.Call{Session: aipapi.SessionRef{AuthoritativeSessionID: "proxy-session"}, Messages: []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("history")}},
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("latest")}},
	}}
}

func testCoordinatorCallWithoutAuthority() aipapi.Call {
	call := testCoordinatorCall()
	call.Session.AuthoritativeSessionID = ""
	return call
}

func testCoordinatorPayload() Payload {
	return Payload{Model: "model", Input: []inputItem{
		textMessageItem{Type: "message", Role: "user", Content: "history"},
		textMessageItem{Type: "message", Role: "user", Content: "latest"},
	}}
}
