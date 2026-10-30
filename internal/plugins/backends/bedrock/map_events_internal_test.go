package bedrock

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type mockConverseReader struct {
	ch  <-chan types.ConverseStreamOutput
	err error
}

func (m *mockConverseReader) Events() <-chan types.ConverseStreamOutput { return m.ch }

func (m *mockConverseReader) Close() error { return nil }

func (m *mockConverseReader) Err() error { return m.err }

// closingChannelReader closes its event channel when Close is invoked (mirrors how the
// SDK invokes Reader.Close from ConverseStreamEventStream.Close).
type closingChannelReader struct {
	ch chan types.ConverseStreamOutput
}

func (m *closingChannelReader) Events() <-chan types.ConverseStreamOutput { return m.ch }

func (m *closingChannelReader) Close() error {
	close(m.ch)
	return nil
}

func (m *closingChannelReader) Err() error { return nil }

func TestConverseStream_Close_unblocksRecv(t *testing.T) {
	t.Parallel()
	recvAtSelect := make(chan struct{}, 1)
	recvSelectHookMu.Lock()
	recvSelectEntryHook = func() {
		select {
		case recvAtSelect <- struct{}{}:
		default:
		}
	}
	recvSelectHookMu.Unlock()
	t.Cleanup(func() {
		recvSelectHookMu.Lock()
		recvSelectEntryHook = nil
		recvSelectHookMu.Unlock()
	})

	ch := make(chan types.ConverseStreamOutput)
	sdk := bedrockruntime.NewConverseStreamEventStream(func(es *bedrockruntime.ConverseStreamEventStream) {
		es.Reader = &closingChannelReader{ch: ch}
	})
	es := newConverseStream(sdk, 0)
	recvDone := make(chan struct{})
	go func() {
		defer close(recvDone)
		_, _ = es.Recv(context.Background())
	}()
	<-recvAtSelect
	if err := es.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	waitTimer := time.NewTimer(2 * time.Second)
	defer waitTimer.Stop()
	select {
	case <-recvDone:
	case <-waitTimer.C:
		t.Fatal("Recv did not unblock after Close")
	}
}

func TestConverseStream_Close_idempotent_race(t *testing.T) {
	t.Parallel()
	ch := make(chan types.ConverseStreamOutput)
	sdk := bedrockruntime.NewConverseStreamEventStream(func(es *bedrockruntime.ConverseStreamEventStream) {
		es.Reader = &closingChannelReader{ch: ch}
	})
	es := newConverseStream(sdk, 0)
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			_ = es.Close()
		})
	}
	wg.Wait()
}

func TestConverseStream_Recv_wrapsSDKErr(t *testing.T) {
	t.Parallel()
	root := errors.New("root")
	ch := make(chan types.ConverseStreamOutput)
	close(ch)
	sdk := bedrockruntime.NewConverseStreamEventStream(func(es *bedrockruntime.ConverseStreamEventStream) {
		es.Reader = &mockConverseReader{ch: ch, err: root}
	})
	es := newConverseStream(sdk, 0)
	_, err := es.Recv(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "bedrock: recv stream") {
		t.Fatalf("got %q", err.Error())
	}
	if !errors.Is(err, root) {
		t.Fatalf("underlying: %v", err)
	}
}

func TestHandleOutput_metadataUsageDetails(t *testing.T) {
	t.Parallel()
	s := &converseStream{pending: stream.NewPendingEventQueue(0)}
	err := s.handleOutput(&types.ConverseStreamOutputMemberMetadata{
		Value: types.ConverseStreamMetadataEvent{
			Usage: &types.TokenUsage{
				InputTokens:           aws.Int32(11),
				OutputTokens:          aws.Int32(8),
				TotalTokens:           aws.Int32(19),
				CacheReadInputTokens:  aws.Int32(3),
				CacheWriteInputTokens: aws.Int32(4),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	ev := findUsageEvent(t, stream.DrainPending(&s.pending))
	if ev.InputTokens != 11 || ev.OutputTokens != 8 {
		t.Fatalf("usage tokens: in=%d out=%d", ev.InputTokens, ev.OutputTokens)
	}
	assertUsageIntField(t, ev, "CacheReadTokens", 3)
	assertUsageIntField(t, ev, "CacheWriteTokens", 4)
	assertUsageIntField(t, ev, "TotalTokens", 19)
	assertUsageRawJSONContains(t, ev, "InputTokens")
}

func TestHandleOutput_metadataUsagePreservesCacheLifetimeAndRejectsNegative(t *testing.T) {
	t.Parallel()
	s := &converseStream{
		pending:                stream.NewPendingEventQueue(0),
		ProviderEvidenceBuffer: coremetering.NewProviderEvidenceBuffer(),
	}
	err := s.handleOutput(&types.ConverseStreamOutputMemberMetadata{
		Value: types.ConverseStreamMetadataEvent{
			Usage: &types.TokenUsage{
				InputTokens:          aws.Int32(2),
				OutputTokens:         aws.Int32(-1),
				TotalTokens:          aws.Int32(-1),
				CacheDetails:         []types.CacheDetail{{InputTokens: aws.Int32(0), Ttl: types.CacheTTLFiveMinutes}},
				CacheReadInputTokens: aws.Int32(3),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ev := findUsageEvent(t, stream.DrainPending(&s.pending))
	if ev.UsagePresence.OutputTokens || ev.OutputTokens != 0 || ev.UsagePresence.TotalTokens || ev.TotalTokens != 0 {
		t.Fatalf("negative Bedrock counters became present: %+v", ev)
	}
	if !ev.UsagePresence.InputTokens || !ev.UsagePresence.CacheReadTokens {
		t.Fatalf("valid Bedrock counters were lost: %+v", ev)
	}
	s.BindEconomicEvidence(coremetering.ObservationIdentity{StoreID: "store", BLegID: "b-leg"})
	observations := s.DrainEconomicObservations()
	if len(observations) != 1 {
		t.Fatalf("Bedrock V2 observations = %d, want 1", len(observations))
	}
	foundLifetime := false
	for _, measure := range observations[0].Measures {
		for _, dimension := range measure.Key.Dimensions {
			if dimension.Name == "cache_lifetime" && dimension.Value == "5m" {
				foundLifetime = true
			}
		}
	}
	if !foundLifetime {
		t.Fatalf("Bedrock cache lifetime measure missing: %+v", observations[0].Measures)
	}
}

func findUsageEvent(t *testing.T, events []aipapi.Event) aipapi.Event {
	t.Helper()
	for _, ev := range events {
		if ev.Kind == aipapi.EventUsageDelta {
			return ev
		}
	}
	t.Fatalf("usage event not found in %+v", events)
	return aipapi.Event{}
}

func assertUsageIntField(t *testing.T, ev aipapi.Event, name string, want int64) {
	t.Helper()
	field := reflect.ValueOf(ev).FieldByName(name)
	if !field.IsValid() {
		return
	}
	if got := field.Int(); got != want {
		t.Fatalf("%s: got %d, want %d", name, got, want)
	}
}

func assertUsageRawJSONContains(t *testing.T, ev aipapi.Event, needle string) {
	t.Helper()
	field := reflect.ValueOf(ev).FieldByName("RawUsageJSON")
	if !field.IsValid() {
		return
	}
	switch field.Kind() {
	case reflect.String:
		if !strings.Contains(field.String(), needle) {
			t.Fatalf("RawUsageJSON: %q does not contain %q", field.String(), needle)
		}
	case reflect.Slice:
		if !strings.Contains(string(field.Bytes()), needle) {
			t.Fatalf("RawUsageJSON: %q does not contain %q", string(field.Bytes()), needle)
		}
	}
}
