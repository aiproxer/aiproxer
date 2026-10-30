package runtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	aipruntime "github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestReplayLineage_recvFailoverIncrementsBLegs(t *testing.T) {
	t.Parallel()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := aipruntime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(1)
	ex.Backends = map[string]execbackend.Backend{
		"bad": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return &oneThenFailStream{
					first: aipapi.Event{Kind: aipapi.EventResponseStarted},
					then:  aipapi.RecoverablePreOutputError(errors.New("recv")),
				}, nil
			},
		},
		"ok": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "lineage-recv"},
		Route:   aipapi.RouteIntent{Selector: "bad:m|ok:m"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
	s, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	alegID := strings.TrimSpace(call.Session.ALegID)
	if alegID == "" {
		t.Fatal("expected aleg id on call after execute")
	}
	leg, err := st.FetchALeg(context.Background(), alegID)
	if err != nil {
		t.Fatal(err)
	}
	rowsBefore, _ := st.LoadAttempts(context.Background(), leg.ALegID)
	for {
		_, err := s.Recv(context.Background())
		if err != nil {
			break
		}
	}
	_ = s.Close()
	rowsAfter, err := st.LoadAttempts(context.Background(), leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rowsAfter) <= len(rowsBefore) {
		t.Fatalf("expected attempt rows after recv failover, before=%d after=%d", len(rowsBefore), len(rowsAfter))
	}
}

type oneThenFailStream struct {
	sent  bool
	first aipapi.Event
	then  error
}

func (o *oneThenFailStream) Recv(context.Context) (aipapi.Event, error) {
	if !o.sent {
		o.sent = true
		return o.first, nil
	}
	return aipapi.Event{}, o.then
}

func (o *oneThenFailStream) Close() error { return nil }

func (o *oneThenFailStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func TestReplayFixture_minimalRequestJSON(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	p := filepath.Join(repoRoot, "testdata", "replay", "minimal_request.json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["user_message"] == nil {
		t.Fatalf("fixture: %#v", got)
	}
}
