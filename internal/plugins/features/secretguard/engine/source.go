package engine

import (
	"context"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

// Environment is the composition-injected process environment port.
// Multi-user construction must never call Lookup or Snapshot (D4).
type Environment interface {
	Lookup(name string) (value string, ok bool)
	// Snapshot returns a defensive copy of KEY=VALUE pairs (os.Environ shape).
	Snapshot() []string
}

// EnvReader is a deprecated alias for Environment.
type EnvReader = Environment

// SingleUserOptions configures single-user environment catalog loading.
type SingleUserOptions struct {
	IncludePopularEnv bool
	IncludeEnv        []string
	ExcludeEnv        []string
	MinSecretBytes    int
	// Matcher controls redaction presentation for the composed static matcher.
	// Zero value means composition defaults (preserve known prefixes, mask '*').
	Matcher MatcherOptions
	// MatcherConfigured is true when Matcher was set from decoded feature YAML.
	// When false, NewSingleUserSource applies composition defaults for Matcher.
	MatcherConfigured bool
}

// Source is an opaque composition-owned secret source. It must not expose raw catalog values.
type Source interface {
	AccessMode() AccessMode
	// MatcherResolver returns an opaque resolver; implementations own secret bytes privately.
	MatcherResolver() sdk.MatcherResolver
	// EntryCount is safe inventory metadata (names/values are never returned).
	EntryCount() int
	// SourceCategories returns bounded source-category labels present in the catalog.
	SourceCategories() []string
}

type composedSource struct {
	mode       AccessMode
	resolver   sdk.MatcherResolver
	entryCount int
	categories []string
}

func (s composedSource) AccessMode() AccessMode { return s.mode }
func (s composedSource) MatcherResolver() sdk.MatcherResolver {
	return s.resolver
}
func (s composedSource) EntryCount() int { return s.entryCount }
func (s composedSource) SourceCategories() []string {
	if len(s.categories) == 0 {
		return nil
	}
	out := make([]string, len(s.categories))
	copy(out, s.categories)
	return out
}

// NewMultiUserSource builds a request-credential-only source.
// It must not call env even when env is non-nil (security boundary, not validation).
func NewMultiUserSource(env Environment) (Source, error) {
	_ = env // intentionally unused: multi-user must not read process environment
	return composedSource{
		mode:       ModeMultiUser,
		resolver:   sdk.ContextMatcherResolver{},
		entryCount: 0,
		categories: []string{string(sdk.SourceCategoryRequestCred)},
	}, nil
}

// NewSingleUserSource loads the name-preserving environment catalog via Snapshot,
// proxy credential matching, optional popular exact/inferred names, and include/exclude lists.
func NewSingleUserSource(env Environment, opts SingleUserOptions) (Source, error) {
	inv := collectSingleUserInventory(env, opts)
	cat, err := BuildCatalog(catalogInputsFromInventory(inv), opts.MinSecretBytes)
	if err != nil {
		return nil, err
	}
	matcherOpts := opts.Matcher
	if !opts.MatcherConfigured {
		matcherOpts = MatcherOptions{PreserveKnownPrefixes: true}
	}
	return composedSource{
		mode:       ModeSingleUser,
		resolver:   NewStaticMatcherResolver(cat, matcherOpts),
		entryCount: cat.EntryCount(),
		categories: cat.SourceCategories(),
	}, nil
}

// NewDisabledSource returns an empty source used when the secrets-guard feature is disabled.
// It performs no environment access. AccessMode reports ModeSingleUser as a neutral default
// (feature-disabled posture); EntryCount is 0 and Resolve returns (nil, nil).
func NewDisabledSource() Source {
	return composedSource{
		mode:       ModeSingleUser,
		resolver:   disabledMatcherResolver{},
		entryCount: 0,
	}
}

type disabledMatcherResolver struct{}

func (disabledMatcherResolver) Resolve(ctx context.Context) (sdk.Matcher, error) {
	_ = ctx
	return nil, nil
}

var _ sdk.MatcherResolver = disabledMatcherResolver{}
