package aipapi_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestPartFileRef_requiresFileRefThroughCall(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{{
				Kind: aipapi.PartFileRef,
			}},
		}},
	}
	if err := call.Validate(); err == nil {
		t.Fatal("expected error for empty FileRef")
	}
}

func TestPartFileRef_validThroughCall(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{
				aipapi.FilePart("file-abc123", "application/pdf", "report.pdf"),
			},
		}},
	}
	if err := call.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPartFileRef_emptyFileRefRejected(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{{
				Kind:     aipapi.PartFileRef,
				FileMIME: "application/pdf",
			}},
		}},
	}
	if err := call.Validate(); err == nil {
		t.Fatal("expected error for empty FileRef")
	}
}

func TestPartFileRef_mimeAndNameOptional(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleUser,
			Parts: []aipapi.Part{{
				Kind:    aipapi.PartFileRef,
				FileRef: "file-xyz",
			}},
		}},
	}
	if err := call.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
