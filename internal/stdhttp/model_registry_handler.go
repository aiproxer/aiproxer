package stdhttp

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/core/modelview"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
)

const openAIModelsPath = "/v1/models"

// ModelRegistryHandler serves OpenAI-compatible GET /v1/models.
// When the request carries a bound registry view (generation-dispatched), that
// immutable view is used. Otherwise the live runtime is read for compatibility
// with legacy/direct constructors.
type ModelRegistryHandler struct {
	rt *modelregistry.Runtime
}

var _ http.Handler = (*ModelRegistryHandler)(nil)

// NewModelRegistryHandler returns a concrete handler for GET /v1/models.
func NewModelRegistryHandler(rt *modelregistry.Runtime) *ModelRegistryHandler {
	return &ModelRegistryHandler{rt: rt}
}

func (h *ModelRegistryHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, modelGen, ok := h.modelsBody(r)
	if !ok || body == nil {
		body = []byte(`{"object":"list","data":[]}`)
		modelGen = ""
	}
	etag := modelsETagFromRequest(r, modelGen)
	if etag != "" {
		w.Header().Set("ETag", etag)
		if match := strings.TrimSpace(r.Header.Get("If-None-Match")); match != "" && etagMatches(match, etag) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (h *ModelRegistryHandler) modelsBody(r *http.Request) (body []byte, generation string, ok bool) {
	if r != nil {
		if bv, has := modelregistry.BoundViewFromContext(r.Context()); has {
			return bv.ModelsJSON()
		}
	}
	if h != nil && h.rt != nil {
		return h.rt.ModelsJSON()
	}
	return nil, "", false
}

// modelsETagFromRequest prefers the aggregate model-view digest when bound;
// otherwise falls back to legacy config+model generation quoting.
func modelsETagFromRequest(r *http.Request, modelGeneration string) string {
	if r != nil {
		if id, ok := modelview.FromContext(r.Context()); ok {
			if etag := id.QuotedETag(); etag != "" {
				return etag
			}
		}
	}
	configGen := int64(0)
	if r != nil {
		if b, has := runtimehost.BindingFromContext(r.Context()); has {
			configGen = b.Meta().ID
		}
	}
	return modelsETag(configGen, modelGeneration)
}

// modelsETag builds a generation-aware ETag. When a config generation is bound,
// identity includes both config and model generations (req 9.6). Without a
// config binding, the legacy model-generation-only ETag is preserved.
// Raw generation strings are quoted safely via quoteETag (no unquoted interpolation).
func modelsETag(configGeneration int64, modelGeneration string) string {
	modelGeneration = strings.TrimSpace(modelGeneration)
	if configGeneration > 0 && modelGeneration != "" {
		return quoteETag(fmt.Sprintf("cfg-%d/model-%s", configGeneration, modelGeneration))
	}
	if configGeneration > 0 {
		return quoteETag("cfg-" + strconv.FormatInt(configGeneration, 10))
	}
	return quoteETag(modelGeneration)
}

func quoteETag(generation string) string {
	generation = strings.TrimSpace(generation)
	if generation == "" {
		return ""
	}
	return `"` + generation + `"`
}

func etagMatches(ifNoneMatch, etag string) bool {
	if ifNoneMatch == "*" {
		return true
	}
	for part := range strings.SplitSeq(ifNoneMatch, ",") {
		part = strings.TrimSpace(part)
		if after, ok := strings.CutPrefix(part, "W/"); ok {
			part = strings.TrimSpace(after)
		}
		if part == etag {
			return true
		}
	}
	return false
}
