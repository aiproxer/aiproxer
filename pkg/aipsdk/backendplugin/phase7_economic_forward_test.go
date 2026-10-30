package backendplugin_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestPhase7EconomicForward_DrainsV2BeforeCanonicalAndOnOpenFailure(t *testing.T) {
	t.Parallel()
	evidence := backendplugin.AccountingEvidenceV2{Observation: phase7Observation(), Coverage: backendplugin.EvidenceCoverageComplete}
	for _, tc := range []struct {
		name    string
		openErr error
	}{
		{name: "receive", openErr: nil},
		{name: "open failure", openErr: errors.New("open failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stream := &phase7NegotiatedStream{fakeExecuteStream: newFakeExecuteStream(context.Background(), validStartFrame(t)), neg: phase7ForwardNegotiation()}
			ms := &phase7EconomicManaged{evidence: []backendplugin.AccountingEvidenceV2{evidence}, events: []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "visible"}}}
			err := backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
				return ms, tc.openErr
			})
			if tc.openErr != nil {
				if !errors.Is(err, tc.openErr) {
					t.Fatalf("error=%v, want open error", err)
				}
			} else if err != nil {
				t.Fatalf("ForwardExecute: %v", err)
			}
			if len(stream.sent) < 2 || stream.sent[1].AccountingV2 == nil {
				t.Fatalf("V2 evidence was not drained before terminal/error: %+v", stream.sent)
			}
			for _, frame := range stream.sent {
				if frame.Kind == backendplugin.ServerFrameEvent && frame.Event != nil && frame.Event.Kind == backendplugin.EventUsageDelta {
					t.Fatal("host-only V2 evidence became a canonical usage event")
				}
			}
		})
	}
}

func TestPhase7EconomicForward_UnsupportedV2FailsClosedBeforeProviderContent(t *testing.T) {
	t.Parallel()
	stream := newFakeExecuteStream(context.Background(), validStartFrame(t))
	ms := &phase7EconomicManaged{evidence: []backendplugin.AccountingEvidenceV2{{Observation: phase7Observation()}}}
	err := backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
		return ms, nil
	})
	if !errors.Is(err, backendplugin.ErrAccountingEvidenceV2Unsupported) {
		t.Fatalf("error=%v, want unsupported V2 capability", err)
	}
	for _, frame := range stream.sent {
		if frame.Kind == backendplugin.ServerFrameEvent {
			t.Fatal("provider content was forwarded after unsupported V2 evidence")
		}
	}
}

func TestPhase7EconomicForward_OldHostEmptyV2KeepsLegacyV1(t *testing.T) {
	t.Parallel()
	input := int64(5)
	legacy := backendplugin.AccountingEvidence{
		InputTokens: &input,
		Presence:    aipapi.UsagePresence{InputTokens: true},
		Source:      backendplugin.AccountingSourceProviderReported,
		Authority:   backendplugin.AccountingAuthorityAuthoritative,
		Plane:       backendplugin.AccountingPlaneProviderBillable,
		DedupeKey:   "legacy-v1-phase7",
	}
	stream := &phase7NegotiatedStream{
		fakeExecuteStream: newFakeExecuteStream(context.Background(), validStartFrame(t)),
		neg: backendplugin.Negotiation{
			Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorAccountingEvidenceV2 - 1,
			EnabledFeatures: []string{backendplugin.FeatureCancellationHandshake},
		},
	}
	ms := &phase7EconomicManaged{
		legacy: []backendplugin.AccountingEvidence{legacy},
		events: []aipapi.Event{{Kind: aipapi.EventTextDelta, Delta: "visible"}},
	}
	if err := backendplugin.ForwardExecute(stream, func(context.Context, backendplugin.Invocation, aipapi.Call) (aipapi.ManagedEventStream, error) {
		return ms, nil
	}); err != nil {
		t.Fatalf("ForwardExecute: %v", err)
	}
	if len(stream.sent) < 4 || stream.sent[1].Accounting == nil || stream.sent[1].Accounting.DedupeKey != legacy.DedupeKey {
		t.Fatalf("old-host frames = %+v, want legacy V1 evidence before content", stream.sent)
	}
	if stream.sent[2].Kind != backendplugin.ServerFrameEvent || stream.sent[2].Event == nil || stream.sent[2].Event.Delta == nil || *stream.sent[2].Event.Delta != "visible" {
		t.Fatalf("canonical event was not forwarded after empty V2 drain: %+v", stream.sent)
	}
}

func phase7ForwardNegotiation() backendplugin.Negotiation {
	return backendplugin.Negotiation{
		Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorAccountingEvidenceV2,
		EnabledFeatures: []string{backendplugin.FeatureAccountingEvidenceV2, backendplugin.FeatureCancellationHandshake},
	}
}

type phase7NegotiatedStream struct {
	*fakeExecuteStream
	neg backendplugin.Negotiation
}

func (s *phase7NegotiatedStream) Negotiation() backendplugin.Negotiation { return s.neg }

type phase7EconomicManaged struct {
	evidence []backendplugin.AccountingEvidenceV2
	legacy   []backendplugin.AccountingEvidence
	events   []aipapi.Event
	index    int
}

func (m *phase7EconomicManaged) Recv(context.Context) (aipapi.Event, error) {
	if m.index >= len(m.events) {
		return aipapi.Event{}, io.EOF
	}
	ev := m.events[m.index]
	m.index++
	return ev, nil
}

func (m *phase7EconomicManaged) Send(aipapi.Event) error { return nil }
func (m *phase7EconomicManaged) Close() error            { return nil }
func (m *phase7EconomicManaged) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func (m *phase7EconomicManaged) DrainAccountingEvidenceV2() []backendplugin.AccountingEvidenceV2 {
	out := append([]backendplugin.AccountingEvidenceV2(nil), m.evidence...)
	m.evidence = nil
	return out
}

func (m *phase7EconomicManaged) DrainAccountingEvidence() []backendplugin.AccountingEvidence {
	out := append([]backendplugin.AccountingEvidence(nil), m.legacy...)
	m.legacy = nil
	return out
}
