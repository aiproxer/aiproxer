package modelcatalog

// CapabilityTriState is an explicit tri-state for protocol-neutral capabilities (unknown vs explicit false).
type CapabilityTriState uint8

const (
	// CapabilityUnknown means the catalog or override did not establish support or denial.
	CapabilityUnknown CapabilityTriState = iota
	// CapabilityUnsupported means the model is explicitly known not to support the capability.
	CapabilityUnsupported
	// CapabilitySupported means the model is explicitly known to support the capability.
	CapabilitySupported
)

// LimitTriState distinguishes unknown limits, explicit lack of a limit concept, and a known numeric limit.
type LimitTriState uint8

const (
	// LimitUnknown means no reliable limit was derived from catalog or overrides.
	LimitUnknown LimitTriState = iota
	// LimitUnsupported means the source explicitly indicates no context limit applies for this model path.
	LimitUnsupported
	// LimitPresent means Tokens holds a positive context token budget (or provider-normalized unit).
	LimitPresent
)

// LimitFact carries limit state separate from "unknown" (requirement 3.6).
type LimitFact struct {
	State  LimitTriState
	Tokens int64
}

// FactSource identifies where effective model facts originated (requirement 9.7).
type FactSource uint8

const (
	// FactSourceNone means no catalog or override facts apply for this evaluation branch.
	FactSourceNone FactSource = iota
	// FactSourcePairOverride is a backend+model administrator override.
	FactSourcePairOverride
	// FactSourceModelOverride is a model-name-only administrator override.
	FactSourceModelOverride
	// FactSourceCatalog is a matching models.dev catalog entry after normalization.
	FactSourceCatalog
	// FactSourceBackendDeclaration is the backend adapter capability surface only.
	FactSourceBackendDeclaration
)

// MatchKind classifies catalog model id matching for diagnostics (requirements 4.x).
type MatchKind uint8

const (
	// MatchNone means no catalog match classification applies (backend-only path).
	MatchNone MatchKind = iota
	// MatchNoMatch means no catalog entry matched the route model.
	MatchNoMatch
	// MatchExact is a full string identity match against catalog model ids.
	MatchExact
	// MatchNonExact is a deterministic normalized match with a single catalog candidate.
	MatchNonExact
	// MatchAmbiguous means normalized matching produced multiple catalog ids.
	MatchAmbiguous
)

// EligibilityReason is a compact routing-time outcome for context limits (executor integration later).
type EligibilityReason uint8

const (
	EligibilityUnknown EligibilityReason = iota
	EligibilityEligible
	EligibilityContextLimitExceeded
)

// String returns a stable label for operator diagnostics (not localized).
func (f FactSource) String() string {
	switch f {
	case FactSourceNone:
		return "none"
	case FactSourcePairOverride:
		return "pair_override"
	case FactSourceModelOverride:
		return "model_override"
	case FactSourceCatalog:
		return "catalog"
	case FactSourceBackendDeclaration:
		return "backend"
	default:
		return "unknown"
	}
}

// String returns a stable match classification label.
func (k MatchKind) String() string {
	switch k {
	case MatchNone:
		return "none"
	case MatchNoMatch:
		return "no_match"
	case MatchExact:
		return "exact"
	case MatchNonExact:
		return "non_exact"
	case MatchAmbiguous:
		return "ambiguous"
	default:
		return "unknown"
	}
}

// String returns a stable eligibility reason label.
func (r EligibilityReason) String() string {
	switch r {
	case EligibilityUnknown:
		return "unknown"
	case EligibilityEligible:
		return "eligible"
	case EligibilityContextLimitExceeded:
		return "context_limit_exceeded"
	default:
		return "unknown"
	}
}

// ModelFacts is protocol-neutral state used for compatibility and diagnostics (design §ModelFacts).
type ModelFacts struct {
	Tools             CapabilityTriState
	StructuredOutputs CapabilityTriState
	Reasoning         CapabilityTriState
	Vision            CapabilityTriState
	Documents         CapabilityTriState
	ContextLimit      LimitFact
	InputLimit        LimitFact
	OutputLimit       LimitFact
	Source            FactSource
	MatchKind         MatchKind
}
