package runtimebundle_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
)

func TestCompileGeneration_FailureMatrix_RollbackNotClose_NoOrphan(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		fault   runtimebundle.CandidateFaultInject
		compose runtimebundle.HandlerComposer
		secret  string
	}{
		{name: "handler_boundary", fault: runtimebundle.CandidateFaultInject{After: "handler"}},
		{name: "composer_clone", fault: runtimebundle.CandidateFaultInject{After: "composer-clone"}},
		{
			name: "composer_error",
			compose: func(context.Context, *config.Config, *slog.Logger, httpcontract.StandardHTTPInput) (http.Handler, error) {
				return nil, errors.New("composer boom")
			},
		},
		{
			name: "composer_panic",
			compose: func(context.Context, *config.Config, *slog.Logger, httpcontract.StandardHTTPInput) (http.Handler, error) {
				panic("Bearer sk-live-abcdefghijklmnopqrstuv")
			},
			secret: "sk-live-abcdefghijklmnopqrstuv",
		},
		{
			name: "nil_handler",
			compose: func(context.Context, *config.Config, *slog.Logger, httpcontract.StandardHTTPInput) (http.Handler, error) {
				return nil, nil
			},
		},
		{name: "ledger_transfer_refusal", fault: runtimebundle.CandidateFaultInject{After: "ledger-transfer"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ps := newProcessForGeneration(t)
			candCfg := stubCandidateConfig(t, "gen-fail", "fail-text", "gen-fail:stub-default", []config.PluginConfig{
				{ID: "openai-responses", Enabled: true},
			})
			compose := tc.compose
			if compose == nil {
				compose = stdhttp.ComposeStandardHTTP
			}
			_, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
				Process: ps, Candidate: candCfg, Compose: compose, FaultInject: tc.fault,
			})
			if err == nil {
				t.Fatal("expected compile failure")
			}
			if ps.Closed() {
				t.Fatal("process-owned services must survive candidate rollback")
			}
			if tc.secret != "" && strings.Contains(err.Error(), tc.secret) {
				t.Fatalf("panic value must be secret-safe, got %v", err)
			}
			ok, err2 := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
				Process: ps, Candidate: candCfg, Compose: stdhttp.ComposeStandardHTTP,
			})
			if err2 != nil {
				t.Fatalf("recovery compile after %s: %v", tc.name, err2)
			}
			t.Cleanup(func() { _ = ok.Close() })
		})
	}
}

func TestCompileGeneration_Failure_JoinsPrimaryWithRollback(t *testing.T) {
	t.Parallel()
	ps := newProcessForGeneration(t)
	candCfg := stubCandidateConfig(t, "join-fail", "join-text", "join-fail:stub-default", []config.PluginConfig{
		{ID: "openai-responses", Enabled: true},
	})
	_, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process:   ps,
		Candidate: candCfg,
		Compose: func(context.Context, *config.Config, *slog.Logger, httpcontract.StandardHTTPInput) (http.Handler, error) {
			return nil, fmt.Errorf("primary-compose-fail")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "primary-compose-fail") {
		t.Fatalf("err=%v", err)
	}
	if ps.Closed() {
		t.Fatal("process must survive")
	}
}
