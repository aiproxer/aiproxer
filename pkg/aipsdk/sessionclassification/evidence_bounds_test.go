package sessionclassification_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
)

// TestEvidenceMetadataBytesCountsOnlyBoundedMetadata proves the evidence byte
// cost is a fixed shape: the already-accepted client identity length plus a
// constant-width category bitset, independent of tool count, body size or
// prompt material. The operation string is a copy of an operation the carrier
// already holds, so it is never charged twice.
func TestEvidenceMetadataBytesCountsOnlyBoundedMetadata(t *testing.T) {
	t.Parallel()

	empty := sessionclassification.Evidence{}
	if !empty.IsZero() {
		t.Fatal("zero evidence must report IsZero")
	}

	const fixed = int64(sessionclassification.ToolCategorySetBytes)
	if got := empty.MetadataBytes(); got != fixed {
		t.Fatalf("empty evidence MetadataBytes = %d, want %d (fixed carrier width)", got, fixed)
	}

	const userAgent = "codex_cli_rs/1.2.3"
	withIdentity := sessionclassification.Evidence{
		Operation:       aipapi.OperationOpenAIResponses,
		ClientUserAgent: userAgent,
	}
	if withIdentity.IsZero() {
		t.Fatal("evidence with an accepted client identity must not report IsZero")
	}
	if got, want := withIdentity.MetadataBytes(), fixed+int64(len(userAgent)); got != want {
		t.Fatalf("MetadataBytes = %d, want %d", got, want)
	}

	full := withIdentity
	full.ToolCategories = sessionclassification.ToolCategorySet(0).
		AddToolName("read_file").
		AddToolName("grep").
		AddToolName("bash").
		AddToolName("edit_file").
		AddToolName("delete_file").
		AddToolName("web_search").
		AddToolName("not-a-canonical-tool")
	if got, want := full.MetadataBytes(), fixed+int64(len(userAgent)); got != want {
		t.Fatalf("a saturated category bitset changed MetadataBytes: got %d, want %d", got, want)
	}
}

// TestEvidenceValidateEnforcesBoundedCarrierContract proves evidence validates as
// bounded carrier metadata: an accepted identity must fit the caller's byte
// budget, category bits must stay inside the defined set, and the SDK must not
// define a second client-identity acceptance policy.
func TestEvidenceValidateEnforcesBoundedCarrierContract(t *testing.T) {
	t.Parallel()

	const budget = 64
	valid := sessionclassification.Evidence{
		Operation:       aipapi.OperationOpenAIResponses,
		ClientUserAgent: "codex_cli_rs/1.2.3",
		ToolCategories: sessionclassification.ToolCategorySet(0).
			AddToolName("read_file").
			AddToolName("edit_file").
			AddToolName("bash"),
	}
	if err := valid.Validate(budget); err != nil {
		t.Fatalf("bounded evidence rejected: %v", err)
	}

	if err := (sessionclassification.Evidence{}).Validate(budget); err != nil {
		t.Fatalf("absent evidence must stay valid so insufficient evidence keeps the session unknown: %v", err)
	}

	if err := (sessionclassification.Evidence{}).Validate(0); err == nil {
		t.Fatal("a non-positive byte budget must be rejected")
	}

	oversized := valid
	oversized.ClientUserAgent = strings.Repeat("a", budget+1)
	if err := oversized.Validate(budget); err == nil {
		t.Fatal("an over-budget client identity must be rejected")
	}

	undefinedBits := valid
	undefinedBits.ToolCategories |= 1 << 15
	if err := undefinedBits.Validate(budget); err == nil {
		t.Fatal("category bits outside the defined set must be rejected")
	}
}
