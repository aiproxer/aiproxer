package service

import (
	"context"
	"io"
	"testing"

	"github.com/aiproxer/aiproxer/connectors/claudeagentacp/internal/product"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type scriptedManagedStream struct {
	events []aipapi.Event
}

func (s *scriptedManagedStream) Recv(context.Context) (aipapi.Event, error) {
	if len(s.events) == 0 {
		return aipapi.Event{}, io.EOF
	}
	ev := s.events[0]
	s.events = s.events[1:]
	return ev, nil
}

func (s *scriptedManagedStream) Close() error { return nil }

func (s *scriptedManagedStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{}
}

func TestClassifiedStream_SwallowsAuthStatusIntoDiagnostics(t *testing.T) {
	t.Parallel()
	var summaries []string
	inner := &scriptedManagedStream{events: []aipapi.Event{
		{Kind: aipapi.EventWarning, WarningCode: "auth_status", WarningMessage: "auth identity kind=account label=\"Claude Max\""},
		{Kind: aipapi.EventTextDelta, Delta: "hi"},
		{Kind: aipapi.EventWarning, WarningCode: "other", WarningMessage: "kept"},
	}}
	stream := &classifiedStream{
		ManagedEventStream: inner,
		spec:               mustTestSpec(t),
		phase:              aipapi.PhasePreOutput,
		onAuthStatus:       func(summary string) { summaries = append(summaries, summary) },
	}
	var got []aipapi.Event
	for {
		ev, err := stream.Recv(context.Background())
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		got = append(got, ev)
	}
	if len(summaries) != 1 || summaries[0] != "auth identity kind=account label=\"Claude Max\"" {
		t.Fatalf("diagnostics = %v", summaries)
	}
	if len(got) != 2 || got[0].Kind != aipapi.EventTextDelta || got[1].WarningCode != "other" {
		t.Fatalf("client-visible events = %#v", got)
	}
}

func mustTestSpec(t *testing.T) *product.ClaudeSpec {
	t.Helper()
	auth, err := product.NewAuthEnv(product.AuthConfig{}, map[string]string{"api_key": "test-only"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := product.NewClaudeSpec("test-adapter", t.TempDir(), auth)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}
