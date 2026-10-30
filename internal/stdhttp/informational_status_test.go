package stdhttp

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"strconv"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

// A 102 is not the final status, for either access logging or panic recovery.
// Mutant: record 102 as committed -> wrong access status / empty final 200.
func TestInformationalStatusPreservesFinalStatusAndRecovery(t *testing.T) {
	t.Parallel()
	for _, status := range []int{200, 400, 500} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Parallel()
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			cfg := &config.Config{}
			cfg.Logging.AccessLog = true
			h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusProcessing)
				if status == 500 {
					panic("private panic detail")
				}
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "final")
			})
			finished := make(chan struct{})
			stack := accessLogMiddleware(cfg, logger, RecoveryMiddleware(logger, h))
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(finished)
				stack.ServeHTTP(w, r)
			}))
			defer srv.Close()
			informational := 0
			req, err := http.NewRequestWithContext(httptrace.WithClientTrace(t.Context(), &httptrace.ClientTrace{
				Got1xxResponse: func(code int, _ textproto.MIMEHeader) error {
					if code == 102 {
						informational++
					}
					return nil
				},
			}), http.MethodGet, srv.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			<-finished
			if informational != 1 || resp.StatusCode != status {
				t.Fatalf("1xx=%d final=%d want 102 -> %d", informational, resp.StatusCode, status)
			}
			if !strings.Contains(logs.String(), `"status":`+strconv.Itoa(status)) {
				t.Fatalf("access log has wrong final status: %s", logs.String())
			}
			if status == 500 && string(body) != "internal error\n" {
				t.Fatalf("unsafe or missing panic response: %q", body)
			}
		})
	}
}
