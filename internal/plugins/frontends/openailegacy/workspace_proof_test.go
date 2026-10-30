package openailegacy_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// The bounded proof path must project the validated workspace hint into the
// identity extensions (same canonical workspace_path the ordinary decode
// path sets) and must decline invalid hints to canonical decode, which
// returns the existing protocol 400.
func TestOpenAIChatProfile_CompileProof_WorkspaceHint(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello world"}]}`)
	prof := openailegacy.NewProfile()
	plain, err := prof.CompileProof(context.Background(), defaultChatProofInput(body, "", nil))
	if err != nil {
		t.Fatalf("plain CompileProof: %v", err)
	}
	hdr := http.Header{}
	hdr.Set(aipsdk.HeaderWorkspace, "/tmp/aip-workspace-hint-proj-a")
	withHint, err := prof.CompileProof(context.Background(), defaultChatProofInput(body, "", hdr))
	if err != nil {
		t.Fatalf("hinted CompileProof: %v", err)
	}
	if withHint.State.Proof.Identity == plain.State.Proof.Identity {
		t.Fatal("bounded path must project the workspace hint into identity extensions")
	}
	bad := http.Header{}
	bad.Set(aipsdk.HeaderWorkspace, "relative/path")
	if _, err := prof.CompileProof(context.Background(), defaultChatProofInput(body, "", bad)); err == nil {
		t.Fatal("invalid workspace hint must decline bounded proof to canonical decode")
	}
}
