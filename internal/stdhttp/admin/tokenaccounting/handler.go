package tokenaccounting

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	"github.com/aiproxer/aiproxer/internal/jsonbody"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const defaultMaxBodyBytes int64 = 1 << 20

// Service is the narrow application seam used by the admin HTTP adapter.
type Service interface {
	Count(context.Context, CountRequest) (CountResponse, error)
}

// CountCallService is the concrete app CountCall seam (*tokenaccounting/app.Service).
type CountCallService interface {
	CountCall(context.Context, app.CountCallInput) (app.CountResult, error)
}

type Options struct {
	Enabled      bool
	MaxBodyBytes int64
	Service      Service
}

type CountRequest struct {
	Backend string      `json:"backend"`
	Model   string      `json:"model"`
	Mode    app.Mode    `json:"mode,omitempty"`
	Call    aipapi.Call `json:"call"`
}

type CountResponse struct {
	Planes []PlaneCount `json:"planes"`
}

type PlaneCount struct {
	Plane             aipapi.UsagePlane     `json:"plane"`
	Tokens            TokenDimensions       `json:"tokens"`
	Source            aipapi.UsageSource    `json:"source"`
	Authority         aipapi.UsageAuthority `json:"authority"`
	Tokenizer         aipapi.TokenizerRef   `json:"tokenizer"`
	UnavailableReason string                `json:"unavailable_reason,omitempty"`
	FallbackReason    app.FallbackReason    `json:"fallback_reason,omitempty"`
}

type TokenDimensions struct {
	Input      int `json:"input,omitempty"`
	Output     int `json:"output,omitempty"`
	CacheRead  int `json:"cache_read,omitempty"`
	CacheWrite int `json:"cache_write,omitempty"`
	Reasoning  int `json:"reasoning,omitempty"`
	Total      int `json:"total,omitempty"`
}

type appServiceAdapter struct {
	svc CountCallService
}

// AdaptCountCallService projects CountCall onto [Service]. Typed-nil *app.Service → nil.
func AdaptCountCallService(svc CountCallService) Service {
	if countCallServiceIsNil(svc) {
		return nil
	}
	return appServiceAdapter{svc: svc}
}

func countCallServiceIsNil(svc CountCallService) bool {
	if svc == nil {
		return true
	}
	if concrete, ok := svc.(*app.Service); ok && concrete == nil {
		return true
	}
	return false
}

func (a appServiceAdapter) Count(ctx context.Context, req CountRequest) (CountResponse, error) {
	result, err := a.svc.CountCall(ctx, app.CountCallInput{
		Backend: req.Backend,
		Model:   req.Model,
		CallID:  req.Call.ID,
		Call:    req.Call,
	})
	if err != nil {
		return CountResponse{}, err
	}
	plane := result.Accounting.Plane
	if plane == aipapi.UsagePlaneUnknown {
		plane = aipapi.UsagePlaneClientVisible
	}
	return CountResponse{Planes: []PlaneCount{{
		Plane: plane,
		Tokens: TokenDimensions{
			Input:      result.InputTokens,
			Output:     result.OutputTokens,
			CacheRead:  result.CacheReadTokens,
			CacheWrite: result.CacheWriteTokens,
			Reasoning:  result.ReasoningTokens,
			Total:      result.TotalTokens,
		},
		Source:    result.Accounting.Source,
		Authority: result.Accounting.Authority,
		Tokenizer: result.Accounting.Tokenizer,
	}}}, nil
}

func NewHandler(opts Options) http.Handler {
	service := opts.Service
	maxBodyBytes := opts.MaxBodyBytes
	if maxBodyBytes == 0 {
		maxBodyBytes = defaultMaxBodyBytes
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !opts.Enabled {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if service == nil {
			writeError(w, http.StatusServiceUnavailable, "count_unavailable")
			return
		}

		var req CountRequest
		if err := jsonbody.Decode(w, r, &req, jsonbody.Policy{
			MaxBytes: maxBodyBytes,
		}); err != nil {
			switch {
			case errors.Is(err, context.Canceled):
				writeError(w, http.StatusRequestTimeout, "request_canceled")
			case errors.Is(err, jsonbody.ErrTooLarge):
				writeError(w, http.StatusRequestEntityTooLarge, "request_too_large")
			default:
				writeError(w, http.StatusBadRequest, "invalid_json")
			}
			return
		}

		resp, err := service.Count(r.Context(), req)
		if err != nil {
			writeError(w, statusForError(err), classifyError(err))
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})
}

func statusForError(err error) int {
	switch {
	case errors.Is(err, app.ErrCountingDisabled):
		return http.StatusNotFound
	case errors.Is(err, context.Canceled):
		return http.StatusRequestTimeout
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout
	default:
		return http.StatusServiceUnavailable
	}
}

func classifyError(err error) string {
	switch {
	case errors.Is(err, app.ErrCountingDisabled):
		return "count_disabled"
	case errors.Is(err, context.Canceled):
		return "request_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "count_timeout"
	case errors.Is(err, app.ErrProviderUnsupported):
		return "provider_unsupported"
	case errors.Is(err, app.ErrProviderUnavailable), errors.Is(err, app.ErrLocalUnavailable):
		return "count_unavailable"
	default:
		return "count_unavailable"
	}
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
