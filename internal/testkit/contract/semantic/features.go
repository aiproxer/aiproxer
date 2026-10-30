package semantic

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkcontract "github.com/aiproxer/aiproxer/pkg/aipsdk/contract"
)

// Semantic scenario metadata is defined in the public SDK contract package so
// connector authors and in-process TCKs consume one corpus.
type (
	SemanticFeature    = sdkcontract.SemanticFeature
	ScenarioID         = sdkcontract.ScenarioID
	ScenarioTransport  = sdkcontract.ScenarioTransport
	ScenarioDescriptor = sdkcontract.ScenarioDescriptor
)

const (
	FeatureText             = sdkcontract.FeatureText
	FeatureStreaming        = sdkcontract.FeatureStreaming
	FeatureTools            = sdkcontract.FeatureTools
	FeatureParallelTools    = sdkcontract.FeatureParallelTools
	FeatureVision           = sdkcontract.FeatureVision
	FeatureVideo            = sdkcontract.FeatureVideo
	FeatureAnnotations      = sdkcontract.FeatureAnnotations
	FeatureDocuments        = sdkcontract.FeatureDocuments
	FeatureStructuredOutput = sdkcontract.FeatureStructuredOutput
	FeatureReasoning        = sdkcontract.FeatureReasoning
	FeatureReasoningReplay  = sdkcontract.FeatureReasoningReplay
	FeatureAssistantPhase   = sdkcontract.FeatureAssistantPhase
	FeatureAssistantMedia   = sdkcontract.FeatureAssistantMedia
	FeatureOrderedItems     = sdkcontract.FeatureOrderedItems
	FeatureItemReferences   = sdkcontract.FeatureItemReferences
	FeatureCompaction       = sdkcontract.FeatureCompaction
	FeatureExtensions       = sdkcontract.FeatureExtensions
	FeatureLifecycle        = sdkcontract.FeatureLifecycle
	TransportHTTP           = sdkcontract.TransportHTTP
	TransportStreaming      = sdkcontract.TransportStreaming
	TransportWebSocket      = sdkcontract.TransportWebSocket
	TransportConnector      = sdkcontract.TransportConnector
)

// SubjectKind classifies a subject under test.
type SubjectKind string

const (
	KindFrontend        SubjectKind = "frontend"
	KindBackendFamily   SubjectKind = "backend_family"
	KindCanonicalCore   SubjectKind = "canonical_core"
	KindProviderProfile SubjectKind = "provider_profile"
	KindConnector       SubjectKind = "connector"
)

// SubjectDescriptor describes a subject certified by TCK.
type SubjectDescriptor struct {
	ID           string                `json:"id"`
	Kind         SubjectKind           `json:"kind"`
	Profile      string                `json:"profile,omitempty"`
	Capabilities []aipapi.Capability   `json:"capabilities,omitempty"`
	Dialects     aipapi.DialectSupport `json:"dialects,omitzero"`
	// Transports lists the transports supported by this subject. An empty list
	// means the subject accepts the scenario's declared transport.
	Transports []ScenarioTransport `json:"transports,omitempty"`
}

// ScenarioFailure describes a scenario execution failure.
type ScenarioFailure struct {
	ScenarioID ScenarioID `json:"scenario_id"`
	Reason     string     `json:"reason"`
	Fatal      bool       `json:"fatal"`
}

// ExecutionEvidence is emitted by a subject boundary after it executes a scenario.
// Certification must not manufacture this evidence by calling a lower-level probe itself.
type ExecutionEvidence struct {
	ScenarioID            ScenarioID `json:"scenario_id"`
	Executed              bool       `json:"executed"`
	BoundaryCalls         int        `json:"boundary_calls"`
	UpstreamCalls         int        `json:"upstream_calls"`
	Opened                bool       `json:"opened"`
	EffectiveCapabilities bool       `json:"effective_capabilities"`
	Derived               bool       `json:"derived"`
	ExactMatch            bool       `json:"exact_match"`
	Rejected              bool       `json:"rejected"`
	Accepted              bool       `json:"accepted"`
	CanonicalCall         bool       `json:"canonical_call"`
	WireResponse          bool       `json:"wire_response"`
	ErrorMapped           bool       `json:"error_mapped"`
	StreamValidated       bool       `json:"stream_validated"`
	UsagePresent          bool       `json:"usage_present"`
	LifecycleClosed       bool       `json:"lifecycle_closed"`
	Cancelled             bool       `json:"cancelled"`
}

// Certification contains machine-readable TCK certification evidence.
type Certification struct {
	SubjectID    string                `json:"subject_id"`
	SubjectKind  SubjectKind           `json:"subject_kind"`
	Profile      string                `json:"profile,omitempty"`
	Capabilities []aipapi.Capability   `json:"capabilities,omitempty"`
	Dialects     aipapi.DialectSupport `json:"dialects,omitzero"`
	Transports   []ScenarioTransport   `json:"transports,omitempty"`
	Passed       []ScenarioID          `json:"passed"`
	Negative     []ScenarioID          `json:"negative"`
	Failed       []ScenarioFailure     `json:"failed,omitempty"`
	Executed     bool                  `json:"executed"`
	// ExecutedScenarioIDs is the call-level evidence behind Passed/Negative.
	// Selector output alone is never certification evidence.
	ExecutedScenarioIDs []ScenarioID `json:"executed_scenarios"`
	HarnessCalls        int          `json:"harness_calls"`
}

// ValidateReleaseReady verifies the certification has no failures, valid metadata,
// exact scenario corpus membership, exact positive/negative selection alignment, and uniqueness.
func (c Certification) ValidateReleaseReady() error {
	if c.SubjectID == "" {
		return fmt.Errorf("certification subject ID must not be empty")
	}
	switch c.SubjectKind {
	case KindFrontend, KindBackendFamily, KindCanonicalCore, KindProviderProfile, KindConnector:
		// Valid
	default:
		return fmt.Errorf("certification subject kind %q is invalid", c.SubjectKind)
	}

	if !c.Executed || c.HarnessCalls == 0 {
		return fmt.Errorf("certification for %s (%s) has no executed harness boundary", c.SubjectID, c.SubjectKind)
	}
	if len(c.ExecutedScenarioIDs) == 0 {
		return fmt.Errorf("certification for %s (%s) has no executed scenario evidence", c.SubjectID, c.SubjectKind)
	}
	if len(c.Failed) > 0 {
		return fmt.Errorf("certification for %s (%s) has %d failures: first=%s: %s",
			c.SubjectID, c.SubjectKind, len(c.Failed), c.Failed[0].ScenarioID, c.Failed[0].Reason)
	}

	corpus := BaselineScenarioCorpus()
	corpusMap := make(map[ScenarioID]ScenarioDescriptor, len(corpus))
	for _, sc := range corpus {
		corpusMap[sc.ID] = sc
	}

	subject := SubjectDescriptor{
		ID:           c.SubjectID,
		Kind:         c.SubjectKind,
		Profile:      c.Profile,
		Capabilities: c.Capabilities,
		Dialects:     c.Dialects,
		Transports:   c.Transports,
	}
	expectedPos, expectedNeg := SelectScenariosForSubject(subject, corpus)

	if len(c.Passed) == 0 && len(expectedPos) > 0 {
		return fmt.Errorf("certification for %s (%s) has zero passed scenarios but expected %d positive scenarios",
			c.SubjectID, c.SubjectKind, len(expectedPos))
	}

	executed := make(map[ScenarioID]bool, len(c.ExecutedScenarioIDs))
	for _, id := range c.ExecutedScenarioIDs {
		if id == "" || executed[id] {
			return fmt.Errorf("certification contains invalid or duplicate executed scenario %q", id)
		}
		if _, ok := corpusMap[id]; !ok {
			return fmt.Errorf("executed scenario %q is not in baseline corpus", id)
		}
		executed[id] = true
	}
	seenPassed := make(map[ScenarioID]bool, len(c.Passed))
	for _, id := range c.Passed {
		if id == "" {
			return fmt.Errorf("certification contains empty scenario ID in passed list")
		}
		if _, ok := corpusMap[id]; !ok {
			return fmt.Errorf("certification contains scenario ID %q not in baseline corpus", id)
		}
		if seenPassed[id] {
			return fmt.Errorf("certification contains duplicate scenario ID %q in passed list", id)
		}
		seenPassed[id] = true
		if !executed[id] {
			return fmt.Errorf("scenario %q passed without execution evidence", id)
		}
		if !slices.Contains(expectedPos, id) {
			return fmt.Errorf("scenario %q passed but is not in expected positive selection for subject %s", id, c.SubjectID)
		}
	}

	seenNegative := make(map[ScenarioID]bool, len(c.Negative))
	for _, id := range c.Negative {
		if id == "" {
			return fmt.Errorf("certification contains empty scenario ID in negative list")
		}
		if _, ok := corpusMap[id]; !ok {
			return fmt.Errorf("certification contains scenario ID %q not in baseline corpus", id)
		}
		if seenNegative[id] {
			return fmt.Errorf("certification contains duplicate scenario ID %q in negative list", id)
		}
		if seenPassed[id] {
			return fmt.Errorf("scenario ID %q appears in both passed and negative lists", id)
		}
		seenNegative[id] = true
		if !executed[id] {
			return fmt.Errorf("scenario %q recorded negative without execution evidence", id)
		}
		if !slices.Contains(expectedNeg, id) {
			return fmt.Errorf("scenario %q recorded as negative but is not in expected negative selection for subject %s", id, c.SubjectID)
		}
	}

	// Verify exact coverage of expected positive and negative scenarios
	for _, exp := range expectedPos {
		if !seenPassed[exp] {
			return fmt.Errorf("expected positive scenario %q missing from certification passed list for subject %s", exp, c.SubjectID)
		}
	}
	for _, exp := range expectedNeg {
		if !seenNegative[exp] {
			return fmt.Errorf("expected negative scenario %q missing from certification negative list for subject %s", exp, c.SubjectID)
		}
	}

	return nil
}

// MarshalJSON returns serialized JSON certification evidence.
func (c Certification) MarshalJSON() ([]byte, error) {
	type Alias Certification
	return json.Marshal(Alias(c))
}

// BaselineScenarioCorpus returns the shared SDK scenario metadata.
func BaselineScenarioCorpus() []ScenarioDescriptor {
	return sdkcontract.BaselineScenarioCorpus()
}

// SelectScenariosForSubject filters the scenario corpus into positive and negative scenario lists
// based on subject capabilities, item dialects, reasoning dialects, compaction dialects, and extension requirements.
func SelectScenariosForSubject(subject SubjectDescriptor, corpus []ScenarioDescriptor) (positive []ScenarioID, negative []ScenarioID) {
	capSet := make(map[aipapi.Capability]bool, len(subject.Capabilities))
	for _, c := range subject.Capabilities {
		capSet[c] = true
	}

	hasItemDialect := func(req aipapi.DialectRequirement) bool {
		for _, d := range subject.Dialects.ItemDialects {
			if d.Kind == req.Kind && d.Dialect == req.Dialect {
				return true
			}
		}
		return false
	}

	hasReasoningDialect := func(req aipapi.DialectRequirement) bool {
		for _, d := range subject.Dialects.ReasoningDialects {
			if d.Kind == req.Kind && d.Dialect == req.Dialect {
				return true
			}
		}
		return false
	}

	hasCompactionDialect := func(req aipapi.DialectRequirement) bool {
		for _, d := range subject.Dialects.CompactionDialects {
			if d.Kind == req.Kind && d.Dialect == req.Dialect {
				return true
			}
		}
		return false
	}

	hasExtensionType := func(req aipapi.ExtensionRequirement) bool {
		for _, ext := range subject.Dialects.ExtensionTypes {
			if ext.Namespace == req.Namespace && ext.Type == req.Type {
				return true
			}
		}
		return false
	}

	transportAllowed := func(transport ScenarioTransport) bool {
		if len(subject.Transports) == 0 || transport == "" {
			return true
		}
		return slices.Contains(subject.Transports, transport)
	}

	kindAllowsTransport := func(kind SubjectKind, transport ScenarioTransport) bool {
		switch kind {
		case KindFrontend:
			return transport == TransportHTTP || transport == TransportStreaming || transport == TransportWebSocket
		case KindBackendFamily, KindProviderProfile, KindCanonicalCore:
			return transport == TransportHTTP || transport == TransportStreaming || transport == TransportConnector
		case KindConnector:
			return transport == TransportConnector || transport == TransportHTTP || transport == TransportStreaming
		default:
			return false
		}
	}

	for _, sc := range corpus {
		match := kindAllowsTransport(subject.Kind, sc.Transport) && transportAllowed(sc.Transport)
		// A scenario can be positive only when its subject kind and transport
		// are both applicable; capability declarations alone are insufficient.

		// Check Capabilities
		for _, reqCap := range sc.Requires.Capabilities {
			if !capSet[reqCap] {
				match = false
				break
			}
		}

		// Check Item Dialects
		if match {
			for _, reqDialect := range sc.Requires.ItemDialects {
				if !hasItemDialect(reqDialect) {
					match = false
					break
				}
			}
		}

		// Check Reasoning Dialects
		if match {
			for _, reqReasoning := range sc.Requires.ReasoningDialects {
				if !hasReasoningDialect(reqReasoning) {
					match = false
					break
				}
			}
		}

		// Check Compaction Dialects
		if match {
			for _, reqCompaction := range sc.Requires.CompactionDialects {
				if !hasCompactionDialect(reqCompaction) {
					match = false
					break
				}
			}
		}

		// Check Extension Types
		if match {
			for _, reqExt := range sc.Requires.ExtensionTypes {
				if !hasExtensionType(reqExt) {
					match = false
					break
				}
			}
		}

		if match {
			positive = append(positive, sc.ID)
		} else {
			negative = append(negative, sc.ID)
		}
	}
	return positive, negative
}
