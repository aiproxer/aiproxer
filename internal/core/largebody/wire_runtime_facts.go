package largebody

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
)

// DefaultWireMaxAttempts is the pre-route retry-budget default carried in
// WireRouteFacts when route-plan assembly has not yet supplied a budget.
// Route-plan assembly owns the authoritative retry budget; this constant only
// seeds pre-route facts so the inventory stays complete without a Call.
const DefaultWireMaxAttempts = 3

// WireRouteFacts holds bounded route and candidate selection facts.
// Every field has an explicit named downstream consumer (Requirement 19.1, 19.4).
type WireRouteFacts struct {
	// ProfileID identifies the certified frontend profile.
	// Named consumer: candidate profile validation, backend wire proof lookup.
	ProfileID string

	// RouteSelector is the extracted route selector string.
	// Named consumer: buildRoutePlan, candidate resolution, late route-override validation.
	RouteSelector string

	// RouteOrigin names why the route was chosen: the client named it, the
	// proxy chose its default route, or a continuation parent's lineage
	// restored it.
	// Named consumer: route model resolution, initial route assessment.
	RouteOrigin routing.RouteOrigin

	// ClientModel is the client-requested model identifier.
	// Named consumer: route selection, backend capability check, default model fallback.
	ClientModel string

	// CandidateModel is the selected candidate model identifier for attempt execution.
	// Named consumer: candidate execution, model token splice, attempt verification.
	CandidateModel string

	// RoutePrefs contains caller-specified provider preferences.
	// Named consumer: candidate filtering, affinity key resolution.
	RoutePrefs []string

	// MaxAttempts bounds total attempt count.
	// Named consumer: retry budget enforcement in route plan.
	MaxAttempts int

	// DefaultBackend is the fallback backend identifier.
	// Named consumer: default backend selection in route plan.
	DefaultBackend string
}

// Validate enforces bounds under the semantic-fact budget.
func (f WireRouteFacts) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	for name, v := range map[string]string{
		"profile id":      f.ProfileID,
		"route selector":  f.RouteSelector,
		"client model":    f.ClientModel,
		"candidate model": f.CandidateModel,
		"default backend": f.DefaultBackend,
	} {
		if int64(len(v)) > maxFactBytes {
			return fmt.Errorf("largebody: route facts %s exceeds %d bytes", name, maxFactBytes)
		}
	}
	if int64(len(f.RoutePrefs)) > maxFactBytes {
		return fmt.Errorf("largebody: route prefs count exceeds %d", maxFactBytes)
	}
	for i, pref := range f.RoutePrefs {
		if int64(len(pref)) > maxFactBytes {
			return fmt.Errorf("largebody: route pref %d exceeds %d bytes", i, maxFactBytes)
		}
	}
	if f.MaxAttempts < 0 {
		return fmt.Errorf("largebody: route facts max attempts must be >= 0, got %d", f.MaxAttempts)
	}
	return nil
}

// WireProtocolFacts holds bounded protocol requirements and framing facts.
// Every field has an explicit named downstream consumer (Requirement 19.1, 19.4).
type WireProtocolFacts struct {
	// Operation is the certified request operation.
	// Named consumer: frontend-pipeline dispatch, wire-open framing.
	Operation aipapi.Operation

	// Delivery is the certified delivery mode (streaming vs non-streaming).
	// Named consumer: attempt execution, holdalive wrapping, response pipeline.
	Delivery aipapi.DeliveryMode

	// RequirementsID is the static profile-defined requirement-set label.
	// Named consumer: protocol conformance check, candidate feature requirements.
	RequirementsID string

	// ControlCount counts certified protocol control carriers.
	// Named consumer: protocol-control validation without prompt content.
	ControlCount int64
}

// Validate enforces bounds under the semantic-fact budget.
func (f WireProtocolFacts) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if strings.TrimSpace(string(f.Operation)) == "" {
		return fmt.Errorf("largebody: protocol facts operation must not be empty")
	}
	if int64(len(f.Operation)) > maxFactBytes {
		return fmt.Errorf("largebody: protocol facts operation exceeds %d bytes", maxFactBytes)
	}
	switch f.Delivery {
	case aipapi.DeliveryModeStreaming, aipapi.DeliveryModeNonStreaming:
	default:
		return fmt.Errorf("largebody: protocol facts delivery mode %q is unknown", string(f.Delivery))
	}
	if int64(len(f.RequirementsID)) > maxFactBytes {
		return fmt.Errorf("largebody: protocol requirements id exceeds %d bytes", maxFactBytes)
	}
	if f.ControlCount < 0 {
		return fmt.Errorf("largebody: protocol control count must be >= 0, got %d", f.ControlCount)
	}
	return nil
}

// WireMaxOutputFacts holds bounded max-output token bounds.
// Every field has an explicit named downstream consumer (Requirement 19.1, 19.4).
type WireMaxOutputFacts struct {
	// MaxOutputTokens is the effective maximum output token bound.
	// Named consumer: clamp preview, attempt authority admission, metering quantities.
	MaxOutputTokens int64

	// ClampedMaxOutput is the optional narrow-down clamped bound from authority admission.
	// Named consumer: attempt narrow-down clamp enforcement.
	ClampedMaxOutput *int64
}

// Validate enforces non-negative bounds.
func (f WireMaxOutputFacts) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if f.MaxOutputTokens < 0 {
		return fmt.Errorf("largebody: max output tokens must be >= 0, got %d", f.MaxOutputTokens)
	}
	if f.ClampedMaxOutput != nil && *f.ClampedMaxOutput < 0 {
		return fmt.Errorf("largebody: clamped max output must be >= 0, got %d", *f.ClampedMaxOutput)
	}
	return nil
}

// WireIdentityFacts holds bounded request and trace identity evidence.
// Every field has an explicit named downstream consumer (Requirement 16, 19.1, 19.4).
type WireIdentityFacts struct {
	// RequestID is the canonical logical request identifier.
	// Named consumer: trace correlation, frontend-ingress checkpoint ID, stable call ID.
	RequestID string

	// TraceID is the runtime trace identifier.
	// Named consumer: distributed tracing, diagnostics, checkpoint correlation.
	TraceID string

	// CanonicalDigest is the exact canonical semantic identity digest.
	// Named consumer: deterministic response IDs, timestamps, stable call token.
	CanonicalDigest IdentityDigest

	// CheckpointID is the deterministic public checkpoint identity.
	// Named consumer: metering public checkpoint identification.
	CheckpointID string
}

// Validate enforces identity presence and bounds.
func (f WireIdentityFacts) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if strings.TrimSpace(f.RequestID) == "" {
		return fmt.Errorf("largebody: identity request id must not be empty")
	}
	if int64(len(f.RequestID)) > maxFactBytes {
		return fmt.Errorf("largebody: identity request id exceeds %d bytes", maxFactBytes)
	}
	if int64(len(f.TraceID)) > maxFactBytes {
		return fmt.Errorf("largebody: identity trace id exceeds %d bytes", maxFactBytes)
	}
	if f.CanonicalDigest.IsZero() {
		return fmt.Errorf("largebody: identity canonical digest must not be zero")
	}
	if int64(len(f.CheckpointID)) > maxFactBytes {
		return fmt.Errorf("largebody: identity checkpoint id exceeds %d bytes", maxFactBytes)
	}
	return nil
}

// WireSessionFacts holds bounded session inputs and turn shapes.
// Every field has an explicit named downstream consumer (Requirement 14, 19.1, 19.4).
type WireSessionFacts struct {
	// Input holds authoritative session IDs and resume tokens.
	// Named consumer: PrepareSecureSession, ExecuteBeginTurn post-commit.
	Input SessionInput

	// TurnShape holds the bounded normalized client-turn shape without prompt text.
	// Named consumer: RecordClientTurnAfterGate.
	TurnShape ClientTurnShape

	// ClassificationEvidence holds the bounded classification summary for this
	// turn: accepted client identity plus fixed tool-category bits, and nothing
	// else. It lets the wire path classify without reconstructing a canonical
	// Call, and it carries no header bag, tool list, transcript or path
	// (Requirements 5.2, 5.3, 5.5).
	// Named consumer: wire session-classification stage.
	ClassificationEvidence sessionclassification.Evidence
}

// Validate enforces session and turn shape bounds under maxFactBytes.
func (f WireSessionFacts) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if err := f.Input.Validate(maxFactBytes); err != nil {
		return fmt.Errorf("largebody: session facts input: %w", err)
	}
	if err := f.TurnShape.Validate(maxFactBytes); err != nil {
		return fmt.Errorf("largebody: session facts turn shape: %w", err)
	}
	if err := f.ClassificationEvidence.Validate(maxFactBytes); err != nil {
		return fmt.Errorf("largebody: session facts classification evidence: %w", err)
	}
	return nil
}

// WireSourceFacts holds bounded source integrity and body bounds.
// Every field has an explicit named downstream consumer (Requirement 19.1, 19.4, 20).
type WireSourceFacts struct {
	// SourceDigest is the SHA-256 digest of captured source payload.
	// Named consumer: replay reader integrity check, attempt checkpoint evidence.
	SourceDigest SourceDigest

	// BodyBytes is the exact captured body size.
	// Named consumer: body limit check, spool reservation verification, response facts.
	BodyBytes int64

	// BodyMode is the certified body representation mode.
	// Named consumer: framing and media-type validation.
	BodyMode BodyMode
}

// Validate enforces source presence and non-negative body size.
func (f WireSourceFacts) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if f.SourceDigest.IsZero() {
		return fmt.Errorf("largebody: source digest must not be zero")
	}
	if f.BodyBytes <= 0 {
		return fmt.Errorf("largebody: source body bytes must be > 0, got %d", f.BodyBytes)
	}
	if err := f.BodyMode.Validate(); err != nil {
		return fmt.Errorf("largebody: source body mode: %w", err)
	}
	return nil
}

// WireRewriteFacts holds bounded rewrite contract and splice details.
// Every field has an explicit named downstream consumer (Requirement 9, 19.1, 19.4).
type WireRewriteFacts struct {
	// Semantics is the certified rewrite semantics contract.
	// Named consumer: backend wire proof validation (ResolveWireRequest, ResolveWireDomain).
	Semantics RewriteSemantics

	// ModelSpan is the exact byte range of the model token.
	// Named consumer: streaming model token splice reader.
	ModelSpan Span

	// ReplacementModel is the per-attempt candidate model identifier.
	// Named consumer: per-attempt replacement model encoding.
	ReplacementModel string

	// RewrittenLength is the computed outbound body length.
	// Named consumer: outbound HTTP Content-Length header calculation.
	RewrittenLength int64

	// RewriteDigest is the digest of the model rewrite token/splice.
	// Named consumer: backend attempt checkpoint evidence (WireBackendIngressInput.RewriteDigest).
	RewriteDigest [32]byte
}

// Validate enforces rewrite contract consistency and bounds.
func (f WireRewriteFacts) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if err := f.Semantics.Validate(); err != nil {
		return fmt.Errorf("largebody: rewrite facts semantics: %w", err)
	}
	if err := f.ModelSpan.Validate(); err != nil {
		return fmt.Errorf("largebody: rewrite facts model span: %w", err)
	}
	if f.Semantics.NeedsModelRewrite() && f.ModelSpan.Length == 0 {
		return fmt.Errorf("largebody: rewrite facts model rewrite requires non-empty span")
	}
	if int64(len(f.ReplacementModel)) > maxFactBytes {
		return fmt.Errorf("largebody: rewrite facts replacement model exceeds %d bytes", maxFactBytes)
	}
	if f.RewrittenLength < 0 {
		return fmt.Errorf("largebody: rewrite facts rewritten length must be >= 0, got %d", f.RewrittenLength)
	}
	return nil
}

// WireEconomicFacts holds bounded billing and metering facts.
// Every field has an explicit named downstream consumer (Requirement 15, 19.1, 19.4).
type WireEconomicFacts struct {
	// BillingCallID is the BillingCallID-scoped identity string.
	// Named consumer: BillingCallID-scoped exposure admission, usage append, journal commands.
	BillingCallID string

	// AccountID is the customer/principal account identifier.
	// Named consumer: cheap pre-route credit gate, exposure admission.
	AccountID string

	// CustomerPricingRef is the customer pricing configuration version reference.
	// Named consumer: post-usage rating, exposure quote.
	CustomerPricingRef string

	// ChargePolicyRef is the charge policy version reference.
	// Named consumer: charge policy evaluation.
	ChargePolicyRef string

	// RequestCount is the request quantity fact (always 1 for single-request wire turn).
	// Named consumer: metering quantities builder, clamp exposure quantities.
	RequestCount int64

	// MaxOutputQuantity is the max output quantity fact.
	// Named consumer: metering checkpoint quantities.
	MaxOutputQuantity int64
}

// Validate enforces non-empty billing call ID and bounds.
func (f WireEconomicFacts) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if strings.TrimSpace(f.BillingCallID) == "" {
		return fmt.Errorf("largebody: economic billing call id must not be empty")
	}
	for name, v := range map[string]string{
		"billing call id":      f.BillingCallID,
		"account id":           f.AccountID,
		"customer pricing ref": f.CustomerPricingRef,
		"charge policy ref":    f.ChargePolicyRef,
	} {
		if int64(len(v)) > maxFactBytes {
			return fmt.Errorf("largebody: economic facts %s exceeds %d bytes", name, maxFactBytes)
		}
	}
	if f.RequestCount < 0 {
		return fmt.Errorf("largebody: economic request count must be >= 0, got %d", f.RequestCount)
	}
	if f.MaxOutputQuantity < 0 {
		return fmt.Errorf("largebody: economic max output quantity must be >= 0, got %d", f.MaxOutputQuantity)
	}
	return nil
}

// WireTurnFacts groups the 8 explicit bounded runtime wire-fact domains.
// Every single fact has a named downstream consumer; no shadow Call schema is retained
// (Requirements 19.1, 19.4, 19.7; Design section 11).
type WireTurnFacts struct {
	Route     WireRouteFacts
	Protocol  WireProtocolFacts
	MaxOutput WireMaxOutputFacts
	Identity  WireIdentityFacts
	Session   WireSessionFacts
	Source    WireSourceFacts
	Rewrite   WireRewriteFacts
	Economic  WireEconomicFacts
}

// AuditNamedConsumers returns the complete mapping of every fact in the inventory
// to its explicit named downstream consumer (Requirement 19.1, 19.4).
func (f WireTurnFacts) AuditNamedConsumers() map[string]string {
	return map[string]string{
		"route.profile_id":                "candidate profile validation, backend wire proof lookup",
		"route.route_selector":            "buildRoutePlan, candidate resolution, late route-override validation",
		"route.route_origin":              "route model resolution, initial route assessment",
		"route.client_model":              "route selection, backend capability check, default model fallback",
		"route.candidate_model":           "candidate execution, model token splice, attempt verification",
		"route.route_prefs":               "candidate filtering, affinity key resolution",
		"route.max_attempts":              "retry budget enforcement in route plan",
		"route.default_backend":           "default backend selection in route plan",
		"protocol.operation":              "frontend-pipeline dispatch, wire-open framing",
		"protocol.delivery":               "attempt execution, holdalive wrapping, response pipeline",
		"protocol.requirements_id":        "protocol conformance check, candidate feature requirements",
		"protocol.control_count":          "protocol-control validation without prompt content",
		"max_output.max_output_tokens":    "clamp preview, attempt authority admission, metering quantities",
		"max_output.clamped_max_output":   "attempt narrow-down clamp enforcement",
		"identity.request_id":             "trace correlation, frontend-ingress checkpoint ID, stable call ID",
		"identity.trace_id":               "distributed tracing, diagnostics, checkpoint correlation",
		"identity.canonical_digest":       "deterministic response IDs, timestamps, stable call token",
		"identity.checkpoint_id":          "metering public checkpoint identification",
		"session.input":                   "PrepareSecureSession, ExecuteBeginTurn post-commit",
		"session.turn_shape":              "RecordClientTurnAfterGate",
		"session.classification_evidence": "wire session-classification stage (bounded metadata only)",
		"source.source_digest":            "replay reader integrity check, attempt checkpoint evidence",
		"source.body_bytes":               "body limit check, spool reservation verification, response facts",
		"source.body_mode":                "framing and media-type validation",
		"rewrite.semantics":               "backend wire proof validation (ResolveWireRequest, ResolveWireDomain)",
		"rewrite.model_span":              "streaming model token splice reader",
		"rewrite.replacement_model":       "per-attempt replacement model encoding",
		"rewrite.rewritten_length":        "outbound HTTP Content-Length header calculation",
		"rewrite.rewrite_digest":          "backend attempt checkpoint evidence (WireBackendIngressInput.RewriteDigest)",
		"economic.billing_call_id":        "BillingCallID-scoped exposure admission, usage append, journal commands",
		"economic.account_id":             "cheap pre-route credit gate, exposure admission",
		"economic.customer_pricing":       "post-usage rating, exposure quote",
		"economic.charge_policy":          "charge policy evaluation",
		"economic.request_count":          "metering quantities builder, clamp exposure quantities",
		"economic.max_output_quantity":    "metering checkpoint quantities",
	}
}

// shadowCallForbiddenTokens are field names (plus optional plural) that would
// indicate a retained shadow Call schema: prompt content, message trees, tool
// lists, attachments, raw headers, or history.
var shadowCallForbiddenTokens = []string{
	"PROMPT",
	"MESSAGES",
	"TOOLS",
	"ATTACHMENTS",
	"SYSTEM",
	"DEVELOPER",
	"RAWHEADERS",
	"CALL",
	"HISTORY",
}

// AssertNoShadowCall verifies structurally that WireTurnFacts retains no shadow
// Call schema: no prompt content, messages, tools, attachments, raw headers,
// history, and no aipapi.Call reference anywhere in the 8 fact domains
// (Requirements 19.1, 19.4, 19.7).
func (f WireTurnFacts) AssertNoShadowCall() error {
	callType := reflect.TypeFor[aipapi.Call]()
	seen := make(map[reflect.Type]bool)
	var walk func(current reflect.Type, path string) error
	walk = func(current reflect.Type, path string) error {
		for current.Kind() == reflect.Pointer {
			current = current.Elem()
		}
		switch current.Kind() {
		case reflect.Array, reflect.Slice:
			return walk(current.Elem(), path+"[]")
		case reflect.Map:
			if err := walk(current.Elem(), path+"[value]"); err != nil {
				return err
			}
			return walk(current.Key(), path+"[key]")
		case reflect.Struct:
			if current == callType {
				return fmt.Errorf("largebody: wire facts contain aipapi.Call at %s", path)
			}
			if seen[current] {
				return nil
			}
			seen[current] = true
			for field := range current.Fields() {
				upperName := strings.ToUpper(field.Name)
				for _, forbidden := range shadowCallForbiddenTokens {
					if upperName == forbidden || upperName == forbidden+"S" {
						return fmt.Errorf("largebody: forbidden shadow Call field %q at %s", field.Name, path+"."+field.Name)
					}
				}
				if err := walk(field.Type, path+"."+field.Name); err != nil {
					return err
				}
			}
			return nil
		default:
			return nil
		}
	}
	return walk(reflect.TypeFor[WireTurnFacts](), "WireTurnFacts")
}

// wireFactConsumerAliases maps irregular struct field paths to their canonical
// audit keys in AuditNamedConsumers. These are deliberate, reviewable exceptions
// to the default domain.snake_case(field) rule.
var wireFactConsumerAliases = map[string]string{
	"Economic.CustomerPricingRef": "economic.customer_pricing",
	"Economic.ChargePolicyRef":    "economic.charge_policy",
}

// wireFactSnake converts a CamelCase Go field or type name to snake_case,
// treating the ID acronym as a single word (ProfileID -> profile_id).
func wireFactSnake(name string) string {
	name = strings.ReplaceAll(name, "ID", "Id")
	var out strings.Builder
	for i, r := range name {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				out.WriteByte('_')
			}
			out.WriteRune(r - 'A' + 'a')
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}

// wireFactDomainKey derives the audit-key domain prefix from a domain struct
// type name (WireRouteFacts -> route, WireMaxOutputFacts -> max_output).
func wireFactDomainKey(typeName string) string {
	stripped := strings.TrimPrefix(typeName, "Wire")
	stripped = strings.TrimSuffix(stripped, "Facts")
	return wireFactSnake(stripped)
}

// AssertConsumerCoverage mechanically verifies the "only facts with named
// consumers" ratchet (Requirements 19.1, 19.4): every direct field of all 8
// fact domains must have a non-empty AuditNamedConsumers entry, and every
// audit key must resolve to a real struct field (stale entries fail too).
func (f WireTurnFacts) AssertConsumerCoverage() error {
	consumers := f.AuditNamedConsumers()
	turnType := reflect.TypeFor[WireTurnFacts]()
	for domainField := range turnType.Fields() {
		domainType := domainField.Type
		for domainType.Kind() == reflect.Pointer {
			domainType = domainType.Elem()
		}
		if domainType.Kind() != reflect.Struct {
			return fmt.Errorf("largebody: wire facts domain %q is not a struct", domainField.Name)
		}
		domain := wireFactDomainKey(domainType.Name())
		for factField := range domainType.Fields() {
			if !factField.IsExported() {
				continue
			}
			key := domain + "." + wireFactSnake(factField.Name)
			if alias, ok := wireFactConsumerAliases[domainField.Name+"."+factField.Name]; ok {
				key = alias
			}
			consumer, ok := consumers[key]
			if !ok {
				return fmt.Errorf("largebody: wire fact %q has no named consumer entry", domainField.Name+"."+factField.Name)
			}
			if strings.TrimSpace(consumer) == "" {
				return fmt.Errorf("largebody: wire fact %q has an empty named consumer", key)
			}
		}
	}
	for key := range consumers {
		domain, rest, found := strings.Cut(key, ".")
		if !found || strings.TrimSpace(rest) == "" {
			return fmt.Errorf("largebody: malformed consumer audit key %q", key)
		}
		resolved := false
		for i := 0; i < turnType.NumField() && !resolved; i++ {
			domainField := turnType.Field(i)
			domainType := domainField.Type
			for domainType.Kind() == reflect.Pointer {
				domainType = domainType.Elem()
			}
			if domainType.Kind() != reflect.Struct || wireFactDomainKey(domainType.Name()) != domain {
				continue
			}
			for factField := range domainType.Fields() {
				if !factField.IsExported() {
					continue
				}
				expected := domain + "." + wireFactSnake(factField.Name)
				if alias, ok := wireFactConsumerAliases[domainField.Name+"."+factField.Name]; ok {
					expected = alias
				}
				if expected == key {
					resolved = true
					break
				}
			}
		}
		if !resolved {
			return fmt.Errorf("largebody: stale consumer audit key %q matches no struct field", key)
		}
	}
	return nil
}

// Validate checks bounds across all 8 fact domains under maxFactBytes.
func (f WireTurnFacts) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if err := f.Route.Validate(maxFactBytes); err != nil {
		return fmt.Errorf("largebody: wire turn facts route: %w", err)
	}
	if err := f.Protocol.Validate(maxFactBytes); err != nil {
		return fmt.Errorf("largebody: wire turn facts protocol: %w", err)
	}
	if err := f.MaxOutput.Validate(maxFactBytes); err != nil {
		return fmt.Errorf("largebody: wire turn facts max output: %w", err)
	}
	if err := f.Identity.Validate(maxFactBytes); err != nil {
		return fmt.Errorf("largebody: wire turn facts identity: %w", err)
	}
	if err := f.Session.Validate(maxFactBytes); err != nil {
		return fmt.Errorf("largebody: wire turn facts session: %w", err)
	}
	// The carrier must describe the same turn the protocol facts describe:
	// classifying a different operation than the one being executed would let a
	// wire turn be judged by another request's evidence
	// (Requirements 5.2, 5.5).
	if !f.Session.ClassificationEvidence.IsZero() && f.Session.ClassificationEvidence.Operation != f.Protocol.Operation {
		return fmt.Errorf("largebody: wire turn facts classification evidence operation %q does not match protocol operation %q",
			string(f.Session.ClassificationEvidence.Operation), string(f.Protocol.Operation))
	}
	if err := f.Source.Validate(maxFactBytes); err != nil {
		return fmt.Errorf("largebody: wire turn facts source: %w", err)
	}
	if err := f.Rewrite.Validate(maxFactBytes); err != nil {
		return fmt.Errorf("largebody: wire turn facts rewrite: %w", err)
	}
	if err := f.Economic.Validate(maxFactBytes); err != nil {
		return fmt.Errorf("largebody: wire turn facts economic: %w", err)
	}
	return nil
}

// NewWireTurnFactsFromProof constructs a WireTurnFacts from a validated Proof and AssessmentStamp.
func NewWireTurnFactsFromProof(proof Proof, stamp AssessmentStamp, requestID, traceID, billingCallID string) (WireTurnFacts, error) {
	if stamp.IsZero() {
		return WireTurnFacts{}, fmt.Errorf("largebody: assessment stamp must not be zero")
	}
	reqID := strings.TrimSpace(requestID)
	if reqID == "" {
		reqID = proof.Session.AuthoritativeSessionID
	}
	if reqID == "" {
		reqID = proof.Identity.String()
	}
	trID := strings.TrimSpace(traceID)
	if trID == "" {
		trID = reqID
	}
	bCallID := strings.TrimSpace(billingCallID)
	if bCallID == "" {
		bCallID = reqID
	}

	facts := WireTurnFacts{
		Route: WireRouteFacts{
			ProfileID:     proof.ProfileID,
			RouteSelector: proof.RouteSelector,
			RouteOrigin:   proof.RouteOrigin,
			ClientModel:   proof.ClientModel,
			MaxAttempts:   DefaultWireMaxAttempts,
		},
		Protocol: WireProtocolFacts{
			Operation:      proof.Operation,
			Delivery:       proof.Delivery,
			RequirementsID: proof.Facts.RequirementsID,
			ControlCount:   proof.Facts.ControlCount,
		},
		MaxOutput: WireMaxOutputFacts{
			MaxOutputTokens: proof.MaxOutputTokens,
		},
		Identity: WireIdentityFacts{
			RequestID:       reqID,
			TraceID:         trID,
			CanonicalDigest: proof.Identity,
			CheckpointID:    "customer-request:" + reqID,
		},
		Session: WireSessionFacts{
			Input:                  proof.Session,
			TurnShape:              proof.Turn,
			ClassificationEvidence: proof.ClassificationEvidence,
		},
		Source: WireSourceFacts{
			SourceDigest: proof.Source,
			BodyBytes:    proof.BodyBytes,
			BodyMode:     proof.Mode,
		},
		Rewrite: WireRewriteFacts{
			Semantics: proof.Rewrite,
			ModelSpan: proof.ModelSpan,
		},
		Economic: WireEconomicFacts{
			BillingCallID:     bCallID,
			RequestCount:      1,
			MaxOutputQuantity: proof.MaxOutputTokens,
		},
	}

	return facts, nil
}

// ToWireRequestFacts projects facts into a WireRequestFacts for candidate attempt evaluation.
func (f WireTurnFacts) ToWireRequestFacts(candidateModel string) WireRequestFacts {
	cand := candidateModel
	if cand == "" {
		cand = f.Route.CandidateModel
	}
	if cand == "" {
		cand = f.Route.ClientModel
	}
	return WireRequestFacts{
		ProfileID:       f.Route.ProfileID,
		Operation:       f.Protocol.Operation,
		Delivery:        f.Protocol.Delivery,
		BodyMode:        f.Source.BodyMode,
		Rewrite:         f.Rewrite.Semantics,
		ClientModel:     f.Route.ClientModel,
		CandidateModel:  cand,
		MaxOutputTokens: f.MaxOutput.MaxOutputTokens,
	}
}

// ToWireDomainFacts projects facts into a WireDomainFacts for domain evaluation.
func (f WireTurnFacts) ToWireDomainFacts(universal bool, candidateModels ...string) WireDomainFacts {
	return WireDomainFacts{
		ProfileID:       f.Route.ProfileID,
		Operation:       f.Protocol.Operation,
		Delivery:        f.Protocol.Delivery,
		BodyMode:        f.Source.BodyMode,
		Rewrite:         f.Rewrite.Semantics,
		UniversalModel:  universal,
		CandidateModels: candidateModels,
	}
}

// ToResponseFacts projects facts into ResponseFacts for frontend response bridging.
func (f WireTurnFacts) ToResponseFacts(effectiveModel string) ResponseFacts {
	model := effectiveModel
	if model == "" {
		model = f.Route.CandidateModel
	}
	if model == "" {
		model = f.Route.ClientModel
	}
	alegID := f.Session.Input.ALegID
	if alegID == "" {
		alegID = f.Identity.RequestID
	}
	sessID := f.Session.Input.AuthoritativeSessionID
	if sessID == "" {
		sessID = f.Identity.RequestID
	}
	return ResponseFacts{
		RequestID:       f.Identity.RequestID,
		TraceID:         f.Identity.TraceID,
		ALegID:          alegID,
		SessionID:       sessID,
		Operation:       f.Protocol.Operation,
		Delivery:        f.Protocol.Delivery,
		EffectiveModel:  model,
		Source:          f.Source.SourceDigest,
		BodyBytes:       f.Source.BodyBytes,
		RewrittenLength: f.Rewrite.RewrittenLength,
	}
}

// ToSessionResponseCarrier projects facts into SessionResponseCarrier for frontend session headers.
func (f WireTurnFacts) ToSessionResponseCarrier() SessionResponseCarrier {
	return SessionResponseCarrier{
		AuthoritativeSessionID: f.Session.Input.AuthoritativeSessionID,
		ALegID:                 f.Session.Input.ALegID,
		ResumeToken:            f.Session.Input.ResumeToken,
	}
}

// DefaultTestWireTurnFacts returns a fully populated, valid WireTurnFacts for tests and benchmarks.
func DefaultTestWireTurnFacts() WireTurnFacts {
	return WireTurnFacts{
		Route: WireRouteFacts{
			ProfileID:      "openai-responses-v1",
			RouteSelector:  "gpt-5",
			ClientModel:    "gpt-5",
			CandidateModel: "gpt-5-turbo",
			RoutePrefs:     []string{"prefer-fast"},
			MaxAttempts:    DefaultWireMaxAttempts,
			DefaultBackend: "openai-default",
		},
		Protocol: WireProtocolFacts{
			Operation:      aipapi.OperationOpenAIResponses,
			Delivery:       aipapi.DeliveryModeStreaming,
			RequirementsID: "req-responses-v1",
			ControlCount:   1,
		},
		MaxOutput: WireMaxOutputFacts{
			MaxOutputTokens: 4096,
		},
		Identity: WireIdentityFacts{
			RequestID:       "req-default-test",
			TraceID:         "trace-default-test",
			CanonicalDigest: NewIdentityDigest([32]byte{1, 2, 3, 4}),
			CheckpointID:    "customer-request:req-default-test",
		},
		Session: WireSessionFacts{
			Input: SessionInput{
				AuthoritativeSessionID: "sess-default-test",
				ClientSessionID:        "client-sess-default",
				ALegID:                 "aleg-default-test",
				ResumeToken:            NewSensitiveString("secret-token"),
				NewSessionRequested:    true,
			},
			TurnShape: ClientTurnShape{
				Items: []ClientTurnItemShape{
					{
						Kind:    aipapi.ItemKindMessage,
						Role:    aipapi.RoleUser,
						Ordinal: 0,
						Parts: []ClientTurnPartShape{
							{Kind: aipapi.ContentPartText, ContentBytes: 256},
						},
					},
				},
				TotalContentBytes: 256,
			},
			ClassificationEvidence: sessionclassification.Evidence{
				Operation:       aipapi.OperationOpenAIResponses,
				ClientUserAgent: "codex_cli_rs/1.2.3",
				ToolCategories: sessionclassification.ToolCategorySet(0).
					AddToolName("read_file").AddToolName("edit_file").AddToolName("bash"),
			},
		},
		Source: WireSourceFacts{
			SourceDigest: NewSourceDigest([32]byte{5, 6, 7, 8}),
			BodyBytes:    256,
			BodyMode:     BodyModeIdentityJSON,
		},
		Rewrite: WireRewriteFacts{
			Semantics:        NewNoRewrite(),
			ReplacementModel: "",
			RewrittenLength:  256,
		},
		Economic: WireEconomicFacts{
			BillingCallID:      "bill-default-test",
			AccountID:          "acct-default-test",
			CustomerPricingRef: "pricing-v1",
			ChargePolicyRef:    "charge-v1",
			RequestCount:       1,
			MaxOutputQuantity:  4096,
		},
	}
}
