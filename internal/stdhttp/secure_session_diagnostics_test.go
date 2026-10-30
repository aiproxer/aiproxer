package stdhttp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	ssessiondiag "github.com/aiproxer/aiproxer/internal/core/securesession/adapters/diag"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

func TestSecureSessionDiagnostics_mount_matchesComposePattern(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memory.New(memory.Options{})
	fp := domain.TokenFingerprint{}
	fp[2] = 2
	_, err := st.Create(ctx, domain.CreateRecord{
		SessionID: "mount-sess", ResumeFingerprint: fp,
		Owner: domain.PrincipalRef{ID: "op-user"}, Policy: domain.PolicyMetadata{},
		ALegID: "a-mount", ResumeEligible: true, CreatedAt: time.Unix(1, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	const base = "/debug/sessions"
	const secret = "s3cr3t-12chars-minimum-secret-value-here-ok"
	ssh, err := ssessiondiag.NewHandler(base, st, "standard", nil, slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err != nil {
		t.Fatal(err)
	}
	dh := diag.WrapDiagnosticsProtect(secret, ssh)
	mux := http.NewServeMux()
	mux.Handle("GET "+base+"/", dh)
	mux.Handle("GET "+base, dh)

	req := httptest.NewRequest(http.MethodGet, base+"/mount-sess", nil)
	req.Header.Set(diag.HeaderDiagnosticsSecret, secret)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", rr.Code, rr.Body.String())
	}
	var env map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	sess, ok := env["session"].(map[string]any)
	if !ok || sess["session_id"] != "mount-sess" {
		t.Fatalf("session %#v", env)
	}
	if strings.Contains(rr.Body.String(), "resume_fingerprint") {
		t.Fatal("response must not expose fingerprint fields")
	}
}
