// Bounded protocol observability and privacy fixtures for task 10.3 of
// agent-loop-explicit-completion-protocol (spec:
// .kiro/specs/agent-loop-explicit-completion-protocol, design Observability and
// Accounting/Context/Traffic; requirements 11.1-11.5).
//
// Every cell here drives the same real seam slices 10.1 and 10.2 deployed: a real
// frontend handler on a real httptest origin, the real runtime executor, a real
// backend adapter, a real reference-provider origin emulator, and the real Agent
// Loop Guard generation composed through the production feature registry, the
// enabled-surface merge, and the production request-snapshot builder. No provider
// fake is ever assigned, and the origin scripting, trace, and wire-hygiene
// helpers of slices 10.1/10.2 are reused unchanged.
//
// The three telemetry dimensions requirement 11.1 asks for are read from REAL
// sinks, never from a string filter over a rendered log buffer:
//
//   - activation: the real `control_tool_projection` structured record emitted by
//     the generic runtime's projection stage through the production diag logging
//     seam;
//   - control: the real `control_tool_call` structured record emitted by the
//     generic runtime's control-call interception seam through that same seam;
//   - terminal: the real `terminal_decision_evaluation` structured record
//     emitted by the generic terminal owner.
//
// Every assertion below reads the structured ATTRIBUTES those production call
// sites attached, captured through a real *slog.Handler, and every bounded
// vocabulary is enumerated from the production constants that own it rather than
// matched by prefix.
//
// Usage and cost attribution (requirements 11.3 and 11.4) is read from the real
// usage.Observer accounting seam the runtime snapshot already owns, and from the
// real upstream request log the origin emulator serves.
package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard"
	"github.com/aiproxer/aiproxer/internal/plugins/features/agentloopguard/protocolpolicy"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controltool"
	aipfeature "github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

// --- the real structured-log sink ---------------------------------------------

// algTelemetryRecord is one captured production log record: its level, its
// production message, and every structured attribute the production call site
// attached. Attributes are captured as [slog.Value] rather than as rendered text,
// so an assertion reads the actual telemetry attribute the seam produced.
type algTelemetryRecord struct {
	Level   slog.Level
	Message string
	keys    []string
	values  map[string]slog.Value
}

// Attr returns one captured attribute value and whether the production call site
// attached that attribute at all.
func (rec algTelemetryRecord) Attr(key string) (slog.Value, bool) {
	v, ok := rec.values[key]
	return v, ok
}

// Str returns one captured string attribute, or "" when it is absent.
func (rec algTelemetryRecord) Str(key string) string {
	v, ok := rec.values[key]
	if !ok {
		return ""
	}
	return v.String()
}

// algTelemetrySink is a real [slog.Handler] installed as the deployment's
// Executor.Log. It is the sink requirement 11.2 is asserted against: it captures
// the attributes the production logging seams actually attach, so "no result text
// in a reason code" is proved from real telemetry state rather than from a
// substring search over formatted output.
type algTelemetrySink struct {
	mu      sync.Mutex
	records []algTelemetryRecord
}

func (s *algTelemetrySink) Enabled(context.Context, slog.Level) bool { return true }

func (s *algTelemetrySink) Handle(_ context.Context, r slog.Record) error {
	rec := algTelemetryRecord{
		Level:   r.Level,
		Message: r.Message,
		values:  make(map[string]slog.Value),
	}
	r.Attrs(func(a slog.Attr) bool {
		rec.keys = append(rec.keys, a.Key)
		rec.values[a.Key] = a.Value
		return true
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, rec)
	return nil
}

func (s *algTelemetrySink) WithAttrs([]slog.Attr) slog.Handler { return s }
func (s *algTelemetrySink) WithGroup(string) slog.Handler      { return s }

func (s *algTelemetrySink) snapshot() []algTelemetryRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]algTelemetryRecord(nil), s.records...)
}

// withMessage returns every captured record of one production message.
func (s *algTelemetrySink) withMessage(message string) []algTelemetryRecord {
	var out []algTelemetryRecord
	for _, rec := range s.snapshot() {
		if rec.Message == message {
			out = append(out, rec)
		}
	}
	return out
}

// dump renders every captured record and attribute so a failure names the exact
// telemetry state that was observed.
func (s *algTelemetrySink) dump() string {
	var b strings.Builder
	for _, rec := range s.snapshot() {
		b.WriteString(rec.Level.String() + " " + rec.Message)
		for _, k := range rec.keys {
			b.WriteString(" " + k + "=" + strconv.Quote(rec.values[k].String()))
		}
		b.WriteByte('\n')
	}
	if b.Len() == 0 {
		return "(no telemetry records were captured)"
	}
	return b.String()
}

// --- the real usage/accounting sink -------------------------------------------

// algProtocolMessages are the three production telemetry messages the preferred
// completion protocol is observable through. The privacy property of requirement
// 11.2 is scoped to exactly these messages: they are the ones that carry the
// activation, control, and terminal bounded dimensions this task certifies.
// Unrelated executor diagnostics (attempt_opened, cancellation, crash reports)
// carry their own attributes and are owned elsewhere; nothing in this file widens
// or weakens them.
var algProtocolMessages = []string{
	"control_tool_projection",
	"control_tool_call",
	"terminal_decision_evaluation",
}

// algProtocolTelemetry returns every captured record of one of the three
// protocol telemetry messages.
func algProtocolTelemetry(sink *algTelemetrySink) []algTelemetryRecord {
	var out []algTelemetryRecord
	for _, message := range algProtocolMessages {
		out = append(out, sink.withMessage(message)...)
	}
	return out
}

// algUsageRecorder is a real [usage.Observer]: the accounting seam the production
// runtime snapshot already owns. It retains every canonical usage observation the
// runtime emits, so requirements 11.3 and 11.4 are proved against the quantities
// the runtime really attributed.
type algUsageRecorder struct {
	mu     sync.Mutex
	events []usage.Event
}

func (r *algUsageRecorder) OnUsage(_ context.Context, ev usage.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
	return nil
}

func (r *algUsageRecorder) snapshot() []usage.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]usage.Event(nil), r.events...)
}

// --- the real auxiliary seam ---------------------------------------------------

// algAuxRecorder observes the REAL auxiliary seam. It wraps the production
// auxiliary client rather than replacing it: every recorded request is one the
// real verifier really built and the real executor really received, and the
// request is forwarded unchanged.
//
// requirement 11.4 is about auxiliary usage/TRACE LINEAGE, so what this recorder
// certifies is the lineage the real detached request carries: its bounded role,
// its private visibility, its detached session mode, the suppression list that
// keeps the request non-recursive, and its parent trace/A-leg/B-leg references.
// It records the request the verifier really built whether or not that request is
// authorized to reach a provider; today it is not, which is what the legacy
// auxiliary cell asserts explicitly.
type algAuxRecorder struct {
	inner   auxiliary.Client
	mu      sync.Mutex
	records []auxiliary.Request
}

func (r *algAuxRecorder) Stream(ctx context.Context, req auxiliary.Request) (aipapi.EventStream, error) {
	r.record(req)
	return r.inner.Stream(ctx, req)
}

func (r *algAuxRecorder) Collect(ctx context.Context, req auxiliary.Request) (aipapi.Collected, error) {
	r.record(req)
	return r.inner.Collect(ctx, req)
}

func (r *algAuxRecorder) record(req auxiliary.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, req)
}

func (r *algAuxRecorder) snapshot() []auxiliary.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]auxiliary.Request(nil), r.records...)
}

// --- deployment with the observability seams attached --------------------------

// algDeployTelemetry composes one real deployment that additionally exposes the
// real observability and accounting seams this task asserts against.
//
// The Agent Loop Guard generation is composed exactly as algDeployColumn composes
// it: the same production registry, enabled-surface merge, and production
// snapshot builder, replayed into a contribution set that additionally carries
// the shared observation factory. Only the snapshot OPTIONS differ: the real
// structured logger, the real usage observer, and the real production auxiliary
// client are attached. No provider fake and no telemetry fake is assigned.
func algDeployTelemetry(t *testing.T, col algColumn) (*Deployment, *algTrace, *algTelemetrySink, *algUsageRecorder, *algAuxRecorder) {
	t.Helper()

	var d *Deployment
	var tr *algTrace
	cs := aipfeature.NewContributionSet()
	if col.Strategy == "" {
		// The no-protocol comparison column composes no Agent Loop Guard generation
		// at all: it is the requirement 1.1 no-provider shape, reached through the
		// same real harness selector with an empty strategy.
		d = Deploy(t, DeploymentSpec{
			Frontend:      col.Frontend,
			Backend:       col.Backend,
			ProfileID:     col.ProfileID,
			Transport:     col.Transport,
			OriginHandler: col.Origin,
			Candidates:    col.Candidates,
		})
		if d == nil {
			t.Fatal("Deploy returned nil for the no-protocol comparison column")
		}
		tr = &algTrace{}
		d.Exec.Bus = hooks.New(hooks.Config{ResponsePartHooks: []sdkhooks.ResponsePartHook{algTraceHook{tr: tr}}})
	} else {
		d, tr = algDeployColumn(t, col)
		planes, err := AgentLoopGuardFeaturePlanes(t, col.Strategy)
		if err != nil {
			t.Fatalf("AgentLoopGuardFeaturePlanes: %v", err)
		}
		if err := planes.ReplayTo(cs, "alg-telemetry-observer"); err != nil {
			t.Fatalf("ReplayTo: %v", err)
		}
	}

	tel := &algTelemetrySink{}
	usageRec := &algUsageRecorder{}
	auxRec := &algAuxRecorder{}

	// The real structured logger reaches every production telemetry call site:
	// the runtime reads Executor.Log when it opens an attempt, so installing the
	// capturing handler before the request is enough.
	d.Exec.Log = slog.New(tel)

	if err := aipfeature.Contribute(cs, aipfeature.PlaneStreamObserverFactories, "alg-telemetry-observer", []response.StreamObserverFactory{algTraceObserverFactory{tr: tr}}); err != nil {
		t.Fatalf("Contribute stream observer factories: %v", err)
	}
	d.Exec.RuntimeSnapshot = extensions.NewRequestRuntimeSnapshot(d.Exec.Bus, extensions.SnapshotOptions{
		FeaturePlanes: cs.Freeze(),
		UsageObserver: usageRec,
		// The real production auxiliary construction, wired exactly as
		// internal/infra/runtimebundle/build_extension.go wires it, so a legacy
		// verifier's detached request really runs through this executor. The
		// recorder wraps it rather than replacing it.
		Aux: auxRecOf(auxRec, d),
	})
	return d, tr, tel, usageRec, auxRec
}

// auxRecOf returns the recording wrapper over the real production auxiliary
// client bound to this deployment's executor.
func auxRecOf(rec *algAuxRecorder, d *Deployment) auxiliary.Client {
	rec.inner = auxreq.NewClient(func() auxreq.ExecutorRunner { return d.Exec })
	return rec
}

// algTelemetryColumn is the message-authority OpenAI-Responses column the
// telemetry cells deploy, with the requested strategy.
func algTelemetryColumn(strategy string, origin http.Handler) algColumn {
	return algColumn{
		Strategy:  strategy,
		Frontend:  FrontendOpenAIResponses,
		Backend:   BackendOpenAIResponses,
		Transport: TransportJSON,
		Origin:    origin,
	}
}

// algTelemetryCreateBody renders one A-leg create document with an explicit
// input string and optional extra canonical fields, so every telemetry cell
// plants the client facts it needs.
func algTelemetryCreateBody(t *testing.T, input string, extra map[string]any) string {
	t.Helper()
	doc := map[string]any{"model": "gpt-4o-mini", "input": input, "stream": false}
	maps.Copy(doc, extra)
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal telemetry create body: %v", err)
	}
	return string(raw)
}

// --- bounded vocabularies, enumerated from their production owners -------------

// algActivationVocabulary is the CLOSED activation alphabet the generic
// control-tool projection seam may report. Every value is enumerated from the
// production constant that owns it, so the set cannot drift silently and an
// unbounded activation reason fails the test instead of being tolerated.
func algActivationVocabulary() map[string]bool {
	return map[string]bool{
		controltool.ReasonActive:                  true,
		controltool.ReasonBackendToolsUnsupported: true,
		controltool.ReasonToolChoiceNone:          true,
		controltool.ReasonToolChoiceConstrained:   true,
		controltool.ReasonToolChoiceRequired:      true,
		controltool.ReasonAllowedToolsConstrained: true,
		controltool.ReasonToolNameCollision:       true,
		controltool.ReasonInstructionCollision:    true,
		controltool.ReasonOutputFormatUnsupported: true,
	}
}

// algPreferredTerminalReasons is the CLOSED terminal reason alphabet the
// preferred-strategy terminal provider may report, enumerated from the production
// protocolpolicy constants.
func algPreferredTerminalReasons() map[string]bool {
	return map[string]bool{
		protocolpolicy.ReasonInvalidInput:       true,
		protocolpolicy.ReasonInvalidState:       true,
		protocolpolicy.ReasonAuthoritative:      true,
		protocolpolicy.ReasonExplicitCompletion: true,
		protocolpolicy.ReasonPreOutputFailure:   true,
		protocolpolicy.ReasonUnsafeAction:       true,
		protocolpolicy.ReasonMissingTrajectory:  true,
		protocolpolicy.ReasonMissingObjective:   true,
		protocolpolicy.ReasonProtocolInactive:   true,
		protocolpolicy.ReasonProtocolTerminal:   true,
		protocolpolicy.ReasonNoProgress:         true,
		protocolpolicy.ReasonBudgetExhausted:    true,
		protocolpolicy.ReasonIntentBuildFailed:  true,
		protocolpolicy.ReasonMissingSignal:      true,
	}
}

// algLegacyTerminalReasons is the CLOSED terminal reason alphabet the legacy
// semantic-verifier strategy may report, enumerated from the production causepolicy
// and progress constants. It is disjoint in practice from the preferred alphabet,
// which is exactly what lets telemetry distinguish the two strategies.
func algLegacyTerminalReasons() map[string]bool {
	out := map[string]bool{
		"context_canceled":       true,
		"deadline_exceeded":      true,
		"invalid_input":          true,
		"explicit_completion":    true,
		"output_not_committed":   true,
		"insufficient_evidence":  true,
		"unfinished_objective":   true,
		"budget_exhausted":       true,
		"invalid_progress_state": true,
	}
	// The shared cause policy and progress policy contribute their own bounded
	// classifications; they are enumerated here rather than tolerated by prefix.
	for _, reason := range []string{
		"authoritative_candidate",
		"pre_output_transport",
		"missing_trajectory",
		"verifier_eligible",
		"unsafe_action",
		"unsupported_cause",
	} {
		out[reason] = true
	}
	return out
}

// =============================================================================
// Scope item 1 — bounded protocol vocabularies telemetry can distinguish
// =============================================================================

// TestPreferredProtocolTelemetry_activationVocabularyIsClosedAndDistinguishable
// is requirement 11.1 for the activation dimension.
//
// The real control-tool projection stage logs one structured record per candidate
// activation attempt. Every value it reports must come from the CLOSED activation
// vocabulary, and the bounded outcomes requirement 11.1 names must each be
// distinguishable through that one seam: the active reason plus the
// backend-tools-unsupported, incompatible-tool-choice, and tool-name-collision
// reasons are each produced by a real deployment whose real backend capabilities
// and real client request create that condition.
//
// The cell runs serially because the backend-tools-unsupported row deploys the
// compatible-profile backend, whose real profile build installs a process
// environment variable and therefore cannot run in parallel.
func TestPreferredProtocolTelemetry_activationVocabularyIsClosedAndDistinguishable(t *testing.T) {
	const morphProfile = "morph"

	cases := []struct {
		name       string
		col        func(t *testing.T) algColumn
		body       func(t *testing.T) string
		wantReason string
	}{
		{
			name: "active",
			col: func(t *testing.T) algColumn {
				t.Helper()
				o, _ := algScriptedOrigin(t, algCompletionTurn(t, "resp_alg_tel_active"))
				return algTelemetryColumn(AgentLoopGuardStrategyAttemptCompletion, o)
			},
			body: func(t *testing.T) string {
				t.Helper()
				return algTelemetryCreateBody(t, "apply the schema, verify the backfill, then report the result", nil)
			},
			wantReason: controltool.ReasonActive,
		},
		{
			name: "backend_tools_unsupported",
			col: func(t *testing.T) algColumn {
				t.Helper()
				o, _ := algScriptedChatOrigin(t, algChatTextTurn(t, "chatcmpl_alg_tel_notools", algUnmarkedText))
				return algColumn{Strategy: AgentLoopGuardStrategyAttemptCompletion, Frontend: FrontendOpenAIResponses, Backend: BackendCompatibleOpenAI, ProfileID: morphProfile, Transport: TransportJSON, Origin: o}
			},
			body: func(t *testing.T) string {
				t.Helper()
				return algTelemetryCreateBody(t, "apply the schema, verify the backfill, then report the result", nil)
			},
			wantReason: controltool.ReasonBackendToolsUnsupported,
		},
		{
			name: "tool_choice_none",
			col: func(t *testing.T) algColumn {
				t.Helper()
				o, _ := algScriptedOrigin(t, algUnmarkedStopTurn(t, "resp_alg_tel_none"), algRepairStopTurn(t, "resp_alg_tel_none_repair"))
				return algTelemetryColumn(AgentLoopGuardStrategyAttemptCompletion, o)
			},
			body: func(t *testing.T) string {
				t.Helper()
				return algTelemetryCreateBody(t, "apply the schema, verify the backfill, then report the result", map[string]any{"tool_choice": "none"})
			},
			wantReason: controltool.ReasonToolChoiceNone,
		},
		{
			name: "tool_name_collision",
			col: func(t *testing.T) algColumn {
				t.Helper()
				o, _ := algScriptedOrigin(t, algUnmarkedStopTurn(t, "resp_alg_tel_collision"), algRepairStopTurn(t, "resp_alg_tel_collision_repair"))
				return algTelemetryColumn(AgentLoopGuardStrategyAttemptCompletion, o)
			},
			body: func(t *testing.T) string {
				t.Helper()
				return algTelemetryCreateBody(t, "apply the schema, verify the backfill, then report the result", map[string]any{
					"tools": []any{algClientTool(algControlToolName)},
				})
			},
			wantReason: controltool.ReasonToolNameCollision,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, _, tel, _, _ := algDeployTelemetry(t, tc.col(t))

			status, frames, err := algPostCreateBody(t.Context(), d, tc.body(t), false, nil)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if status != http.StatusOK && status != http.StatusBadRequest {
				t.Fatalf("status = %d, want a decided outcome; wire=%s", status, algWireBody(frames))
			}

			records := tel.withMessage("control_tool_projection")
			if len(records) == 0 {
				t.Fatalf("the real projection seam emitted NO activation telemetry; telemetry=%s", tel.dump())
			}
			for _, rec := range records {
				v, ok := rec.Attr("reason_code")
				if !ok {
					t.Fatalf("an activation record carries no reason_code attribute: %+v", rec)
				}
				if v.Kind() != slog.KindString {
					t.Fatalf("activation reason_code is %v, want a bounded string", v.Kind())
				}
				if !algActivationVocabulary()[v.String()] {
					t.Fatalf("activation reason_code %q is outside the closed activation vocabulary", v.String())
				}
				if rec.Str("provider_id") == "" {
					t.Fatalf("an activation record carries no provider identity: %+v", rec)
				}
			}
			if got := records[len(records)-1].Str("reason_code"); got != tc.wantReason {
				t.Fatalf("activation reason_code = %q, want %q; telemetry=%s", got, tc.wantReason, tel.dump())
			}
		})
	}
}

// TestPreferredProtocolTelemetry_terminalReasonsAreBoundedAndCoverRequiredOutcomes
// is requirement 11.1 for the terminal dimension.
//
// The real terminal owner reports one structured record per evaluation. Under the
// preferred strategy its reason alphabet must stay inside the closed
// protocolpolicy vocabulary, and requirement 11.1's completion-observed,
// reprompted, reprompt-exhausted and protocol-inactive outcomes must each be
// observable through that one seam.
func TestPreferredProtocolTelemetry_terminalReasonsAreBoundedAndCoverRequiredOutcomes(t *testing.T) {
	t.Parallel()

	// completion-observed: a turn whose only model output is the proxy-owned
	// completion, so the terminal policy consumes the trusted signal.
	completeOrigin, _ := algScriptedOrigin(t, algCompletionTurn(t, "resp_alg_tel_complete"))
	complete, _, completeTel, _, _ := algDeployTelemetry(t, algTelemetryColumn(AgentLoopGuardStrategyAttemptCompletion, completeOrigin))
	completeBody := algTelemetryCreateBody(t, "apply the schema, verify the backfill, then report the result", nil)
	if status, frames, err := algPostCreateBody(t.Context(), complete, completeBody, false, nil); err != nil {
		t.Fatalf("completion-observed create: %v", err)
	} else if status != http.StatusOK {
		t.Fatalf("completion-observed status = %d, want 200; wire=%s", status, algWireBody(frames))
	}

	// reprompted and reprompt-exhausted: one real unmarked two-leg turn, where the
	// first candidate reports the missing-signal reprompt reason and the
	// post-repair candidate reports the exhausted reason.
	unmarkedOrigin, unmarkedUpstream := algScriptedMissingSignalOrigin(t, algUnmarkedStopTurn(t, "resp_alg_tel_unmarked"), "resp_alg_tel_repair")
	unmarked, _, unmarkedTel, _, _ := algDeployTelemetry(t, algTelemetryColumn(AgentLoopGuardStrategyAttemptCompletion, unmarkedOrigin))
	if status, frames, err := algPostCreateBody(t.Context(), unmarked, completeBody, false, nil); err != nil {
		t.Fatalf("unmarked create: %v", err)
	} else if status != http.StatusOK {
		t.Fatalf("unmarked status = %d, want 200; wire=%s", status, algWireBody(frames))
	}
	algAssertBoundedUpstreamCount(t, unmarkedUpstream, 2)

	// protocol-inactive: the same client document with a client-owned completion
	// tool makes the proxy-owned protocol inactive, so the terminal policy must
	// say so with its own bounded reason instead of inventing missing work.
	inactiveOrigin, inactiveUpstream := algScriptedOrigin(t, algUnmarkedStopTurn(t, "resp_alg_tel_inactive"))
	inactive, _, inactiveTel, _, _ := algDeployTelemetry(t, algTelemetryColumn(AgentLoopGuardStrategyAttemptCompletion, inactiveOrigin))
	inactiveBody := algTelemetryCreateBody(t, "apply the schema, verify the backfill, then report the result", map[string]any{
		"tools": []any{algClientTool(algControlToolName)},
	})
	if status, frames, err := algPostCreateBody(t.Context(), inactive, inactiveBody, false, nil); err != nil {
		t.Fatalf("inactive create: %v", err)
	} else if status != http.StatusOK {
		t.Fatalf("inactive status = %d, want 200; wire=%s", status, algWireBody(frames))
	}
	algAssertBoundedUpstreamCount(t, inactiveUpstream, 1)

	seen := map[string]int{}
	for _, sink := range []*algTelemetrySink{completeTel, unmarkedTel, inactiveTel} {
		for _, rec := range sink.withMessage("terminal_decision_evaluation") {
			reason := rec.Str("reason_code")
			if reason == "" {
				t.Fatalf("a terminal evaluation record carries no reason_code: %+v", rec)
			}
			if rec.Str("provider_id") == "" {
				t.Fatalf("a terminal evaluation record carries no provider identity: %+v", rec)
			}
			if !algPreferredTerminalReasons()[reason] {
				t.Fatalf("preferred terminal reason_code %q is outside the closed protocolpolicy vocabulary", reason)
			}
			seen[reason]++
		}
	}

	// Requirement 11.1 names these four terminal outcomes explicitly; each must be
	// distinguishable through the one real seam.
	for _, want := range []struct {
		outcome string
		reason  string
	}{
		{"completion-observed", protocolpolicy.ReasonExplicitCompletion},
		{"reprompted", protocolpolicy.ReasonMissingSignal},
		{"reprompt-exhausted", protocolpolicy.ReasonBudgetExhausted},
		{"protocol-inactive", protocolpolicy.ReasonProtocolInactive},
	} {
		if seen[want.reason] == 0 {
			t.Fatalf("telemetry never reported the %q outcome (%s); observed reasons=%v telemetry=%s",
				want.outcome, want.reason, seen, unmarkedTel.dump())
		}
	}
}

// TestPreferredProtocolTelemetry_strategyVocabularyIsClosedAndDistinguishable
// is the strategy dimension of the task, read from the same real seams.
//
// The two strategies are the feature's own closed two-value configuration
// vocabulary, and telemetry must be able to tell them apart without a feature
// label being invented in generic core: the preferred strategy contributes a
// proxy-owned control provider and therefore emits activation telemetry under the
// control-provider identity, while the legacy strategy contributes no control
// provider and emits only the legacy terminal reason alphabet.
func TestPreferredProtocolTelemetry_strategyVocabularyIsClosedAndDistinguishable(t *testing.T) {
	t.Parallel()

	emittedControlProvider := map[string]bool{}
	for _, strategy := range []string{
		AgentLoopGuardStrategyAttemptCompletion,
		AgentLoopGuardStrategySemanticVerifier,
	} {
		origin, _ := algScriptedOrigin(t, algUnmarkedStopTurn(t, "resp_alg_strategy_"+strategy))
		d, _, tel, _, _ := algDeployTelemetry(t, algTelemetryColumn(strategy, origin))
		body := algTelemetryCreateBody(t, "apply the schema, verify the backfill, then report the result", nil)
		if status, frames, err := algPostCreateBody(t.Context(), d, body, false, nil); err != nil {
			t.Fatalf("%s create: %v", strategy, err)
		} else if status != http.StatusOK && status != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want a decided outcome; wire=%s", strategy, status, algWireBody(frames))
		}

		for _, rec := range tel.withMessage("control_tool_projection") {
			if rec.Str("provider_id") == "" {
				t.Fatalf("%s emitted an activation record with no provider identity", strategy)
			}
			if rec.Str("reason_code") == "" && rec.Str("reason_code") != controltool.ReasonActive {
				t.Fatalf("%s emitted an activation record outside the closed vocabulary", strategy)
			}
			emittedControlProvider[strategy] = true
		}
		for _, rec := range tel.withMessage("terminal_decision_evaluation") {
			reason := rec.Str("reason_code")
			if reason == "" {
				t.Fatalf("%s emitted a terminal record with no reason_code", strategy)
			}
			if !algLegacyTerminalReasons()[reason] && !algPreferredTerminalReasons()[reason] {
				t.Fatalf("%s reported terminal reason %q outside every enumerated bounded vocabulary", strategy, reason)
			}
		}
	}

	if !emittedControlProvider[AgentLoopGuardStrategyAttemptCompletion] {
		t.Fatal("the preferred strategy emitted no control-provider activation telemetry, so telemetry cannot distinguish the strategies")
	}
	if emittedControlProvider[AgentLoopGuardStrategySemanticVerifier] {
		t.Fatal("the legacy strategy emitted proxy-owned control-provider activation telemetry, violating requirement 1.3")
	}
}

// TestPreferredProtocolTelemetry_controlCallOutcomesAreBoundedAndDistinguishable
// is requirement 11.1 for the control dimension, covering the design's
// `control=observed|valid|invalid|args_too_large|multiple_calls|handler_error`
// vocabulary and the malformed-control-call outcome requirement 11.1 names.
//
// The real control-call interception seam must report a bounded, content-free
// outcome for a proxy-owned completion call, and it must distinguish the valid
// completion from the malformed one.
//
// The seam emits one record per CLAIMED EVENT, not per handled call: a start, each
// buffered args delta, and the completion verdict are separate claimed events. A
// claimed event that carries no classification of its own is the design's
// `observed` outcome, so this cell pins that the ordinary claim sequence is
// reported as observed rather than as malformed control traffic, that the outcome
// and reason dimensions of one record never contradict each other, and that the
// invalid count is exactly the number of malformed calls this fixture really
// claims.
func TestPreferredProtocolTelemetry_controlCallOutcomesAreBoundedAndDistinguishable(t *testing.T) {
	t.Parallel()

	origin, upstream := algScriptedOrigin(t,
		algCompletionTurn(t, "resp_alg_tel_valid"),
		algUpstreamTurn{JSON: algResourceJSON(t, "resp_alg_tel_malformed", []any{
			algControlOutput(algControlItemID, algControlCallID, `{"result":"`+algCompletionResult+`","command":"`+algControlToolName+`"}`),
		})},
	)
	d, _, tel, _, _ := algDeployTelemetry(t, algTelemetryColumn(AgentLoopGuardStrategyAttemptCompletion, origin))
	body := algTelemetryCreateBody(t, "apply the schema, verify the backfill, then report the result", nil)

	for _, turn := range []string{"valid completion", "malformed control call"} {
		if status, frames, err := algPostCreateBody(t.Context(), d, body, false, nil); err != nil {
			t.Fatalf("%s: %v", turn, err)
		} else if status != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200; wire=%s", turn, status, algWireBody(frames))
		}
	}

	// This fixture really serves three upstream legs: the valid completion ends
	// its turn, while the malformed completion earns the one bounded
	// protocol-repair leg and the scripted origin repeats its final turn, so the
	// proxy claims the same malformed call twice. Pinning the leg count is what
	// makes the exact malformed-verdict count below a fact about this fixture
	// rather than a magic number.
	algAssertBoundedUpstreamCount(t, upstream, 3)
	const wantMalformedVerdicts = 2

	records := tel.withMessage("control_tool_call")
	if len(records) == 0 {
		t.Fatalf("the real control-call seam emitted NO control-call telemetry for two handled proxy-owned completion calls; telemetry=%s",
			tel.dump())
	}

	outcomes := algControlOutcomeVocabulary()
	allowedReasons := algControlReasonVocabulary()
	seen := map[string]int{}
	reasons := map[string]int{}
	for _, rec := range records {
		outcome, ok := rec.Attr("outcome")
		if !ok {
			t.Fatalf("a control-call record carries no outcome attribute: %+v", rec)
		}
		if outcome.Kind() != slog.KindString {
			t.Fatalf("control outcome is %v, want a bounded string", outcome.Kind())
		}
		if !outcomes[outcome.String()] {
			t.Fatalf("control outcome %q is outside the closed control vocabulary", outcome.String())
		}
		if v, ok := rec.Attr("reason_code"); ok {
			if v.Kind() != slog.KindString {
				t.Fatalf("control reason_code is %v, want a bounded string", v.Kind())
			}
			if !allowedReasons[v.String()] {
				t.Fatalf("control reason_code %q is outside the closed control reason vocabulary", v.String())
			}
			reasons[v.String()]++
		} else {
			// Both bounded dimensions of one record must exist for them to agree.
			t.Fatalf("a control-call record carries no reason_code attribute: %+v", rec)
		}
		if rec.Str("provider_id") == "" {
			t.Fatalf("a control-call record carries no provider identity: %+v", rec)
		}
		seen[outcome.String()]++
	}
	// The outcome and the reason code of one record are one classification, so
	// they must never contradict each other in either direction. A seam that
	// collapses an unclassified claim into the unknown bucket would report
	// `invalid`/`control_call_reason_unknown` for an ordinary lifecycle event and
	// satisfy the vocabulary checks above while making the design's `observed`
	// outcome unreachable.
	for _, rec := range records {
		outcome, reason := rec.Str("outcome"), rec.Str("reason_code")
		var wantOutcome string
		switch reason {
		case controlReasonObserved:
			wantOutcome = controlOutcomeObserved
		case controlReasonCompletionOK:
			wantOutcome = controlOutcomeValid
		case controlReasonCompletionBad:
			wantOutcome = controlOutcomeInvalid
		default:
			// The single collapse bucket is shared by the released, fatal, and
			// handler-error paths, so it constrains only the vocabulary.
			continue
		}
		if outcome != wantOutcome {
			t.Fatalf("control-call telemetry pairs reason_code %q with outcome %q; one classification must report one outcome; telemetry=%s",
				reason, outcome, tel.dump())
		}
		if outcome == controlOutcomeObserved && reason != controlReasonObserved {
			t.Fatalf("an observed control call carries reason_code %q; the observed outcome must be distinguishable from the invalid collapse bucket; telemetry=%s",
				reason, tel.dump())
		}
	}
	// Requirement 11.1 names `observed` precisely so that an ordinary private-path
	// claim is distinguishable from a malformed control call. The design's control
	// vocabulary lists it, so the seam must really be able to report it.
	if seen[controlOutcomeObserved] == 0 {
		t.Fatalf("telemetry never reported a claimed lifecycle event as %q; an ordinary claim carries no verdict of its own and must not be reported as malformed traffic; outcomes=%v reasons=%v telemetry=%s",
			controlOutcomeObserved, seen, reasons, tel.dump())
	}
	if seen[controlOutcomeValid] == 0 {
		t.Fatalf("telemetry never reported a valid completion; outcomes=%v telemetry=%s", seen, tel.dump())
	}
	if seen[controlOutcomeInvalid] == 0 {
		t.Fatalf("telemetry never reported the malformed control call of requirement 11.1 as invalid; outcomes=%v telemetry=%s", seen, tel.dump())
	}
	// EXACTLY the malformed verdicts, never noise: this fixture claims the same
	// malformed call on two upstream legs, and every other claimed event is either
	// the valid completion or an ordinary unclassified claim. A seam that reports
	// ordinary lifecycle claims as malformed inflates this count, so it is pinned
	// to the real number rather than to "at least one".
	if got := seen[controlOutcomeInvalid]; got != wantMalformedVerdicts {
		t.Fatalf("telemetry reported %d invalid control-call outcome(s), want exactly the %d malformed-call verdicts this fixture claims; an ordinary claimed event reported as malformed control traffic is indistinguishable from the malformed call itself; outcomes=%v reasons=%v telemetry=%s",
			got, wantMalformedVerdicts, seen, reasons, tel.dump())
	}
	if got, want := reasons[controlReasonCompletionBad], wantMalformedVerdicts; got != want {
		t.Fatalf("telemetry reported %d record(s) carrying the bounded invalid reason %q, want %d; reasons=%v telemetry=%s",
			got, controlReasonCompletionBad, want, reasons, tel.dump())
	}

	// The two turns must be told apart by their classification, not merely both
	// appearing somewhere in the record. Exactly one handled call is valid, and its
	// classification is the provider's own bounded completion reason; the malformed
	// one is invalid and carries the provider's bounded invalid reason. A seam that
	// collapsed both onto one outcome would still satisfy the counts above, so the
	// pairing is asserted directly.
	if seen[controlOutcomeValid] != 1 {
		t.Fatalf("exactly one of the two handled control calls may be reported valid, got %d; the malformed call must not be classified as a completion: outcomes=%v telemetry=%s",
			seen[controlOutcomeValid], seen, tel.dump())
	}
	if reasons[controlReasonCompletionOK] != 1 {
		t.Fatalf("exactly one handled control call may carry the bounded completion reason %q, got %d; reasons=%v telemetry=%s",
			controlReasonCompletionOK, reasons[controlReasonCompletionOK], reasons, tel.dump())
	}
	if reasons[controlReasonCompletionBad] == 0 {
		t.Fatalf("telemetry never reported the malformed control call with the bounded invalid reason %q; reasons=%v telemetry=%s",
			controlReasonCompletionBad, reasons, tel.dump())
	}
}

// =============================================================================
// Scope item 2 — bounded result / args / prompt / raw-ID privacy
// =============================================================================

// TestPreferredProtocolTelemetry_noResultArgsPromptOrRawIDReachesAnyReasonCode
// is requirement 11.2 read from the real telemetry attribute set.
//
// Every structured attribute the three production telemetry seams attach is read
// directly. The bounded dimensions must carry only enumerated vocabulary values
// and none of the planted protocol content, and the only attributes permitted to
// carry raw correlation identifiers are the lineage fields the design's
// Observability section explicitly keeps in trace/log fields and never promotes
// to metric labels.
func TestPreferredProtocolTelemetry_noResultArgsPromptOrRawIDReachesAnyReasonCode(t *testing.T) {
	t.Parallel()

	const (
		canaryResult   = "PRIVACY-RESULT-9f3a: schema applied and backfill verified"
		canaryPrompt   = "PRIVACY-PROMPT-b71c applies the schema and verifies the backfill"
		canaryExtraArg = "PRIVACY-ARG-2d4e: privileged side effect"
		canaryCallID   = "call_alg_telemetry_private"
		canaryItemID   = "fc_alg_telemetry_private"
	)

	args := `{"result":"` + canaryResult + `","leak":"` + canaryExtraArg + `"}`
	origin, _ := algScriptedOrigin(t, algUpstreamTurn{JSON: algResourceJSON(t, "resp_alg_tel_privacy", []any{
		algControlOutput(canaryItemID, canaryCallID, args),
	})})
	d, _, tel, _, _ := algDeployTelemetry(t, algTelemetryColumn(AgentLoopGuardStrategyAttemptCompletion, origin))
	status, frames, err := algPostCreateBody(t.Context(), d, algTelemetryCreateBody(t, canaryPrompt, nil), false, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; wire=%s", status, algWireBody(frames))
	}

	records := algProtocolTelemetry(tel)
	if len(records) == 0 {
		t.Fatal("the deployment produced no protocol telemetry at all, so the privacy property cannot be proved")
	}

	canaries := map[string]string{
		"completion result text":  canaryResult,
		"control argument text":   canaryExtraArg,
		"prompt text":             canaryPrompt,
		"raw control call id":     canaryCallID,
		"raw control item id":     canaryItemID,
		"control argument member": `"leak"`,
	}
	// Bounded dimensions: every value must come from a closed enumerated set, and
	// none may carry protocol content.
	bounded := map[string]bool{
		"reason_code":     true,
		"outcome":         true,
		"decision_kind":   true,
		"candidate_cause": true,
		"provider_id":     true,
	}
	// Lineage correlation fields: the design's Observability section keeps existing
	// trace/A-leg/B-leg correlation in trace and log fields under current policy
	// and forbids promoting it to metric labels.
	lineage := map[string]bool{
		"trace_id": true,
		"call_id":  true,
		"a_leg_id": true,
		"b_leg_id": true,
	}
	// Non-content routing facts this seam already publishes on the same records.
	routing := map[string]bool{
		"candidate_key":    true,
		"backend":          true,
		"output_committed": true,
	}

	for _, rec := range records {
		for _, key := range rec.keys {
			value := rec.values[key].String()
			for label, canary := range canaries {
				if strings.Contains(value, canary) {
					t.Fatalf("telemetry attribute %q of record %q leaked the %s: record=%+v", key, rec.Message, label, rec)
				}
			}
			switch {
			case bounded[key]:
				if !algBoundedDimensionAllowed(key, value) {
					t.Fatalf("telemetry attribute %q of record %q carries the non-vocabulary value %q; every bounded dimension must be drawn from a closed enumerated set",
						key, rec.Message, value)
				}
			case lineage[key], routing[key]:
				// Classified above: permitted non-content facts.
			default:
				t.Fatalf("telemetry record %q attached the unclassified attribute %q; a new telemetry attribute must be classified as a bounded dimension, a lineage field, or a non-content routing fact",
					rec.Message, key)
			}
		}
	}
}

// algBoundedDimensionAllowed reports whether one bounded telemetry dimension
// carries a value from its closed enumerated production vocabulary.
func algBoundedDimensionAllowed(key, value string) bool {
	switch key {
	case "reason_code":
		return algActivationVocabulary()[value] ||
			algPreferredTerminalReasons()[value] ||
			algLegacyTerminalReasons()[value] ||
			algTerminalReasonVocabulary()[value] ||
			algControlReasonVocabulary()[value]
	case "outcome":
		return algControlOutcomeVocabulary()[value]
	case "decision_kind":
		switch value {
		case "allow_stop", "continue", "":
			return true
		default:
			return false
		}
	case "candidate_cause":
		switch value {
		case "normal", "refusal", "content_filter", "transport", "provider_error", "limit", "canceled", "authority_denied", "unknown":
			return true
		default:
			return false
		}
	case "provider_id":
		return value != "" && len(value) <= 128 && !strings.ContainsAny(value, " \t\n\"{}\\")
	default:
		return false
	}
}

// algTerminalReasonVocabulary is the CLOSED platform-owned terminal reason
// alphabet: the outcomes the generic terminal owner itself classifies before or
// around any feature provider.
func algTerminalReasonVocabulary() map[string]bool {
	return map[string]bool{
		"no_provider":       true,
		"invalid_provider":  true,
		"invalid_input":     true,
		"provider_error":    true,
		"provider_panic":    true,
		"invalid_decision":  true,
		"deadline_exceeded": true,
		"context_canceled":  true,
		"normal":            true,
		"refusal":           true,
		"content_filter":    true,
		"transport":         true,
		"limit":             true,
		"canceled":          true,
		"authority_denied":  true,
	}
}

// The bounded control-call outcome alphabet, spelled out literally from the
// design's Observability block
// (`control=observed|valid|invalid|args_too_large|multiple_calls|handler_error`)
// so the expectation is independent of the production code that owns it.
const (
	controlOutcomeObserved      = "observed"
	controlOutcomeValid         = "valid"
	controlOutcomeInvalid       = "invalid"
	controlOutcomeArgsTooLarge  = "args_too_large"
	controlOutcomeMultipleCalls = "multiple_calls"
	controlOutcomeHandlerError  = "handler_error"
)

// The bounded control-call reason alphabet: the closed capture classifications
// owned by internal/core/runtime/control_call_capture.go, the two validated
// provider outcome reasons owned by the feature's completion handler, the bare
// observed reason, and the single static collapse bucket the seam uses for any
// classification it does not recognise.
const (
	controlReasonObserved      = "control_call_observed"
	controlReasonCompletionOK  = "completion_complete"
	controlReasonCompletionBad = "completion_invalid"
	controlReasonUnknown       = "control_call_reason_unknown"
)

// algControlOutcomeVocabulary is the CLOSED control-call outcome alphabet.
func algControlOutcomeVocabulary() map[string]bool {
	return map[string]bool{
		controlOutcomeObserved:      true,
		controlOutcomeValid:         true,
		controlOutcomeInvalid:       true,
		controlOutcomeArgsTooLarge:  true,
		controlOutcomeMultipleCalls: true,
		controlOutcomeHandlerError:  true,
	}
}

// algControlReasonVocabulary is the CLOSED control-call reason alphabet.
func algControlReasonVocabulary() map[string]bool {
	out := map[string]bool{
		controlReasonObserved:      true,
		controlReasonCompletionOK:  true,
		controlReasonCompletionBad: true,
		controlReasonUnknown:       true,
	}
	for _, reason := range []string{
		"control_call_id_invalid",
		"control_call_duplicate_start",
		"control_call_duplicate_finish",
		"control_call_multiple_calls",
		"control_call_name_conflict",
		"control_call_args_after_finish",
		"control_call_args_overflow",
		"control_call_args_malformed",
		"control_call_before_start",
		"control_call_result_observed",
		"control_call_malformed_item",
		"control_call_unterminated",
	} {
		out[reason] = true
	}
	return out
}

// TestPreferredProtocolTelemetry_noMetricLabelCarriesProtocolContent is the
// metric-label half of requirement 11.2, read from the REAL production metric
// registry.
//
// The host's one Prometheus registry is built here exactly as production builds
// it, gathered through the real prometheus.Gatherer, and every label value of
// every gathered series is checked against the planted protocol content. Today the
// protocol owns no metric series at all, so this is characterization that the
// bounded vocabulary requirement holds structurally: there is no label to leak,
// and the day someone adds one this cell starts reading it.
func TestPreferredProtocolTelemetry_noMetricLabelCarriesProtocolContent(t *testing.T) {
	t.Parallel()

	const (
		canaryResult = "PRIVACY-METRIC-RESULT-51ad: backfill verified"
		canaryPrompt = "PRIVACY-METRIC-PROMPT-7c2e migrate the production schema"
	)

	bundle := metrics.NewBundle(nil, nil, nil)
	families, err := bundle.Registry.Gather()
	if err != nil {
		t.Fatalf("gather the real production metric registry: %v", err)
	}
	if len(families) == 0 {
		t.Fatal("the real production metric registry gathered no series at all, so the registry is not the sink under test")
	}

	canaries := []string{canaryResult, canaryPrompt, algControlCallID, algControlItemID, algCompletionResult}
	for _, mf := range families {
		for _, metric := range mf.GetMetric() {
			for _, pair := range metric.GetLabel() {
				value := pair.GetValue()
				for _, canary := range canaries {
					if strings.Contains(value, canary) {
						t.Fatalf("metric %q label %q carries protocol content %q", mf.GetName(), pair.GetName(), canary)
					}
				}
				// Every label value must stay inside a short, content-free token.
				if len(value) > 128 || strings.ContainsAny(value, "{}\"\n\r\t") {
					t.Fatalf("metric %q label %q carries a non-token value %q; metric labels must stay bounded and content-free",
						mf.GetName(), pair.GetName(), value)
				}
			}
		}
	}
}

// =============================================================================
// Scope item 3 — upstream usage and cost attribution
// =============================================================================

// TestPreferredProtocolTelemetry_upstreamUsageSurvivesLocalControlHandling is
// requirement 11.3 read from the real usage observer.
//
// A proxy-owned completion is handled entirely inside the proxy: it is not an
// upstream request, so it must create no provider usage. The scripted origin
// reports the SAME upstream quantities in both runs of the identical client
// request, so any divergence in the observed usage would be usage the proxy
// manufactured rather than usage the provider reported. The honest comparison is
// therefore the same A-leg document with no Agent Loop Guard generation composed
// at all.
func TestPreferredProtocolTelemetry_upstreamUsageSurvivesLocalControlHandling(t *testing.T) {
	t.Parallel()

	withProtocol := runAlgTelemetryUsageRun(t, AgentLoopGuardStrategyAttemptCompletion, true)
	withoutProtocol := runAlgTelemetryUsageRun(t, "", true)

	if len(withProtocol.usage) == 0 {
		t.Fatal("the protocol run produced no usage observation at all")
	}
	if withProtocol.upstreamLegs != 1 {
		t.Fatalf("the protocol run reached the upstream provider %d time(s), want exactly 1; local control handling is not an upstream request",
			withProtocol.upstreamLegs)
	}
	if len(withProtocol.usage) != 1 {
		t.Fatalf("the protocol run produced %d usage observation(s), want exactly 1; requirement 11.3 forbids manufacturing provider usage for the local control-tool handling: %v",
			len(withProtocol.usage), algUsageList(withProtocol.usage))
	}
	if len(withoutProtocol.usage) != len(withProtocol.usage) {
		t.Fatalf("the protocol run produced %d usage observation(s) and the no-protocol run produced %d for the same upstream turn; local control handling must create no extra provider usage",
			len(withProtocol.usage), len(withoutProtocol.usage))
	}

	got, want := withProtocol.usage[0], withoutProtocol.usage[0]
	if algUsageQuantities(got) != algUsageQuantities(want) {
		t.Fatalf("protocol-run usage = %s, want the unchanged upstream quantities %s from the no-protocol run for the identical upstream turn",
			algUsageQuantities(got), algUsageQuantities(want))
	}
	if got.TotalTokens <= 0 || got.OutputTokens <= 0 || got.InputTokens <= 0 {
		t.Fatalf("protocol-run usage reports non-positive quantities %s; the real provider quantities must survive local control handling",
			algUsageQuantities(got))
	}

	// The quantities must be attributed to the REAL B-leg of that run, not to a
	// synthetic control-local identity. Each deployment allocates its own opaque
	// B-leg, so the honest comparison is against the B-leg the same run's own
	// telemetry reported when the attempt really opened.
	if got.BLegID == "" {
		t.Fatal("protocol-run usage carries no B-leg attribution; upstream usage must stay attributed to the real B-leg")
	}
	if !withProtocol.bLegs[got.BLegID] {
		t.Fatalf("protocol-run usage is attributed to B-leg %q, which this run never opened; observed B-legs=%v",
			got.BLegID, withProtocol.bLegs)
	}
	if want.BLegID == "" || withoutProtocol.bLegs[want.BLegID] != true {
		t.Fatalf("the no-protocol run's usage is attributed to B-leg %q, which that run never opened; observed B-legs=%v",
			want.BLegID, withoutProtocol.bLegs)
	}
	// The completion the proxy handled locally is private: it is neither published
	// as client content nor carried into the provider usage observation, whose
	// quantity-only shape is the accounting contract.
	if strings.Contains(algUsageRawText(got), algCompletionResult) {
		t.Fatal("the local control-tool result text appears in the usage observation; provider usage must stay quantity-only")
	}
}

// algUsageRawText renders every content-bearing field of one usage observation so
// an assertion can read exactly what the accounting seam retained.
func algUsageRawText(ev usage.Event) string {
	return strings.Join([]string{ev.RawUsageJSON, ev.Model, ev.BackendID, ev.FrontendID, ev.SessionID}, " ")
}

// =============================================================================
// Scope item 4 — legacy auxiliary usage attribution
// =============================================================================

// TestPreferredProtocolTelemetry_legacyAuxiliaryLineageStaysSeparatelyAttributable
// is requirement 11.4 read from the real auxiliary seam, plus the requirement 9.4
// and 11.3 no-auxiliary half.
//
// Requirement 11.4 asks that the legacy verifier's existing auxiliary
// usage/TRACE LINEAGE remain separately attributable as before. The lineage is
// exactly what the real auxiliary request carries: a bounded role, private
// visibility, a detached session mode, a non-recursive suppression list, and the
// parent trace/A-leg/B-leg references. This cell reads those facts from the real
// request the real verifier built and the real executor really received, and it
// reads the real accounting seam for the primary turn's own quantities.
//
// USAGE attribution is characterized as it behaves in production today: the
// verifier's detached request fails closed at request-authority admission before
// any provider work, because the billing auxiliary-role allowlist carries no entry
// for the verifier's role. So no auxiliary leg is served and no auxiliary usage is
// attributable, and this cell says exactly that instead of claiming a usage
// observation the deployment cannot produce.
//
// The preferred strategy runs the identical clean-stop candidate and must issue no
// auxiliary request at all, which is what requirements 9.4 and 11.3 forbid.
func TestPreferredProtocolTelemetry_legacyAuxiliaryLineageStaysSeparatelyAttributable(t *testing.T) {
	t.Parallel()

	legacy := runAlgTelemetryAuxiliaryRun(t, AgentLoopGuardStrategySemanticVerifier)
	if len(legacy.auxiliary) == 0 {
		t.Fatal("the legacy verifier issued no auxiliary request at all; requirement 9.2 requires the detached bounded verifier")
	}
	if len(legacy.usage) == 0 {
		t.Fatal("the legacy run produced no primary usage observation")
	}

	primary := legacy.usage[0]
	for i, req := range legacy.auxiliary {
		// The bounded role is the feature's own fixed default, so it is a stable
		// classification rather than request content.
		if req.Role != agentloopguard.DefaultVerifierRole {
			t.Fatalf("auxiliary request %d role = %q, want the bounded default verifier role %q",
				i, req.Role, agentloopguard.DefaultVerifierRole)
		}
		if strings.TrimSpace(req.Visibility) == "" {
			t.Fatalf("auxiliary request %d carries no visibility classification", i)
		}
		if strings.ContainsAny(req.Role+req.Visibility, " \t\r\n") {
			t.Fatalf("auxiliary request %d carries a non-token lineage value: role=%q visibility=%q", i, req.Role, req.Visibility)
		}
		// The detached request stays inside its parent's lineage rather than
		// becoming an unrelated top-level request.
		if req.ParentTraceID != primary.TraceID {
			t.Fatalf("auxiliary request %d parent trace = %q, want the primary trace %q",
				i, req.ParentTraceID, primary.TraceID)
		}
		if req.ParentALegID != primary.ALegID {
			t.Fatalf("auxiliary request %d parent A-leg = %q, want the primary A-leg %q",
				i, req.ParentALegID, primary.ALegID)
		}
		if req.ParentBLegID != primary.BLegID {
			t.Fatalf("auxiliary request %d parent B-leg = %q, want the primary B-leg %q",
				i, req.ParentBLegID, primary.BLegID)
		}
		// The verifier suppresses itself, so its own auxiliary request can never
		// recurse into another verifier call.
		if !slices.Contains(req.DisablePlugins, agentloopguard.ID) {
			t.Fatalf("auxiliary request %d does not suppress %q; a recursive verifier call is not separately attributable",
				i, agentloopguard.ID)
		}
	}

	// The primary turn's own accounting is untouched by the auxiliary request: the
	// auxiliary lineage is correlated, never merged into the primary quantities.
	if primary.TotalTokens <= 0 || primary.InputTokens <= 0 {
		t.Fatalf("the legacy primary usage reports %s; the primary quantities must remain real",
			algUsageQuantities(primary))
	}
	if legacy.upstreamLegs != 1 {
		t.Fatalf("the legacy run reached the upstream provider %d time(s), want exactly its own primary leg; the verifier's own detached request is not served upstream today, because request-authority admission fails it closed before any provider work",
			legacy.upstreamLegs)
	}
	// The current production fact that makes requirement 11.4's auxiliary USAGE
	// half vacuous, asserted where it lives: the billing auxiliary-role allowlist
	// has no entry for the verifier's role, so no auxiliary workload identity can
	// exist for it and no auxiliary usage can be attributed to it. This is a
	// faithful characterization of a real gap outside this task's boundary, not an
	// aspiration. When the allowlist gains the verifier role, THIS assertion is
	// what must be replaced by an auxiliary usage-lineage assertion.
	if identity, err := usageevidence.WorkloadIdentityFromAuxiliaryRole(agentloopguard.DefaultVerifierRole); err == nil {
		t.Fatalf("billing now projects an auxiliary workload identity %+v for the verifier role %q, so the verifier's detached request is no longer rejected at request-authority admission; replace this characterization with an auxiliary usage-lineage assertion, requirement 11.4 is no longer vacuous",
			identity, agentloopguard.DefaultVerifierRole)
	}

	preferred := runAlgTelemetryAuxiliaryRun(t, AgentLoopGuardStrategyAttemptCompletion)
	if len(preferred.auxiliary) != 0 {
		t.Fatalf("the preferred strategy issued %d auxiliary request(s); requirements 9.4 and 11.3 forbid any verifier auxiliary work in the preferred path",
			len(preferred.auxiliary))
	}
	if len(preferred.usage) == 0 {
		t.Fatal("the preferred run produced no usage observation at all")
	}
	if preferred.upstreamLegs < 2 {
		t.Fatalf("the preferred strategy reached the upstream provider %d time(s), want the original leg plus its one bounded protocol-repair leg",
			preferred.upstreamLegs)
	}
}

// --- run helpers ---------------------------------------------------------------

// algUsageRun is one real deployment's accounting and auxiliary outcome.
type algUsageRun struct {
	usage        []usage.Event
	auxiliary    []auxiliary.Request
	upstreamLegs int
	// bLegs are the B-leg identities the real attempt-opened telemetry of the same
	// run reported, which is how a usage observation is proven to be attributed to
	// a B-leg this run really opened.
	bLegs map[string]bool
}

// runAlgTelemetryUsageRun drives one real turn against a scripted origin whose
// upstream resource reports a fixed, distinctive usage block.
//
// completion=true plants the proxy-owned completion call, which is handled
// entirely inside the proxy and therefore must never become an upstream request.
func runAlgTelemetryUsageRun(t *testing.T, strategy string, completion bool) algUsageRun {
	t.Helper()

	name := strategyName(strategy)
	origin, upstream := algScriptedOrigin(t, algTelemetryUsageTurn(t, "resp_alg_usage_"+name, completion))
	d, _, tel, usageRec, auxRec := algDeployTelemetry(t, algTelemetryColumn(strategy, origin))
	body := algTelemetryCreateBody(t, "apply the schema, verify the backfill, then report the result", nil)
	if status, frames, err := algPostCreateBody(t.Context(), d, body, false, nil); err != nil {
		t.Fatalf("%s create: %v", strategy, err)
	} else if status != http.StatusOK {
		t.Fatalf("%s status = %d, want 200; wire=%s", strategy, status, algWireBody(frames))
	}
	return algUsageRun{
		usage: usageRec.snapshot(), auxiliary: auxRec.snapshot(),
		upstreamLegs: upstream.count(), bLegs: algOpenedBLegs(tel),
	}
}

// algOpenedBLegs collects the B-leg identities the real attempt-opened telemetry
// of one run reported. It reads the same real structured sink the privacy cell
// reads, so it is telemetry evidence rather than an internal accessor.
func algOpenedBLegs(tel *algTelemetrySink) map[string]bool {
	out := map[string]bool{}
	for _, rec := range tel.withMessage("backend_attempt_opened") {
		if bLeg := rec.Str("b_leg_id"); bLeg != "" {
			out[bLeg] = true
		}
	}
	return out
}

// runAlgTelemetryAuxiliaryRun drives one real unmarked clean stop, the candidate
// class the legacy semantic verifier evaluates.
//
// Under the legacy strategy the verifier really builds its own detached request
// and really hands it to the production auxiliary client, so the recorder observes
// real request lineage. That request is NOT served by the scripted origin today:
// request-authority admission fails it closed before any provider work because the
// billing auxiliary-role allowlist has no entry for the verifier's role. The
// scripted second leg therefore exists to keep an unexpected auxiliary request
// observable in the served count rather than silently unanswered; the cell asserts
// the current production fact that the detached request never reaches it.
func runAlgTelemetryAuxiliaryRun(t *testing.T, strategy string) algUsageRun {
	t.Helper()

	name := strategyName(strategy)
	// Leg 0 answers the primary turn with ordinary committed text and a clean
	// stop. A second leg is scripted with the verifier verdict document so that,
	// if the verifier's detached auxiliary request ever does reach a provider,
	// it is served by the origin instead of failing as an unanswered request.
	// Today it never does: the billing auxiliary-role allowlist has no entry for
	// the verifier's role, so the request fails closed before provider
	// admission and the second leg stays unused. The cell asserts that current
	// production fact rather than the aspirational behaviour.
	origin := algScriptTransport(
		algLeg{json: algTelemetryUnmarkedResource(t, "resp_alg_aux_"+name)},
		algLeg{json: algTelemetryVerifierResource(t, "resp_alg_aux_"+name+"_verifier")},
	)
	d, _, tel, usageRec, auxRec := algDeployTelemetry(t, algTelemetryColumn(strategy, origin))
	body := algTelemetryCreateBody(t, "apply the schema, verify the backfill, then report the result", nil)
	if status, _, err := algPostCreateBody(t.Context(), d, body, false, nil); err != nil {
		t.Fatalf("%s create: %v", strategy, err)
	} else if status != http.StatusOK {
		t.Fatalf("%s status = %d, want 200", strategy, status)
	}
	return algUsageRun{
		usage: usageRec.snapshot(), auxiliary: auxRec.snapshot(),
		upstreamLegs: origin.algServeCount(), bLegs: algOpenedBLegs(tel),
	}
}

// algTelemetryUnmarkedResource is one upstream response carrying ordinary
// assistant text and a clean stop, with a distinctive usage block.
func algTelemetryUnmarkedResource(t *testing.T, id string) string {
	t.Helper()
	return algResourceJSONWithUsage(t, id, []any{algMessageOutput("msg_"+id, algUnmarkedText)}, 111, 22, 133)
}

// algTelemetryVerifierResource is the bounded verifier verdict the detached
// auxiliary request really receives. Its usage block is deliberately different
// from the primary turn's so the two lineages cannot be confused.
func algTelemetryVerifierResource(t *testing.T, id string) string {
	t.Helper()
	return algResourceJSONWithUsage(t, id, []any{algMessageOutput("msg_"+id, `{"kind":"COMPLETE","reason":"all requested work is done"}`)}, 7, 3, 10)
}

// algTelemetryUsageTurn is the completion/non-completion upstream wire for the
// usage-survival comparison, always reporting the same distinctive usage block.
func algTelemetryUsageTurn(t *testing.T, id string, completion bool) algUpstreamTurn {
	t.Helper()
	output := []any{algMessageOutput("msg_"+id, algUnmarkedText)}
	if completion {
		output = append(output, algControlOutput(algControlItemID, algControlCallID, algControlArgs(t, algCompletionResult)))
	}
	return algUpstreamTurn{JSON: algResourceJSONWithUsage(t, id, output, 111, 22, 133)}
}

// algResourceJSONWithUsage renders one completed upstream non-streaming response
// with a fixed usage block, so two runs of the same turn really do report the
// same provider quantities.
func algResourceJSONWithUsage(t *testing.T, id string, output []any, in, out, total int) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"id":         id,
		"object":     "response",
		"created_at": 1715620000,
		"status":     "completed",
		"model":      "gpt-4o-mini",
		"output":     output,
		"usage":      map[string]any{"input_tokens": in, "output_tokens": out, "total_tokens": total},
	})
	if err != nil {
		t.Fatalf("marshal upstream resource %q: %v", id, err)
	}
	return string(raw)
}

func strategyName(strategy string) string {
	if strategy == "" {
		return "none"
	}
	return strategy
}

// algUsageQuantities renders the quantity-only view of one usage observation, so
// no identifier or content can leak into a test failure either.
func algUsageQuantities(ev usage.Event) string {
	return fmt.Sprintf("in=%d out=%d cache_read=%d cache_write=%d reasoning=%d total=%d cost=%d %s",
		ev.InputTokens, ev.OutputTokens, ev.CacheReadTokens, ev.CacheWriteTokens,
		ev.ReasoningTokens, ev.TotalTokens, ev.CostNanoUnits, ev.Currency)
}

func algUsageList(events []usage.Event) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, algUsageQuantities(ev))
	}
	return out
}
