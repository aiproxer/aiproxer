package secretguard_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

type stubMatcher struct{}

func (stubMatcher) ScanBytes(context.Context, []byte) ([]secretguard.Finding, error) {
	return nil, nil
}

func (stubMatcher) ScanString(context.Context, string) ([]secretguard.Finding, error) {
	return nil, nil
}

func (stubMatcher) RedactBytes(context.Context, []byte) ([]byte, []secretguard.Finding, error) {
	return nil, nil, nil
}

func (stubMatcher) RedactString(context.Context, string) (string, []secretguard.Finding, error) {
	return "", nil, nil
}

type stubResolver struct{}

func (stubResolver) Resolve(context.Context) (secretguard.Matcher, error) {
	return stubMatcher{}, nil
}

type stubGuard struct {
	id  string
	ord int
	fm  secretguard.FailureMode
}

func (g stubGuard) ID() string                           { return g.id }
func (g stubGuard) Order() int                           { return g.ord }
func (g stubGuard) FailureMode() secretguard.FailureMode { return g.fm }
func (g stubGuard) Evaluate(context.Context, *aipapi.Call, secretguard.Meta, secretguard.Services) (secretguard.Decision, error) {
	return secretguard.Decision{Outcome: secretguard.OutcomePass}, nil
}

func TestCompileTimeContracts(t *testing.T) {
	t.Parallel()
	var (
		_ secretguard.Guard           = stubGuard{}
		_ secretguard.Matcher         = stubMatcher{}
		_ secretguard.MatcherResolver = stubResolver{}
	)
}

func TestFinding_hasNoValueOrExcerptFields(t *testing.T) {
	t.Parallel()
	rt := reflect.TypeFor[secretguard.Finding]()
	for f := range rt.Fields() {
		switch f.Name {
		case "Value", "Excerpt", "Secret", "SecretValue", "Content", "Sample":
			t.Fatalf("Finding must not expose field %q", f.Name)
		}
	}
}

func TestServices_hasNoRawSecretAccessor(t *testing.T) {
	t.Parallel()
	rt := reflect.TypeFor[secretguard.Services]()
	for f := range rt.Fields() {
		switch f.Name {
		case "Catalog", "Secrets", "Env", "EnvReader", "RawSecrets", "Values":
			t.Fatalf("Services must not expose raw secret accessor field %q", f.Name)
		}
	}
	if _, ok := rt.FieldByName("MatcherResolver"); !ok {
		t.Fatal("Services must expose MatcherResolver")
	}
}

func TestOutcomeConstants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		got  secretguard.Outcome
		want secretguard.Outcome
	}{
		{name: "pass", got: secretguard.OutcomePass, want: "pass"},
		{name: "log", got: secretguard.OutcomeLog, want: "log"},
		{name: "redacted", got: secretguard.OutcomeRedacted, want: "redacted"},
		{name: "block", got: secretguard.OutcomeBlock, want: "block"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.got != tc.want {
				t.Fatalf("outcome: got %q want %q", tc.got, tc.want)
			}
		})
	}
}

func TestFailureMode_ordering(t *testing.T) {
	t.Parallel()
	if secretguard.FailureModeUnspecified != 0 {
		t.Fatal("unspecified must be zero value")
	}
	if secretguard.FailOpen == secretguard.FailClosed {
		t.Fatal("FailOpen and FailClosed must differ")
	}
}

func TestGuard_Evaluate_passThroughStub(t *testing.T) {
	t.Parallel()
	g := stubGuard{id: "g", ord: 10, fm: secretguard.FailClosed}
	d, err := g.Evaluate(t.Context(), &aipapi.Call{}, secretguard.Meta{}, secretguard.Services{
		MatcherResolver: stubResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != secretguard.OutcomePass {
		t.Fatalf("outcome: got %q", d.Outcome)
	}
	if g.Order() != 10 || g.FailureMode() != secretguard.FailClosed {
		t.Fatal("order/failure mode mismatch")
	}
}
