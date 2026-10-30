package checkpoint_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestCaptureFrontendIngress_SetsTraceFromCallID(t *testing.T) {
	t.Parallel()
	snap, err := checkpoint.CaptureFrontendIngress(checkpoint.FrontendIngressInput{
		Call: aipapi.Call{
			ID:      "trace-fe-1",
			Session: aipapi.SessionRef{ALegID: "a-fe"},
		},
		FrontendID:   "openai-responses",
		CheckpointID: "fe-ingress:trace-fe-1",
		StreamID:     "fe-ingress:trace-fe-1",
		Now:          time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	c := snap.Public.Correlation
	if c.TraceID != "trace-fe-1" || c.RequestID != "trace-fe-1" || c.ALegID != "a-fe" {
		t.Fatalf("correlation=%+v", c)
	}
	if snap.Public.FrontendID != "openai-responses" {
		t.Fatalf("FrontendID=%q", snap.Public.FrontendID)
	}
	if snap.Public.StreamID != "fe-ingress:trace-fe-1" {
		t.Fatalf("StreamID=%q want fe-ingress:trace-fe-1", snap.Public.StreamID)
	}
	if snap.Public.StreamID == c.TraceID {
		t.Fatalf("StreamID must stay distinct from TraceID (%q)", c.TraceID)
	}
}

func TestCaptureBackendIngress_UsesTraceIDNotFEStream(t *testing.T) {
	t.Parallel()
	snap, err := checkpoint.CaptureBackendIngress(checkpoint.BackendIngressInput{
		Call: aipapi.Call{
			ID: "req-be", Session: aipapi.SessionRef{ALegID: "a-1"},
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}},
		},
		AttemptID: "att-1", BLegID: "b-1", ALegID: "a-1",
		BackendID: "backend-1", Model: "model-1",
		CheckpointID: "be-in", StreamID: "be-ingress:att-1",
		TraceID: "trace-runtime",
		Now:     time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	c := snap.Public.Correlation
	if c.TraceID != "trace-runtime" {
		t.Fatalf("TraceID=%q want trace-runtime", c.TraceID)
	}
	if c.TraceID == "fe-ingress:req-be" || c.TraceID == snap.Public.StreamID {
		t.Fatal("must not reuse FE stream id as TraceID")
	}
	if c.RequestID != "req-be" || c.BLegID != "b-1" || c.AttemptID != "att-1" {
		t.Fatalf("correlation=%+v", c)
	}
	if snap.Public.BackendID != "backend-1" || snap.Public.Model != "model-1" {
		t.Fatalf("backend/model=%s/%s", snap.Public.BackendID, snap.Public.Model)
	}
}

func TestCaptureBackendIngress_DefaultsTraceToCallID(t *testing.T) {
	t.Parallel()
	snap, err := checkpoint.CaptureBackendIngress(checkpoint.BackendIngressInput{
		Call: aipapi.Call{
			ID: "req-only", Session: aipapi.SessionRef{ALegID: "a-1"},
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}},
		},
		AttemptID: "att-1", BLegID: "b-1", ALegID: "a-1",
		CheckpointID: "be", StreamID: "be-ingress:att-1",
		Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Public.Correlation.TraceID != "req-only" {
		t.Fatalf("TraceID=%q want Call.ID", snap.Public.Correlation.TraceID)
	}
}

func TestImmutableFEIngress_SurvivesCallMutation(t *testing.T) {
	t.Parallel()
	work := aipapi.Call{
		ID: "req-immut",
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("original")},
		}},
	}
	holder := &checkpoint.RequestHolder{}
	_, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call: work, CheckpointID: "fe", StreamID: "fe-ingress:req-immut", Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	work.Messages[0].Parts[0].Text = "mutated-by-hook"
	if holder.FrontendIngress.Call.Messages[0].Parts[0].Text != "original" {
		t.Fatal("FE ingress call must remain immutable clone")
	}
}

func TestBindScope_DoesNotMutateFrozenCall(t *testing.T) {
	t.Parallel()
	snap, err := checkpoint.CaptureFrontendIngress(checkpoint.FrontendIngressInput{
		Call: aipapi.Call{ID: "req-scope", Messages: []aipapi.Message{{
			Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")},
		}}},
		CheckpointID: "fe", StreamID: "fe-s", Now: time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	before := snap.Call.Messages[0].Parts[0].Text
	snap.BindScope(scope.PrincipalScopeView{})
	if snap.Call.Messages[0].Parts[0].Text != before {
		t.Fatal("BindScope must not mutate Call")
	}
}

func TestCheckpointFact_JSONRoundTripPresenceAndCorrelation(t *testing.T) {
	t.Parallel()
	fe, err := checkpoint.CaptureFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         aipapi.Call{ID: "req-json", Session: aipapi.SessionRef{ALegID: "a-json"}},
		FrontendID:   "fe-plugin",
		CheckpointID: "fe-cp",
		StreamID:     "fe-ingress:req-json",
		Now:          time.Unix(3, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(fe.Public)
	if err != nil {
		t.Fatal(err)
	}
	var decoded metering.Checkpoint
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Correlation.TraceID != "req-json" || decoded.FrontendID != "fe-plugin" {
		t.Fatalf("decoded=%+v", decoded)
	}

	fact, err := checkpoint.FactFromEgress(checkpoint.EgressFactInput{
		Checkpoint: checkpoint.FrontendEgressCheckpoint(fe),
		FactID:     "fe-egress:req-json:1",
		Sequence:   1,
		Quantities: []metering.Quantity{
			{Component: metering.ComponentInputToken, Unit: metering.UnitToken, Value: 0, Present: true},
		},
		Money: &metering.MoneyObservation{NanoUnits: 0, Currency: "USD", Present: true, Source: metering.SourceProviderReported},
		Now:   time.Unix(3, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	fraw, err := json.Marshal(fact)
	if err != nil {
		t.Fatal(err)
	}
	var fdec metering.Fact
	if err := json.Unmarshal(fraw, &fdec); err != nil {
		t.Fatal(err)
	}
	in, ok := checkpoint.QuantityComponentValue(fdec.Quantities, metering.ComponentInputToken)
	if !ok || in != 0 {
		t.Fatalf("authoritative zero input lost: ok=%v v=%d", ok, in)
	}
	if fdec.Money == nil || !fdec.Money.Present || fdec.Money.NanoUnits != 0 {
		t.Fatalf("authoritative zero money lost: %+v", fdec.Money)
	}
	if fdec.Correlation.TraceID != "req-json" || fdec.Correlation.ALegID != "a-json" {
		t.Fatalf("fact correlation=%+v", fdec.Correlation)
	}
}
