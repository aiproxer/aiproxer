package diag_test

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func FuzzStableCallIdentity(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"ID":"x","Messages":[{"Role":"user","Parts":[{"Kind":"text","Text":"y"}]}]}`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		raw = testkit.CapBytes(raw, 512<<10)
		var c aipapi.Call
		if err := json.Unmarshal(raw, &c); err != nil {
			c = aipapi.Call{}
		}
		_ = diag.StableCallToken(&c)
		_ = diag.StableCallID(&c)
		_ = diag.StableUnix(&c)
	})
}
