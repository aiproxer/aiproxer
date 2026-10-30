package secretguard

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

// GenerationServices is the immutable, generation-owned secret-guard service
// capability. The concrete BetterLeaks scanner remains private to this package;
// generic runtime only carries this value opaquely through the frozen plane.
type GenerationServices struct {
	redaction          engine.MatcherOptions
	source             engine.Source
	betterLeaks        *betterLeaksScanner
	localAutoDiscovery bool
	betterLeaksEnabled bool
	betterLeaksFacts   DetectorFacts
}

// DetectorPosture is the bounded operator-facing detector inventory for one
// immutable generation. It contains no catalog values, scanner handles, or
// upstream BetterLeaks objects.
type DetectorPosture struct {
	AccessMode                string
	LocalAutoDiscoveryEnabled bool
	BetterLeaksEnabled        bool
	BetterLeaksFacts          DetectorFacts
	DiscoveryDetectorCount    int
}

// BuildGenerationServices creates the generation-owned exact source and, when
// enabled, one precompiled BetterLeaks scanner. The scanner is never rebuilt
// for a request or fragment and the returned facts are safe to project.
func BuildGenerationServices(policy DetectorPolicy, source engine.Source, redaction ...engine.MatcherOptions) (*GenerationServices, error) {
	if source == nil {
		source = engine.NewDisabledSource()
	}
	services := &GenerationServices{
		redaction:          engine.MatcherOptions{MaskByte: '*', PreserveKnownPrefixes: true},
		source:             source,
		localAutoDiscovery: policy.LocalAutoDiscoveryEnabled,
		betterLeaksEnabled: policy.BetterLeaks.Enabled,
	}
	if len(redaction) > 0 {
		services.redaction = redaction[0]
		if services.redaction.MaskByte == 0 {
			services.redaction.MaskByte = '*'
		}
	}
	if !policy.BetterLeaks.Enabled {
		return services, nil
	}
	scanner, err := newBetterLeaksScanner(policy.BetterLeaks)
	if err != nil {
		return nil, err
	}
	services.betterLeaks = scanner
	services.betterLeaksFacts = scanner.policyFacts()
	return services, nil
}

// MatcherResolver returns the exact request-scoped matcher capability. The
// BetterLeaks scanner is deliberately not presented as an exact matcher; its
// request traversal/enforcement bridge is owned by a later feature task.
func (s *GenerationServices) MatcherResolver() sdk.MatcherResolver {
	if s == nil || s.source == nil {
		return nil
	}
	return s.source.MatcherResolver()
}

// DetectorFacts returns an immutable defensive copy of the BetterLeaks policy
// facts captured while composing this generation.
func (s *GenerationServices) DetectorFacts() DetectorFacts {
	if s == nil {
		return DetectorFacts{}
	}
	facts := s.betterLeaksFacts
	facts.RuleIDs = append([]string(nil), facts.RuleIDs...)
	return facts
}

// Posture projects only bounded detector facts for diagnostics and frozen plane
// construction. Raw scanner/config objects remain inside this package.
func (s *GenerationServices) Posture() DetectorPosture {
	if s == nil {
		return DetectorPosture{}
	}
	count := 0
	if s.localAutoDiscovery {
		count++
	}
	if s.betterLeaksEnabled {
		count++
	}
	return DetectorPosture{
		AccessMode:                string(s.source.AccessMode()),
		LocalAutoDiscoveryEnabled: s.localAutoDiscovery,
		BetterLeaksEnabled:        s.betterLeaksEnabled,
		BetterLeaksFacts:          s.DetectorFacts(),
		DiscoveryDetectorCount:    count,
	}
}

// scanFragments is intentionally package-private. Task 6 owns the bridge from
// discovery findings to existing enforcement and rewriting semantics.
func (s *GenerationServices) scanFragments(ctx context.Context, fragments []LogicalFragment) (betterLeaksScanResult, error) {
	if s == nil || !s.betterLeaksEnabled || s.betterLeaks == nil {
		return betterLeaksScanResult{}, nil
	}
	return s.betterLeaks.scanFragments(ctx, fragments)
}
