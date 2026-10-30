package reasoningpreservation

import (
	"context"
	"errors"
	"reflect"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

// resolverSanitizer adapts a request-scoped MatcherResolver to TrustedTextSanitizer.
// It resolves the matcher per invocation so multi-user (ContextMatcherResolver)
// and single-user (static resolver) are handled without capturing secrets.
type resolverSanitizer struct {
	resolver sdk.MatcherResolver
}

// NewResolverSanitizer returns a TrustedTextSanitizer that resolves the matcher
// from resolver on each SanitizeText call. Nil resolver returns nil so
// CompressionServices validation can fail closed.
func NewResolverSanitizer(r sdk.MatcherResolver) TrustedTextSanitizer {
	if matcherResolverIsNil(r) {
		return nil
	}
	return resolverSanitizer{resolver: r}
}

func (r resolverSanitizer) SanitizeText(ctx context.Context, text string) (string, error) {
	if matcherResolverIsNil(r.resolver) {
		return "", errors.New("reasoning-output-preservation: trusted secret matcher resolver is required")
	}
	m, err := r.resolver.Resolve(ctx)
	if err != nil {
		return "", err
	}
	if m == nil {
		return "", errors.New("reasoning-output-preservation: trusted secret matcher unavailable for redaction")
	}
	out, _, err := m.RedactString(ctx, text)
	return out, err
}

var _ TrustedTextSanitizer = resolverSanitizer{}

func matcherResolverIsNil(r sdk.MatcherResolver) bool {
	if r == nil {
		return true
	}
	rv := reflect.ValueOf(r)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}
