package metering

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipsdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// TestPhase19R4ChunkInvariantWholeVsAdversarialPartitions certifies Requirement
// 4.5 at the owning boundary accumulator: the same canonical provider output
// measured whole and across multiple adversarial chunk partitions yields equal
// normalized observations, equal fingerprints and equal quantities, with no
// double counting and no exact-tokenizer claim.
//
// The accumulator deliberately measures byte length plus a method-labelled
// byte/4 estimate (local.boundary.token_estimate.v1, QualityEstimated); it must
// never equate a sum of independently tokenized chunks with exact whole-output
// tokenization. Partitions therefore include splits inside multi-byte UTF-8
// sequences (emoji/CJK), byte-by-byte delivery, rune-by-rune delivery, and a
// word-join split that would change any true subword tokenization.
func TestPhase19R4ChunkInvariantWholeVsAdversarialPartitions(t *testing.T) {
	t.Parallel()

	const full = "hello h\xc3\xa9llo \U0001F30D\u4e16\u754c output with tokenizer-sensitive joins: unbelievable antidisestablishmentarianism!"
	fullBytes := int64(len(full))
	wantTokens := (fullBytes + 3) / 4

	byteByByte := make([]string, 0, len(full))
	for i := range len(full) {
		byteByByte = append(byteByByte, full[i:i+1])
	}
	runeByRune := make([]string, 0)
	for _, r := range full {
		runeByRune = append(runeByRune, string(r))
	}
	// Split inside the 4-byte emoji and inside a 3-byte CJK rune: byte offsets
	// that no UTF-8-aware chunker would choose.
	midEmoji := strings.Index(full, "\U0001F30D")
	midCJK := strings.Index(full, "世界")
	adversarial := []string{
		full[:midEmoji+1],
		full[midEmoji+1 : midCJK+2],
		full[midCJK+2:],
	}
	partitions := map[string][]string{
		"whole":        {full},
		"two-chunk":    {full[:len(full)/2], full[len(full)/2:]},
		"word-split":   {"hello ", "h\xc3\xa9llo \U0001F30D", "\u4e16\u754c output with tokenizer-sensitive joins: ", "unbelievable antidisestablishmentarianism!"},
		"byte-by-byte": byteByByte,
		"rune-by-rune": runeByRune,
		"utf8-split":   adversarial,
	}

	providerMedia := MediaSummary{Kind: MediaVideo, Count: 1, DurationMillis: 1500, DurationPresent: true, Frames: 45, FramesPresent: true, Bytes: 900, BytesPresent: true}
	customerMedia := MediaSummary{Kind: MediaImage, Count: 1, Bytes: 120, BytesPresent: true, WidthPixels: 256, WidthPresent: true, HeightPixels: 256, HeightPresent: true}

	type outcome struct {
		snapshot     BoundarySnapshot
		observations []aipsdkmetering.Observation
	}
	results := make(map[string]outcome, len(partitions))
	for name, chunks := range partitions {
		acc := NewBoundaryAccumulator(BoundaryConfig{MaxTextBytes: 64 * 1024, MaxMediaEntries: 8})
		for _, chunk := range chunks {
			acc.ObserveProviderEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: chunk})
		}
		acc.ObserveProviderEvent(aipapi.Event{Kind: aipapi.EventAssistantFileRef, AssistantMIME: "video/mp4"}, providerMedia)
		// Customer plane uses a deliberately different partition of the same
		// text so provider/customer independence is exercised, not assumed.
		for _, chunk := range runeByRune {
			acc.ObserveCustomerEvent(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: chunk})
		}
		acc.ObserveCustomerEvent(aipapi.Event{Kind: aipapi.EventAssistantFileRef, AssistantMIME: "image/jpeg"}, customerMedia)
		snapshot := acc.Snapshot()
		observations := acc.Observations(testBoundaryIdentity())
		if len(observations) != 3 {
			t.Fatalf("%s: observation count = %d, want 3 local planes", name, len(observations))
		}
		for _, observation := range observations {
			if err := observation.Validate(); err != nil {
				t.Fatalf("%s: observation validation: %v", name, err)
			}
		}
		results[name] = outcome{snapshot: snapshot, observations: observations}
	}

	reference := results["whole"]
	if reference.snapshot.ProviderOutput.TextBytes != fullBytes {
		t.Fatalf("whole provider bytes = %d, want %d (double count or loss)", reference.snapshot.ProviderOutput.TextBytes, fullBytes)
	}
	if reference.snapshot.ProviderOutput.TextTokens != wantTokens {
		t.Fatalf("whole provider tokens = %d, want byte/4 estimate %d", reference.snapshot.ProviderOutput.TextTokens, wantTokens)
	}
	if reference.snapshot.CustomerOutput.TextBytes != fullBytes {
		t.Fatalf("whole customer bytes = %d, want %d", reference.snapshot.CustomerOutput.TextBytes, fullBytes)
	}

	for name, got := range results {
		if name == "whole" {
			continue
		}
		if got.snapshot.ProviderOutput.TextBytes != reference.snapshot.ProviderOutput.TextBytes ||
			got.snapshot.ProviderOutput.TextTokens != reference.snapshot.ProviderOutput.TextTokens {
			t.Fatalf("%s provider output = %#v, want %#v (chunk-sensitive measurement)",
				name, got.snapshot.ProviderOutput, reference.snapshot.ProviderOutput)
		}
		if got.snapshot.CustomerOutput.TextBytes != reference.snapshot.CustomerOutput.TextBytes ||
			got.snapshot.CustomerOutput.TextTokens != reference.snapshot.CustomerOutput.TextTokens {
			t.Fatalf("%s customer output = %#v, want %#v (chunk-sensitive measurement)",
				name, got.snapshot.CustomerOutput, reference.snapshot.CustomerOutput)
		}
		// Normalized observation planes 1 (provider) and 2 (customer) must be
		// byte-identical after canonicalization: same measures, same order,
		// same fingerprint.
		for plane := 1; plane <= 2; plane++ {
			wantJSON, err := reference.observations[plane].CanonicalJSON()
			if err != nil {
				t.Fatalf("whole plane %d canonical json: %v", plane, err)
			}
			gotJSON, err := got.observations[plane].CanonicalJSON()
			if err != nil {
				t.Fatalf("%s plane %d canonical json: %v", name, plane, err)
			}
			if !bytes.Equal(wantJSON, gotJSON) {
				t.Fatalf("%s plane %d canonical observation differs from whole-output measurement:\nwhole=%s\n%s=%s",
					name, plane, wantJSON, name, gotJSON)
			}
			if wantFingerprint, gotFingerprint := reference.observations[plane].Fingerprint(), got.observations[plane].Fingerprint(); wantFingerprint == "" || wantFingerprint != gotFingerprint {
				t.Fatalf("%s plane %d fingerprint = %q, whole = %q (must be stable non-empty and equal)",
					name, plane, gotFingerprint, wantFingerprint)
			}
		}
	}

	// The token quantity must remain a labelled estimate in every plane: the
	// suite proves chunk invariance of the byte/4 estimate while pinning that
	// no partition upgrades it to an exact whole-output tokenization.
	for plane, planeName := range map[int]string{1: "provider", 2: "customer"} {
		observation := reference.observations[plane]
		var sawTokenEstimate bool
		for _, measure := range observation.Measures {
			if measure.Key.Component != aipsdkmetering.ComponentTextToken {
				continue
			}
			sawTokenEstimate = true
			if measure.Quality != aipsdkmetering.QualityEstimated {
				t.Fatalf("%s token quality = %q, want estimated (must not claim exact whole-output tokenization)", planeName, measure.Quality)
			}
			if measure.MethodRef != "local.boundary.token_estimate.v1" {
				t.Fatalf("%s token method = %q, want local.boundary.token_estimate.v1", planeName, measure.MethodRef)
			}
			if measure.Reason == "" {
				t.Fatalf("%s token estimate must carry its limitation reason", planeName)
			}
			if measure.Value == nil {
				t.Fatalf("%s token estimate has no value", planeName)
			}
		}
		if !sawTokenEstimate {
			t.Fatalf("%s plane carries no text token estimate", planeName)
		}
	}

	// Multimodal provider/customer separation must survive every partition
	// unchanged: provider video properties are never replaced by the customer
	// image projection.
	for name, got := range results {
		providerMediaGot := got.snapshot.ProviderOutput.Media
		if len(providerMediaGot) != 1 || providerMediaGot[0].Kind != MediaVideo || providerMediaGot[0].Frames != 45 || providerMediaGot[0].DurationMillis != 1500 {
			t.Fatalf("%s provider media = %#v, want independent video plane", name, providerMediaGot)
		}
		customerMediaGot := got.snapshot.CustomerOutput.Media
		if len(customerMediaGot) != 1 || customerMediaGot[0].Kind != MediaImage || customerMediaGot[0].Bytes != 120 {
			t.Fatalf("%s customer media = %#v, want independent image plane", name, customerMediaGot)
		}
	}

	// Re-observation of the same snapshot must be stable (no future accumulation).
	first := reference.observations[1].Fingerprint()
	second := reference.observations[1].Fingerprint()
	if first == "" || first != second {
		t.Fatalf("provider fingerprint unstable: %q then %q", first, second)
	}
}
