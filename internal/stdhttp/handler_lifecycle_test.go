package stdhttp

import (
	"strings"
	"testing"
)

func TestComposeStandardHTTP_projectsWithoutBuiltRehydration(t *testing.T) {
	t.Parallel()
	httpInput := mustReadFile(t, "contract/http_input.go")
	if strings.Contains(httpInput, "func standardHTTPInputFromRequestPlane") {
		t.Fatal("standardHTTPInputFromRequestPlane must remain deleted after Task 3.5")
	}
	rp := mustReadFile(t, "request_plane.go")
	if strings.Contains(rp, "requestPlaneAsBuilt") {
		t.Fatal("requestPlaneAsBuilt must remain deleted")
	}
	if strings.Contains(rp, "func ComposeRequestPlane") {
		t.Fatal("ComposeRequestPlane must remain deleted after Task 3.5")
	}
	if !strings.Contains(rp, "func ComposeStandardHTTP") {
		t.Fatal("ComposeStandardHTTP must remain the canonical composer")
	}
}
