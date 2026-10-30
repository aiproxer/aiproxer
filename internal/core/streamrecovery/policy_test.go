package streamrecovery_test

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/streamrecovery"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestPolicyEOFAfterResponseFinishedPassesThrough(t *testing.T) {
	t.Parallel()
	p := streamrecovery.NewPolicy(streamrecovery.Config{Enabled: true}, time.Unix(1, 0))
	p.ObserveBackendEvent(aipapi.Event{Kind: aipapi.EventResponseFinished}, time.Unix(2, 0))
	dec := p.DecideEOF(io.EOF, time.Unix(3, 0))
	if dec.Kind != streamrecovery.DecisionPassThrough {
		t.Fatalf("decision: got %s", dec.Kind)
	}
}

func TestPolicyEOFBeforeClientOutputRecoversPreOutput(t *testing.T) {
	t.Parallel()
	p := streamrecovery.NewPolicy(streamrecovery.Config{Enabled: true}, time.Unix(1, 0))
	p.ObserveBackendEvent(aipapi.Event{Kind: aipapi.EventResponseStarted}, time.Unix(2, 0))
	dec := p.DecideEOF(io.EOF, time.Unix(3, 0))
	if dec.Kind != streamrecovery.DecisionRecoverPreOutput {
		t.Fatalf("decision: got %s", dec.Kind)
	}
}

func TestPolicyEOFAfterClientOutputFinishesPostOutput(t *testing.T) {
	t.Parallel()
	p := streamrecovery.NewPolicy(streamrecovery.Config{Enabled: true, EmitWarning: true}, time.Unix(1, 0))
	p.ObserveBackendEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}, time.Unix(2, 0))
	p.ObserveClientEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}, time.Unix(2, 0))
	dec := p.DecideEOF(io.EOF, time.Unix(3, 0))
	if dec.Kind != streamrecovery.DecisionFinishPostOutput {
		t.Fatalf("decision: got %s", dec.Kind)
	}
	if dec.Warning.Kind != aipapi.EventWarning || dec.Finish.Kind != aipapi.EventResponseFinished {
		t.Fatalf("expected warning and finish, got %#v %#v", dec.Warning, dec.Finish)
	}
}

func TestPolicyEOFAfterClientOutputSuppressesWarningWhenDisabled(t *testing.T) {
	t.Parallel()
	p := streamrecovery.NewPolicy(streamrecovery.Config{Enabled: true, EmitWarning: false}, time.Unix(1, 0))
	p.ObserveBackendEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}, time.Unix(2, 0))
	p.ObserveClientEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}, time.Unix(2, 0))
	dec := p.DecideEOF(io.EOF, time.Unix(3, 0))
	if dec.Kind != streamrecovery.DecisionFinishPostOutput {
		t.Fatalf("decision: got %s", dec.Kind)
	}
	if dec.Warning.Kind != "" {
		t.Fatalf("expected no warning when EmitWarning=false, got %#v", dec.Warning)
	}
	if dec.Finish.Kind != aipapi.EventResponseFinished {
		t.Fatalf("expected finish, got %#v", dec.Finish)
	}
}

func TestPolicyDisabledDoesNotRecover(t *testing.T) {
	t.Parallel()
	p := streamrecovery.NewPolicy(streamrecovery.Config{Enabled: false}, time.Unix(1, 0))
	p.ObserveBackendEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}, time.Unix(2, 0))
	p.ObserveClientEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}, time.Unix(2, 0))
	dec := p.DecideEOF(io.EOF, time.Unix(3, 0))
	if dec.Kind != streamrecovery.DecisionPassThrough {
		t.Fatalf("decision: got %s", dec.Kind)
	}
}

func TestPolicyCancellationSurfacesFailure(t *testing.T) {
	t.Parallel()
	p := streamrecovery.NewPolicy(streamrecovery.Config{Enabled: true}, time.Unix(1, 0))
	dec := p.DecideEOF(context.Canceled, time.Unix(3, 0))
	if dec.Kind != streamrecovery.DecisionSurfaceFailure || !errors.Is(dec.Err, context.Canceled) {
		t.Fatalf("decision: got %s err=%v", dec.Kind, dec.Err)
	}
}

func TestPolicyIdleAfterClientOutputFinishesPostOutput(t *testing.T) {
	t.Parallel()
	p := streamrecovery.NewPolicy(streamrecovery.Config{
		Enabled:     true,
		IdleTimeout: 45 * time.Second,
		GracePeriod: 3 * time.Second,
	}, time.Unix(1, 0))
	p.ObserveBackendEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}, time.Unix(10, 0))
	p.ObserveClientEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "x"}, time.Unix(10, 0))
	dec := p.DecideIdle(time.Unix(58, 0))
	if dec.Kind != streamrecovery.DecisionFinishPostOutput {
		t.Fatalf("decision: got %s", dec.Kind)
	}
}

func TestPolicyIdleBeforeClientOutputRecoversPreOutput(t *testing.T) {
	t.Parallel()
	p := streamrecovery.NewPolicy(streamrecovery.Config{
		Enabled:     true,
		IdleTimeout: 45 * time.Second,
		GracePeriod: 3 * time.Second,
	}, time.Unix(1, 0))
	p.ObserveBackendEvent(aipapi.Event{Kind: aipapi.EventResponseStarted}, time.Unix(10, 0))
	dec := p.DecideIdle(time.Unix(58, 0))
	if dec.Kind != streamrecovery.DecisionRecoverPreOutput {
		t.Fatalf("decision: got %s", dec.Kind)
	}
}
