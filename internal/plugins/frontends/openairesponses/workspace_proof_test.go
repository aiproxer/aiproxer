package openairesponses_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestOpenAIResponsesProfile_CompileProof_WorkspaceHint(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-4o","input":"hello world"}`)
	prof := openairesponses.NewProfile()
	plain, err := prof.CompileProof(context.Background(), defaultProofInput(body, "", nil))
	if err != nil {
		t.Fatalf("plain CompileProof: %v", err)
	}
	hdr := http.Header{}
	hdr.Set(aipsdk.HeaderWorkspace, "/tmp/aip-workspace-hint-proj-a")
	withHint, err := prof.CompileProof(context.Background(), defaultProofInput(body, "", hdr))
	if err != nil {
		t.Fatalf("hinted CompileProof: %v", err)
	}
	if withHint.State.Proof.Identity == plain.State.Proof.Identity {
		t.Fatal("bounded path must project the workspace hint into identity extensions")
	}
	bad := http.Header{}
	bad.Set(aipsdk.HeaderWorkspace, "relative/path")
	if _, err := prof.CompileProof(context.Background(), defaultProofInput(body, "", bad)); err == nil {
		t.Fatal("invalid workspace hint must decline bounded proof to canonical decode")
	}
}
