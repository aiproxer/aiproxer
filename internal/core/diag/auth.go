package diag

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// HeaderDiagnosticsSecret is the HTTP header clients must send when diagnostics.shared_secret is set.
const HeaderDiagnosticsSecret = aipsdk.HeaderDiagnosticsSecret

// WrapDiagnosticsProtect returns next unchanged when secret is empty; otherwise it requires an exact
// match of HeaderDiagnosticsSecret (constant-time when lengths match).
func WrapDiagnosticsProtect(secret string, next http.Handler) http.Handler {
	return WrapDiagnosticsProtectHeaders(secret, nil, next)
}

// WrapDiagnosticsProtectHeaders is [WrapDiagnosticsProtect] with extra accept names
// for the shared secret. Empty names use [HeaderDiagnosticsSecret]. First non-empty
// matching value wins.
func WrapDiagnosticsProtectHeaders(secret string, names []string, next http.Handler) http.Handler {
	want := strings.TrimSpace(secret)
	if want == "" || next == nil {
		return next
	}
	if len(names) == 0 {
		names = []string{HeaderDiagnosticsSecret}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := aipsdk.FirstHeader(r.Header, names)
		if !constantTimeEqualString(got, want) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func constantTimeEqualString(a, b string) bool {
	ha := sha256.Sum256([]byte(a))
	hb := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}
