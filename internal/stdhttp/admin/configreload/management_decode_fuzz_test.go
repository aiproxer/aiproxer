package configreload_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	mgmtreload "github.com/aiproxer/aiproxer/internal/stdhttp/admin/configreload"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

// FuzzManagementReloadDecode exercises bounded POST body validation for the
// management reload adapter. Invalid bodies must reject without invoking
// Reload; empty/`{}` bodies may trigger Reload once (req 12.4-12.8, 16.9).
//
// Each fuzz input builds an isolated handler/coordinator so parallel fuzz
// workers never share mutable reload counters.
func FuzzManagementReloadDecode(f *testing.F) {
	f.Add([]byte(""), "application/json")
	f.Add([]byte("{}"), "application/json")
	f.Add([]byte("{"), "application/json")
	f.Add([]byte(`{"path":"/etc/passwd"}`), "application/json")
	f.Add([]byte(`{"yaml":"x: 1"}`), "application/json")
	f.Add([]byte(`{"ok":true}`), "application/json")
	f.Add([]byte("not-json"), "text/plain")
	f.Add(bytes.Repeat([]byte("a"), 128), "application/json")

	f.Fuzz(func(t *testing.T, body []byte, contentType string) {
		if len(body) > 512 {
			body = body[:512]
		}
		contentType = boundUTF8Mgmt(contentType, 64)

		var reloads atomic.Int64
		coord := newFakeCoordinator("/fixed/startup/config.yaml", func(context.Context, sdkreload.Trigger) sdkreload.Result {
			reloads.Add(1)
			return sdkreload.Result{Category: sdkreload.ResultNoop}
		})
		h, err := mgmtreload.NewHandler(mgmtreload.Options{
			Address:      "127.0.0.1:0",
			AuthMode:     mgmtreload.AuthModeBearer,
			BearerToken:  "fuzz-management-token",
			MaxBodyBytes: 256,
		}, coord)
		if err != nil {
			t.Fatal(err)
		}

		req := httptest.NewRequest(http.MethodPost, mgmtreload.ReloadPath, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer fuzz-management-token")
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)

		trimmed := strings.TrimSpace(string(body))
		expectReload := trimmed == "" || (trimmed == "{}" && contentTypeOK(contentType))
		if expectReload {
			if reloads.Load() != 1 {
				t.Fatalf("accepted body should reload once: reloads=%d status=%d body=%q ct=%q", reloads.Load(), rr.Code, trimmed, contentType)
			}
			if rr.Code != http.StatusOK {
				t.Fatalf("accepted body status=%d", rr.Code)
			}
		} else if reloads.Load() != 0 {
			t.Fatalf("invalid body invoked Reload: status=%d body=%q ct=%q", rr.Code, trimmed, contentType)
		}
		if rr.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("must not emit CORS")
		}
	})
}

func contentTypeOK(ct string) bool {
	if ct == "" {
		return true
	}
	return strings.HasPrefix(strings.ToLower(ct), "application/json")
}

func boundUTF8Mgmt(s string, maxRunes int) string {
	if maxRunes <= 0 || s == "" {
		return s
	}
	n := 0
	for i := range s {
		if n == maxRunes {
			return s[:i]
		}
		n++
	}
	return s
}
