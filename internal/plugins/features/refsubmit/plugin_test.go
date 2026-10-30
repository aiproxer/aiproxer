package refsubmit

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

func TestSubmitHook_setsExtension(t *testing.T) {
	t.Parallel()
	h := NewSubmitHook(Config{Marker: "probe"})
	call := &aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}}}}}
	_, err := h.Handle(context.Background(), call, &sdk.SubmitMeta{TraceID: "t"})
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := call.Extensions["x_aip_ref_submit"]
	if !ok {
		t.Fatal("missing extension")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil || s != "probe" {
		t.Fatalf("value %q err %v", string(raw), err)
	}
}
