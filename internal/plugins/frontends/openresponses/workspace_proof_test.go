package openresponses_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestOpenResponsesProfile_CompileProof_WorkspaceHint(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-4o","input":"hello world","store":false}`)
	prof := openresponses.NewProfile()
	plain, err := prof.CompileProof(context.Background(), defaultOpenResponsesProofInput(body, "gpt-4o", nil))
	if err != nil {
		t.Fatalf("plain CompileProof: %v", err)
	}
	hdr := http.Header{}
	hdr.Set(aipsdk.HeaderWorkspace, "/tmp/aip-workspace-hint-proj-a")
	withHint, err := prof.CompileProof(context.Background(), defaultOpenResponsesProofInput(body, "gpt-4o", hdr))
	if err != nil {
		t.Fatalf("hinted CompileProof: %v", err)
	}
	if withHint.State.Proof.Identity == plain.State.Proof.Identity {
		t.Fatal("bounded path must project the workspace hint into identity extensions")
	}
	bad := http.Header{}
	bad.Set(aipsdk.HeaderWorkspace, "relative/path")
	if _, err := prof.CompileProof(context.Background(), defaultOpenResponsesProofInput(body, "gpt-4o", bad)); err == nil {
		t.Fatal("invalid workspace hint must decline bounded proof to canonical decode")
	}
}
