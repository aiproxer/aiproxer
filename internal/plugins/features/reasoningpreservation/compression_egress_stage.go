package reasoningpreservation

import (
	"context"
	"crypto/sha256"
)

// PreparedReservation is passed to the next stage after successful egress
// decision, sanitization, input budgeting, and authoritative CAS promotion.
// It is content-bearing only for the next stage's local use; it is not
// persisted. ReservationResult is content-free. Segments contain only local
// placement index + sanitized text; no session/account/lineage/anchor/digest
// is ever placed here.
type PreparedReservation struct {
	Reservation      ReservationResult
	Segments         []CompressorInputSegment
	Decision         CompressionEgressDecision
	EgressPolicyHash [32]byte
	Route            string
}

// PostEgressStage is the next composable stage after egress+redaction.
// It receives the sanitized segments and decision metadata. No request
// build/provider is performed in this stage (task 4.3 boundary).
type PostEgressStage func(context.Context, PreparedReservation) error

// ComputeEgressPolicyHash derives a stable versioned authoritative hash
// from the trusted egress decision PolicyVersion + explicit route +
// purpose + source class. It is deterministic and provider-neutral.
// The separator 0x00 avoids collisions across field boundaries.
// Version prefix "v1" ensures stable versioning if scheme changes.
func ComputeEgressPolicyHash(decision CompressionEgressDecision, route string) [32]byte {
	h := sha256.New()
	_, _ = h.Write([]byte("v1"))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(decision.PolicyVersion))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(route))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(EgressPurposeReasoningSemanticCompression))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(EgressSourceClassSemanticText))
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// NewPostReservationEgressStage returns a PostReservationStage that
// evaluates the trusted egress policy with explicit route/purpose/source/
// principal from the trusted correlation, redacts locally before byte/token
// accounting, prepares sanitized segments from the authoritative artifact
// (via store Snapshot lookup, never from correlation/model metadata),
// enforces MaxInputBytes/Tokens after redaction, derives the authoritative
// EgressPolicyHash, performs full CAS provisional->authoritative promotion
// via UpdateReservationPolicyHash(returns nil, reasoningpreservation.SanitizationNone, sha256.Sum256([]byte("test-route"))) so original remains untouched.
// Trusted sanitizer is taken from svc.Sanitizer, not from untrusted
// policy decision; policy may request redact but cannot inject arbitrary
// sanitizer authority.
func NewPostReservationEgressStage(cfg Config, store CompressionStore, svc CompressionServices, next PostEgressStage) PostReservationStage {
	return NewPostReservationEgressStageWithTelemetry(cfg, store, svc, next, nil)
}

// NewPostReservationEgressStageWithTelemetry is the telemetry-aware egress stage.
// It records content-free privacy outcomes (allow/redact/deny/missing-policy) without content.
func NewPostReservationEgressStageWithTelemetry(cfg Config, store CompressionStore, svc CompressionServices, next PostEgressStage, tel *Telemetry) PostReservationStage {
	return func(ctx context.Context, res ReservationResult) error {
		if !res.IsReserved() {
			return nil
		}
		if store == nil {
			return nil
		}
		if res.Claim.ReservationID == "" || res.Correlation.ArtifactID == "" {
			clearCompressionWithCleanup(ctx, store, res.Correlation.Partition, res.Correlation.ArtifactID, res.Claim.ReservationID)
			return nil
		}
		route := cfg.Compression.Route
		// Evaluate policy with explicit narrow inputs from trusted correlation.
		// Do not trust sanitizer embedded in policy decision; use svc.Sanitizer.
		// Full scope is passed so external policies can enforce tenant/org/workspace/project/cost-center/policy-label restrictions.
		principalView := NewEgressPrincipalScopeView(res.Correlation.Scope)
		in := CompressionEgressInput{
			Route:       route,
			Purpose:     EgressPurposeReasoningSemanticCompression,
			SourceClass: EgressSourceClassSemanticText,
			Principal:   principalView,
		}
		var dec CompressionEgressDecision
		if svc.EgressPolicy == nil {
			dec = CompressionEgressDecision{Action: EgressDeny, PolicyVersion: "missing-policy"}
		} else {
			d, err := svc.EgressPolicy.Decide(ctx, in)
			if err != nil || d.PolicyVersion == "" {
				dec = CompressionEgressDecision{Action: EgressDeny, PolicyVersion: "missing-policy"}
			} else {
				dec = d
				// Ignore untrusted decision sanitizer; use trusted service sanitizer for redact.
				if dec.Action == EgressRedactThenAllow {
					if svc.Sanitizer == nil {
						dec = CompressionEgressDecision{Action: EgressDeny, PolicyVersion: dec.PolicyVersion}
					} else {
						dec.Sanitizer = svc.Sanitizer
					}
				}
			}
		}
		if tel != nil && cfg.Compression.Enabled {
			// Content-free privacy telemetry before provider work.
			switch dec.Action {
			case EgressDeny:
				if dec.PolicyVersion == "missing-policy" {
					tel.RecordShadowMeasurement(OutcomeEgressMissingPolicy, res.Correlation.SourceBytes, 0, 0, 0, 0)
				} else {
					tel.RecordShadowMeasurement(OutcomeEgressDeny, res.Correlation.SourceBytes, 0, 0, 0, 0)
				}
			case EgressRedactThenAllow:
				tel.RecordShadowMeasurement(OutcomeEgressRedact, res.Correlation.SourceBytes, 0, 0, 0, 0)
			case EgressAllow:
				tel.RecordShadowMeasurement(OutcomeEgressAllow, res.Correlation.SourceBytes, 0, 0, 0, 0)
			}
		}
		if dec.Action == EgressDeny {
			clearCompressionWithCleanup(ctx, store, res.Correlation.Partition, res.Correlation.ArtifactID, res.Claim.ReservationID)
			return nil
		}
		// Retrieve authoritative artifact safely via Snapshot lookup by partition/artifact ID.
		// Never use text from correlation or model metadata.
		snap, err := store.Snapshot(ctx, res.Correlation.Partition)
		if err != nil {
			clearCompressionWithCleanup(ctx, store, res.Correlation.Partition, res.Correlation.ArtifactID, res.Claim.ReservationID)
			return nil
		}
		var artifact *TurnArtifact
		for i := range snap {
			if snap[i].ID == res.Correlation.ArtifactID {
				artifact = &snap[i]
				break
			}
		}
		if artifact == nil {
			clearCompressionWithCleanup(ctx, store, res.Correlation.Partition, res.Correlation.ArtifactID, res.Claim.ReservationID)
			return nil
		}
		// Prepare sanitized segments from authoritative artifact. Redaction occurs
		// locally before byte/token accounting inside PrepareSemanticSegments.
		// Enforce MaxInputBytes/Tokens after redaction.
		segments, outcome, err := PrepareSemanticSegments(ctx, artifact.Reasoning, dec, cfg.Compression.MaxInputBytes, cfg.Compression.MaxInputTokens)
		if err != nil || outcome != OutcomePrepared {
			clearCompressionWithCleanup(ctx, store, res.Correlation.Partition, res.Correlation.ArtifactID, res.Claim.ReservationID)
			return nil
		}
		// segments is sanitized, bounded, and contains only local index+text.
		// Derive authoritative hash from decision metadata (stable versioned).
		authoritativeHash := ComputeEgressPolicyHash(dec, route)
		routeHash := sha256.Sum256([]byte(route))
		sanitization := SanitizationNone
		if dec.Action == EgressRedactThenAllow {
			sanitization = SanitizationRedacted
		}
		// Full CAS promotion provisional -> authoritative using threaded claim verbatim.
		err = store.UpdateReservationPolicyHash(ctx, res.Claim, res.Correlation.EgressPolicyRefHash, res.Correlation.SemanticDigest, authoritativeHash, sanitization, routeHash)
		if err != nil {
			clearCompressionWithCleanup(ctx, store, res.Correlation.Partition, res.Correlation.ArtifactID, res.Claim.ReservationID)
			return nil
		}
		if next != nil {
			pr := PreparedReservation{
				Reservation:      res,
				Segments:         segments,
				Decision:         dec,
				EgressPolicyHash: authoritativeHash,
				Route:            route,
			}
			_ = next(ctx, pr)
		}
		return nil
	}
}

// buildEgressStage is an internal helper for composition wiring that
// returns nil when compression disabled or services invalid, else the
// egress stage with the given next.
func buildEgressStage(cfg Config, store CompressionStore, svc CompressionServices, next PostEgressStage) PostReservationStage {
	if !cfg.Compression.Enabled {
		return nil
	}
	if store == nil {
		return nil
	}
	if err := svc.validateFor(cfg); err != nil {
		return nil
	}
	return NewPostReservationEgressStage(cfg, store, svc, next)
}
