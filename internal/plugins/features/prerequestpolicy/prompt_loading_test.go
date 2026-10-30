package prerequestpolicy_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/prerequestpolicy"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
)

func TestNewHandlers_rejectsPromptTraversalWithoutDecodeConfig(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, "prompts")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "outside.md"), []byte("outside prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := prerequestpolicy.NewHandlers(promptFileConfig(dir, "../outside.md"))
	if err == nil || !strings.Contains(err.Error(), "path traversal") {
		t.Fatalf("expected traversal rejection for directly constructed config, got %v", err)
	}
}

func TestNewHandlers_loadsPromptWithoutDecodeConfig(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"policy.md", "..policy.md", filepath.Join("..archive", "policy.md")} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name), []byte("  Return ALLOW or DENY.\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			assertPromptRequest(t, dir, name)
		})
	}
}

func TestNewHandlers_loadsTrustedSymlinkTargetWithoutDecodeConfig(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, "prompts")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "outside.md")
	if err := os.WriteFile(target, []byte("  Return ALLOW or DENY.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "policy.md")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink creation unavailable without Windows privilege: %v", err)
		}
		t.Fatal(err)
	}
	assertPromptRequest(t, dir, "policy.md")
}

func assertPromptRequest(t *testing.T, dir, name string) {
	t.Helper()
	handlers, err := prerequestpolicy.NewHandlers(promptFileConfig(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if len(handlers) != 1 {
		t.Fatalf("handlers: %d", len(handlers))
	}
	aux := &collectText{text: "ALLOW"}
	decision, err := handlers[0].Handle(context.Background(), validCall(), prerequest.Meta{}, prerequest.Services{Aux: aux})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Deny || len(aux.reqs) != 1 {
		t.Fatalf("decision=%+v auxiliary calls=%d", decision, len(aux.reqs))
	}
	if got := aux.reqs[0].Call.Instructions[0].Parts[0].Text; got != "Return ALLOW or DENY." {
		t.Fatalf("loaded prompt = %q", got)
	}
}

func promptFileConfig(dir, name string) prerequestpolicy.Config {
	return prerequestpolicy.Config{
		PromptDir: dir,
		Handlers: []prerequestpolicy.HandlerConfig{{
			PromptFilename:     name,
			ModelRoutingString: "local:policy",
			DenyPattern:        "DENY",
		}},
	}
}
