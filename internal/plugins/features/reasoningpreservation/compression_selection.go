package reasoningpreservation

import (
	"context"
	"crypto/sha256"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// ReasoningViewResult is the immutable stage result for 6.1.
// It is content-free and carries the surrogate eligibility before any substitution (6.2).
type ReasoningViewResult struct {
	Kind   ViewKind
	Reason string
}

// selectReasoningViews is the private helper that implements the selection logic.
// It iterates restorable candidates, fetches the attached surrogate, evaluates
// current EgressPolicy exactly once per invocation (bounded, no model content),
// and runs the pure SelectReasoningViewWithCurrentPolicy. No mutation, no goroutines.
// Policy decision is hoisted: eligible candidates are filtered first, then a single
// immutable Decide result is applied to all.
func selectReasoningViews(ctx context.Context, cfg CompressionConfig, cs CompressionStore, svc CompressionServices, partition SessionPartition, candidates []restoreCandidate, support aipapi.ReasoningReplaySupport, meta request.AttemptMeta) map[string]ReasoningViewResult {
	if cs == nil || len(candidates) == 0 {
		return nil
	}
	type eligible struct {
		artifactID     string
		artifact       TurnArtifact
		surrogate      *ReasoningSurrogate
		classification Classification
	}
	var eligibles []eligible
	for _, c := range candidates {
		if c.Unsupported {
			continue
		}
		st, ok, err := cs.GetCompressionState(ctx, partition, c.ArtifactID)
		if err != nil || !ok || st.Surrogate == nil {
			continue
		}
		eligibles = append(eligibles, eligible{
			artifactID:     c.ArtifactID,
			artifact:       c.Artifact,
			surrogate:      st.Surrogate,
			classification: c.Classification,
		})
	}
	if len(eligibles) == 0 {
		return nil
	}
	// Evaluate policy once for the whole attempt with trusted control-plane input.
	curHash, curSan, curAction, curVersion, curErr := currentPolicyForSelection(ctx, cfg, svc, meta)
	out := make(map[string]ReasoningViewResult, len(eligibles))
	for _, e := range eligibles {
		if curErr != nil || curVersion == "" || curAction == EgressDeny {
			out[e.artifactID] = ReasoningViewResult{Kind: ViewOriginal, Reason: "policy_no_longer_permits"}
			continue
		}
		if curAction != EgressAllow && curAction != EgressRedactThenAllow {
			out[e.artifactID] = ReasoningViewResult{Kind: ViewOriginal, Reason: "policy_no_longer_permits"}
			continue
		}
		kind, reason := SelectReasoningViewWithCurrentPolicy(cfg, e.artifact, e.surrogate, support, e.classification, &curHash, &curSan)
		out[e.artifactID] = ReasoningViewResult{Kind: kind, Reason: reason}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// currentPolicyForSelection evaluates the current EgressPolicy exactly once per
// selection with trusted control-plane input. Scope is taken from meta.Scope
// if present, otherwise from the trusted context via scope.ScopeFromContext.
// No model content is inspected; call is bounded synchronous.
func currentPolicyForSelection(ctx context.Context, cfg CompressionConfig, svc CompressionServices, meta request.AttemptMeta) (hash [32]byte, sanitization string, action EgressAction, version string, err error) {
	if svc.EgressPolicy == nil {
		return [32]byte{}, "", EgressDeny, "", nil
	}
	// Resolve principal scope without model content.
	var principal scope.PrincipalScopeView
	if meta.Scope.PrincipalID.String() != "" || meta.Scope.SubjectKind != "" {
		principal = meta.Scope
	} else if v, ok := scope.ScopeFromContext(ctx); ok {
		principal = v
	}
	// Egress input is narrow, trusted control-plane only.
	// Full scope is propagated so policies can enforce tenant/org/workspace/project/cost-center/policy-label restrictions.
	in := CompressionEgressInput{
		Route:       cfg.Route,
		Purpose:     EgressPurposeReasoningSemanticCompression,
		SourceClass: EgressSourceClassSemanticText,
		Principal:   NewEgressPrincipalScopeView(principal),
	}
	dec, decErr := svc.EgressPolicy.Decide(ctx, in)
	if decErr != nil {
		return [32]byte{}, "", EgressDeny, "", decErr
	}
	if dec.PolicyVersion == "" {
		return [32]byte{}, "", EgressDeny, "", nil
	}
	if dec.Action == EgressDeny {
		return [32]byte{}, "", EgressDeny, dec.PolicyVersion, nil
	}
	san := SanitizationNone
	if dec.Action == EgressRedactThenAllow {
		san = SanitizationRedacted
	}
	h := ComputeEgressPolicyHash(dec, cfg.Route)
	return h, san, dec.Action, dec.PolicyVersion, nil
}

// ViewKind is the pure selection decision before any mutation.
// 6.1 may return Original/Surrogate + reason but must NOT substitute yet (6.2).
type ViewKind string

const (
	ViewOriginal  ViewKind = "original"
	ViewSurrogate ViewKind = "surrogate"
)

// SelectReasoningView is a pure eligibility function over the original artifact
// + surrogate + candidate ReplaySupport + client classification.
// It is a wrapper without current-policy check for backward compatibility.
func SelectReasoningView(cfg CompressionConfig, artifact TurnArtifact, surrogate *ReasoningSurrogate, support aipapi.ReasoningReplaySupport, classification Classification) (ViewKind, string) {
	return SelectReasoningViewWithCurrentPolicy(cfg, artifact, surrogate, support, classification, nil, nil)
}

// SelectReasoningViewWithCurrentPolicy is the policy-aware pure variant.
// If currentPolicyHash/currentSanitization are non-nil, they are the trusted
// current EgressPolicy result prepared by the stage (hash via ComputeEgressPolicyHash,
// sanitization via EgressAllow/Redact). When provided, they must match the
// stored surrogate's EgressPolicyHash and Sanitization, otherwise
// policy_no_longer_permits. No I/O inside pure; stage does the Decide call.
func SelectReasoningViewWithCurrentPolicy(cfg CompressionConfig, artifact TurnArtifact, surrogate *ReasoningSurrogate, support aipapi.ReasoningReplaySupport, classification Classification, currentPolicyHash *[32]byte, currentSanitization *string) (ViewKind, string) {
	if surrogate == nil {
		return ViewOriginal, "no_surrogate"
	}
	// Existing client reasoning precedence preserved.
	if classification == ClassPreserved {
		return ViewOriginal, "client_preserved"
	}
	if classification != ClassMissing {
		return ViewOriginal, "client_not_missing"
	}
	// Re-run canonical classifier on each original placement.
	semanticIndexes := make([]int, 0, len(artifact.Reasoning))
	exactOrUnknown := make(map[int]struct{}, len(artifact.Reasoning))
	semanticDialects := make([]aipapi.ReasoningDialect, 0, len(artifact.Reasoning))
	for i, pr := range artifact.Reasoning {
		sem := ClassifyReasoningPart(pr.Part)
		switch sem {
		case ReplaySemanticText:
			semanticIndexes = append(semanticIndexes, i)
			if pr.Part.Reasoning != nil {
				d := aipapi.NormalizeReasoningDialect(pr.Part.Reasoning.Dialect)
				semanticDialects = append(semanticDialects, d)
			}
		case ReplayExactRequired, ReplayUnknown:
			exactOrUnknown[i] = struct{}{}
		default:
			exactOrUnknown[i] = struct{}{}
		}
	}
	if len(semanticIndexes) == 0 {
		return ViewOriginal, "no_semantic_text"
	}
	// Destination must support original dialect via existing ReasoningReplaySupport/dialectSet.
	supported := dialectSet(support.Dialects)
	for _, d := range semanticDialects {
		if _, ok := supported[d]; !ok {
			return ViewOriginal, "unsupported_destination"
		}
	}
	// Surrogate correlation: exact original digest/semantic/policy/sanitization/route and expected indexes.
	if surrogate.OriginalDigest != artifact.Anchor {
		return ViewOriginal, "stale_original_digest"
	}
	if surrogate.SemanticDigest != computeSemanticDigest(artifact.Reasoning) {
		return ViewOriginal, "stale_semantic_digest"
	}
	if surrogate.PolicyRevision != cfg.EgressPolicyRef {
		return ViewOriginal, "stale_policy"
	}
	if !isValidSanitization(surrogate.Sanitization) {
		return ViewOriginal, "stale_sanitization"
	}
	var zeroHash [32]byte
	if surrogate.AuthorizedRouteHash == zeroHash {
		return ViewOriginal, "stale_route"
	}
	routeHash := sha256.Sum256([]byte(cfg.Route))
	if surrogate.AuthorizedRouteHash != routeHash {
		return ViewOriginal, "stale_route"
	}
	if surrogate.EgressPolicyHash == zeroHash {
		return ViewOriginal, "stale_egress_hash"
	}
	// EgressPolicyHash is the authoritative hash of decision PolicyVersion+route+purpose+source,
	// not sha256(EgressPolicyRef). Its correctness is enforced at attach time via
	// pending/surrogate correlation; selection only requires it be non-zero and that
	// PolicyRevision matches the current config ref (checked above).
	if currentPolicyHash != nil {
		if *currentPolicyHash != surrogate.EgressPolicyHash {
			return ViewOriginal, "policy_no_longer_permits"
		}
	}
	if currentSanitization != nil {
		if *currentSanitization != surrogate.Sanitization {
			return ViewOriginal, "policy_no_longer_permits"
		}
	}
	// Expected indexes: surrogate must exactly equal the current semantic subset.
	if len(surrogate.Segments) != len(semanticIndexes) {
		return ViewOriginal, "stale_indexes"
	}
	segSet := make(map[int]struct{}, len(surrogate.Segments))
	for _, seg := range surrogate.Segments {
		if _, isExact := exactOrUnknown[seg.PlacementIndex]; isExact {
			return ViewOriginal, "stale_indexes_exact"
		}
		if _, dup := segSet[seg.PlacementIndex]; dup {
			return ViewOriginal, "stale_indexes"
		}
		segSet[seg.PlacementIndex] = struct{}{}
	}
	semSet := make(map[int]struct{}, len(semanticIndexes))
	for _, idx := range semanticIndexes {
		semSet[idx] = struct{}{}
	}
	for k := range segSet {
		if _, ok := semSet[k]; !ok {
			return ViewOriginal, "stale_indexes"
		}
	}
	for k := range semSet {
		if _, ok := segSet[k]; !ok {
			return ViewOriginal, "stale_indexes"
		}
	}
	return ViewSurrogate, "eligible"
}
