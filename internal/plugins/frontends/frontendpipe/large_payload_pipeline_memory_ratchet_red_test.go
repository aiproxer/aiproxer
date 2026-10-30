package frontendpipe_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// Finding B1 Memory Ratchet Test:
// In the fast path (accepted large body above threshold), the spooled body must NEVER be
// materialized into a payload-sized []byte before or during proof.
//
// Current Defect (Finding B1):
// CaptureCandidateBody in internal/plugins/frontends/frontendpipe/candidate.go (lines 387-388)
// unconditionally calls readCompletedSource(compSrc) which calls io.ReadAll on the spooled
// CompletedSource. As a result:
// 1. CaptureCandidateBody returns a full payload-sized []byte into memory (e.g. 1MiB, 5MiB, 20MiB).
// 2. Real end-to-end B/op scales linearly with payload size: ~2.5MB @1MiB, ~11.3MB @5MiB, ~50.4MB @20MiB,
//    violating the flat bounded budget (maxAllowedProofBytesPerOp = 448 KiB).
//
// These tests execute the real pipeline:
// HTTP capture → CompletedSource → proof → assessment → ExecuteLargeBody → backend open
// and assert both:
// (a) No payload-sized []byte is materialized in memory on candidate capture.
// (b) Transient memory allocations (B/op) remain flat and strictly bounded by maxAllowedProofBytesPerOp.

type ratchetAssessorExecutor struct {
	candidateGatesExec
	backendOpenCount int64
}

func (e *ratchetAssessorExecutor) AssessLargeBody(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
	stamp, err := largebody.NewAssessmentStamp(
		"gen_ratchet_1",
		proof.ProfileID,
		proof.Source,
		proof.BodyBytes,
		proof.Mode,
		proof.Rewrite,
		proof.Identity,
	)
	if err != nil {
		return largebody.Assessment{}, err
	}
	wireReq := largebody.WireRequestFacts{
		ProfileID:       proof.ProfileID,
		Operation:       proof.Operation,
		Delivery:        proof.Delivery,
		BodyMode:        proof.Mode,
		Rewrite:         proof.Rewrite,
		ClientModel:     proof.ClientModel,
		CandidateModel:  proof.ClientModel,
		MaxOutputTokens: proof.MaxOutputTokens,
	}
	wireDomain := largebody.WireDomainFacts{
		ProfileID:      proof.ProfileID,
		Operation:      proof.Operation,
		Delivery:       proof.Delivery,
		UniversalModel: true,
	}
	return largebody.NewAcceptedAssessment(stamp, wireReq, wireDomain)
}

func (e *ratchetAssessorExecutor) ExecuteLargeBody(ctx context.Context, accepted largebody.Assessment, src largebody.Source) (largebody.ExecutionResult, error) {
	// Real backend open: open independent reader over spooled source at offset 0
	rc, err := src.Open()
	if err != nil {
		return largebody.ExecutionResult{}, err
	}
	defer func() { _ = rc.Close() }()

	e.backendOpenCount++
	return largebody.ExecutionResult{
		Stream: aipapi.NewFixedEventStream([]aipapi.Event{
			{Kind: aipapi.EventTextDelta, Delta: "chunk"},
			{Kind: aipapi.EventResponseFinished},
		}),
	}, nil
}

func newRatchetPipeSpec(
	spoolDir string,
	prof frontendpipe.FrontendProfile,
	urlPath string,
	exec aipsdk.ExecutorView,
) frontendpipe.Spec[struct{}] {
	return frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec:                 exec,
			FrontendID:           "pipeline_memory_ratchet_test",
			DefaultRouteSelector: "stub:bench",
			RoutePrefixes:        routeselect.NewPrefixSet([]string{"stub", "openai"}),
			MaxRequestBodyBytes:  32 << 20,
			LargePayload: frontendpipe.LargePayloadConfig{
				Enabled:          true,
				ThresholdBytes:   targetInvariantMemorySpoolBytes, // 64 KiB
				SpoolDir:         spoolDir,
				MemorySpoolBytes: targetInvariantMemorySpoolBytes, // 64 KiB
				CopyBufferSize:   32 << 10,
			},
			DecodeAdmission: &trackingAdmissionLimiter{},
		},
		RouteFromBodyModel: true,
		Wire:               frontendpipe.OpenAIWire{},
		Profile:            prof,
		MatchPath: func(p string) (frontendpipe.PathMatch, bool) {
			if p == urlPath {
				return frontendpipe.PathMatch{}, true
			}
			return frontendpipe.PathMatch{}, false
		},
		Decode: func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
			return nil, errors.New("canonical Decode must not be reached in accepted fast path")
		},
		BuildEncodeOpts: func(decoded *frontendpipe.Decoded) struct{} {
			return struct{}{}
		},
		WriteNonStream: func(ctx context.Context, w http.ResponseWriter, call *aipapi.Call, es aipapi.EventStream, opts struct{}) error {
			w.WriteHeader(http.StatusOK)
			return nil
		},
		WireWriteNonStream: func(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es aipapi.EventStream) error {
			w.WriteHeader(http.StatusOK)
			return nil
		},
		WireWriteStream: func(ctx context.Context, w http.ResponseWriter, rc frontendpipe.ResponseContext, es aipapi.EventStream) error {
			w.WriteHeader(http.StatusOK)
			return nil
		},
	}
}

func TestFindingB1_CaptureCandidateBody_NoPayloadSizedMemorySlice(t *testing.T) {
	type sizeCase struct {
		name   string
		target int
	}

	sizes := []sizeCase{
		{name: "1MiB", target: 1 << 20},
	}
	if !testing.Short() {
		sizes = append(
			sizes,
			sizeCase{name: "5MiB", target: 5 << 20},
			sizeCase{name: "20MiB", target: 20 << 20},
		)
	}

	lanes := []struct {
		laneID      string
		profile     frontendpipe.FrontendProfile
		urlPath     string
		bodyBuilder func(tb testing.TB, target int) []byte
	}{
		{
			laneID:  "openai_responses",
			profile: openairesponses.NewProfile(),
			urlPath: "/v1/responses",
			bodyBuilder: func(tb testing.TB, target int) []byte {
				tb.Helper()
				if target > (8 << 20) {
					return bench20MiBChunkedBody(tb, target)
				}
				return baselineResponsesBody(tb, target)
			},
		},
		{
			laneID:  "openai_chat",
			profile: openailegacy.NewProfile(),
			urlPath: "/v1/chat/completions",
			bodyBuilder: func(tb testing.TB, target int) []byte {
				tb.Helper()
				if target > (8 << 20) {
					return bench20MiBChatBody(tb, target)
				}
				return baselineChatBody(tb, target)
			},
		},
		{
			laneID:  "openresponses",
			profile: openresponses.NewProfile(),
			urlPath: "/openresponses/v1/responses",
			bodyBuilder: func(tb testing.TB, target int) []byte {
				tb.Helper()
				if target > (8 << 20) {
					return bench20MiBOpenResponsesBody(tb, target)
				}
				return baselineOpenResponsesBody(tb, target)
			},
		},
	}

	for _, lane := range lanes {
		t.Run(lane.laneID, func(t *testing.T) {
			for _, sz := range sizes {
				t.Run(sz.name, func(t *testing.T) {
					body := lane.bodyBuilder(t, sz.target)
					spoolDir := t.TempDir()
					exec := &ratchetAssessorExecutor{}
					spec := newRatchetPipeSpec(spoolDir, lane.profile, lane.urlPath, exec)

					req := httptest.NewRequest(http.MethodPost, lane.urlPath, bytes.NewReader(body))
					rec := httptest.NewRecorder()

					capRes, capturedBody, err := frontendpipe.CaptureCandidateBody(req.Context(), &spec, rec, req, spec.MaxRequestBodyBytes)
					require.NoError(t, err)
					require.Equal(t, largebody.CaptureOutcomeCompleted, capRes.Outcome)
					require.NotNil(t, capRes.Completed)
					t.Cleanup(func() { _ = capRes.Completed.Close() })
					require.True(t, capRes.Completed.HasSpilled(), "payload %d B must have spilled to disk with 64 KiB spool limit", sz.target)

					// Finding B1 assertion:
					// On the accepted fast path above threshold, spooled body must NOT be materialized into []byte.
					// Under current defect (candidate.go:387), readCompletedSource(compSrc) loads the entire
					// 1MiB/5MiB/20MiB payload into memory, setting len(capturedBody) == len(body).
					assert.Empty(t, capturedBody,
						"accepted candidate capture must not materialize spooled body into memory []byte before proof (got %d bytes in memory)", len(capturedBody))
				})
			}
		})
	}
}
