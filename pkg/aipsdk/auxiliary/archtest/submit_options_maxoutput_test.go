package archtest_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

type externalThreeMethodClient struct{}

func (externalThreeMethodClient) SubmitCollect(context.Context, auxiliary.Request, auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	return "", nil
}

func (externalThreeMethodClient) Await(context.Context, auxiliary.JobID) (aipapi.Collected, error) {
	return aipapi.Collected{}, nil
}
func (externalThreeMethodClient) Forget(auxiliary.JobID) {}

var (
	_ auxiliary.BackgroundClient = externalThreeMethodClient{}
	_ auxiliary.BackgroundClient = (*externalThreeMethodClient)(nil)
)

func TestSubmitOptions_SourceCompatibility_ThreeMethodStillCompiles(t *testing.T) {
	t.Parallel()
	var c auxiliary.BackgroundClient = externalThreeMethodClient{}
	if _, ok := any(c).(auxiliary.BackgroundPoller); ok {
		t.Fatalf("external three-method client must NOT satisfy BackgroundPoller")
	}
}

func TestSubmitOptions_ZeroValueHasMaxOutputBytesZero(t *testing.T) {
	t.Parallel()
	var opts auxiliary.SubmitOptions
	if opts.MaxOutputBytes != 0 {
		t.Fatalf("zero-value MaxOutputBytes=%d want 0", opts.MaxOutputBytes)
	}
	// Ensure zero value preserves existing behavior (no panic, no allocation).
	if opts.CoalesceKey != "" || opts.Timeout != 0 || !opts.Deadline.IsZero() || opts.OnCoalesced != nil {
		t.Fatalf("zero-value other fields must be zero")
	}
}

func TestSubmitOptions_ExactFieldArchitecture(t *testing.T) {
	t.Parallel()
	typ := reflect.TypeFor[auxiliary.SubmitOptions]()
	if typ.Kind() != reflect.Struct {
		t.Fatalf("SubmitOptions kind=%v want struct", typ.Kind())
	}
	if typ.NumField() != 5 {
		t.Fatalf("SubmitOptions num fields=%d want 5 (CoalesceKey, Timeout, Deadline, OnCoalesced, MaxOutputBytes)", typ.NumField())
	}
	expect := []struct {
		name string
		typ  string
	}{
		{"CoalesceKey", "string"},
		{"Timeout", "time.Duration"},
		{"Deadline", "time.Time"},
		{"OnCoalesced", "func(bool)"},
		{"MaxOutputBytes", "int"},
	}
	for i, e := range expect {
		f := typ.Field(i)
		if f.Name != e.name {
			t.Fatalf("field %d name=%q want %q", i, f.Name, e.name)
		}
		if f.Type.String() != e.typ {
			t.Fatalf("field %d %s type=%q want %q", i, e.name, f.Type.String(), e.typ)
		}
	}
	// Verify MaxOutputBytes is the last additive field for source compatibility.
	last := typ.Field(4)
	if last.Name != "MaxOutputBytes" {
		t.Fatalf("last field=%q want MaxOutputBytes", last.Name)
	}
}

func TestSubmitOptions_DisabledStillDoesNotRequirePoll(t *testing.T) {
	t.Parallel()
	var c auxiliary.BackgroundClient = auxiliary.DisabledBackgroundClient{}
	if _, ok := any(c).(auxiliary.BackgroundPoller); ok {
		t.Fatalf("DisabledBackgroundClient must NOT require BackgroundPoller")
	}
}
