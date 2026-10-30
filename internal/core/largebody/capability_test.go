package largebody_test

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// canonicalOnlyExecutor is an external/manual executor: it implements only the
// public aipsdk.ExecutorView contract and knows nothing about the internal
// large-body capability.
type canonicalOnlyExecutor struct {
	executeCalls int
}

func (e *canonicalOnlyExecutor) Execute(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	e.executeCalls++
	return stubStream{}, nil
}

func (e *canonicalOnlyExecutor) CancelALeg(context.Context, aipapi.ALegCancelRequest) error {
	return nil
}

func (e *canonicalOnlyExecutor) WallClock() func() time.Time { return nil }

// capableExecutor is a standard internal executor: public view plus the
// internal optional large-body capability.
type capableExecutor struct {
	canonicalOnlyExecutor
	assessCalls  int
	executeCalls int
}

func (e *capableExecutor) AssessLargeBody(_ context.Context, _ largebody.Proof) (largebody.Assessment, error) {
	e.assessCalls++
	return largebody.Assessment{
		Decision: largebody.AssessmentDecisionDecline,
		Reason:   largebody.DeclineReasonAuthorityBlocker,
	}, nil
}

func (e *capableExecutor) ExecuteLargeBody(_ context.Context, _ largebody.Assessment, _ largebody.Source) (largebody.ExecutionResult, error) {
	e.executeCalls++
	return largebody.ExecutionResult{}, nil
}

var (
	_ aipsdk.ExecutorView = (*canonicalOnlyExecutor)(nil)
	_ aipsdk.ExecutorView = (*capableExecutor)(nil)
)

func executorViewMethodNames(t *testing.T) []string {
	t.Helper()
	typ := reflect.TypeFor[aipsdk.ExecutorView]()
	if typ.Kind() != reflect.Interface {
		t.Fatalf("aipsdk.ExecutorView is %s, want interface", typ.Kind())
	}
	var names []string
	for method := range typ.Methods() {
		names = append(names, method.Name)
	}
	sort.Strings(names)
	return names
}

func TestExecutorView_PublicContractUnchanged(t *testing.T) {
	t.Parallel()
	names := executorViewMethodNames(t)
	want := []string{"CancelALeg", "Execute", "WallClock"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("aipsdk.ExecutorView methods = %v, want %v (no mandatory large-body methods)", names, want)
	}
	for _, forbidden := range []string{"AssessLargeBody", "ExecuteLargeBody", "LargeBodyStaticDisposition"} {
		for _, name := range names {
			if name == forbidden {
				t.Fatalf("aipsdk.ExecutorView must not gain mandatory method %q", forbidden)
			}
		}
	}
}

func TestLargeBodyCapability_DefinedInternally(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[largebody.LargeBodyExecutor]()
	if typ.Kind() != reflect.Interface {
		t.Fatalf("largebody.LargeBodyExecutor is %s, want interface", typ.Kind())
	}
	var names []string
	for method := range typ.Methods() {
		names = append(names, method.Name)
	}
	sort.Strings(names)
	want := []string{"AssessLargeBody", "ExecuteLargeBody"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("largebody.LargeBodyExecutor methods = %v, want %v", names, want)
	}
	if typ.PkgPath() == "" || typ.PkgPath() == "github.com/aiproxer/aiproxer/pkg/aipsdk" {
		t.Fatalf("large-body capability lives in %q, must stay internal (not public SDK)", typ.PkgPath())
	}
}

func TestAsLargeBodyExecutor_NilAndAbsentYieldCanonical(t *testing.T) {
	t.Parallel()
	if got, ok := largebody.AsLargeBodyExecutor(nil); ok || got != nil {
		t.Fatalf("AsLargeBodyExecutor(nil) = (%v, %v), want (nil, false)", got, ok)
	}
	var nilView aipsdk.ExecutorView
	if got, ok := largebody.AsLargeBodyExecutor(nilView); ok || got != nil {
		t.Fatalf("AsLargeBodyExecutor(typed-nil view) = (%v, %v), want (nil, false)", got, ok)
	}
	canonical := &canonicalOnlyExecutor{}
	if got, ok := largebody.AsLargeBodyExecutor(canonical); ok || got != nil {
		t.Fatalf("AsLargeBodyExecutor(canonical-only) = (%v, %v), want (nil, false)", got, ok)
	}
	capable := &capableExecutor{}
	got, ok := largebody.AsLargeBodyExecutor(capable)
	if !ok || got == nil {
		t.Fatalf("AsLargeBodyExecutor(capable) = (%v, %v), want (non-nil, true)", got, ok)
	}
	if _, err := got.AssessLargeBody(context.Background(), largebody.Proof{}); err == nil && capable.assessCalls != 1 {
		t.Fatalf("assessCalls = %d, want 1 via asserted capability", capable.assessCalls)
	}
}

func TestExternalManualExecutor_RemainsCanonicalOnly(t *testing.T) {
	t.Parallel()
	manual := &canonicalOnlyExecutor{}
	var view aipsdk.ExecutorView = manual
	// Interface satisfaction is asserted at compile time above (var _ aipsdk.ExecutorView);
	// manual is a non-nil concrete pointer, so a runtime view == nil check cannot fail.
	if _, ok := largebody.AsLargeBodyExecutor(view); ok {
		t.Fatal("external/manual executor without capability must stay canonical-only")
	}
	stream, err := view.Execute(context.Background(), &aipapi.Call{})
	if err != nil {
		t.Fatalf("canonical Execute error = %v", err)
	}
	if stream == nil {
		t.Fatal("canonical Execute must return a stream")
	}
	if manual.executeCalls != 1 {
		t.Fatalf("canonical Execute calls = %d, want 1", manual.executeCalls)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("stream Close error = %v", err)
	}
}
