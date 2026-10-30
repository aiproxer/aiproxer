package largebody_test

import (
	"crypto/sha256"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/compactionfacts"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func sha256Digest(b byte) [32]byte {
	var d [32]byte
	for i := range d {
		d[i] = b
	}
	return d
}

func makeValidProof(gen string) (largebody.Proof, [32]byte) {
	srcDigest := sha256.Sum256([]byte(`{"model":"gpt-4o","prompt":"hello"}`))
	identDigest := sha256.Sum256([]byte(`canonical-identity`))

	proof := largebody.Proof{
		ProfileID:       "openai-chat",
		Operation:       aipapi.OperationOpenAIChatCompletions,
		Delivery:        aipapi.DeliveryModeStreaming,
		RouteSelector:   "default",
		ClientModel:     "gpt-4o",
		MaxOutputTokens: 4096,
		Facts:           largebody.ProtocolFacts{RequirementsID: "chat-v1", ControlCount: 0},
		Mode:            largebody.BodyModeIdentityJSON,
		Rewrite:         largebody.NewNoRewrite(),
		ModelSpan:       largebody.Span{},
		Identity:        largebody.NewIdentityDigest(identDigest),
		Turn: largebody.ClientTurnShape{
			Items: []largebody.ClientTurnItemShape{
				{
					Kind:    aipapi.ItemKindMessage,
					Role:    aipapi.RoleUser,
					Ordinal: 0,
					Parts: []largebody.ClientTurnPartShape{
						{Kind: aipapi.ContentPartText, ContentBytes: 32},
					},
				},
			},
			TotalContentBytes: 32,
		},
		Session: largebody.SessionInput{
			AuthoritativeSessionID: "sess-1",
			ClientSessionID:        "client-1",
			ALegID:                 "a-leg-1",
		},
		Source:    largebody.NewSourceDigest(srcDigest),
		BodyBytes: int64(len(`{"model":"gpt-4o","prompt":"hello"}`)),
	}
	return proof, srcDigest
}

// TestTask11_8_StampBindsCandidateDomainGeneration verifies that AssessmentStamp
// binds generation identity, profile/proof identity, source digest/size, body mode/rewrite contract,
// and candidate/domain proof generation (Requirements 6.7, 8.5; Task 11.8).
func TestTask11_8_StampBindsCandidateDomainGeneration(t *testing.T) {
	t.Parallel()
	proof, srcDigest := makeValidProof("gen-1")
	identDigest := sha256Digest(0xaa)

	// 1. NewAssessmentStamp with explicit candidateDomainGen
	stamp, err := largebody.NewAssessmentStamp(
		"gen-1",
		"openai-chat",
		largebody.NewSourceDigest(srcDigest),
		proof.BodyBytes,
		proof.Mode,
		proof.Rewrite,
		largebody.NewIdentityDigest(identDigest),
		"domain-gen-1",
	)
	if err != nil {
		t.Fatalf("NewAssessmentStamp error: %v", err)
	}

	if stamp.GenerationID() != "gen-1" {
		t.Errorf("GenerationID = %q, want %q", stamp.GenerationID(), "gen-1")
	}
	if stamp.ProfileID() != "openai-chat" {
		t.Errorf("ProfileID = %q, want %q", stamp.ProfileID(), "openai-chat")
	}
	if stamp.SourceDigest() != largebody.NewSourceDigest(srcDigest) {
		t.Errorf("SourceDigest mismatch")
	}
	if stamp.BodyBytes() != proof.BodyBytes {
		t.Errorf("BodyBytes = %d, want %d", stamp.BodyBytes(), proof.BodyBytes)
	}
	if stamp.BodyMode() != proof.Mode {
		t.Errorf("BodyMode mismatch")
	}
	if stamp.Rewrite() != proof.Rewrite {
		t.Errorf("Rewrite mismatch")
	}
	if stamp.IdentityDigest() != largebody.NewIdentityDigest(identDigest) {
		t.Errorf("IdentityDigest mismatch")
	}
	if stamp.CandidateDomainGeneration() != "domain-gen-1" {
		t.Errorf("CandidateDomainGeneration = %q, want %q", stamp.CandidateDomainGeneration(), "domain-gen-1")
	}

	// 2. Default candidateDomainGen falls back to generationID
	defaultStamp, err := largebody.NewAssessmentStamp(
		"gen-2",
		"openai-chat",
		largebody.NewSourceDigest(srcDigest),
		proof.BodyBytes,
		proof.Mode,
		proof.Rewrite,
		largebody.NewIdentityDigest(identDigest),
	)
	if err != nil {
		t.Fatalf("NewAssessmentStamp default error: %v", err)
	}
	if defaultStamp.CandidateDomainGeneration() != "gen-2" {
		t.Errorf("default CandidateDomainGeneration = %q, want %q", defaultStamp.CandidateDomainGeneration(), "gen-2")
	}

	// 3. BindAssessmentStamp helper
	boundStamp, err := largebody.BindAssessmentStamp("gen-1", proof, "domain-gen-1")
	if err != nil {
		t.Fatalf("BindAssessmentStamp error: %v", err)
	}
	if boundStamp.GenerationID() != "gen-1" {
		t.Errorf("bound GenerationID = %q, want %q", boundStamp.GenerationID(), "gen-1")
	}
	if boundStamp.ProfileID() != proof.ProfileID {
		t.Errorf("bound ProfileID = %q, want %q", boundStamp.ProfileID(), proof.ProfileID)
	}
	if boundStamp.CandidateDomainGeneration() != "domain-gen-1" {
		t.Errorf("bound CandidateDomainGeneration = %q, want %q", boundStamp.CandidateDomainGeneration(), "domain-gen-1")
	}

	// 4. Verify opacity: all fields must remain unexported
	stampTyp := reflect.TypeFor[largebody.AssessmentStamp]()
	for field := range stampTyp.Fields() {
		if field.PkgPath == "" {
			t.Fatalf("AssessmentStamp field %q is exported; stamp must stay opaque (Requirement 6.7)", field.Name)
		}
	}
}

// TestTask11_8_ExecuteRevalidation_DisagreementIsInvariantFailure verifies that
// direct stamp validation revalidates generation/profile/source/size/mode/rewrite/candidate-domain generation
// against live facts, and that ANY disagreement produces an invariant failure error, NEVER fallback to canonical
// (Requirements 6.6, 6.7, 8.5; Task 11.8).
func TestTask11_8_ExecuteRevalidation_DisagreementIsInvariantFailure(t *testing.T) {
	t.Parallel()
	proof, srcDigest := makeValidProof("gen-1")
	identDigest := sha256Digest(0xbb)

	stamp, err := largebody.NewAssessmentStamp(
		"gen-1",
		"openai-chat",
		largebody.NewSourceDigest(srcDigest),
		proof.BodyBytes,
		proof.Mode,
		proof.Rewrite,
		largebody.NewIdentityDigest(identDigest),
		"domain-gen-1",
	)
	if err != nil {
		t.Fatalf("NewAssessmentStamp error: %v", err)
	}

	baseLive := largebody.LiveExecutionFacts{
		GenerationID:              "gen-1",
		ProfileID:                 "openai-chat",
		Source:                    largebody.NewSourceDigest(srcDigest),
		BodyBytes:                 proof.BodyBytes,
		Mode:                      proof.Mode,
		Rewrite:                   proof.Rewrite,
		CandidateDomainGeneration: "domain-gen-1",
	}

	// Baseline matching facts must validate successfully
	if err := largebody.ValidateAssessmentStamp(stamp, baseLive); err != nil {
		t.Fatalf("matching live facts rejected: %v", err)
	}

	// Disagreement test cases: each must fail with ErrStampDisagreement (invariant failure)
	tests := []struct {
		name      string
		modify    func(f *largebody.LiveExecutionFacts)
		wantField string
	}{
		{
			name: "generation identity mismatch",
			modify: func(f *largebody.LiveExecutionFacts) {
				f.GenerationID = "gen-2-drifted"
			},
			wantField: "generation",
		},
		{
			name: "profile identity mismatch",
			modify: func(f *largebody.LiveExecutionFacts) {
				f.ProfileID = "openai-responses-v1"
			},
			wantField: "profile",
		},
		{
			name: "source digest mismatch",
			modify: func(f *largebody.LiveExecutionFacts) {
				f.Source = largebody.NewSourceDigest(sha256Digest(0x99))
			},
			wantField: "source",
		},
		{
			name: "body size mismatch",
			modify: func(f *largebody.LiveExecutionFacts) {
				f.BodyBytes = proof.BodyBytes + 1
			},
			wantField: "body size",
		},
		{
			name: "body mode mismatch",
			modify: func(f *largebody.LiveExecutionFacts) {
				f.Mode = largebody.BodyMode("compressed_json")
			},
			wantField: "body mode",
		},
		{
			name: "rewrite contract mismatch",
			modify: func(f *largebody.LiveExecutionFacts) {
				f.Rewrite, _ = largebody.NewModelTokenRewrite(largebody.Span{Offset: 10, Length: 5})
			},
			wantField: "rewrite",
		},
		{
			name: "candidate domain generation mismatch",
			modify: func(f *largebody.LiveExecutionFacts) {
				f.CandidateDomainGeneration = "domain-gen-2-drifted"
			},
			wantField: "candidate/domain proof generation",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			live := baseLive
			tc.modify(&live)

			err := largebody.ValidateAssessmentStamp(stamp, live)
			if err == nil {
				t.Fatalf("%s: expected invariant failure, got nil", tc.name)
			}

			// Invariant: must wrap ErrStampDisagreement
			if !errors.Is(err, largebody.ErrStampDisagreement) {
				t.Fatalf("%s: error must be ErrStampDisagreement, got %v", tc.name, err)
			}

			// Invariant: error message must detail the mismatched field
			if !strings.Contains(strings.ToLower(err.Error()), tc.wantField) {
				t.Errorf("%s: error %q does not mention %q", tc.name, err.Error(), tc.wantField)
			}

			// Invariant: must never be a decline or request canonical fallback
			var invErr *largebody.StampInvariantError
			if !errors.As(err, &invErr) {
				t.Fatalf("%s: error must be *largebody.StampInvariantError, got %T", tc.name, err)
			}
			if !invErr.IsInvariantFailure() {
				t.Fatalf("%s: IsInvariantFailure() must report true", tc.name)
			}
		})
	}
}

// TestTask11_8_ExecuteRevalidation_InvalidAcceptedAssessment verifies that
// direct execution validation fails fail-closed with a typed invariant error for
// an unaccepted, declined, or zero-stamp assessment.
func TestTask11_8_ExecuteRevalidation_InvalidAcceptedAssessment(t *testing.T) {
	t.Parallel()
	live := largebody.LiveExecutionFacts{
		GenerationID:              "gen-1",
		ProfileID:                 "openai-chat",
		Source:                    largebody.NewSourceDigest(sha256Digest(0x01)),
		BodyBytes:                 100,
		Mode:                      largebody.BodyModeIdentityJSON,
		Rewrite:                   largebody.NewNoRewrite(),
		CandidateDomainGeneration: "domain-gen-1",
	}

	assertInvariantFailure := func(t *testing.T, err error, wantField string) {
		t.Helper()
		if err == nil {
			t.Fatalf("expected invariant failure on %s, got nil", wantField)
		}
		if !errors.Is(err, largebody.ErrStampDisagreement) {
			t.Fatalf("expected ErrStampDisagreement for %s, got %v", wantField, err)
		}
		var invErr *largebody.StampInvariantError
		if !errors.As(err, &invErr) {
			t.Fatalf("expected *largebody.StampInvariantError for %s, got %T", wantField, err)
		}
		if invErr.Field != wantField {
			t.Fatalf("expected disagreement field %q, got %q (%v)", wantField, invErr.Field, err)
		}
		if !invErr.IsInvariantFailure() {
			t.Fatalf("IsInvariantFailure() must report true for %s", wantField)
		}
	}

	// 1. Zero assessment
	assertInvariantFailure(t, largebody.ValidateExecuteLargeBody(largebody.Assessment{}, nil, live), "decision")

	// 2. Declined assessment
	declined, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonAuthorityBlocker)
	assertInvariantFailure(t, largebody.ValidateExecuteLargeBody(declined, nil, live), "decision")

	// 3. Zero stamp in accepted assessment
	emptyStampAcc := largebody.Assessment{
		Decision: largebody.AssessmentDecisionAccept,
	}
	assertInvariantFailure(t, largebody.ValidateExecuteLargeBody(emptyStampAcc, nil, live), "stamp")
}

func TestDirectValidation_ExecuteTamperRejected(t *testing.T) {
	t.Parallel()
	proof, _ := makeValidProof("gen-1")
	proof.RequiredCapabilities = []aipapi.Capability{aipapi.CapabilityStreaming}

	// Attach initial compaction facts
	h1 := sha256.Sum256([]byte("item-1"))
	h2 := sha256.Sum256([]byte("item-2"))
	proof.CompactionFacts = compactionfacts.RequestFacts{
		Operation:   aipapi.OperationOpenAIChatCompletions,
		ItemCount:   2,
		ItemHashes:  [][32]byte{h1, h2},
		StartRuleID: "codex.local_checkpoint.v1",
	}
	proof.CompactionComplete = true

	stamp, err := largebody.BindAssessmentStamp("gen-1", proof, "domain-gen-1")
	if err != nil {
		t.Fatalf("BindAssessmentStamp error: %v", err)
	}

	wireReq := largebody.WireRequestFacts{
		ProfileID:            "openai-chat",
		Operation:            aipapi.OperationOpenAIChatCompletions,
		Delivery:             aipapi.DeliveryModeStreaming,
		BodyMode:             proof.Mode,
		Rewrite:              proof.Rewrite,
		ClientModel:          "gpt-4o",
		CandidateModel:       "gpt-4o",
		MaxOutputTokens:      4096,
		RequiredCapabilities: []aipapi.Capability{aipapi.CapabilityStreaming},
	}

	wireDomain := largebody.WireDomainFacts{
		ProfileID:            "openai-chat",
		Operation:            aipapi.OperationOpenAIChatCompletions,
		Delivery:             aipapi.DeliveryModeStreaming,
		BodyMode:             proof.Mode,
		Rewrite:              proof.Rewrite,
		CandidateModels:      []string{"gpt-4o"},
		RequiredCapabilities: []aipapi.Capability{aipapi.CapabilityStreaming},
	}

	accepted, err := largebody.NewAcceptedAssessment(stamp, wireReq, wireDomain)
	if err != nil {
		t.Fatalf("NewAcceptedAssessment error: %v", err)
	}
	accepted.CompactionFacts = proof.CompactionFacts.Clone()
	accepted.CompactionComplete = proof.CompactionComplete

	liveFor := func(s largebody.AssessmentStamp, bodyBytes int64) largebody.LiveExecutionFacts {
		return largebody.LiveExecutionFacts{
			GenerationID:              s.GenerationID(),
			ProfileID:                 s.ProfileID(),
			Source:                    s.SourceDigest(),
			BodyBytes:                 bodyBytes,
			Mode:                      s.BodyMode(),
			Rewrite:                   s.Rewrite(),
			CandidateDomainGeneration: s.CandidateDomainGeneration(),
		}
	}

	assertInvariantFailure := func(t *testing.T, err error, wantField string) {
		t.Helper()
		if err == nil {
			t.Fatalf("expected invariant failure on %s, got nil", wantField)
		}
		if !errors.Is(err, largebody.ErrStampDisagreement) {
			t.Fatalf("expected ErrStampDisagreement for %s tamper, got %v", wantField, err)
		}
		var invErr *largebody.StampInvariantError
		if !errors.As(err, &invErr) {
			t.Fatalf("expected *largebody.StampInvariantError for %s tamper, got %T", wantField, err)
		}
		if invErr.Field != wantField {
			t.Fatalf("expected disagreement field %q, got %q (%v)", wantField, invErr.Field, err)
		}
		if !invErr.IsInvariantFailure() {
			t.Fatalf("IsInvariantFailure() must report true for %s tamper", wantField)
		}
	}

	// Valid control: all unrelated facts match, so direct validation accepts.
	live := liveFor(stamp, stamp.BodyBytes())
	if err := largebody.ValidateExecuteLargeBody(accepted, nil, live); err != nil {
		t.Fatalf("valid control rejected: %v", err)
	}

	t.Run("request required capabilities tamper", func(t *testing.T) {
		t.Parallel()
		tampered := accepted
		tampered.WireRequest.RequiredCapabilities = []aipapi.Capability{aipapi.CapabilityStreaming, aipapi.CapabilityTools}
		err := largebody.ValidateExecuteLargeBody(tampered, nil, live)
		assertInvariantFailure(t, err, "required capabilities")
	})

	t.Run("wire domain required capabilities tamper", func(t *testing.T) {
		t.Parallel()
		tampered := accepted
		tampered.WireDomain.RequiredCapabilities = []aipapi.Capability{aipapi.CapabilityStreaming, aipapi.CapabilityTools}
		err := largebody.ValidateExecuteLargeBody(tampered, nil, live)
		assertInvariantFailure(t, err, "wire domain required capabilities")
	})

	t.Run("replacement assessment capability tamper", func(t *testing.T) {
		t.Parallel()
		replacementStamp, err := largebody.BindAssessmentStamp("gen-1", proof, "domain-gen-2")
		if err != nil {
			t.Fatalf("BindAssessmentStamp: %v", err)
		}
		replacement, err := largebody.NewAcceptedAssessment(replacementStamp, wireReq, wireDomain)
		if err != nil {
			t.Fatalf("NewAcceptedAssessment: %v", err)
		}
		replacement.CompactionFacts = proof.CompactionFacts.Clone()
		replacement.CompactionComplete = proof.CompactionComplete
		replacementLive := liveFor(replacementStamp, replacementStamp.BodyBytes())
		if err := largebody.ValidateExecuteLargeBody(replacement, nil, replacementLive); err != nil {
			t.Fatalf("replacement control rejected: %v", err)
		}
		tamperedReq := replacement
		tamperedReq.WireRequest.RequiredCapabilities = []aipapi.Capability{aipapi.CapabilityStreaming, aipapi.CapabilityDocuments}
		assertInvariantFailure(t, largebody.ValidateExecuteLargeBody(tamperedReq, nil, replacementLive), "required capabilities")
		tamperedDomain := replacement
		tamperedDomain.WireDomain.RequiredCapabilities = []aipapi.Capability{aipapi.CapabilityStreaming, aipapi.CapabilityDocuments}
		assertInvariantFailure(t, largebody.ValidateExecuteLargeBody(tamperedDomain, nil, replacementLive), "wire domain required capabilities")
	})

	t.Run("compaction facts tamper", func(t *testing.T) {
		t.Parallel()
		tampered := accepted
		tampered.CompactionFacts = accepted.CompactionFacts.Clone()
		tampered.CompactionFacts.ItemHashes[0][0] ^= 0xFF
		err := largebody.ValidateExecuteLargeBody(tampered, nil, live)
		assertInvariantFailure(t, err, "compaction facts")
	})

	t.Run("stamp swap", func(t *testing.T) {
		t.Parallel()
		swapped := accepted
		swappedStamp, err := largebody.NewAssessmentStamp(
			"gen-1",
			"openai-chat",
			largebody.NewSourceDigest(sha256Digest(0xee)),
			proof.BodyBytes+1,
			proof.Mode,
			proof.Rewrite,
			largebody.NewIdentityDigest(sha256Digest(0xff)),
			"domain-gen-1",
		)
		if err != nil {
			t.Fatalf("NewAssessmentStamp: %v", err)
		}
		swapped.Stamp = swappedStamp
		swappedLive := liveFor(swappedStamp, proof.BodyBytes)
		err = largebody.ValidateExecuteLargeBody(swapped, stubSource{size: proof.BodyBytes}, swappedLive)
		assertInvariantFailure(t, err, "body size")
	})
}
