package refsubmit

import (
	"context"
	"encoding/json"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

// ID is the feature plugin id for YAML registration.
const ID = "ref-submit-annotate"

const defaultOrder = 50

type hook struct {
	order  int
	marker string
}

var _ sdk.SubmitHook = hook{}

// NewSubmitHook returns a submit hook that records a JSON marker extension on the canonical call.
func NewSubmitHook(cfg Config) sdk.SubmitHook {
	o := defaultOrder
	if cfg.Order != nil {
		o = *cfg.Order
	}
	m := cfg.Marker
	if m == "" {
		m = "ref_submit"
	}
	return hook{order: o, marker: m}
}

func (hook) ID() string                   { return ID }
func (h hook) Order() int                 { return h.order }
func (hook) FailureMode() sdk.FailureMode { return sdk.FailOpen }

func (h hook) Handle(_ context.Context, call *aipapi.Call, _ *sdk.SubmitMeta) (sdk.SubmitDecision, error) {
	if call == nil {
		return sdk.SubmitDecision{}, nil
	}
	if call.Extensions == nil {
		call.Extensions = map[string]json.RawMessage{}
	}
	raw, err := json.Marshal(h.marker)
	if err != nil {
		return sdk.SubmitDecision{}, err
	}
	call.Extensions["x_aip_ref_submit"] = raw
	return sdk.SubmitDecision{}, nil
}
