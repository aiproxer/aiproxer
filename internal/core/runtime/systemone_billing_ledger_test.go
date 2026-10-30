package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/systemonecompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
	"gopkg.in/yaml.v3"
)

// sysoneLedgerFailure scripts one key's upstream failure for the
// billing-ledger integration test.
type sysoneLedgerFailure struct {
	status     int
	retryAfter string
	body       string
}

// sysoneLedgerEmulator fails scripted keys and serves a billable decision
// payload to every other key.
type sysoneLedgerEmulator struct {
	fail map[string]sysoneLedgerFailure
}

func (e *sysoneLedgerEmulator) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if f, ok := e.fail[key]; ok {
			if f.retryAfter != "" {
				w.Header().Set("Retry-After", f.retryAfter)
			}
			body := f.body
			if body == "" {
				body = `{"detail":[{"loc":["body","questions"],"msg":"limited","type":"value_error"}]}`
			}
			w.WriteHeader(f.status)
			_, _ = io.WriteString(w, body)
			return
		}
		_, _ = io.WriteString(w, `{"model":"jev","answers":{"yes":{"type":"noul","noul":0.6}},"usage":{"input_tokens":11,"output_tokens":5}}`)
	})
}

func sysoneLedgerNode(t *testing.T, base, root string) yaml.Node {
	t.Helper()
	var n yaml.Node
	raw := fmt.Sprintf("backend_prefix: decision\nbase_url: %s\napi_key_env_var_root: %s\nmodels:\n  source: inline\n  items:\n    - canonical_id: jev\n      native_id: jev\n", base, root)
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func sysoneLedgerCall() aipapi.Call {
	r := aipapi.DecisionRequest{Evidence: json.RawMessage(`{"state":[1]}`), Questions: []aipapi.DecisionQuestion{
		{ID: "yes", Kind: aipapi.DecisionKindNoul, TrueCriteria: json.RawMessage(`"yes"`)},
	}}
	return aipapi.Call{Decision: &r, Invocation: aipapi.Invocation{Operation: aipapi.OperationDecisionEvaluate, DeliveryMode: aipapi.DeliveryModeNonStreaming}}
}

func sysoneLedgerCandidate() routing.AttemptCandidate {
	return routing.AttemptCandidate{Primary: routing.Primary{Model: "jev"}}
}

// admitSysoneStreamThroughAttemptPipeline feeds a real backend stream through
// the production admission seams the response pipeline uses: stream usage
// events with source identities enter with the stream role, and the host-only
// sideband drains with the sideband role. It returns the drained
// source-tagged evidence ready for terminal record construction.
func admitSysoneStreamThroughAttemptPipeline(t *testing.T, attempt *attemptSession, stream aipapi.ManagedEventStream) []capturedBillingEvidence {
	t.Helper()
	ctx := context.Background()
	for {
		ev, err := stream.Recv(ctx)
		if err != nil {
			// io.EOF ends a success stream; a terminal failure ends a
			// failure-evidence stream. Either way the host-only sideband
			// below carries the billable evidence.
			break
		}
		if ev.Kind == aipapi.EventUsageDelta && strings.TrimSpace(ev.Accounting.DedupeKey) != "" {
			attempt.rememberUsageEvidenceOnceAs(ev, billingEvidenceRoleStream)
		}
	}
	attempt.drainStreamUsageEvidence(stream)
	events, _ := attempt.billingEvidenceDrain()
	return events
}

func sysoneLedgerRecord(t *testing.T, events []capturedBillingEvidence) usageevidence.CallLegUsageRecord {
	t.Helper()
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	return billingLegRecord(billingLegDraft{
		callID: callID, aLegID: "a-sysone", storeID: "sysone-store", bLegID: "b-sysone", seq: 1,
		primary:   routing.Primary{Backend: "systemone", Model: "jev"},
		startedAt: time.Unix(100, 0).UTC(), finishedAt: time.Unix(101, 0).UTC(),
		command:        sdkterminal.CommandNormalFinish,
		evidenceEvents: events,
	})
}

// TestSystemOneRotationSuccessBillingLedgerReceivesFailedAttempt executes the
// real SystemOne backend through the production attempt-evidence pipeline:
// the first key fails billable, the second succeeds, and the failed attempt
// must survive admission, deduplication, and terminal mapping into the final
// economic record. Before the rotation-evidence repair the success stream
// carried no sideband, so the final record held zero observations.
func TestSystemOneRotationSuccessBillingLedgerReceivesFailedAttempt(t *testing.T) {
	// No t.Parallel: the test binds process-environment credential slots.
	root := "SYS1_LEDGER_SUCCESS_API_KEY"
	for _, name := range []string{root, root + "_2"} {
		t.Setenv(name, "")
	}
	t.Setenv(root, "sysone-ledger-k1")
	t.Setenv(root+"_2", "sysone-ledger-k2")

	emu := &sysoneLedgerEmulator{fail: map[string]sysoneLedgerFailure{
		"sysone-ledger-k1": {status: 401, body: `{"usage":{"input_tokens":3,"output_tokens":0}}`},
	}}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	be, err := systemonecompat.BuildCompatible("decision", sysoneLedgerNode(t, srv.URL, root), srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := be.Open(context.Background(), sysoneLedgerCall(), sysoneLedgerCandidate())
	if err != nil {
		t.Fatalf("rotation success must not return an error: %v", err)
	}
	defer func() { _ = stream.Close() }()

	attempt := &attemptSession{}
	events := admitSysoneStreamThroughAttemptPipeline(t, attempt, stream)
	record := sysoneLedgerRecord(t, events)
	if len(record.Observations) != 1 {
		t.Fatalf("final economic observations = %d, want exactly the failed billable attempt", len(record.Observations))
	}
	observation := record.Observations[0]
	if !strings.HasPrefix(observation.SourceEventKey, "systemone:failed-attempt-usage:") {
		t.Fatalf("observation source = %q, want the failed-attempt source identity", observation.SourceEventKey)
	}
	if strings.Contains(observation.SourceEventKey, "sysone-ledger-k1") {
		t.Fatalf("observation source leaks secret material: %q", observation.SourceEventKey)
	}
	found := false
	for _, measure := range observation.Measures {
		if measure.Key.Component == sdkmetering.ComponentInputToken && measure.Value != nil && measure.Value.CanonicalString() == "3/0" {
			found = true
		}
	}
	if !found {
		t.Fatalf("failed-attempt input quantity missing in %+v", observation.Measures)
	}
}

// TestSystemOneRotationAllFailBillingLedgerReceivesBothAttempts executes the
// real SystemOne backend through the same pipeline with every key failing
// billable: both attempts must survive into the final economic record as
// distinct observations. Before distinct source identities, the shared
// constant key collapsed the second attempt into a conflict, leaving one
// observation.
func TestSystemOneRotationAllFailBillingLedgerReceivesBothAttempts(t *testing.T) {
	// No t.Parallel: the test binds process-environment credential slots.
	root := "SYS1_LEDGER_ALLFAIL_API_KEY"
	for _, name := range []string{root, root + "_2"} {
		t.Setenv(name, "")
	}
	t.Setenv(root, "sysone-ledger-k1")
	t.Setenv(root+"_2", "sysone-ledger-k2")

	emu := &sysoneLedgerEmulator{fail: map[string]sysoneLedgerFailure{
		"sysone-ledger-k1": {status: 401, body: `{"usage":{"input_tokens":3,"output_tokens":0}}`},
		"sysone-ledger-k2": {status: 401, body: `{"usage":{"input_tokens":5,"output_tokens":0}}`},
	}}
	srv := httptest.NewServer(emu.handler())
	t.Cleanup(srv.Close)

	be, err := systemonecompat.BuildCompatible("decision", sysoneLedgerNode(t, srv.URL, root), srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := be.Open(context.Background(), sysoneLedgerCall(), sysoneLedgerCandidate())
	if err != nil {
		t.Fatalf("terminal failure must arrive as an evidence stream, got error: %v", err)
	}
	defer func() { _ = stream.Close() }()

	attempt := &attemptSession{}
	events := admitSysoneStreamThroughAttemptPipeline(t, attempt, stream)
	record := sysoneLedgerRecord(t, events)
	if len(record.Observations) != 2 {
		t.Fatalf("final economic observations = %d, want one per billable attempt", len(record.Observations))
	}
	seen := map[string]string{}
	for _, observation := range record.Observations {
		if !strings.HasPrefix(observation.SourceEventKey, "systemone:failed-attempt-usage:") {
			t.Fatalf("observation source = %q, want failed-attempt identities", observation.SourceEventKey)
		}
		for _, measure := range observation.Measures {
			if measure.Key.Component == sdkmetering.ComponentInputToken && measure.Value != nil {
				seen[observation.SourceEventKey] = measure.Value.CanonicalString()
			}
		}
	}
	if len(seen) != 2 {
		t.Fatalf("distinct billable quantities = %v, want both attempts", seen)
	}
}
