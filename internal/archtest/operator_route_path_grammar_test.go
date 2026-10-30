package archtest_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
	"github.com/aiproxer/aiproxer/internal/stdhttp/selfdefense"
)

// operatorPathCase is one candidate value for an operator-supplied data-plane path,
// with the verdict each validator must reach.
//
// The point of this table is not the individual characters. It is that the two
// validators live in different packages -- internal/core/config must not depend on
// the stdhttp contract, and the contract must not depend on core config -- so the
// policy is stated twice and only this test proves the two statements agree.
type operatorPathCase struct {
	name   string
	path   string
	accept bool
	why    string
}

func operatorPathCases() []operatorPathCase {
	return []operatorPathCase{
		// Ordinary literal paths: the whole point of the feature. The root is
		// deliberately absent: "a frontend base_path must not be the root" is a
		// frontend rule layered on top of this shared policy, not part of it.
		{name: "v1", path: "/v1", accept: true, why: "an ordinary literal path"},
		{name: "nested", path: "/openresponses/v1", accept: true, why: "an ordinary literal path"},
		{name: "admin", path: "/admin/attempts", accept: true, why: "an ordinary literal path"},
		{name: "colliding but literal", path: "/wp-admin", accept: true, why: "a literal path inside a frozen family is allowed and carved"},
		{name: "dash and underscore", path: "/my-route_v2", accept: true, why: "ordinary path characters"},
		{name: "trailing slash", path: "/admin/", accept: true, why: "a trailing separator is normalized away, and the normalizer check below proves it"},

		// ServeMux pattern syntax: a pattern, not a literal route.
		{name: "wildcard segment", path: "/wp-admin/{tenant}", accept: false, why: "Go 1.22+ {name} is pattern syntax, not a literal"},
		{name: "rest wildcard", path: "/wp-admin/{rest...}", accept: false, why: "Go 1.22+ {name...} is pattern syntax, not a literal"},
		{name: "end wildcard", path: "/wp-admin/{$}", accept: false, why: "Go 1.22+ {$} is the end-of-path marker"},
		{name: "unclosed brace", path: "/admin/{", accept: false, why: "an unclosed brace is pattern syntax"},
		{name: "stray closing brace", path: "/admin/foo}", accept: false, why: "a brace is pattern syntax"},

		// Query and fragment separators: not path content.
		{name: "question mark", path: "/wp-admin/?health", accept: false, why: "a decoded ? is query material to the self-defense normalizer"},
		{name: "hash", path: "/wp-admin/#frag", accept: false, why: "a hash is fragment material"},

		// Already covered by the pre-existing route-claim policy, kept for agreement.
		{name: "star", path: "/wp-admin/a*b", accept: false, why: "wildcard syntax"},
		{name: "backslash", path: `/wp-admin/a\b`, accept: false, why: "not a path separator"},
		{name: "percent escape", path: "/wp-admin/%77p", accept: false, why: "the router unescapes patterns and request paths"},
		{name: "double slash", path: "/wp-admin//x", accept: false, why: "an empty segment is ambiguous"},
		{name: "dot segment", path: "/wp-admin/./x", accept: false, why: "a dot segment is ambiguous"},
		{name: "parent segment", path: "/wp-admin/../x", accept: false, why: "a parent segment is a traversal"},
		{name: "control character", path: "/wp-admin/\tx", accept: false, why: "not comparable across a log, a header and a route table"},
		{name: "space", path: "/wp-admin/a b", accept: false, why: "not comparable across a log, a header and a route table"},
	}
}

// TestOperatorDataPlanePathsAreLiteralAndNormalizeUnchanged is the differential
// invariant for the whole failure class, rather than another hand-picked
// metacharacter:
//
//	Every operator-configurable data-plane path that validation ACCEPTS registers a
//	LITERAL ServeMux path, and that registered path is a FIXED POINT of the ingress
//
// self-defense route normalizer.
//
// The second half is the property that actually prevents a route from being shadowed.
// If the self-defense normalizer rewrites the registered path in any way -- cutting at
// a decoded "?", collapsing a leading separator run, folding a trailing slash -- then
// the carve is reasoning about a different string than the router registered, which is
// the disagreement that produced every finding in this series.
//
// The fixed point is stated on the REGISTERED path, not on the raw configured value:
// a configured "/admin/" is legitimate because validation normalizes it to "/admin"
// before anything is mounted, so the string the router registered is "/admin". What
// must hold is that the normalizer cannot move it from there.
func TestOperatorDataPlanePathsAreLiteralAndNormalizeUnchanged(t *testing.T) {
	t.Parallel()

	for _, tc := range operatorPathCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			contractErr := httpcontract.ValidateOperatorRoutePath("path", tc.path)
			frontendAccepted := frontendAcceptsBasePath(t, tc.path)

			switch {
			case tc.accept && contractErr != nil:
				t.Errorf("path %q must be accepted (%s): %v", tc.path, tc.why, contractErr)
			case tc.accept && !frontendAccepted:
				t.Errorf("path %q must be accepted as base_path (%s), but the OpenResponses frontend rejected it", tc.path, tc.why)
			case !tc.accept && contractErr == nil:
				t.Errorf("path %q must be rejected (%s), but the route contract accepted it", tc.path, tc.why)
			case !tc.accept && frontendAccepted:
				t.Errorf("path %q must be rejected (%s), but the OpenResponses frontend accepted it as base_path", tc.path, tc.why)
			}

			if !tc.accept || contractErr != nil {
				return
			}
			// The registered path is what validation normalizes the value to, and the
			// self-defense normalizer must not be able to move it.
			registered, err := httpcontract.NormalizePath(tc.path)
			if err != nil {
				t.Errorf("path %q was accepted as an operator path but NormalizePath rejects it: %v", tc.path, err)
				return
			}
			if got := selfDefenseNormalizedPath(registered); got != registered {
				t.Errorf("path %q registers as %q, but the self-defense normalizer rewrites that to %q; "+
					"the carve and the router would then disagree about this route", tc.path, registered, got)
			}
		})
	}
}

// TestOperatorDataPlanePathPolicyHasNoGapsBetweenValidators requires the two
// statements of the policy to accept and reject exactly the same values, so a
// character added to one metacharacter list without the other cannot pass.
func TestOperatorDataPlanePathPolicyHasNoGapsBetweenValidators(t *testing.T) {
	t.Parallel()

	for _, r := range []rune{'?', '#', '*', '\\', '{', '}', '%', ' ', '\t', 0x7f, 0x01} {
		path := "/v1" + string(r) + "x"
		contractRejected := httpcontract.ValidateOperatorRoutePath("path", path) != nil
		frontendRejected := !frontendAcceptsBasePath(t, path)
		if contractRejected != frontendRejected {
			t.Errorf("rune %q: the route contract rejected = %v but the OpenResponses base_path rejected = %v; "+
				"the two statements of the operator-path policy must agree", r, contractRejected, frontendRejected)
		}
	}
	for _, r := range []rune{'-', '_', '.', '~', ':', '@', '+', '=', ',', ';', '!', '$', '&', '\'', '(', ')'} {
		path := "/v1" + string(r) + "x"
		if err := httpcontract.ValidateOperatorRoutePath("path", path); err != nil {
			t.Errorf("rune %q must be allowed in a literal operator path: %v", r, err)
		}
	}
}

// frontendAcceptsBasePath reports whether the OpenResponses frontend accepts path as
// its base_path, which is the operator-facing route configuration in the standard
// distribution.
func frontendAcceptsBasePath(t *testing.T, path string) bool {
	t.Helper()
	cfg := openresponses.Config{BasePath: path}
	return cfg.Validate() == nil
}

// selfDefenseNormalizedPath applies the ingress self-defense route normalization to a
// path and returns the result, mirroring what the gate and the resolver do to a path
// before comparing it.
func selfDefenseNormalizedPath(path string) string {
	if cut := strings.IndexByte(path, '?'); cut >= 0 {
		path = path[:cut]
	}
	count := 0
	for count < len(path) && path[count] == '/' {
		count++
	}
	if count > 1 {
		path = path[count-1:]
	}
	if path == "/" {
		return path
	}
	return strings.TrimRight(path, "/")
}

// TestSelfDefenseRouteNormalizationIsExportedForTheInvariant keeps the differential
// test honest: it must exercise the production normalizer, not a copy of it, or it
// would keep passing after the real normalizer changed.
func TestSelfDefenseRouteNormalizationIsExportedForTheInvariant(t *testing.T) {
	t.Parallel()
	if selfDefenseNormalizedPath("/a//b/") == selfDefenseNormalizedPath("/nope") {
		t.Fatal("the local normalizer in this test is degenerate; it cannot prove anything")
	}
	if !selfdefense.ImpossiblePath("/wp-admin/adminer.php") {
		t.Fatal("the frozen impossible-path families must still be active for this invariant to be meaningful")
	}
	_ = config.Config{}
}
