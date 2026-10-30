package gemini

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestRouteDescriptorsPreserveNestedGeminiMountPatterns(t *testing.T) {
	t.Parallel()
	descriptors, err := RouteDescriptors(ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(descriptors) != 2 || descriptors[0].MountPattern != "/v1beta/" || descriptors[1].MountPattern != "/v1beta1/" {
		t.Fatalf("unexpected descriptors: %+v", descriptors)
	}
	for _, descriptor := range descriptors {
		if descriptor.Claim.Path != descriptor.MountPattern[:len(descriptor.MountPattern)-1] {
			t.Fatalf("claim path=%q mount=%q", descriptor.Claim.Path, descriptor.MountPattern)
		}
	}

	mux := http.NewServeMux()
	if err := Mount(mux, aipsdk.FrontendMountOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1beta/models/gemini:generateContent", "/v1beta1/models/gemini:generateContent"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound {
			t.Fatalf("nested Gemini path %q was not mounted", path)
		}
	}
}
