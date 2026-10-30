package reasoningpreservation

import (
	"encoding/json"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// cloneReasoningPartForView clones all ReasoningPart fields preserving byte equality.
func cloneReasoningPartForView(p *aipapi.ReasoningPart) *aipapi.ReasoningPart {
	if p == nil {
		return nil
	}
	out := *p
	if p.Opaque != nil {
		out.Opaque = append(json.RawMessage(nil), p.Opaque...)
	} else {
		out.Opaque = nil
	}
	if p.Summary != nil {
		out.Summary = append(json.RawMessage(nil), p.Summary...)
	} else {
		out.Summary = nil
	}
	if p.Content != nil {
		out.Content = append(json.RawMessage(nil), p.Content...)
	} else {
		out.Content = nil
	}
	if p.EncryptedContent != nil {
		out.EncryptedContent = append(json.RawMessage(nil), p.EncryptedContent...)
	} else {
		out.EncryptedContent = nil
	}
	return &out
}

func clonePartForView(p aipapi.Part) aipapi.Part {
	out := p
	if len(p.Content) > 0 {
		out.Content = append(json.RawMessage(nil), p.Content...)
	} else {
		out.Content = nil
	}
	if p.Reasoning != nil {
		out.Reasoning = cloneReasoningPartForView(p.Reasoning)
	}
	return out
}

func cloneArtifactForView(a TurnArtifact) TurnArtifact {
	out := a
	if len(a.Reasoning) > 0 {
		out.Reasoning = make([]PlacedReasoning, len(a.Reasoning))
		for i := range a.Reasoning {
			out.Reasoning[i] = PlacedReasoning{
				BeforeNonReasoningPart: a.Reasoning[i].BeforeNonReasoningPart,
				Part:                   clonePartForView(a.Reasoning[i].Part),
			}
		}
	} else {
		out.Reasoning = nil
	}
	return out
}

// BuildEphemeralArtifact returns a defensive copy of original with only matching semantic
// Reasoning.Text fields replaced from surrogate. BeforeNonReasoningPart, dialect,
// signature/opaque/summary/content/encrypted fields remain byte-equivalent.
// If surrogate is nil, or any correlation is ambiguous/missing/duplicate/index out of range
// or semantic subset mismatch, the whole original defensive copy is returned (fallback).
func BuildEphemeralArtifact(original TurnArtifact, surrogate *ReasoningSurrogate) TurnArtifact {
	// Always defensive copy first.
	cloned := cloneArtifactForView(original)
	if surrogate == nil {
		return cloned
	}
	// Build placementIndex -> text map, detect duplicate/out-of-range.
	segMap := make(map[int]string, len(surrogate.Segments))
	for _, seg := range surrogate.Segments {
		if seg.PlacementIndex < 0 || seg.PlacementIndex >= len(original.Reasoning) {
			return cloned
		}
		if _, dup := segMap[seg.PlacementIndex]; dup {
			return cloned
		}
		segMap[seg.PlacementIndex] = seg.Text
	}
	// Validate semantic subset exactly matches surrogate indexes.
	semanticCount := 0
	for i, pr := range original.Reasoning {
		sem := ClassifyReasoningPart(pr.Part)
		if sem == ReplaySemanticText {
			semanticCount++
			if _, ok := segMap[i]; !ok {
				// missing surrogate for semantic placement
				return cloned
			}
		} else {
			// exact/unknown must not have surrogate
			if _, ok := segMap[i]; ok {
				return cloned
			}
		}
	}
	if len(surrogate.Segments) != semanticCount {
		return cloned
	}
	// All validations passed: replace only Text for semantic placements.
	for i := range cloned.Reasoning {
		if ClassifyReasoningPart(original.Reasoning[i].Part) == ReplaySemanticText {
			if txt, ok := segMap[i]; ok {
				// Preserve all other fields, only Text changes.
				cloned.Reasoning[i].Part.Reasoning.Text = txt
			}
		}
	}
	return cloned
}

// BuildEphemeralArtifacts builds defensive ephemeral copies for all artifacts.
// For each ViewSurrogate decision, the corresponding surrogate is fetched via getSurrogate;
// if fetch fails or fallback triggers, the original defensive copy is used.
// The store is never mutated; returned slice is ephemeral.
func BuildEphemeralArtifacts(arts []TurnArtifact, decisions map[string]ReasoningViewResult, getSurrogate func(string) (*ReasoningSurrogate, bool)) []TurnArtifact {
	if len(arts) == 0 {
		return nil
	}
	out := make([]TurnArtifact, len(arts))
	for i, art := range arts {
		if decisions != nil {
			if dec, ok := decisions[art.ID]; ok && dec.Kind == ViewSurrogate {
				if getSurrogate != nil {
					if sur, ok2 := getSurrogate(art.ID); ok2 && sur != nil {
						out[i] = BuildEphemeralArtifact(art, sur)
						continue
					}
				}
			}
		}
		out[i] = cloneArtifactForView(art)
	}
	return out
}

// BuildEphemeralCandidates builds defensive ephemeral candidates for restore.
// For each candidate where decisions is ViewSurrogate, the surrogate is fetched and
// the artifact's semantic Text is replaced defensively; otherwise original is cloned.
// Fallback (duplicate/missing/index mismatch) returns original whole artifact.
func BuildEphemeralCandidates(cands []restoreCandidate, decisions map[string]ReasoningViewResult, getSurrogate func(string) (*ReasoningSurrogate, bool)) []restoreCandidate {
	if len(cands) == 0 {
		return nil
	}
	out := make([]restoreCandidate, len(cands))
	for i, c := range cands {
		nc := c
		// Only candidates with ViewSurrogate are eligible per 6.1 (ClassMissing already filtered)
		if decisions != nil {
			if dec, ok := decisions[c.ArtifactID]; ok && dec.Kind == ViewSurrogate {
				if getSurrogate != nil {
					if sur, ok2 := getSurrogate(c.ArtifactID); ok2 && sur != nil {
						nc.Artifact = BuildEphemeralArtifact(c.Artifact, sur)
						out[i] = nc
						continue
					}
				}
			}
		}
		nc.Artifact = cloneArtifactForView(c.Artifact)
		out[i] = nc
	}
	return out
}
